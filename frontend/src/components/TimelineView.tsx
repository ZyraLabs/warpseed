import { resumeTransfer, cancelTransfer } from "../ipc";
import { describeTransferError, formatSize } from "../lib/format";
import { baseName } from "../lib/path";
import { useUiStore } from "../store";
import { Check, Close, Play, Warning } from "./Icon";
import "../timeline.css";

interface Entry {
  key: string;
  at: number;
  kind: "ok" | "err" | "info";
  text: string;
  detail?: string;
  transferId?: number; // failed entries carry retry/skip actions
}

function timeOf(rfc3339: string): number {
  const t = Date.parse(rfc3339);
  return Number.isNaN(t) ? 0 : t;
}

function hhmm(at: number): string {
  const d = new Date(at);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

function dayLabel(at: number, now: number): string {
  const d = new Date(at);
  const n = new Date(now);
  const day = (x: Date) => `${x.getFullYear()}-${x.getMonth()}-${x.getDate()}`;
  if (day(d) === day(n)) return "Today";
  const y = new Date(now - 86_400_000);
  if (day(d) === day(y)) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
}

/** Activity view: the session as a chronological story — live work pinned on
    top, then finished/failed transfers and session events interleaved. */
export default function TimelineView() {
  const transfers = useUiStore((s) => s.transfers);
  const progress = useUiStore((s) => s.progress);
  const sessionLog = useUiStore((s) => s.sessionLog);

  const now = Date.now();
  const active = transfers.filter((t) => t.state === "active");
  const queued = transfers.filter((t) => t.state === "pending" || t.state === "dispatched");

  // Historical entries: settled queue rows + captured session events. The
  // queue rows survive restarts (SQLite), the session events do not — so a
  // fresh launch still shows what landed, just not the connect chatter.
  const entries: Entry[] = [];
  for (const t of transfers) {
    const at = timeOf(t.updatedAt);
    if (t.state === "completed") {
      entries.push({
        key: `t${t.id}`,
        at,
        kind: "ok",
        text: `${baseName(t.src)} landed`,
        detail: `${t.size > 0 ? formatSize(t.size) : ""}${t.dst ? ` · ${t.dst}` : ""}`,
      });
    } else if (t.state === "failed") {
      entries.push({
        key: `t${t.id}`,
        at,
        kind: "err",
        text: `${baseName(t.src)} stalled at ${t.size > 0 ? `${Math.floor((t.bytesDone / t.size) * 100)}%` : "start"}`,
        detail: describeTransferError(t.error ?? "failed"),
        transferId: t.id,
      });
    } else if (t.state === "cancelled") {
      entries.push({ key: `t${t.id}`, at, kind: "info", text: `${baseName(t.src)} cancelled` });
    }
  }
  for (const [i, e] of sessionLog.entries()) {
    // Completed/failed session events would duplicate the queue rows above;
    // keep the log's connection and hyperlane lines only.
    if (e.text.includes(" completed") || e.text.includes(" failed")) continue;
    entries.push({ key: `s${i}-${e.at}`, at: e.at, kind: e.kind, text: e.text });
  }
  entries.sort((a, b) => b.at - a.at);

  let lastDay = "";

  return (
    <div className="tl" role="region" aria-label="Activity timeline">
      <div className="tl__scroll">
        <div className="tl__spine" aria-hidden="true" />

        {active.map((t) => {
          const p = progress[t.id];
          const bytes = p && p.bytes > t.bytesDone ? p.bytes : t.bytesDone;
          const pct = t.size > 0 ? Math.min(bytes / t.size, 1) : 0;
          const chunks = p?.chunks && p.chunks.length > 1 ? p.chunks : null;
          return (
            <div key={t.id} className="tl__row">
              <span className="tl__time">now</span>
              <span className="tl__dot tl__dot--live" />
              <div className="tl__card tl__card--live">
                <span className="tl__name" title={t.src}>
                  {baseName(t.src)}
                </span>
                <span className="tl__lanes">
                  {(chunks ?? [pct]).map((f, i) => (
                    <span key={i} className="tl__lane">
                      <span style={{ transform: `scaleX(${Math.min(Math.max(f, 0), 1)})` }} />
                    </span>
                  ))}
                </span>
                <span className="tl__meta">
                  {Math.floor(pct * 100)}% · {formatSize(t.state === "active" ? p?.rate ?? 0 : 0)}/s
                </span>
              </div>
            </div>
          );
        })}
        {queued.length > 0 && (
          <div className="tl__row">
            <span className="tl__time" />
            <span className="tl__dot" />
            <div className="tl__line">
              {queued.length} queued · next: {baseName(queued[0].src)}
            </div>
          </div>
        )}

        {entries.map((e) => {
          const label = dayLabel(e.at, now);
          const header = label !== lastDay ? <div className="tl__day">{label}</div> : null;
          lastDay = label;
          return (
            <div key={e.key}>
              {header}
              <div className="tl__row">
                <span className="tl__time">{e.at > 0 ? hhmm(e.at) : ""}</span>
                <span className={`tl__dot tl__dot--${e.kind}`} />
                {e.transferId !== undefined ? (
                  <div className="tl__card tl__card--err">
                    <span className="tl__name">
                      <Warning size={12} /> {e.text}
                    </span>
                    {e.detail && <span className="tl__meta">{e.detail}</span>}
                    <span className="tl__actions">
                      <button className="btn btn--primary" onClick={() => void resumeTransfer(e.transferId!)}>
                        <Play size={11} /> Retry now
                      </button>
                      <button className="btn" onClick={() => void cancelTransfer(e.transferId!)}>
                        <Close size={11} /> Skip
                      </button>
                    </span>
                  </div>
                ) : (
                  <div className="tl__line">
                    {e.kind === "ok" && <Check size={12} className="tl__ico tl__ico--ok" />}
                    <span>{e.text}</span>
                    {e.detail && <span className="tl__detail">{e.detail}</span>}
                  </div>
                )}
              </div>
            </div>
          );
        })}

        {entries.length === 0 && active.length === 0 && queued.length === 0 && (
          <div className="tl__empty">Nothing yet — queue a transfer and the story starts here.</div>
        )}
      </div>
    </div>
  );
}
