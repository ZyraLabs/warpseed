package main

import (
	"context"
	"fmt"
	"log"

	"warpseed/internal/events"
	"warpseed/internal/localfs"
	"warpseed/internal/queue"
)

// App is the Bindings facade: the ONLY type exposed to the frontend, and
// (with internal/events) the only file that touches the Wails API surface.
// Keep methods thin — they delegate to internal packages.
type App struct {
	ctx   context.Context
	sink  events.Sink
	store *queue.Store
}

func NewApp() *App {
	return &App{sink: events.NullSink{}}
}

// startup wires services once the Wails runtime context exists.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.sink = events.NewWailsSink(ctx)

	path, err := queue.DefaultPath()
	if err != nil {
		log.Printf("queue: resolve path: %v", err)
		return
	}
	store, err := queue.Open(path)
	if err != nil {
		// The app must still browse local files with a broken DB; transfer
		// features will surface the error when they arrive in Phase 1.
		log.Printf("queue: open %s: %v", path, err)
		a.sink.Emit("app:error", fmt.Sprintf("queue database unavailable: %v", err))
		return
	}
	a.store = store
	if n, err := store.RecoverInterrupted(); err != nil {
		log.Printf("queue: recover: %v", err)
	} else if n > 0 {
		log.Printf("queue: requeued %d interrupted transfer(s)", n)
	}
}

func (a *App) shutdown(_ context.Context) {
	if a.store != nil {
		a.store.Close()
	}
}

// --- Local filesystem bindings (Phase 0: local-only dual-pane shell) ---

// ListLocal returns a sorted directory listing.
func (a *App) ListLocal(path string) (localfs.Listing, error) {
	return localfs.List(path)
}

// LocalRoots returns top-level navigation targets (drives on Windows).
func (a *App) LocalRoots() []localfs.Root {
	return localfs.Roots()
}

// LocalHome returns the user's home directory.
func (a *App) LocalHome() (string, error) {
	return localfs.Home()
}

// SchemaVersion lets the frontend show DB health in the status bar.
func (a *App) SchemaVersion() int {
	if a.store == nil {
		return 0
	}
	v, err := a.store.SchemaVersion()
	if err != nil {
		return 0
	}
	return v
}
