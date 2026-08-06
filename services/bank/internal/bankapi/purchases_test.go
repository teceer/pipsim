package bankapi_test

import (
	"context"
	"testing"

	eventsv1 "github.com/teceer/pipsim/gen/go/pips/events/v1"
	"github.com/teceer/pipsim/services/bank/internal/bankapi"
	"github.com/teceer/pipsim/services/bank/internal/ledger"
)

func purchase(payer, payee string, amount int64) *eventsv1.PurchaseMade {
	return &eventsv1.PurchaseMade{Payer: payer, Payee: payee, Amount: amount}
}

func TestBookPurchaseMovesMoneyWithoutGatingIt(t *testing.T) {
	ctx := context.Background()
	m := ledger.NewMemory()
	if _, err := m.Transfer(ctx, ledger.TreasuryAccount, "pip:7", 100, ledger.KindIssuance, 1); err != nil {
		t.Fatalf("issuance: %v", err)
	}

	if err := bankapi.BookPurchase(ctx, m, "evt-1", purchase("pip:7", "workplace:9", 40), 12); err != nil {
		t.Fatalf("book: %v", err)
	}

	if got, _ := m.GetBalance(ctx, "pip:7"); got != 60 {
		t.Errorf("payer balance = %d, want 60", got)
	}
	if got, _ := m.GetBalance(ctx, "workplace:9"); got != 40 {
		t.Errorf("payee balance = %d, want 40", got)
	}
}

// Kafka is at-least-once, so this is the ordinary case. The event id is the
// idempotency key — deliberately not (payer, payee, tick, kind), which would
// collapse two genuine purchases from the same seller in one tick into one.
func TestBookPurchaseIsIdempotentOnTheEventID(t *testing.T) {
	ctx := context.Background()
	m := ledger.NewMemory()
	if _, err := m.Transfer(ctx, ledger.TreasuryAccount, "pip:7", 100, ledger.KindIssuance, 1); err != nil {
		t.Fatalf("issuance: %v", err)
	}

	for range 3 {
		if err := bankapi.BookPurchase(ctx, m, "evt-1", purchase("pip:7", "workplace:9", 40), 12); err != nil {
			t.Fatalf("book: %v", err)
		}
	}

	if got, _ := m.GetBalance(ctx, "pip:7"); got != 60 {
		t.Errorf("payer balance = %d after three deliveries, want 60", got)
	}
}

// Two purchases in one tick from the same seller are two movements. Under the
// old (payer, payee, tick, kind) key the second would have been answered from
// the first's cached result — charged once in the bank, twice in the core.
func TestTwoPurchasesInOneTickAreBookedSeparately(t *testing.T) {
	ctx := context.Background()
	m := ledger.NewMemory()
	if _, err := m.Transfer(ctx, ledger.TreasuryAccount, "pip:7", 100, ledger.KindIssuance, 1); err != nil {
		t.Fatalf("issuance: %v", err)
	}

	for _, id := range []string{"evt-1", "evt-2"} {
		if err := bankapi.BookPurchase(ctx, m, id, purchase("pip:7", "workplace:9", 40), 12); err != nil {
			t.Fatalf("book %s: %v", id, err)
		}
	}

	if got, _ := m.GetBalance(ctx, "pip:7"); got != 20 {
		t.Errorf("payer balance = %d, want 20 — both purchases must be booked", got)
	}
}

// An event with no id has no idempotency key, so booking it would double the
// payment on the next redelivery. Refusing is the safe answer.
func TestBookPurchaseRefusesAnEventWithoutAnID(t *testing.T) {
	m := ledger.NewMemory()
	if err := bankapi.BookPurchase(context.Background(), m, "", purchase("pip:7", "workplace:9", 40), 12); err == nil {
		t.Error("expected an error for an event with no id")
	}
	if entries := m.Journal(); len(entries) != 0 {
		t.Errorf("journal has %d entries, want none", len(entries))
	}
}
