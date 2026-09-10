# warpseed roadmap — from 1.1.0

Consolidated from the two design workflows of 2026-09-02 (navigation/sorting,
close-guard), the 1.0 roadmap, and everything found during the 1.1.0 work.
Ordered by what it costs the user, not by what is interesting to build.

Legend: **[S]** small (under an hour) · **[M]** medium (a few hours) ·
**[L]** large (a day or more)

---

## Phase 1 — Data safety

These can lose or waste a user's data. Nothing else ships first.

**Status: complete.** 1.2 shipped in 1.1.3, 1.3–1.6 in 1.1.4, and 1.1 in
1.1.6. Every known way the queue could lose, waste or misplace a user's bytes
is closed. New data-safety work goes at the top of this phase and ships before
anything in Phase 2.

| # | Item | Size | Why it matters |
|---|---|---|---|
| 1.1 | ~~**Overwrite/conflict policy**~~ | M | **Done in 1.1.6.** The destination is checked at enqueue, before any bytes move. Rules per clash kind (newer+larger, smaller, older, identical, other) each resolve to overwrite / skip / keep both / ask, configurable in Settings. "Ask" holds the row in the queue with the sizes and dates shown, resolvable per row or all at once. Applies to uploads and downloads. |
| 1.2 | ~~**Stop discarding a chunk plan on a one-connection run**~~ | S | **Done in 1.1.3.** A plan with real progress now requeues as a capacity error instead of falling to the linear path, and the connection ceiling never narrows a chunk-eligible transfer below 2 lanes (which would have looked like a deliberate setting and slipped past the guard). Still discarded, correctly, when the plan is invalid: size/mtime mismatch, or the user setting lanes to 1. |
| 1.3 | ~~**Per-destination in-flight lock**~~ | S | **Done in 1.1.4.** `activeDst` keyed by site+path for uploads and by local path for downloads stops two rows writing one placeholder; `EnqueueTransfer` returns the existing row for an exact re-queue instead of adding a second. A *different* source aimed at the same destination is still accepted — that is a conflict for 1.1 to resolve, not a duplicate to swallow. |
| 1.4 | ~~**Clear `attempt`/`error` in `RecoverInterrupted`**~~ | S | **Done in 1.1.4.** Recovery resets `attempt`, `error` and `next_retry_at`; byte progress is untouched, so it still resumes rather than restarts. |
| 1.5 | ~~**Clean up placeholders on cancel**~~ | S | **Done in 1.1.4.** Cancel discards both placeholder kinds, the chunk plan and the byte count — for a running transfer once the engine has stopped but its connections are still open, for a queued or paused one immediately. `ClearDoneTransfers` sweeps cancelled rows the same way, so the row is never deleted while its file survives. Placeholders are left alone when another live row targets the same destination. |
| 1.6 | ~~**Strengthen the chunked download resume guard**~~ | M | **Done in 1.1.4.** `verifyResumableLocalPart` reads back a 256 KiB window of each claimed range from the server and compares it against the part, mirroring the upload path. The size check alone was vacuous — preallocation guarantees it — so a snapshot restore or a part from a different file of the same length would have been published with the right length and the wrong bytes. |

## Phase 2 — Things you have already asked for

| # | Item | Size | Notes |
|---|---|---|---|
| 2.1 | **Drag and drop** | L | Cross-pane drop queues a transfer; drop on a folder row targets it; same-pane drop onto a folder moves (destructive — needs confirmation). Explicitly excludes drag in/out of Explorer. |
| 2.2 | ~~**Close-guard dialog + close-to-pill**~~ | M | **Done in 1.1.8** (WP-B1 + WP-F1, the must-ship pair). `OnBeforeClose` emits and returns on the UI thread — no channel wait, no WaitGroup, no SQL — and the answer arrives later through ConfirmQuit/CancelQuit/CloseToPill. Three escapes keep the window closable: the quit latch, a second close gesture, and a 2 s force-quit if the frontend never acks. `ui.close_action` (ask/quit/pill) applies only while transfers are RUNNING, so an idle app always closes instantly. WP-B2 (graceful drain) and B3/F2/F3 remain. |
| 2.3 | ~~**Folder tree children cache**~~ | S | **Done in 1.1.8.** `frontend/src/lib/treeCache.ts` holds children, expansion and failed-listing markers keyed by source+path, outside React so a branch survives collapsing its parent and closing the sidebar. Invalidated subtree-wide by `fs:changed` and purged per source on connect/disconnect. Measured: re-expand went from 1 listing to 0, restoring a collapsed branch from N to 0. Also fixed a staleness bug this uncovered — every remote root is "/", so switching a pane between sites showed the previous server's folders. |
| 2.4 | **Upload throughput** | ? | Blocked on measurement: 8-lane and 16-lane numbers on an empty queue. 1 lane = 35 KiB/s vs 3 lanes = 905 KiB/s is 26×, which points at per-stream collapse on the link rather than warpseed. |

### Open questions blocking 2.1 and 2.2

Raised by the Phase 2 mapping on 2026-09-08. Each changes what gets built, so they are decisions
rather than implementation detail.

**2.2 close guard — RESOLVED 2026-09-10 by reading the recovered design**
1. ~~What closes the app with `close_action = 'pill'`?~~ The setting is scoped to *"when closing with
   transfers running"*: with nothing running `decideClose` always allows, whatever the preference. No
   un-closable window, and `TestCloseGuardAlwaysHasAnExit` pins it for every preference value.
2. ~~`running == 0` with pending rows.~~ Allow, per the design, matching today's behaviour exactly.
3. ~~Guard versus the shared confirm slot.~~ The guard is its own component with its own store flag,
   so it cannot collide with `askConfirm`.

**2.1 drag and drop**
4. Cross-pane drop where both panes are the SAME source (both local, or the same site): reject with a
   toast, or treat it as a move? The queue is siteId-based and has no same-kind cross-pane path, so
   rejecting is the literal reading of the roadmap.
5. Drop onto a folder row in the OTHER pane, same source — move (matching the same-pane rule) or the
   rejection above? This is the case a two-local-panes user hits first and the roadmap does not cover.
6. Remote move partial failure: `MoveInto` returns a count-moved-so-far plus an error. Report
   "Moved 3 of 7, then: <error>" as localfs already does, or attempt a rollback that can itself fail?

## FTP / FTPS — assessed 2026-09-09, verdict: DEFER behind 3.1

Asked for as a fallback for cPanel hosts that only offer FTP. Deferred, not
rejected, and the reasoning matters more than the verdict:

- **There is no engine to swap.** `internal/engine/core/core.go` declares no
  interface at all; the concrete `*sftpfast.Client` is threaded through `app.go`
  (24 lines) and `internal/dispatch/dispatcher.go` (21, including
  `type Factory func(...) ([]*sftpfast.Client, error)`). Adding a protocol starts
  with an interface extraction across the two files where this project's worst
  defects have lived.
- **Hyperlane does not survive the port, and half of it cannot.** Chunked
  *download* is possible but expensive: FTP has no ranged read, so each lane
  needs its own control connection doing PASV/EPSV → REST → RETR, and RETR
  streams to EOF with no way to stop at an offset. Chunked *upload* is
  impossible — preallocation (ALLO is advisory), ranged writes, fsync and atomic
  replace all have no FTP equivalent, and concurrent STOR to one path is
  undefined by the protocol.
- **What an FTP user would actually get:** browsing, mkdir/rename/delete,
  single-stream download and upload with REST resume. Not Hyperlane. Anyone
  reading "FTP supported" will expect Hyperlane speeds and report the
  single-stream rate as a bug, so the copy has to be solved before shipping.
- **Error classification breaks.** `core.Classify` matches SSH wire strings;
  FTP returns numeric `textproto.Error` codes, so every FTP failure would fall
  through to ClassPermanent and never retry.
- **Security promises change.** Host-key TOFU is an SFTP concept; FTPS needs
  certificate pinning instead, and plain FTP sends credentials in the clear,
  which contradicts what the app currently promises.

**Recommendation:** ship 3.1 (SSH key and agent auth) first — it serves far more
seedbox users than a cPanel-only FTP host does. Revisit FTP after, and if it
ships, ship it as "browse and transfer, single stream" with that stated plainly.

## Phase 3 — The biggest missing capability

| # | Item | Size | Notes |
|---|---|---|---|
| 3.1 | **SSH key and agent auth** | M | `client.go:72` offers `ssh.Password` only. For a public SFTP client this is the largest functional gap — most seedbox users authenticate with keys. Private key files with passphrase, plus Pageant/OpenSSH agent, plus site-manager UI. |

## Phase 4 — Explorer parity

| # | Item | Size |
|---|---|---|
| 4.1 | **Compare directories (Shift+F2)** — "what haven't I pulled down yet", the question the tool exists to answer. Zero round trips over listings already in memory. Arguably the highest-value item in this phase. | M |
| 4.2 | Hidden/system file filtering + Ctrl+Alt+H (browsing `C:\` shows `pagefile.sys`, `$Recycle.Bin`…) | S |
| 4.3 | Alt+D alias for Ctrl+L, F4 address dropdown | S |
| 4.4 | Type column, header column chooser, double-click-divider autofit | M |
| 4.5 | Inline F2 rename (currently a modal; must survive virtualizer recycling and the 500ms `fs:changed` reload) | M |
| 4.6 | Rubber-band drag selection | M |
| 4.7 | Ctrl+Left/Ctrl+Right push folder to other pane, Ctrl+U swap panes (resolve the WinSCP binding conflict first) | S |
| 4.8 | DirTree: keyboard reachability, roving arrows, resizable width, auto-expand-to-path | M |
| 4.9 | Breadcrumb sibling dropdowns and remote path autocomplete | M |
| 4.10 | Move operations within a pane (destination picker) | M |

## Phase 5 — Robustness and platform

| # | Item | Size | Notes |
|---|---|---|---|
| 5.1 | Windows Recycle Bin for local deletes | M | `localfs.Delete` is `os.RemoveAll`. Needs `SHFileOperationW`; cross-compiles but cannot be tested from Linux — verify natively. |
| 5.2 | Single-instance lock | S | A relaunch should surface the running window. |
| 5.3 | Shared `useFocusTrap` for all dialogs | S | None of PromptDialog, HostKeyDialog, QuickConnect or SettingsDialog trap Tab. |
| 5.9 | ~~**Window narrower than ~960px clips the right of every row**~~ | S | **Done in 1.1.7.** `.app` now uses `grid-template-columns: minmax(0, 1fr)` instead of letting the column size to its widest child, so `.dock__body` scrolls sideways rather than the app overflowing into a clipping ancestor. Found again from the other end: column widths were draggable but useless, because widening File pushed the rest past the invisible edge. |
| 5.4 | Full ARIA grid with `aria-activedescendant` | M | Needs NVDA testing on Windows; getting it wrong is silent. |
| 5.5 | Taskbar button progress (`ITaskbarList3`) | M | What Microsoft recommends for long-running work. Unproven spike. |
| 5.6 | Bump CI actions off deprecated Node 20 | S | |
| 5.7 | Orphaned-placeholder sweeper | M | Catches the exit-then-clear case no per-row fix reaches. |
| 5.8 | Rolling checksum per checkpoint, or `check-file@openssh.com` | M | Head/tail sampling passes on corruption confined to a range's middle. |

## Phase 6 — New surface

| # | Item | Size |
|---|---|---|
| 6.1 | Installer + code signing (removes the SmartScreen warning) | M |
| 6.2 | FTP/FTPS/S3/WebDAV via rclone-as-library (`rcadapter` designed, not written) | L |
| 6.3 | Drag out to Explorer — needs native OLE `IDataObject`; Wails v2 does not expose it | L |
| 6.4 | Starmap view; Timeline history persisted to the database | M |

---

## Ordering

Phase 1 first, in numbered order — 1.2 through 1.5 are all small and land in
the same area of the dispatcher, so they go together after 1.1.

Then 2.1 and 2.2, then 3.1, which is the thing most likely to turn a curious
Reddit visitor away.

Phase 4 onward is genuinely optional and should be re-prioritised against real
feedback rather than this list.

## Standing rules learned the hard way

- **Measure layout, never reason about it.** Three agents read the header DOM
  from source and all missed a CSS override that made every column header
  unclickable. `document.elementFromPoint` in a real browser found it in
  seconds. The mock backend (`?mock=1`) makes this cheap.
- **Get the A/B before fixing a performance problem.** The fsync hypothesis
  was arithmetically plausible, consumed a build cycle, and was wrong.
- **Every check must be run, not asserted.** `gofmt`, `go vet`, `go test`,
  `tsc`, `vite build`, and the `GOOS=windows` cross-build before every commit.
