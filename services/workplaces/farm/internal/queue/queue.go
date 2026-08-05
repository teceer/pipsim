// Package queue consumes work offers from RabbitMQ.
//
// This is the bus doing the job it was chosen for. Several farm replicas share
// the queue `pipsim.work.farm`, so an offer is delivered to exactly one of them
// and acknowledged individually — competing consumers. A Kafka consumer group
// would fan the same offer to every partition reader, and a log is the wrong
// shape for "somebody, anybody, take this one".
//
// The workplace never calls sim-core. It answers on `pipsim.work.hired` and the
// gateway turns that into an intent, which keeps the rule that a workplace
// holds no pip state and speaks to the world only through the gateway.
package queue

import (
	"context"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"google.golang.org/protobuf/proto"

	workv1 "github.com/teceer/pipsim/gen/go/pips/work/v1"
)

const (
	exchange   = "pipsim.work"
	hiredKey   = "hired"
	offerQueue = "pipsim.work.farm"

	// More than one offer in flight per replica, but not many. A large prefetch
	// would let one replica hoard offers it has no capacity for while another
	// sits idle — the opposite of what competing consumers are for.
	prefetch = 4
)

// Decider is the workplace's own logic. Returning false with a reason is a
// normal outcome, not an error: a full farm declining an offer is the system
// working.
type Decider func(ctx context.Context, pipID, tick uint64) (accepted bool, reason string)

type Consumer struct {
	url         string
	workplaceID uint64
	kind        string
	decide      Decider
}

func NewConsumer(url string, workplaceID uint64, kind string, decide Decider) *Consumer {
	return &Consumer{url: url, workplaceID: workplaceID, kind: kind, decide: decide}
}

// Run consumes until the context is cancelled, reconnecting on failure.
//
// A broker restart should not need a pod restart; the workplace simply stops
// being offered work until the connection is back.
func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.session(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("amqp session ended, retrying", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (c *Consumer) session(ctx context.Context) error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	if err := ch.Qos(prefetch, 0, false); err != nil {
		return err
	}

	// autoAck false: the point of this bus is that an offer is only gone once
	// the consumer says it handled it. A replica that dies mid-decision must
	// have its offer redelivered to someone else.
	deliveries, err := ch.Consume(offerQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	slog.Info("consuming work offers", "queue", offerQueue, "prefetch", prefetch)
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-closed:
			return err
		case d, ok := <-deliveries:
			if !ok {
				return nil
			}
			c.handle(ctx, ch, d)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, ch *amqp.Channel, d amqp.Delivery) {
	var offer workv1.WorkOffer
	if err := proto.Unmarshal(d.Body, &offer); err != nil {
		// Unparseable: never redeliver it, or it loops forever. The dead-letter
		// exchange on this queue is where it goes to be noticed.
		slog.Error("undecodable offer, dead-lettering", "err", err)
		_ = d.Reject(false)
		return
	}

	accepted, reason := c.decide(ctx, offer.GetPipId(), offer.GetTick())

	outcome, err := proto.Marshal(&workv1.HireOutcome{
		PipId:         offer.GetPipId(),
		WorkplaceId:   c.workplaceID,
		WorkplaceKind: c.kind,
		Accepted:      accepted,
		Reason:        reason,
		Tick:          offer.GetTick(),
		TraceId:       offer.GetTraceId(),
	})
	if err != nil {
		_ = d.Reject(false)
		return
	}

	if err := ch.PublishWithContext(ctx, exchange, hiredKey, false, false, amqp.Publishing{
		ContentType: "application/x-protobuf",
		Body:        outcome,
	}); err != nil {
		// The shift may already be recorded locally, but nobody has been told.
		// Requeue so another replica — or this one, later — can answer, and let
		// the lease reap the local shift if it never gets confirmed.
		slog.Warn("could not publish outcome, requeueing offer", "err", err)
		_ = d.Nack(false, true)
		return
	}

	_ = d.Ack(false)
}
