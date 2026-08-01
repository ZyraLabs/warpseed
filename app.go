package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"warpseed/internal/creds"
	"warpseed/internal/engine/core"
	"warpseed/internal/engine/sftpfast"
	"warpseed/internal/events"
	"warpseed/internal/hostkeys"
	"warpseed/internal/localfs"
	"warpseed/internal/queue"
)

// App is the Bindings facade: the ONLY type exposed to the frontend, and
// (with internal/events) the only layer that touches the Wails API surface.
// Methods stay thin — they delegate to internal packages.
type App struct {
	ctx      context.Context
	sink     events.Sink
	broker   *events.Broker
	store    *queue.Store
	creds    creds.Store
	hostkeys *hostkeys.Store

	mu       sync.Mutex
	sessions map[int64]*sftpfast.Client // browse connection per connected site
}

func NewApp() *App {
	return &App{
		sink:     events.NullSink{},
		creds:    creds.Default(),
		sessions: make(map[int64]*sftpfast.Client),
	}
}

// startup wires services once the Wails runtime context exists.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.sink = events.NewWailsSink(ctx)
	a.broker = events.NewBroker(a.sink, 2*time.Minute)

	path, err := queue.DefaultPath()
	if err != nil {
		log.Printf("queue: resolve path: %v", err)
		return
	}
	store, err := queue.Open(path)
	if err != nil {
		// The app must still browse local files with a broken DB; transfer
		// features surface the error when used.
		log.Printf("queue: open %s: %v", path, err)
		a.sink.Emit("app:error", fmt.Sprintf("queue database unavailable: %v", err))
		return
	}
	a.store = store
	a.hostkeys = hostkeys.New(store.DB())
	if n, err := store.RecoverInterrupted(); err != nil {
		log.Printf("queue: recover: %v", err)
	} else if n > 0 {
		log.Printf("queue: requeued %d interrupted transfer(s)", n)
	}
}

func (a *App) shutdown(_ context.Context) {
	a.mu.Lock()
	for id, c := range a.sessions {
		c.Close()
		delete(a.sessions, id)
	}
	a.mu.Unlock()
	if a.store != nil {
		a.store.Close()
	}
}

var errNoStore = errors.New("queue database unavailable")

// --- Local filesystem bindings ---

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

// --- Site bindings ---

// Sites lists saved sites.
func (a *App) Sites() ([]queue.Site, error) {
	if a.store == nil {
		return nil, errNoStore
	}
	return a.store.Sites()
}

// SaveSite creates or updates a site. A non-empty password is stored in the
// platform credential store under a per-site reference; the DB row only ever
// holds that reference.
func (a *App) SaveSite(site queue.Site, password string) (queue.Site, error) {
	if a.store == nil {
		return queue.Site{}, errNoStore
	}
	id, err := a.store.SaveSite(site)
	if err != nil {
		return queue.Site{}, err
	}
	site.ID = id

	if password != "" {
		ref := fmt.Sprintf("site-%d", id)
		if err := a.creds.Set(ref, password); err != nil {
			return queue.Site{}, fmt.Errorf("store credential: %w", err)
		}
		if site.CredRef != ref {
			if err := a.store.SetSiteCredRef(id, ref); err != nil {
				return queue.Site{}, err
			}
			site.CredRef = ref
		}
	}
	return site, nil
}

// DeleteSite removes a site, its pinned host keys (FK cascade), its stored
// credential, and any live session.
func (a *App) DeleteSite(id int64) error {
	if a.store == nil {
		return errNoStore
	}
	site, err := a.store.SiteByID(id)
	if err != nil {
		return err
	}
	a.DisconnectSite(id)
	if site.CredRef != "" {
		if err := a.creds.Delete(site.CredRef); err != nil && !errors.Is(err, creds.ErrNotFound) {
			log.Printf("creds: delete %s: %v", site.CredRef, err)
		}
	}
	return a.store.DeleteSite(id)
}

// --- Session bindings (browse connection per site) ---

// ConnectSite dials the site's dedicated browse connection. Unknown host
// keys block on a frontend TOFU prompt; changed keys fail loudly.
func (a *App) ConnectSite(id int64) error {
	if a.store == nil {
		return errNoStore
	}
	a.mu.Lock()
	if _, ok := a.sessions[id]; ok {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	site, err := a.store.SiteByID(id)
	if err != nil {
		return err
	}
	password := ""
	if site.CredRef != "" {
		if p, err := a.creds.Get(site.CredRef); err == nil {
			password = p
		}
	}

	hostKeyCB := a.hostkeys.Callback(id, func(algo, fingerprint string) bool {
		return a.broker.Ask("hostkey", map[string]any{
			"siteId":      id,
			"host":        site.Host,
			"algo":        algo,
			"fingerprint": fingerprint,
		})
	})

	a.emitConnState(id, "connecting")
	client, err := sftpfast.Dial(a.ctx, sftpfast.Config{
		Host:     site.Host,
		Port:     site.Port,
		User:     site.Username,
		Password: password,
	}, hostKeyCB)
	if err != nil {
		a.emitConnState(id, "error")
		return fmt.Errorf("connect %s: %w", site.Name, err)
	}

	a.mu.Lock()
	a.sessions[id] = client
	a.mu.Unlock()
	a.emitConnState(id, "connected")
	return nil
}

// DisconnectSite closes the site's browse connection if open.
func (a *App) DisconnectSite(id int64) {
	a.mu.Lock()
	client, ok := a.sessions[id]
	if ok {
		delete(a.sessions, id)
	}
	a.mu.Unlock()
	if ok {
		client.Close()
		a.emitConnState(id, "disconnected")
	}
}

// ListRemote reads a remote directory over the site's browse connection.
func (a *App) ListRemote(id int64, path string) (core.Listing, error) {
	a.mu.Lock()
	client, ok := a.sessions[id]
	a.mu.Unlock()
	if !ok {
		return core.Listing{}, fmt.Errorf("site %d is not connected", id)
	}
	return client.List(path)
}

// ResolvePrompt answers a blocking engine prompt (host key, overwrite...).
func (a *App) ResolvePrompt(promptID string, answer bool) {
	if a.broker != nil {
		a.broker.Resolve(promptID, answer)
	}
}

func (a *App) emitConnState(siteID int64, state string) {
	a.sink.Emit("site:connstate", map[string]any{"siteId": siteID, "state": state})
}
