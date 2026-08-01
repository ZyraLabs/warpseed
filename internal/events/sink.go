package events

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Sink is the only path from engine code to the frontend. Engines depend on
// this interface, never on Wails — the v2→v3 migration (or a Tauri fallback)
// then touches this package alone. (Approved plan: EventSink facade.)
type Sink interface {
	Emit(event string, payload any)
}

// WailsSink emits over the Wails v2 event bus.
type WailsSink struct {
	ctx context.Context
}

func NewWailsSink(ctx context.Context) *WailsSink {
	return &WailsSink{ctx: ctx}
}

func (s *WailsSink) Emit(event string, payload any) {
	runtime.EventsEmit(s.ctx, event, payload)
}

// NullSink discards events; used in tests and before startup completes.
type NullSink struct{}

func (NullSink) Emit(string, any) {}
