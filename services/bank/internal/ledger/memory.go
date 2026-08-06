package ledger

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// Memory is an in-process Ledger. It exists so `make test` never needs a
// running Postgres — the property, idempotency and double-entry invariants
// are checked against this implementation, and Postgres is exercised
// separately in an integration test gated behind a real database, the same
// way services/workplaces/conformance gates on WORKPLACE_ADDR.
type Memory struct {
	mu         sync.Mutex
	balances   map[string]int64
	journal    []JournalEntry
	idempotent map[string]Result
}

func NewMemory() *Memory {
	return &Memory{
		balances:   make(map[string]int64),
		idempotent: make(map[string]Result),
	}
}

func idempotencyKey(payer, payee string, tick uint64, kind Kind) string {
	return fmt.Sprintf("%s|%s|%d|%s", payer, payee, tick, kind)
}

func (m *Memory) OpenAccount(_ context.Context, accountID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A Go map returns the zero value for a key that was never set, so an
	// account "exists" the moment it is read or written — this just makes
	// that explicit and returns what it would already report.
	return m.balances[accountID], nil
}

func (m *Memory) Transfer(_ context.Context, payer, payee string, amount int64, kind Kind, tick uint64) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := idempotencyKey(payer, payee, tick, kind)
	if r, seen := m.idempotent[key]; seen {
		return r, nil
	}

	payerBalance := m.balances[payer]
	if payer != TreasuryAccount && payerBalance < amount {
		r := Result{OK: false, Reason: "insufficient funds", PayerBalance: payerBalance}
		m.idempotent[key] = r
		return r, nil
	}

	transferID := uuid.NewString()
	m.balances[payer] -= amount
	m.balances[payee] += amount
	m.journal = append(m.journal,
		JournalEntry{TransferID: transferID, Account: payer, Amount: -amount, Kind: kind, Tick: tick},
		JournalEntry{TransferID: transferID, Account: payee, Amount: amount, Kind: kind, Tick: tick},
	)

	r := Result{OK: true, PayerBalance: m.balances[payer]}
	m.idempotent[key] = r
	return r, nil
}

func (m *Memory) BatchTransfer(_ context.Context, payer string, credits []Credit, kind Kind, tick uint64) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// One workplace runs at most one payroll batch per tick per kind — the
	// credit list is the whole payload, so the payer/tick/kind triple alone
	// is a safe idempotency key for the batch as a unit.
	key := idempotencyKey(payer, "batch", tick, kind)
	if r, seen := m.idempotent[key]; seen {
		return r, nil
	}

	var total int64
	for _, c := range credits {
		total += c.Amount
	}

	payerBalance := m.balances[payer]
	if payer != TreasuryAccount && payerBalance < total {
		r := Result{OK: false, Reason: "insufficient funds", PayerBalance: payerBalance}
		m.idempotent[key] = r
		return r, nil
	}

	transferID := uuid.NewString()
	m.balances[payer] -= total
	m.journal = append(m.journal, JournalEntry{TransferID: transferID, Account: payer, Amount: -total, Kind: kind, Tick: tick})
	for _, c := range credits {
		m.balances[c.Payee] += c.Amount
		m.journal = append(m.journal, JournalEntry{TransferID: transferID, Account: c.Payee, Amount: c.Amount, Kind: kind, Tick: tick})
	}

	r := Result{OK: true, PayerBalance: m.balances[payer]}
	m.idempotent[key] = r
	return r, nil
}

func (m *Memory) GetBalance(_ context.Context, accountID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.balances[accountID], nil
}

// Journal is a test/debug hook onto the raw entries — the property and
// double-entry tests read it directly rather than through the Ledger
// interface, since no RPC exposes the journal itself.
func (m *Memory) Journal() []JournalEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]JournalEntry, len(m.journal))
	copy(out, m.journal)
	return out
}

// Balances is a test/debug hook for computing sum(balances) directly.
func (m *Memory) Balances() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(m.balances))
	for k, v := range m.balances {
		out[k] = v
	}
	return out
}
