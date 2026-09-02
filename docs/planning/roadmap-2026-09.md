# warpseed roadmap — from 1.1.0

Consolidated from the two design workflows of 2026-09-02 (navigation/sorting,
close-guard), the 1.0 roadmap, and everything found during the 1.1.0 work.
Ordered by what it costs the user, not by what is interesting to build.

Legend: **[S]** small (under an hour) · **[M]** medium (a few hours) ·
**[L]** large (a day or more)

---

## Phase 1 — Data safety

These can lose or waste a user's data. Nothing else ships first.

| # | Item | Size | Why it matters |
|---|---|---|---|
| 1.1 | **Overwrite/conflict policy** | M | Downloading a file you already have silently replaces it, *after* re-transferring the whole thing. No prompt, no check (`download.go:106`). User-specified rules: newer+larger → overwrite, smaller → ask, older → ask, configurable, with skip/overwrite/rename and apply-to-all. |
| 1.2 | **Stop discarding a chunk plan on a one-connection run** | S | `dispatcher.go:427-434` clears the plan whenever `chunked` is false, and `chunkPlan` returns false whenever fewer than 2 connections arrive — routine under OpenSSH `MaxStartups`. A 90%-done 50 GB transfer can throw away 45 GB and restart linearly. Worst efficiency bug in the codebase. |
| 1.3 | **Per-destination in-flight lock** | S | Two queue rows with the same destination compute the same placeholder path and interleave writes. Benign for downloads (identical bytes); for uploads the sources can differ. Add a `siteID+Dst` lock plus a duplicate-Dst check in `EnqueueTransfer`. |
| 1.4 | **Clear `attempt`/`error` in `RecoverInterrupted`** | S | A row killed mid-transfer at attempt 2 gets zero retries on its next genuine failure (`maxAttempts` is 3) and carries a stale error into a fresh run. Closing the app is not a transfer failure. |
| 1.5 | **Clean up placeholders on cancel** | S | Cancel sets state and cancels the context but removes nothing. A cancelled 50 GB chunked upload leaves a full-size `.wschunk` on the seedbox — sparse on ext4/XFS, but seedbox quotas are usually billed on *apparent* size. |
| 1.6 | **Strengthen the chunked download resume guard** | M | Downloads check size only, which preallocation makes near-vacuous by the code's own admission (`chunked.go:120-122`). Uploads already do head/tail byte comparison. Bring downloads up to the same bar. |

## Phase 2 — Things you have already asked for

| # | Item | Size | Notes |
|---|---|---|---|
| 2.1 | **Drag and drop** | L | Cross-pane drop queues a transfer; drop on a folder row targets it; same-pane drop onto a folder moves (destructive — needs confirmation). Explicitly excludes drag in/out of Explorer. |
| 2.2 | **Close-guard dialog + close-to-pill** | M | Fully designed (WP-B1, WP-F1 must-ship; B2 is already done). Wails v2 has no tray, so the mini pill is the answer. `OnBeforeClose` must emit-and-return, never block. |
| 2.3 | **Folder tree children cache** | S | `DirTree.tsx:66` discards children on collapse, so every re-expand re-lists over the network — worse now that nodes default to collapsed. Cache keyed by source+path, invalidated by `fs:changed`. |
| 2.4 | **Upload throughput** | ? | Blocked on measurement: 8-lane and 16-lane numbers on an empty queue. 1 lane = 35 KiB/s vs 3 lanes = 905 KiB/s is 26×, which points at per-stream collapse on the link rather than warpseed. |

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
