// Work allocation over RabbitMQ.
//
// The gateway offers unemployed pips to the `pipsim.work` exchange and listens
// for outcomes on `pipsim.work.hired`. It does not ask a workplace whether it
// has room — it publishes an offer and lets whichever replica has capacity take
// it. That is the difference between this bus and the other two: an offer is a
// task claimed by exactly one consumer, not a fact broadcast to all of them.
//
// The payoff is not decoration. Direct RPC forced this driver to be a
// singleton, which is why world-gateway had to drop to one replica: two
// drivers polling the same workplace would both see a free position and both
// hire into it. With allocation on a queue the broker arbitrates instead.

package economy

import (
	"context"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"google.golang.org/protobuf/proto"

	workv1 "github.com/teceer/pipsim/gen/go/pips/work/v1"
)

const (
	exchange    = "pipsim.work"
	hiredQueue  = "pipsim.work.hired"
	offerPrefix = "offer."
	// The naming convention every workplace's consumer follows — see e.g.
	// services/workplaces/farm/internal/queue: offerQueue = "pipsim.work.farm".
	// Restated here rather than imported, because a Go workplace consumer is
	// not a dependency the gateway should have — this is the one place the
	// convention has to be known on both sides of the queue.
	workQueuePrefix = "pipsim.work."

	// Offers published per round.
	//
	// Flow control, not policy. Every unemployed pip could be offered every
	// round, but when the workplaces are full that is dozens of messages a
	// second whose only outcome is a rejection. Trickling them keeps the queue
	// readable and still fills a vacancy within seconds of it opening.
	maxOffersPerRound = 8
)

// Publisher sends offers and reads outcomes back.
type Publisher struct {
	conn *amqp.Connection
	ch   *amqp.Channel
	// A channel of its own, never touched by Offer or ConsumeOutcomes: an
	// amqp.Channel is not safe for concurrent use, and QueueDepth is called
	// from the metrics callback on its own goroutine.
	metricsCh *amqp.Channel
}

func Dial(url string) (*Publisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, err
	}
	metricsCh, err := conn.Channel()
	if err != nil {
		ch.Close()
		conn.Close()
		return nil, err
	}
	return &Publisher{conn: conn, ch: ch, metricsCh: metricsCh}, nil
}

func (p *Publisher) Close() {
	if p.metricsCh != nil {
		_ = p.metricsCh.Close()
	}
	if p.ch != nil {
		_ = p.ch.Close()
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
}

// QueueDepth reports how many offers are waiting for kind's workplace —
// pipsim.economy.offers_pending. A passive inspect, never a declare: the
// queue belongs to the consumer side, and this must not create it.
func (p *Publisher) QueueDepth(kind string) (int, error) {
	q, err := p.metricsCh.QueueInspect(workQueuePrefix + kind)
	if err != nil {
		// A failed passive inspect — e.g. the workplace has not declared its
		// queue yet — closes the AMQP channel it was issued on, per the
		// protocol. Reopen so the next call is not doomed to repeat this.
		if ch, cerr := p.conn.Channel(); cerr == nil {
			p.metricsCh = ch
		}
		return 0, err
	}
	return q.Messages, nil
}

func (p *Publisher) Offer(ctx context.Context, kind string, pipID, tick uint64, traceID string) error {
	body, err := proto.Marshal(&workv1.WorkOffer{
		PipId:   pipID,
		Tick:    tick,
		TraceId: traceID,
	})
	if err != nil {
		return err
	}

	return p.ch.PublishWithContext(ctx, exchange, offerPrefix+kind, false, false, amqp.Publishing{
		ContentType: "application/x-protobuf",
		Body:        body,
		// Offers describe a world that moves on. One nobody claimed within a
		// few seconds is about a pip that has probably found work or died, and
		// acting on it later would be worse than dropping it.
		Expiration: "10000",
	})
}

// ConsumeOutcomes calls onHired for every accepted outcome until ctx is done.
//
// Rejections are counted and dropped: a full workplace declining an offer is
// the system working, and turning that into a retry loop would just be polling
// with extra steps.
func (p *Publisher) ConsumeOutcomes(ctx context.Context, onHired func(pipID, workplaceID uint64)) error {
	deliveries, err := p.ch.Consume(hiredQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	slog.Info("consuming hire outcomes", "queue", hiredQueue)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return nil
			}

			var outcome workv1.HireOutcome
			if err := proto.Unmarshal(d.Body, &outcome); err != nil {
				slog.Error("undecodable outcome, dead-lettering", "err", err)
				_ = d.Reject(false)
				continue
			}

			if outcome.GetAccepted() {
				onHired(outcome.GetPipId(), outcome.GetWorkplaceId())
			}
			_ = d.Ack(false)
		}
	}
}

// RunOutcomes keeps ConsumeOutcomes alive across broker restarts.
func RunOutcomes(ctx context.Context, url string, onHired func(pipID, workplaceID uint64)) {
	for ctx.Err() == nil {
		pub, err := Dial(url)
		if err == nil {
			err = pub.ConsumeOutcomes(ctx, onHired)
			pub.Close()
		}
		if ctx.Err() != nil {
			return
		}
		slog.Warn("outcome consumer ended, retrying", "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}
