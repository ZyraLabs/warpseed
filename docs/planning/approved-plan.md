# Plan: DevProjects Workspace + "warpseed" — Windows x64 Fast Transfer Client

## Context

`~/DevProjects` is a brand-new empty directory that becomes the root for all development projects. The first project, **warpseed**, is a Windows x64 GUI file-transfer app replacing CuteFTP (slow; can't browse while downloading) and WinSCP (speeds have degraded). Primary use case: **seedbox transfers** — large single files over high-RTT links, exactly where SFTP clients collapse. Hard requirements: blazingly fast multithreaded transfers, secure protocols (SFTP first; FTP/FTPS, S3, WebDAV via rclone), browse-while-transferring, persistent queue with true pause/resume, built from this Linux machine for Windows x64.

Process (ultracode): 5 parallel research agents (engine, GUI stacks, OSS landscape, transfer-client architecture, workspace conventions) → 2 competing full designs (fork rclone-ui vs greenfield Go/Wails) → adversarial judge. **Verdict: greenfield Go + Wails hybrid won 54–49**, precisely because the two speed gaps rclone structurally cannot close (no multi-connection-per-file SFTP — issue #8185 open since 2024; no byte-level resume) are the ones that matter for seedbox workloads. Research dossiers + designs + verdict archived at `<session-scratch>/research_*.md`, `design_*.md`, `verdict.md` (copy into the repo's `docs/` during Phase 0).

User decisions: engine per research; GUI stack best-fit; Windows now / cheap x-plat later; all protocol families, speed-aligned; app name **warpseed**.

**Development workflow (established on the tech-time-tracker project):** Claude authors all code here on the Spark (Linux); the user works from their Windows dev box via VS Code remote, pulls the code, and **compiles/runs natively on Windows** — auditing as they go. Visual Studio is available on the Windows box (useful for WebView2 debugging and any win32 drag-out work later). The Linux cross-build (`CGO_ENABLED=0 GOOS=windows`) remains as Claude's fast sanity check that every commit compiles for Windows; the user's Windows build is the build of record. CI/code-signing infrastructure is deferred until release time. **MVP definition per user: transfer speed AND GUI speed — complete enough to daily-drive, ruthlessly optimized so it's smooth on Windows.**

---

## Part 1 — DevProjects workspace setup (do this first)

Verified Claude Code behavior driving the layout:
- Root `CLAUDE.md` **is** loaded in every child-repo session (parent-directory walk) → keep it <50 lines.
- Root `.claude/settings.json` is **NOT** inherited by child git repos → shared defaults live in `~/.claude/settings.json`; per-repo settings come from a template.
- Skill discovery stops at the repo root → share skills/rules by **symlinking** from `_shared/claude/` (officially supported).

### Create

```
DevProjects/
├── CLAUDE.md              # <50 lines: workspace map, naming, "one repo per project — never git init at root"
├── README.md              # human index of projects + status
├── .editorconfig          # tabs for Go; 2-space yaml/json/ts; lf; utf-8
├── .mise.toml             # pin Go 1.25.x + Node LTS
├── .claude/skills/new-project/   # scaffolder skill (root sessions only)
├── _shared/
│   ├── claude/{rules,skills}/    # canonical shared assets — symlink targets
│   ├── configs/                  # .golangci.yml, gitignore + per-repo .claude/settings.json templates
│   └── scripts/                  # new-project.sh, cross-build helpers
├── _templates/
│   ├── go-wails-app/      # incl. GH Actions linux-build → windows-sign pipeline
│   ├── go-cli/
│   └── pbi-project/       # PBIP/TMDL layout for Power BI work
├── _scratch/              # throwaway spikes
├── _archive/              # retired projects (keep .git)
└── warpseed/              # first project — own git repo
```

Naming: lowercase kebab-case repos; `_` prefix reserved for meta dirs.

### Install
- **Spark (Linux, Claude's side)**: `mise` (Go 1.25.x, Node LTS) · Wails v2 CLI + `wails doctor` · `nsis` (apt, optional) · `golangci-lint` · Taskfile · `gh`.
- **Windows dev box (user's side, one-time)**: Go 1.25.x · Node LTS · Wails v2 CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest` + `wails doctor`) · WebView2 runtime (preinstalled on Win11) · NSIS when packaging. Visual Studio already present.
- Later, at release time: Azure Trusted/Artifact Signing (~$10/mo) + optional GitHub Actions pipeline.

---

## Part 2 — warpseed architecture (judge-approved design + grafts)

### Shape

**One Go process, no sidecar.** Wails **v2.13 stable** (not v3 alpha; all Wails API usage confined to a `Bindings` facade + `EventSink` + frontend `ipc.ts` so a future v3 migration is a contained diff). The Go backend IS the transfer engine. React 18 + TS + Vite frontend renders in WebView2's separate process — engine load physically cannot jank the UI.

**Two engines, one queue:**
1. **`sftpfast` (custom, SFTP only)** — `github.com/pkg/sftp` + `x/crypto/ssh` (the libraries rclone itself uses). Pipelined 64–256 outstanding requests/file, chunk-size probe 32K→255K, cipher prefs (aes128-gcm, chacha20-poly1305). v1 adds mscp-style **multi-connection chunked transfers** (2–4 conns, offset ranges into one preallocated `.part` — merge-free) and **byte-level pause/resume** (`.part` + 64-bit SFTP offsets + size/mtime validation, per-chunk checkpoints). Exists precisely for the five things rclone can't do: byte-resume, multi-conn SFTP, push progress, blocking prompts, instant pause.
2. **`rcadapter` (rclone v1.75 as Go library)** — `librclone.RPC()` in-process (pure Go, CGO stays off; identical surface to rcd so swapping to a subprocess later is contained). Provides FTP/FTPS, S3/MinIO, WebDAV + 70 backends: `operations/list`, `sync/copy` with `_async`+`_group`, `job/status|stop`, `core/stats` (300ms poll → same typed events), `core/bwlimit`. Never `--inplace` (destination-deletion hazard). UI labels these sites honestly as "file-level resume".

**Key components** (`internal/…`):
- `queue/` — SQLite-WAL queue-of-record (`modernc.org/sqlite`, pure Go) at `%APPDATA%\warpseed\warpseed.db`. Tables: sites, transfers, chunks, hostkeys, settings. States `pending→dispatched→active→completed|paused|failed|cancelled`, transactional transitions, two-level retry (op-level immediate; file-level exponential backoff + jitter), crash recovery demotes active→pending on startup. rclone jobids never trusted across restarts.
- `engine/conn/` — **SiteConnectionManager**: one dedicated browse connection per site that NEVER carries file data (the structural CuteFTP fix) + lazily-grown transfer pool with 250–750ms jittered dials; classifies "administratively prohibited" (MaxSessions) vs TCP-refused (MaxStartups/per-IP caps — seedbox hosts commonly cap ~8/IP) and auto-shrinks + **requeues instead of erroring**; per-site cap user-settable (default 4).
- `events/` — EventCoalescer (≤10 Hz/transfer) + PromptBroker: blocking host-key TOFU / overwrite / FTPS-cert prompts over the Wails event bus (SHA256 fingerprint dialog; loud distinct "HOST KEY CHANGED" alarm). Graft: pre-dispatch conflict resolution (stat destination while pending, apply site policy) is the default; blocking prompts only for mid-transfer races.
- `creds/` — Windows Credential Manager via `danieljoos/wincred`; agent-first key auth: `\\.\pipe\openssh-ssh-agent` (go-winio) → Pageant fallback (go-pageant); all behind interfaces with fakes so the Linux dev loop and tests run without Windows. Graft: rclone-backed SSH-family sites get `known_hosts_file` pointed at an app-owned file populated by our own pre-flight keyscan — never rclone's no-check default.
- `frontend/` — dual-pane commander with TanStack Virtual lists (100k-row dirs), Zustand, deliberate Fluent-2-inspired custom design system (anti-template rules apply). Graft: **port rclone-ui's Apache-2.0 navigator components** (Commander/FilePanel/FileList/Breadcrumb, react-resizable-panels) instead of rebuilding — attacks the biggest schedule risk. Graft: rAF-lerp + EMA progress interpolation for rclone-polled transfers.

**Build & dev loop (split-machine):** Claude develops with `wails dev` natively on Linux (Windows-only paths behind build tags + fakes) and sanity-checks every commit with `CGO_ENABLED=0 GOOS=windows GOARCH=amd64` cross-compile (no CGO anywhere — all deps pure Go). The user pulls on the Windows box and runs `wails dev` / `wails build` natively — the build of record, exercising real DPAPI, agent pipes, WebView2, and the actual seedbox link. NSIS + signing + CI deferred to release time.

**GUI speed budget (MVP acceptance criteria, measured on the user's Windows box):** cold start to interactive < 2s; directory panes scroll at 60fps on 100k-entry listings (TanStack Virtual + no layout-bound animations per web perf rules); UI stays under 16ms/frame with 8 active transfers streaming progress (EventCoalescer ≤10 Hz/transfer, rAF-lerped bars); listing round-trip during heavy transfers < 500ms; zero UI stalls on queue operations (all engine calls async, never on the render path).

### Phases

**Phase 0 — Workspace + scaffold (~1–1.5 wks)**: Part 1 tree; `warpseed` repo from template; Wails v2 boots on Linux; windows/amd64 cross-build + NSIS proven day one; CI skeleton; SQLite schema + migrations; Bindings/EventSink facades; local-only dual-pane shell; copy research/design dossiers into `docs/`.

**Phase 1 — MVP: full-speed SFTP + smooth GUI (the user's MVP bar)**: complete sftpfast engine — single-connection pipelined (16–64× WinSCP's 1MB in-flight window) **plus multi-connection chunked transfers** (2–4 conns, offset ranges into preallocated `.part`, per-chunk checkpoint resume — pulled forward from Phase 2 because seedbox speed IS the MVP); SiteConnectionManager; persistent queue + crash recovery; byte-level pause/resume; site manager + wincred + agent auth; TOFU prompts; polished dual-pane GUI meeting the speed budget above (ported rclone-ui navigator components, deliberate design system — no template look). Graft: **S3/MinIO site type via librclone lands end of Phase 1**. Cuts: FTP/WebDAV, bandwidth limits, drag-out, importers, themes. User builds and benchmarks against their real seedbox throughout.

**Phase 2 — v1: multi-backend + refinement (~4–6 wks)**: full rcadapter (FTP/FTPS, WebDAV; SQLite+wincred-backed rclone config so secrets never hit disk); token-bucket bandwidth limits; known_hosts/PuTTY importers; FTPS cert TOFU; refinement pass on the MVP driven by the user's daily-driving feedback; testcontainers sshd matrix (hostile MaxSessions/MaxStartups configs); NSIS installer + signing.

**Phase 3 — v1.x (~3–5 wks, incremental)**: drag-out to Explorer (CFSTR_FILEDESCRIPTOR, Go/win32); Linux (libsecret) + macOS (Keychain) builds; optional MSIX; directory-sync view; evaluate contributing chunked SFTP upstream to rclone #8185; evaluate Wails v3 once GA.

### Top risks
- **HIGH — custom-engine scope balloon** → build order inside Phase 1: single-connection pipelined first (a working, already-fast client), then chunked multi-connection as a feature-flagged layer following mscp's proven design; property-based tests on resume/offset logic from the start; if chunked mode stalls, the MVP still ships fast on pipelining alone and chunking slips a release, not the MVP.
- **LOW (downgraded from HIGH) — Windows fidelity**: the user builds and runs natively on the Windows dev box throughout, exercising real DPAPI, agent pipes, and WebView2 continuously; creds stay behind interfaces with fakes so Claude's Linux test loop still covers the logic. SmartScreen/signing addressed at release.
- **MEDIUM — Wails v2→v3 / cross-compile not officially blessed** → three-file facade confinement; CI cross-builds + smoke-tests every merge; documented fallback = Tauri 2 + rcd sidecar (rclone-ui prior art) reusing the React frontend and Go queue/engine.
- **MEDIUM — rclone-as-library coupling** (~25–30MB binary, semantics leaking) → rclone pinned per release; only rcadapter imports it; graft: **rc-endpoint smoke suite gates every rclone version bump**.
- **MEDIUM — server connection caps** (seedbox hosts especially) → SiteConnectionManager designed around them; integration tests against sshd containers with hostile caps; browse connection always reserved so browsing survives pool collapse.

### Verification
- **Phase 0 exit**: `task build:windows` on the Spark cross-compiles clean; the user pulls, runs `wails build` on the Windows box, and warpseed.exe launches with the dual-pane shell; `wails dev` hot-reloads on both machines.
- **Engine correctness**: `go test ./...` incl. property-based resume/offset tests; testcontainers OpenSSH matrix (MaxSessions 2/10, MaxStartups 1:100:2, chunk-size limits) — pool must shrink+requeue, never error.
- **Speed benchmark (the point of the project)**: scripted comparison vs WinSCP/CuteFTP run by the user on the Windows box against (a) a LAN sshd, (b) the real seedbox at high RTT — assert warpseed saturates the link where the others don't; record in `docs/benchmarks.md`. GUI speed budget items measured on the same box.
- **Browse-while-transfer**: E2E test — start 4 large transfers, assert directory listing round-trip stays <500ms and UI stays interactive (user-verified on Windows; Playwright automation added at release time).
- **Pause/resume**: kill -9 mid-transfer → restart → resume from recorded offset with hash-verified result; repeat for chunked mode per-chunk.

### Execution order
1. Part 1 workspace (one session) → 2. Phase 0 scaffold → 3. Phase 1 MVP (TDD per workflow rules) → 4. Phase 2 → 5. Phase 3. Each phase ends with a code review + tagged release.
