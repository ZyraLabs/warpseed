// Package dispatch runs the transfer queue: claims pending rows, executes
// them on dedicated per-transfer connections (never the browse connection),
// coalesces progress events, and applies the two-level retry ladder.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"warpseed/internal/applog"

	"golang.org/x/time/rate"

	"warpseed/internal/engine/core"
	"warpseed/internal/engine/sftpfast"
	"warpseed/internal/events"
	"warpseed/internal/queue"
)

// Factory dials n fresh transfer connections for a site (never the browse
// connection). Supplied by the app layer, which owns credentials and
// host-key pins. It may return fewer than n if the server refuses extras;
// callers must cope with what they get.
type Factory func(ctx context.Context, siteID int64, n int) ([]*sftpfast.Client, error)

const (
	// 8, not 6: the shipped lane counts are 4 for downloads and 3 for uploads,
	// so a budget of 6 cannot run one of each — whichever direction claimed
	// the connections first held them for the whole transfer while the other
	// waited. 4 + 3 needs 7; 8 leaves one spare.
	defaultGlobalCap = 8
	defaultSiteCap   = 8
	maxAttempts      = 3
	// Chunked (multi-connection) downloads: default threshold and stream
	// count. A server that caps per-connection speed is the whole reason
	// this exists — one big file otherwise runs at one connection's rate.
	defaultChunkMinMB  = 256
	defaultChunkStream = 4
	// Uploads use their own pair: a ~12 MiB/s uplink is already saturated by
	// three lanes against the same per-connection cap, so a fourth buys no
	// throughput and costs a handshake, a MaxStartups slot and a global slot
	// a concurrent download could have used.
	defaultUploadChunkMinMB  = 128
	defaultUploadChunkStream = 3
	maxChunkStreams          = 16
	// progress cadence: events ≤4Hz per transfer, DB checkpoint every 3s
	eventEvery = 250 * time.Millisecond
	dbEvery    = 3 * time.Second
	// limiter burst: one pipeline window so throttling stays smooth
	limiterBurst = 1 << 20
	// How long an observed server connection ceiling is trusted before the
	// next transfer probes the configured width again.
	grantTTL = 2 * time.Minute
	// Budget for the one-off connection a cancelled upload needs to delete
	// its remote placeholder. Short: this is cleanup, and the user has
	// already moved on.
	cleanupDialTimeout = 30 * time.Second
)

// grant is one observation of how many connections a site's server actually
// handed over, with when it was seen. Both halves matter: the count keeps
// the queue moving against a real limit, the timestamp keeps a momentary
// refusal from becoming a permanent one.
type grant struct {
	n  int
	at time.Time
}

type Dispatcher struct {
	store   *queue.Store
	sink    events.Sink
	factory Factory
	wake    chan struct{}
	// paused is the queue-wide stop: nothing new starts and whatever was
	// running goes back to pending. It mirrors the persisted "queue.paused"
	// setting so a paused queue stays paused across a restart — the whole
	// point of it for someone who wants to launch warpseed WITHOUT last
	// night's queue springing back to life.
	paused atomic.Bool
	// stopping is set by Stop so a transfer cut off by shutdown is requeued
	// clean rather than classified as an error and put on the retry ladder.
	// The pump gates on it too: a requeued row must not be re-claimed and
	// started into a database that is about to close.
	stopping atomic.Bool

	mu      sync.Mutex
	running sync.WaitGroup // in-flight runTransfer goroutines, for Stop
	// sweeps counts background placeholder discards from bulk cancels, so
	// tests (and Stop) can wait for them.
	sweeps  sync.WaitGroup
	cancels map[int64]context.CancelFunc
	// cancelledAt is stamped at the one place a running transfer's context
	// is cancelled, so the verbose log can report how long the lanes took
	// to let go — the number that separates a stuck kernel write from a
	// slow server.
	cancelledAt map[int64]time.Time
	// requeue names the running rows a queue-wide pause stopped. Recorded
	// per row at the moment of the stop, because the global flag is not
	// evidence by the time the engine unwinds: a Resume that lands while
	// the lanes are still letting go would otherwise turn the pause into
	// a failure. Cleared in release.
	requeue map[int64]bool
	slots   map[int64]int // connections reserved per active transfer
	perSite map[int64]int
	// granted is the connection ceiling a site's SERVER was last observed
	// to enforce: set when a dial came back short, cleared when the site
	// goes idle or when the observation goes stale. Without it,
	// wait-for-full-width deadlocks against a server that will never grant
	// the full width — the queue would sit idle for hours waiting on
	// connections the server has already refused. A missing entry means
	// "not observed", i.e. ask for the configured width.
	granted map[int64]grant
	// activeDst holds the destinations currently being written, so two queue
	// rows aimed at the same file cannot run at once. The placeholder path
	// is derived from the destination alone, so a second runner would
	// interleave writes into the same .wspart — for downloads the bytes are
	// identical and it merely wastes the transfer, but two uploads can have
	// different sources and would splice two files together.
	activeDst map[string]bool
	activeN   int

	limMu       sync.Mutex
	limiter     *rate.Limiter // nil = unthrottled
	windowBytes int64         // atomic: bytes moved since last tick
	observedMax float64       // best aggregate rate seen (bytes/sec)
}

func New(store *queue.Store, sink events.Sink, factory Factory) *Dispatcher {
	d := &Dispatcher{
		store:       store,
		sink:        sink,
		factory:     factory,
		wake:        make(chan struct{}, 1),
		cancels:     make(map[int64]context.CancelFunc),
		cancelledAt: make(map[int64]time.Time),
		requeue:     make(map[int64]bool),
		slots:       make(map[int64]int),
		perSite:     make(map[int64]int),
		granted:     make(map[int64]grant),
		activeDst:   make(map[string]bool),
	}
	// Paused if the user left it paused, or asked for every launch to start
	// that way. Derived here rather than written back, so turning "start
	// paused" off later changes only future launches and never has to undo
	// a flag it planted.
	d.paused.Store(store.Setting("queue.paused", "0") == "1" ||
		store.Setting("queue.start_paused", "0") == "1")
	return d
}

// Wake nudges the dispatch loop (after enqueue/resume).
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run pumps the queue until ctx ends. Call in a goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	const tick = 2 * time.Second
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	d.observedMax = float64(d.store.SettingInt("bw.observed_max", 0))
	lastPersist := time.Now()
	lastSwap := time.Now()
	for {
		d.refreshLimiter()
		d.pump(ctx)
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
			moved := atomic.SwapInt64(&d.windowBytes, 0)
			// Measure against real elapsed time: wake-driven iterations make
			// the interval shorter than the tick and would inflate the rate.
			elapsed := time.Since(lastSwap).Seconds()
			lastSwap = time.Now()
			if elapsed < 0.5 {
				elapsed = 0.5
			}
			agg := float64(moved) / elapsed
			d.limMu.Lock()
			// Only decay on a fresh UNTHROTTLED measurement: an idle window
			// carries no information, and a throttled window is capped below
			// observedMax by construction (decaying on those would ratchet
			// the baseline down until throughput collapsed).
			if moved > 0 && d.limiter == nil {
				d.observedMax *= 0.999
			}
			if agg > d.observedMax {
				d.observedMax = agg
			}
			observed := d.observedMax
			d.limMu.Unlock()
			if time.Since(lastPersist) > 30*time.Second {
				lastPersist = time.Now()
				if err := d.store.SetSetting("bw.observed_max", fmt.Sprintf("%.0f", observed)); err != nil {
					log.Printf("dispatch: persist observed max: %v", err)
				}
			}
		}
	}
}

// refreshLimiter re-reads bandwidth settings. "percent" mode throttles to a
// fraction of the best aggregate rate this install has measured — the
// user's "give me 80% of my max" variable throttle.
func (d *Dispatcher) refreshLimiter() {
	mode := d.store.Setting("bw.mode", "off")
	var limit rate.Limit
	switch mode {
	case "fixed":
		if b := d.store.SettingInt("bw.limit_bytes", 0); b > 0 {
			limit = rate.Limit(b)
		}
	case "percent":
		pct := d.store.SettingInt("bw.percent", 80)
		d.limMu.Lock()
		observed := d.observedMax
		d.limMu.Unlock()
		if observed > 0 && pct > 0 && pct < 100 {
			limit = rate.Limit(observed * float64(pct) / 100)
		}
	}
	d.limMu.Lock()
	defer d.limMu.Unlock()
	if limit == 0 {
		d.limiter = nil
		return
	}
	// Adjust in place: replacing the limiter would hand out a fresh full
	// burst on every change and strand goroutines waiting on the old one.
	if d.limiter == nil {
		d.limiter = rate.NewLimiter(limit, limiterBurst)
	} else if d.limiter.Limit() != limit {
		d.limiter.SetLimit(limit)
	}
}

func (d *Dispatcher) currentLimiter() *rate.Limiter {
	d.limMu.Lock()
	defer d.limMu.Unlock()
	return d.limiter
}

func (d *Dispatcher) pump(ctx context.Context) {
	if d.paused.Load() || d.stopping.Load() {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	pending, err := d.store.PendingTransfers(now)
	if err != nil {
		log.Printf("dispatch: pending: %v", err)
		return
	}
	// Clamp at the point of use: a persisted 0 (from any source, including
	// an older build) must never silently freeze the queue.
	globalCap := d.store.SettingInt("transfers.global_max", defaultGlobalCap)
	if globalCap < 1 {
		globalCap = defaultGlobalCap
	}
	siteDefault := d.store.SettingInt("transfers.site_max", defaultSiteCap)
	if siteDefault < 1 {
		siteDefault = defaultSiteCap
	}
	siteCaps := make(map[int64]int)
	// Queues whose next transfer could not be admitted this pass. Filling the
	// spare connections with whatever fits further down the queue would let a
	// run of small files hold a wide one at the head of the line, and makes
	// the order the queue shows a lie.
	//
	// Keyed by site AND DIRECTION. Queue order is a promise within one
	// direction — don't let small downloads jump a big one — but it is not a
	// promise between them: an upload that cannot fit has no business
	// stopping downloads from being considered. Keyed by site alone, one
	// upload waiting for width froze every download to that server.
	blocked := make(map[string]bool)
	for _, t := range pending {
		if blocked[laneGroup(t)] {
			continue
		}
		siteCap, ok := siteCaps[t.SiteID]
		if !ok {
			siteCap = siteDefault
			if site, err := d.store.SiteByID(t.SiteID); err == nil && site.MaxTransfers > 0 {
				siteCap = site.MaxTransfers
			}
			if siteCap < 1 {
				siteCap = defaultSiteCap
			}
			siteCaps[t.SiteID] = siteCap
		}

		// A chunked transfer occupies several connections, so it reserves
		// that many slots — concurrency limits stay honest either way.
		streams := d.streamsFor(t, siteCap)

		d.mu.Lock()
		// Re-checked under the lock SetPaused takes to stop the running
		// set: a pause that lands mid-pass must not be followed by a claim
		// it never saw, or one transfer would start in a paused queue.
		if d.paused.Load() || d.stopping.Load() {
			d.mu.Unlock()
			return
		}
		if _, running := d.cancels[t.ID]; running {
			d.mu.Unlock()
			continue
		}
		streams = d.clampToGranted(t.SiteID, streams)
		if !d.fits(t.SiteID, streams, globalCap, siteCap) {
			d.mu.Unlock()
			blocked[laneGroup(t)] = true
			continue
		}
		if dk := dstKey(t); d.activeDst[dk] {
			// Another row is already writing this exact file. Skip it rather
			// than blocking the site: everything else in the queue is
			// unaffected, and this one is picked up as soon as that runner
			// finishes.
			d.mu.Unlock()
			continue
		}
		tctx, cancel := context.WithCancel(ctx)
		d.cancels[t.ID] = cancel
		d.activeDst[dstKey(t)] = true
		d.slots[t.ID] = streams
		d.perSite[t.SiteID] += streams
		d.activeN += streams
		d.mu.Unlock()

		// Claim only while the row is still pending. This list was read
		// before the locks above were taken, so the user may have paused or
		// cancelled this row in between; an unconditional write would
		// resurrect a cancelled transfer and start moving bytes for it.
		won, err := d.store.ClaimPending(t.ID)
		if err != nil {
			log.Printf("dispatch: claim %d: %v", t.ID, err)
			d.release(t)
			continue
		}
		if !won {
			applog.Debugf("dispatch: transfer %d: no longer pending, skipping claim", t.ID)
			d.release(t)
			continue
		}
		// Stamp the run so Activity can report a real speed. Best-effort:
		// losing a timestamp costs a "—" in one row, never the transfer.
		if serr := d.store.MarkTransferStarted(t.ID, now, t.BytesDone); serr != nil {
			log.Printf("dispatch: mark started %d: %v", t.ID, serr)
		}
		d.emitStateSrc(t.ID, t.Src, "active", "")
		d.running.Add(1)
		go d.runTransfer(tctx, t, streams)
	}
}

// clampToGranted lowers a request to what this site's server was last seen
// to allow. Caller holds d.mu.
//
// Waiting for a full width only makes sense when the width will eventually
// be free. When a server refuses the extra connections (the "granted 4/6"
// line in the log), the shortfall is permanent, and a queue that holds out
// for the configured width would leave most of the budget idle until the
// one running transfer finished. Asking for what the server actually gives
// runs those files concurrently instead.
func (d *Dispatcher) clampToGranted(siteID int64, streams int) int {
	g, ok := d.granted[siteID]
	if !ok {
		return streams
	}
	// A refusal is a snapshot, not a verdict. MaxStartups is momentary and
	// per-source penalties expire, so an observation that is no longer fresh
	// is dropped and the next transfer probes the configured width again —
	// otherwise one unlucky dial would hold a site narrow for a whole
	// overnight run.
	if time.Since(g.at) > grantTTL {
		delete(d.granted, siteID)
		return streams
	}
	if g.n > 0 && streams > g.n {
		// Never narrow a chunk-eligible transfer to a single connection.
		// One lane sends it down the linear path, which deletes the .wschunk
		// and the saved plan — tens of gigabytes discarded because a server
		// was briefly busy. Asking for 2 and being given 1 is caught
		// downstream and requeued with the plan intact; asking for 1 is not
		// caught at all, because it looks like a deliberate setting.
		if streams >= 2 && g.n < 2 {
			return 2
		}
		return g.n
	}
	return streams
}

// fits reports whether a transfer's FULL connection request can be admitted
// right now. Caller holds d.mu.
//
// A transfer waits for its full width rather than starting on whatever is
// spare: running a 4-lane file on one leftover connection is how a queue of
// large files ended up with every file crawling at one connection's speed
// while Settings said 4 lanes. streamsFor has already clamped the request
// to both caps, so a request that does not fit now always fits once the
// running transfers drain — waiting can never become starving.
func (d *Dispatcher) fits(siteID int64, streams, globalCap, siteCap int) bool {
	return d.activeN+streams <= globalCap && d.perSite[siteID]+streams <= siteCap
}

// streamsFor decides how many connections a transfer gets: >1 for files past
// the chunking threshold, which each direction sets separately.
func (d *Dispatcher) streamsFor(t queue.Transfer, siteCap int) int {
	if t.Engine != "sftpfast" {
		return 1
	}
	minKey, streamKey := "transfers.chunk_min_mb", "transfers.chunk_streams"
	defMin, defStream := defaultChunkMinMB, defaultChunkStream
	if t.Direction == "upload" {
		minKey, streamKey = "transfers.upload_chunk_min_mb", "transfers.upload_chunk_streams"
		defMin, defStream = defaultUploadChunkMinMB, defaultUploadChunkStream
	}
	minMB := d.store.SettingInt(minKey, defMin)
	if minMB <= 0 || t.Size < int64(minMB)<<20 {
		return 1
	}
	streams := d.store.SettingInt(streamKey, defStream)
	if streams < 2 {
		return 1
	}
	if streams > maxChunkStreams {
		streams = maxChunkStreams
	}
	// A transfer can never reserve more connections than the caps allow.
	// This clamp is also what makes pump's wait-for-full-width admission
	// safe: a request that fits inside both caps always fits once the
	// running transfers drain, so nothing can queue behind a demand that
	// can never be met. It does mean a per-site budget below the lane
	// count silently narrows Hyperlane, which Settings now says out loud.
	globalCap := d.store.SettingInt("transfers.global_max", defaultGlobalCap)
	if globalCap < 1 {
		globalCap = defaultGlobalCap
	}
	if streams > globalCap {
		streams = globalCap
	}
	if streams > siteCap {
		streams = siteCap
	}
	if streams < 2 {
		return 1
	}
	return streams
}

// laneGroup names the queue a transfer waits in: one per site per direction.
// Head-of-line blocking applies within a group, never across them.
func laneGroup(t queue.Transfer) string {
	return fmt.Sprintf("%d/%s", t.SiteID, t.Direction)
}

// dstKey identifies the file a transfer writes. An upload's destination is
// remote, so it is only unique within its site; a download's is a local
// path, unique across all of them.
func dstKey(t queue.Transfer) string {
	if t.Direction == "upload" {
		return fmt.Sprintf("%d:%s", t.SiteID, t.Dst)
	}
	return "local:" + t.Dst
}

// parentDir returns the containing directory of a transfer destination,
// using the separator convention of the side it lives on.
func parentDir(dst string, remote bool) string {
	if remote {
		return path.Dir(dst)
	}
	return filepath.Dir(dst)
}

// removePart deletes a leftover local partial file, ignoring absence, and
// reports whether the file is now gone.
func removePart(path string) bool {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("dispatch: remove %s: %v", path, err)
		return false
	}
	return true
}

// removeTransferPart deletes a leftover partial file on whichever side the
// destination lives, ignoring absence, and reports whether the file is now
// gone. An upload's Dst is a remote path, so os.Remove would be a no-op at
// best and a wrong deletion at worst.
//
// The caller needs the answer, not just a log line: a row whose placeholder
// is still out there must keep its byte count, or a full-size .wschunk sits
// on a seedbox quota with nothing pointing at it.
func (d *Dispatcher) removeTransferPart(t queue.Transfer, c *sftpfast.Client, suffix string) bool {
	p := t.Dst + suffix
	if t.Direction == "upload" {
		if c == nil {
			return false
		}
		if err := c.RemoveRemote(p); err != nil {
			log.Printf("dispatch: remove remote %s: %v", p, err)
			return false
		}
		return true
	}
	return removePart(p)
}

// first is the client to run one-off remote housekeeping on; nil when the
// server granted us nothing.
func first(cs []*sftpfast.Client) *sftpfast.Client {
	if len(cs) == 0 {
		return nil
	}
	return cs[0]
}

// resize adjusts a running transfer's slot reservation, returning capacity
// when the server granted fewer connections than requested.
func (d *Dispatcher) resize(t queue.Transfer, actual int) {
	d.mu.Lock()
	if prev, ok := d.slots[t.ID]; ok && actual < prev {
		diff := prev - actual
		d.slots[t.ID] = actual
		d.perSite[t.SiteID] -= diff
		d.activeN -= diff
		// The server just told us its real ceiling. Remember it so the
		// rows behind this one ask for what they can actually get.
		d.granted[t.SiteID] = grant{n: actual, at: time.Now()}
	}
	d.mu.Unlock()
	d.Wake()
}

// Stop cancels every running transfer and waits up to grace for their final
// state writes to land. Called on shutdown BEFORE the store closes: a
// transfer that completes while the database is closing loses its
// "completed" write, and because a successful run has already renamed its
// partial file away, the requeue on next launch has nothing to resume from
// and re-transfers the whole file.
//
// Returns whether everything finished in time; a timeout is not an error the
// caller can do anything about, but it is worth logging.
func (d *Dispatcher) Stop(grace time.Duration) bool {
	// Under d.mu, which is what makes startSweep's refusal airtight: a
	// sweep registered after this point would be an Add behind the Wait
	// below, and Go panics on that.
	d.mu.Lock()
	d.stopping.Store(true)
	d.mu.Unlock()
	n := d.stopAll(false)
	applog.Debugf("dispatch: shutdown: stopping %d transfer(s)", n)

	done := make(chan struct{})
	go func() {
		d.running.Wait()
		d.sweeps.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(grace):
		log.Printf("dispatch: %s grace expired with transfers still stopping", grace)
		return false
	}
}

// startSweep runs a background placeholder discard, unless the dispatcher
// is stopping — in which case the leftovers are Clear done's to sweep.
//
// The refusal and the WaitGroup Add happen under d.mu, and Stop sets
// `stopping` under that same lock before it waits. Without that, a check
// that passed just before Stop could Add behind Stop's Wait, which is a
// WaitGroup misuse Go answers with a panic — crashing the app as it
// closes. release() is the reason this is not merely theoretical: pump
// calls it synchronously on a lost claim, outside the `running` group Stop
// waits on first.
func (d *Dispatcher) startSweep(fn func()) {
	d.mu.Lock()
	if d.stopping.Load() {
		d.mu.Unlock()
		return
	}
	d.sweeps.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.sweeps.Done()
		fn()
	}()
}

// stopAll cancels every running transfer's context and reports how many.
// With requeue set, each is also marked as stopped by the queue-wide pause
// so finishWithError requeues it clean (see the requeue field).
func (d *Dispatcher) stopAll(requeue bool) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for id, cancel := range d.cancels {
		d.cancelledAt[id] = now
		if requeue {
			d.requeue[id] = true
		}
		cancel()
	}
	return len(d.cancels)
}

// release hands a transfer's reservations back. It also closes the one
// gap in cancel's follow-through: pump registers the reservation BEFORE
// its claim, and a requeued row keeps it until this runs, so a cancel that
// lands in either window finds the row "running", leaves the discard to a
// goroutine that will never do it, and the cancelled row keeps its
// placeholder. Reading the row back here catches both cases. Rare, so
// the (possibly remote) discard runs in the background rather than
// holding the pump.
func (d *Dispatcher) release(t queue.Transfer) {
	d.mu.Lock()
	if slots, ok := d.slots[t.ID]; ok {
		delete(d.cancels, t.ID)
		delete(d.cancelledAt, t.ID)
		delete(d.requeue, t.ID)
		delete(d.slots, t.ID)
		delete(d.activeDst, dstKey(t))
		d.perSite[t.SiteID] -= slots
		d.activeN -= slots
		if d.perSite[t.SiteID] <= 0 {
			// Idle site: drop both the counter and the observed ceiling, so
			// the next transfer probes the configured width again rather
			// than believing a limit that may have lifted.
			delete(d.perSite, t.SiteID)
			delete(d.granted, t.SiteID)
		}
	}
	d.mu.Unlock()
	if cur, err := d.store.TransferByID(t.ID); err == nil && cur.State == "cancelled" && cur.BytesDone > 0 {
		d.startSweep(func() {
			d.discardPartials(t, nil)
			d.sink.Emit("queue:changed", nil)
		})
	}
}

func (d *Dispatcher) runTransfer(ctx context.Context, t queue.Transfer, streams int) {
	defer d.running.Done()
	defer d.release(t)
	defer d.Wake() // a freed slot may unblock the next pending row
	started := time.Now()
	defer func() {
		// Runs before release() (defers are LIFO), so the stamp is still
		// there. A stop that takes seconds to honour is a lane stuck in
		// the kernel or the library; this is the number that shows it.
		if ctx.Err() == nil || !applog.Verbose() {
			return
		}
		d.mu.Lock()
		at, stamped := d.cancelledAt[t.ID]
		d.mu.Unlock()
		if stamped {
			applog.Debugf("dispatch: transfer %d: lanes released %s after stop request (%s after start)", t.ID, time.Since(at).Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
		} else {
			applog.Debugf("dispatch: transfer %d: lanes released %s after start (%v)", t.ID, time.Since(started).Round(time.Millisecond), ctx.Err())
		}
	}()
	applog.Debugf("dispatch: transfer %d: starting %s %s with %d stream(s)", t.ID, t.Direction, filepath.Base(t.Src), streams)

	clients, err := d.factory(ctx, t.SiteID, streams)
	if err != nil || len(clients) == 0 {
		if err == nil {
			err = fmt.Errorf("no connections available")
		}
		d.finishWithError(ctx, t, fmt.Errorf("connect: %w", err))
		return
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	// The server may have granted fewer connections than asked for; hand the
	// unused slots back so the rest of the queue can use them.
	if len(clients) < streams {
		d.resize(t, len(clients))
	}

	// Cancellation watchdog: pkg/sftp can block indefinitely inside a copy
	// when a connection dies silently (acks never arrive), and closing the
	// file handle can't help — it needs the same mutex the copy holds.
	// Tearing down this transfer's dedicated connections always unblocks it,
	// so Pause/Cancel/shutdown free the dispatcher slot instead of leaking it.
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		select {
		case <-ctx.Done():
			for _, c := range clients {
				c.Close()
			}
		case <-watchdogDone:
		}
	}()

	var (
		progMu    sync.Mutex
		done      = t.BytesDone
		chunkDone map[int]int64
		chunkLen  map[int]int64
	)
	lastEvent, lastDB := time.Time{}, time.Now()

	emitProgress := func() {
		payload := map[string]any{"id": t.ID, "bytes": done, "size": t.Size}
		if len(chunkLen) > 0 {
			// Per-chunk fractions drive the segmented "hyperlane" bar: the
			// engine's parallelism made visible.
			fr := make([]float64, len(chunkLen))
			for i := range fr {
				if l := chunkLen[i]; l > 0 {
					fr[i] = float64(chunkDone[i]) / float64(l)
				}
			}
			payload["chunks"] = fr
		}
		d.sink.Emit("transfer:progress", payload)
	}

	// progress is called from several goroutines in chunked mode.
	progress := func(delta int64) {
		if lim := d.currentLimiter(); lim != nil {
			// Charge every byte: a single chunked read can exceed the burst
			// size, and clamping (rather than looping) would let the excess
			// through untracked and blow past the user's limit.
			for owed := delta; owed > 0; {
				n := owed
				if n > limiterBurst {
					n = limiterBurst
				}
				if err := lim.WaitN(ctx, int(n)); err != nil {
					break // cancelled: the copy is stopping anyway
				}
				owed -= n
			}
		}
		atomic.AddInt64(&d.windowBytes, delta)

		progMu.Lock()
		done += delta
		emit := time.Since(lastEvent) >= eventEvery
		if emit {
			lastEvent = time.Now()
		}
		persist := time.Since(lastDB) >= dbEvery
		if persist {
			lastDB = time.Now()
		}
		snapshot := done
		if emit {
			emitProgress()
		}
		progMu.Unlock()

		if persist {
			if err := d.store.UpdateTransferProgress(t.ID, snapshot); err != nil {
				log.Printf("dispatch: progress %d: %v", t.ID, err)
			}
		}
	}

	ranges, chunked := d.chunkPlan(t, clients, streams)
	// A chunked transfer that is part way through must never be dropped onto
	// the linear path just because the connections were not there this time:
	// the fallback below deletes its .wschunk and its saved plan, which on
	// this app's workload is tens of gigabytes of transferred bytes thrown
	// away. A server that grants one connection where two were asked for is
	// ordinary (the "granted 1/4" line in the log), so this is not rare.
	// Requeue instead — capacity errors retry with backoff, and the plan is
	// still there when the connections are.
	if !chunked && streams >= 2 && len(clients) < 2 && d.hasChunkProgress(t.ID) {
		d.finishWithError(ctx, t, fmt.Errorf(
			"too many connections in use: a part-transferred file needs 2, the server granted %d", len(clients)))
		return
	}
	// The two engines lay their partial files out differently (chunked is
	// preallocated and sparse; linear is a growing prefix), so whichever
	// runs must clear the other's leftovers — adopting the wrong one would
	// publish a file full of holes.
	if chunked {
		d.removeTransferPart(t, first(clients), sftpfast.PartSuffix)
	} else {
		d.removeTransferPart(t, first(clients), sftpfast.ChunkPartSuffix)
		if derr := d.store.DeleteChunks(t.ID); derr != nil {
			log.Printf("dispatch: clear chunk plan %d: %v", t.ID, derr)
		}
	}

	// The engine reports the offset it actually resumed from, so bytes_done
	// reflects reality even when the DB seed and the .wspart disagree.
	onStart := func(offset int64) {
		progMu.Lock()
		done = offset
		progMu.Unlock()
	}
	onChunkProgress := func(idx int, delta int64) {
		progMu.Lock()
		chunkDone[idx] += delta
		progMu.Unlock()
		progress(delta)
	}
	onChunkCheckpoint := func(idx int, cdone int64) {
		if cerr := d.store.UpdateChunkProgress(t.ID, idx, cdone, "checkpoint"); cerr != nil {
			log.Printf("dispatch: chunk %d/%d checkpoint: %v", t.ID, idx, cerr)
		}
	}

	if chunked {
		chunkDone = make(map[int]int64, len(ranges))
		chunkLen = make(map[int]int64, len(ranges))
		var resumed int64
		for _, r := range ranges {
			chunkLen[r.Idx] = r.Length
			chunkDone[r.Idx] = r.Done
			resumed += r.Done
		}
		done = resumed
		if t.Direction == "upload" {
			err = sftpfast.UploadChunks(ctx, clients, t.Src, t.Dst, t.Size, ranges, onChunkProgress, onChunkCheckpoint)
		} else {
			err = sftpfast.DownloadChunks(ctx, clients, t.Src, t.Dst, t.Size, ranges, onChunkProgress, onChunkCheckpoint)
		}
		if err == nil {
			if cerr := d.store.DeleteChunks(t.ID); cerr != nil {
				log.Printf("dispatch: clear chunks %d: %v", t.ID, cerr)
			}
		} else if errors.Is(err, sftpfast.ErrChunkPreallocUnsupported) {
			// The server will not size the part file up front, so the chunked
			// resume guard cannot be made sound. Drop the plan and finish this
			// attempt on the single-stream path rather than burning a retry:
			// core.Classify buckets this sentinel as ClassPermanent.
			if cerr := d.store.DeleteChunks(t.ID); cerr != nil {
				log.Printf("dispatch: clear chunk plan %d: %v", t.ID, cerr)
			}
			d.removeTransferPart(t, first(clients), sftpfast.ChunkPartSuffix)
			// Clearing chunkLen too: a stale map would keep painting a
			// segmented bar for a transfer that is now one stream.
			progMu.Lock()
			done, chunkLen, chunkDone = 0, nil, nil
			progMu.Unlock()
			err = clients[0].Upload(ctx, t.Src, t.Dst, onStart, progress)
		} else if errors.Is(err, sftpfast.ErrChunkStateLost) {
			// The partial file backing those offsets is gone or altered.
			// Drop the checkpoints and requeue for a clean run rather than
			// assembling zeros where the "done" ranges should be.
			if cerr := d.store.DeleteChunks(t.ID); cerr != nil {
				log.Printf("dispatch: reset lost chunks %d: %v", t.ID, cerr)
			}
			d.removeTransferPart(t, first(clients), sftpfast.ChunkPartSuffix)
			if uerr := d.store.UpdateTransferProgress(t.ID, 0); uerr != nil {
				log.Printf("dispatch: reset progress %d: %v", t.ID, uerr)
			}
			if ctx.Err() == nil && t.Attempt+1 < maxAttempts {
				// State is now a genuinely clean slate, so requeue for an
				// immediate fresh run. finishWithError cannot: Classify
				// buckets this message as ClassPermanent and would fail the
				// row outright, contradicting the restart promised above.
				msg := "partial file no longer matched its checkpoints — restarting from zero"
				next := time.Now().UTC().Format(time.RFC3339)
				ok, serr := d.store.ScheduleRetryIfActive(t.ID, next, &msg)
				if serr != nil {
					log.Printf("dispatch: restart %d: %v", t.ID, serr)
				}
				if !ok {
					d.settleStopped(t)
					return
				}
				d.emitStateSrc(t.ID, t.Src, "pending", msg)
				d.sink.Emit("transfer:progress", map[string]any{"id": t.ID, "bytes": int64(0), "size": t.Size})
				return // deferred release/Wake still run
			}
			err = fmt.Errorf("partial file no longer matches its checkpoints: %w", err)
		}
	} else if t.Direction == "upload" {
		err = clients[0].Upload(ctx, t.Src, t.Dst, onStart, progress)
	} else {
		err = clients[0].Download(ctx, t.Src, t.Dst, onStart, progress)
	}

	progMu.Lock()
	final := done
	progMu.Unlock()
	if uerr := d.store.UpdateTransferProgress(t.ID, final); uerr != nil {
		log.Printf("dispatch: final progress %d: %v", t.ID, uerr)
	}

	if err == nil {
		// Deliberately NOT guarded on the row still being active, unlike
		// every other terminal write. The engine has already renamed the
		// placeholder into place, so the file is complete and real; a
		// cancel or pause that landed during that tail arrived too late to
		// change anything on disk. "Completed" is the only honest state —
		// "cancelled" would discard nothing (the placeholders are gone),
		// show 0 bytes beside a finished file, and for an upload whose
		// rename replaced an older remote file, honouring the cancel by
		// deleting would destroy the user's data.
		if serr := d.store.SetTransferState(t.ID, "completed", nil); serr != nil {
			log.Printf("dispatch: complete %d: %v", t.ID, serr)
		}
		d.sink.Emit("transfer:progress", map[string]any{"id": t.ID, "bytes": final, "size": t.Size})
		d.emitStateSrc(t.ID, t.Src, "completed", "")
		// A finished transfer changes a directory someone may be looking at.
		d.sink.Emit("fs:changed", map[string]any{
			"source": map[bool]string{true: "remote", false: "local"}[t.Direction == "upload"],
			"siteId": t.SiteID,
			"dir":    parentDir(t.Dst, t.Direction == "upload"),
		})
		return
	}
	d.finishWithError(ctx, t, err)
}

// hasChunkProgress reports whether a saved chunk plan holds bytes that would
// be lost by falling back to the linear path. A plan with nothing
// transferred yet is not worth protecting — rebuilding it costs nothing.
func (d *Dispatcher) hasChunkProgress(id int64) bool {
	chunks, err := d.store.Chunks(id)
	if err != nil {
		// Unreadable plan: assume there is something to protect rather than
		// deleting on the strength of a failed query.
		log.Printf("dispatch: chunk progress %d: %v", id, err)
		return true
	}
	for _, c := range chunks {
		if c.BytesDone > 0 {
			return true
		}
	}
	return false
}

// chunkPlan builds (or resumes) the byte-range plan for a chunked transfer.
// Returns ok=false whenever the single-connection path should be used.
func (d *Dispatcher) chunkPlan(t queue.Transfer, clients []*sftpfast.Client, streams int) ([]sftpfast.ChunkRange, bool) {
	if streams < 2 || len(clients) < 2 || t.Size <= 0 {
		return nil, false
	}
	// The plan is only valid against the exact file it was built for: both
	// size and mtime must still match, or a resumed range would splice new
	// content into old offsets. The source is local for an upload and remote
	// for a download; everything downstream is the same either way.
	var size, mtime int64
	if t.Direction == "upload" {
		st, serr := os.Stat(t.Src)
		if serr != nil || st.IsDir() {
			return nil, false
		}
		size, mtime = st.Size(), st.ModTime().Unix()
	} else {
		var serr error
		size, mtime, serr = clients[0].StatRemote(t.Src)
		if serr != nil {
			return nil, false
		}
	}
	changed := size != t.Size || (t.SrcMtime != 0 && mtime != t.SrcMtime)
	if changed {
		if derr := d.store.DeleteChunks(t.ID); derr != nil {
			log.Printf("dispatch: reset chunks %d: %v", t.ID, derr)
		}
		d.removeTransferPart(t, first(clients), sftpfast.ChunkPartSuffix)
		if size != t.Size {
			return nil, false // size drift also invalidates the queued row
		}
	}

	saved, err := d.store.Chunks(t.ID)
	if err != nil {
		log.Printf("dispatch: load chunks %d: %v", t.ID, err)
		return nil, false
	}
	if len(saved) < 2 {
		saved = queue.PlanChunks(t.ID, t.Size, len(clients))
		if len(saved) < 2 {
			return nil, false
		}
		if serr := d.store.SaveChunks(t.ID, saved); serr != nil {
			log.Printf("dispatch: save chunks %d: %v", t.ID, serr)
			return nil, false
		}
		if serr := d.store.SetTransferSrcMtime(t.ID, mtime); serr != nil {
			log.Printf("dispatch: record src mtime %d: %v", t.ID, serr)
		}
	}

	ranges := make([]sftpfast.ChunkRange, 0, len(saved))
	var covered int64
	for i, c := range saved {
		if c.BytesDone < 0 || c.BytesDone > c.Length {
			return nil, false // corrupt checkpoint: fall back to a clean run
		}
		// The engines' completeness gate is pure byte accounting, so an
		// overlapping set summing to size would pass it and publish a file
		// with a hole. Contiguity is checked here, once, for both directions.
		if i == 0 && c.Offset != 0 {
			return nil, false
		}
		if i > 0 && c.Offset != saved[i-1].Offset+saved[i-1].Length {
			return nil, false
		}
		covered += c.Length
		ranges = append(ranges, sftpfast.ChunkRange{
			Idx: c.Idx, Offset: c.Offset, Length: c.Length, Done: c.BytesDone,
		})
	}
	if covered != t.Size {
		return nil, false
	}
	return ranges, true
}

// finishWithError applies pause/cancel intent (already written to the DB by
// the control methods) or the retry ladder. c is this transfer's connection
// where one was opened, so a cancelled upload can still reach its remote
// placeholder; nil when the dial itself failed.
func (d *Dispatcher) finishWithError(ctx context.Context, t queue.Transfer, err error) {
	if ctx.Err() != nil {
		if d.settleStopped(t) {
			return
		}
		d.mu.Lock()
		byQueue := d.requeue[t.ID]
		d.mu.Unlock()
		if byQueue || d.stopping.Load() {
			// The queue was paused or the app is closing. Neither is a
			// transfer failure, so the row goes back to pending from a
			// clean slate — attempts reset, no error — with its bytes
			// intact. Left to the classifier, "context canceled" is
			// permanent and would fail the row; "use of closed network
			// connection" is transient and would put it on the backoff
			// ladder. Both are the wrong story for a stop the user asked
			// for.
			ok, rerr := d.store.Requeue(t.ID)
			if rerr != nil {
				// The row stays 'active' for RecoverInterrupted to requeue
				// on the next launch. Falling through would hand a pause
				// to the classifier, which files "context canceled" as a
				// permanent failure.
				log.Printf("dispatch: requeue %d: %v (left active for recovery)", t.ID, rerr)
				return
			} else if ok {
				d.emitStateSrc(t.ID, t.Src, "pending", "")
				return
			} else if d.settleStopped(t) {
				// Requeue found the row no longer active: the user moved
				// it between the read above and now. Their choice stands.
				return
			}
		}
	}

	// Every write below is guarded on the row still being active. The
	// user can cancel, pause or requeue a row at any point while the engine
	// is unwinding, and an unguarded write here would overwrite that with
	// a retry or a failure — the row they just cancelled coming back as
	// queued. When the guard refuses, the row is settled as it stands.
	msg := err.Error()
	class := core.Classify(err)
	retryable := class == core.ClassTransient || class == core.ClassCapacity
	if retryable && t.Attempt+1 < maxAttempts {
		backoff := time.Duration(1<<uint(t.Attempt)) * 5 * time.Second
		next := time.Now().UTC().Add(backoff).Format(time.RFC3339)
		ok, serr := d.store.ScheduleRetryIfActive(t.ID, next, &msg)
		if serr != nil {
			log.Printf("dispatch: retry %d: %v", t.ID, serr)
		}
		if !ok {
			d.settleStopped(t)
			return
		}
		d.emitStateSrc(t.ID, t.Src, "pending", msg)
		return
	}
	ok, serr := d.store.FinishActive(t.ID, "failed", &msg)
	if serr != nil {
		log.Printf("dispatch: fail %d: %v", t.ID, serr)
	}
	if !ok {
		d.settleStopped(t)
		return
	}
	d.emitStateSrc(t.ID, t.Src, "failed", msg)
}

// settleStopped finishes a transfer whose row is no longer the engine's:
// while the goroutine was running or unwinding, the user cancelled,
// paused or requeued it (Pause/Cancel/Resume write the desired state
// first, so the state IS the intent), or a queue pause requeued it. The
// row is left exactly as it stands and announced; a cancelled one has its
// leftovers discarded — safe here and nowhere earlier, because the engine
// has returned and nothing is still writing to the placeholders. Reports
// false only when the row is still active, i.e. still the caller's to
// finish.
func (d *Dispatcher) settleStopped(t queue.Transfer) bool {
	cur, err := d.store.TransferByID(t.ID)
	if err != nil || cur.State == "active" {
		return false
	}
	if cur.State == "cancelled" {
		d.discardPartials(t, nil)
	}
	d.emitStateSrc(t.ID, t.Src, cur.State, "")
	return true
}

// Pause stops an active transfer keeping its .wspart (byte-resume) or parks
// a pending one.
func (d *Dispatcher) Pause(id int64) error {
	if err := d.store.SetTransferState(id, "paused", nil); err != nil {
		return err
	}
	d.cancelIfRunning(id)
	return nil
}

// Resume requeues a paused/failed transfer; the engine resumes at the
// recorded .wspart offset.
func (d *Dispatcher) Resume(id int64) error {
	if err := d.store.SetTransferState(id, "pending", nil); err != nil {
		return err
	}
	d.emitState(id, "pending", "")
	d.Wake()
	return nil
}

// discardPartials throws away everything a cancelled transfer left behind:
// both placeholder kinds, the saved chunk plan and the byte count.
//
// Cancel is not Pause. Nothing is coming back for these bytes, and a
// cancelled 50 GB chunked upload otherwise leaves a full-size .wschunk on
// the seedbox — sparse on the filesystem, but seedbox quotas are billed on
// apparent size, so the user pays for a file they cancelled.
//
// It re-reads the row and does nothing unless it is still cancelled. The
// caller's view can be stale by the time this runs: pump claims from a list
// it read earlier and rewrites the row to active, so a cancel that lost that
// race would otherwise delete the placeholders of a transfer that is
// starting up. Deleting bytes is not something to do on a stale read.
//
// The placeholder path is derived from the destination alone, so a second
// queue row aimed at the same destination owns these files too; deleting
// them would reset that row to zero or unlink a file it is writing. When one
// exists, the files stay and only this row's bookkeeping is cleared. No
// batch list is needed for that: OtherLiveTransfersForDst never counts a
// cancelled row as an owner, so rows cancelled together cannot each mistake
// the other for one.
//
// c is a connection to reach an upload's remote placeholders with, shared
// across a batch; nil means dial a short-lived one, which is what the
// single-row callers do.
func (d *Dispatcher) discardPartials(t queue.Transfer, c *sftpfast.Client) bool {
	if d.stopping.Load() && t.Direction == "upload" {
		// Shutdown is closing the sessions and the database behind this;
		// a remote delete could take a 30 s dial the grace does not cover,
		// and its bookkeeping would then land on a closed store. The row
		// stays cancelled with its bytes recorded, and Clear done sweeps
		// the placeholder next time the site is reachable. A download's
		// placeholder is a local unlink, so it goes now as promised.
		log.Printf("dispatch: cancel %d: shutting down, remote placeholders left for Clear done", t.ID)
		return true
	}
	cur, err := d.store.TransferByID(t.ID)
	if err != nil || cur.State != "cancelled" {
		applog.Debugf("dispatch: cancel %d: no longer cancelled (%v), leaving its data alone", t.ID, err)
		return true
	}
	if cur.BytesDone <= 0 {
		// Nothing was ever written, so there are no placeholders and nothing
		// to reset. Skipping the work matters: "Skip all" on a folder of
		// held uploads would otherwise dial a fresh SSH connection per row
		// to delete files that were never created.
		return true
	}
	if n, err := d.store.OtherLiveTransfersForDst(nil, t.Dst); err != nil {
		log.Printf("dispatch: cancel %d: dst owners: %v", t.ID, err)
		return false
	} else if n > 0 {
		applog.Debugf("dispatch: cancel %d: %d other row(s) still target %s, leaving placeholders", t.ID, n, t.Dst)
		return true
	}

	var gone bool
	if t.Direction == "upload" {
		// The placeholders are on the server, and this transfer's own
		// connections are already gone: cancelling closes them, which is the
		// only reliable way to unblock a copy stuck inside pkg/sftp. So the
		// cleanup borrows the batch's connection, or dials its own briefly.
		if c == nil {
			ctx, cancel := context.WithTimeout(context.Background(), cleanupDialTimeout)
			defer cancel()
			cs, derr := d.factory(ctx, t.SiteID, 1)
			if derr != nil || len(cs) == 0 {
				log.Printf("dispatch: cancel %d: no connection to remove remote placeholders: %v", t.ID, derr)
				return false
			}
			defer cs[0].Close()
			c = cs[0]
		}
		gone = d.removeTransferPart(t, c, sftpfast.PartSuffix)
		// Both attempted, never short-circuited: they are two different
		// files and the second is the expensive one to strand.
		gone = d.removeTransferPart(t, c, sftpfast.ChunkPartSuffix) && gone
	} else {
		gone = removePart(t.Dst + sftpfast.PartSuffix)
		gone = removePart(t.Dst+sftpfast.ChunkPartSuffix) && gone
	}
	if !gone {
		// A shared connection that died mid-batch, a server that refused,
		// a local file still locked. The row keeps its byte count so the
		// leftovers stay accounted for and Clear done can sweep them when
		// the site is reachable again; zeroing it here would leave a
		// full-size file with nothing pointing at it.
		log.Printf("dispatch: cancel %d: placeholders not removed, left for Clear done", t.ID)
		return false
	}

	if err := d.store.DeleteChunks(t.ID); err != nil {
		log.Printf("dispatch: cancel %d: clear chunk plan: %v", t.ID, err)
	}
	if err := d.store.UpdateTransferProgress(t.ID, 0); err != nil {
		log.Printf("dispatch: cancel %d: reset progress: %v", t.ID, err)
	}
	d.sink.Emit("transfer:progress", map[string]any{"id": t.ID, "bytes": int64(0), "size": t.Size})
	return true
}

// ActiveCount reports how many transfers are running right now. It reads only
// in-memory state — never the database — because the close guard calls it from
// the Windows UI thread, where a blocked query would freeze the message pump
// that has to paint the dialog we are about to ask for.
func (d *Dispatcher) ActiveCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cancels)
}

// Cancel aborts one transfer and marks it cancelled. One path with the
// bulk cancel, so a single row and a batch cannot drift apart: the mark is
// guarded (a row that completed a moment before the click stays
// completed), the conflict is cleared, and the follow-through is the same.
func (d *Dispatcher) Cancel(id int64) error {
	_, err := d.CancelMany([]int64{id})
	return err
}

// CancelMany cancels the named rows: queued, paused and running alike. The
// rows are marked cancelled in one pass, so the state is already right when
// each running goroutine's engine lets go; a queued or paused row has no
// goroutine to clean up after it, so its leftovers are discarded here, the
// same as Cancel does one at a time. One queue:changed at the end, not one
// per row — a folder of two thousand files cancelled in one click must not
// refetch the list two thousand times.
func (d *Dispatcher) CancelMany(ids []int64) (int, error) {
	// Swept even when a later batch failed: the rows the earlier batches
	// marked are cancelled in the database, and a running one among them
	// must be stopped or it would finish under a cancelled row.
	marked, err := d.store.CancelByID(ids)
	serr := d.sweepCancelled(marked)
	if err == nil {
		err = serr
	}
	return len(marked), err
}

// CancelQueued cancels everything waiting — pending, held for a decision,
// or paused — and leaves running transfers alone. Marked by state, not by
// id, so a row queued while the confirmation sat open is cancelled with
// the rest rather than left as the one survivor of "cancel everything".
func (d *Dispatcher) CancelQueued() (int, error) {
	marked, err := d.store.CancelQueued()
	serr := d.sweepCancelled(marked)
	if err == nil {
		err = serr
	}
	return len(marked), err
}

// sweepCancelled runs the cancel follow-through for the rows a mark
// statement reported changing — exactly those, so a row the pump claimed
// meanwhile is neither cut off nor counted as gone from its destination,
// and a row that arrived meanwhile is not left with its placeholder.
//
// Two passes, in this order. Running rows are stopped first — every
// marked id, straight from the mark, before a single read or emit — so the
// window in which a running transfer can finish or fail under a
// 'cancelled' row is as short as it can be (the terminal writes are
// state-guarded as well, so even that window is safe). Idle rows'
// placeholders come second, in the background: an idle upload's discard
// needs a connection, so the count and the list refresh return at once and
// the rows read as cancelled while the remote deletes happen. Stop waits
// for the sweep; discardPartials re-reads each row, so a Clear done that
// overtakes it is harmless.
//
// The idle discards pass no `going` batch: OtherLiveTransfersForDst never
// counts cancelled rows, and every marked row is cancelled, so the list
// would be redundant — and at a folder of tens of thousands of files it
// would exceed SQLite's bound-parameter ceiling and fail every discard.
func (d *Dispatcher) sweepCancelled(marked []int64) error {
	for _, id := range marked {
		d.cancelIfRunning(id) // no-op for a row with no goroutine
	}
	d.sink.Emit("queue:changed", nil)
	rows, err := d.store.TransfersByID(marked)
	if err != nil {
		return err
	}
	idle := rows[:0:0]
	for _, t := range rows {
		d.mu.Lock()
		_, running := d.cancels[t.ID]
		d.mu.Unlock()
		// A running row discards on its own way out (settleStopped), or
		// in release if its claim never completed.
		if !running && t.BytesDone > 0 {
			idle = append(idle, t)
		}
	}
	if len(idle) == 0 {
		return nil
	}
	d.startSweep(func() { d.discardBatch(idle) })
	return nil
}

// discardBatch throws away the leftovers of rows cancelled together,
// opening at most one connection per site instead of one per row. An
// upload's placeholders are on the server, so each row's discard needs a
// connection; dialling per row meant a folder of part-uploaded files
// cancelled in one click waited out a separate 30 s timeout for every one
// of them against a seedbox that was not answering — half an hour of
// dialling for fifty rows. A site that refuses once is not asked again in
// this batch, for the same reason.
//
// The list is refreshed as it goes rather than only at the end, so the
// rows stop showing byte counts for data that is already gone.
func (d *Dispatcher) discardBatch(rows []queue.Transfer) {
	defer d.sink.Emit("queue:changed", nil)
	conns := make(map[int64]*sftpfast.Client)
	refused := make(map[int64]bool)
	redialed := make(map[int64]bool)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i, t := range rows {
		var c *sftpfast.Client
		if t.Direction == "upload" {
			if d.stopping.Load() {
				// discardPartials would refuse these anyway; stop dialling.
				return
			}
			if refused[t.SiteID] {
				continue
			}
			if c = conns[t.SiteID]; c == nil {
				ctx, cancel := context.WithTimeout(context.Background(), cleanupDialTimeout)
				cs, derr := d.factory(ctx, t.SiteID, 1)
				cancel()
				if derr != nil || len(cs) == 0 {
					log.Printf("dispatch: cancel: no connection to site %d to remove remote placeholders: %v", t.SiteID, derr)
					refused[t.SiteID] = true
					continue
				}
				c = cs[0]
				conns[t.SiteID] = c
			}
		}
		if !d.discardPartials(t, c) && t.Direction == "upload" {
			// A shared session can die partway through a long batch (an
			// idle timeout, a session limit). Drop it and re-dial once for
			// this site; a second failure stops us asking again in this
			// batch rather than paying a timeout for every row left.
			if dead := conns[t.SiteID]; dead != nil {
				dead.Close()
				delete(conns, t.SiteID)
			}
			if redialed[t.SiteID] {
				refused[t.SiteID] = true
			}
			redialed[t.SiteID] = true
		}
		if i%25 == 24 {
			d.sink.Emit("queue:changed", nil)
		}
	}
}

// SetPaused stops or restarts the whole queue. Pausing cancels every
// running transfer's context; finishWithError sees the flag and requeues
// each one clean, so they resume from their placeholders — a pause, not a
// failure. The flag is persisted first, so a crash between the write and
// the cancel still comes back paused. Resuming just nudges the pump.
func (d *Dispatcher) SetPaused(on bool) error {
	value := "0"
	if on {
		value = "1"
	}
	if err := d.store.SetSetting("queue.paused", value); err != nil {
		return fmt.Errorf("persist queue pause: %w", err)
	}
	if on {
		d.paused.Store(true)
		n := d.stopAll(true)
		log.Printf("queue: paused (%d running transfer(s) stopping)", n)
	} else {
		// Heal any row a failed requeue left 'active' with no goroutine:
		// the pump only lists pending rows, so nothing else would ever
		// pick it up again in this run. Rows still unwinding are excluded.
		//
		// Before the pump is let back in, not after: once paused is false a
		// pump pass can claim a row this snapshot did not name, and the
		// reset would then wind a genuinely running transfer back to
		// pending underneath its own goroutine.
		d.mu.Lock()
		running := make([]int64, 0, len(d.cancels))
		for id := range d.cancels {
			running = append(running, id)
		}
		d.mu.Unlock()
		if n, err := d.store.RequeueExcept(running); err != nil {
			log.Printf("queue: resume: requeue orphans: %v", err)
		} else if n > 0 {
			log.Printf("queue: resumed (%d orphaned row(s) requeued)", n)
		} else {
			log.Printf("queue: resumed")
		}
		d.paused.Store(false)
	}
	d.sink.Emit("queue:paused", map[string]any{"paused": on})
	if !on {
		d.Wake()
	}
	return nil
}

// Paused reports the queue-wide pause. In-memory only, so it is safe from
// any thread, including the UI thread the close guard runs on.
func (d *Dispatcher) Paused() bool { return d.paused.Load() }

func (d *Dispatcher) cancelIfRunning(id int64) {
	d.mu.Lock()
	cancel, ok := d.cancels[id]
	if ok {
		d.cancelledAt[id] = time.Now()
	}
	d.mu.Unlock()
	if ok {
		applog.Debugf("dispatch: transfer %d: stop requested", id)
		cancel()
	}
}

func (d *Dispatcher) emitState(id int64, state, errMsg string) {
	d.emitStateSrc(id, "", state, errMsg)
}

// emitStateSrc is emitState with the source path in the payload. The UI
// names the transfer from its own row list, but a row claimed straight
// after enqueue can go active before that list has caught up; the path
// lets the session log still say which file, instead of "transfer #412".
func (d *Dispatcher) emitStateSrc(id int64, src, state, errMsg string) {
	payload := map[string]any{"id": id, "state": state}
	if src != "" {
		payload["src"] = src
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	d.sink.Emit("transfer:state", payload)
	d.sink.Emit("queue:changed", nil)
}
