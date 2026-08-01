package events

import (
	"sync"
	"testing"
	"time"
)

// captureSink records emitted events for assertions.
type captureSink struct {
	mu     sync.Mutex
	events []struct {
		Name    string
		Payload any
	}
}

func (c *captureSink) Emit(name string, payload any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, struct {
		Name    string
		Payload any
	}{name, payload})
}

func TestAskResolvedTrue(t *testing.T) {
	// Arrange
	sink := &captureSink{}
	b := NewBroker(sink, time.Second)

	// Act — resolve from "the frontend" once the event lands
	done := make(chan bool)
	go func() { done <- b.Ask("hostkey", map[string]any{"host": "example.test"}) }()

	var id string
	deadline := time.After(time.Second)
	for id == "" {
		select {
		case <-deadline:
			t.Fatal("prompt event never emitted")
		default:
			sink.mu.Lock()
			if len(sink.events) > 0 {
				id = sink.events[0].Payload.(map[string]any)["promptId"].(string)
			}
			sink.mu.Unlock()
		}
	}
	b.Resolve(id, true)

	// Assert
	if answer := <-done; !answer {
		t.Fatal("Ask = false, want true after Resolve(true)")
	}
	if sink.events[0].Name != "prompt:hostkey" {
		t.Fatalf("event name = %q", sink.events[0].Name)
	}
}

func TestAskTimesOutToDeny(t *testing.T) {
	// Arrange
	b := NewBroker(&captureSink{}, 30*time.Millisecond)

	// Act
	answer := b.Ask("hostkey", nil)

	// Assert
	if answer {
		t.Fatal("timeout must deny, got accept")
	}
}

func TestResolveUnknownIDIsNoop(t *testing.T) {
	b := NewBroker(&captureSink{}, time.Second)
	b.Resolve("prompt-999", true) // must not panic or block
}
