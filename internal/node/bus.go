package node

import (
	"sync"

	"gonje/internal/logx"
	"gonje/internal/nje/link"
)

// Bus fans events out to subscribers (the shell, tests) and logs them.
// Publishing never blocks: a subscriber whose channel is full misses
// events (counted in Dropped).
type Bus struct {
	mu      sync.Mutex
	subs    map[int]chan link.Event
	next    int
	log     *logx.Logger
	Dropped int
}

// NewBus creates a bus that logs to log.
func NewBus(log *logx.Logger) *Bus { return &Bus{subs: map[int]chan link.Event{}, log: log} }

// Subscribe returns an id and a channel receiving all future events.
func (b *Bus) Subscribe(size int) (int, <-chan link.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan link.Event, size)
	b.next++
	b.subs[b.next] = ch
	return b.next, ch
}

// Unsubscribe stops delivery to id.
func (b *Bus) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, id)
}

// Publish logs ev and delivers it to every subscriber.
//
// Record dumps and keepalives are written to the log by the link layer
// itself (with the raw TTB blocks), so only the other events are logged
// here to avoid duplicates.
func (b *Bus) Publish(ev link.Event) {
	if ev.Kind != link.EvDump && ev.Kind != link.EvKeepalive {
		lv := logx.Info
		if ev.Kind == link.EvError {
			lv = logx.Error
		}
		msg := ev.Text
		if ev.Node != "" {
			msg = "[" + ev.Node + "] " + msg
		}
		lines := []string{msg}
		for _, l := range ev.Lines {
			lines = append(lines, "    "+l)
		}
		b.log.Lines(lv, lines) // each line is escaped by the logger

	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			b.Dropped++
		}
	}
}
