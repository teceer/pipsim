package bankapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/teceer/pipsim/gen/go/pips/events/v1"
	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
)

const topicMoney = "pipsim.economy.money.v1"

// KafkaEvents publishes WagePaid/PurchaseMade/AccountOverdrawn/MoneyIssued
// facts. Fire-and-forget, like sim-core's own publisher (see
// crates/server/src/events.rs): losing one of these is a monitoring problem,
// not a reason to fail a transfer that already committed.
type KafkaEvents struct {
	writer *kafka.Writer
}

func NewKafkaEvents(brokers []string) *KafkaEvents {
	return &KafkaEvents{
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(brokers...),
			Topic:                  topicMoney,
			Balancer:               &kafka.Hash{},
			AllowAutoTopicCreation: true,
		},
	}
}

func (k *KafkaEvents) Close() error { return k.writer.Close() }

func (k *KafkaEvents) publish(ctx context.Context, key string, tick uint64, payload isPayload) {
	envelope := &eventsv1.EventEnvelope{
		EventId:    uuid.NewString(),
		Tick:       tick,
		OccurredAt: timestamppb.Now(),
		Producer:   "bank",
	}
	setPayload(envelope, payload)

	buf, err := proto.Marshal(envelope)
	if err != nil {
		slog.Error("could not encode money event envelope", "err", err)
		return
	}

	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := k.writer.WriteMessages(writeCtx, kafka.Message{Key: []byte(key), Value: buf}); err != nil {
		slog.Warn("could not publish money event", "topic", topicMoney, "err", err)
	}
}

// isPayload exists only so publish() can take any of the four event structs
// without repeating the marshal/publish plumbing per type.
type isPayload interface{}

func setPayload(e *eventsv1.EventEnvelope, payload isPayload) {
	switch p := payload.(type) {
	case *eventsv1.WagePaid:
		e.Payload = &eventsv1.EventEnvelope_WagePaid{WagePaid: p}
	case *eventsv1.PurchaseMade:
		e.Payload = &eventsv1.EventEnvelope_PurchaseMade{PurchaseMade: p}
	case *eventsv1.AccountOverdrawn:
		e.Payload = &eventsv1.EventEnvelope_AccountOverdrawn{AccountOverdrawn: p}
	case *eventsv1.MoneyIssued:
		e.Payload = &eventsv1.EventEnvelope_MoneyIssued{MoneyIssued: p}
	}
}

func (k *KafkaEvents) WagePaid(ctx context.Context, payer, payee string, amount int64, tick uint64) {
	k.publish(ctx, payee, tick, &eventsv1.WagePaid{Payer: payer, Payee: payee, Amount: amount})
}

// PurchaseMade's `kind` is always RESOURCE_KIND_UNSPECIFIED here: the bank
// only ever sees "this was a purchase-kind transfer," never what was bought
// — that's owned by the selling workplace, not the ledger. A consumer that
// wants the resource kind reads it off the workplace's own ResourceProduced
// fact for the same tick instead.
func (k *KafkaEvents) PurchaseMade(ctx context.Context, payer, payee string, amount int64, kind int32, tick uint64) {
	k.publish(ctx, payer, tick, &eventsv1.PurchaseMade{
		Payer:  payer,
		Payee:  payee,
		Amount: amount,
		Kind:   workplacev1.ResourceKind(kind),
	})
}

func (k *KafkaEvents) AccountOverdrawn(ctx context.Context, account string, attempted, balance int64, tick uint64) {
	k.publish(ctx, account, tick, &eventsv1.AccountOverdrawn{
		Account:         account,
		AttemptedAmount: attempted,
		Balance:         balance,
	})
}

func (k *KafkaEvents) MoneyIssued(ctx context.Context, payee string, amount int64, tick uint64) {
	k.publish(ctx, payee, tick, &eventsv1.MoneyIssued{Payee: payee, Amount: amount})
}
