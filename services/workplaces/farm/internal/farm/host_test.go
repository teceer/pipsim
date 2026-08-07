package farm

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

func twoFarms() *Host {
	return NewHost(New(1), New(3))
}

func TestListReportsEveryBuilding(t *testing.T) {
	res, err := twoFarms().List(context.Background(),
		connect.NewRequest(&workplacev1.ListRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	got := res.Msg.GetWorkplaces()
	if len(got) != 2 {
		t.Fatalf("want 2 buildings, got %d", len(got))
	}
	// Sorted, so the gateway registering them on a loop does not see them
	// reorder between calls for no reason.
	if got[0].GetWorkplaceId() != 1 || got[1].GetWorkplaceId() != 3 {
		t.Errorf("want ids 1,3 in order, got %d,%d",
			got[0].GetWorkplaceId(), got[1].GetWorkplaceId())
	}
}

func TestDescribeWithoutAnIdIsRefusedWhenHostingSeveral(t *testing.T) {
	_, err := twoFarms().Describe(context.Background(),
		connect.NewRequest(&workplacev1.DescribeRequest{}))
	if err == nil {
		t.Fatal("want an error: with two buildings there is no 'who are you'")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("want InvalidArgument, got %v", connect.CodeOf(err))
	}
}

// The single-building case has to keep working: the Helm chart, `make run` and
// every workplace written before List all address a service without naming a
// building.
func TestDescribeWithoutAnIdWorksWhenHostingOne(t *testing.T) {
	h := NewHost(New(7))
	res, err := h.Describe(context.Background(),
		connect.NewRequest(&workplacev1.DescribeRequest{}))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if res.Msg.GetWorkplaceId() != 7 {
		t.Errorf("want workplace 7, got %d", res.Msg.GetWorkplaceId())
	}
}

func TestUnknownBuildingIsNotFound(t *testing.T) {
	_, err := twoFarms().Describe(context.Background(),
		connect.NewRequest(&workplacev1.DescribeRequest{WorkplaceId: 99}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("want NotFound, got %v", connect.CodeOf(err))
	}
}

// Capacity belongs to a building, not to the process. Filling one must leave
// the other empty — the bug this whole change exists to make impossible.
func TestBuildingsDoNotShareShifts(t *testing.T) {
	h := twoFarms()
	ctx := context.Background()

	for pip := uint64(100); pip < 103; pip++ {
		res, err := h.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
			WorkplaceId: 1, PipId: pip, Tick: 1,
		}))
		if err != nil || !res.Msg.GetAccepted() {
			t.Fatalf("StartShift at farm 1 for pip %d: %v", pip, err)
		}
	}

	one, _ := h.Describe(ctx, connect.NewRequest(
		&workplacev1.DescribeRequest{WorkplaceId: 1}))
	three, _ := h.Describe(ctx, connect.NewRequest(
		&workplacev1.DescribeRequest{WorkplaceId: 3}))

	if one.Msg.GetCurrentWorkers() != 3 {
		t.Errorf("farm 1: want 3 workers, got %d", one.Msg.GetCurrentWorkers())
	}
	if three.Msg.GetCurrentWorkers() != 0 {
		t.Errorf("farm 3: want 0 workers, got %d", three.Msg.GetCurrentWorkers())
	}
}

// A pip on shift at one building is a stranger at the next one, even though
// both live in this process.
func TestWorkIsScopedToTheBuildingThatHired(t *testing.T) {
	h := twoFarms()
	ctx := context.Background()

	_, err := h.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
		WorkplaceId: 1, PipId: 42, Tick: 1,
	}))
	if err != nil {
		t.Fatalf("StartShift: %v", err)
	}

	res, err := h.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: 3, PipId: 42, Tick: 2,
	}))
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !res.Msg.GetShiftShouldEnd() {
		t.Error("farm 3 paid a pip employed by farm 1")
	}
}

// Offers rotate. A fixed order would fill the lowest id to capacity before the
// next building ever saw a candidate, making configuration order the hiring
// policy.
func TestOffersRotateBetweenBuildings(t *testing.T) {
	h := twoFarms()
	ctx := context.Background()

	for pip := uint64(200); pip < 204; pip++ {
		if ok, reason := h.ConsiderOffer(ctx, pip, 1); !ok {
			t.Fatalf("offer for pip %d rejected: %s", pip, reason)
		}
	}

	one, _ := h.Describe(ctx, connect.NewRequest(
		&workplacev1.DescribeRequest{WorkplaceId: 1}))
	three, _ := h.Describe(ctx, connect.NewRequest(
		&workplacev1.DescribeRequest{WorkplaceId: 3}))

	if one.Msg.GetCurrentWorkers() != 2 || three.Msg.GetCurrentWorkers() != 2 {
		t.Errorf("want four offers split 2/2, got %d/%d",
			one.Msg.GetCurrentWorkers(), three.Msg.GetCurrentWorkers())
	}
}

// An offer is only refused once every building has refused it.
func TestAnOfferFallsThroughToABuildingWithRoom(t *testing.T) {
	h := twoFarms()
	ctx := context.Background()

	// Fill farm 1 completely.
	for i := 0; i < MaxWorkers; i++ {
		_, err := h.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
			WorkplaceId: 1, PipId: uint64(1_000 + i), Tick: 1,
		}))
		if err != nil {
			t.Fatalf("filling farm 1: %v", err)
		}
	}

	for pip := uint64(300); pip < 305; pip++ {
		if ok, reason := h.ConsiderOffer(ctx, pip, 1); !ok {
			t.Fatalf("offer for pip %d rejected with room at farm 3: %s", pip, reason)
		}
	}

	three, _ := h.Describe(ctx, connect.NewRequest(
		&workplacev1.DescribeRequest{WorkplaceId: 3}))
	if three.Msg.GetCurrentWorkers() != 5 {
		t.Errorf("farm 3: want the 5 overflow pips, got %d",
			three.Msg.GetCurrentWorkers())
	}
}

func TestWorkersSumsAcrossBuildings(t *testing.T) {
	h := twoFarms()
	ctx := context.Background()

	_, _ = h.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
		WorkplaceId: 1, PipId: 1, Tick: 1}))
	_, _ = h.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
		WorkplaceId: 3, PipId: 2, Tick: 1}))

	if got := h.Workers(); got != 2 {
		t.Errorf("want 2 workers across both buildings, got %d", got)
	}
}

func TestParseSpecs(t *testing.T) {
	got, err := ParseSpecs(" 1 , 3 ")
	if err != nil {
		t.Fatalf("ParseSpecs: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Errorf("unexpected specs: %+v", got)
	}

	// Strict on purpose: a dropped building is an economy quietly smaller than
	// the one that was configured, and nothing would report it.
	for _, bad := range []string{"", "0", "x", "1,x"} {
		if _, err := ParseSpecs(bad); err == nil {
			t.Errorf("ParseSpecs(%q) should have failed", bad)
		}
	}
}
