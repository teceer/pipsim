package ledger

import (
	"context"
	"math/rand"
	"testing"
)

func TestTransferMovesMoney(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	res, err := m.Transfer(ctx, TreasuryAccount, "pip:1", 100, KindIssuance, 1)
	if err != nil || !res.OK {
		t.Fatalf("issuance failed: %+v, %v", res, err)
	}

	res, err = m.Transfer(ctx, "pip:1", "workplace:9", 40, KindPurchase, 2)
	if err != nil || !res.OK {
		t.Fatalf("purchase failed: %+v, %v", res, err)
	}
	if res.PayerBalance != 60 {
		t.Fatalf("expected payer balance 60, got %d", res.PayerBalance)
	}

	bal, _ := m.GetBalance(ctx, "workplace:9")
	if bal != 40 {
		t.Fatalf("expected payee balance 40, got %d", bal)
	}
}

// A pip can never spend money it does not have.
func TestTransferRejectsInsufficientFunds(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	res, err := m.Transfer(ctx, "pip:1", "workplace:9", 40, KindPurchase, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected the transfer to be rejected")
	}
	bal, _ := m.GetBalance(ctx, "pip:1")
	if bal != 0 {
		t.Fatalf("a rejected transfer must not move partial money, got %d", bal)
	}
}

// (payer, payee, tick, kind) is the idempotency key: a retried call must not
// move money twice, exactly the guard ADR 0006 moves out of every workplace
// and into the ledger once.
func TestTransferIsIdempotent(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	_, _ = m.Transfer(ctx, TreasuryAccount, "pip:1", 100, KindIssuance, 1)

	first, err := m.Transfer(ctx, "pip:1", "workplace:9", 10, KindPurchase, 5)
	if err != nil || !first.OK {
		t.Fatalf("first transfer failed: %+v, %v", first, err)
	}
	second, err := m.Transfer(ctx, "pip:1", "workplace:9", 10, KindPurchase, 5)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("retried transfer returned a different result: %+v vs %+v", first, second)
	}

	bal, _ := m.GetBalance(ctx, "pip:1")
	if bal != 90 {
		t.Fatalf("retried transfer moved money twice, balance is %d", bal)
	}
}

func TestBatchTransferPaysAllOrNothing(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	_, _ = m.Transfer(ctx, TreasuryAccount, "workplace:9", 50, KindIssuance, 1)

	res, err := m.BatchTransfer(ctx, "workplace:9", []Credit{
		{Payee: "pip:1", Amount: 30},
		{Payee: "pip:2", Amount: 30},
	}, KindWage, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected the batch to be rejected as a whole")
	}
	if bal, _ := m.GetBalance(ctx, "pip:1"); bal != 0 {
		t.Fatalf("no credit should have landed, pip:1 has %d", bal)
	}
	if bal, _ := m.GetBalance(ctx, "pip:2"); bal != 0 {
		t.Fatalf("no credit should have landed, pip:2 has %d", bal)
	}

	res, err = m.BatchTransfer(ctx, "workplace:9", []Credit{
		{Payee: "pip:1", Amount: 20},
		{Payee: "pip:2", Amount: 30},
	}, KindWage, 3)
	if err != nil || !res.OK {
		t.Fatalf("affordable batch should have succeeded: %+v, %v", res, err)
	}
	if bal, _ := m.GetBalance(ctx, "pip:1"); bal != 20 {
		t.Fatalf("pip:1 expected 20, got %d", bal)
	}
	if bal, _ := m.GetBalance(ctx, "pip:2"); bal != 30 {
		t.Fatalf("pip:2 expected 30, got %d", bal)
	}
}

// Every transfer's journal rows sum to zero — the double-entry invariant.
func TestJournalEntriesAreDoubleEntry(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	_, _ = m.Transfer(ctx, TreasuryAccount, "pip:1", 100, KindIssuance, 1)
	_, _ = m.Transfer(ctx, "pip:1", "workplace:9", 40, KindPurchase, 2)
	_, _ = m.BatchTransfer(ctx, "workplace:9", []Credit{{Payee: "pip:2", Amount: 10}}, KindWage, 3)

	byTransfer := map[string]int64{}
	for _, e := range m.Journal() {
		byTransfer[e.TransferID] += e.Amount
	}
	if len(byTransfer) == 0 {
		t.Fatal("no journal entries recorded")
	}
	for id, sum := range byTransfer {
		if sum != 0 {
			t.Fatalf("transfer %s does not sum to zero: %d", id, sum)
		}
	}
}

// The strongest invariant in the project: the sum of all accounts is
// constant except at an explicit issuance. A random sequence of transfers,
// wage batches and purchases must never violate it.
func TestClosedSupplyUnderRandomTransfers(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	rng := rand.New(rand.NewSource(7))

	accounts := []string{"pip:1", "pip:2", "pip:3", "workplace:9", "workplace:10"}

	_, _ = m.Transfer(ctx, TreasuryAccount, "workplace:9", 1_000_000, KindIssuance, 0)

	for tick := uint64(1); tick <= 500; tick++ {
		switch rng.Intn(3) {
		case 0:
			payer := accounts[rng.Intn(len(accounts))]
			payee := accounts[rng.Intn(len(accounts))]
			_, _ = m.Transfer(ctx, payer, payee, int64(rng.Intn(50)), KindPurchase, tick)
		case 1:
			payer := accounts[rng.Intn(len(accounts))]
			_, _ = m.BatchTransfer(ctx, payer, []Credit{
				{Payee: accounts[rng.Intn(len(accounts))], Amount: int64(rng.Intn(30))},
				{Payee: accounts[rng.Intn(len(accounts))], Amount: int64(rng.Intn(30))},
			}, KindWage, tick)
		case 2:
			// Occasional issuance — the one legitimate way the sum moves,
			// and only ever from the treasury.
			_, _ = m.Transfer(ctx, TreasuryAccount, accounts[rng.Intn(len(accounts))], int64(rng.Intn(100)), KindIssuance, tick)
		}
	}

	var sum int64
	for _, bal := range m.Balances() {
		sum += bal
	}
	if sum != 0 {
		t.Fatalf("money supply is not closed: sum(balances) = %d, want 0", sum)
	}
}
