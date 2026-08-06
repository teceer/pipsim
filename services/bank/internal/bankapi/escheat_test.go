package bankapi_test

import (
	"context"
	"testing"

	"github.com/teceer/pipsim/services/bank/internal/bankapi"
	"github.com/teceer/pipsim/services/bank/internal/ledger"
)

// sumBalances is the closed-supply check: the treasury's negative and
// everyone else's positive balances sum to zero, always, except while an
// issuance is in flight.
func sumBalances(t *testing.T, m *ledger.Memory) int64 {
	t.Helper()
	var total int64
	for _, v := range m.Balances() {
		total += v
	}
	return total
}

func TestEscheatReturnsADeadPipsBalanceToTheTreasury(t *testing.T) {
	ctx := context.Background()
	m := ledger.NewMemory()

	if _, err := m.Transfer(ctx, ledger.TreasuryAccount, "pip:7", 250, ledger.KindIssuance, 1); err != nil {
		t.Fatalf("issuance: %v", err)
	}
	if got := sumBalances(t, m); got != 0 {
		t.Fatalf("supply not closed after issuance: %d", got)
	}

	if err := bankapi.Escheat(ctx, m, 7, 42); err != nil {
		t.Fatalf("escheat: %v", err)
	}

	balance, err := m.GetBalance(ctx, "pip:7")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 0 {
		t.Errorf("dead pip still holds %d", balance)
	}
	if treasury, _ := m.GetBalance(ctx, ledger.TreasuryAccount); treasury != 0 {
		t.Errorf("treasury = %d, want the issuance back at 0", treasury)
	}
	if got := sumBalances(t, m); got != 0 {
		t.Errorf("supply not closed after escheat: %d", got)
	}
}

// A redelivered PipDied must not move money twice. Kafka is at-least-once and
// the consumer commits after handling, so this is the ordinary case, not an
// edge one.
func TestEscheatIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m := ledger.NewMemory()
	if _, err := m.Transfer(ctx, ledger.TreasuryAccount, "pip:7", 250, ledger.KindIssuance, 1); err != nil {
		t.Fatalf("issuance: %v", err)
	}

	for range 3 {
		if err := bankapi.Escheat(ctx, m, 7, 42); err != nil {
			t.Fatalf("escheat: %v", err)
		}
	}

	if treasury, _ := m.GetBalance(ctx, ledger.TreasuryAccount); treasury != 0 {
		t.Errorf("treasury = %d after three deliveries, want 0", treasury)
	}
	if got := sumBalances(t, m); got != 0 {
		t.Errorf("supply not closed: %d", got)
	}

	// Two journal rows for the issuance, two for the one escheat that moved
	// money. A second pair would mean the redelivery was booked.
	if entries := m.Journal(); len(entries) != 4 {
		t.Errorf("journal has %d entries, want 4", len(entries))
	}
}

// Most pips die broke. That path must not write a zero-amount transfer, or
// the journal fills with rows describing nothing.
func TestEscheatOfABrokePipWritesNothing(t *testing.T) {
	ctx := context.Background()
	m := ledger.NewMemory()

	if err := bankapi.Escheat(ctx, m, 7, 42); err != nil {
		t.Fatalf("escheat: %v", err)
	}

	if entries := m.Journal(); len(entries) != 0 {
		t.Errorf("journal has %d entries, want none", len(entries))
	}
}
