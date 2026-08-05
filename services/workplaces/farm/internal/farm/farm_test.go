package farm

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

func hire(t *testing.T, s *Service, pip uint64) bool {
	t.Helper()
	res, err := s.StartShift(context.Background(),
		connect.NewRequest(&workplacev1.StartShiftRequest{PipId: pip, Tick: 1}))
	if err != nil {
		t.Fatalf("StartShift: %v", err)
	}
	return res.Msg.GetAccepted()
}

func TestCapacityIsEnforced(t *testing.T) {
	s := New(1, 0, 0)
	for i := 1; i <= MaxWorkers; i++ {
		if !hire(t, s, uint64(i)) {
			t.Fatalf("pip %d should have been hired", i)
		}
	}
	if hire(t, s, 9999) {
		t.Fatal("a full farm accepted one more worker")
	}
}

// The failure this reproduces was seen in the cluster: sim-core restarted with
// a fresh world and the farm went on holding every position for pips that no
// longer existed, so nobody could ever be hired again.
func TestAbandonedShiftsExpireAndFreeTheirPositions(t *testing.T) {
	now := time.Now()
	s := New(1, 0, 0)
	s.now = func() time.Time { return now }

	for i := 1; i <= MaxWorkers; i++ {
		hire(t, s, uint64(i))
	}
	if hire(t, s, 9999) {
		t.Fatal("precondition: the farm should be full")
	}

	// Nobody works these shifts; time simply passes.
	now = now.Add(shiftLease + time.Second)

	if !hire(t, s, 9999) {
		t.Fatal("expired shifts did not free their positions")
	}
	if got := s.Workers(); got != 1 {
		t.Fatalf("expected the ghosts to be reaped, got %d workers", got)
	}
}

func TestWorkingRenewsTheLease(t *testing.T) {
	now := time.Now()
	s := New(1, 0, 0)
	s.now = func() time.Time { return now }

	hire(t, s, 7)

	// Keep working across a span far longer than the lease.
	for i := 0; i < 5; i++ {
		now = now.Add(shiftLease - time.Second)
		res, err := s.Work(context.Background(),
			connect.NewRequest(&workplacev1.WorkRequest{PipId: 7, Tick: uint64(i)}))
		if err != nil {
			t.Fatalf("Work: %v", err)
		}
		if res.Msg.GetShiftShouldEnd() {
			t.Fatalf("shift was reaped at iteration %d despite being worked", i)
		}
	}
}

func TestWorkForAnUnknownPipEndsTheShift(t *testing.T) {
	s := New(1, 0, 0)
	res, err := s.Work(context.Background(),
		connect.NewRequest(&workplacev1.WorkRequest{PipId: 404, Tick: 1}))
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !res.Msg.GetShiftShouldEnd() {
		t.Fatal("a pip the farm never hired should be told to stop")
	}
}

// The bug this pins: the driver batches, calling Work once a second while the
// world ticks ten times. Flat per-call amounts made employment a slower death
// than idling, because a working pip drains food every tick and was paid once.
func TestWorkPaysForEveryElapsedTick(t *testing.T) {
	s := New(1, 0, 0)
	if !hire(t, s, 3) {
		t.Fatal("precondition: hire failed")
	}

	// Shift started at tick 1; ask for work at tick 11.
	res, err := s.Work(context.Background(),
		connect.NewRequest(&workplacev1.WorkRequest{PipId: 3, Tick: 11}))
	if err != nil {
		t.Fatalf("Work: %v", err)
	}

	const elapsed = 10
	if got := res.Msg.GetNeedDeltas()[needFood]; got != foodPerTick*elapsed {
		t.Fatalf("food for %d ticks = %d, want %d", elapsed, got, foodPerTick*elapsed)
	}
	if got := res.Msg.GetProduced()[0].GetAmount(); got != grainPerTick*elapsed {
		t.Fatalf("grain for %d ticks = %d, want %d", elapsed, got, grainPerTick*elapsed)
	}
}

func TestWorkTwiceInOneTickPaysOnce(t *testing.T) {
	s := New(1, 0, 0)
	hire(t, s, 4)

	first, _ := s.Work(context.Background(),
		connect.NewRequest(&workplacev1.WorkRequest{PipId: 4, Tick: 5}))
	if first.Msg.GetNeedDeltas()[needFood] == 0 {
		t.Fatal("the first call should have paid")
	}

	second, _ := s.Work(context.Background(),
		connect.NewRequest(&workplacev1.WorkRequest{PipId: 4, Tick: 5}))
	if len(second.Msg.GetNeedDeltas()) != 0 {
		t.Fatal("a repeated call in the same tick minted food")
	}
}

// A driver that stalls and resumes must not hand out a windfall.
func TestWorkIsCappedAfterALongGap(t *testing.T) {
	s := New(1, 0, 0)
	hire(t, s, 5)

	res, _ := s.Work(context.Background(),
		connect.NewRequest(&workplacev1.WorkRequest{PipId: 5, Tick: 100_000}))
	if got := res.Msg.GetNeedDeltas()[needFood]; got != foodPerTick*maxTicksPerWork {
		t.Fatalf("food after a long gap = %d, want the cap %d", got, foodPerTick*maxTicksPerWork)
	}
}
