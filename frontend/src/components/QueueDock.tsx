import { useEffect } from "react";
import {
  cancelTransfer,
  clearDoneTransfers,
  on,
  pauseTransfer,
  resumeTransfer,
  transfersList,
  type TransferProgress,
  type TransferState,
} from "../ipc";
import { formatSize } from "../lib/format";
import { useUiStore } from "../store";

const STATE_ICON: Record<string, string> = {
  pending: "◷",
  dispatched: "◷",
  active: "▶",
  paused: "⏸",
  completed: "✓",
  failed: "⚠",
  cancelled: "✕",
};

function baseName(p: string): string {
  const i = Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\"));
  return i >= 0 ? p.slice(i + 1) : p;
}

function eta(bytes: number, size: number, rate: number): string {
  if (rate <= 0 || size <= 0 || bytes >= size) return "";
  const s = Math.round((size - bytes) / rate);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** Persistent queue dock (ux-spec §4): collapsed aggregate strip, expandable
    row list, pause/resume/cancel with byte-resume semantics. */
export default function QueueDock() {
  const transfers = useUiStore((s) => s.transfers);
  const progress = useUiStore((s) => s.progress);
  const open = useUiStore((s) => s.queueOpen);
  const setOpen = useUiStore((s) => s.setQueueOpen);
  const setTransfers = useUiStore((s) => s.setTransfers);
  const applyProgress = useUiStore((s) => s.applyProgress);
  const patchTransferState = useUiStore((s) => s.patchTransferState);
  const sites = useUiStore((s) => s.sites);

  useEffect(() => {
    const refresh = () => void transfersList().then(setTransfers).catch(() => undefined);
    refresh();
    const offChanged = on("queue:changed", refresh);
    const offProgress = on<TransferProgress>("transfer:progress", (p) =>
      applyProgress(p.id, p.bytes, p.size),
    );
    const offState = on<TransferState>("transfer:state", (s) =>
      patchTransferState(s.id, s.state, s.error),
    );
    return () => {
      offChanged();
      offProgress();
      offState();
    };
  }, [setTransfers, applyProgress, patchTransferState]);

  const live = transfers.map((t) => {
    const p = progress[t.id];
    const bytes = p && p.bytes > t.bytesDone ? p.bytes : t.bytesDone;
    return { ...t, bytes, rate: t.state === "active" ? p?.rate ?? 0 : 0 };
  });

  const active = live.filter((t) => t.state === "active");
  const queued = live.filter((t) => t.state === "pending" || t.state === "dispatched");
  const failed = live.filter((t) => t.state === "failed");
  const aggRate = active.reduce((s, t) => s + t.rate, 0);
  const incomplete = live.filter((t) => !["completed", "cancelled"].includes(t.state));
  const totalBytes = incomplete.reduce((s, t) => s + Math.max(t.size, 0), 0);
  const doneBytes = incomplete.reduce((s, t) => s + t.bytes, 0);

  return (
    <div className={`dock ${active.length ? "dock--active" : ""}`}>
      <button className="dock__strip" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span>{open ? "▾" : "▴"}</span>
        <span className="dock__microbar" aria-hidden>
          <div
            style={{ transform: `scaleX(${totalBytes > 0 ? doneBytes / totalBytes : 0})` }}
          />
        </span>
        {aggRate > 0 && <span className="agg-rate">{formatSize(aggRate)}/s</span>}
        <span>
          {active.length} active · {queued.length} queued
        </span>
        {failed.length > 0 && <span className="chip-failed">{failed.length} failed ⚠</span>}
        <span className="grow" />
        <span>queue</span>
      </button>

      {open && (
        <div className="dock__body">
          <div className="dock__header">
            <span className="grow" />
            <button onClick={() => void clearDoneTransfers()}>✕ Clear done</button>
          </div>
          {live.length === 0 ? (
            <div className="dock__empty">Nothing queued — mark files and press F5</div>
          ) : (
            live.map((t) => {
              const pct = t.size > 0 ? Math.min(t.bytes / t.size, 1) : 0;
              const siteName = sites.find((s) => s.id === t.siteId)?.name ?? `site ${t.siteId}`;
              const hasError = t.state === "failed" && t.error;
              return (
                <div key={t.id} className={`trow trow--${t.state} ${hasError ? "trow--witherror" : ""}`}>
                  <span className="trow__icon">{STATE_ICON[t.state] ?? "·"}</span>
                  <span className="trow__name" title={t.src}>
                    {baseName(t.src)}
                  </span>
                  <span className="trow__route" title={`${t.src} → ${t.dst}`}>
                    {siteName} → {t.dst}
                  </span>
                  <span className="trow__bar" aria-hidden>
                    <div style={{ transform: `scaleX(${pct})` }} />
                  </span>
                  <span className="trow__pct">
                    {t.size > 0 ? `${Math.floor(pct * 100)}%` : formatSize(t.bytes)}
                  </span>
                  <span className="trow__rate">
                    {t.state === "active" && t.rate > 0
                      ? `${formatSize(t.rate)}/s`
                      : eta(t.bytes, t.size, t.rate)}
                  </span>
                  <span className="trow__actions">
                    {t.state === "active" || t.state === "pending" ? (
                      <button title="Pause" onClick={() => void pauseTransfer(t.id)}>
                        ⏸
                      </button>
                    ) : t.state === "paused" || t.state === "failed" ? (
                      <button title="Resume / retry" onClick={() => void resumeTransfer(t.id)}>
                        ▶
                      </button>
                    ) : null}
                    {!["completed", "cancelled"].includes(t.state) && (
                      <button title="Cancel" onClick={() => void cancelTransfer(t.id)}>
                        ✕
                      </button>
                    )}
                  </span>
                  {hasError && <span className="trow__error">{t.error}</span>}
                </div>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}
