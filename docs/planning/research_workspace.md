## RECOMMENDATION
Adopt the flat per-project-repo layout under ~/DevProjects with underscore-prefixed meta directories (_shared, _templates, _scratch, _archive), a <50-line root CLAUDE.md (workspace map + naming + "one repo per project" rule — it verifiably loads in every child-repo session via parent-directory walking), a root .editorconfig, and optionally .mise.toml to pin toolchains. Do NOT rely on a root .claude/settings.json or root .claude/skills for child repos — docs confirm settings resolve at each git repo's root and skill discovery stops at the repo root; instead keep shared defaults in ~/.claude/settings.json (user scope, solo dev) and share rules/skills by symlinking from _shared/claude/ into each repo's .claude/rules/ and .claude/skills/ (symlinks are explicitly supported for both). For the first project: create DevProjects/filebeam as its own repo from a _templates/go-wails-app template; use Wails v2 (or v3 alpha if comfortable) because the Windows target needs no CGO — dev-loop and windows/amd64 release builds plus NSIS installer all run natively on this Linux box (install: Go via mise, wails CLI, nsis, golangci-lint, gh). Reserve Windows work for a GitHub Actions windows-latest job doing Azure Trusted/Artifact Signing (azure/trusted-signing-action, Windows-runner-only), MSI/MSIX packaging, and WebView2 smoke tests; add a local Win11 eval VM only if interactive debugging demands it.

## FINDINGS
VERIFIED CLAUDE CODE BEHAVIOR (current docs at code.claude.com, Aug 2026) — this drives the whole layout:

1. CLAUDE.md walks UP the directory tree from the cwd: launching Claude in DevProjects/my-repo loads ~/DevProjects/CLAUDE.md IN FULL, before the repo's own CLAUDE.md ("Claude Code reads CLAUDE.md files by walking up the directory tree from your current working directory"). So a root CLAUDE.md is the correct place for workspace-wide conventions — but it is injected into EVERY session in every repo, so keep it under ~50 lines. `claudeMdExcludes` (glob, any settings layer) can suppress it per-repo if ever needed.

2. .claude/settings.json does NOT propagate from parent directories. Docs: project settings are read/written "at the root of the git repository, resolved through worktrees". A DevProjects/.claude/settings.json applies ONLY to sessions started in DevProjects itself (which is not a git repo). Each project repo needs its own .claude/settings.json; put shared solo-dev defaults in user scope ~/.claude/settings.json instead. Precedence: managed > CLI args > local > project > user; permission rules MERGE across scopes.

3. Skills: "Project skills load from .claude/skills/ in the directory where you start Claude Code and in every parent directory up to the repository root." Discovery STOPS at the repo root — a DevProjects/.claude/skills/ is invisible inside child repos. Three sanctioned sharing mechanisms: (a) symlinks — a skill dir entry "can be a symlink to a directory elsewhere on disk", deduped if reachable twice; (b) `--add-dir` loads .claude/skills/ from the added dir (explicitly an exception; `permissions.additionalDirectories` in settings.json does NOT load skills); (c) user-level ~/.claude/skills. Nested .claude/skills/ below the start dir lazy-load when Claude touches files there (directory-qualified names like `apps/web:deploy`).

4. .claude/rules/ explicitly supports symlinks ("maintain a shared set of rules and link them into multiple projects") plus path-scoped rules via `paths:` frontmatter. User-level rules (~/.claude/rules/, where the ECC rules already live) load before project rules. So the pattern for this workspace: keep canonical shared rules/skills in DevProjects/_shared/claude/ and symlink into each repo's .claude/.

PROPOSED TREE for ~/DevProjects:
```
DevProjects/
├── CLAUDE.md            # <50 lines: workspace map, naming rules, where things live
├── README.md            # human index of projects + status
├── .editorconfig        # baseline (tabs for Go, 2-space yaml/json, lf, utf-8)
├── .mise.toml           # optional: pin go/node via mise for everything below
├── .claude/
│   ├── settings.json    # only for sessions started AT the root (housekeeping)
│   └── skills/          # ditto — root-session-only helpers (e.g. new-project scaffolder)
├── _shared/             # canonical shared assets (symlink TARGETS)
│   ├── claude/rules/    #   ln -s into <repo>/.claude/rules/shared
│   ├── claude/skills/   #   ln -s individual skills into <repo>/.claude/skills/
│   ├── configs/         #   .golangci.yml, .editorconfig, gitignore templates
│   └── scripts/         #   new-project.sh, cross-build helpers
├── _templates/
│   ├── go-cli/          # cmd/, internal/, Taskfile, .golangci.yml, CLAUDE.md stub
│   ├── go-wails-app/    # desktop template incl. GH Actions windows sign/package job
│   └── pbi-project/     # PBIP/TMDL layout + pbi-cli conventions
├── _scratch/            # throwaway spikes, never git repos of record
├── _archive/            # retired projects moved here verbatim (keep .git)
├── filebeam/            # first project — its own git repo (Go desktop file transfer)
└── pbi-<client>-<model>/ # Power BI repos
```
Naming: lowercase kebab-case repo names; `_` prefix reserves non-project meta dirs (sorts first, visually distinct, trivially excluded from "list my projects" globs). One git repo per project (no root-level git). Root CLAUDE.md content: the tree above, naming conventions, "each project is its own git repo — never run git from DevProjects root", pointer to _templates for scaffolding, note that _scratch is disposable. Do NOT duplicate the global ~/.claude/CLAUDE.md (pbi-cli routing) or ECC rules — those already load user-scope.

TOOLCHAIN FOR PROJECT #1 (Go desktop → windows/amd64, built from Linux):
- Pure-Go core: `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build` needs nothing beyond the Go toolchain; a file-transfer engine/CLI cross-compiles trivially, including -H=windowsgui.
- Wails (Go GUI, recommended): Windows is the EASIEST cross target because the Windows backend (WebView2 via syscalls) needs no CGO — v3 docs: builds for Windows "from any host OS with no additional setup"; Docker (wails-cross image) only if you add CGO deps. NSIS installers build on Linux (`apt install nsis`). Status Aug 2026: v3 still alpha but core APIs declared production-stable and used in production; v2 still actively maintained (v2.13.0 July 2026). v2 cross-compiles Linux→Windows too (community-verified, discussion #2632 / metal3d/wails-cross-compile), though not officially blessed.
- Fyne alternative: requires CGO for Windows → mingw-w64 or fyne-cross (Docker); more friction.
- Tauri alternative: `rustup target add x86_64-pc-windows-msvc` + `cargo install cargo-xwin` + `apt install nsis lld llvm clang`; NSIS bundle builds on Linux but Tauri docs label it "highly experimental"; MSI (WiX) is Windows-only.
- Avalonia alternative: `dotnet publish -r win-x64 --self-contained` works perfectly from Linux; MSIX packaging is Windows-only.
- NEEDS WINDOWS REGARDLESS (use GitHub Actions windows-latest, free for public / cheap for private, instead of a local VM): (1) Code signing — Azure Trusted Signing (renamed "Artifact Signing" Jan 2026, ~$10/mo, no hardware token) via azure/trusted-signing-action, which runs ONLY on Windows runners; osslsigncode on Linux only works with file-based certs, which post-2023 CA rules (HSM-mandatory) make impractical. (2) MSI/MSIX packaging. (3) Real WebView2/UI smoke tests + SmartScreen behavior. Optional local Win11 eval VM (quickemu/VirtualBox) for interactive debugging.

INSTALL/CONFIGURE LIST (Linux host):
- mise (or asdf) at DevProjects root to pin Go (current: 1.24/1.25 line) per .mise.toml; or distro Go.
- git + gh CLI (ECC workflow expects gh search/PRs).
- Wails CLI: `go install github.com/wailsapp/wails/v3/cmd/wails3@latest` (or v2 wails@latest) + `wails doctor`.
- nsis (apt) for Windows installer from Linux; Docker only if CGO enters the picture.
- golangci-lint + a Taskfile/Makefile with a `build:windows` target (GOOS=windows GOARCH=amd64).
- Azure account + Trusted/Artifact Signing resource; GH Actions workflow: linux job (test + cross-build) → windows job (sign, NSIS/MSIX, smoke test).
- Create _shared/, _templates/, _scratch/, _archive/, root CLAUDE.md, README.md, .editorconfig; symlink _shared/claude/rules into new repos' .claude/rules/; per-repo .claude/settings.json from template (root-level one won't apply inside repos — verified).
- Optionally register the root as a Claude Code "housekeeping" session target with a scaffold skill in DevProjects/.claude/skills/new-project.

## OPTIONS
- Per-project git repos under DevProjects root (flat, with _shared/_templates/_scratch/_archive meta dirs) [?, standard practice]: PROS: Matches Claude Code's model: settings.json and skills resolve per git-repo root, so each repo is a clean project boundary; Root CLAUDE.md still loads in every repo (parent-walk verified in docs) giving cheap workspace-wide conventions; Independent histories, licensing, archiving; archive = move dir to _archive/; Solo-dev friendly: no monorepo tooling (turborepo/bazel) overhead CONS: Shared rules/skills need symlinks or user-scope placement (no automatic inheritance of .claude/settings.json/skills from the root); Cross-project refactors span repos
- Single monorepo for all projects [?, mature but wrong fit here]: PROS: One settings.json/skills tree covers everything; Nested .claude/skills/ per package with directory-qualified names works well CONS: Unrelated domains (Go desktop app + Power BI models) share history and CI triggers; Ancestor-CLAUDE.md noise; needs claudeMdExcludes hygiene; Harder to open-source or archive one project
- Go + Wails (v2 stable / v3 alpha) for the desktop app [MIT, v2 stable & maintained (v2.13, Jul 2026); v3 alpha with production-stable core]: PROS: Windows is the easiest cross target: no CGO needed, builds windows/amd64 straight from Linux; v3: NSIS installer generation works on Linux; Docker image handles rare CGO cases; Single Go codebase for transfer engine + GUI; fits ECC go-* skills already installed CONS: v3 still alpha (docs/tooling churn); v2 cross-compile community-supported rather than official; Ships a WebView2-dependent UI (bundled runtime download on old Windows)
- Go + Fyne [BSD-3, stable]: PROS: Pure-Go API, native rendering, no webview CONS: Windows cross-build requires CGO → mingw-w64 or fyne-cross Docker; Less native look; heavier friction than Wails for this target
- Tauri (Rust) cross-built with cargo-xwin [MIT/Apache-2.0, Tauri 2.x stable; Linux→Windows path officially 'highly experimental']: PROS: Smallest binaries; NSIS bundling works on Linux (cargo-xwin + nsis + lld/llvm/clang) CONS: Abandons Go (user's stated stack); MSI requires Windows/WiX; Experimental cross path can break per-project
- Avalonia (.NET) [MIT, stable (11.x)]: PROS: dotnet publish -r win-x64 --self-contained works flawlessly from Linux; strong desktop widget set CONS: Different language stack from Go; MSIX packaging Windows-only; larger runtime footprint
- Windows needs: GitHub Actions windows-latest + Azure Trusted/Artifact Signing (vs local Windows VM) [?, current best practice 2025-2026]: PROS: trusted-signing-action handles Authenticode without hardware tokens (~$10/mo); runs sign+MSIX+smoke tests in CI; No local VM maintenance; reproducible releases CONS: Signing action is Windows-runner-only; interactive UI debugging still benefits from an optional local Win11 eval VM
