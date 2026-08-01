package events

import (
	"fmt"
	"sync"
	"time"
)

// Broker turns engine-side blocking questions (host key, overwrite) into
// request/response over the event bus: Emit "prompt:<kind>" with a promptId,
// block the asking goroutine, resolve when the frontend answers. Timeout is
// a deny — never a silent accept.
type Broker struct {
	sink    Sink
	timeout time.Duration

	mu      sync.Mutex
	seq     int64
	waiting map[string]chan bool
}

func NewBroker(sink Sink, timeout time.Duration) *Broker {
	return &Broker{
		sink:    sink,
		timeout: timeout,
		waiting: make(map[string]chan bool),
	}
}

// Ask blocks the calling goroutine until the frontend resolves the prompt or
// the timeout elapses (deny).
func (b *Broker) Ask(kind string, payload map[string]any) bool {
	b.mu.Lock()
	b.seq++
	id := fmt.Sprintf("prompt-%d", b.seq)
	ch := make(chan bool, 1)
	b.waiting[id] = ch
	b.mu.Unlock()

	msg := map[string]any{"promptId": id}
	for k, v := range payload {
		msg[k] = v
	}
	b.sink.Emit("prompt:"+kind, msg)

	select {
	case answer := <-ch:
		return answer
	case <-time.After(b.timeout):
		b.drop(id)
		return false
	}
}

// Resolve answers a pending prompt; unknown or already-resolved ids are
// ignored (double-click safe).
func (b *Broker) Resolve(id string, answer bool) {
	b.mu.Lock()
	ch, ok := b.waiting[id]
	delete(b.waiting, id)
	b.mu.Unlock()
	if ok {
		ch <- answer
	}
}

func (b *Broker) drop(id string) {
	b.mu.Lock()
	delete(b.waiting, id)
	b.mu.Unlock()
}
