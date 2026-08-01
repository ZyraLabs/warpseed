# FileBeam — Fork-First Design: rclone-ui (Tauri 2 + React) fork driving rclone rcd, with an app-owned persistent queue, pre-flight host-key TOFU, and a staged attack on rclone's SFTP speed gaps

## SUMMARY
Fork rclone-ui/rclone-ui (Apache-2.0, Tauri 2 + React + TypeScript, v3.7.1 2026-07-15) — the only living, permissively licensed project that already ships ~80% of the hard requirements: a dual-pane Commander with independent per-panel navigation over async rclone rc jobids (browse-while-transfer solved by construction), a working rcd sidecar lifecycle, and a Linux→Windows x64 cross-build already scripted in its package.json (cargo-xwin + NSIS). Keep rclone v1.75.0 (MIT) as the engine via `rclone rcd` on an authenticated random loopback port. The delta work is four well-scoped subsystems the fork lacks: (1) an app-level SQLite-WAL persistent transfer queue whose scheduler dispatches `_async` rc jobs and implements pause/resume as job/stop + requeue (rcd jobs are in-memory and cannot be the queue of record); (2) an SFTP-first site manager with secrets in Windows Credential Manager (keyring-rs) injected per-call via rclone connection strings, never persisted in rclone.conf; (3) host-key TOFU solved concretely despite rcd having no interactive prompt channel — a russh-based pre-flight keyscan in the Tauri core populates an app-owned known_hosts file BEFORE rclone ever connects; (4) tuned SFTP speed defaults (concurrency 128, chunk_size 255k, aes-gcm ciphers) that saturate 1 Gbps on a single connection today, with a Phase-3 Go helper (`fastsftp.exe`, pkg/sftp) closing rclone's two real gaps — multi-connection-per-file SFTP and byte-level resume — for files over ~1 GiB. Solo-dev honest: new Rust is confined to ~4 small modules (~2k lines); everything else is TypeScript. MVP in ~5-7 weeks, shippable signed v1 in ~10-12 weeks.

## ARCHITECTURE
## PROCESS MODEL (3 processes + 1 optional helper)

1. **Tauri host process (Rust)** — window management, sidecar lifecycle, and the four new native modules: QueueScheduler, SecretStore, HostKeyService, SQLite store. This process is the queue's system of record and the only thing that talks to rcd's HTTP API for job control (the webview talks to Rust via Tauri commands/events, not to rcd directly — one auth boundary, one client).
2. **WebView2 process (React/TS UI)** — dual-pane Commander (kept from fork), queue panel, site manager, dialogs. Runs in its own OS process; heavy engine work cannot jank it by construction (GUI dossier).
3. **rclone rcd sidecar** — Tauri `externalBin` (`binaries/rclone-x86_64-pc-windows-msvc.exe`, pinned v1.75.0, upgradeable independently of app releases). Spawned as: `rclone rcd --rc-addr 127.0.0.1:<random-port> --rc-user <per-launch-random> --rc-pass <per-launch-random> --use-json-log --log-level INFO`. No `--rc-no-auth`, no web GUI serving. Health-checked with `rc/noop` on 2s interval; auto-respawn with exponential backoff; killed on app exit (Tauri on-exit hook + Windows Job Object so orphans die with the parent). Crash isolation: rcd dying never kills the GUI; queue rows in `running` state are reset to `pending` and re-dispatched.
4. **fastsftp.exe (Phase 3, Go)** — optional per-transfer helper for huge single files (multi-connection + byte-resume). Detailed below.

## FORK SURGERY — KEEP / RIP OUT / BUILD

**KEEP (verified present in rclone-ui v3.7.1 source per OSS dossier):** Tauri 2 scaffold and Rust sidecar management; `src/pages/Commander.tsx` dual-pane layout (react-resizable-panels, left/right FilePanel refs); the navigator suite (FileList, FilePanel, PathBreadcrumb, RemoteSidebar, PreviewDrawer, useFileNavigation); the rc client layer and its jobid tracking (/sync/copy, /operations/copyfile → jobid → panel refresh); `build:windows: tauri build --runner cargo-xwin --target x86_64-pc-windows-msvc` in package.json; updater + notifications plumbing; winget/NSIS packaging path.

**RIP OUT:** cloud-manager onboarding and marketing UX; mount/VFS management (WinFSP fragility, out of scope); the cross-platform scheduler (schtasks.rs/launchd.rs/crontab.rs); webhooks; flathub/brew packaging (retain winget + NSIS); any telemetry/paid-tier hooks; HeroUI default look (reskin per Phase 2 — anti-template design rules apply).

**BUILD (the four deltas):** persistent queue + scheduler; SFTP-first site manager + credential layer; host-key TOFU flow; queue panel UI + progress interpolation. Plus Phase-3 fastsftp helper.

## MODULE LAYOUT (repo: /home/fredxpert/DevProjects/filebeam, own git repo per workspace dossier; upstream kept as `upstream` remote)

```
filebeam/
├── src/                          # React + TS (bulk of feature work lives here)
│   ├── pages/Commander.tsx       # kept, reskinned
│   ├── components/navigator/     # kept (FileList/FilePanel/Breadcrumb/RemoteSidebar)
│   ├── features/queue/           # NEW: QueuePanel.tsx, TransferRow.tsx, useQueue.ts
│   ├── features/sites/           # NEW: SiteManager.tsx, SiteForm.tsx, HostKeyDialog.tsx,
│   │                             #      ConflictDialog.tsx
│   ├── lib/rc.ts                 # kept/extended rc client (browse-path only)
│   └── lib/progress.ts           # NEW: poll interpolation (rAF lerp + EMA speed)
├── src-tauri/src/
│   ├── sidecar.rs                # kept: spawn, random port, per-launch creds, health
│   ├── queue/{model,store,scheduler}.rs   # NEW: rusqlite WAL store + tokio scheduler
│   ├── hostkey.rs                # NEW: russh keyscan + app-owned known_hosts writer
│   ├── secrets.rs                # NEW: keyring-rs → Windows Credential Manager
│   └── fastpath.rs               # Phase 3: fastsftp.exe invocation + checkpoint mgmt
├── helper/fastsftp/              # Phase 3: Go, pkg/sftp + x/crypto/ssh
├── binaries/                     # rclone sidecar per target triple
└── .github/workflows/{ci.yml,release.yml}
```

**Storage (%APPDATA%/FileBeam/):** `app.db` (SQLite WAL: sites, transfers, settings, host_keys); `known_hosts` (app-owned, rclone points at it); `logs/`; rclone.conf stays EMPTY of secrets (see credentials). On Linux/macOS later: XDG/Library equivalents — nothing Windows-specific in the schema.

## HARD REQUIREMENT #1 — SPEED

- **SFTP (primary):** per-site remote options injected on every call: `concurrency=128` (up from default 64 outstanding requests/file), `chunk_size=255k` (fallback ladder to 32k on server error — engine dossier: 128×255k ≈ 32 MB in flight ≈ saturates 1 Gbps well past 120 ms RTT), `ciphers` preferring aes128-gcm@openssh.com / chacha20-poly1305, `connections=0` (unlimited pool, capped by our scheduler), concurrent reads/writes left ON (rclone defaults). Global `--transfers 4` default, per-site override (1-8) exposed in site settings.
- **FTP/FTPS:** `--ftp-concurrency` per site; FTPS is raw TLS streams — often the fastest option when offered.
- **S3/WebDAV:** `--multi-thread-streams 4 --multi-thread-cutoff 256Mi` passed via per-job `_config` (works on these backends; NOT on SFTP — honest). S3-compatible (MinIO) site type is first-class for user-controlled servers: the fastest secure mechanism rclone offers, multipart resume included, zero extra engine code.
- **Auto-degrade on server caps (arch dossier):** scheduler watches job errors for "administratively prohibited"/connection-refused (OpenSSH MaxSessions=10 / MaxStartups 10:30:60); on hit it halves the per-site concurrent-transfer cap, requeues (not fails) the transfer, and ramps back up lazily. rclone alone fails hard here; the app must not.
- **Phase 3 gap-closure:** (a) `fastsftp.exe` — N SSH connections (default = min(4, cores)), offset-ranged reads/writes into a preallocated destination file (merge-free, avoiding Cyberduck's segment-assembly corruption mode), JSON progress on stdout, checkpoint file for byte-resume; invoked by the scheduler for files > 1 GiB on sites flagged multi-connection-tolerant (probe on first use). mscp (PEARC'23) proves this works against stock sshd. (b) In parallel, upstream a chunked/OpenChunkWriter SFTP contribution to rclone issue #8185 — if merged, the helper retires.

## HARD REQUIREMENT #2 — BROWSE WHILE TRANSFERS RUN

Solved by construction, twice over: (1) rc calls are fully concurrent — `operations/list` for a panel runs on its own SFTP connection from rclone's internal pool while `_async` sync/copy jobs stream data (engine dossier: this is the direct CuteFTP fix; no VFS/mount involved). (2) The UI lives in a separate WebView2 process, so even a saturated engine cannot freeze rendering. The fork's Commander already refreshes panels off jobid completion. Scheduler additionally reserves headroom: per-site transfer cap ≤ (observed session cap − 1) so a listing channel is always available — the WinSCP/FileZilla "dedicated browse connection" lesson applied to rclone's pool.

## HARD REQUIREMENT #3 — PERSISTENT QUEUE WITH PAUSE/RESUME

rcd jobs are in-memory and jobids reset per process (arch dossier) → **the queue of record is ours**: SQLite WAL, transactional state transitions (FileZilla's queue.sqlite3 pattern minus its corruption bug class).

- **Schema:** `transfers(id, site_id, direction, src_path, dst_path, kind, size, state, jobid, rc_group, bytes_done, attempts, priority, error, created_at, updated_at)`; states: pending → dispatched → running → {done | failed | paused | canceled}.
- **Dispatch:** scheduler (tokio task in Tauri core) pops by priority/FIFO within per-site cap and global cap; issues `operations/copyfile` (file) or `sync/copy` (dir) with `_async=true`, `_group="queue/<transfer-id>"`, per-job `_config` (transfers, multi-thread flags, `--partial`; NEVER `--inplace` — arch dossier: inplace can delete pre-existing destination data on failure).
- **Progress:** scheduler polls `core/stats` with `group=queue/<id>` for each active job on a 300 ms tick (one HTTP round per job, batched), plus `job/status`; results coalesced and pushed to the webview as Tauri events at ~5-10 Hz. **Smooth bars from poll-only stats:** `lib/progress.ts` interpolates bytes between samples via requestAnimationFrame using last-known speed, snaps on each real sample, and smooths displayed speed with an EMA — visually indistinguishable from push. `core/transferred` backfills per-file completion records.
- **Pause (single):** `job/stop {jobid}` → state=paused, bytes_done retained for display. **Pause all:** stop dispatching + job/stop all active. **Resume:** re-dispatch; for directories, sync/copy inherently skips completed files (size/modtime) → file-granularity resume for free. For a single interrupted file rclone restarts at byte 0 — surfaced honestly in the UI ("restarting file") until Phase 3's fastsftp gives true byte-resume with checkpoints.
- **Crash/restart recovery:** on startup, any dispatched/running rows are reset to pending; user is offered auto-resume. Two-level retry per arch dossier: rclone's `--low-level-retries` handles op-level; scheduler adds file-level exponential backoff + jitter (max 5 attempts), distinguishing transient vs permanent errors from rclone's JSON log/error strings.
- **Bandwidth:** global limit via live `core/bwlimit {rate: "up:down"}`; per-job `BwLimit` in `_config` for per-site limits.
- **Overwrite/conflict policy (no interactive prompt channel exists in rcd):** conflicts are resolved BEFORE dispatch — scheduler runs `operations/stat` on the destination; if it exists, apply the site policy (ask-once-in-UI / overwrite / skip / rename-with-suffix) while the item sits in pending. No mid-transfer prompt is ever needed.

## HARD REQUIREMENT #4 — HOST-KEY UX (rcd has NO interactive prompt channel — concrete solution)

The app owns trust, not rclone. `hostkey.rs` (russh, Apache-2.0, pure Rust, in the Tauri core — no extra binary):
1. On first connect / "Test Connection", perform an SSH transport handshake only (client handler captures the server host key then aborts — ~150 lines of russh); compute SHA256:base64 fingerprint.
2. React `HostKeyDialog` shows fingerprint + key type for TOFU accept/reject (blocking Tauri command round-trip — trivially possible because this never goes through rcd).
3. On accept, append to app-owned `%APPDATA%/FileBeam/known_hosts`; the site's rclone remote always carries `known_hosts_file=<that path>` → rclone runs STRICT host-key checking from then on. rclone's insecure default (no check when known_hosts_file unset) is never used.
4. **Key change:** rclone job fails with a host-key mismatch error → scheduler intercepts, re-runs keyscan, raises a loud, visually distinct "HOST KEY CHANGED" alarm dialog showing old + new fingerprints; the known_hosts entry is rewritten only on explicit confirmation. Pre-pinned fingerprints in site config supported for scripted use. Import from OpenSSH known_hosts and PuTTY registry offered in the site manager (v1).

## HARD REQUIREMENT #5 — CREDENTIALS ON WINDOWS

- Site definitions (host/port/user/options) live in app.db. **Secrets never touch rclone.conf or app.db**: passwords and key passphrases are stored in Windows Credential Manager (DPAPI-backed) via keyring-rs under `filebeam:site:<uuid>`.
- At call time the scheduler builds an **on-the-fly connection-string remote** — `:sftp,host=...,user=...,port=...,known_hosts_file=...,pass=<obscured>:` — obscuring the password via `core/obscure` first; secrets exist only in memory and on the authenticated loopback request. Nothing persists in rclone's config.
- **Keys agent-first:** `key_use_agent=true` targeting the Windows OpenSSH agent (`\\.\pipe\openssh-ssh-agent`); fallback to key file + passphrase-from-Credential-Manager. Pageant users are pointed at its OpenSSH-pipe mode (modern Pageant exposes one) — full dual-protocol support deferred past v1, documented.
- DPAPI caveat handled: if Credential Manager read fails (passwordless/S4U logon), degrade to session-scoped prompting.
- rcd surface hardening: loopback-only bind, random port, per-launch random user/pass, JSON log scrubbing of connection strings.

## SOLO-DEV HONESTY — RUST+REACT SURFACE

New Rust is fenced into 4 modules (queue store/scheduler, secrets, hostkey, fastpath) — roughly 1,500-2,500 lines total, mostly cookbook tokio + rusqlite + Tauri command patterns; the fork already contains the genuinely fiddly Rust (sidecar lifecycle, packaging). All product/UI iteration happens in TypeScript where ecosystem and AI-assist leverage are highest. Escape hatch if Rust drags: the scheduler can be reimplemented in TS inside the webview against tauri-plugin-sql, at the cost of queue processing pausing when the window is destroyed — acceptable fallback, not the plan.

## UPSTREAM DIVERGENCE POSTURE

Treat the fork as a **one-time booster, not a tracked dependency**: all new code lives in new directories (`features/queue`, `features/sites`, `src-tauri/src/queue` etc.); upstream remains a git remote for selective cherry-picks (security fixes, Tauri version bumps) only; no rebase treadmill after Phase 1. The engine is decoupled by design — rclone.exe upgrades are a binary swap validated by a smoke suite against the rc endpoints we use (operations/list, operations/stat, operations/copyfile, sync/copy, job/status, job/stop, core/stats, core/bwlimit, core/obscure, core/transferred, rc/noop). Upstream rclone-ui's ~2-dev bus factor therefore stops mattering ~6 weeks in.

## LINUX→WINDOWS BUILD & CI

- **Daily dev on Ubuntu:** `pnpm tauri dev` runs the Linux (webkitgtk) build for fast iteration; rc layer is byte-identical across platforms.
- **Windows artifact from Linux:** the fork's existing `pnpm tauri build --runner cargo-xwin --target x86_64-pc-windows-msvc` + `apt install nsis lld llvm clang`; sidecar shipped as `binaries/rclone-x86_64-pc-windows-msvc.exe` (plain GOOS=windows rclone build — trivial, CGO off).
- **GitHub Actions:** `ci.yml` — ubuntu-latest: eslint/tsc/vitest, cargo clippy/test, cross-build NSIS on every PR (fail-fast canary for cargo-xwin breakage). `release.yml` — ubuntu job produces the NSIS installer; windows-latest job runs a WebView2 smoke test (Playwright against the built app), signs via azure/trusted-signing-action (Artifact Signing, ~$10/mo, Windows-runner-only), publishes GitHub Release + winget manifest. MSI/WiX deferred (Windows-only tooling; NSIS suffices for v1). Optional local Win11 eval VM (quickemu) for interactive debugging only.
- **Later Linux/macOS builds:** Tauri targets both; the only Windows-specific code paths (Credential Manager, agent pipe) sit behind the keyring-rs abstraction which already backs onto Secret Service/Keychain — cheap ports by design.

## STACK
**Engine:** rclone v1.75.0 (2026-07-31), MIT — sidecar binary, pinned per release, independently upgradeable. Phase 3 helper: Go 1.25 (BSD), github.com/pkg/sftp (BSD-2) + golang.org/x/crypto/ssh (BSD-3).
**Fork base:** rclone-ui/rclone-ui v3.7.1 (2026-07-15), Apache-2.0 — permits closed or open derivative; NOTICE/attribution retained.
**App shell:** Tauri 2.x (MIT OR Apache-2.0), Rust stable ~1.8x; tauri-plugin-single-instance, tauri-plugin-updater (MIT/Apache); tauri-plugin-drag (community, MIT) evaluated for drag-out in v1.x.
**Rust crates:** tokio 1.x (MIT), rusqlite 0.3x bundled SQLite (MIT), russh 0.4x+ (Apache-2.0, handshake-only host-key capture), keyring 3.x (MIT/Apache → Windows Credential Manager / Secret Service / Keychain), reqwest 0.12 (MIT/Apache, rc HTTP client), serde/serde_json (MIT/Apache).
**Frontend:** React 18/19 (MIT), TypeScript 5.x (Apache-2.0), HeroUI (MIT, inherited then reskinned), react-resizable-panels (MIT, inherited), TanStack Virtual v3 (MIT) for 100k-row directory listings, Zustand (MIT) for queue/UI state.
**Build/CI:** pnpm; cargo-xwin (MIT/Apache) for x86_64-pc-windows-msvc from Ubuntu; NSIS 3 (zlib) via apt; GitHub Actions ubuntu-latest + windows-latest; azure/trusted-signing-action (MIT action; Azure Artifact Signing ~$10/mo); winget manifest.
**Storage:** SQLite (public domain) WAL mode; app-owned OpenSSH known_hosts file format.
**License posture:** entire shipped stack is MIT/Apache/BSD/zlib — no copyleft anywhere in the distribution.

## PHASES
### Phase 0 — Scaffold & fork surgery (1-1.5 weeks solo)
Create /home/fredxpert/DevProjects/filebeam as its own git repo from the rclone-ui fork (upstream kept as remote). Strip branding, mounts, scheduler, webhooks, non-Windows packaging. Rebrand, pin rclone v1.75.0 sidecar, verify sidecar spawn hardening (random port, per-launch creds, Job Object cleanup). Prove the pipeline end-to-end: Linux dev loop runs; cargo-xwin + NSIS cross-build produces an installer that boots in a Win11 VM with working dual-pane browse of an SFTP remote. CI skeleton (lint/test/cross-build on PR).
### Phase 1 — MVP (SFTP-first, the four deltas) (4-6 weeks solo)
Site manager (SFTP + S3 types) with keyring-rs Credential Manager secrets and connection-string remotes via core/obscure; russh pre-flight keyscan + TOFU HostKeyDialog + app-owned known_hosts + key-changed alarm; persistent SQLite queue + tokio scheduler (dispatch _async jobs with _group, pause=job/stop+requeue, directory-granularity resume, crash recovery, two-level retry with backoff+jitter, auto-degrade on 'administratively prohibited'); queue panel UI with interpolated progress (300ms core/stats polls → rAF lerp); tuned SFTP defaults (concurrency 128, chunk_size 255k with fallback ladder, aes-gcm ciphers); pre-dispatch overwrite policy via operations/stat. Exit criterion: daily-drivable CuteFTP replacement for SFTP — browse both panes fluidly while a multi-GB queue runs, survive app restart mid-queue.
### Phase 2 — v1 (breadth, polish, distribution) (3-5 weeks solo)
FTP/FTPS and WebDAV site types with per-protocol tuning (--ftp-concurrency; --multi-thread-streams for S3/WebDAV via _config); global + per-site bandwidth limits (core/bwlimit live, per-job BwLimit); known_hosts/PuTTY import; full reskin per anti-template design rules (Fluent-2-leaning, both themes); TanStack Virtual on FileList for huge directories; auto-update wiring; Azure Artifact Signing + winget + GitHub Releases; Playwright WebView2 smoke suite on windows-latest; docs. Exit criterion: signed, updatable public v1.
### Phase 3 — v1.x speed gap closure (optional but planned) (2-4 weeks solo)
fastsftp.exe Go helper: N-connection offset-ranged transfers into a preallocated destination (merge-free), checkpoint file giving true byte-level pause/resume, JSON progress on stdout; scheduler routes files >1 GiB on multi-connection-tolerant sites (probed once per site) to the helper. In parallel, prepare an upstream rclone PR toward issue #8185 (chunked multi-connection SFTP) — if merged, retire the helper. Also: drag-out to Explorer via tauri-plugin-drag or custom CFSTR_FILEDESCRIPTOR shim; Pageant dual-protocol agent support.

## RISKS
- [HIGH] rcd has no interactive prompt channel — host-key trust and overwrite decisions cannot be asked mid-operation, and a naive build ships rclone's insecure no-host-key-check default. -> Move every interactive decision out of the transfer path: russh pre-flight keyscan + TOFU dialog populates an app-owned known_hosts BEFORE rclone connects, and every remote carries known_hosts_file for strict checking; overwrite conflicts resolved pre-dispatch via operations/stat + site policy. Key-mismatch job errors are intercepted and raised as a distinct changed-key alarm. This is designed-in from Phase 1, not bolted on.
- [HIGH] rclone cannot byte-resume an interrupted single file (restarts at byte 0) and has no per-transfer pause — 'pause/resume' could disappoint on huge single files, and --inplace can even destroy existing destination data on failure. -> Always --partial, never --inplace. Pause/resume implemented app-side as job/stop + requeue; directory transfers resume at file granularity for free via sync semantics. UI is honest ('file will restart'). Phase 3 fastsftp helper adds true checkpointed byte-resume + multi-connection speed for >1 GiB files, with an upstream #8185 contribution as the long-term retirement path.
- [MEDIUM] cargo-xwin Linux→Windows cross-build is officially 'experimental' and could break on a Tauri/Rust/toolchain bump, stranding releases. -> The fork already ships this exact pipeline (working prior art). Pin Rust toolchain, Tauri, and cargo-xwin versions; run the cross-build on every PR as a fail-fast canary; keep a fallback native-build job on windows-latest in release.yml (the signing step already requires a Windows runner, so the fallback path is pre-paid).
- [MEDIUM] Solo dev drowns in the Rust half of the Tauri stack (borrow checker, async, FFI-ish plumbing), stalling the queue scheduler — the one component everything depends on. -> Confine new Rust to 4 small cookbook-pattern modules (~2k lines); all product iteration stays in TypeScript; the fork already contains the hardest Rust (sidecar lifecycle, bundling). Defined escape hatch: reimplement the scheduler in TS against tauri-plugin-sql if velocity demands, accepting queue-pauses-with-window as the tradeoff. Time-box the Rust scheduler to 2 weeks before triggering the fallback.
- [MEDIUM] Upstream divergence: rclone-ui is a ~2-core-dev project — it may pivot, stall, or refactor in ways that make cherry-picking impossible; meanwhile rclone's rc API could shift under a sidecar upgrade. -> Treat the fork as a one-time booster: isolate all new code in new directories, stop rebasing after Phase 1, cherry-pick only security/Tauri fixes. Decouple the engine: rclone.exe is pinned per release and upgraded deliberately behind a smoke suite covering the 11 rc endpoints in use; rclone's rc API has years of stability and multiple shipping GUIs depending on it, making breaking changes unlikely and detectable.
