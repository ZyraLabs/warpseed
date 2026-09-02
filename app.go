package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"warpseed/internal/applog"
	"warpseed/internal/creds"
	"warpseed/internal/dispatch"
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
	ctx        context.Context
	sink       events.Sink
	broker     *events.Broker
	store      *queue.Store
	creds      creds.Store
	hostkeys   *hostkeys.Store
	dispatcher *dispatch.Dispatcher
	logw       *applog.Writer

	mu       sync.Mutex
	sessions map[int64]*sftpfast.Client // browse connection per connected site

	mini         bool // window currently shrunk to the pill
	miniW, miniH int  // window geometry to restore when mini mode ends
}

func NewApp() *App {
	return &App{
		sink:     events.NullSink{},
		creds:    creds.Default(),
		sessions: make(map[int64]*sftpfast.Client),
	}
}

// appVersion is stamped into the log so a pasted excerpt identifies its build.
// Kept in step with wails.json productVersion, which the release workflow
// checks against the tag.
const appVersion = "1.1.0"

// shutdownGrace is how long a close waits for in-flight transfers to record
// their final state. Long enough for a checkpoint write to land, short enough
// that closing never feels stuck.
const shutdownGrace = 3 * time.Second

// startup wires services once the Wails runtime context exists.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.sink = events.NewWailsSink(ctx)
	a.broker = events.NewBroker(a.sink, 2*time.Minute)

	// Redirect the standard logger to a file first, before anything below can
	// log. A Wails GUI build has no console, so until this line every
	// log.Printf in the codebase went to a stderr nobody could read.
	if dir, derr := configDir(); derr == nil {
		if w, lerr := applog.Open(dir, "warpseed.log"); lerr == nil {
			a.logw = w
			log.SetOutput(w)
			log.SetFlags(log.LstdFlags | log.Lmsgprefix)
			log.Printf("warpseed %s starting", appVersion)
		}
		// A logger we cannot open is not worth failing to start over; stderr
		// stays the fallback exactly as before.
	}

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

	a.dispatcher = dispatch.New(store, a.sink, a.dialTransfers)
	go a.dispatcher.Run(ctx)
}

// dialTransfers opens n dedicated data connections for one transfer.
// Unknown host keys are DENIED here (nil prompt): pins are established by
// the interactive browse connect, so the queue can never silently trust a
// host. Dials are staggered — a burst of parallel connections trips
// OpenSSH's MaxStartups and gets dropped probabilistically.
func (a *App) dialTransfers(ctx context.Context, siteID int64, n int) ([]*sftpfast.Client, error) {
	if n < 1 {
		n = 1
	}
	site, err := a.store.SiteByID(siteID)
	if err != nil {
		return nil, err
	}
	password := ""
	if site.CredRef != "" {
		if p, err := a.creds.Get(site.CredRef); err == nil {
			password = p
		}
	}
	cfg := sftpfast.Config{
		Host:     site.Host,
		Port:     site.Port,
		User:     site.Username,
		Password: password,
	}
	hostKey := a.hostkeys.Callback(siteID, nil)

	clients := make([]*sftpfast.Client, 0, n)
	for i := 0; i < n; i++ {
		// Cancellation is checked BETWEEN dials only. Aborting a connection
		// that is open but has not yet authenticated is exactly what
		// OpenSSH's PerSourcePenalties (on by default since 9.8) punishes —
		// enough of them and the server refuses this IP for minutes.
		if ctx.Err() != nil {
			break
		}
		if i > 0 {
			time.Sleep(dialStagger)
			if ctx.Err() != nil {
				break
			}
		}

		// Cap concurrent handshakes process-wide: the server's MaxStartups
		// counter is global, and several transfers may be dialling at once.
		dialGate <- struct{}{}
		c, derr := sftpfast.Dial(ctx, cfg, hostKey)
		<-dialGate

		if derr != nil {
			// The first connection is mandatory; refusals beyond it just
			// mean the server won't grant more, so run with what we have
			// rather than retrying into a penalty.
			if i == 0 {
				return nil, derr
			}
			log.Printf("dispatch: site %d granted %d/%d connections: %v", siteID, i, n, derr)
			break
		}
		clients = append(clients, c)
	}
	if len(clients) == 0 {
		return nil, ctx.Err()
	}
	return clients, nil
}

// dialGate bounds concurrent SSH handshakes across the whole app. OpenSSH's
// MaxStartups counter is global (default 10 unauthenticated connections), so
// tripping it drops connections server-side and can look like an attack to
// fail2ban. 8 is mscp's published choice against the same default.
var dialGate = make(chan struct{}, 8)

// dialStagger spaces connection setups so a burst never reads as a scan.
const dialStagger = 120 * time.Millisecond

func closeAll(clients []*sftpfast.Client) {
	for _, c := range clients {
		c.Close()
	}
}

func (a *App) shutdown(_ context.Context) {
	// Stop the dispatcher and give in-flight transfers a moment to record
	// their final state BEFORE the database closes. Without this, a transfer
	// that finished microseconds earlier loses its "completed" write, stays
	// 'active', and RecoverInterrupted requeues it on next launch — but the
	// successful run already renamed its .wspart away, so there is nothing to
	// resume from and the whole file transfers again from byte zero.
	if a.dispatcher != nil {
		a.dispatcher.Stop(shutdownGrace)
	}
	a.mu.Lock()
	for id, c := range a.sessions {
		c.Close()
		delete(a.sessions, id)
	}
	a.mu.Unlock()
	if a.store != nil {
		a.store.Close()
	}
	if a.logw != nil {
		log.SetOutput(os.Stderr) // nothing may write to a closed file
		a.logw.Close()
	}
}

// LogDir opens the folder holding warpseed.log in Explorer, so a bug report
// can carry the log without the user hunting through %APPDATA%.
func (a *App) LogDir() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return dir, openInFileManager(dir)
}

// configDir is where the database and the log live, side by side.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(base, "warpseed"), nil
}

var errNoStore = errors.New("queue database unavailable")

// safeLocalName reduces a server-supplied name to a single local path
// element. Remote names are attacker-controlled: a POSIX filename may legally
// contain '\' or "..", both of which Windows treats as path syntax, so
// path.Base alone cannot keep a download inside its destination directory.
func safeLocalName(remote string) (string, error) {
	name := path.Base(remote)
	name = strings.ReplaceAll(name, `\`, "_")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." || !filepath.IsLocal(name) {
		return "", fmt.Errorf("refusing unsafe remote file name %q", remote)
	}
	return name, nil
}

// safeLocalJoin places a remote-relative path under root, refusing anything
// that escapes it.
func safeLocalJoin(root, relative string) (string, error) {
	rel := filepath.FromSlash(relative)
	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("refusing unsafe remote path %q", relative)
	}
	return filepath.Join(root, rel), nil
}

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

// DiskSpace reports free/total bytes of the volume holding path (Deck view).
func (a *App) DiskSpace(path string) (localfs.Space, error) {
	return localfs.DiskSpace(path)
}

// SetMiniMode shrinks the window to an always-on-top ambient pill, or
// restores the previous geometry. The min-size clamp must move first —
// the 900×560 floor from launch would otherwise swallow the shrink.
func (a *App) SetMiniMode(on bool) {
	if a.ctx == nil {
		return
	}
	if on {
		a.mu.Lock()
		if a.mini { // re-entrant call: the captured size would be the pill's own
			a.mu.Unlock()
			return
		}
		a.mini = true
		a.mu.Unlock()
		w, h := wruntime.WindowGetSize(a.ctx)
		a.mu.Lock()
		a.miniW, a.miniH = w, h
		a.mu.Unlock()
		wruntime.WindowSetMinSize(a.ctx, 320, 96)
		wruntime.WindowSetSize(a.ctx, 380, 96)
		wruntime.WindowSetAlwaysOnTop(a.ctx, true)
		return
	}
	wruntime.WindowSetAlwaysOnTop(a.ctx, false)
	wruntime.WindowSetMinSize(a.ctx, 900, 560)
	a.mu.Lock()
	a.mini = false
	w, h := a.miniW, a.miniH
	a.mu.Unlock()
	if w < 900 || h < 560 {
		w, h = 1280, 800
	}
	wruntime.WindowSetSize(a.ctx, w, h)
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
	// Updates must never silently drop fields the caller didn't send —
	// blanking cred_ref would orphan the stored credential and break connect.
	if site.ID != 0 {
		if prev, err := a.store.SiteByID(site.ID); err == nil {
			if site.CredRef == "" {
				site.CredRef = prev.CredRef
			}
			if site.OptionsJSON == "" {
				site.OptionsJSON = prev.OptionsJSON
			}
		}
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
	existing := a.sessions[id]
	a.mu.Unlock()
	if existing != nil {
		if existing.Alive(3 * time.Second) {
			return nil
		}
		// The session died underneath us (idle timeout, network change).
		// Evict it so this call falls through to a fresh dial — otherwise
		// reconnect stays a silent no-op until the app is restarted.
		a.dropSession(id, existing)
	}

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
	// Two ConnectSite calls can race past the liveness check and both dial.
	// Install-or-discard under the lock: the loser closes its own client
	// instead of orphaning the winner's (a leaked authenticated session
	// holds a server slot and its mux goroutines for the process lifetime).
	if cur, ok := a.sessions[id]; ok && cur != client {
		a.mu.Unlock()
		client.Close()
		return nil
	}
	a.sessions[id] = client
	a.mu.Unlock()
	go a.keepAlive(id, client)
	a.emitConnState(id, "connected")
	return nil
}

// dropSession closes and removes one browse session — but only while it is
// still the registered one, so a stale keepalive can never tear down a
// session the user has already re-established.
func (a *App) dropSession(id int64, c *sftpfast.Client) {
	a.mu.Lock()
	if a.sessions[id] != c {
		a.mu.Unlock()
		return
	}
	delete(a.sessions, id)
	a.mu.Unlock()
	c.Close()
	a.emitConnState(id, "disconnected")
}

// keepAlive pings a browse session until it dies or is replaced. The pings
// keep NAT/firewall idle timers from silently killing the connection, and a
// failed ping flips the UI to disconnected so the next connect redials.
func (a *App) keepAlive(id int64, c *sftpfast.Client) {
	// a.ctx is only set in startup; guard so a caller that skips the Wails
	// lifecycle (tests, future refactors) cannot panic a background goroutine.
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		a.mu.Lock()
		current := a.sessions[id] == c
		a.mu.Unlock()
		if !current {
			return
		}
		if !c.Alive(10 * time.Second) {
			a.dropSession(id, c)
			return
		}
	}
}

// evictIfDead drops the session when err says the transport is gone: a
// permission error keeps the session, a dead connection frees it so the UI
// can offer a reconnect that works. Suspect errors are corroborated with a
// probe first — one slow call timing out (a transient net.Error on a
// high-RTT link) must not tear down a healthy session.
func (a *App) evictIfDead(id int64, c *sftpfast.Client, err error) {
	if !isDeadConn(err) {
		return
	}
	if c.Alive(3 * time.Second) {
		return
	}
	a.dropSession(id, c)
}

// isDeadConn classifies errors that mean the SSH transport is gone, as
// opposed to ordinary SFTP failures like "permission denied".
func isDeadConn(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection lost") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "connection reset")
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
	l, err := client.List(path)
	if err != nil {
		a.evictIfDead(id, client, err)
	}
	return l, err
}

// RemoteHome resolves the SFTP session's home directory.
func (a *App) RemoteHome(id int64) (string, error) {
	a.mu.Lock()
	client, ok := a.sessions[id]
	a.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("site %d is not connected", id)
	}
	home, err := client.Home()
	if err != nil {
		a.evictIfDead(id, client, err)
	}
	return home, err
}

// --- File operation bindings (local and remote) ---
//
// Each returns after the change lands so the caller can refresh, and emits
// fs:changed so any pane showing that directory updates itself.

// DeleteLocal removes local files/folders (recursive for folders).
func (a *App) DeleteLocal(paths []string, dir string) (int, error) {
	n, err := localfs.Delete(paths)
	a.emitFsChanged("local", 0, dir)
	return n, err
}

// RenameLocal renames one local entry in place.
func (a *App) RenameLocal(path, newName, dir string) error {
	err := localfs.Rename(path, newName)
	a.emitFsChanged("local", 0, dir)
	return err
}

// MkdirLocal creates a local folder.
func (a *App) MkdirLocal(parent, name string) error {
	err := localfs.Mkdir(parent, name)
	a.emitFsChanged("local", 0, parent)
	return err
}

// MoveLocal relocates local entries into destDir.
func (a *App) MoveLocal(paths []string, destDir, dir string) (int, error) {
	n, err := localfs.Move(paths, destDir)
	a.emitFsChanged("local", 0, dir)
	a.emitFsChanged("local", 0, destDir)
	return n, err
}

// DeleteRemote removes remote files/folders over the site's browse
// connection (recursive for folders).
func (a *App) DeleteRemote(siteID int64, paths []string, dir string) (int, error) {
	client, err := a.session(siteID)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, p := range paths {
		if rerr := client.Remove(a.ctx, p); rerr != nil {
			a.evictIfDead(siteID, client, rerr)
			a.emitFsChanged("remote", siteID, dir)
			return removed, rerr
		}
		removed++
	}
	a.emitFsChanged("remote", siteID, dir)
	return removed, nil
}

// RenameRemote renames one remote entry in place.
func (a *App) RenameRemote(siteID int64, path, newName, dir string) error {
	client, err := a.session(siteID)
	if err != nil {
		return err
	}
	rerr := client.RenameEntry(path, newName)
	a.evictIfDead(siteID, client, rerr)
	a.emitFsChanged("remote", siteID, dir)
	return rerr
}

// MkdirRemote creates a remote folder.
func (a *App) MkdirRemote(siteID int64, parent, name string) error {
	client, err := a.session(siteID)
	if err != nil {
		return err
	}
	rerr := client.MkdirEntry(parent, name)
	a.evictIfDead(siteID, client, rerr)
	a.emitFsChanged("remote", siteID, parent)
	return rerr
}

func (a *App) session(siteID int64) (*sftpfast.Client, error) {
	a.mu.Lock()
	client, ok := a.sessions[siteID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("site %d is not connected", siteID)
	}
	return client, nil
}

// emitFsChanged tells panes showing this directory to reload.
func (a *App) emitFsChanged(kind string, siteID int64, dir string) {
	a.sink.Emit("fs:changed", map[string]any{
		"source": kind, "siteId": siteID, "dir": dir,
	})
}

// ResolvePrompt answers a blocking engine prompt (host key, overwrite...).
func (a *App) ResolvePrompt(promptID string, answer bool) {
	if a.broker != nil {
		a.broker.Resolve(promptID, answer)
	}
}

// --- Transfer bindings ---

// DownloadItem is one remote file or folder selected for download.
type DownloadItem struct {
	Src   string `json:"src"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
}

// UploadItem is one local file or folder selected for upload.
type UploadItem struct {
	Src   string `json:"src"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
}

// EnqueueDownloads queues remote files for download into localDir. Folders
// are expanded in the background over the site's browse connection,
// preserving their relative structure under localDir/<foldername>/.
func (a *App) EnqueueDownloads(siteID int64, items []DownloadItem, localDir string) ([]int64, error) {
	if a.store == nil {
		return nil, errNoStore
	}
	// Validate preconditions before enqueuing anything so the call is
	// all-or-nothing (a partial failure would queue files while reporting
	// total failure to the UI).
	var dirs, files []DownloadItem
	for _, it := range items {
		if it.IsDir {
			dirs = append(dirs, it)
		} else {
			files = append(files, it)
		}
	}
	var client *sftpfast.Client
	if len(dirs) > 0 {
		a.mu.Lock()
		client = a.sessions[siteID]
		a.mu.Unlock()
		if client == nil {
			return nil, fmt.Errorf("connect the site before queuing folders")
		}
	}

	ids := make([]int64, 0, len(files))
	for _, it := range files {
		name, err := safeLocalName(it.Src)
		if err != nil {
			return ids, err
		}
		id, err := a.store.EnqueueTransfer(queue.Transfer{
			SiteID: siteID,
			Src:    it.Src,
			Dst:    filepath.Join(localDir, name),
			Size:   it.Size,
		})
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}

	if len(dirs) > 0 {
		go a.expandRemoteDirs(client, siteID, dirs, localDir)
	}

	a.sink.Emit("queue:changed", nil)
	a.dispatcher.Wake()
	return ids, nil
}

// expandRemoteDirs walks queued folders and enqueues their files. Runs in a
// goroutine: big trees must never block the UI thread.
func (a *App) expandRemoteDirs(client *sftpfast.Client, siteID int64, dirs []DownloadItem, localDir string) {
	total := 0
	skipped := 0
	for _, dir := range dirs {
		root := path.Clean(dir.Src)
		rootName, nerr := safeLocalName(root)
		if nerr != nil {
			a.sink.Emit("app:error", nerr.Error())
			continue
		}
		err := client.WalkFiles(a.ctx, root, func(remote string, size int64) error {
			rel := strings.TrimPrefix(remote, root)
			rel = strings.TrimPrefix(rel, "/")
			// Every path component here is server-supplied; keep it inside
			// localDir/<folder>/ or skip the entry entirely.
			dst, jerr := safeLocalJoin(filepath.Join(localDir, rootName), rel)
			if jerr != nil {
				skipped++
				return nil
			}
			if _, err := a.store.EnqueueTransfer(queue.Transfer{
				SiteID: siteID, Src: remote, Dst: dst, Size: size,
			}); err != nil {
				return err
			}
			total++
			if total%25 == 0 {
				a.sink.Emit("queue:changed", nil)
				a.dispatcher.Wake()
			}
			return nil
		})
		if err != nil {
			a.sink.Emit("app:error", fmt.Sprintf("folder %s: %v", path.Base(root), err))
		}
	}
	msg := fmt.Sprintf("Queued %d file(s) from %d folder(s)", total, len(dirs))
	if skipped > 0 {
		msg += fmt.Sprintf(" · %d skipped (unsafe names)", skipped)
	}
	a.sink.Emit("app:info", msg)
	a.sink.Emit("queue:changed", nil)
	a.dispatcher.Wake()
}

// EnqueueUploads queues local files/folders for upload into remoteDir on the
// site. Folder trees are expanded in the background off the local disk.
func (a *App) EnqueueUploads(siteID int64, items []UploadItem, remoteDir string) ([]int64, error) {
	if a.store == nil {
		return nil, errNoStore
	}
	ids := make([]int64, 0, len(items))
	var dirs []UploadItem
	for _, it := range items {
		if it.IsDir {
			dirs = append(dirs, it)
			continue
		}
		id, err := a.store.EnqueueTransfer(queue.Transfer{
			SiteID:    siteID,
			Direction: "upload",
			Src:       it.Src,
			Dst:       path.Join(remoteDir, filepath.Base(it.Src)),
			Size:      it.Size,
		})
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}

	if len(dirs) > 0 {
		go a.expandLocalDirs(siteID, dirs, remoteDir)
	}

	a.sink.Emit("queue:changed", nil)
	a.dispatcher.Wake()
	return ids, nil
}

func (a *App) expandLocalDirs(siteID int64, dirs []UploadItem, remoteDir string) {
	const maxEntries = 50000
	total := 0
	skipped := 0
	for _, dir := range dirs {
		root := filepath.Clean(dir.Src)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if cerr := a.ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil {
				// An unreadable subtree must not abort the whole walk.
				skipped++
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			// Regular files only: following symlinks would upload their
			// targets from outside the selected tree.
			if !d.Type().IsRegular() {
				skipped++
				return nil
			}
			if total >= maxEntries {
				return fmt.Errorf("more than %d files under %q — refusing runaway walk", maxEntries, root)
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil // vanished mid-walk; skip
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			dst := path.Join(remoteDir, filepath.Base(root), filepath.ToSlash(rel))
			if _, err := a.store.EnqueueTransfer(queue.Transfer{
				SiteID: siteID, Direction: "upload", Src: p, Dst: dst, Size: info.Size(),
			}); err != nil {
				return err
			}
			total++
			if total%25 == 0 {
				a.sink.Emit("queue:changed", nil)
				a.dispatcher.Wake()
			}
			return nil
		})
		if err != nil {
			a.sink.Emit("app:error", fmt.Sprintf("folder %s: %v", filepath.Base(root), err))
		}
	}
	uploadMsg := fmt.Sprintf("Queued %d file(s) from %d folder(s)", total, len(dirs))
	if skipped > 0 {
		uploadMsg += fmt.Sprintf(" · %d skipped (links/unreadable)", skipped)
	}
	a.sink.Emit("app:info", uploadMsg)
	a.sink.Emit("queue:changed", nil)
	a.dispatcher.Wake()
}

// TransfersList returns the newest queue rows for the dock.
func (a *App) TransfersList() ([]queue.Transfer, error) {
	if a.store == nil {
		return nil, errNoStore
	}
	return a.store.Transfers(200)
}

// PauseTransfer stops a transfer keeping its .wspart for byte-resume.
func (a *App) PauseTransfer(id int64) error { return a.dispatcher.Pause(id) }

// ResumeTransfer requeues a paused or failed transfer.
func (a *App) ResumeTransfer(id int64) error { return a.dispatcher.Resume(id) }

// CancelTransfer aborts a transfer.
func (a *App) CancelTransfer(id int64) error { return a.dispatcher.Cancel(id) }

// ClearDoneTransfers removes completed and cancelled rows.
func (a *App) ClearDoneTransfers() error {
	if a.store == nil {
		return errNoStore
	}
	_, err := a.store.ClearFinished()
	a.sink.Emit("queue:changed", nil)
	return err
}

// --- Bookmark bindings ---
//
// siteID 0 is the local filesystem, matching the storage convention.

// BookmarksFor lists saved folders for one pane source.
func (a *App) BookmarksFor(siteID int64) ([]queue.Bookmark, error) {
	if a.store == nil {
		return nil, errNoStore
	}
	return a.store.Bookmarks(siteID)
}

// AddBookmark saves the given folder.
func (a *App) AddBookmark(siteID int64, path, label string) error {
	if a.store == nil {
		return errNoStore
	}
	_, err := a.store.AddBookmark(siteID, path, label)
	return err
}

// DeleteBookmark removes a saved folder.
func (a *App) DeleteBookmark(id int64) error {
	if a.store == nil {
		return errNoStore
	}
	return a.store.DeleteBookmark(id)
}

// SetSiteRemotePath records the folder a site opens in, without requiring
// the caller to round-trip the whole site record.
func (a *App) SetSiteRemotePath(siteID int64, path string) error {
	if a.store == nil {
		return errNoStore
	}
	site, err := a.store.SiteByID(siteID)
	if err != nil {
		return err
	}
	site.RemotePath = path
	if _, err := a.store.SaveSite(site); err != nil {
		return err
	}
	return nil
}

// --- Data bindings (where settings live, and backing them up) ---

// DataInfo describes the settings store for the Data section of Settings.
type DataInfo struct {
	Path    string   `json:"path"`
	Folder  string   `json:"folder"`
	Backups []string `json:"backups"`
}

// DataLocation reports where settings are kept and what snapshots exist.
func (a *App) DataLocation() (DataInfo, error) {
	if a.store == nil {
		return DataInfo{}, errNoStore
	}
	p, err := a.store.Path()
	if err != nil {
		return DataInfo{}, err
	}
	backups, err := a.store.Backups()
	if err != nil {
		return DataInfo{}, err
	}
	return DataInfo{Path: p, Folder: filepath.Dir(p), Backups: backups}, nil
}

// BackupData snapshots the settings database and returns the new file name.
func (a *App) BackupData() (string, error) {
	if a.store == nil {
		return "", errNoStore
	}
	dest, err := a.store.Backup(time.Now())
	if err != nil {
		return "", err
	}
	return filepath.Base(dest), nil
}

// OpenDataFolder reveals the settings folder in Explorer. Wails'
// BrowserOpenURL refuses the file:// scheme, so this shells out instead —
// and reports failure rather than looking like a dead button.
func (a *App) OpenDataFolder() error {
	info, err := a.DataLocation()
	if err != nil {
		return err
	}
	if err := openInFileManager(info.Folder); err != nil {
		return fmt.Errorf("open %s: %w", info.Folder, err)
	}
	return nil
}

// --- Settings bindings ---

// settingValidators allowlists the keys the frontend may write AND the
// values it may write: a persisted 0 concurrency would stall the queue
// forever, so bad values are rejected at the boundary, not absorbed.
var settingValidators = map[string]func(string) error{
	"transfers.global_max": intRange(1, 16),
	"transfers.site_max":   intRange(1, 8),
	"bw.limit_bytes":       intRange(0, 1<<40),
	"bw.percent":           intRange(10, 95),
	"bw.mode":              oneOf("off", "fixed", "percent"),
	// "dark"/"light" are the pre-v3 names, still accepted so an existing
	// setting keeps working; the frontend maps them to the new themes.
	"ui.theme":         oneOf("clay", "cobalt", "iris", "system", "flightdeck", "drafting", "press", "nightshift", "dark", "light"),
	"ui.local_default": anyString, // the folder local panes open in
	// UI layout state lives here rather than in browser storage, so one
	// backup of the database captures everything except credentials.
	"ui.queue_columns": jsonBlob,
	"ui.queue_sort":    jsonBlob,
	"ui.pane_columns":  jsonBlob,
	"ui.pane_sort":     jsonBlob,
	"ui.recents":       jsonBlob,
	// One-time flags ("1" once shown) — e.g. the post-first-transfer
	// support-the-project toast must never repeat.
	"ui.donate_nudged":        oneOf("", "1"),
	"transfers.chunk_min_mb":  intRange(0, 1<<20), // 0 disables chunking
	"transfers.chunk_streams": intRange(1, 16),
	// Uploads get their own pair: the uplink saturates at far fewer lanes
	// than the downlink, so one shared value would be wrong in both
	// directions.
	"transfers.upload_chunk_min_mb":  intRange(0, 1<<20), // 0 disables upload chunking
	"transfers.upload_chunk_streams": intRange(1, 16),
}

func intRange(lo, hi int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("expected a number, got %q", v)
		}
		if n < lo || n > hi {
			return fmt.Errorf("value %d out of range %d–%d", n, lo, hi)
		}
		return nil
	}
}

// anyString accepts any value — used for free-form settings like a folder
// path, which the filesystem validates when it is actually used.
func anyString(string) error { return nil }

// jsonBlob accepts UI state the frontend owns, bounded so a runaway writer
// cannot bloat the settings table.
func jsonBlob(v string) error {
	const maxLen = 64 << 10
	if len(v) > maxLen {
		return fmt.Errorf("value is %d bytes, over the %d limit", len(v), maxLen)
	}
	if v != "" && !json.Valid([]byte(v)) {
		return fmt.Errorf("value is not valid JSON")
	}
	return nil
}

func oneOf(allowed ...string) func(string) error {
	return func(v string) error {
		for _, a := range allowed {
			if v == a {
				return nil
			}
		}
		return fmt.Errorf("value %q must be one of %v", v, allowed)
	}
}

// GetSettings returns all settings for the settings dialog.
func (a *App) GetSettings() (map[string]string, error) {
	if a.store == nil {
		return nil, errNoStore
	}
	return a.store.AllSettings()
}

// SetSetting updates one allowlisted setting and nudges the dispatcher so
// caps/throttle apply immediately.
func (a *App) SetSetting(key, value string) error {
	if a.store == nil {
		return errNoStore
	}
	validate, ok := settingValidators[key]
	if !ok {
		return fmt.Errorf("setting %q is not user-settable", key)
	}
	if err := validate(value); err != nil {
		return fmt.Errorf("setting %s: %w", key, err)
	}
	if err := a.store.SetSetting(key, value); err != nil {
		return err
	}
	a.sink.Emit("settings:changed", map[string]string{"key": key, "value": value})
	if a.dispatcher != nil {
		a.dispatcher.Wake()
	}
	return nil
}

func (a *App) emitConnState(siteID int64, state string) {
	a.sink.Emit("site:connstate", map[string]any{"siteId": siteID, "state": state})
}
