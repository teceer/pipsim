package bankapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/teceer/pipsim/gen/go/pips/events/v1"
	"github.com/teceer/pipsim/services/bank/internal/ledger"
)

const topicPurchases = "pipsim.economy.purchases.v1"

// BookPurchase records a purchase sim-core has already settled.
//
// The asymmetry with payroll is deliberate and is the whole point of ADR
// 0006's split. Payroll is gated here, because "can this workplace afford its
// wage bill" is a solvency question and solvency is the bank's. A purchase is
// gated by the core, because it is decided inside a tick against the pip's
// replica balance, and the core may not make a network call to ask us.
//
// Exactly one side gates each movement, and the other books it. That is what
// makes it impossible for the two to disagree about whether a transaction
// happened — the failure that had a pip pay for ale and receive nothing.
//
// `eventID` is the envelope's id, so a redelivered fact books once.
func BookPurchase(ctx context.Context, l ledger.Ledger, eventID string, p *eventsv1.PurchaseMade, tick uint64) error {
	if eventID == "" {
		// Without an id there is no idempotency key, and booking it would
		// double the pip's payment on the next redelivery.
		return errors.New("purchase event has no id")
	}
	return l.Post(ctx, eventID, p.GetPayer(), p.GetPayee(), p.GetAmount(), ledger.KindPurchase, tick)
}

// PurchaseConsumer books the purchases sim-core reports.
type PurchaseConsumer struct {
	reader *kafka.Reader
	ledger ledger.Ledger
}

func NewPurchaseConsumer(brokers []string, l ledger.Ledger) *PurchaseConsumer {
	return &PurchaseConsumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:     brokers,
			Topic:       topicPurchases,
			GroupID:     "bank-purchases",
			MaxWait:     time.Second,
			StartOffset: kafka.FirstOffset,
		}),
		ledger: l,
	}
}

func (c *PurchaseConsumer) Close() error { return c.reader.Close() }

// Run consumes until the context is cancelled. The offset is committed only
// after the booking succeeds — a bank that dies mid-write re-reads the message
// and books an operation that is idempotent on the event id.
func (c *PurchaseConsumer) Run(ctx context.Context) {
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("could not read a purchase event", "err", err)
			continue
		}

		if err := c.handle(ctx, msg.Value); err != nil {
			slog.Warn("could not book a purchase", "err", err)
			continue
		}

		if err := c.reader.CommitMessages(ctx, msg); err != nil && ctx.Err() == nil {
			slog.Warn("could not commit a purchase offset", "err", err)
		}
	}
}

func (c *PurchaseConsumer) handle(ctx context.Context, value []byte) error {
	var envelope eventsv1.EventEnvelope
	if err := proto.Unmarshal(value, &envelope); err != nil {
		// Dropped rather than retried: it will not parse next time either,
		// and blocking the partition would stop every later purchase from
		// reaching the journal.
		slog.Warn("could not decode a purchase envelope", "err", err)
		return nil
	}

	purchase := envelope.GetPurchaseMade()
	if purchase == nil {
		return nil
	}
	return BookPurchase(ctx, c.ledger, envelope.GetEventId(), purchase, envelope.GetTick())
}
