package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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

	a.dispatcher = dispatch.New(store, a.sink, a.dialTransfer)
	go a.dispatcher.Run(ctx)
}

// dialTransfer opens a dedicated data connection for one transfer. Unknown
// host keys are DENIED here (nil prompt): pins are established by the
// interactive browse connect, so the queue can never silently trust a host.
func (a *App) dialTransfer(ctx context.Context, siteID int64) (*sftpfast.Client, error) {
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
	return sftpfast.Dial(ctx, sftpfast.Config{
		Host:     site.Host,
		Port:     site.Port,
		User:     site.Username,
		Password: password,
	}, a.hostkeys.Callback(siteID, nil))
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

// RemoteHome resolves the SFTP session's home directory.
func (a *App) RemoteHome(id int64) (string, error) {
	a.mu.Lock()
	client, ok := a.sessions[id]
	a.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("site %d is not connected", id)
	}
	return client.Home()
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
	"ui.theme":             oneOf("dark", "light", "system"),
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
