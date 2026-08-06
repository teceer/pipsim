package ledger

import (
	"context"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration is plain SQL run once at startup. No migration framework: this is
// two tables and an idempotency table, and a framework would be more code
// than the schema it manages. Own Postgres schema per the repo's "schema per
// service" rule — nothing outside this package reads bank.* directly.
const Migration = `
CREATE SCHEMA IF NOT EXISTS bank;

CREATE TABLE IF NOT EXISTS bank.accounts (
	id      TEXT PRIMARY KEY,
	balance BIGINT NOT NULL DEFAULT 0
);

-- Never the source of truth by itself: every transfer writes two rows here
-- that sum to zero, and a balance is a fold over this table — accounts.balance
-- is a cache maintained in the same transaction, re-derivable if it ever
-- drifts.
CREATE TABLE IF NOT EXISTS bank.journal (
	id          BIGSERIAL PRIMARY KEY,
	transfer_id UUID NOT NULL,
	account_id  TEXT NOT NULL,
	amount      BIGINT NOT NULL,
	kind        TEXT NOT NULL,
	tick        BIGINT NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS journal_account_id_idx ON bank.journal (account_id);

-- (payer, payee, tick, kind) is the idempotency key from ADR 0006: RabbitMQ
-- redelivers, a gateway cycle retries, and this is where the guard against
-- being paid twice in one tick lives, once, for everyone.
CREATE TABLE IF NOT EXISTS bank.idempotency (
	payer         TEXT NOT NULL,
	payee         TEXT NOT NULL,
	tick          BIGINT NOT NULL,
	kind          TEXT NOT NULL,
	ok            BOOLEAN NOT NULL,
	reason        TEXT NOT NULL DEFAULT '',
	payer_balance BIGINT NOT NULL,
	PRIMARY KEY (payer, payee, tick, kind)
);
`

// Postgres is the authoritative Ledger, backing the running service.
type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// Migrate runs Migration. Safe to call on every startup: every statement is
// idempotent (IF NOT EXISTS).
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, Migration)
	return err
}

func (p *Postgres) OpenAccount(ctx context.Context, accountID string) (int64, error) {
	var balance int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO bank.accounts (id, balance) VALUES ($1, 0)
		ON CONFLICT (id) DO UPDATE SET id = bank.accounts.id
		RETURNING balance
	`, accountID).Scan(&balance)
	return balance, err
}

func (p *Postgres) GetBalance(ctx context.Context, accountID string) (int64, error) {
	var balance int64
	err := p.pool.QueryRow(ctx, `SELECT balance FROM bank.accounts WHERE id = $1`, accountID).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return balance, err
}

func ensureAccount(ctx context.Context, tx pgx.Tx, accountID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO bank.accounts (id, balance) VALUES ($1, 0) ON CONFLICT DO NOTHING`, accountID)
	return err
}

// lockAccount returns the balance of accountID, having taken a row lock that
// holds until the transaction ends — the mechanism that makes a concurrent
// Transfer against the same account wait rather than race.
func lockAccount(ctx context.Context, tx pgx.Tx, accountID string) (int64, error) {
	var balance int64
	err := tx.QueryRow(ctx, `SELECT balance FROM bank.accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&balance)
	return balance, err
}

func lookupIdempotent(ctx context.Context, tx pgx.Tx, payer, payee string, tick uint64, kind Kind) (Result, bool, error) {
	var r Result
	err := tx.QueryRow(ctx, `
		SELECT ok, reason, payer_balance FROM bank.idempotency
		WHERE payer = $1 AND payee = $2 AND tick = $3 AND kind = $4
	`, payer, payee, tick, string(kind)).Scan(&r.OK, &r.Reason, &r.PayerBalance)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}
	return r, err == nil, err
}

func recordIdempotent(ctx context.Context, tx pgx.Tx, payer, payee string, tick uint64, kind Kind, r Result) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO bank.idempotency (payer, payee, tick, kind, ok, reason, payer_balance)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, payer, payee, tick, string(kind), r.OK, r.Reason, r.PayerBalance)
	return err
}

func (p *Postgres) Transfer(ctx context.Context, payer, payee string, amount int64, kind Kind, tick uint64) (Result, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if r, seen, err := lookupIdempotent(ctx, tx, payer, payee, tick, kind); err != nil {
		return Result{}, err
	} else if seen {
		return r, nil
	}

	// Lock in a stable order regardless of who is payer and who is payee —
	// two concurrent transfers between the same pair, in opposite
	// directions, must not deadlock each other.
	balances := make(map[string]int64, 2)
	for _, id := range lockOrder(payer, payee) {
		if err := ensureAccount(ctx, tx, id); err != nil {
			return Result{}, err
		}
		balance, err := lockAccount(ctx, tx, id)
		if err != nil {
			return Result{}, err
		}
		balances[id] = balance
	}
	payerBalance := balances[payer]

	if payer != TreasuryAccount && payerBalance < amount {
		// Not recorded under the idempotency key. A rejection is not a
		// result: nothing moved, so there is nothing to replay. Caching it
		// would mean a payer funded later in the same tick is still refused,
		// answered from a memory of having been broke.
		return Result{OK: false, Reason: "insufficient funds", PayerBalance: payerBalance}, nil
	}

	transferID := uuid.NewString()
	if _, err := tx.Exec(ctx, `UPDATE bank.accounts SET balance = balance - $1 WHERE id = $2`, amount, payer); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE bank.accounts SET balance = balance + $1 WHERE id = $2`, amount, payee); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bank.journal (transfer_id, account_id, amount, kind, tick) VALUES
		($1, $2, $3, $4, $5), ($1, $6, $7, $4, $5)
	`, transferID, payer, -amount, string(kind), tick, payee, amount); err != nil {
		return Result{}, err
	}

	r := Result{OK: true, PayerBalance: payerBalance - amount}
	if err := recordIdempotent(ctx, tx, payer, payee, tick, kind, r); err != nil {
		return Result{}, err
	}
	return r, tx.Commit(ctx)
}

// Post books a decided movement. See Ledger.Post — no balance check, and the
// caller's transferID is the idempotency key. The journal's own primary key
// does that work: a repeated insert conflicts and is dropped, so a redelivered
// fact books exactly once without a separate idempotency row.
func (p *Postgres) Post(ctx context.Context, transferID, payer, payee string, amount int64, kind Kind, tick uint64) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, id := range lockOrder(payer, payee) {
		if err := ensureAccount(ctx, tx, id); err != nil {
			return err
		}
		if _, err := lockAccount(ctx, tx, id); err != nil {
			return err
		}
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO bank.journal (transfer_id, account_id, amount, kind, tick) VALUES
		($1, $2, $3, $4, $5), ($1, $6, $7, $4, $5)
		ON CONFLICT DO NOTHING
	`, transferID, payer, -amount, string(kind), tick, payee, amount)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Already booked. The balances were moved by the first delivery.
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, `UPDATE bank.accounts SET balance = balance - $1 WHERE id = $2`, amount, payer); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bank.accounts SET balance = balance + $1 WHERE id = $2`, amount, payee); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) BatchTransfer(ctx context.Context, payer string, credits []Credit, kind Kind, tick uint64) (Result, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// One workplace runs at most one payroll batch per tick per kind, so the
	// batch as a whole shares one idempotency row keyed against a
	// synthetic "batch" payee.
	if r, seen, err := lookupIdempotent(ctx, tx, payer, "batch", tick, kind); err != nil {
		return Result{}, err
	} else if seen {
		return r, nil
	}

	if err := ensureAccount(ctx, tx, payer); err != nil {
		return Result{}, err
	}
	payerBalance, err := lockAccount(ctx, tx, payer)
	if err != nil {
		return Result{}, err
	}

	var total int64
	for _, c := range credits {
		total += c.Amount
	}

	if payer != TreasuryAccount && payerBalance < total {
		// Not cached — see Transfer.
		return Result{OK: false, Reason: "insufficient funds", PayerBalance: payerBalance}, nil
	}

	transferID := uuid.NewString()
	if _, err := tx.Exec(ctx, `UPDATE bank.accounts SET balance = balance - $1 WHERE id = $2`, total, payer); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bank.journal (transfer_id, account_id, amount, kind, tick) VALUES ($1, $2, $3, $4, $5)
	`, transferID, payer, -total, string(kind), tick); err != nil {
		return Result{}, err
	}
	for _, c := range credits {
		if err := ensureAccount(ctx, tx, c.Payee); err != nil {
			return Result{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE bank.accounts SET balance = balance + $1 WHERE id = $2`, c.Amount, c.Payee); err != nil {
			return Result{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO bank.journal (transfer_id, account_id, amount, kind, tick) VALUES ($1, $2, $3, $4, $5)
		`, transferID, c.Payee, c.Amount, string(kind), tick); err != nil {
			return Result{}, err
		}
	}

	r := Result{OK: true, PayerBalance: payerBalance - total}
	if err := recordIdempotent(ctx, tx, payer, "batch", tick, kind, r); err != nil {
		return Result{}, err
	}
	return r, tx.Commit(ctx)
}

func lockOrder(a, b string) []string {
	if a == b {
		return []string{a}
	}
	ids := []string{a, b}
	sort.Strings(ids)
	return ids
}
