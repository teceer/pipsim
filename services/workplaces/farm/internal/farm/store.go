package farm

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store holds shifts somewhere every replica can see them.
//
// This exists because of a failure measured in the cluster. Allocation already
// distributes correctly — the work queue hands each offer to exactly one
// replica — but everything *after* the hire did not. Shift state lived in each
// replica's memory while Work and EndShift are ordinary RPCs that the Service
// load-balances, so a pip hired by one replica was unknown to the other. At two
// replicas they held 24 and 13 shifts while the gateway believed in 24.
//
// The lease survives the move: an entry carries the wall-clock of its last
// Work call and is reaped once it goes stale, so an abandoned shift still frees
// its position without anyone having to ask who still exists.
type Store interface {
	// Claim atomically reaps expired shifts, checks capacity and takes a
	// position. Returns false with a reason when the pip cannot start.
	Claim(ctx context.Context, pip, tick uint64) (bool, string, error)
	// Touch renews the lease and reports how many ticks the shift is owed for.
	// Returns ok=false when the shift is not held here.
	Touch(ctx context.Context, pip, tick uint64) (elapsed int32, ok bool, err error)
	// Release frees a position, reporting the tick the shift began on so the
	// caller can say how long it lasted. found is false if it held none.
	Release(ctx context.Context, pip, tick uint64) (startedTick uint64, found bool, err error)
	Count(ctx context.Context) (int, error)
}

// --- In memory ---------------------------------------------------------------

// memStore is the single-replica implementation, and the one the tests use.
//
// It is not a mock. Without a Redis URL the farm runs on it and behaves exactly
// as it did before this file existed — one replica, correct; several replicas,
// divergent. That is the honest default: the store is what makes horizontal
// scaling possible, not something that pretends to.
type memStore struct {
	mu     sync.Mutex
	shifts map[uint64]*shift
	max    int
	lease  time.Duration
	maxGap uint64
	now    func() time.Time
}

type shift struct {
	startedTick  uint64
	lastWorkTick uint64
	lastWork     time.Time
}

func newMemStore(max int, lease time.Duration, maxGap uint64, now func() time.Time) *memStore {
	return &memStore{
		shifts: make(map[uint64]*shift),
		max:    max,
		lease:  lease,
		maxGap: maxGap,
		now:    now,
	}
}

// reapLocked drops shifts whose lease has expired. Callers hold mu.
func (m *memStore) reapLocked() {
	for pip, sh := range m.shifts {
		if m.now().Sub(sh.lastWork) > m.lease {
			delete(m.shifts, pip)
			slog.Info("shift lease expired", "pip", pip, "workers", len(m.shifts))
		}
	}
}

func (m *memStore) Claim(_ context.Context, pip, tick uint64) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reap first: a full-looking workplace may be full of expired leases.
	m.reapLocked()

	if _, already := m.shifts[pip]; already {
		return false, "already on shift here", nil
	}
	if len(m.shifts) >= m.max {
		return false, "no free positions", nil
	}

	m.shifts[pip] = &shift{startedTick: tick, lastWorkTick: tick, lastWork: m.now()}
	return true, "", nil
}

func (m *memStore) Touch(_ context.Context, pip, tick uint64) (int32, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sh, working := m.shifts[pip]
	if !working {
		return 0, false, nil
	}

	var elapsed int32
	if tick > sh.lastWorkTick {
		elapsed = int32(min(tick-sh.lastWorkTick, m.maxGap))
	}
	sh.lastWorkTick = tick
	sh.lastWork = m.now()
	return elapsed, true, nil
}

func (m *memStore) Release(_ context.Context, pip, _ uint64) (uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sh, ok := m.shifts[pip]
	if !ok {
		return 0, false, nil
	}
	delete(m.shifts, pip)
	return sh.startedTick, true, nil
}

func (m *memStore) Count(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.shifts), nil
}

// --- Redis ------------------------------------------------------------------

type redisStore struct {
	rdb    *redis.Client
	key    string
	max    int
	lease  time.Duration
	maxGap int64
}

func NewRedisStore(rdb *redis.Client, workplaceID uint64, max int, lease time.Duration, maxGap int64) Store {
	return &redisStore{
		rdb:    rdb,
		key:    "pipsim:workplace:" + strconv.FormatUint(workplaceID, 10) + ":shifts",
		max:    max,
		lease:  lease,
		maxGap: maxGap,
	}
}

// One hash per workplace, field = pip id, value = "started:lastTick:lastMs".
//
// A hash rather than a key per pip: capacity is a property of the whole set, so
// counting has to be one operation rather than a scan, and the reap-check-insert
// has to be atomic. Per-key TTLs would give expiry for free but make counting a
// race.
const claimScript = `
local now   = tonumber(ARGV[3])
local lease = tonumber(ARGV[4])
local max   = tonumber(ARGV[5])

local entries = redis.call('HGETALL', KEYS[1])
local held = 0
for i = 1, #entries, 2 do
  local field, value = entries[i], entries[i+1]
  local lastMs = tonumber(string.match(value, "[^:]+:[^:]+:(.+)"))
  if lastMs == nil or now - lastMs > lease then
    redis.call('HDEL', KEYS[1], field)
  elseif field == ARGV[1] then
    return {0, 'already on shift here'}
  else
    held = held + 1
  end
end

if held >= max then
  return {0, 'no free positions'}
end

redis.call('HSET', KEYS[1], ARGV[1], ARGV[2] .. ':' .. ARGV[2] .. ':' .. ARGV[3])
return {1, ''}
`

// Touch is a script too: read-modify-write on one field would otherwise let two
// concurrent Work calls each be paid for the same elapsed ticks.
const touchScript = `
local value = redis.call('HGET', KEYS[1], ARGV[1])
if not value then
  return {0, 0}
end

local started, lastTick = string.match(value, "([^:]+):([^:]+):")
local tick = tonumber(ARGV[2])
local elapsed = tick - tonumber(lastTick)
if elapsed < 0 then elapsed = 0 end
local cap = tonumber(ARGV[4])
if elapsed > cap then elapsed = cap end

redis.call('HSET', KEYS[1], ARGV[1], started .. ':' .. ARGV[2] .. ':' .. ARGV[3])
return {1, elapsed}
`

func (s *redisStore) Claim(ctx context.Context, pip, tick uint64) (bool, string, error) {
	res, err := s.rdb.Eval(ctx, claimScript, []string{s.key},
		strconv.FormatUint(pip, 10),
		strconv.FormatUint(tick, 10),
		strconv.FormatInt(time.Now().UnixMilli(), 10),
		strconv.FormatInt(s.lease.Milliseconds(), 10),
		strconv.Itoa(s.max),
	).Slice()
	if err != nil {
		return false, "", err
	}

	ok, _ := res[0].(int64)
	reason, _ := res[1].(string)
	return ok == 1, reason, nil
}

func (s *redisStore) Touch(ctx context.Context, pip, tick uint64) (int32, bool, error) {
	res, err := s.rdb.Eval(ctx, touchScript, []string{s.key},
		strconv.FormatUint(pip, 10),
		strconv.FormatUint(tick, 10),
		strconv.FormatInt(time.Now().UnixMilli(), 10),
		strconv.FormatInt(s.maxGap, 10),
	).Slice()
	if err != nil {
		return 0, false, err
	}

	found, _ := res[0].(int64)
	elapsed, _ := res[1].(int64)
	return int32(elapsed), found == 1, nil
}

func (s *redisStore) Release(ctx context.Context, pip, _ uint64) (uint64, bool, error) {
	field := strconv.FormatUint(pip, 10)

	value, err := s.rdb.HGet(ctx, s.key, field).Result()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}

	started, _ := strconv.ParseUint(strings.SplitN(value, ":", 2)[0], 10, 64)
	return started, true, s.rdb.HDel(ctx, s.key, field).Err()
}

func (s *redisStore) Count(ctx context.Context) (int, error) {
	n, err := s.rdb.HLen(ctx, s.key).Result()
	return int(n), err
}
