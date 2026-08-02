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
	defaultGlobalCap = 6
	defaultSiteCap   = 3
	maxAttempts      = 3
	// Chunked (multi-connection) downloads: default threshold and stream
	// count. A server that caps per-connection speed is the whole reason
	// this exists — one big file otherwise runs at one connection's rate.
	defaultChunkMinMB  = 256
	defaultChunkStream = 4
	maxChunkStreams    = 16
	// progress cadence: events ≤4Hz per transfer, DB checkpoint every 3s
	eventEvery = 250 * time.Millisecond
	dbEvery    = 3 * time.Second
	// limiter burst: one pipeline window so throttling stays smooth
	limiterBurst = 1 << 20
)

type Dispatcher struct {
	store   *queue.Store
	sink    events.Sink
	factory Factory
	wake    chan struct{}

	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
	slots   map[int64]int // connections reserved per active transfer
	perSite map[int64]int
	activeN int

	limMu       sync.Mutex
	limiter     *rate.Limiter // nil = unthrottled
	windowBytes int64         // atomic: bytes moved since last tick
	observedMax float64       // best aggregate rate seen (bytes/sec)
}

func New(store *queue.Store, sink events.Sink, factory Factory) *Dispatcher {
	return &Dispatcher{
		store:   store,
		sink:    sink,
		factory: factory,
		wake:    make(chan struct{}, 1),
		cancels: make(map[int64]context.CancelFunc),
		slots:   make(map[int64]int),
		perSite: make(map[int64]int),
	}
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
	for _, t := range pending {
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
		if d.activeN+streams > globalCap || d.perSite[t.SiteID]+streams > siteCap {
			// Let a multi-stream transfer through alone rather than starving
			// it forever when the caps are smaller than its stream count.
			if !(d.activeN == 0 && d.perSite[t.SiteID] == 0) {
				d.mu.Unlock()
				continue
			}
		}
		if _, running := d.cancels[t.ID]; running {
			d.mu.Unlock()
			continue
		}
		tctx, cancel := context.WithCancel(ctx)
		d.cancels[t.ID] = cancel
		d.slots[t.ID] = streams
		d.perSite[t.SiteID] += streams
		d.activeN += streams
		d.mu.Unlock()

		if err := d.store.SetTransferState(t.ID, "active", nil); err != nil {
			log.Printf("dispatch: claim %d: %v", t.ID, err)
			d.release(t)
			continue
		}
		d.emitState(t.ID, "active", "")
		go d.runTransfer(tctx, t, streams)
	}
}

// streamsFor decides how many connections a transfer gets: >1 only for
// downloads of files past the chunking threshold.
func (d *Dispatcher) streamsFor(t queue.Transfer, siteCap int) int {
	if t.Direction == "upload" || t.Engine != "sftpfast" {
		return 1
	}
	minMB := d.store.SettingInt("transfers.chunk_min_mb", defaultChunkMinMB)
	if minMB <= 0 || t.Size < int64(minMB)<<20 {
		return 1
	}
	streams := d.store.SettingInt("transfers.chunk_streams", defaultChunkStream)
	if streams < 2 {
		return 1
	}
	if streams > maxChunkStreams {
		streams = maxChunkStreams
	}
	// A transfer can never reserve more connections than the caps allow.
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

// parentDir returns the containing directory of a transfer destination,
// using the separator convention of the side it lives on.
func parentDir(dst string, remote bool) string {
	if remote {
		return path.Dir(dst)
	}
	return filepath.Dir(dst)
}

// removePart deletes a leftover partial file, ignoring absence.
func removePart(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("dispatch: remove %s: %v", path, err)
	}
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
	}
	d.mu.Unlock()
	d.Wake()
}

func (d *Dispatcher) release(t queue.Transfer) {
	d.mu.Lock()
	if slots, ok := d.slots[t.ID]; ok {
		delete(d.cancels, t.ID)
		delete(d.slots, t.ID)
		d.perSite[t.SiteID] -= slots
		d.activeN -= slots
	}
	d.mu.Unlock()
}

func (d *Dispatcher) runTransfer(ctx context.Context, t queue.Transfer, streams int) {
	defer d.release(t)
	defer d.Wake() // a freed slot may unblock the next pending row

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
	// The two engines lay their partial files out differently (chunked is
	// preallocated and sparse; linear is a growing prefix), so whichever
	// runs must clear the other's leftovers — adopting the wrong one would
	// publish a file full of holes.
	if chunked {
		removePart(t.Dst + sftpfast.PartSuffix)
	} else {
		removePart(t.Dst + sftpfast.ChunkPartSuffix)
		if derr := d.store.DeleteChunks(t.ID); derr != nil {
			log.Printf("dispatch: clear chunk plan %d: %v", t.ID, derr)
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
		err = sftpfast.DownloadChunks(ctx, clients, t.Src, t.Dst, t.Size, ranges,
			func(idx int, delta int64) {
				progMu.Lock()
				chunkDone[idx] += delta
				progMu.Unlock()
				progress(delta)
			},
			func(idx int, cdone int64) {
				if cerr := d.store.UpdateChunkProgress(t.ID, idx, cdone, "checkpoint"); cerr != nil {
					log.Printf("dispatch: chunk %d/%d checkpoint: %v", t.ID, idx, cerr)
				}
			})
		if err == nil {
			if cerr := d.store.DeleteChunks(t.ID); cerr != nil {
				log.Printf("dispatch: clear chunks %d: %v", t.ID, cerr)
			}
		} else if errors.Is(err, sftpfast.ErrChunkStateLost) {
			// The partial file backing those offsets is gone or altered.
			// Drop the checkpoints and requeue for a clean run rather than
			// assembling zeros where the "done" ranges should be.
			if cerr := d.store.DeleteChunks(t.ID); cerr != nil {
				log.Printf("dispatch: reset lost chunks %d: %v", t.ID, cerr)
			}
			removePart(t.Dst + sftpfast.ChunkPartSuffix)
			if uerr := d.store.UpdateTransferProgress(t.ID, 0); uerr != nil {
				log.Printf("dispatch: reset progress %d: %v", t.ID, uerr)
			}
			err = fmt.Errorf("partial file no longer matches its checkpoints — restarting: %w", err)
		}
	} else {
		// The engine reports the offset it actually resumed from, so
		// bytes_done reflects reality even when the DB seed and the
		// .wspart disagree.
		onStart := func(offset int64) {
			progMu.Lock()
			done = offset
			progMu.Unlock()
		}
		if t.Direction == "upload" {
			err = clients[0].Upload(ctx, t.Src, t.Dst, onStart, progress)
		} else {
			err = clients[0].Download(ctx, t.Src, t.Dst, onStart, progress)
		}
	}

	progMu.Lock()
	final := done
	progMu.Unlock()
	if uerr := d.store.UpdateTransferProgress(t.ID, final); uerr != nil {
		log.Printf("dispatch: final progress %d: %v", t.ID, uerr)
	}

	if err == nil {
		if serr := d.store.SetTransferState(t.ID, "completed", nil); serr != nil {
			log.Printf("dispatch: complete %d: %v", t.ID, serr)
		}
		d.sink.Emit("transfer:progress", map[string]any{"id": t.ID, "bytes": final, "size": t.Size})
		d.emitState(t.ID, "completed", "")
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

// chunkPlan builds (or resumes) the byte-range plan for a chunked download.
// Returns ok=false whenever the single-connection path should be used.
func (d *Dispatcher) chunkPlan(t queue.Transfer, clients []*sftpfast.Client, streams int) ([]sftpfast.ChunkRange, bool) {
	if streams < 2 || len(clients) < 2 || t.Direction == "upload" || t.Size <= 0 {
		return nil, false
	}
	// The plan is only valid against the exact file it was built for: both
	// size and mtime must still match, or a resumed range would splice new
	// content into old offsets.
	size, mtime, err := clients[0].StatRemote(t.Src)
	if err != nil {
		return nil, false
	}
	changed := size != t.Size || (t.SrcMtime != 0 && mtime != t.SrcMtime)
	if changed {
		if derr := d.store.DeleteChunks(t.ID); derr != nil {
			log.Printf("dispatch: reset chunks %d: %v", t.ID, derr)
		}
		removePart(t.Dst + sftpfast.ChunkPartSuffix)
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
	for _, c := range saved {
		if c.BytesDone < 0 || c.BytesDone > c.Length {
			return nil, false // corrupt checkpoint: fall back to a clean run
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
// the control methods) or the retry ladder.
func (d *Dispatcher) finishWithError(ctx context.Context, t queue.Transfer, err error) {
	if ctx.Err() != nil {
		// Pause/Cancel wrote the desired terminal state before cancelling;
		// the .wspart stays on disk so resume continues at the same offset.
		if cur, gerr := d.store.TransferByID(t.ID); gerr == nil &&
			(cur.State == "paused" || cur.State == "cancelled") {
			d.emitState(t.ID, cur.State, "")
			return
		}
	}

	msg := err.Error()
	class := core.Classify(err)
	retryable := class == core.ClassTransient || class == core.ClassCapacity
	if retryable && t.Attempt+1 < maxAttempts {
		backoff := time.Duration(1<<uint(t.Attempt)) * 5 * time.Second
		next := time.Now().UTC().Add(backoff).Format(time.RFC3339)
		if serr := d.store.ScheduleRetry(t.ID, next, &msg); serr != nil {
			log.Printf("dispatch: retry %d: %v", t.ID, serr)
		}
		d.emitState(t.ID, "pending", msg)
		return
	}
	if serr := d.store.SetTransferState(t.ID, "failed", &msg); serr != nil {
		log.Printf("dispatch: fail %d: %v", t.ID, serr)
	}
	d.emitState(t.ID, "failed", msg)
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

// Cancel aborts and marks cancelled.
func (d *Dispatcher) Cancel(id int64) error {
	if err := d.store.SetTransferState(id, "cancelled", nil); err != nil {
		return err
	}
	d.cancelIfRunning(id)
	d.emitState(id, "cancelled", "")
	return nil
}

func (d *Dispatcher) cancelIfRunning(id int64) {
	d.mu.Lock()
	cancel, ok := d.cancels[id]
	d.mu.Unlock()
	if ok {
		cancel()
	}
}

func (d *Dispatcher) emitState(id int64, state, errMsg string) {
	payload := map[string]any{"id": id, "state": state}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	d.sink.Emit("transfer:state", payload)
	d.sink.Emit("queue:changed", nil)
}
