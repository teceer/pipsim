package conformance

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

// ADR 0006's money chapter: a workplace pays what it declares, does not pay
// twice for one tick, and charges the price it advertises. Checkable against
// any implementation, which is what keeps the polyglot claim honest as the
// contract widens.

func TestDescribeExposesWageAndPricing(t *testing.T) {
	c, id := client(t)

	res, err := c.Describe(context.Background(),
		connect.NewRequest(&workplacev1.DescribeRequest{WorkplaceId: id}))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	msg := res.Msg

	if msg.GetWage() < 0 {
		t.Errorf("wage must not be negative, got %d", msg.GetWage())
	}
	if msg.GetEffort() < 0 {
		t.Errorf("effort must not be negative, got %d", msg.GetEffort())
	}
	for _, offer := range msg.GetSells() {
		if offer.GetPrice() <= 0 {
			t.Errorf("sells %v with a non-positive price %d", offer.GetKind(), offer.GetPrice())
		}
		if offer.GetKind() == workplacev1.ResourceKind_RESOURCE_KIND_UNSPECIFIED {
			t.Error("sells an unspecified resource kind")
		}
	}
}

// A wage follows the same elapsed-tick pricing and same-tick idempotency
// need_deltas already established — it is checked the same way here.
func TestWorkPaysWageProportionalToElapsed(t *testing.T) {
	c, id := client(t)
	ctx := context.Background()
	pip := uint64(probePip + 10)

	desc, err := c.Describe(ctx, connect.NewRequest(&workplacev1.DescribeRequest{WorkplaceId: id}))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Msg.GetWage() == 0 {
		t.Skip("this workplace does not pay a wage")
	}

	start, err := c.StartShift(ctx, connect.NewRequest(&workplacev1.StartShiftRequest{
		WorkplaceId: id, PipId: pip, Tick: 1,
	}))
	if err != nil {
		t.Fatalf("StartShift: %v", err)
	}
	if !start.Msg.GetAccepted() {
		t.Skipf("workplace is full (%q); cannot run the lifecycle", start.Msg.GetReason())
	}
	defer func() {
		_, _ = c.EndShift(ctx, connect.NewRequest(&workplacev1.EndShiftRequest{
			WorkplaceId: id, PipId: pip, Tick: 100, Reason: "conformance probe",
		}))
	}()

	ten, err := c.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: id, PipId: pip, Tick: 11,
	}))
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if ten.Msg.GetWage() == 0 {
		t.Error("Work over ten ticks paid no wage, but Describe advertises one")
	}

	// Same tick again: paying twice would let a chatty caller mint currency.
	again, err := c.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: id, PipId: pip, Tick: 11,
	}))
	if err != nil {
		t.Fatalf("Work (repeat): %v", err)
	}
	if again.Msg.GetWage() != 0 {
		t.Errorf("a repeated call in the same tick paid again: wage=%d", again.Msg.GetWage())
	}

	one, err := c.Work(ctx, connect.NewRequest(&workplacev1.WorkRequest{
		WorkplaceId: id, PipId: pip, Tick: 12,
	}))
	if err != nil {
		t.Fatalf("Work (one tick): %v", err)
	}
	if one.Msg.GetWage()*10 != ten.Msg.GetWage() {
		t.Errorf("wage not proportional: one tick paid %d, ten ticks paid %d",
			one.Msg.GetWage(), ten.Msg.GetWage())
	}
}

// A workplace that sells something charges exactly what it advertised.
func TestBuyChargesTheAdvertisedPrice(t *testing.T) {
	c, id := client(t)
	ctx := context.Background()
	pip := uint64(probePip + 11)

	desc, err := c.Describe(ctx, connect.NewRequest(&workplacev1.DescribeRequest{WorkplaceId: id}))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	offers := desc.Msg.GetSells()
	if len(offers) == 0 {
		t.Skip("this workplace sells nothing")
	}
	kind := offers[0].GetKind()
	advertised := offers[0].GetPrice()

	res, err := c.Buy(ctx, connect.NewRequest(&workplacev1.BuyRequest{
		WorkplaceId: id, PipId: pip, Kind: kind, Tick: 1,
	}))
	if err != nil {
		t.Fatalf("Buy: %v", err)
	}
	if !res.Msg.GetOk() {
		t.Fatalf("Buy for an advertised kind was refused: %q", res.Msg.GetReason())
	}
	if res.Msg.GetPrice() != advertised {
		t.Errorf("Describe advertised %d, Buy charged %d", advertised, res.Msg.GetPrice())
	}

	// A kind nobody advertises must be refused, not silently sold.
	unadvertised, err := c.Buy(ctx, connect.NewRequest(&workplacev1.BuyRequest{
		WorkplaceId: id, PipId: pip, Kind: workplacev1.ResourceKind_RESOURCE_KIND_TOOL, Tick: 1,
	}))
	if err != nil {
		t.Fatalf("Buy (unadvertised): %v", err)
	}
	sold := false
	for _, o := range offers {
		if o.GetKind() == workplacev1.ResourceKind_RESOURCE_KIND_TOOL {
			sold = true
		}
	}
	if !sold && unadvertised.Msg.GetOk() {
		t.Error("Buy sold a resource kind Describe never advertised")
	}
}
