// Package conformance checks that a running workplace honours
// pips.workplace.v1.WorkplaceService — whatever language it is written in.
//
// The repo's central claim about workplaces is that adding a building type is
// an hour of work in any language and touches neither sim-core nor the gateway.
// Until this file existed, that claim was a sentence in a README with exactly
// one implementation behind it. Point this at an address and it becomes a
// checked property:
//
//	make -C services/workplaces/farm run &
//	WORKPLACE_ADDR=localhost:8090 go test ./services/workplaces/conformance -v
//
//	make -C services/workplaces/tavern run &
//	WORKPLACE_ADDR=localhost:8090 go test ./services/workplaces/conformance -v
//
// It deliberately uses the same Connect client the gateway uses rather than a
// hand-built request. The first run against the Elixir tavern failed on
// `application/proto; charset=utf-8` — a mistake no amount of reading the
// tavern's own tests would have caught, because both sides of those tests
// shared the bug.
//
// Skipped without WORKPLACE_ADDR, so `make test` stays cluster-free.
package conformance

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
	"github.com/teceer/pipsim/gen/go/pips/workplace/v1/workplacev1connect"
)

// High enough not to collide with whatever the cluster is doing if this is
// pointed at a live workplace.
const probePip = 999_000_001

func client(t *testing.T) (workplacev1connect.WorkplaceServiceClient, uint64) {
	t.Helper()

	addr := os.Getenv("WORKPLACE_ADDR")
	if addr == "" {
		t.Skip("set WORKPLACE_ADDR=host:port to run the conformance suite")
	}

	c := workplacev1connect.NewWorkplaceServiceClient(
		&http.Client{Timeout: 10 * time.Second}, "http://"+addr)

	desc, err := c.Describe(context.Background(),
		connect.NewRequest(&workplacev1.DescribeRequest{}))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	return c, desc.Msg.GetWorkplaceId()
}

func TestDescribeIdentifiesTheWorkplace(t *testing.T) {
	c, id := client(t)

	res, err := c.Describe(context.Background(),
		connect.NewRequest(&workplacev1.DescribeRequest{WorkplaceId: id}))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	msg := res.Msg

	if msg.GetWorkplaceId() == 0 {
		t.Error("workplace_id must be set; the gateway registers buildings by it")
	}
	if msg.GetKind() == "" {
		t.Error("kind must be set; it is the work queue's routing key")
	}
	if msg.GetMaxWorkers() <= 0 {
		t.Error("max_workers must be positive; sim-core copies it as the building's capacity")
	}
	if msg.GetPosition() == nil {
		t.Error("position must be set; pips walk to it")
	}
	if msg.GetCurrentWorkers() > msg.GetMaxWorkers() {
		t.Errorf("current_workers %d exceeds max_workers %d",
			msg.GetCurrentWorkers(), msg.GetMaxWorkers())
	}

	t.Logf("%s #%d, %d/%d workers at %v",
		msg.GetKind(), msg.GetWorkplaceId(),
		msg.GetCurrentWorkers(), msg.GetMaxWorkers(), msg.GetPosition())
}

// The shift lifecycle, in the order the gateway drives it.
func TestAShiftCanBeStartedWorkedAndEnded(t *testing.T) {
	c, id := client(t)
	ctx := context.Background()

	start, err := c.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
		WorkplaceId: id, PipId: probePip, Tick: 1,
	}))
	if err != nil {
		t.Fatalf("StartShift: %v", err)
	}
	if !start.Msg.GetAccepted() {
		t.Skipf("workplace is full (%q); cannot run the lifecycle", start.Msg.GetReason())
	}
	defer func() {
		_, _ = c.EndShift(ctx, connect.NewRequest(&workplacev1.EndShiftRequest{
			WorkplaceId: id, PipId: probePip, Tick: 100, Reason: "conformance probe",
		}))
	}()

	// Ten ticks later. Every workplace prices by elapsed ticks, because the
	// gateway batches — one call a second against ten ticks of world. A
	// workplace paying per call makes its own contract depend on the caller's
	// cadence.
	work, err := c.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: id, PipId: probePip, Tick: 11,
	}))
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if work.Msg.GetShiftShouldEnd() {
		t.Fatal("a shift started a moment ago should not be over")
	}
	if len(work.Msg.GetNeedDeltas()) == 0 {
		t.Error("Work over ten ticks returned no need deltas; the shift did nothing")
	}
	tenTicks := work.Msg.GetNeedDeltas()

	// Same tick again: paying twice would let a chatty caller mint resources.
	again, err := c.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: id, PipId: probePip, Tick: 11,
	}))
	if err != nil {
		t.Fatalf("Work (repeat): %v", err)
	}
	if len(again.Msg.GetNeedDeltas()) != 0 {
		t.Errorf("a repeated call in the same tick paid again: %v", again.Msg.GetNeedDeltas())
	}

	// One tick on should be worth about a tenth of ten.
	one, err := c.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: id, PipId: probePip, Tick: 12,
	}))
	if err != nil {
		t.Fatalf("Work (one tick): %v", err)
	}
	for need, ten := range tenTicks {
		got := one.Msg.GetNeedDeltas()[need]
		if ten == 0 {
			continue
		}
		if got*10 != ten {
			t.Errorf("need %d: one tick gave %d, ten ticks gave %d — not proportional",
				need, got, ten)
		}
	}
}

func TestWorkForAPipItNeverHiredEndsTheShift(t *testing.T) {
	c, id := client(t)

	res, err := c.Work(context.Background(), connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: id, PipId: probePip + 1, Tick: 1,
	}))
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !res.Msg.GetShiftShouldEnd() {
		t.Fatal("an unknown pip must be told to stop, not silently paid")
	}
}

// A stalled driver that resumes must not hand out a windfall.
func TestALongGapIsCapped(t *testing.T) {
	c, id := client(t)
	ctx := context.Background()
	pip := uint64(probePip + 2)

	start, err := c.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
		WorkplaceId: id, PipId: pip, Tick: 1,
	}))
	if err != nil {
		t.Fatalf("StartShift: %v", err)
	}
	if !start.Msg.GetAccepted() {
		t.Skipf("workplace is full (%q)", start.Msg.GetReason())
	}
	defer func() {
		_, _ = c.EndShift(ctx, connect.NewRequest(&workplacev1.EndShiftRequest{
			WorkplaceId: id, PipId: pip, Tick: 100, Reason: "conformance probe",
		}))
	}()

	huge, err := c.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: id, PipId: pip, Tick: 1_000_000,
	}))
	if err != nil {
		t.Fatalf("Work: %v", err)
	}

	for need, delta := range huge.Msg.GetNeedDeltas() {
		if delta > 10_000 || delta < -10_000 {
			t.Errorf("need %d moved by %d after a million ticks — the gap is not capped",
				need, delta)
		}
	}
}

func TestCanEmployAgreesWithItself(t *testing.T) {
	c, id := client(t)

	res, err := c.CanEmploy(context.Background(), connect.NewRequest(&workplacev1.CanEmployRequest{
		WorkplaceId: id, PipId: probePip + 3,
	}))
	if err != nil {
		t.Fatalf("CanEmploy: %v", err)
	}
	if !res.Msg.GetAllowed() && res.Msg.GetReason() == "" {
		t.Error("a refusal must say why; the gateway logs it and nothing else can")
	}
}

// EndShift for someone who was never hired is a no-op, not an error. The
// gateway calls it speculatively when it notices a pip is gone.
func TestEndShiftIsIdempotent(t *testing.T) {
	c, id := client(t)
	ctx := context.Background()

	for range 2 {
		if _, err := c.EndShift(ctx, connect.NewRequest(&workplacev1.EndShiftRequest{
			WorkplaceId: id, PipId: probePip + 4, Tick: 1, Reason: "conformance probe",
		})); err != nil {
			t.Fatalf("EndShift: %v", err)
		}
	}
}
