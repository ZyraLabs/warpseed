import { useEffect, useRef, useState } from "react";
import type { ComponentType } from "react";
import {
  cancelTransfer,
  clearDoneTransfers,
  getSettings,
  on,
  pauseTransfer,
  resumeTransfer,
  setSetting,
  transfersList,
  type TransferProgress,
  type TransferState,
} from "../ipc";
import { useColumnWidths, type ColumnSpec } from "../hooks/useColumnWidths";
import { describeTransferError as describeError, formatSize } from "../lib/format";
import { baseName } from "../lib/path";
import { useUiStore } from "../store";
import {
  ArrowUp,
  Check,
  ChevronRight,
  Close,
  Pause,
  Play,
  Refresh,
  Slipstream,
  Warning,
  type IconProps,
} from "./Icon";

/** Shared stroke icons per state (design contract: no glyph characters).
    Queued work gets the single "up next" chevron; running states reuse the
    playback icons so the dock reads at a glance. */
const STATE_ICON: Record<string, ComponentType<IconProps>> = {
  pending: ChevronRight,
  dispatched: ChevronRight,
  active: Play,
  paused: Pause,
  completed: Check,
  failed: Warning,
  cancelled: Close,
};

function eta(bytes: number, size: number, rate: number): string {
  if (rate <= 0 || size <= 0 || bytes >= size) return "";
  const s = Math.round((size - bytes) / rate);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** Persistent queue dock (ux-spec §4): collapsed aggregate strip, expandable
    row list, pause/resume/cancel with byte-resume semantics. */
/** Resizable queue columns; the progress track absorbs the leftover space. */
const QUEUE_COLUMNS: ColumnSpec[] = [
  { id: "name", label: "File", min: 90, initial: 200 },
  { id: "route", label: "Destination", min: 80, initial: 170 },
  { id: "size", label: "Size", min: 56, initial: 84 },
  { id: "rate", label: "Speed / ETA", min: 60, initial: 88 },
  { id: "pct", label: "%", min: 40, initial: 52 },
];

/** "added" is queue order (newest first, as the store returns rows). */
type QSortKey = "added" | "state" | "name" | "dest" | "size" | "rate" | "pct";
interface QSort {
  key: QSortKey;
  desc: boolean;
}

const COL_SORT: Record<string, QSortKey> = {
  name: "name",
  route: "dest",
  size: "size",
  rate: "rate",
  pct: "pct",
};

/** Ascending state sort surfaces what needs attention: errors first, then
    running work, with finished rows at the bottom. */
const STATE_RANK: Record<string, number> = {
  failed: 0,
  active: 1,
  paused: 2,
  dispatched: 3,
  pending: 4,
  completed: 5,
  cancelled: 6,
};

export default function QueueDock() {
  const { style: colStyle, startResize, reset } = useColumnWidths(QUEUE_COLUMNS, "ui.queue_columns");
  const [streak, setStreak] = useState(false);
  const transfers = useUiStore((s) => s.transfers);
  const progress = useUiStore((s) => s.progress);
  const open = useUiStore((s) => s.queueOpen);
  const setOpen = useUiStore((s) => s.setQueueOpen);
  const setTransfers = useUiStore((s) => s.setTransfers);
  const applyProgress = useUiStore((s) => s.applyProgress);
  const patchTransferState = useUiStore((s) => s.patchTransferState);
  const sites = useUiStore((s) => s.sites);
  const [sort, setSort] = useState<QSort>({ key: "added", desc: false });
  // A click during the async hydration read must win over the stale stored
  // value (same rule prefs.ts enforces for the other UI settings).
  const sortTouched = useRef(false);

  // Hydrate the saved sort once; the click handler persists changes.
  useEffect(() => {
    void getSettings()
      .then((cfg) => {
        if (sortTouched.current) return;
        const raw = cfg["ui.queue_sort"];
        if (!raw) return;
        const s = JSON.parse(raw) as Partial<QSort>;
        const keys: QSortKey[] = ["added", "state", "name", "dest", "size", "rate", "pct"];
        if (typeof s.key === "string" && (keys as string[]).includes(s.key)) {
          setSort({ key: s.key, desc: s.desc === true });
        }
      })
      .catch(() => undefined);
  }, []);

  /** Click cycles ascending → descending → back to queue order. */
  const toggleSort = (key: QSortKey) => {
    sortTouched.current = true;
    setSort((prev) => {
      const next: QSort =
        prev.key !== key
          ? { key, desc: false }
          : prev.desc
            ? { key: "added", desc: false }
            : { key, desc: true };
      void setSetting("ui.queue_sort", JSON.stringify(next)).catch(() => undefined);
      return next;
    });
  };

  useEffect(() => {
    const refresh = () => void transfersList().then(setTransfers).catch(() => undefined);
    refresh();
    // Session-log capture lives here because the dock is always mounted.
    // Change detection uses local snapshots: this handler writes the new
    // state into the store itself, so comparing against the store would
    // always answer "unchanged".
    const push = useUiStore.getState().pushSessionEvent;
    const lastState = new Map(useUiStore.getState().transfers.map((t) => [t.id, t.state]));
    const seenLanes = new Set<number>(); // ids already announced as multi-lane
    const offChanged = on("queue:changed", refresh);
    const offProgress = on<TransferProgress>("transfer:progress", (p) => {
      applyProgress(p.id, p.bytes, p.size, p.chunks);
      if (p.chunks && p.chunks.length > 1 && !seenLanes.has(p.id)) {
        seenLanes.add(p.id);
        const t = useUiStore.getState().transfers.find((x) => x.id === p.id);
        push("info", `hyperlane ×${p.chunks.length} engaged — ${t ? baseName(t.src) : `#${p.id}`}`);
      }
    });
    const offState = on<TransferState>("transfer:state", (s) => {
      const prev = lastState.get(s.id);
      lastState.set(s.id, s.state);
      const t = useUiStore.getState().transfers.find((x) => x.id === s.id);
      const name = t ? baseName(t.src) : `transfer #${s.id}`;
      if (s.state === "completed") {
        push("ok", `${name} completed${t && t.size > 0 ? ` · ${formatSize(t.size)}` : ""}`);
      } else if (s.state === "failed") {
        const why = s.error ?? "";
        push("err", `${name} failed${why ? ` — ${why.length > 80 ? why.slice(0, 79) + "…" : why}` : ""}`);
      } else if (s.state === "active" && prev !== "active") {
        push("info", `${name} in flight`);
      }
      patchTransferState(s.id, s.state, s.error);
      if (s.state === "active") setStreak(true); // warp-line streak (§8.2)
    });
    return () => {
      offChanged();
      offProgress();
      offState();
    };
  }, [setTransfers, applyProgress, patchTransferState]);

  const live = transfers.map((t) => {
    const p = progress[t.id];
    const bytes = p && p.bytes > t.bytesDone ? p.bytes : t.bytesDone;
    // Lanes belong to a running multi-connection transfer; once it settles,
    // fall back to the single bar so the row reads as done/paused/failed.
    const showLanes = t.state === "active" || t.state === "paused";
    return {
      ...t,
      bytes,
      rate: t.state === "active" ? p?.rate ?? 0 : 0,
      chunks: showLanes ? p?.chunks : undefined,
    };
  });

  const rows = [...live];
  if (sort.key !== "added") {
    const dir = sort.desc ? -1 : 1;
    const pctOf = (t: (typeof live)[number]) => (t.size > 0 ? t.bytes / t.size : 0);
    rows.sort((a, b) => {
      let d = 0;
      switch (sort.key) {
        case "name":
          d = baseName(a.src).localeCompare(baseName(b.src), undefined, { sensitivity: "base" });
          break;
        case "dest":
          d = a.dst.localeCompare(b.dst, undefined, { sensitivity: "base" });
          break;
        case "size":
          d = a.size - b.size;
          break;
        case "rate":
          d = a.rate - b.rate;
          break;
        case "pct":
          d = pctOf(a) - pctOf(b);
          break;
        case "state":
          d = (STATE_RANK[a.state] ?? 9) - (STATE_RANK[b.state] ?? 9);
          break;
      }
      if (d === 0) return b.id - a.id; // ties keep queue order regardless of direction
      return d * dir;
    });
  }

  const active = live.filter((t) => t.state === "active");
  const queued = live.filter((t) => t.state === "pending" || t.state === "dispatched");
  const failed = live.filter((t) => t.state === "failed");
  const aggRate = active.reduce((s, t) => s + t.rate, 0);
  const incomplete = live.filter((t) => !["completed", "cancelled"].includes(t.state));
  const totalBytes = incomplete.reduce((s, t) => s + Math.max(t.size, 0), 0);
  const doneBytes = incomplete.reduce((s, t) => s + t.bytes, 0);

  return (
    <div
      className={`dock ${active.length ? "dock--active" : ""} ${streak ? "dock--streak" : ""}`}
      onAnimationEnd={(e) => e.animationName === "warp-streak" && setStreak(false)}
    >
      <button className="dock__strip" onClick={() => setOpen(!open)} aria-expanded={open}>
        <Slipstream size={14} className="dock__glyph" />
        <span className="dock__microbar" aria-hidden>
          <div
            style={{ transform: `scaleX(${totalBytes > 0 ? doneBytes / totalBytes : 0})` }}
          />
        </span>
        {aggRate > 0 && <span className="agg-rate">{formatSize(aggRate)}/s</span>}
        <span>
          {active.length} active · {queued.length} queued
        </span>
        {failed.length > 0 && (
          <span className="chip-failed">
            <Warning size={11} />
            {failed.length} failed
          </span>
        )}
        <span className="grow" />
        <span className="dock__title">Queue</span>
        <ChevronRight size={12} className={`dock__caret ${open ? "dock__caret--open" : ""}`} />
      </button>

      {open && (
        <div className="dock__body" style={colStyle}>
          <div className="dock__header">
            <span className="grow" />
            <button onClick={reset} title="Restore default column widths">
              <Refresh size={12} />
              Reset columns
            </button>
            <button onClick={() => void clearDoneTransfers()}>
              <Close size={12} />
              Clear done
            </button>
          </div>

          {/* Column headers double as resize handles — drag the divider on
              the right of a heading to widen it. */}
          <div className="trow trow--head">
            <button
              className={`trow__icon thead__sort ${sort.key === "state" ? "thead__sort--on" : ""}`}
              role="columnheader"
              aria-sort={sort.key === "state" ? (sort.desc ? "descending" : "ascending") : "none"}
              onClick={() => toggleSort("state")}
              title="Sort by status (failed first)"
            >
              {sort.key === "state" ? (
                <ArrowUp size={10} className={`thead__dir ${sort.desc ? "thead__dir--desc" : ""}`} />
              ) : (
                <Warning size={11} />
              )}
            </button>
            {QUEUE_COLUMNS.map((c) => {
              const key = COL_SORT[c.id];
              const on = sort.key === key;
              return (
                <span
                  key={c.id}
                  className={`thead thead--${c.id}`}
                  role="columnheader"
                  aria-sort={on ? (sort.desc ? "descending" : "ascending") : "none"}
                >
                  <button
                    className={`thead__sort ${on ? "thead__sort--on" : ""}`}
                    onClick={() => toggleSort(key)}
                    title={`Sort by ${c.label.toLowerCase()} — click again to reverse, again for queue order`}
                  >
                    {c.label}
                    {/* The arrow is always rendered so the label never shifts when
                        the sorted column changes; idle ones stay invisible. */}
                    <ArrowUp
                      size={9}
                      className={`thead__dir ${on ? (sort.desc ? "thead__dir--desc" : "") : "thead__dir--idle"}`}
                    />
                  </button>
                  <span
                    className="thead__grip"
                    role="separator"
                    aria-orientation="vertical"
                    aria-label={`Resize ${c.label}`}
                    onMouseDown={(e) => startResize(c.id, e)}
                  />
                </span>
              );
            })}
            <span className="thead">Progress</span>
            <span />
          </div>

          {rows.length === 0 ? (
            <div className="dock__empty">Nothing queued — mark files and press F5</div>
          ) : (
            rows.map((t) => {
              const pct = t.size > 0 ? Math.min(t.bytes / t.size, 1) : 0;
              const siteName = sites.find((s) => s.id === t.siteId)?.name ?? `site ${t.siteId}`;
              const hasError = t.state === "failed" && t.error;
              const lanes = t.chunks && t.chunks.length > 1 ? t.chunks : null;
              const StateIcon = STATE_ICON[t.state] ?? ChevronRight;
              return (
                <div key={t.id} className={`trow trow--${t.state} ${hasError ? "trow--witherror" : ""}`}>
                  <span className="trow__icon">
                    <StateIcon size={13} />
                  </span>
                  <span className="trow__name" title={t.src}>
                    {baseName(t.src)}
                  </span>
                  <span className="trow__route" title={`${t.src} → ${t.dst}`}>
                    {siteName} → {t.dst}
                  </span>
                  <span className="trow__size" title={t.size > 0 ? `${t.size} bytes` : undefined}>
                    {t.size > 0 ? formatSize(t.size) : "—"}
                  </span>
                  <span className="trow__rate">
                    {t.state === "active" && t.rate > 0
                      ? `${formatSize(t.rate)}/s`
                      : eta(t.bytes, t.size, t.rate)}
                  </span>
                  <span className="trow__pct">
                    {t.size > 0 ? `${Math.floor(pct * 100)}%` : formatSize(t.bytes)}
                  </span>
                  {/* Hyperlane: one sub-track per connection when a file is
                      split across several — the engine made visible. */}
                  {lanes ? (
                    <span
                      className="trow__bar trow__bar--hyper"
                      title={`Hyperlane · ${lanes.length} parallel connections`}
                    >
                      {lanes.map((f, i) => (
                        <span key={i} className="hyper__lane">
                          <span style={{ transform: `scaleX(${Math.min(Math.max(f, 0), 1)})` }} />
                        </span>
                      ))}
                    </span>
                  ) : (
                    <span className="trow__bar" aria-hidden>
                      <div style={{ transform: `scaleX(${pct})` }} />
                    </span>
                  )}
                  <span className="trow__actions">
                    {t.state === "active" || t.state === "pending" ? (
                      <button title="Pause" onClick={() => void pauseTransfer(t.id)}>
                        <Pause size={11} />
                      </button>
                    ) : t.state === "paused" || t.state === "failed" ? (
                      <button title="Resume / retry" onClick={() => void resumeTransfer(t.id)}>
                        <Play size={11} />
                      </button>
                    ) : null}
                    {!["completed", "cancelled"].includes(t.state) && (
                      <button title="Cancel" onClick={() => void cancelTransfer(t.id)}>
                        <Close size={11} />
                      </button>
                    )}
                  </span>
                  {hasError && <span className="trow__error">{describeError(t.error ?? "")}</span>}
                </div>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}
