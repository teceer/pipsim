package economy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	bankv1 "github.com/teceer/pipsim/gen/go/pips/bank/v1"
	simv1 "github.com/teceer/pipsim/gen/go/pips/sim/v1"
	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

func workplaceAccount(id uint64) string { return fmt.Sprintf("workplace:%d", id) }
func pipAccount(id uint64) string       { return fmt.Sprintf("pip:%d", id) }

// payWages moves one workplace's payroll for a cycle in one BatchTransfer and
// one CreditBalancesIntent — the ADR's "one Transfer per workplace per
// cycle, one intent carrying the credits back," not one round trip per pip.
//
// A bank that cannot be reached skips payroll for the cycle rather than
// stalling it — the same posture farm.go already takes toward a store blip:
// Unavailable is deliberately not shift_should_end. Wages resume the next
// cycle a shift is still open.
func (f *Fleet) payWages(ctx context.Context, workplaceID, tick uint64, credits map[uint64]int64) {
	if f.bank == nil || len(credits) == 0 {
		return
	}

	bankCredits := make([]*bankv1.BatchTransferRequest_Credit, 0, len(credits))
	simCredits := make([]*simv1.CreditBalancesIntent_Credit, 0, len(credits))
	for pip, amount := range credits {
		if amount <= 0 {
			continue
		}
		bankCredits = append(bankCredits, &bankv1.BatchTransferRequest_Credit{
			Payee: pipAccount(pip), Amount: amount,
		})
		simCredits = append(simCredits, &simv1.CreditBalancesIntent_Credit{
			PipId: pip, Amount: amount,
		})
	}
	if len(bankCredits) == 0 {
		return
	}

	res, err := f.bank.BatchTransfer(ctx, connect.NewRequest(&bankv1.BatchTransferRequest{
		Payer:   workplaceAccount(workplaceID),
		Credits: bankCredits,
		Kind:    bankv1.TransferKind_TRANSFER_KIND_WAGE,
		Tick:    tick,
	}))
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnavailable {
			slog.Warn("payroll transfer failed", "workplace", workplaceID, "err", err)
		}
		return
	}
	if !res.Msg.GetOk() {
		slog.Warn("payroll rejected", "workplace", workplaceID, "reason", res.Msg.GetReason())
		return
	}

	// The bank already moved the money; this is the replica sync sim-core
	// needs so a tick can decide "can this pip afford it" without a network
	// call. Best-effort: a pip believing it is briefly poorer than the
	// ledger says is the safe direction of error the ADR calls out.
	if _, err := f.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_CreditBalances{
			CreditBalances: &simv1.CreditBalancesIntent{
				PayerAccountId: workplaceAccount(workplaceID),
				Credits:        simCredits,
				Tick:           tick,
			},
		},
	})); err != nil {
		slog.Warn("could not record payroll credits in sim-core", "workplace", workplaceID, "err", err)
	}
}

// Buy runs one purchase: ask the workplace for its price, check the pip can
// actually complete it, then submit the transfer as an intent.
//
// The bank is not called. A purchase is settled by sim-core against the pip's
// replica balance inside a tick — that is the only place the decision can be
// made without a network call, so the core gates it and the bank books the
// fact afterwards off `pipsim.economy.purchases.v1`.
//
// The earlier shape had the bank commit first and the core apply second. When
// the core disagreed — which it does whenever a credit has not reached it yet
// — the pip was charged and got nothing, and the replica ended up believing
// the pip was richer than the ledger did. Exactly one side gates each
// movement, and it is the side that can see what the other cannot.
//
// The checks here are a courtesy, not the gate: they turn "silently nothing
// happened" into a reason the caller can show. The core re-checks both, and
// its answer is the one that counts.
func (f *Fleet) Buy(ctx context.Context, pipID, workplaceID uint64, kind workplacev1.ResourceKind, tick uint64) (ok bool, reason string, price int64, err error) {
	var d *Driver
	for _, candidate := range f.drivers {
		if candidate.ID() == workplaceID {
			d = candidate
			break
		}
	}
	if d == nil {
		return false, "unknown workplace", 0, nil
	}

	snap, err := f.sim.Snapshot(ctx, connect.NewRequest(&simv1.SnapshotRequest{}))
	if err != nil {
		return false, "", 0, err
	}
	var buyer *simv1.Pip
	for _, p := range snap.Msg.GetPips() {
		if p.GetId() == pipID {
			buyer = p
			break
		}
	}
	if buyer == nil {
		return false, "no such pip", 0, nil
	}
	// Buying is physical, like working: ADR 0004 made buildings places rather
	// than addresses, and a purchase from across the map would undo that.
	if buyer.GetInsideWorkplaceId() != workplaceID {
		return false, "the pip is not inside this building", 0, nil
	}

	buyRes, err := d.workplace.Buy(ctx, connect.NewRequest(&workplacev1.BuyRequest{
		WorkplaceId: workplaceID,
		PipId:       pipID,
		Kind:        kind,
		Tick:        tick,
	}))
	if err != nil {
		return false, "", 0, err
	}
	if !buyRes.Msg.GetOk() {
		return false, buyRes.Msg.GetReason(), 0, nil
	}
	price = buyRes.Msg.GetPrice()

	if buyer.GetBalance() < price {
		return false, "insufficient funds", price, nil
	}

	if _, err := f.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_Transfer{
			Transfer: &simv1.TransferIntent{
				PayerAccountId: pipAccount(pipID),
				PayeeAccountId: workplaceAccount(workplaceID),
				Amount:         price,
				ResourceKind:   int32(kind),
				Kind:           simv1.TransferKind_TRANSFER_KIND_PURCHASE,
				Tick:           tick,
			},
		},
	})); err != nil {
		return false, "", price, err
	}

	return true, "", price, nil
}

// Endow gives a workplace the capital it pays wages out of.
//
// Without it the economy cannot start: every account opens at zero, a payer
// with nothing pays nobody, and payroll rejects for insufficient funds
// forever. Money has to enter the world somewhere, and issuance from the
// treasury is the one sanctioned way — ADR 0006 keeps the supply closed by
// making every other movement a transfer between existing accounts.
//
// Tops up to `capital` whenever a workplace falls below a quarter of it,
// rather than only funding an account at exactly zero.
//
// The old rule — skip anything holding more than nothing — starved the economy
// dead. Money leaves circulation permanently: a pip that dies with wages in
// its pocket has that balance escheated to the treasury (ADR 0006), and
// nothing ever sends it back out. Both workplaces were issued 10,000, drained
// to 304 and 15 within the hour, and were then skipped forever because a
// balance of 15 is "more than nothing". Payroll rejected for insufficient
// funds on every cycle, so no pip was paid, so no pip could buy food.
//
// The treasury is therefore the redistributor as well as the issuer, which is
// what closes the loop: escheat pulls money in, endowment pushes it back out.
// That is a bailout, named as one. It is the right shape for a simulation
// whose population turns over constantly — a workplace here does not fail
// through mismanagement, it fails because its customers keep dying — and it
// keeps money supply auditable, since every movement is still a transfer
// between existing accounts.
//
// The quarter is hysteresis, not a magic number: topping up on any shortfall
// would issue a transfer per cycle forever, and the gap has to be wide enough
// that a workplace can pay a full round of wages between refills.
func (f *Fleet) Endow(ctx context.Context, capital int64) {
	if f.bank == nil || capital <= 0 {
		return
	}

	snap, err := f.sim.Snapshot(ctx, connect.NewRequest(&simv1.SnapshotRequest{}))
	if err != nil {
		slog.Warn("could not read the world to endow workplaces", "err", err)
		return
	}
	tick := snap.Msg.GetTick()

	for _, d := range f.drivers {
		id := d.ID()
		if id == 0 {
			continue // has not introduced itself yet
		}
		account := workplaceAccount(id)

		balance, err := f.bank.GetBalance(ctx, connect.NewRequest(&bankv1.GetBalanceRequest{
			AccountId: account,
		}))
		if err != nil {
			slog.Warn("could not read a workplace balance", "workplace", id, "err", err)
			continue
		}
		held := balance.Msg.GetBalance()
		amount := endowmentFor(held, capital)
		if amount == 0 {
			continue
		}

		res, err := f.bank.Transfer(ctx, connect.NewRequest(&bankv1.TransferRequest{
			Payer:  "treasury",
			Payee:  account,
			Amount: amount,
			Kind:   bankv1.TransferKind_TRANSFER_KIND_ISSUANCE,
			Tick:   tick,
		}))
		if err != nil || !res.Msg.GetOk() {
			slog.Warn("could not endow a workplace", "workplace", id, "err", err)
			continue
		}

		// The core keeps its own replica, and it will not accept an issuance
		// unless the transfer says that is what it is — the treasury's licence
		// to go negative is carried by the kind, not by its identity.
		if _, err := f.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
			Intent: &simv1.SubmitIntentRequest_Transfer{
				Transfer: &simv1.TransferIntent{
					PayerAccountId: "treasury",
					PayeeAccountId: account,
					// `amount`, not `capital`: the core's replica has to move by
					// exactly what the ledger moved, or the two disagree about
					// what the workplace can afford.
					Amount: amount,
					Kind:   simv1.TransferKind_TRANSFER_KIND_ISSUANCE,
					Tick:   tick,
				},
			},
		})); err != nil {
			slog.Warn("could not record an endowment in sim-core", "workplace", id, "err", err)
			continue
		}

		slog.Info("endowed a workplace",
			"workplace", id, "held", held, "issued", amount, "capital", capital)
	}
}

// endowmentFor answers how much to issue to a workplace holding `held`.
//
// Zero means leave it alone. Split out from Endow because it is the whole
// policy — the rest of that function is two RPCs — and because a rule about
// money is worth testing without standing up a bank.
func endowmentFor(held, capital int64) int64 {
	// Half, and the number is measured rather than chosen: two workplaces
	// paying ~26 working pips drain about 300 a second, so 10,000 lasts barely
	// thirty. A quarter-mark checked every 30s left them empty for the tail of
	// every window — the "insufficient funds" warnings did not stop, they
	// merely became periodic. Refilling from half, three times as often, keeps
	// the balance clear of zero between passes.
	//
	// It is still hysteresis and not a target: topping up on any shortfall at
	// all would issue a transfer every single pass.
	if held >= capital/2 {
		return 0
	}
	// Top up *to* capital rather than *by* it. Issuing the full amount to a
	// workplace that still holds some would inflate the supply a little on
	// every pass, and money supply in this project is meant to be closed.
	//
	// A negative balance is possible in principle — nothing stops a workplace
	// being overdrawn — and subtraction handles it: the shortfall is larger, so
	// the issuance is larger, and it lands at exactly `capital`.
	return capital - held
}

// KeepEndowed tops workplaces up for as long as ctx lives.
//
// Endowing once at startup was enough only while nothing spent the money. It
// is not a retry loop: the treasury has to keep refilling because escheat
// keeps draining, so this runs for the life of the process rather than until
// it first succeeds.
func (f *Fleet) KeepEndowed(ctx context.Context, capital int64, every time.Duration) {
	// The first pass is deferred: a workplace has to have registered and been
	// given an id before it can hold an account.
	t := time.NewTimer(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		f.Endow(ctx, capital)
		t.Reset(every)
	}
}
