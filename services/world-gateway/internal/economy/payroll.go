package economy

import (
	"context"
	"fmt"
	"log/slog"

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

// Buy runs one purchase end to end: ask the workplace for the price, move
// the money through the bank, then tell sim-core what the purchase does to
// the pip. Each step only proceeds once the previous one confirms — a
// workplace that declines, or a pip that cannot afford it, spends nothing.
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

	if f.bank == nil {
		return false, "bank unavailable", price, nil
	}
	transferRes, err := f.bank.Transfer(ctx, connect.NewRequest(&bankv1.TransferRequest{
		Payer:  pipAccount(pipID),
		Payee:  workplaceAccount(workplaceID),
		Amount: price,
		Kind:   bankv1.TransferKind_TRANSFER_KIND_PURCHASE,
		Tick:   tick,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnavailable {
			return false, "bank unavailable", price, nil
		}
		return false, "", price, err
	}
	if !transferRes.Msg.GetOk() {
		return false, transferRes.Msg.GetReason(), price, nil
	}

	if _, err := f.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_Transfer{
			Transfer: &simv1.TransferIntent{
				PayerAccountId: pipAccount(pipID),
				PayeeAccountId: workplaceAccount(workplaceID),
				Amount:         price,
				ResourceKind:   int32(kind),
				Tick:           tick,
			},
		},
	})); err != nil {
		slog.Warn("could not record purchase in sim-core", "pip", pipID, "workplace", workplaceID, "err", err)
	}

	return true, "", price, nil
}
