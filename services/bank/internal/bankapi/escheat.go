package bankapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/teceer/pipsim/gen/go/pips/events/v1"
	"github.com/teceer/pipsim/services/bank/internal/ledger"
)

const topicLifecycle = "pipsim.pip.lifecycle.v1"

// Escheat moves a dead pip's balance back to the treasury.
//
// Money supply is closed (ADR 0006): the sum of every account is constant
// except at an explicit issuance. An account whose holder no longer exists
// would hold that money forever, and since starvation is the ordinary way a
// pip leaves the world, the supply would silently strand a purse per death.
//
// sim-core does the same to its replica inside `remove_at`, off the same
// fact. Neither side tells the other what the balance was — each returns the
// number it holds — so a divergence between them shows up as a mismatch the
// reconciliation test can see, rather than being papered over by one side
// dictating to the other.
//
// Idempotent by construction: the transfer's key is
// (pip account, treasury, tick, ESCHEAT), and a pip dies exactly once, so a
// redelivered PipDied re-runs it to no effect. The zero-balance early return
// makes the common redelivery case free.
func Escheat(ctx context.Context, l ledger.Ledger, pipID, tick uint64) error {
	account := fmt.Sprintf("pip:%d", pipID)

	balance, err := l.GetBalance(ctx, account)
	if err != nil {
		return err
	}
	if balance <= 0 {
		// Nothing to reclaim. A negative balance is impossible for a pip —
		// only the treasury may go negative — so this is purely the
		// "died broke" case, which is most of them.
		return nil
	}

	res, err := l.Transfer(ctx, account, ledger.TreasuryAccount, balance, ledger.KindEscheat, tick)
	if err != nil {
		return err
	}
	if !res.OK {
		// The balance was read a moment ago and a dead pip cannot spend, so
		// this should be unreachable. Log rather than retry: a wrong guess
		// about why would move money a second time.
		slog.Warn("escheat rejected", "pip", pipID, "amount", balance, "reason", res.Reason)
	}
	return nil
}

// LifecycleConsumer reclaims the balances of pips that have died.
//
// A consumer rather than an RPC from the gateway: the death is a fact that
// already happened and it concerns whoever cares (ADR 0002), and the bank is
// the one service that owns the closed-supply invariant. Making the gateway
// responsible for calling us would put the invariant in the hands of a
// service that has no stake in it.
type LifecycleConsumer struct {
	reader *kafka.Reader
	ledger ledger.Ledger
}

func NewLifecycleConsumer(brokers []string, l ledger.Ledger) *LifecycleConsumer {
	return &LifecycleConsumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers: brokers,
			Topic:   topicLifecycle,
			// A group, so that scaling the bank does not have every replica
			// reclaiming the same purse. The transfer is idempotent anyway,
			// but relying on that for correctness under normal operation
			// would be using the safety net as the floor.
			GroupID:     "bank-escheat",
			MaxWait:     time.Second,
			StartOffset: kafka.FirstOffset,
		}),
		ledger: l,
	}
}

func (c *LifecycleConsumer) Close() error { return c.reader.Close() }

// Run consumes until the context is cancelled.
//
// The offset is committed only after the escheat succeeds, so a bank that
// dies mid-transfer re-reads the message and reruns an idempotent operation.
// The reverse order would lose a purse on exactly the crash this is meant to
// survive.
func (c *LifecycleConsumer) Run(ctx context.Context) {
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("could not read a lifecycle event", "err", err)
			continue
		}

		if err := c.handle(ctx, msg.Value); err != nil {
			// Not committed: it will be redelivered. Better a repeat of an
			// idempotent transfer than a balance stranded on a dead account.
			slog.Warn("could not handle a lifecycle event", "err", err)
			continue
		}

		if err := c.reader.CommitMessages(ctx, msg); err != nil && ctx.Err() == nil {
			slog.Warn("could not commit a lifecycle offset", "err", err)
		}
	}
}

func (c *LifecycleConsumer) handle(ctx context.Context, value []byte) error {
	var envelope eventsv1.EventEnvelope
	if err := proto.Unmarshal(value, &envelope); err != nil {
		// Undecodable payloads are dropped rather than retried forever — a
		// message that cannot be parsed will not parse on the next attempt
		// either, and blocking the partition on it would stop every later
		// death from being reclaimed.
		slog.Warn("could not decode a lifecycle envelope", "err", err)
		return nil
	}

	died := envelope.GetPipDied()
	if died == nil {
		return nil
	}
	return Escheat(ctx, c.ledger, died.GetPipId(), envelope.GetTick())
}
