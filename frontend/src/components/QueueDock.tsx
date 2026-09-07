import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ComponentType } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  cancelTransfer,
  clearDoneTransfers,
  clearFailedTransfers,
  getSettings,
  on,
  pauseTransfer,
  resumeTransfer,
  retryFailedTransfers,
  setSetting,
  type Transfer,
  type TransferProgress,
  type TransferState,
} from "../ipc";
import { useColumnWidths, type ColumnSpec } from "../hooks/useColumnWidths";
import { describeTransferError as describeError, formatSize } from "../lib/format";
import { baseName } from "../lib/path";
import { toast } from "../lib/toast";
import { useUiStore } from "../store";
import PromptDialog, { type PromptSpec } from "./PromptDialog";
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

/** Trailing window for coalescing queue:changed bursts into one refetch. */
const REFRESH_COALESCE_MS = 120;

/** Row heights from queue.css; measured after first paint, these are only
    the first-render estimates. */
const ROW_H = 34;
const ROW_H_ERROR = 54;

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

/** One collator for the whole dock: localeCompare with an options object
    builds a collator per comparison, which at 2000 rows is tens of
    milliseconds per sort. */
const collator = new Intl.Collator(undefined, { sensitivity: "base" });

/** Rows in flight pin above everything else in the default order. */
const liveRank = (t: Transfer): number => (t.state === "active" || t.state === "dispatched" ? 0 : 1);

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
  const [prompt, setPrompt] = useState<PromptSpec | null>(null);
  const transfers = useUiStore((s) => s.transfers);
  const progress = useUiStore((s) => s.progress);
  const open = useUiStore((s) => s.queueOpen);
  const setOpen = useUiStore((s) => s.setQueueOpen);
  const refreshTransfers = useUiStore((s) => s.refreshTransfers);
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
    // queue:changed fires once per state change, so a run of small files
    // completing at 8 a second would refetch the whole list 8 times a
    // second. Coalesce bursts into one trailing fetch; the store drops any
    // response that a newer read or a state patch has overtaken.
    let timer: number | null = null;
    const fetchNow = () => {
      if (timer !== null) {
        window.clearTimeout(timer);
        timer = null;
      }
      void refreshTransfers();
    };
    const refresh = () => {
      if (timer !== null) return; // a fetch is already scheduled; it will see this change
      timer = window.setTimeout(() => {
        timer = null;
        fetchNow();
      }, REFRESH_COALESCE_MS);
    };
    fetchNow();
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
      // A row claimed straight after enqueue goes active before the
      // coalesced refetch has landed it: name it from the payload and pull
      // the list now rather than on the timer. (The patch below is
      // overlaid on that read when it lands, so it is not wasted.)
      if (!t) fetchNow();
      const name = t ? baseName(t.src) : s.src ? baseName(s.src) : `transfer #${s.id}`;
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
      if (timer !== null) window.clearTimeout(timer);
      offChanged();
      offProgress();
      offState();
    };
  }, [refreshTransfers, applyProgress, patchTransferState]);

  // Every progress tick re-renders the dock, so the sort is split: orders
  // that depend only on the rows (name, destination, size, state, and the
  // default) are memoized against the row list, and only the two orders
  // that read live progress (speed, %) re-sort per tick.
  const ordered = useMemo(() => {
    const dir = sort.desc ? -1 : 1;
    const tie = (a: Transfer, b: Transfer) => b.id - a.id; // queue order regardless of direction
    switch (sort.key) {
      case "name":
        return [...transfers].sort(
          (a, b) => collator.compare(baseName(a.src), baseName(b.src)) * dir || tie(a, b),
        );
      case "dest":
        return [...transfers].sort((a, b) => collator.compare(a.dst, b.dst) * dir || tie(a, b));
      case "size":
        return [...transfers].sort((a, b) => (a.size - b.size) * dir || tie(a, b));
      case "state":
        return [...transfers].sort(
          (a, b) => ((STATE_RANK[a.state] ?? 9) - (STATE_RANK[b.state] ?? 9)) * dir || tie(a, b),
        );
      default:
        // Queue order, newest first — with what is in flight pinned to the
        // top (ux-spec §4) so a 2000-row backlog never buries it.
        return [...transfers].sort((a, b) => liveRank(a) - liveRank(b) || tie(a, b));
    }
  }, [transfers, sort]);

  const live = ordered.map((t) => {
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

  let rows = live;
  if (sort.key === "rate" || sort.key === "pct") {
    const dir = sort.desc ? -1 : 1;
    const pctOf = (t: (typeof live)[number]) => (t.size > 0 ? t.bytes / t.size : 0);
    rows = [...live].sort((a, b) => {
      const d = sort.key === "rate" ? a.rate - b.rate : pctOf(a) - pctOf(b);
      if (d === 0) return b.id - a.id; // ties keep queue order regardless of direction
      return d * dir;
    });
  }

  // The body is the scroll container; the toolbar and column headers sit
  // sticky inside it above the rows, so the row list starts partway down
  // the scroll content. Only the rows in view are mounted: the window can
  // hold up to 2000 unfinished rows and every progress tick re-renders the
  // dock, which is fine for a dozen rows and a stall for two thousand.
  const bodyRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [listTop, setListTop] = useState(0);
  const hasRows = rows.length > 0;
  useLayoutEffect(() => {
    // The list only exists while there are rows: a dock opened empty and
    // filled later must measure again when the list mounts.
    if (!open || !hasRows || !bodyRef.current || !listRef.current) return;
    // offsetTop is layout position, unaffected by the body's scroll.
    setListTop(listRef.current.offsetTop - bodyRef.current.offsetTop);
  }, [open, hasRows]);
  // Stable callbacks: the virtualizer rebuilds its whole measurement table
  // whenever getItemKey/estimateSize change identity, which per progress
  // tick would be the O(rows) work virtualizing was meant to remove.
  const rowsRef = useRef(rows);
  rowsRef.current = rows;
  const getItemKey = useCallback((i: number) => rowsRef.current[i].id, []);
  const estimateSize = useCallback(
    (i: number) => {
      const t = rowsRef.current[i];
      return t.state === "failed" && t.error ? ROW_H_ERROR : ROW_H;
    },
    [],
  );
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => bodyRef.current,
    getItemKey,
    estimateSize,
    overscan: 8,
    scrollMargin: listTop,
  });

  // Strip figures that depend only on the rows are memoized against them;
  // only the rate and the done-bytes total read live progress per tick.
  const counts = useMemo(() => {
    let queued = 0;
    let failed = 0;
    let totalBytes = 0;
    for (const t of transfers) {
      if (t.state === "pending" || t.state === "dispatched") queued++;
      else if (t.state === "failed") failed++;
      if (t.state !== "completed" && t.state !== "cancelled") totalBytes += Math.max(t.size, 0);
    }
    return { queued, failed, totalBytes };
  }, [transfers]);
  // A drive pulled mid-run, or a server that spent an hour refusing
  // connections, fails a whole batch at once. Both of these exist so the
  // recovery is one click rather than one click per file.
  const retryFailed = useCallback(() => {
    void retryFailedTransfers()
      .then((n) => toast("success", `Requeued ${n} failed transfer${n === 1 ? "" : "s"}`))
      .catch((err: unknown) => toast("error", String(err)));
  }, []);

  const clearFailed = useCallback(() => {
    // Snapshot the ids with the count: the backend clears these rows and no
    // others, so what the dialog says is what happens even if more fail
    // while it sits open.
    const ids = transfers.filter((t) => t.state === "failed").map((t) => t.id);
    const one = ids.length === 1;
    setPrompt({
      title: `Clear ${ids.length} failed transfer${one ? "" : "s"}?`,
      body: one
        ? "Its part-downloaded data is deleted too, so this file starts from the beginning if you queue it again. Finished files are untouched."
        : "Their part-downloaded data is deleted too, so these files start from the beginning if you queue them again. Finished files are untouched.",
      confirmLabel: "Clear failed",
      danger: true,
      onConfirm: () => {
        void clearFailedTransfers(ids)
          .then(({ cleared, kept }) => {
            toast("success", `Cleared ${cleared} failed transfer${cleared === 1 ? "" : "s"}`);
            // A kept row still has data somewhere — a remote placeholder on a
            // site that is not connected, or a file we could not delete.
            // Saying "cleared" and leaving it on screen would look like a bug.
            if (kept > 0) {
              toast(
                "info",
                `${kept} kept: their data could not be removed. Connect the site and try again.`,
              );
            }
          })
          .catch((err: unknown) => toast("error", String(err)));
      },
    });
  }, [transfers]);

  const active = live.filter((t) => t.state === "active");
  const aggRate = active.reduce((s, t) => s + t.rate, 0);
  let doneBytes = 0;
  for (const t of live) if (t.state !== "completed" && t.state !== "cancelled") doneBytes += t.bytes;
  const { totalBytes } = counts;

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
          {active.length} active · {counts.queued} queued
        </span>
        {counts.failed > 0 && (
          <span className="chip-failed">
            <Warning size={11} />
            {counts.failed} failed
          </span>
        )}
        <span className="grow" />
        <span className="dock__title">Queue</span>
        <ChevronRight size={12} className={`dock__caret ${open ? "dock__caret--open" : ""}`} />
      </button>

      <PromptDialog spec={prompt} onClose={() => setPrompt(null)} />

      {open && (
        <div className="dock__body" style={colStyle} ref={bodyRef}>
          {/* The failure actions sit LEFT of the spacer on purpose. The app
              grid stretches to its widest row, so at narrow windows the
              right end of this bar is clipped by an ancestor — measured,
              not assumed. Anything a user needs after a batch failure has
              to stay on the reachable side. */}
          <div className="dock__header">
            {counts.failed > 0 && (
              <>
                <button onClick={retryFailed} title="Requeue every failed transfer, resuming where each stopped">
                  <Play size={12} />
                  Retry failed
                </button>
                <button onClick={clearFailed} title="Remove every failed transfer and its part-downloaded data">
                  <Warning size={12} />
                  Clear failed
                </button>
              </>
            )}
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
            <div
              className="dock__list"
              ref={listRef}
              style={{ height: virtualizer.getTotalSize() }}
            >
            {virtualizer.getVirtualItems().map((vi) => {
              const t = rows[vi.index];
              const pct = t.size > 0 ? Math.min(t.bytes / t.size, 1) : 0;
              const siteName = sites.find((s) => s.id === t.siteId)?.name ?? `site ${t.siteId}`;
              const hasError = t.state === "failed" && t.error;
              const lanes = t.chunks && t.chunks.length > 1 ? t.chunks : null;
              const StateIcon = STATE_ICON[t.state] ?? ChevronRight;
              return (
                <div
                  key={t.id}
                  data-index={vi.index}
                  ref={virtualizer.measureElement}
                  className={`trow trow--virtual trow--${t.state} ${hasError ? "trow--witherror" : ""}`}
                  style={{ transform: `translateY(${vi.start - listTop}px)` }}
                >
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
            })}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
