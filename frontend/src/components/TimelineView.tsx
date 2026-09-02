import { resumeTransfer, cancelTransfer, type Transfer } from "../ipc";
import { describeTransferError, formatDuration, formatSize } from "../lib/format";
import { baseName } from "../lib/path";
import { useUiStore } from "../store";
import { Check, Close, Play, Warning } from "./Icon";
import "../timeline.css";

/** What one finished run actually achieved. null when the row predates the
    run-timing columns, or when the run was too short to divide by — a bogus
    "812 MiB/s" from a 40ms rounding window is worse than no number. */
interface RunStats {
  bytes: number;
  ms: number;
  rate: number;
}

function runStats(t: Transfer): RunStats | null {
  if (!t.startedAt) return null;
  const from = timeOf(t.startedAt);
  const to = timeOf(t.updatedAt);
  if (from <= 0 || to <= from) return null;
  const ms = to - from;
  // A resumed transfer only moved what was left, so credit it with that.
  const bytes = t.size > 0 ? t.size - (t.startBytes ?? 0) : t.bytesDone;
  if (bytes <= 0 || ms < 1000) return null;
  return { bytes, ms, rate: bytes / (ms / 1000) };
}

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
        detail: (() => {
          const r = runStats(t);
          const size = t.size > 0 ? formatSize(t.size) : "";
          // Speed first: it is the number being looked for. The path is
          // still there, just no longer the loudest thing in the line.
          const speed = r ? `${formatDuration(r.ms)} · ${formatSize(r.rate)}/s` : "";
          return [size, speed, t.dst].filter(Boolean).join(" · ");
        })(),
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

  // Today's totals. Averaged over time SPENT TRANSFERRING, not wall-clock:
  // the gap between two transfers is not slowness, and dividing by it would
  // make a productive evening look terrible.
  const midnight = new Date(now).setHours(0, 0, 0, 0);
  let doneBytes = 0;
  let doneMs = 0;
  let best = 0;
  let doneCount = 0;
  let failCount = 0;
  for (const t of transfers) {
    if (timeOf(t.updatedAt) < midnight) continue;
    if (t.state === "failed") failCount++;
    if (t.state !== "completed") continue;
    doneCount++;
    const r = runStats(t);
    if (!r) continue;
    doneBytes += r.bytes;
    doneMs += r.ms;
    if (r.rate > best) best = r.rate;
  }
  const liveRate = active.reduce((n, t) => n + (progress[t.id]?.rate ?? 0), 0);
  const avg = doneMs > 0 ? doneBytes / (doneMs / 1000) : 0;
  const showSummary = doneCount > 0 || failCount > 0 || active.length > 0;

  let lastDay = "";

  return (
    <div className="tl" role="region" aria-label="Activity timeline">
      {showSummary && (
        <div className="tl__summary" aria-label="Today at a glance">
          <div className="tl__stat">
            <span className="tl__stat-k">{active.length > 0 ? "Now" : "Idle"}</span>
            <span className="tl__stat-v">
              {active.length > 0 ? `${formatSize(liveRate)}/s` : "—"}
            </span>
            <span className="tl__stat-s">
              {active.length > 0
                ? `${active.length} running${queued.length > 0 ? ` · ${queued.length} queued` : ""}`
                : queued.length > 0
                  ? `${queued.length} queued`
                  : "nothing running"}
            </span>
          </div>
          <div className="tl__stat">
            <span className="tl__stat-k">Moved today</span>
            <span className="tl__stat-v">{doneBytes > 0 ? formatSize(doneBytes) : "—"}</span>
            <span className="tl__stat-s">
              {doneCount} done{failCount > 0 ? ` · ${failCount} failed` : ""}
            </span>
          </div>
          <div className="tl__stat">
            <span className="tl__stat-k">Average</span>
            <span className="tl__stat-v">{avg > 0 ? `${formatSize(avg)}/s` : "—"}</span>
            <span className="tl__stat-s">
              {doneMs > 0 ? `over ${formatDuration(doneMs)} transferring` : "no finished runs"}
            </span>
          </div>
          <div className="tl__stat">
            <span className="tl__stat-k">Best</span>
            <span className="tl__stat-v">{best > 0 ? `${formatSize(best)}/s` : "—"}</span>
            <span className="tl__stat-s">fastest run today</span>
          </div>
        </div>
      )}
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
