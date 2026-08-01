// Package dispatch runs the transfer queue: claims pending rows, executes
// them on dedicated per-transfer connections (never the browse connection),
// coalesces progress events, and applies the two-level retry ladder.
package dispatch

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"warpseed/internal/engine/core"
	"warpseed/internal/engine/sftpfast"
	"warpseed/internal/events"
	"warpseed/internal/queue"
)

// Factory dials a fresh transfer connection for a site. Supplied by the app
// layer (it owns credentials and host-key pins).
type Factory func(ctx context.Context, siteID int64) (*sftpfast.Client, error)

const (
	defaultGlobalCap = 6
	defaultSiteCap   = 3
	maxAttempts      = 3
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
		d.mu.Lock()
		if d.activeN >= globalCap || d.perSite[t.SiteID] >= siteCap {
			d.mu.Unlock()
			continue
		}
		if _, running := d.cancels[t.ID]; running {
			d.mu.Unlock()
			continue
		}
		tctx, cancel := context.WithCancel(ctx)
		d.cancels[t.ID] = cancel
		d.perSite[t.SiteID]++
		d.activeN++
		d.mu.Unlock()

		if err := d.store.SetTransferState(t.ID, "active", nil); err != nil {
			log.Printf("dispatch: claim %d: %v", t.ID, err)
			d.release(t)
			continue
		}
		d.emitState(t.ID, "active", "")
		go d.runTransfer(tctx, t)
	}
}

func (d *Dispatcher) release(t queue.Transfer) {
	d.mu.Lock()
	if _, ok := d.cancels[t.ID]; ok {
		delete(d.cancels, t.ID)
		d.perSite[t.SiteID]--
		d.activeN--
	}
	d.mu.Unlock()
}

func (d *Dispatcher) runTransfer(ctx context.Context, t queue.Transfer) {
	defer d.release(t)
	defer d.Wake() // a freed slot may unblock the next pending row

	client, err := d.factory(ctx, t.SiteID)
	if err != nil {
		d.finishWithError(ctx, t, fmt.Errorf("connect: %w", err))
		return
	}
	defer client.Close()

	// Cancellation watchdog: pkg/sftp can block indefinitely inside a copy
	// when a connection dies silently (acks never arrive), and closing the
	// file handle can't help — it needs the same mutex the copy holds.
	// Tearing down this transfer's dedicated connection always unblocks it,
	// so Pause/Cancel/shutdown free the dispatcher slot instead of leaking it.
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-watchdogDone:
		}
	}()

	done := t.BytesDone
	lastEvent, lastDB := time.Time{}, time.Now()
	progress := func(delta int64) {
		done += delta
		atomic.AddInt64(&d.windowBytes, delta)
		if lim := d.currentLimiter(); lim != nil {
			n := delta
			if n > limiterBurst {
				n = limiterBurst
			}
			_ = lim.WaitN(ctx, int(n)) // throttle by blocking the copy pipeline
		}
		if time.Since(lastEvent) >= eventEvery {
			lastEvent = time.Now()
			d.sink.Emit("transfer:progress", map[string]any{
				"id": t.ID, "bytes": done, "size": t.Size,
			})
		}
		if time.Since(lastDB) >= dbEvery {
			lastDB = time.Now()
			if err := d.store.UpdateTransferProgress(t.ID, done); err != nil {
				log.Printf("dispatch: progress %d: %v", t.ID, err)
			}
		}
	}

	// The engine reports the offset it actually resumed from, so bytes_done
	// reflects reality even when the DB seed and the .wspart disagree.
	onStart := func(offset int64) { done = offset }
	if t.Direction == "upload" {
		err = client.Upload(ctx, t.Src, t.Dst, onStart, progress)
	} else {
		err = client.Download(ctx, t.Src, t.Dst, onStart, progress)
	}
	if uerr := d.store.UpdateTransferProgress(t.ID, done); uerr != nil {
		log.Printf("dispatch: final progress %d: %v", t.ID, uerr)
	}

	if err == nil {
		if serr := d.store.SetTransferState(t.ID, "completed", nil); serr != nil {
			log.Printf("dispatch: complete %d: %v", t.ID, serr)
		}
		d.sink.Emit("transfer:progress", map[string]any{"id": t.ID, "bytes": done, "size": t.Size})
		d.emitState(t.ID, "completed", "")
		return
	}
	d.finishWithError(ctx, t, err)
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
