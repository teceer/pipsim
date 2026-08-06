// Package bankapi translates pips.bank.v1.BankService onto the ledger.
//
// Nothing here decides anything about money — it validates shape, calls the
// Ledger, and reports facts. If bank ever grows an `if kind == "ale"`, the
// boundary from ADR 0006 has leaked: prices have one owner, the selling
// workplace, and this service only moves what it is told to move.
package bankapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	bankv1 "github.com/teceer/pipsim/gen/go/pips/bank/v1"
	"github.com/teceer/pipsim/gen/go/pips/bank/v1/bankv1connect"
	"github.com/teceer/pipsim/services/bank/internal/ledger"
)

// EventPublisher emits the facts a transfer produces. Optional: a bank with
// no broker configured still moves money correctly, it just cannot be
// watched doing it.
type EventPublisher interface {
	WagePaid(ctx context.Context, payer, payee string, amount int64, tick uint64)
	PurchaseMade(ctx context.Context, payer, payee string, amount int64, kind int32, tick uint64)
	AccountOverdrawn(ctx context.Context, account string, attempted, balance int64, tick uint64)
	MoneyIssued(ctx context.Context, payee string, amount int64, tick uint64)
}

// noopPublisher is used when no broker is configured. A bank must still work
// without Kafka reachable — the wage/purchase facts are an observability
// nicety, not a precondition for moving money.
type noopPublisher struct{}

func (noopPublisher) WagePaid(context.Context, string, string, int64, uint64)            {}
func (noopPublisher) PurchaseMade(context.Context, string, string, int64, int32, uint64) {}
func (noopPublisher) AccountOverdrawn(context.Context, string, int64, int64, uint64)     {}
func (noopPublisher) MoneyIssued(context.Context, string, int64, uint64)                 {}

var _ bankv1connect.BankServiceHandler = (*Handler)(nil)

type Handler struct {
	ledger ledger.Ledger
	events EventPublisher
}

func New(l ledger.Ledger, events EventPublisher) *Handler {
	if events == nil {
		events = noopPublisher{}
	}
	return &Handler{ledger: l, events: events}
}

func (h *Handler) OpenAccount(ctx context.Context, req *connect.Request[bankv1.OpenAccountRequest]) (*connect.Response[bankv1.OpenAccountResponse], error) {
	id := req.Msg.GetAccountId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errAccountIDRequired)
	}
	balance, err := h.ledger.OpenAccount(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&bankv1.OpenAccountResponse{
		Account: &bankv1.Account{Id: id, Balance: balance},
	}), nil
}

func (h *Handler) Transfer(ctx context.Context, req *connect.Request[bankv1.TransferRequest]) (*connect.Response[bankv1.TransferResponse], error) {
	m := req.Msg
	if m.GetPayer() == "" || m.GetPayee() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errAccountIDRequired)
	}

	kind := kindFromProto(m.GetKind())
	res, err := h.ledger.Transfer(ctx, m.GetPayer(), m.GetPayee(), m.GetAmount(), kind, m.GetTick())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	if !res.OK {
		h.events.AccountOverdrawn(ctx, m.GetPayer(), m.GetAmount(), res.PayerBalance, m.GetTick())
	} else {
		switch kind {
		case ledger.KindWage:
			h.events.WagePaid(ctx, m.GetPayer(), m.GetPayee(), m.GetAmount(), m.GetTick())
		case ledger.KindPurchase:
			h.events.PurchaseMade(ctx, m.GetPayer(), m.GetPayee(), m.GetAmount(), 0, m.GetTick())
		case ledger.KindIssuance:
			h.events.MoneyIssued(ctx, m.GetPayee(), m.GetAmount(), m.GetTick())
		}
	}

	return connect.NewResponse(&bankv1.TransferResponse{
		Ok:           res.OK,
		Reason:       res.Reason,
		PayerBalance: res.PayerBalance,
	}), nil
}

func (h *Handler) BatchTransfer(ctx context.Context, req *connect.Request[bankv1.BatchTransferRequest]) (*connect.Response[bankv1.BatchTransferResponse], error) {
	m := req.Msg
	if m.GetPayer() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errAccountIDRequired)
	}

	credits := make([]ledger.Credit, 0, len(m.GetCredits()))
	for _, c := range m.GetCredits() {
		credits = append(credits, ledger.Credit{Payee: c.GetPayee(), Amount: c.GetAmount()})
	}

	kind := kindFromProto(m.GetKind())
	res, err := h.ledger.BatchTransfer(ctx, m.GetPayer(), credits, kind, m.GetTick())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	if !res.OK {
		var total int64
		for _, c := range credits {
			total += c.Amount
		}
		h.events.AccountOverdrawn(ctx, m.GetPayer(), total, res.PayerBalance, m.GetTick())
	} else if kind == ledger.KindWage {
		for _, c := range credits {
			h.events.WagePaid(ctx, m.GetPayer(), c.Payee, c.Amount, m.GetTick())
		}
	}

	return connect.NewResponse(&bankv1.BatchTransferResponse{
		Ok:           res.OK,
		Reason:       res.Reason,
		PayerBalance: res.PayerBalance,
	}), nil
}

func (h *Handler) GetBalance(ctx context.Context, req *connect.Request[bankv1.GetBalanceRequest]) (*connect.Response[bankv1.GetBalanceResponse], error) {
	balance, err := h.ledger.GetBalance(ctx, req.Msg.GetAccountId())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&bankv1.GetBalanceResponse{Balance: balance}), nil
}

func kindFromProto(k bankv1.TransferKind) ledger.Kind {
	switch k {
	case bankv1.TransferKind_TRANSFER_KIND_WAGE:
		return ledger.KindWage
	case bankv1.TransferKind_TRANSFER_KIND_PURCHASE:
		return ledger.KindPurchase
	case bankv1.TransferKind_TRANSFER_KIND_ISSUANCE:
		return ledger.KindIssuance
	default:
		slog.Warn("transfer with unspecified kind")
		return ledger.Kind("")
	}
}

var errAccountIDRequired = errors.New("account id is required")
