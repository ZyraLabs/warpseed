# Close guard and close-to-pill — design

> **Provenance.** This design was produced by a design workflow on 2026-09-02 and
> was never committed; it survived only inside a workflow journal outside this
> repository and was recovered on 2026-09-08. It is reproduced here close to
> verbatim so it cannot be lost again. Line-number anchors are against the 1.1.0
> tree and several are now stale — see "Stale anchors" at the end before using
> them. Roadmap item 2.2 tracks the implementation.

## Verdict — what happens today
Today, closing warpseed with live transfers really is a hard kill and nothing warns you — main.go:17-32 registers only OnStartup and OnShutdown, so WM_CLOSE goes straight to winc.Exit, and app.go:172-182 closes the browse sessions and the SQLite store without cancelling or awaiting a single transfer goroutine. The good news is that this is much less destructive than it looks: internal/queue/store.go:112-120 (RecoverInterrupted) flips every 'active'/'dispatched' row back to 'pending' on next launch, partial data survives as .wspart / .wschunk, and internal/engine/sftpfast/chunked.go:36-37 checkpoints every 8 MiB or 30 s per lane. So an abrupt close costs roughly a second of transfer per lane, not a restart. Two real defects sit behind it: shutdown() closes the DB while workers may still be writing 'completed' (a 50 GB file that just finished can get re-transferred in full), and an interrupted .wschunk already shows the FINAL file size on both the local disk and the seedbox, with nothing in the codebase that ever sweeps an abandoned one.

The change: add Wails' OnBeforeClose (pkg/options/options.go:64 — verified present, and internal/frontend/desktop/windows/frontend.go:459-466 honours a true return, with winc/form.go:276 returning 0 for WM_CLOSE so the veto genuinely holds). It fires on the UI message-pump thread, so it must never block: it counts running transfers from the dispatcher's in-memory map, emits "app:close-requested", and returns true immediately. The dialog then offers Keep warpseed open / Close and resume later / Minimize to pill. With zero running transfers it returns false and the app closes instantly, exactly as today. Two escapes prevent a wedged frontend from making warpseed unclosable: the frontend acks receipt within 2 s or Go quits itself, and a second close gesture always quits.

On "close to taskbar": Wails v2.13.0 has no system-tray API at all (options.App has no tray field; pkg/menu/tray.go has zero callers), so the honest answer is the mini pill you already built — SetMiniMode at app.go:232-266. It keeps a title bar, a taskbar button and an Alt+Tab entry. We explicitly do NOT use HideWindowOnClose or WindowHide: with no tray, a hidden window is a process only Task Manager can find. Closing the pill itself is safe — it is the same window, so its X re-enters the same guard, and the guard restores the full window before showing the dialog.
## Copy — exact dialog strings
=== CLOSE-GUARD DIALOG (frontend/src/components/CloseGuardDialog.tsx) ===

Let n = payload.running (transfers in state 'active'/'dispatched' at the moment
of the close gesture, supplied by Go). Let q = the number of rows in the store
with state 'pending' or 'dispatched' minus nothing — computed on the frontend as
transfers.filter(t => t.state === "pending").length.

TITLE (<h2>, id="closeguard-title"):
  n === 1  ->  Closing warpseed stops the running transfer
  n  >  1  ->  Closing warpseed stops {n} running transfers

BODY PARAGRAPH 1 (<p>, id="closeguard-desc", always rendered):
  n === 1  ->  Its progress is saved. warpseed restarts it automatically the next time you open it and picks up from the last checkpoint, so at most about 8 MB is re-sent.
  n  >  1  ->  Their progress is saved. warpseed restarts them automatically the next time you open it and each picks up from its last checkpoint, so at most about 8 MB per connection is re-sent.

BODY PARAGRAPH 2 (<p>, always rendered — this is the placeholder truth):
  Unfinished transfers keep their data in a placeholder file next to the destination, ending .wspart or .wschunk — on the server for uploads. A .wschunk already shows the final file size but is not finished. Leave these files alone; warpseed needs them to resume. If you later cancel a transfer, delete its placeholder yourself — warpseed leaves it behind.

BODY PARAGRAPH 3 (<p>, rendered only when q > 0):
  q === 1  ->  The queued transfer is untouched and starts when you're back.
  q  >  1  ->  {q} queued transfers are untouched and start when you're back.

CHECKBOX (bottom-left of the action row, unchecked on every open):
  Don't ask again — always close and resume

BUTTONS (left to right in .dialog__actions):
  Keep warpseed open        (class "btn")
  Close and resume later    (class "btn")
  Minimize to pill          (class "btn btn--primary", autoFocus)

ARIA:
  aria-label on the scrim's dialog element is not used; instead
  role="alertdialog" aria-modal="true"
  aria-labelledby="closeguard-title" aria-describedby="closeguard-desc"

INTERPOLATION RULES (binding):
  1. Never render the numeral 1 for a count. n === 1 and q === 1 take the
     definite-article variants above; digits appear only for values >= 2.
  2. Compose each branch as a whole string. Do not build them from fragments
     with a pluralise() helper.
  3. "about 8 MB" is derived, not typed: Go exports the checkpoint bound (see
     WP-B1) and sends it in the event payload as checkpointMB; render
     `about ${checkpointMB} MB`. If the constant changes, the copy changes.
  4. Counts render inline in prose in the UI font — a deliberate exception to
     ux-spec §6.3 ("all data sets in --font-data"), because a mono digit
     mid-sentence reads as a code token. Record this in ux-spec.
  5. There is no n === 0 variant: Go never emits the event when nothing is
     running, so the dialog cannot render for an idle app.

=== SETTINGS ROW (frontend/src/components/SettingsDialog.tsx) ===

Section heading (<h3>):
  Closing

Segmented radiogroup, aria-label="When closing with transfers running",
values written to ui.close_action:
  ask   ->  Ask
  quit  ->  Close
  pill  ->  Minimize to pill

Helper note (<p class="set-note">):
  Unfinished transfers always resume the next time you open warpseed. Choosing "Close" skips the confirmation; choosing "Minimize to pill" shrinks the window instead of closing it.

=== TOAST (Go, emitted on the ui.close_action = "pill" path) ===
  warpseed is still running in the pill — press Escape to bring the window back

=== TOAST (frontend, on a failed CloseToPill call) ===
  Could not enter mini mode

=== MINI PILL, unchanged strings ===
  title="Back to warpseed"  aria-label="Restore window"
  (WP-F3 adds the same restore action to the pill body; keep both labels.)
## Work packages
### WP-B1 — Non-blocking close guard: OnBeforeClose, quit re-entrancy latch, ack timeout, ui.close_action
**owner:** go-backend  
**priority:** must-ship  

**Files:**
- `main.go`
- `app.go`
- `app_close_test.go`
- `internal/dispatch/dispatcher.go`
- `internal/queue/schema.go`
- `internal/engine/sftpfast/chunked.go`

**Instructions:**

THE ONE RULE: OnBeforeClose runs synchronously on the Windows UI message-pump thread. Verified chain: winc/wndproc.go:87 (WM_CLOSE) -> frontend.go:220-226 (mainWindow.OnClose bind) -> frontend.go:459-466 (Frontend.Quit calls OnBeforeClose, and skips winc.Exit when it returns true) -> winc/form.go:276 (WM_CLOSE returns 0, never DefWindowProc, so the veto really holds). Blocking inside the hook stops the pump; WebView2 renders through that same pump, so the dialog you are waiting for can never paint. NEVER wait on a channel, a sync.WaitGroup, internal/events/Broker.Ask, or any DB query that could block there. Emitting IS safe: runtime.EventsEmit -> ExecJS -> ControlBase.Invoke, and Invoke's tryInvokeOnCurrentGoRoutine (winc/controlbase.go:427-436, 538-547) runs the closure inline when already on the window thread. Put a comment saying exactly this above beforeClose.

1) internal/engine/sftpfast/chunked.go — export the checkpoint bound so the dialog copy cannot drift. Change `checkpointEvery = 8 << 20` to `CheckpointEvery = 8 << 20` and update its uses in chunked.go:280 and upload_chunked.go:479. Leave checkpointInterval unexported.

2) internal/dispatch/dispatcher.go — add, next to the other control methods:

// ActiveCount reports how many transfers are running right now. It reads
// only in-memory state (no DB) because the close guard calls it from the
// Windows UI thread, where a blocked query would freeze the message pump.
func (d *Dispatcher) ActiveCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cancels)
}

(d.cancels gains one entry per claimed transfer in pump and loses it in release, so its length is exactly the running count. Both holds of d.mu are short and never span a DB call.)

3) internal/queue/schema.go — APPEND migration 008 to the end of the `migrations` slice. Never edit 001-007 (see the comment at schema.go:3-4):

	// 008 — what the X button does while transfers are running.
	`
	INSERT OR IGNORE INTO settings(key,value) VALUES ('ui.close_action','ask');
	`,

4) app.go — add imports "sync/atomic" and (if absent) "time". Add to the App struct:

	quitting     atomic.Bool  // set once; makes beforeClose fall through
	closePending atomic.Bool  // a guard dialog is outstanding
	closeAcked   atomic.Bool  // the frontend confirmed the dialog is up
	closeAction  atomic.Value // string: "ask" | "quit" | "pill"

5) app.go startup() — after `a.store = store` and before `a.dispatcher = dispatch.New(...)`, seed the cached setting so the guard never touches SQLite on the UI thread:

	a.closeAction.Store(store.Setting("ui.close_action", "ask"))

6) app.go SetSetting (line ~1082, after `a.store.SetSetting` succeeds, before the sink Emit) — keep the cache fresh:

	if key == "ui.close_action" {
		a.closeAction.Store(value)
	}

7) app.go settingValidators (line 991 map) — add:

	"ui.close_action": oneOf("ask", "quit", "pill"),

Unlisted keys are hard-rejected at app.go:1075-1078, so this line is mandatory.

8) app.go — the decision function, kept pure so it is unit-testable without Wails:

type closeDecision int

const (
	closeAllow closeDecision = iota // let the window close now
	closeAsk                        // show the dialog, veto the close
	closePill                       // shrink to the pill, veto the close
)

// decideClose is the whole close-guard policy, kept free of Wails calls so
// it can be tested. quitting/pending are the latch state; running is the
// live transfer count; action is the cached ui.close_action value.
func decideClose(quitting, pending bool, running int, action string) closeDecision {
	if quitting {
		return closeAllow // second pass from runtime.Quit — MUST fall through
	}
	if pending {
		return closeAllow // a second close gesture is the user insisting
	}
	if running == 0 {
		return closeAllow // idle app closes instantly, exactly as before
	}
	switch action {
	case "quit":
		return closeAllow
	case "pill":
		return closePill
	default:
		return closeAsk
	}
}

9) app.go — the hook itself:

const closeAckTimeout = 2 * time.Second

// beforeClose runs ON THE WINDOWS UI THREAD, synchronously inside the
// WM_CLOSE wndproc (winc/wndproc.go:87 -> frontend.go:459). It must return
// immediately: blocking here stops the message pump that WebView2 needs to
// paint the very dialog we are asking for. Emit and return; the answer
// arrives later through ConfirmQuit/CancelQuit/CloseToPill.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	if a.dispatcher == nil { // queue DB failed to open (startup returns early)
		return false
	}
	action, _ := a.closeAction.Load().(string)
	if action == "" {
		action = "ask"
	}
	switch decideClose(a.quitting.Load(), a.closePending.Load(), a.dispatcher.ActiveCount(), action) {
	case closeAllow:
		a.quitting.Store(true) // a second gesture must never be vetoed again
		return false
	case closePill:
		a.restoreForDialog(ctx)
		a.SetMiniMode(true)
		a.sink.Emit("app:info", "warpseed is still running in the pill — press Escape to bring the window back")
		return true
	}

	// closeAsk
	a.restoreForDialog(ctx)
	a.closePending.Store(true)
	a.closeAcked.Store(false)
	a.sink.Emit("app:close-requested", map[string]any{
		"running":      a.dispatcher.ActiveCount(),
		"checkpointMB": sftpfast.CheckpointEvery >> 20,
	})
	// Escape hatch: if the frontend never acks (crashed webview, JS error
	// before the subscription mounts), quit anyway rather than leaving an
	// unclosable window. This runs on its own goroutine, so blocking is fine.
	go func() {
		time.Sleep(closeAckTimeout)
		if a.closePending.Load() && !a.closeAcked.Load() {
			log.Printf("close guard: frontend never acknowledged in %s — quitting", closeAckTimeout)
			a.quitting.Store(true)
			wruntime.Quit(a.ctx)
		}
	}()
	return true
}

// restoreForDialog puts the window somewhere a modal can actually be seen:
// out of the 380x96 pill, un-minimised, foregrounded. All three calls are
// direct Win32 on the calling thread (frontend.go:270-370), so they are safe
// from inside the wndproc.
func (a *App) restoreForDialog(ctx context.Context) {
	a.mu.Lock()
	mini := a.mini
	a.mu.Unlock()
	if mini {
		a.SetMiniMode(false) // guard the call: SetMiniMode(false) when never in
		                     // mini mode force-resizes to 1280x800 (app.go:262)
	}
	wruntime.WindowUnminimise(ctx)
	wruntime.WindowShow(ctx)
}

10) app.go — the four bindings the frontend calls. Each runs on its own
goroutine (frontend.go:762 `go f.dispatchMessage`), so blocking is allowed:

// AckCloseDialog tells Go the guard dialog is on screen, disarming the
// no-answer timeout. The frontend calls this the instant it receives
// app:close-requested.
func (a *App) AckCloseDialog() { a.closeAcked.Store(true) }

// ConfirmQuit closes the app for real. runtime.Quit re-enters
// Frontend.Quit -> OnBeforeClose (frontend.go:459-466), which is why the
// quitting latch exists: without it the second pass would re-raise the
// dialog forever and the app could never exit.
func (a *App) ConfirmQuit() {
	a.quitting.Store(true)
	wruntime.Quit(a.ctx)
}

// CancelQuit dismisses the guard and re-arms it for the next close gesture.
func (a *App) CancelQuit() {
	a.closePending.Store(false)
	a.closeAcked.Store(false)
}

// CloseToPill answers the guard by shrinking to the ambient pill instead of
// quitting. Nothing is interrupted.
func (a *App) CloseToPill() {
	a.closePending.Store(false)
	a.closeAcked.Store(false)
	a.SetMiniMode(true)
}

11) main.go — add ONE line to options.App, next to OnStartup/OnShutdown:

		OnBeforeClose:    app.beforeClose,

Do NOT set HideWindowOnClose. frontend.go:220-226 takes the WindowHide branch
and skips OnBeforeClose entirely when it is true, so the dialog would never
appear — and with no tray in v2.13 a hidden window is unreachable.

12) Add "warpseed/internal/engine/sftpfast" to app.go's imports if the
CheckpointEvery reference needs it (it is already imported at app.go:26).

**tests:** Write app_close_test.go covering decideClose exhaustively — it is pure, needs no Wails, and pins the whole policy:
- decideClose(true,  false, 5, "ask")  == closeAllow  (quit re-entrancy: the app MUST be able to exit)
- decideClose(false, true,  5, "ask")  == closeAllow  (second gesture escapes a stuck dialog)
- decideClose(false, false, 0, "ask")  == closeAllow  (idle app closes instantly)
- decideClose(false, false, 3, "ask")  == closeAsk
- decideClose(false, false, 3, "quit") == closeAllow
- decideClose(false, false, 3, "pill") == closePill
- decideClose(false, false, 3, "")     == closeAsk    (unknown/empty value falls back to asking)
Also add a Dispatcher.ActiveCount test in internal/dispatch that seeds d.cancels directly and asserts the count, plus a -race run.

Commands (export PATH="$HOME/.local/share/mise/shims:$PATH" first):
  go vet ./...
  go test ./... -race
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...

Manual, on the Windows dev box (the build of record — none of the UI-thread behaviour can be verified on Linux):
  a) 8 active transfers, click the title-bar X -> dialog paints within one frame, window stays open.
  b) Same with Alt+F4, and with taskbar right-click -> Close. All three go through WM_CLOSE and must behave identically.
  c) Empty queue, click X -> app closes instantly with no dialog and no perceptible delay.
  d) Press X, then press X again before answering -> app quits (the insisting escape).
  e) Choose "Close and resume later" -> app exits; relaunch -> rows are back as pending and running, and log shows "queue: requeued N interrupted transfer(s)".

**acceptance:** Closing with zero running transfers is byte-for-byte today's behaviour: no dialog, no added latency, no DB read on the close path. Closing with N>0 running transfers shows the dialog and the window survives. The app can always be closed: the quitting latch means runtime.Quit never loops, a second close gesture always exits, and a frontend that never acks within 2 s is force-quit by Go with a log line. beforeClose performs no channel wait, no WaitGroup wait, no Broker.Ask and no SQL query. ui.close_action is readable and writable through the existing GetSettings/SetSetting bindings and rejects any value outside ask|quit|pill. GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./... passes.

### WP-B2 — Graceful stop: cancel and drain the dispatcher before closing the store, without marking transfers failed
**owner:** go-backend  
**priority:** should-ship  

**Files:**
- `internal/dispatch/dispatcher.go`
- `app.go`

**Instructions:**

THE TRAP, verified: the intuitive implementation regresses behaviour badly. Cancelling the dispatcher context makes each runTransfer return "chunk N cancelled: context canceled". finishWithError (dispatcher.go:678-687) early-returns ONLY when the DB row already reads 'paused' or 'cancelled'; nobody wrote that, so it falls through to core.Classify, which has no match for that string and returns ClassPermanent, so the row is written 'failed' (dispatcher.go:701). RecoverInterrupted (store.go:112-120) rescues only 'dispatched' and 'active' — so every running transfer would need a manual Resume on next launch. That is strictly worse than today. The equally intuitive fix, calling Pause on the way out, has the same defect: Pause writes 'paused' (dispatcher.go:709-716), which RecoverInterrupted also ignores. The correct answer is a stopping flag that makes finishWithError a no-op, leaving the row in 'active' for RecoverInterrupted to requeue.

1) internal/dispatch/dispatcher.go — add to the Dispatcher struct:

	stopping atomic.Bool
	wg       sync.WaitGroup

(import "sync/atomic"; "sync" is already imported.)

2) In pump(), first statement of the function:

	if d.stopping.Load() {
		return // no new work once we are shutting down
	}

3) In pump(), at the launch site (dispatcher.go:256), wrap the goroutine:

	d.wg.Add(1)
	go d.runTransfer(tctx, t, streams)

and add `defer d.wg.Done()` as the FIRST deferred statement inside runTransfer (before `defer d.release(t)`), so Done runs last.

4) finishWithError — insert as the very first statement, before the ctx.Err() block:

	if d.stopping.Load() {
		// Shutting down. Leave the row in 'active': RecoverInterrupted
		// (queue/store.go:112) requeues exactly that state on next launch.
		// Writing 'failed' here (which Classify would do for "context
		// canceled") would silently turn auto-resume into manual resume.
		return
	}

5) runTransfer's cancellation watchdog (dispatcher.go:394-404) — give lanes a
grace period to commit before their connections are torn down. The chunked
UPLOAD's cancel path calls commit(true), which calls rf.Sync() over a live
SFTP connection (upload_chunked.go:436-445, 447-450); if the watchdog closes
the clients first, the Sync errors and checkpoint() is never reached, losing
that lane's final checkpoint. Replace the ctx.Done branch with:

	case <-ctx.Done():
		if d.stopping.Load() {
			// Graceful stop: let the lanes write their final checkpoint
			// (the chunked upload's commit needs a live connection) before
			// tearing the connections down.
			select {
			case <-watchdogDone:
				return
			case <-time.After(1500 * time.Millisecond):
			}
		}
		for _, c := range clients {
			c.Close()
		}

Leave the Pause/Cancel path (stopping == false) exactly as it is: immediate
teardown is what unblocks a wedged pkg/sftp copy.

6) Add the public stop entry point:

// Stop cancels every running transfer and waits for the lanes to write their
// final checkpoints, up to timeout. Rows are deliberately left in 'active':
// RecoverInterrupted requeues them on the next launch, which is how a close
// auto-resumes. Returns true if every transfer finished within the timeout.
func (d *Dispatcher) Stop(timeout time.Duration) bool {
	d.stopping.Store(true)
	d.mu.Lock()
	for _, cancel := range d.cancels {
		cancel()
	}
	d.mu.Unlock()
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

7) app.go startup() — the Wails context is context.Background() decorated with
WithValue only (wails internal/app/app_production.go:31), so ctx.Done() can
never fire and `go a.dispatcher.Run(ctx)` at app.go:87 is currently
uncancellable. Give it its own cancel:

	runCtx, cancel := context.WithCancel(ctx)
	a.dispatchCancel = cancel
	go a.dispatcher.Run(runCtx)

with a new App field `dispatchCancel context.CancelFunc`.

8) app.go shutdown() — replace the body with the correct order. Today it
closes the store (app.go:179) while workers may still be writing; a
'completed' write landing in that window is lost, the row stays 'active',
RecoverInterrupted requeues it, and an already-finished 50 GB file is
re-transferred in full:

func (a *App) shutdown(_ context.Context) {
	if a.dispatcher != nil {
		if !a.dispatcher.Stop(3 * time.Second) {
			log.Printf("shutdown: transfers did not stop within 3s; exiting anyway")
		}
	}
	if a.dispatchCancel != nil {
		a.dispatchCancel()
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
}

The 3 s cap is hard: shutdown runs after RunMainLoop returns (wails
app_production.go:17-24), so the window is already gone and any longer wait
is an invisible hang.

**tests:** Go: add a dispatcher test that seeds d.cancels with stub CancelFuncs and d.wg with a goroutine that returns on ctx.Done, then asserts Stop(1s) == true and that a second Stop is safe. Add a test asserting finishWithError makes no store write when stopping is set (use a store backed by a temp SQLite file, put a row in 'active', call finishWithError with a context.Canceled error, assert the row is still 'active' — this is the regression that matters most).
  go test ./internal/dispatch/... -race
  go vet ./... && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...

Manual on Windows: start one chunked download and one chunked upload (>256 MB and >128 MB respectively so both take the chunked path per schema.go:205-207 and 243-245), confirm the close, note the byte offsets in the queue dock, relaunch, and confirm each resumes within a few MB of where it stopped and that NO row reads 'failed'.

**acceptance:** After a confirmed close, every previously running transfer comes back as 'pending' on next launch and resumes — none is left 'failed' or 'paused'. The store is closed only after the dispatcher has drained or the 3 s cap expires. Shutdown never takes longer than ~3 s. Pause and Cancel behave exactly as before (immediate connection teardown, correct terminal states) because the grace period is gated on the stopping flag.

### WP-B3 — SingleInstanceLock so a relaunch always surfaces the running window
**owner:** go-backend  
**priority:** should-ship  

**Files:**
- `main.go`
- `app.go`

**Instructions:**

Verified present in the pinned version: options.App has a SingleInstanceLock field and pkg/options/options.go:189-198 defines SingleInstanceLock{UniqueId, OnSecondInstanceLaunch} and SecondInstanceData. On Windows it uses a named mutex plus WM_COPYDATA — no CGO, no new dependency.

Two reasons to add it. First, the queue DB is opened with SetMaxOpenConns(1) and is the single source of truth (internal/queue/store.go:39); two warpseed processes running the same dispatcher against the same DB is a genuine hazard that exists today. Second, it is the guaranteed way back to a window the user cannot see — relaunching the shortcut always foregrounds the running instance.

main.go, inside options.App:

		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "warpseed-single-instance-v1",
			OnSecondInstanceLaunch: app.onSecondInstance,
		},

app.go:

// onSecondInstance runs when the user launches warpseed again while an
// instance is already running. It foregrounds the existing window instead of
// starting a second process against the same single-writer queue database.
// Runs on the second-instance processor goroutine, not the UI thread.
func (a *App) onSecondInstance(_ options.SecondInstanceData) {
	if a.ctx == nil {
		return // a relaunch that beat startup: nothing to show yet
	}
	a.mu.Lock()
	mini := a.mini
	a.mu.Unlock()
	if mini {
		a.SetMiniMode(false)
		a.sink.Emit("app:mini-exited", nil)
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.WindowShow(a.ctx)
}

Use WindowShow, not Show: frontend.go:1007 handles the minimised-vs-hidden
distinction and calls SetForegroundWindow + SetFocus. Import
"github.com/wailsapp/wails/v2/pkg/options" in app.go for the parameter type.

Note for the frontend owner: this emits app:mini-exited, which WP-F1 must
subscribe to so the CSS pill class is cleared when Go leaves mini mode on its
own. If WP-F1 ships without that subscription, drop the Emit line rather than
leaving a full UI painted into a pill.

**tests:** CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... and go vet ./...

Manual on Windows (cannot be tested on Linux): launch warpseed, minimise it, then double-click the shortcut again — the second process must exit immediately and the first window must come to the front. Repeat from the pill: the second launch must restore the full window, not leave a 380x96 strip with the full UI painted into it.

**acceptance:** Only one warpseed process can hold the queue database. Launching the app again always brings the existing window to the foreground, out of mini mode and un-minimised. No new module dependency appears in go.mod.

### WP-B4 — Document the close guard and record the ux-spec deviations
**owner:** go-backend  
**priority:** should-ship  

**Files:**
- `docs/design/ux-spec.md`
- `docs/user-guide.md`

**Instructions:**

docs/design/ux-spec.md — add a subsection under §7 (Component inventory & states), numbered 7.13 "Close guard", recording the design AND the three deliberate deviations:

1. Deviation from §1.1 ("no modal that isn't strictly necessary"): the close guard is a modal, justified because it appears only when transfers are actually running and because the alternative — a silent kill of live work — is the thing §1.5 ("Honest state, always") exists to prevent. It never appears for an idle app.
2. Deviation from §6.3 ("all data sets in --font-data"): transfer counts inside the dialog's prose render in the UI font, because a monospace digit mid-sentence reads as a code token.
3. NOT a deviation from §1.6 ("Calm surface, loud alarms"): the close guard is deliberately CALM — no error ring, no warning icon. Loud treatment stays reserved for HOST KEY CHANGED (§5.4) and destructive overwrite (§7.9). Nothing here is destroyed; the transfers resume.

Also record the button-label rule (verb-labelled specific responses; no OK/Cancel, no Yes/No, no button literally called "Close" on its own) and the default-focus rule (focus starts on the safest response, Minimize to pill; Escape maps to Keep warpseed open; neither Enter nor Escape can ever stop a transfer).

State plainly, so nobody re-litigates it: Wails v2.13.0 has no system-tray API — options.App has no tray field, pkg/menu/tray.go and internal/menumanager/traymenu.go have zero callers, and internal/platform/win32's ShellNotifyIcon is unreferenced. "Close to taskbar" is therefore answered by the existing mini pill (app.go:232-266), which keeps a title bar, a taskbar button and an Alt+Tab entry. HideWindowOnClose and runtime.WindowHide are BANNED in this codebase: with no tray, a hidden window is a running process holding SSH connections that only Task Manager can find.

docs/user-guide.md — under "Mini mode", add a short "Closing warpseed" section explaining: the confirmation only appears while transfers are running; unfinished transfers resume automatically on next launch from the last checkpoint; the .wspart / .wschunk placeholder files (a .wschunk shows the final size but is not finished, on the local disk for downloads and on the server for uploads) must be left alone, and are left behind if the transfer is later cancelled; and the Settings -> Closing preference. While editing that file, fix the existing false promise at docs/user-guide.md:245-248 ("Click the pill ... to bring the full window back") — either after WP-F3 lands, or by narrowing the sentence to the restore button until it does.

**tests:** No automated tests. Proof-read against the shipped copy string-for-string: the guide must not promise anything the dialog does not, and must not claim the placeholder files use only the space transferred so far (see the risks — that is unverified on NTFS).

**acceptance:** ux-spec.md carries a §7.13 that names all three deviations explicitly and states the no-tray / no-hidden-window rule. user-guide.md describes the close behaviour, the placeholder files including the abandoned-on-cancel case, and the new setting. No documentation sentence contradicts the dialog copy.

### WP-B5 — Stop orphaning placeholder files when a transfer is cancelled
**owner:** go-backend  
**priority:** nice-to-have  

**Files:**
- `internal/dispatch/dispatcher.go`
- `internal/queue/transfers.go`

**Instructions:**

This is the one finding that costs the user real money, and it is why the dialog copy has to mention placeholders at all. Dispatcher.Cancel (dispatcher.go:729-736) sets the state and cancels the context — it deletes no part file, local or remote. ClearFinished (transfers.go:155-160) deletes the row, cascading the chunk rows away, with the placeholder still sitting there. Nothing else in the codebase ever removes one (verified: the only removals are the next-run repair paths at dispatcher.go:480, 532, 546, 625 and upload_chunked.go:141). So the sequence "abrupt close -> user cancels the stale row on relaunch" permanently orphans a full-apparent-size .wschunk on the seedbox, with no record anywhere that it exists.

Minimum viable fix, in Dispatcher.Cancel after cancelIfRunning:
- For direction == "download": remove t.Dst+sftpfast.PartSuffix and t.Dst+sftpfast.ChunkPartSuffix from the local disk (ignore os.IsNotExist).
- For direction == "upload": the remote placeholder needs a connection, which Cancel does not have. Either borrow the site's browse session through a new App-supplied callback, or record the orphan in a new `orphans` table and sweep it the next time that site connects. Do NOT block Cancel on a dial.
- Then DeleteChunks(id) so no stale checkpoint survives the file.

If the remote half is deferred, the dialog copy's sentence "If you later cancel a transfer, delete its placeholder yourself — warpseed leaves it behind" MUST stay in place. Only remove that sentence when both halves are actually cleaned up.

**tests:** go test ./internal/dispatch/... -race with a temp-directory test: create dst+".wspart" and dst+".wschunk", call Cancel, assert both are gone and the chunk rows are deleted. Assert Cancel still succeeds (no error, correct 'cancelled' state) when neither file exists.

**acceptance:** Cancelling a download removes its local placeholder and its chunk checkpoints. Cancelling an upload either removes the remote placeholder or records it for a later sweep. Cancel never blocks on a network dial and never fails because a placeholder was already gone.

### WP-F1 — Close-guard dialog: bindings, IPC facade, mock, store flag, component, CSS, Tab guard
**owner:** frontend  
**priority:** must-ship  

**Files:**
- `frontend/wailsjs/go/main/App.js`
- `frontend/wailsjs/go/main/App.d.ts`
- `frontend/src/ipc.ts`
- `frontend/src/mock/index.ts`
- `frontend/src/store.ts`
- `frontend/src/components/CloseGuardDialog.tsx`
- `frontend/src/App.tsx`
- `frontend/src/overlays.css`

**Instructions:**

The Wails CLI is NOT installed on the Linux build box (only go and node are on the mise shims path), so the generated bindings must be hand-edited to match what the generator would produce. Follow the existing file's exact shape.

1) frontend/wailsjs/go/main/App.js — add four zero-argument functions in the file's alphabetical position (AckCloseDialog before AddBookmark; CancelQuit before CancelTransfer; CloseToPill after ClearDoneTransfers; ConfirmQuit before ConnectSite). Each is exactly:

export function AckCloseDialog() {
  return window['go']['main']['App']['AckCloseDialog']();
}

2) frontend/wailsjs/go/main/App.d.ts — matching declarations in the same positions:

export function AckCloseDialog():Promise<void>;
export function CancelQuit():Promise<void>;
export function CloseToPill():Promise<void>;
export function ConfirmQuit():Promise<void>;

3) frontend/src/ipc.ts — this is the ONLY module allowed to touch the generated bindings (stated at ipc.ts:1-2). Add the four names to the import block from "../wailsjs/go/main/App", then export thin wrappers next to the other transfer controls:

/** Payload of app:close-requested — emitted when the user tries to close
    warpseed while transfers are running. */
export interface CloseRequest {
  running: number;
  checkpointMB: number;
}

export const ackCloseDialog = (): Promise<void> => AckCloseDialog();
export const confirmQuit = (): Promise<void> => ConfirmQuit();
export const cancelQuit = (): Promise<void> => CancelQuit();
export const closeToPill = (): Promise<void> => CloseToPill();

4) frontend/src/mock/index.ts — add stubs to the App object beside SetMiniMode (mock/index.ts:404). Forgetting these breaks the ?mock=1 browser demo used for docs screenshots with NO compile error:

  async AckCloseDialog() {},
  async ConfirmQuit() {},
  async CancelQuit() {},
  async CloseToPill() {},

5) frontend/src/store.ts — add `closeGuardOpen: boolean` to UiState (beside settingsOpen at line 41), its setter `setCloseGuardOpen: (open: boolean) => void` (beside line 58), the initial value `closeGuardOpen: false` (beside line 78) and the implementation `setCloseGuardOpen: (closeGuardOpen) => set({ closeGuardOpen }),` (beside line 124). Do not persist it.

6) frontend/src/components/CloseGuardDialog.tsx — new file. Copy the structural idiom from HostKeyDialog.tsx (role="alertdialog", NO scrim mousedown dismiss — a close confirmation must take an explicit answer), but use the CALM card, not the error-ringed .dialog--hostkey and no Warning icon: nothing here is destroyed.

import { useEffect, useRef, useState } from "react";
import {
  ackCloseDialog, cancelQuit, closeToPill, confirmQuit, on, setSetting,
  type CloseRequest,
} from "../ipc";
import { formatSize } from "../lib/format";
import { useUiStore } from "../store";

Behaviour, in order:
  a) One subscription, mounted once:
     useEffect(() => on<CloseRequest>("app:close-requested", (p) => {
       setReq(p);
       setDontAsk(false);                                  // never sticky
       useUiStore.getState().setMiniMode(false);           // Go already resized the
                                                           // window; this only drops
                                                           // the .app--mini class
       useUiStore.getState().setCloseGuardOpen(true);
       void ackCloseDialog();                              // MUST be immediate: Go
                                                           // force-quits after 2s
                                                           // without this ack
     }), []);
     Call ackCloseDialog() inside the event handler, never in render or a
     later effect — the 2 s timeout is the escape from a wedged frontend and
     a late ack defeats it.
  b) Also subscribe to "app:mini-exited" (emitted by WP-B3) and clear miniMode
     on it, so a second-instance restore does not leave the full UI painted
     into a pill. If WP-B3 is not shipping, skip this and tell its owner.
  c) `if (!req) return null;`
  d) Live numbers from the store, using exactly the derivation MiniView.tsx:15-20
     and QueueDock.tsx:224-230 use — there are no memoised selectors:
       const transfers = useUiStore((s) => s.transfers);
       const progress  = useUiStore((s) => s.progress);
       const active    = transfers.filter((t) => t.state === "active");
       const queued    = transfers.filter((t) => t.state === "pending").length;
       const aggRate   = active.reduce((s, t) => s + (progress[t.id]?.rate ?? 0), 0);
     progress entries are NEVER deleted from the store, so a rate must always
     be gated on state === "active". Render the rate only when aggRate > 0, as
     `${formatSize(aggRate)}/s`, appended to the title line — never as a
     second paragraph.
     Use req.running for the headline count (authoritative at the moment of
     the close gesture) and req.checkpointMB for the MB figure. Use the store's
     `queued` for paragraph 3.
  e) Three handlers, each closing the dialog locally first:
       keep():   setCloseGuardOpen(false); setReq(null); void cancelQuit();
       close():  setCloseGuardOpen(false); if (dontAsk) void setSetting("ui.close_action", "quit"); void confirmQuit();
       pill():   setCloseGuardOpen(false); setReq(null);
                 void closeToPill().then(() => useUiStore.getState().setMiniMode(true))
                   .catch(() => window.dispatchEvent(new CustomEvent("ws:toast",
                     { detail: { kind: "error", text: "Could not enter mini mode" } })));
                 (backend first, store on success — the same ordering as the
                  header button at App.tsx:238-250)
     The don't-ask-again checkbox is honoured ONLY on close(): ticking it and
     then choosing Keep or Minimize must change nothing, because the user
     never consented to the outcome being suppressed.
  f) Markup:
     <div className="scrim scrim--center">                     {/* no onMouseDown */}
       <div className="dialog dialog--closeguard" role="alertdialog" aria-modal="true"
            aria-labelledby="closeguard-title" aria-describedby="closeguard-desc"
            ref={cardRef}
            onKeyDown={(e) => { if (e.key === "Escape") { e.stopPropagation(); keep(); } }}>
     The Escape stopPropagation is mandatory: App.tsx:118-160 owns a global
     Escape handler that would otherwise also act.
     <h2 id="closeguard-title">, then <p id="closeguard-desc">, then the
     placeholder paragraph, then the conditional queued paragraph, then
     <div className="dialog__actions"> with the checkbox first (it renders
     left of the buttons because .dialog__actions is justify-content:flex-end;
     give the label className="closeguard__dontask" and
     `margin-right: auto`), then the three buttons in the order
     Keep warpseed open (.btn) / Close and resume later (.btn) /
     Minimize to pill (.btn btn--primary, autoFocus).
     autoFocus goes on Minimize to pill — the only response that both keeps
     every transfer alive and honours the user's intent to clear the window,
     and fully reversible with one Escape. This follows the rule already
     written in code at PromptDialog.tsx:72-79. Never autoFocus the closing
     button; no keystroke may stop a transfer.
  g) Focus trap: the codebase has none anywhere, and the global Tab handler
     makes its absence worse here. Add a local Tab handler on the card that
     queries `button, input` inside cardRef and wraps at both ends, and
     restore focus to document.body on close.

7) frontend/src/App.tsx:
   - import CloseGuardDialog and render <CloseGuardDialog /> immediately after
     <HostKeyDialog /> (App.tsx:311).
   - Extend the Tab guard at App.tsx:145. It currently exempts only
     settingsOpen, so Tab pressed on any other dialog's button is swallowed
     and switches panes behind the modal — a three-button dialog with no text
     field is squarely in that hole:
       } else if (e.key === "Tab" && !inField &&
                  !useUiStore.getState().settingsOpen &&
                  !useUiStore.getState().closeGuardOpen) {
   - Leave the miniMode early-return at App.tsx:121-137 alone: the guard
     always leaves mini mode before the dialog renders, so its Escape cannot
     be swallowed.

8) frontend/src/overlays.css — add beside .dialog--hostkey (line ~196). Calm
   card, no error ring:

/* close guard: calm by design — ux-spec §1.6 reserves loud treatment for
   HOST KEY CHANGED and destructive overwrite. Nothing here is destroyed;
   the transfers resume. */
.dialog--closeguard {
  width: 460px;
}
.closeguard__dontask {
  margin-right: auto;
  display: flex;
  align-items: center;
  gap: var(--sp-2);
  font-size: var(--text-xs);
  color: var(--text-mid);
}

Do NOT add a `.app--mini > .scrim` exception. The dialog must never render
into the 380x96 pill; the guard restores the full window first (Go side) and
the component clears the .app--mini class on receipt.

Strings: use the "copy" section of this plan verbatim, including the
interpolation rules (never render the numeral 1 for a count; compose whole
strings per branch).

**tests:** No test runner exists in frontend/package.json, so verification is build + manual:
  export PATH="$HOME/.local/share/mise/shims:$PATH"
  cd frontend && npm run build     # runs tsc then vite build; must be clean

Mock-mode manual pass (npm run dev, open with ?mock=1) — the mock has no Go
side, so drive the dialog by hand from the devtools console:
  window.runtime.EventsEmit("app:close-requested", { running: 3, checkpointMB: 8 })
Check: the dialog paints; the title reads "Closing warpseed stops 3 running
transfers"; emit with running: 1 and confirm the singular branch has no
numeral; Tab cycles only the dialog's controls and never switches panes;
Escape closes the dialog and does not also leave mini mode or open anything
else; the scrim click does nothing; focus starts on Minimize to pill.
Repeat after entering mini mode from the header button and confirm the pill
class drops and the dialog is fully visible.

On the Windows dev box with the real backend: X, Alt+F4 and taskbar Close all
raise the same dialog; "Close and resume later" exits; "Keep warpseed open"
leaves the app running and a later X asks again; "Minimize to pill" shrinks
the window with transfers still moving; pressing X on the pill restores the
full window and asks again (never a 380x96 modal).

**acceptance:** The dialog appears only when Go emits app:close-requested, never for an idle app. ackCloseDialog() is called within milliseconds of the event, so Go's 2 s force-quit never fires on a healthy frontend. All three buttons resolve the guard, and "Keep warpseed open" re-arms it for the next close gesture. The dialog is never rendered inside the mini pill. Tab is trapped inside the dialog and no longer switches panes behind it. Neither Enter nor Escape can stop a transfer. The ?mock=1 demo still boots with no console errors. npm run build is clean.

### WP-F2 — Settings → Closing: the ui.close_action preference with a visible off-switch
**owner:** frontend  
**priority:** should-ship  

**Files:**
- `frontend/src/components/SettingsDialog.tsx`

**Instructions:**

The don't-ask-again checkbox in WP-F1 is only acceptable because the choice is reversible somewhere the user can find it. If this package is deferred, remove the checkbox from WP-F1 — a suppression with no visible off-switch is the thing everyone hates about this pattern.

Add a new <section className="set-section"> after the Transfers section and before Bandwidth, using the existing .segmented radiogroup idiom verbatim from SettingsDialog.tsx:271-289 (there is no checkbox or toggle control anywhere in this dialog — do not invent one):

<section className="set-section">
  <h3>Closing</h3>
  <div className="segmented" role="radiogroup" aria-label="When closing with transfers running">
    {[
      ["ask", "Ask"],
      ["quit", "Close"],
      ["pill", "Minimize to pill"],
    ].map(([v, label]) => (
      <button key={v} className={closeAction === v ? "seg--on" : ""} role="radio"
              aria-checked={closeAction === v} onClick={() => put("ui.close_action", v)}>
        {label}
      </button>
    ))}
  </div>
  <p className="set-note">{/* helper copy from the copy section */}</p>
</section>

with `const closeAction = cfg["ui.close_action"] || "ask";` beside the existing
`const bwMode = cfg["bw.mode"] || "off";` at line ~86.

Use the existing put() helper (SettingsDialog.tsx:76-82): it writes
optimistically and re-reads on backend rejection. No new binding is needed —
GetSettings/SetSetting already cover it, and ipc.ts:282-284 already exposes
both. Do NOT add ui.close_action to lib/prefs.ts: it is read by Go, not
needed synchronously at boot, exactly like ui.theme and ui.local_default.

**tests:** npm run build clean. In mock mode confirm the section renders and the three options are mutually exclusive. On Windows with the real backend: set Close, confirm the X closes with no dialog while transfers run; set Minimize to pill, confirm the X shrinks to the pill and a success toast appears; set Ask, confirm the dialog returns; tick the dialog's don't-ask-again on the closing path, relaunch, and confirm the setting now reads Close.

**acceptance:** ui.close_action is settable from the UI and round-trips through the database. Ticking don't-ask-again in the dialog and then reopening Settings shows the segmented control on Close. An invalid value is impossible: the backend validator rejects anything outside ask|quit|pill.

### WP-F3 — Make the pill body clickable and reachable from the command palette
**owner:** frontend  
**priority:** should-ship  

**Files:**
- `frontend/src/components/MiniView.tsx`
- `frontend/src/components/CommandPalette.tsx`

**Instructions:**

docs/user-guide.md:245-248 already promises "Click the pill or press Escape to bring the full window back", but MiniView.tsx:44 renders no click handler — only the 26px .mini__restore button at line 68 does anything. Once the pill becomes an answer to "I'm closing this", it is the primary surface and users will click its body first.

1) MiniView.tsx — move the restore affordance onto the container while keeping the explicit button:
   <div className="mini" role="button" tabIndex={0} title="Back to warpseed"
        onClick={restore}
        onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); restore(); } }}>
   No stopPropagation is needed on .mini__restore — both do the same thing.
   Preserve restore()'s existing store-first ordering (MiniView.tsx:30-41): set
   miniMode false locally, then fire-and-forget the IPC, so a failed backend
   call leaves a usable window rather than a stuck pill.

2) CommandPalette.tsx currently has no mini-mode command at all, so the pill is
   reachable only from the header icon at App.tsx:235. Add a "Minimize to pill"
   command that runs the same body as that header button (ipcSetMiniMode(true)
   then setMiniMode(true), with the existing error toast on failure), so the
   feature has a keyboard path per ux-spec §1.2.

**tests:** npm run build clean. Mock mode: enter mini mode from the header, click the pill body — the full UI returns; Tab to the pill and press Enter — same; the .mini__restore button still works. Ctrl+K, type "mini" — the command appears and works.

**acceptance:** Clicking anywhere on the pill restores the window, matching what the user guide already claims. The pill is keyboard-operable. Minimize to pill has a command-palette entry.

## Deliberately deferred
- System tray icon. Wails v2.13.0 has no tray API whatsoever — options.App carries no tray field, pkg/menu/tray.go and internal/menumanager/traymenu.go have zero callers anywhere in the module, and internal/platform/win32's ShellNotifyIcon wrapper is unreferenced. A tray means a third-party dependency (energye/systray v1.0.3 was verified to cross-compile CGO-free for windows/amd64 and pulls no extra packages; getlantern/systray pulls eight) driven from a dedicated runtime.LockOSThread goroutine via systray.Run — never RunWithExternalLoop, whose pump goroutine is unlocked and silently dies. Deferred deliberately: a tray icon is the one component that can fail to appear silently (Explorer restart, hidden overflow, a swallowed registerSystray error), and Microsoft's own guidance deprecates minimise-to-notification-area for programs that already have desktop presence. The pill has a title bar, a taskbar button and an Alt+Tab entry; a tray icon would be strictly less recoverable. Revisit only after the dialog and pill have shipped and only as an addition, never as a replacement.
- Taskbar button progress (ITaskbarList3::SetProgressValue) on the main window, which is what Microsoft actually recommends for long-running work. golang.org/x/sys is already a direct dependency so COM vtable syscalls are reachable with CGO off, but this is an unproven spike and belongs behind a windows build tag, not in the close-guard change.
- A general 'find orphaned placeholders' sweeper that scans queue destinations' parent directories for *.wspart / *.wschunk with no matching non-terminal row. WP-B5 covers the per-row cancel case; this catches the exit-then-clear sequence that no per-row fix can reach.
- Clearing `attempt` and `error` in RecoverInterrupted (store.go:113-115). A row killed mid-transfer at attempt 2 currently gets zero retries on its next genuine failure (maxAttempts is 3 at dispatcher.go:35) and carries a stale error message into a fresh run. An app close is not a transfer failure and should not consume the retry budget. One-line change, but it touches crash-recovery semantics and deserves its own review pass.
- The duplicate-destination hazard: EnqueueTransfer (transfers.go:55-74) has no duplicate-Dst check and pump dedupes only by transfer id, so two rows can share a destination, compute the same placeholder path and run concurrently. Not caused by closing, but the chunked upload's own read-back guard exists partly to catch it, and the chunked download has no equivalent guard.
- Strengthening the chunked download's resume guard. chunked.go:98-103 checks size only, which is near-vacuous under preallocation by the code's own admission at chunked.go:120-122, while the upload side does full head/tail byte comparison (upload_chunked.go:326-403). The download side is strictly weaker.
- A shared useFocusTrap hook retrofitted to PromptDialog, HostKeyDialog, QuickConnect and SettingsDialog, none of which trap Tab today. WP-F1 adds a local trap to the new dialog only.

## Risks
- Deadlock is the single fatal failure mode. OnBeforeClose is invoked synchronously on the locked main UI thread from inside DispatchMessage (winc/app.go:22-24 init LockOSThread, winc/wndproc.go:87, frontend.go:459). WebView2 renders and delivers its callbacks through that same pump. Any blocking wait inside the hook — a channel receive, a WaitGroup, internal/events/Broker.Ask (which would hang for its full 2-minute timeout per app.go:62), or a SQLite query that hits the 5000 ms busy_timeout — freezes warpseed with a painted-but-dead window, which is strictly worse than today's instant exit. That is why WP-B1 reads the running count from the dispatcher's in-memory map and the setting from an atomic cache rather than from the database. Put the reason in a code comment; a future 'simplification' into a synchronous wait is the obvious mistake.
- runtime.Quit re-enters OnBeforeClose (pkg/runtime/runtime.go:62-69 -> frontend.go:459-466). Without the quitting latch the app becomes literally unquittable: the confirm path re-raises the dialog forever. The latch must be an atomic.Bool — it is written from a dispatch goroutine and read from the UI thread.
- Never set options.HideWindowOnClose. frontend.go:220-226 takes the WindowHide branch and skips OnBeforeClose entirely, so the dialog would never appear — and with no tray in v2.13 the hidden window has no taskbar button, no Alt+Tab entry and no click target, leaving a process holding live SSH connections that only Task Manager can end. runtime.WindowHide is banned in this codebase for the same reason.
- A graceful stop implemented naively regresses behaviour badly. Cancelling the dispatcher context makes runTransfer return 'context canceled', which core.Classify (core.go:39-62) maps to ClassPermanent, so finishWithError writes state='failed' (dispatcher.go:701) — and RecoverInterrupted (store.go:112-120) rescues only 'dispatched' and 'active'. The user would relaunch to a queue of failed rows each needing a manual Resume. The same trap swallows the intuitive 'pause everything on exit': Pause writes 'paused' (dispatcher.go:709-716), which RecoverInterrupted also ignores. WP-B2's stopping flag exists solely to prevent this; if anyone removes it, the dialog copy becomes a lie.
- Windows logoff and shutdown bypass everything. winc/form.go:251-288 has no WM_QUERYENDSESSION or WM_ENDSESSION case, so DefWindowProc lets the session end and the process dies with neither OnBeforeClose nor OnShutdown. Task Manager 'End task' is the same. RecoverInterrupted plus the 8 MiB / 30 s checkpoints remain the only safety net there — which is an argument against ever raising checkpointEvery.
- The 'about 8 MB' figure is true only for the chunked path (chunked.go:36-37) and only while the constant says so. A single-connection linear download resumes from the .wspart's on-disk size, a tighter bound; a linear upload loses at most one 32 KiB packet because pkg/sftp's ReadFrom falls through to the sequential loop for warpseed's progressReader. The copy says 'at most about 8 MB', which is an upper bound in every case, so it stays true — but deriving the number from the exported constant is what stops it drifting.
- Do NOT claim anywhere that placeholder files use only the space transferred so far. The comment at chunked.go:106-107 asserts Truncate creates a sparse file on NTFS, but the repo contains no FSCTL_SET_SPARSE / DeviceIoControl call and Go's os.File.Truncate on Windows is only SetFileInformationByHandle(FileEndOfFileInfo), so an interrupted 50 GB .wschunk most likely occupies 50 GB of local disk. The seedbox side (ftruncate on ext4) genuinely is sparse. Verify with `fsutil sparse queryflag` on the Windows box before any disk-space claim reaches a user; the shipped copy deliberately says only that the placeholder 'already shows the final file size'.
- 'Resumes automatically' assumes a saved password. dialTransfers passes an empty password when site.CredRef is empty (app.go:103-108); the auth failure classifies as ClassAuth, is not retryable, and lands the row in 'failed' — which RecoverInterrupted will never rescue on any later launch. The copy says 'restarts it automatically ... and picks up from the last checkpoint', not 'will always complete'. Resist any edit that strengthens it, and consider softening further if passwordless or key-auth sites are ever supported.
- Three commit buttons plus a checkbox is a busy modal for an app whose first design principle is 'no modal that isn't strictly necessary' (ux-spec §1.1). It earns its place only because it appears solely while transfers are actually running. If the gate is ever loosened to 'anything in the queue', it becomes the reflex-dismissed confirmation that trains users to stop reading.
- Everything about the Windows close path is read, not run: this is an ARM64 Linux box building a windows/amd64 target. The cross-compile check proves the code compiles, nothing more. Alt+F4 reaching WM_CLOSE through the WebView2 focus chain, the dialog painting within one frame after the veto, SingleInstanceLock's WM_COPYDATA handoff, and the pill's own X re-entering the guard all need confirming on the Windows dev box, which is the build of record.
- The generated bindings are hand-edited because the Wails CLI is not installed here. A mismatch between App.js, App.d.ts and the actual Go method set fails only at runtime, and a forgotten mock stub breaks the ?mock=1 demo used for documentation screenshots with no compile error at all. Regenerate with the CLI on the Windows box at the first opportunity and diff against the hand-edits.

## Stale anchors (added on recovery, 2026-09-08)

The design's line numbers were taken against 1.1.0. Verify each before relying on it:

- Queue schema migrations have grown from 008 to 011 (`internal/queue/schema.go`).
- `app.go` has gained the overwrite policy, the bulk failed/cancelled clear paths and
  `ClearResult`, so every `app.go:NNN` anchor has moved.
- `frontend/src/App.tsx` now renders a shared `ConfirmDialog` driven by
  `useUiStore.askConfirm`; the close guard must decide explicitly whether it uses that
  single confirm slot or stays a separate component (they can otherwise collide —
  `askConfirm` replaces any confirm already on screen).
