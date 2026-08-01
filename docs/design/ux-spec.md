# warpseed UX Specification

**Version 1.0 — 2026-08-01**
Scope: complete UX/visual design for the warpseed dual-pane transfer client (Windows x64, Go + Wails v2 + React/TS). Companion to `docs/planning/approved-plan.md` (GUI speed budget: 60fps on 100k rows, <16ms frames under 8 active transfers) and `docs/planning/design_1.md` (event contract, component list). Everything here is designed to be renderable within that budget: all motion is `transform`/`opacity` only, all lists virtualized, all state colors are tokens.

---

## 1. Design principles

1. **Speed is the aesthetic.** warpseed exists because CuteFTP and WinSCP got slow. Nothing in the UI may cost a frame: no layout-bound animation, no blocking spinner over a pane, no modal that isn't strictly necessary. The app *feels* fast because it *is* fast — and the design shows its speed (live rates, sparkline, sub-120ms transitions) instead of decorating it.
2. **Keyboard-first, mouse-complete.** Every operation is reachable without the mouse, honoring 30 years of commander muscle memory (F5, Tab, Insert). Every keyboard operation also has a discoverable pointer path (context menu, toolbar, palette). Shortcuts are shown inline everywhere: menus, palette rows, tooltips.
3. **The queue is the product.** Seedbox workloads mean hours-long transfers. The queue is never hidden behind a window (CuteFTP) or a bottom tab you forget (WinSCP): it is a persistent, glanceable dock with aggregate speed always visible even when collapsed.
4. **Browse is sacred.** The dedicated browse connection is the engine's structural promise; the UI mirrors it. Panes never grey out, never block, never show a transfer's progress *inside* the listing area. Listings stream in (`listing:chunk`) and render incrementally.
5. **Honest state, always.** Byte-level resume (sftpfast) vs file-level resume (rclone backends) is labeled, not hidden. Errors say what failed and what the app will do next (retry countdown visible). No fake progress, no indeterminate bars where the engine has real numbers.
6. **Calm surface, loud alarms.** The instrument-panel dark theme is deliberately quiet — one accent, low-chroma surfaces — so that the two genuinely dangerous moments (HOST KEY CHANGED, destructive overwrite) can be unmissably loud by contrast.
7. **Dense by default, never cramped.** The audience is power users moving thousands of entries. Compact 26px rows, tabular monospace data columns, and a 4px spacing grid — but with real hierarchy (type scale, surface layering), not the flat grey soup of 2005-era clients.

---

## 2. Information architecture

### 2.1 Window regions (top to bottom)

```
┌──────────────────────────────────────────────────────────────────┐
│ ① Tab strip  [seedbox.example ×] [local↔local ×] [+]     ⚙  ▣    │ 36px
├──────────────────────────────────────────────────────────────────┤
│ ② Pane A toolbar/path          │ ② Pane B toolbar/path           │ 34px
│ ③ Pane A listing               │ ③ Pane B listing                │ flex
│ ④ Pane A footer (sel/filter)   │ ④ Pane B footer                 │ 26px
├──────────────────────────────────────────────────────────────────┤
│ ⑤ Queue dock (collapsible)   ▲ 42.7 MB/s · 3 active · 12 queued  │ 36–320px
├──────────────────────────────────────────────────────────────────┤
│ ⑥ Status bar  site ● connected · 4 conns · sparkline · db v3     │ 26px
└──────────────────────────────────────────────────────────────────┘
```

**Always visible:** tab strip, both panes, queue dock header row (even collapsed), status bar.
**Summoned:** command palette (Ctrl+K), quick-open fuzzy jump (Ctrl+P), site manager (Ctrl+Shift+S), context menus, prompts/modals, toasts, settings.
**Never:** floating tool windows, detachable queue (CuteFTP's separate queue window is explicitly rejected — it's the thing people lost behind other windows).

### 2.2 Region details

- **① Tab strip** — session tabs, WinSCP-style but visual like Warp/Arc: each tab = one site session (or a local↔local workspace). Tab shows a 3px protocol-colored underline dot, site name, connection state dot (pulsing while connecting), close ×. `+` opens quick-connect/site list popover. Right side: settings gear, layout toggle (horizontal/vertical pane split).
- **②–④ Dual panes** — resizable via a 6px drag handle (react-resizable-panels), double-click handle resets 50/50. Each pane is independently local or remote; a session tab remembers both pane paths. Active pane carries the accent treatment (see §8.2).
- **⑤ Queue dock** — full spec in §4. Collapsed = 36px aggregate strip; expanded = user-dragged height, default 240px, min 120px, max 50% of window.
- **⑥ Status bar** — left: active site connection status + pool size ("● seedbox · 4/4 conns"); center: 60-second aggregate speed sparkline (§8.3); right: transient inline status (last op), db/schema health, bandwidth-limit indicator when set.

---

## 3. Navigation model

### 3.1 Full keyboard map

Rule of reconciliation: **WinSCP/Norton-commander keys are never repurposed** — F-keys, Tab, Insert, Space, numpad +/−/* do exactly what a 20-year WinSCP user's hands expect. Modern idioms are added on chords WinSCP left free or barely used (Ctrl+K palette wins over WinSCP's redundant copy alias; Ctrl+P palette-adjacent quick-open wins over "open PuTTY", which moves into the palette).

| Key | Action | Lineage |
|---|---|---|
| `Tab` | Switch active pane | NC/WinSCP |
| `Enter` | Open dir / default action on file (add to queue + start) | NC/WinSCP |
| `Backspace` / `Alt+↑` / `Ctrl+PgUp` | Parent directory | WinSCP + NC |
| `Ctrl+PgDn` | Enter directory under cursor (explicit, works when Enter is remapped) | NC/Total Cmd |
| `Alt+←` / `Alt+→` | Per-pane history back / forward | WinSCP + browser idiom |
| `Ctrl+\` | Go to root | WinSCP |
| `Ctrl+H` | Go to home (remote: SFTP home; local: %USERPROFILE%) | WinSCP |
| `Ctrl+L` | Edit path inline (breadcrumb → text input, select-all) | modern (browsers, VS Code) |
| `Ctrl+K` | Command palette | modern (Raycast/Linear) |
| `Ctrl+P` | Quick-open: fuzzy jump to visited dirs, bookmarks, sites | modern (VS Code) |
| `Ctrl+D` | Bookmark current directory (location profile) | browser idiom; replaces WinSCP "open location profile" |
| `Ctrl+F` | Filter-as-you-type in active pane (see §3.5) | WinSCP incremental search, modernized |
| `Esc` | Clear filter → close popover → collapse palette (in that order) | universal |
| `F2` | Rename inline | WinSCP |
| `F3` | Quick view / preview panel toggle for cursor file | NC lineage |
| `F4` | Edit (download-edit-upload watch loop; Phase 2) | WinSCP |
| `F5` | **Transfer** selected/marked → other pane (copy semantics) | NC/WinSCP/CuteFTP |
| `F6` | Transfer + delete source (move) | NC/WinSCP |
| `Shift+F5` / `Shift+F6` | Same, but open transfer-options sheet first (rename target, queue-only, priority) | WinSCP dialog, opt-in |
| `F7` | New folder | NC/WinSCP |
| `F8` / `Delete` | Delete (with confirm; Shift+Delete = no confirm, permanent) | WinSCP |
| `F9` / `Alt+Enter` | Properties/permissions (chmod on SFTP) | WinSCP |
| `Insert` | Mark/unmark entry, advance cursor | NC/WinSCP |
| `Space` | Mark/unmark entry (no advance); on dirs also computes size | NC/WinSCP |
| `Num +` / `Num −` | Mark / unmark by glob pattern (dialog with live preview) | NC/WinSCP |
| `Num *` | Invert marks | NC/WinSCP |
| `Ctrl+A` | Select all | universal |
| `Ctrl+Shift+A` | Deselect all | replaces WinSCP Shift+Ctrl+L |
| `Shift+↑/↓/PgUp/PgDn/Home/End` | Range select from anchor | universal |
| `Ctrl+C` / `Ctrl+X` / `Ctrl+V` | Copy/cut/paste across panes (enqueues transfers; cut shows §7.1 cut state) | Explorer idiom |
| `Ctrl+T` | New session tab | WinSCP |
| `Ctrl+W` | Close tab | modern (WinSCP compat alias Ctrl+Shift+D kept) |
| `Ctrl+Tab` / `Ctrl+Shift+Tab` | Next / previous tab | universal |
| `Alt+1…9` | Jump to tab N | WinSCP |
| `Ctrl+Alt+B` | Toggle synchronized browsing | WinSCP |
| `Ctrl+S` | Open sync/compare view (Phase 3) | WinSCP "synchronize" |
| `Ctrl+F3…F6` | Sort by name / ext / mtime / size (repeat = reverse) | WinSCP |
| `Ctrl+Alt+H` | Toggle hidden/dot files | WinSCP |
| `Ctrl+Shift+S` | Site manager | CuteFTP F4 lineage, modern chord |
| `Ctrl+Q` | Focus queue dock (then ↑↓ select row, Space pause/resume, Del cancel, Ctrl+↑/↓ reorder) | new |
| `Ctrl+Shift+Q` | Expand/collapse queue dock | new |
| `Ctrl+,` | Settings | modern |
| `F1` | Keyboard map overlay (searchable cheat sheet) | universal |

Focus rules: exactly one pane owns focus; queue dock and palette are focus scopes entered explicitly (Ctrl+Q / Ctrl+K) and exited with Esc back to the last active pane. Focus is never stolen by background events — a completed transfer toasts, it does not focus-jump.

### 3.2 Path bar: breadcrumb with inline edit

- Default state: **breadcrumb segments** (`◈ C: ▸ seeds ▸ complete`), each segment a button (click = navigate; each segment's `▸` opens a sibling-directory dropdown, lazy-listed).
- `Ctrl+L` or click on empty trailing area: morphs to a text input pre-filled with the full path, all selected. `Enter` navigates, `Esc` reverts. The morph is a crossfade, 120ms, opacity only — same box, no layout shift.
- Remote panes: input has **path autocomplete** — after 300ms debounce, the browse connection lists the deepest valid prefix and offers completions in a dropdown (↑↓ + Tab to accept). This is the modernization of WinSCP's remote path completion.
- Overflow: middle segments collapse into a `…` menu; first (root/drive) and last two segments always visible.
- The pane toolbar row also holds: drive/root selector (local) or site badge (remote), up button, history back/forward buttons (with long-press history list), filter indicator, sync-browse link icon (lit when active).

### 3.3 Per-pane history

Each pane keeps a 50-entry navigation stack (persisted per session tab). `Alt+←/→` traverse; long-press or right-click the toolbar arrows shows the stack as a menu. Quick-open (Ctrl+P) merges: pane history ∪ bookmarks ∪ saved sites' default dirs, fuzzy-matched, most-recent-first.

### 3.4 Synchronized browsing

Toggle: `Ctrl+Alt+B` or the link icon between the two path bars. When on: navigating either pane applies the same relative move to the other (enter `foo/` → other pane enters `foo/`; up → up). If the mirrored directory doesn't exist, the *other* pane shows a non-blocking inline banner: "`foo` doesn't exist here — [Create] [Unlink]" (never a modal). Active state: the link icon fills accent and a hairline of `--accent-dim` runs along the top edge of *both* panes (2px, opacity fade-in 120ms).

### 3.5 Filter-as-you-type

`Ctrl+F` (or just typing a printable char when the listing is focused — configurable, default on) opens a 28px filter strip pinned under the pane toolbar. Substring match, case-insensitive; `*`/`?` globbing auto-detected. Listing filters live per keystroke (virtualized list re-derives; no debounce needed at these row counts). Footer shows "42 / 15,203 match". `Enter` moves focus to the filtered list; `Esc` clears + closes. Filter persists across a refresh but clears on navigate. This replaces both WinSCP's incremental search *and* its separate Ctrl+Alt+F filter dialog with one strip.

### 3.6 Selection model

Two coexisting mechanisms, exactly as commander veterans expect:
- **Cursor** — one row per pane, moved by arrows, shown even when the pane is unfocused (dimmed).
- **Marks** — sticky multi-selection via Insert/Space/Num-pad globs, surviving cursor movement and sort changes. Marked rows tint accent at 12% opacity + accent-colored size text. Pane footer aggregates: "7 marked · 3.2 GiB".
- Mouse: click = cursor+select; Ctrl+click = toggle mark; Shift+click = range from anchor; drag on empty = rubber-band marking.
- F5/F6/F8/Ctrl+C act on marks if any exist, else on the cursor row.

---

## 4. Transfer queue UX

### 4.1 Dock anatomy

**Collapsed (36px strip, default at launch):**
`▲ [∑ progress micro-bar 80px] 42.7 MB/s · 3 active · 12 queued · 1 failed ⚠ · ETA 8m 12s`
The micro-bar is the aggregate byte progress of all incomplete items. A failed count > 0 renders in `--state-error` and pulses once (opacity 1→0.6→1, 240ms, twice) when it increments. Click anywhere or `Ctrl+Shift+Q` expands.

**Expanded header (32px):** left — aggregate speed (mono, 14px), total ETA, counts; right — `⏸ Pause all` `▶ Resume all` `✕ Clear done` buttons + collapse chevron. Header is sticky above the virtualized row list.

### 4.2 Queue row (compact 32px, virtualized)

```
[state icon] name.mkv              seedbox → C:\seeds   ▓▓▓▓▓▓░░░░ 62%   38.1 MB/s   ETA 1:42   ⏸ ✕
```
Columns: state icon (16px) · filename (truncate middle, full path in tooltip) · route "site → dest" (12px `--text-dim`) · progress bar (flex, min 120px) · percent (mono) · speed (mono) · ETA (mono) · row actions (pause/resume, cancel; retry replaces pause on failed rows). Actions appear at 40% opacity, 100% on row hover/focus — always present, never layout-shifting in.

**Chunked-transfer signature:** for multi-connection sftpfast transfers, the progress bar is segmented — one sub-track per chunk/connection (2–4), each filling independently. This "hyperlane bar" is warpseed's engine made visible (§8.1). Single-connection and rclone transfers show one continuous track.

Progress bars animate via rAF-lerped `transform: scaleX()` on a full-width inner div (never width animation), fed by the ≤10 Hz coalesced `transfer:progress` events with EMA smoothing.

### 4.3 State semantics

| State (queue table) | Icon | Color token | Bar treatment |
|---|---|---|---|
| pending / dispatched | ◷ | `--text-dim` | empty track |
| active | ▶ | `--state-active` (accent teal) | filling, lerped |
| paused | ⏸ | `--state-paused` (amber) | frozen at offset, 50% saturation |
| completed | ✓ | `--state-done` (green) | full, fades to 40% opacity after 2s |
| failed | ⚠ | `--state-error` (red) | frozen + red hairline underline; row shows error line |
| cancelled | ✕ | `--text-faint` | struck-through name, auto-clears with done |
| retry-wait | ↻ | amber | live countdown in ETA column: "retry in 0:14 · attempt 2/5" |

Resume fidelity badge on every remote row: `⇄ byte` (sftpfast) or `⇄ file` (rclone) — 10px mono chip, tooltip explains. This is the "honest labels" principle in the queue itself.

### 4.4 Behaviors

- **Reorder:** drag rows (6-dot grab handle appears on hover at row left; drag ghost = translated row at 90% opacity) or `Ctrl+↑/↓` with queue focused. Only pending items reorder; actives pin to top. Drop commits `Reorder` binding; optimistic UI with rollback on error.
- **Errors:** failed row expands one extra 20px line: red 12px message ("connection reset by peer — will retry in 0:14 · attempt 2/5") + `[Retry now] [Skip] [Details]`. Aggregate header gains `1 failed ⚠` chip; clicking it filters the list to failures. Exhausted retries fire a toast (§7.8) — the row stays until acted on.
- **Completed items:** stay for the session (dimmed, newest first below actives/pending), so overnight runs are auditable in the morning — the CuteFTP promise. `Clear done` purges; auto-purge threshold (default 500) keeps the dock bounded. Completed rows offer "Open folder" on hover.
- **Queue is not site-dependent** (CuteFTP's killer feature): items from multiple sites coexist; disconnecting a site pauses its items with state chip "site offline", auto-resuming on reconnect.
- **Add-without-start:** `Shift+F5` sheet offers "Queue only"; palette exposes "Queue: start all". A global `⏻ Auto-start` toggle in the dock header supports tag-now-transfer-later workflows.

---

## 5. Site manager & connection UX

### 5.1 Quick connect vs saved sites

- **Quick connect:** the tab-strip `+` opens a popover (not a window): one smart input accepting `sftp://user@host:port/path`, plus protocol segment control [SFTP · FTP · FTPS · S3 · WebDAV] (non-SFTP disabled until their phase, greyed with "Phase 2" chips — honest roadmap). `Enter` connects; on success offers a one-click "Save as site".
- **Site manager (`Ctrl+Shift+S`):** full-height modal panel, left = searchable site list, right = detail form. Sites are 56px list rows, not cards: protocol badge (SFTP teal / FTP amber / FTPS amber-outline / S3 orange / WebDAV violet — 3px left border + 10px mono chip), site name (14px), `user@host` (12px mono dim), last-connected relative time, resume-fidelity chip. Folders/groups supported via drag; fuzzy search on top. Every site row: `Connect` primary button + overflow menu (Connect in new tab, Edit, Duplicate, Delete).

### 5.2 Site form

Sections (single scroll, no wizard — CuteFTP's connection wizard is the dated part; keep the *coverage*, drop the ceremony): Connection (protocol, host, port, initial remote dir, initial local dir) · Authentication · Tuning (per-site connection cap, default 4, slider 1–8 with "seedbox hosts commonly cap ~8/IP" helper text; chunked-transfer toggle; cipher pref override) · Behavior (default overwrite policy, auto-start, sync-browse default).

**Credentials, agent-first messaging:** the auth section leads with a live agent status line — "🔑 OpenSSH agent: 2 keys available" or "Pageant detected" — and the default method is "SSH agent (recommended)". Password/key-file fields sit below with "stored in Windows Credential Manager, never on disk" microcopy. Password inputs show a wincred lock glyph inside the field. No "save password?" checkbox ambiguity: saving the site saves the credential reference, full stop.

### 5.3 Host-key TOFU dialog

First connection to unknown host (`prompt:hostkey`), a **calm, informative** modal:
- Title: "First connection to seedbox.example"
- Body: key type + SHA256 fingerprint in 13px mono, wrapped in a copyable code block; ASCII randomart in a 8px-line-height mono block beside it; helper text "Verify this fingerprint against one your provider published."
- Actions: `[Trust & connect]` (primary, accent) · `[Connect once]` (ghost) · `[Cancel]`. Keyboard: Enter = Trust, Esc = Cancel. Timeout (engine-side) = deny; the dialog shows a quiet 60s countdown ring on Cancel.

### 5.4 KEY-CHANGED alarm — deliberately loud

This is the one place the calm theme breaks, by design:
- Full-window scrim at 80% `--surface-0`; dialog with 2px `--state-error` border and a 4px red top band; title "⚠ HOST KEY HAS CHANGED" in 20px/700.
- Old pinned vs new fingerprint side by side, differing characters highlighted.
- Copy: "This can mean the server was reinstalled — or that the connection is being intercepted."
- Actions inverted from every other dialog: primary = `[Disconnect]` (safe), destructive-styled `[Trust new key]` is **disabled for 3 seconds** and requires typing the host name to enable. No Enter-key accept — Enter maps to Disconnect.
- Entry animation: 240ms scale 0.98→1 + a single 2px red edge pulse on the dialog border. No sound by default (setting available).

---

## 6. Visual design system (tokens v2)

Refines `frontend/src/tokens.css`; hue family 260 (cool slate) retained, warp-teal accent retained. Dark-only through Phase 2 (instrument panel is the identity; a light theme is explicitly out of scope until Phase 3).

### 6.1 Palette

```css
/* surfaces — 5 layers, hue 260, chroma rises slightly with elevation */
--surface-0: oklch(16% 0.012 260);  /* app background, queue dock well */
--surface-1: oklch(20% 0.014 260);  /* pane listing background */
--surface-2: oklch(24% 0.016 260);  /* toolbars, path bars, queue rows */
--surface-3: oklch(29% 0.018 260);  /* hover, popovers, palette */
--surface-4: oklch(34% 0.02  260);  /* active/pressed, dialog surfaces */

/* text — 4 tiers (contrast on surface-1: ≈13:1 / 7:1 / 4.6:1 / decorative) */
--text:        oklch(90% 0.008 250);
--text-mid:    oklch(74% 0.012 250);
--text-dim:    oklch(62% 0.014 250);   /* ≥4.5:1 — smallest body-text tier */
--text-faint:  oklch(46% 0.012 250);   /* decorative/disabled only, never sole info carrier */

/* accent */
--accent:      oklch(78% 0.14 190);    /* warp teal — focus, active pane, links */
--accent-dim:  oklch(60% 0.10 190);
--accent-glow: oklch(78% 0.14 190 / 0.18);  /* auras, marked-row tint */

/* semantic transfer states (reserved — never used decoratively) */
--state-active: var(--accent);
--state-done:   oklch(72% 0.15 150);   /* green */
--state-paused: oklch(78% 0.13 85);    /* amber */
--state-error:  oklch(66% 0.19 25);    /* red */
--state-queued: var(--text-dim);

/* protocol badges */
--proto-sftp: var(--accent);  --proto-ftp: oklch(75% 0.12 85);
--proto-s3:   oklch(72% 0.14 55);      --proto-webdav: oklch(70% 0.12 300);

--border:        oklch(29% 0.014 260);
--border-strong: oklch(38% 0.018 260);
```

### 6.2 Elevation

Depth comes from **surface step + 1px border + one soft shadow**, never heavy blur stacks:
- Level 0 (panes, dock): surface step only, 1px `--border` separators.
- Level 1 (popovers, context menus, palette): `--surface-3`, 1px `--border-strong`, `box-shadow: 0 8px 24px oklch(0% 0 0 / 0.4)`.
- Level 2 (modals): `--surface-4`, same border, `0 16px 48px oklch(0% 0 0 / 0.5)`, scrim `oklch(10% 0.01 260 / 0.6)`.

### 6.3 Typography

```css
--font-ui:   "Inter", "Segoe UI Variable Text", "Segoe UI", system-ui, sans-serif;
--font-data: "Cascadia Mono", "Consolas", ui-monospace, monospace;
```
Change from tokens v1: **Nunito is dropped** — its rounded terminals read friendly-consumer, not instrument-panel. Inter (bundled, subset woff2, `font-display: swap`) with Segoe UI Variable native fallback. All *data* (sizes, dates, speeds, ETAs, paths, fingerprints, percentages) sets in `--font-data` with `font-variant-numeric: tabular-nums` so columns never jitter as numbers tick.

Scale (rem, 16px root): `--text-2xs: 0.6875rem` (11px — chips, badges) · `--text-xs: 0.75rem` (12px — secondary columns, footers) · `--text-sm: 0.8125rem` (13px — **listing rows, default UI**) · `--text-md: 0.875rem` (14px — inputs, buttons, queue speed) · `--text-lg: 1rem` (16px — dialog titles) · `--text-xl: 1.25rem` (20px — alarm titles only). Weights: 400/500/600; 700 reserved for the KEY-CHANGED alarm.

### 6.4 Spacing, radius, sizing

- 4px grid: `--sp-1..--sp-8` = 4, 8, 12, 16, 20, 24, 32, 40px. Component padding uses 8/12; section gaps 16/24.
- Radius: `--radius-xs: 3px` (chips, progress tracks) · `--radius-sm: 6px` (buttons, inputs, rows) · `--radius-md: 10px` (popovers, palette) · `--radius-lg: 14px` (modals). Panes and dock are square-edged into the window frame.
- Density: **compact default** `--row-h: 26px` (listing) / 32px (queue); "relaxed" mode 32px/40px via a single `data-density` attribute — settings toggle, Phase 2.

### 6.5 Motion

```css
--dur-instant: 80ms;   /* hover tints, focus rings */
--dur-fast:    120ms;  /* popover/palette in, breadcrumb morph, pane-switch aura */
--dur-normal:  180ms;  /* dock expand/collapse, dialog in */
--dur-slow:    240ms;  /* alarm entry, error pulse */
--ease-out:  cubic-bezier(0.16, 1, 0.3, 1);   /* everything entering */
--ease-in:   cubic-bezier(0.7, 0, 0.84, 0);   /* everything exiting, 80ms max */
```
Rules: only `transform`, `opacity` (and `clip-path` for the warp-line, §8.1). Dock expand animates `transform: translateY` on the dock against a pre-reserved layout — never animates height. Lists never animate rows in/out (virtualization + motion = jank); new listings crossfade the whole scroll container 80ms. `prefers-reduced-motion`: all durations → 1ms, warp-line and pulses disabled, progress bars still move (data, not decoration).

### 6.6 Iconography

**Lucide** (MIT, tree-shakeable), 16px default on a 16/20/24 grid, `stroke-width: 1.75`. File-type glyphs: folder, file, archive, video, audio, image, disc — mapped by extension, single-color `--text-dim` (color is reserved for state). Protocol/state chips use text, not bespoke icons. No emoji in the product UI.

### 6.7 Focus & accessibility

- `:focus-visible`: 2px `--accent` outline, `outline-offset: -2px` on rows (inset, avoids clipping in virtualized overflow), `2px` offset on buttons/inputs. Never removed, never color-only.
- Contrast: all text tiers except `--text-faint` ≥ 4.5:1 on their surfaces; state colors ≥ 3:1 against `--surface-2` and always paired with an icon/label (paused ⏸, failed ⚠) — color is never the only channel.
- Full app operable by keyboard (§3.1); roving tabindex within listings; `aria-live="polite"` on queue aggregate, `assertive` only for KEY-CHANGED; listings are `role="grid"` with virtual row indices announced ("row 4,312 of 100,000").

---

## 7. Component inventory & states

| # | Component | States (all designed, no browser defaults) |
|---|---|---|
| 7.1 | **Pane row** (26px grid: icon 16 · name flex · size 88 right-mono · mtime 118 mono) | default → hover (`--surface-3` tint, 80ms) → **cursor** (1px inset `--accent-dim` border + `--surface-3`; unfocused pane: border only at 40%) → **selected/marked** (`--accent-glow` fill, accent size text, persists under hover) → **cut** (Ctrl+X: 45% opacity + dashed 1px border) → drop-target dir (accent dashed inset while dragging over) → renaming (inline input, F2) |
| 7.2 | **Path breadcrumb** | segments idle/hover(underline-grow via scaleX)/pressed · overflow `…` · edit-mode input (Ctrl+L morph) · autocomplete dropdown (Level 1 surface, ↑↓+Tab) · invalid path (input shakes ±2px translateX 3×80ms, red 1px border, inline reason) |
| 7.3 | **Tab strip tab** | idle · hover (close × fades in) · active (surface-2 fill + 2px accent bottom bar) · connecting (state dot pulses opacity 0.4↔1, 1s loop) · disconnected (amber dot) · error (red dot) · drag-reorder ghost |
| 7.4 | **Queue row** | per state table §4.3 + hover (actions to 100%) + focused (inset ring) + dragging (ghost 90% opacity, translated only) + error-expanded (+20px detail line) |
| 7.5 | **Buttons** | primary (accent fill, `--surface-0` text, hover brightens via `filter: brightness(1.08)` 80ms, pressed scale 0.97) · ghost (transparent, border on hover) · destructive (red outline; fill only in confirm dialogs) · icon-button 28×28 (hover surface-3 circle) · disabled 40% + `not-allowed` |
| 7.6 | **Inputs / selects** | 30px height, `--surface-0` well, 1px border → `--border-strong` hover → accent focus ring · error (red border + 12px message below, never placeholder-as-label) · with-glyph (wincred lock, search) · custom select = Level-1 popover listbox (native select never shown) |
| 7.7 | **Context menu** | Level 1, 220px min, 30px items: icon 16 + label + shortcut right-aligned `--text-dim` mono; hover surface-4; destructive items red text bottom-grouped after separator; submenu flyout 120ms; fully keyboard (typeahead) |
| 7.8 | **Toast** | bottom-right above status bar, 320px, Level 1, auto-dismiss 5s w/ hover-pause; enter translateY(8px)+fade 120ms; kinds: success (done-green left bar, "yourfile.mkv completed · Open folder"), error (red bar + Retry action), info. Max 3 stacked, then "+2 more" collapses into queue-failures link. Toasts announce; queue remains the source of truth |
| 7.9 | **Modal/prompt** | hostkey TOFU (§5.3) · KEY-CHANGED alarm (§5.4) · **overwrite prompt**: file cards side-by-side (name, size, mtime; newer/larger highlighted green), actions `[Overwrite] [Resume]` (when byte-resume possible — flagship placement) `[Rename] [Skip]`, checkbox "Apply to all in this batch (12 remaining)", "make default for this site" link; Esc = Skip |
| 7.10 | **Command palette** (Ctrl+K) | centered top-third, 560px, Level 1+backdrop-blur 8px on scrim only; input 40px; results 36px rows w/ icon, label, fuzzy-highlight, shortcut hint; modes: default = commands, `>` = sites (connect), `/` = paths, `?` = help; enter 120ms scale 0.98→1 + fade; recents-first ranking |
| 7.11 | **Quick-open** (Ctrl+P) | same shell as palette, seeded with pane history + bookmarks + sites; Enter navigates active pane, Ctrl+Enter other pane |
| 7.12 | **Pane empty/loading/error** | *loading*: 8 skeleton rows (surface-2 blocks, opacity pulse 0.5↔0.8 1.2s) only if listing takes >150ms — under that, direct render (skeletons must never appear on fast local listings) · *empty dir*: centered `--text-faint` folder glyph + "Empty · F7 new folder · Ctrl+V paste" · *filter-empty*: "No matches for 'x' · Esc to clear" · *error*: inline banner (red left bar) with message + `[Retry] [Go up]` — the pane chrome stays interactive, never a dead pane |
| 7.13 | **Queue empty state** | collapsed strip shows "queue idle"; expanded: "Nothing queued — F5 transfers the marked files" with a subtle 16px arrow glyph |

---

## 8. Signature moments

1. **The hyperlane progress bar.** Multi-connection chunked transfers render 2–4 independent sub-tracks in one bar, each chunk filling in parallel. No other client shows this because no other client *does* this — it turns warpseed's core engine advantage into its most recognizable visual. (Track: `--surface-0` well; chunks: `--state-active` at 90/70/55/45% opacity; 1px gaps.)
2. **The warp-line.** A 2px gradient line (accent → transparent) along the queue dock's top edge. Idle: static at 25% opacity. On every transfer start: a single light-streak travels its length left→right in 400ms (`clip-path` sweep on a pre-painted gradient — compositor-only). Under active transfers: idle-line opacity breathes 25↔40% at 4s. This is the app's heartbeat, visible even with the dock collapsed. Disabled under reduced-motion.
3. **Connection aura.** The active pane's toolbar carries a 1px accent top border plus a 12px `--accent-glow` gradient fading downward; on pane switch (Tab) the aura crossfades 120ms between panes. Connected remote panes tint the aura toward their protocol color — one glance tells you which pane is live and what it's talking to.
4. **The status-bar sparkline.** A 96×16px canvas plotting 60s of aggregate throughput (rAF-drawn from EMA'd progress events, accent stroke 1.5px, area fill `--accent-glow`). Hover: tooltip with current/avg/peak. Click: expands queue dock. Quiet proof, always on screen, that warpseed is faster than what it replaced.
5. **Palette-driven everything.** Every command in the app — every menu item, every site, every bookmark — is reachable in ≤4 keystrokes via Ctrl+K, with its shortcut printed on the result row. The palette is also the app's own teacher: search "move" and learn F6 exists. New-user onboarding *is* the palette; there is no tour.

---

## 9. Phased rollout

| Component / capability | Spec § | Phase |
|---|---|---|
| Tokens v2 (palette, type, spacing, motion, Inter+Cascadia) | 6 | **0.5** |
| Pane row full state set (hover/cursor/marked/cut) + selection model | 7.1, 3.6 | **0.5** |
| Keyboard map core (Tab, F-keys local ops, Insert/Space marks, history, Ctrl+L) | 3.1 | **0.5** |
| Breadcrumb-with-inline-edit path bar (local; autocomplete deferred) | 3.2 | **0.5** |
| Filter-as-you-type strip | 3.5 | **0.5** |
| Pane empty/loading/error states, skeleton discipline | 7.12 | **0.5** |
| Command palette + quick-open (local commands/paths) | 7.10–7.11 | **0.5** |
| Context menu, buttons, inputs, toasts | 7.5–7.8 | **0.5** |
| Status bar v2 (without sparkline) | 2.2 | **0.5** |
| Queue dock: strip, rows, states, aggregate header, errors/retry | 4 | **1** |
| Site manager, quick-connect, credentials/agent-first UI | 5.1–5.2 | **1** |
| Host-key TOFU + KEY-CHANGED alarm, overwrite prompt w/ apply-to-all | 5.3–5.4, 7.9 | **1** |
| Session tab strip, per-tab pane state | 7.3 | **1** |
| Remote path autocomplete; F5/F6 transfer semantics; resume-fidelity chips | 3.2, 4.3 | **1** |
| Hyperlane chunked bar; warp-line; connection aura | 8.1–8.3 | **1** (aura/warp-line) / with chunked engine (hyperlane) |
| Sparkline; drag-to-reorder queue; density toggle; F1 shortcut overlay | 8.4, 4.4, 6.4 | **2** |
| Synchronized browsing; sort shortcuts; Num-pad glob marking dialog | 3.4, 3.1 | **2** |
| F3 preview, F4 edit-watch loop; accessibility + reduced-motion audit; S3/FTP protocol badges live | 3.1, 6.7 | **2** |
| Sync/compare view (Ctrl+S), folder-monitor successor | 3.1 | **3** (per plan) |

**Phase 0.5 acceptance:** the local shell looks and drives like the end product — a stranger seeing a screenshot should not be able to date it, and a WinSCP user should be able to mark 5 files and F5 them between panes without reading anything.

---

## Appendix A — Traceability: carried-forward features

| Legacy feature | Source | warpseed form |
|---|---|---|
| Commander dual panes, Tab switch, F5/F6/F7/F8/F2/F9 | WinSCP/NC | §3.1, unchanged bindings |
| Insert/Space/Num+−* marking model | WinSCP/NC | §3.6 |
| Session tabs, Alt+1..9 | WinSCP | §7.3 |
| Synchronized browsing (Ctrl+Alt+B) | WinSCP | §3.4 |
| Remote path autocomplete | WinSCP | §3.2 |
| Incremental search + filter | WinSCP (two features) | §3.5, unified strip |
| Location profiles / bookmarks | WinSCP | Ctrl+D + quick-open (§3.3) |
| Sort hotkeys Ctrl+F3..F6, hidden-file toggle | WinSCP | §3.1 |
| Site manager with per-site settings | CuteFTP/WinSCP | §5 |
| Site-independent persistent queue, tag-now-transfer-later, overnight auditability | CuteFTP | §4.4 |
| Queue reorder + per-item control | CuteFTP | §4.2, 4.4 |
| Overwrite apply-to-all | Both | §7.9 |
| Rejected as dated: connection wizard, floating queue window, toolbar-button forests, modal-heavy errors | CuteFTP | replaced by §5.1 popover, §4 dock, §7.10 palette, §7.12 inline banners |

## Appendix B — References

- [WinSCP Commander Keyboard Shortcuts](https://winscp.net/eng/docs/ui_commander_key) (bindings verified against official docs)
- [CuteFTP product documentation](https://hstechdocs.helpsystems.com/manuals/globalscape/cuteftp9/Introduction_to_CuteFTP.htm) · [CuteFTP Pro feature history](https://hstechdocs.helpsystems.com/manuals/globalscape/archive/cuteftppro3/New_Features_in_CuteFTP_Pro_3.htm) (queue/site-manager feature inventory)
- Modern-feel references: [Raycast design system](https://styles.refero.design/style/3b6a17f0-3bdf-418c-a95e-0b89e5a8b2f8) · [Designing a Command Palette](https://destiner.io/blog/post/designing-a-command-palette/) · [Warp terminal UX analysis](https://dev.to/omriluz1/exploring-warp-terminal-a-modern-approach-to-command-line-productivity-5793) · [ForkLift 4](https://binarynights.com/) (dual-pane + Activity view precedent)
- Internal: `docs/planning/approved-plan.md` (speed budget, phases), `docs/planning/design_1.md` (event contract), `frontend/src/tokens.css` (tokens v1, superseded by §6).
