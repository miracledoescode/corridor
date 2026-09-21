package notify

import (
	"context"
	"log/slog"
	"time"
)

// Store is the alert state the dispatcher needs.
type Store interface {
	// ClaimUndispatchedAlerts atomically stamps dispatched_at on pending
	// alerts and returns them.
	ClaimUndispatchedAlerts(ctx context.Context, limit int) ([]Alert, error)
}

// Sender delivers a message to one chat.
type Sender interface {
	Send(ctx context.Context, chatID, text string) error
}

// Dispatcher pushes newly-detected alerts to subscribers.
type Dispatcher struct {
	store     Store
	sender    Sender
	proChats  []string
	watermark string
	log       *slog.Logger
}

func NewDispatcher(store Store, sender Sender, proChats []string, watermark string, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		store:     store,
		sender:    sender,
		proChats:  proChats,
		watermark: watermark,
		log:       log,
	}
}

// maxPerTick bounds one dispatch pass. A backlog (after downtime, say) drains
// over several ticks rather than firing hundreds of messages at once and
// tripping Telegram's rate limits.
const maxPerTick = 20

// Run dispatches every interval until ctx is cancelled.
//
// Failures are logged and never fatal, for the same reason the spread engine's
// are: this shares a process with ingestion, and ingestion is the thing that
// cannot be rebuilt.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	// WHY claiming stamps dispatched_at BEFORE the message is sent: front-
	// running policy is product law. Subscribers must be served before anyone
	// operating this system could act on an alert, and dispatched_at is the
	// record proving the order. Stamping first also makes delivery at-most-
	// once: if the send fails after the claim, that alert is dropped rather
	// than re-sent later, because a stale arb pushed minutes afterwards is
	// worse than no alert — the window is seconds, and a subscriber acting on
	// a resurrected one loses money.
	alerts, err := d.store.ClaimUndispatchedAlerts(ctx, maxPerTick)
	if err != nil {
		d.log.Error("claim alerts failed", "err", err)
		return
	}
	if len(alerts) == 0 {
		return
	}
	if len(d.proChats) == 0 {
		d.log.Warn("alerts claimed but no subscribers configured", "count", len(alerts))
		return
	}

	for _, a := range alerts {
		text := Format(a, d.watermark)
		for _, chat := range d.proChats {
			if err := d.sender.Send(ctx, chat, text); err != nil {
				// One bad chat id (blocked bot, deleted group) must not stop
				// delivery to everyone else.
				d.log.Error("alert send failed", "err", err, "alert_id", a.ID)
				continue
			}
		}
		d.log.Info("alert dispatched", "alert_id", a.ID, "net_edge", a.Payload.NetEdge)
	}
}
