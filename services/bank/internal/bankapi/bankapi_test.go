package bankapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	bankv1 "github.com/teceer/pipsim/gen/go/pips/bank/v1"
	"github.com/teceer/pipsim/services/bank/internal/ledger"
)

func TestTransferChargesTheAdvertisedAmount(t *testing.T) {
	h := New(ledger.NewMemory(), nil)
	ctx := context.Background()

	issue, err := h.Transfer(ctx, connect.NewRequest(&bankv1.TransferRequest{
		Payer: ledger.TreasuryAccount, Payee: "pip:1", Amount: 100, Kind: bankv1.TransferKind_TRANSFER_KIND_ISSUANCE, Tick: 1,
	}))
	if err != nil || !issue.Msg.GetOk() {
		t.Fatalf("issuance failed: %+v, %v", issue, err)
	}

	res, err := h.Transfer(ctx, connect.NewRequest(&bankv1.TransferRequest{
		Payer: "pip:1", Payee: "workplace:9", Amount: 40, Kind: bankv1.TransferKind_TRANSFER_KIND_PURCHASE, Tick: 2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Msg.GetOk() || res.Msg.GetPayerBalance() != 60 {
		t.Fatalf("unexpected result: %+v", res.Msg)
	}

	balance, err := h.GetBalance(ctx, connect.NewRequest(&bankv1.GetBalanceRequest{AccountId: "workplace:9"}))
	if err != nil {
		t.Fatal(err)
	}
	if balance.Msg.GetBalance() != 40 {
		t.Fatalf("expected 40, got %d", balance.Msg.GetBalance())
	}
}

func TestTransferRejectsEmptyAccountIDs(t *testing.T) {
	h := New(ledger.NewMemory(), nil)
	_, err := h.Transfer(context.Background(), connect.NewRequest(&bankv1.TransferRequest{
		Payer: "", Payee: "pip:1", Amount: 1,
	}))
	if err == nil {
		t.Fatal("expected an error for an empty payer")
	}
}

func TestBatchTransferPaysEveryCredit(t *testing.T) {
	h := New(ledger.NewMemory(), nil)
	ctx := context.Background()

	_, _ = h.Transfer(ctx, connect.NewRequest(&bankv1.TransferRequest{
		Payer: ledger.TreasuryAccount, Payee: "workplace:9", Amount: 100, Kind: bankv1.TransferKind_TRANSFER_KIND_ISSUANCE, Tick: 1,
	}))

	res, err := h.BatchTransfer(ctx, connect.NewRequest(&bankv1.BatchTransferRequest{
		Payer: "workplace:9",
		Credits: []*bankv1.BatchTransferRequest_Credit{
			{Payee: "pip:1", Amount: 30},
			{Payee: "pip:2", Amount: 20},
		},
		Kind: bankv1.TransferKind_TRANSFER_KIND_WAGE,
		Tick: 2,
	}))
	if err != nil || !res.Msg.GetOk() {
		t.Fatalf("batch transfer failed: %+v, %v", res, err)
	}

	b1, _ := h.GetBalance(ctx, connect.NewRequest(&bankv1.GetBalanceRequest{AccountId: "pip:1"}))
	b2, _ := h.GetBalance(ctx, connect.NewRequest(&bankv1.GetBalanceRequest{AccountId: "pip:2"}))
	if b1.Msg.GetBalance() != 30 || b2.Msg.GetBalance() != 20 {
		t.Fatalf("unexpected balances: pip:1=%d pip:2=%d", b1.Msg.GetBalance(), b2.Msg.GetBalance())
	}
}
