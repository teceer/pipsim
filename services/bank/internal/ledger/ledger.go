// Package ledger is the double-entry bookkeeping at the heart of the bank.
//
// Every actor — pip, workplace, treasury — has exactly one account, and every
// movement of money is a transfer between two of them. There is no cash: no
// second pool, no reconciliation between two kinds of money. A balance is a
// fold over the journal, cached and re-derivable — never a mutable column
// trusted as the source of truth. See docs/adr/0006.
package ledger

import "context"

// TreasuryAccount is the one account exempt from the "can never go negative"
// rule. Every transfer is a move, never a creation — issuance is a transfer
// from this account, and its balance going negative is exactly what keeps
// the money supply closed: the treasury's negative and everyone else's
// positive balances always sum to zero.
const TreasuryAccount = "treasury"

type Kind string

const (
	KindWage     Kind = "WAGE"
	KindPurchase Kind = "PURCHASE"
	KindIssuance Kind = "ISSUANCE"
)

// Credit is one payee's share of a BatchTransfer — payroll, one workplace
// paying every pip on shift in a single round trip.
type Credit struct {
	Payee  string
	Amount int64
}

// Result mirrors pips.bank.v1.TransferResponse / BatchTransferResponse. Both
// RPCs return the same shape, so one type serves both.
type Result struct {
	OK           bool
	Reason       string
	PayerBalance int64
}

// JournalEntry is one row. Every transfer writes exactly two — a debit and a
// credit summing to zero — which is what makes the ledger explanatory rather
// than just authoritative: replaying the journal reconstructs any balance.
type JournalEntry struct {
	TransferID string
	Account    string
	Amount     int64
	Kind       Kind
	Tick       uint64
}

// Ledger is the interface both the in-memory (tests, and CI without a
// Postgres) and Postgres-backed implementations satisfy, so the RPC layer
// and its tests never know which one they are driving.
type Ledger interface {
	// OpenAccount is idempotent: opening an account that already exists
	// returns its current balance unchanged.
	OpenAccount(ctx context.Context, accountID string) (balance int64, err error)

	// Transfer moves money from payer to payee. (payer, payee, tick, kind) is
	// the idempotency key — calling this twice with the same key returns the
	// first call's result rather than moving money twice.
	Transfer(ctx context.Context, payer, payee string, amount int64, kind Kind, tick uint64) (Result, error)

	// BatchTransfer is Transfer with one payer and many payees, in a single
	// atomic move — this is payroll, one call per workplace per cycle. Either
	// every credit lands or none do; a payer that cannot cover the sum pays
	// nobody, rather than paying whoever came first in the list.
	BatchTransfer(ctx context.Context, payer string, credits []Credit, kind Kind, tick uint64) (Result, error)

	GetBalance(ctx context.Context, accountID string) (int64, error)
}
