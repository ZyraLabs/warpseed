import { useEffect, useRef, useState } from "react";
import {
  on,
  transfersList,
  type ConnState,
  type TransferProgress,
  type TransferState,
} from "../ipc";
import { formatSize } from "../lib/format";
import { baseName } from "../lib/path";
import { useUiStore } from "../store";
import { Warning } from "./Icon";
import "../flight.css";

/* Flight mode (FlightMode board): giant aggregate speed, a site→PC flow
   diagram with one lane per active transfer, queued/log side rails and a
   session timeline strip. Display-only — all queue actions live in the
   dock; keybindings, focus and virtualization are untouched (ux-spec §1). */

// Progress events arrive ~every 250ms while bytes move; older samples mean
// the transfer stalled and must not keep contributing its last rate.
const STALE_MS = 2500;
const MAX_ACTIVE_LANES = 6;
const MAX_FAILED_LANES = 3;
const TIMELINE_SAMPLES = 120; // one per second — two minutes of history

// Flow diagram geometry (SVG user units; scales with the stage).
const W = 760;
const H = 330;
const PC = { x: 560, y: 90, w: 160, h: 150 };
const SITE_X = 40;
const SITE_W = 160;

function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

/** Cubic bezier coordinate at t, one axis. */
function bez(p0: number, p1: number, p2: number, p3: number, t: number): number {
  const u = 1 - t;
  return u * u * u * p0 + 3 * u * u * t * p1 + 3 * u * t * t * p2 + t * t * t * p3;
}

interface RateParts {
  value: string;
  unit: string;
}

/** formatSize's binary units, split so the numeral and unit can differ in size. */
function rateParts(bps: number): RateParts {
  if (bps < 1) return { value: "0", unit: "B/s" };
  const units = ["B/s", "KiB/s", "MiB/s", "GiB/s", "TiB/s"];
  let v = bps;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return { value: u === 0 ? String(Math.round(v)) : v.toFixed(v >= 100 ? 0 : 1), unit: units[u] };
}

function clock(atMs: number): string {
  const d = new Date(atMs);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

function retrySeconds(nextRetryAt: string | null, nowMs: number): number | null {
  if (!nextRetryAt) return null;
  const t = Date.parse(nextRetryAt);
  if (Number.isNaN(t)) return null;
  const s = Math.ceil((t - nowMs) / 1000);
  return s > 0 ? s : null;
}

interface SiteGeo {
  siteId: number;
  name: string;
  connState: string;
  activeCount: number;
  x: number;
  y: number;
  w: number;
  h: number;
}

interface LaneDot {
  x: number;
  y: number;
  r: number;
  o: number;
}

interface Lane {
  id: number;
  failed: boolean;
  d: string;
  opacity: number;
  dots: LaneDot[];
}

interface TimelineSample {
  v: number;
  err: boolean;
}

export default function FlightView() {
  const transfers = useUiStore((s) => s.transfers);
  const progress = useUiStore((s) => s.progress);
  const sites = useUiStore((s) => s.sites);
  const connStates = useUiStore((s) => s.connStates);
  const sessionLog = useUiStore((s) => s.sessionLog);
  const setTransfers = useUiStore((s) => s.setTransfers);
  const applyProgress = useUiStore((s) => s.applyProgress);
  const patchTransferState = useUiStore((s) => s.patchTransferState);
  const setConnState = useUiStore((s) => s.setConnState);
  const pushSessionEvent = useUiStore((s) => s.pushSessionEvent);

  // Ticked once per second by the timeline sampler so retry countdowns and
  // stall detection stay honest even when no progress events arrive.
  const [now, setNow] = useState(() => Date.now());

  // Same event wiring as QueueDock (the dock may or may not be mounted).
  useEffect(() => {
    const refresh = () => void transfersList().then(setTransfers).catch(() => undefined);
    refresh();
    const seenLanes = new Set<number>(); // ids already announced as multi-lane
    // Wails delivers events to earlier listeners first: the always-mounted
    // dock/App handlers have already written the NEW state into the store by
    // the time these run, so "did it change?" must be answered from local
    // snapshots, never from the store.
    const lastTransfer = new Map(useUiStore.getState().transfers.map((t) => [t.id, t.state]));
    const lastConn: Record<number, string> = { ...useUiStore.getState().connStates };
    const offChanged = on("queue:changed", refresh);
    const offProgress = on<TransferProgress>("transfer:progress", (p) => {
      // The dock may have applied this exact sample already; feeding the EMA
      // a second zero-delta sample microseconds later would decay the rate.
      const cur = useUiStore.getState().progress[p.id];
      if (!cur || cur.bytes !== p.bytes) applyProgress(p.id, p.bytes, p.size, p.chunks);
      if (p.chunks && p.chunks.length > 1 && !seenLanes.has(p.id)) {
        seenLanes.add(p.id);
        const t = useUiStore.getState().transfers.find((x) => x.id === p.id);
        pushSessionEvent(
          "info",
          `hyperlane ×${p.chunks.length} engaged — ${t ? baseName(t.src) : `#${p.id}`}`,
        );
      }
    });
    const offState = on<TransferState>("transfer:state", (s) => {
      const prevState = lastTransfer.get(s.id);
      lastTransfer.set(s.id, s.state);
      const t = useUiStore.getState().transfers.find((x) => x.id === s.id);
      const name = t ? baseName(t.src) : `transfer #${s.id}`;
      if (s.state === "completed") {
        pushSessionEvent("ok", `${name} completed${t && t.size > 0 ? ` · ${formatSize(t.size)}` : ""}`);
      } else if (s.state === "failed") {
        pushSessionEvent("err", `${name} failed${s.error ? ` — ${truncate(s.error, 80)}` : ""}`);
      } else if (s.state === "active" && prevState !== "active") {
        pushSessionEvent("info", `${name} in flight`);
      }
      patchTransferState(s.id, s.state, s.error);
    });
    const offConn = on<ConnState>("site:connstate", (c) => {
      const prev = lastConn[c.siteId];
      lastConn[c.siteId] = c.state;
      if (prev !== c.state && c.state !== "connecting") {
        const site = useUiStore.getState().sites.find((x) => x.id === c.siteId);
        pushSessionEvent(
          c.state === "error" ? "err" : "info",
          `${site?.name ?? `site ${c.siteId}`} ${c.state}`,
        );
      }
      setConnState(c.siteId, c.state);
    });
    return () => {
      offChanged();
      offProgress();
      offState();
      offConn();
    };
  }, [setTransfers, applyProgress, patchTransferState, setConnState, pushSessionEvent]);

  // Session timeline: same sampling approach as Sparkline (1s aggregate of
  // fresh active rates), self-contained buffer, drawn straight to canvas.
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const buf = useRef<TimelineSample[]>([]);
  useEffect(() => {
    const draw = () => {
      const canvas = canvasRef.current;
      if (!canvas) return;
      const ctx = canvas.getContext("2d");
      if (!ctx) return;
      const dpr = window.devicePixelRatio || 1;
      const w = canvas.clientWidth || 600;
      const h = canvas.clientHeight || 52;
      if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
        canvas.width = Math.round(w * dpr);
        canvas.height = Math.round(h * dpr);
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, h);
      const b = buf.current;
      if (b.length < 2) return;
      const max = Math.max(...b.map((s) => s.v), 1);
      const styles = getComputedStyle(document.documentElement);
      const accent = styles.getPropertyValue("--accent").trim() || "#c65a33";
      const err = styles.getPropertyValue("--state-error").trim() || "#c03d2f";
      const px = (i: number) => (i / (TIMELINE_SAMPLES - 1)) * w;
      const py = (v: number) => h - 2 - (v / max) * (h - 8);

      ctx.beginPath();
      b.forEach((s, i) => {
        if (i === 0) ctx.moveTo(px(i), py(s.v));
        else ctx.lineTo(px(i), py(s.v));
      });
      ctx.strokeStyle = accent;
      ctx.lineWidth = 2;
      ctx.lineJoin = "round";
      ctx.stroke();
      ctx.lineTo(px(b.length - 1), h);
      ctx.lineTo(0, h);
      ctx.closePath();
      ctx.globalAlpha = 0.14;
      ctx.fillStyle = accent;
      ctx.fill();
      ctx.globalAlpha = 1;

      ctx.fillStyle = err;
      b.forEach((s, i) => {
        if (!s.err) return;
        ctx.beginPath();
        ctx.arc(px(i), py(s.v), 2.5, 0, Math.PI * 2);
        ctx.fill();
      });
    };

    const tick = () => {
      const state = useUiStore.getState();
      const nowPerf = performance.now();
      let agg = 0;
      let anyFailed = false;
      for (const t of state.transfers) {
        if (t.state === "failed") anyFailed = true;
        if (t.state !== "active") continue;
        const p = state.progress[t.id];
        if (p && nowPerf - p.at < STALE_MS) agg += p.rate;
      }
      const b = buf.current;
      b.push({ v: agg, err: anyFailed });
      if (b.length > TIMELINE_SAMPLES) b.shift();
      draw();
      setNow(Date.now());
    };

    const id = setInterval(tick, 1000);
    draw();
    return () => clearInterval(id);
  }, []);

  // ── derive everything from the store; no duplicated state ──
  const nowPerf = performance.now();
  const live = transfers.map((t) => {
    const p = progress[t.id];
    const bytes = p && p.bytes > t.bytesDone ? p.bytes : t.bytesDone;
    const fresh = p !== undefined && nowPerf - p.at < STALE_MS;
    return {
      ...t,
      bytes,
      rate: t.state === "active" && p && fresh ? p.rate : 0,
      lanes: t.state === "active" ? p?.chunks?.length ?? 1 : 0,
    };
  });

  const active = live.filter((t) => t.state === "active");
  const failed = live.filter((t) => t.state === "failed");
  const queued = live.filter((t) => t.state === "pending" || t.state === "dispatched");
  const done = live.filter((t) => t.state === "completed");
  const aggRate = active.reduce((s, t) => s + t.rate, 0);
  const connCount = active.reduce((s, t) => s + t.lanes, 0);
  const doneBytes = done.reduce((s, t) => s + Math.max(t.size, 0), 0);
  const speed = rateParts(aggRate);

  // Sites on stage: connected, or referenced by work in flight. Cap at 3.
  const flowSiteIds = new Set<number>();
  for (const t of [...active, ...failed, ...queued]) flowSiteIds.add(t.siteId);
  for (const s of sites) {
    const cs = connStates[s.id];
    if (cs === "connected" || cs === "connecting") flowSiteIds.add(s.id);
  }
  const flowSites = sites.filter((s) => flowSiteIds.has(s.id)).slice(0, 3);

  const n = flowSites.length;
  const cardH = n <= 1 ? 150 : n === 2 ? 120 : 92;
  const stackTop = (H - (n * cardH + (n - 1) * 14)) / 2;
  const siteGeo: SiteGeo[] = flowSites.map((s, i) => ({
    siteId: s.id,
    name: s.name,
    connState: connStates[s.id] ?? "idle",
    activeCount: active.filter((t) => t.siteId === s.id).length,
    x: SITE_X,
    y: stackTop + i * (cardH + 14),
    w: SITE_W,
    h: cardH,
  }));

  // Lanes: one bezier per active transfer (top rate lanes first, up to 6),
  // plus failed transfers as dashed error lanes sagging below the flow.
  const laneTransfers = [
    ...[...active].sort((a, b) => b.rate - a.rate).slice(0, MAX_ACTIVE_LANES),
    ...failed.slice(0, MAX_FAILED_LANES),
  ].filter((t) => siteGeo.some((g) => g.siteId === t.siteId));
  const perSiteCount = new Map<number, number>();
  laneTransfers.forEach((t) =>
    perSiteCount.set(t.siteId, (perSiteCount.get(t.siteId) ?? 0) + 1),
  );
  const perSiteIdx = new Map<number, number>();
  const m = laneTransfers.length;
  const entryY = (i: number) => (m === 1 ? 165 : 108 + (i * 116) / (m - 1));

  const lanes: Lane[] = laneTransfers.flatMap((t, i) => {
    const g = siteGeo.find((x) => x.siteId === t.siteId);
    if (!g) return [];
    const isFailed = t.state === "failed";
    const k = perSiteIdx.get(t.siteId) ?? 0;
    perSiteIdx.set(t.siteId, k + 1);
    const c = perSiteCount.get(t.siteId) ?? 1;
    const spread = Math.min(20, (g.h - 24) / Math.max(c, 1));
    const x0 = g.x + g.w;
    const y0 = g.y + g.h / 2 + (k - (c - 1) / 2) * spread;
    const x1 = PC.x;
    const y1 = entryY(i);
    const lift = isFailed ? -34 : 34 - i * 14;
    const c1x = x0 + 140;
    const c1y = y0 - lift;
    const c2x = x1 - 140;
    const c2y = y1 - lift;
    const share = aggRate > 0 ? t.rate / aggRate : 0;
    const frac = t.size > 0 ? Math.min(Math.max(t.bytes / t.size, 0), 1) : 0;
    // Packet dots ride the lane at the completion fraction; uploads travel
    // the other way, so their position is measured from the PC end.
    const dots: LaneDot[] = isFailed
      ? []
      : [frac, frac - 0.1]
          .filter((f) => f > 0.01 && f < 0.99)
          .map((f, di) => {
            const tt = t.direction === "upload" ? 1 - f : f;
            return {
              x: bez(x0, c1x, c2x, x1, tt),
              y: bez(y0, c1y, c2y, y1, tt),
              r: di === 0 ? 4.5 + 2 * share : 3.5,
              o: di === 0 ? 0.95 : 0.4,
            };
          });
    return [
      {
        id: t.id,
        failed: isFailed,
        d: `M${x0} ${y0} C ${c1x} ${c1y}, ${c2x} ${c2y}, ${x1} ${y1}`,
        opacity: isFailed ? 0.8 : 0.3 + 0.6 * share,
        dots,
      },
    ];
  });

  const chips = [
    ...[...active].sort((a, b) => b.rate - a.rate).slice(0, 6),
    ...failed.slice(0, 4),
  ];

  const reticleColor = (cs: string): string =>
    cs === "connected"
      ? "var(--accent)"
      : cs === "connecting"
        ? "var(--state-paused)"
        : cs === "error"
          ? "var(--state-error)"
          : "var(--text-faint)";

  return (
    <div className="flight">
      <div className="flight__body">
        <aside className="flight__rail" aria-label="Queued transfers">
          <div className="flight__rail-head">Queued</div>
          <div className="flight__rail-body">
            {queued.length === 0 ? (
              <div className="flight__rail-empty">Nothing waiting — mark files and press F5</div>
            ) : (
              queued.map((t) => (
                <div key={t.id} className="qrow" title={`${t.src} → ${t.dst}`}>
                  <span className="qrow__name">{baseName(t.src)}</span>
                  <span className="qrow__size">{t.size > 0 ? formatSize(t.size) : "—"}</span>
                </div>
              ))
            )}
          </div>
          <div className={`flight__rail-foot ${queued.length > 0 ? "flight__rail-foot--armed" : ""}`}>
            {queued.length > 0 ? `${queued.length} armed for transfer →` : "queue empty"}
          </div>
        </aside>

        <section className="flight__stage" aria-label="Flight overview">
          <span className="flight__speed">
            {speed.value}
            <span className="flight__speed-unit"> {speed.unit}</span>
          </span>
          <span className="flight__sub">
            {active.length > 0
              ? `Aggregate · ≈${connCount} connections · ${active.length} in flight`
              : `Standby · ${queued.length} queued`}
          </span>

          {flowSites.length === 0 ? (
            <div className="flight__empty">
              No sites connected — connect a site and queue files to see them fly.
            </div>
          ) : (
            <svg
              className="flight__svg"
              viewBox={`0 0 ${W} ${H}`}
              fill="none"
              preserveAspectRatio="xMidYMid meet"
              role="img"
              aria-label={`${active.length} transfers in flight from ${flowSites.length} site${flowSites.length === 1 ? "" : "s"}`}
            >
              {lanes.map((l) => (
                <g key={l.id}>
                  <path
                    d={l.d}
                    stroke={l.failed ? "var(--state-error)" : "var(--accent)"}
                    strokeWidth={l.failed ? 2.5 : 3}
                    strokeLinecap="round"
                    strokeDasharray={l.failed ? "2 8" : undefined}
                    opacity={l.opacity}
                  />
                  {l.dots.map((d, di) => (
                    <circle key={di} cx={d.x} cy={d.y} r={d.r} fill="var(--accent)" opacity={d.o} />
                  ))}
                </g>
              ))}

              {siteGeo.map((g) => {
                const cx = g.x + g.w / 2;
                const rc = reticleColor(g.connState);
                const compact = g.h < 110;
                const ry = g.y + (compact ? g.h / 2 - 12 : 42);
                return (
                  <g key={g.siteId}>
                    <rect
                      x={g.x}
                      y={g.y}
                      width={g.w}
                      height={g.h}
                      rx={14}
                      fill="var(--surface-1)"
                      stroke="var(--border)"
                      strokeWidth={1.5}
                    />
                    <circle cx={cx} cy={ry} r={compact ? 14 : 22} stroke={rc} strokeWidth={2.5} />
                    <circle cx={cx} cy={ry} r={compact ? 6 : 9} fill={rc} opacity={0.25} />
                    <circle cx={cx} cy={ry} r={compact ? 2.5 : 4} fill={rc} />
                    <text x={cx} y={g.y + g.h - (compact ? 22 : 37)} textAnchor="middle" className="fnode-name">
                      {truncate(g.name, 16).toUpperCase()}
                    </text>
                    <text x={cx} y={g.y + g.h - (compact ? 9 : 18)} textAnchor="middle" className="fnode-sub">
                      {`${g.activeCount} IN FLIGHT · ${g.connState.toUpperCase()}`}
                    </text>
                  </g>
                );
              })}

              <rect
                x={PC.x}
                y={PC.y}
                width={PC.w}
                height={PC.h}
                rx={14}
                fill="var(--surface-1)"
                stroke="var(--border)"
                strokeWidth={1.5}
              />
              <rect
                x={PC.x + 40}
                y={PC.y + 22}
                width={80}
                height={46}
                rx={8}
                stroke="var(--text-dim)"
                strokeWidth={2}
              />
              <path
                d={`M${PC.x + 54} ${PC.y + 80} H${PC.x + 106}`}
                stroke="var(--text-dim)"
                strokeWidth={2}
                strokeLinecap="round"
              />
              <text x={PC.x + PC.w / 2} y={PC.y + PC.h - 37} textAnchor="middle" className="fnode-name">
                THIS PC
              </text>
              <text x={PC.x + PC.w / 2} y={PC.y + PC.h - 18} textAnchor="middle" className="fnode-sub">
                {done.length > 0
                  ? `${done.length} LANDED · ${formatSize(doneBytes).toUpperCase()}`
                  : "AWAITING DATA"}
              </text>
            </svg>
          )}

          {chips.length > 0 && (
            <div className="flight__chips">
              {chips.map((t) => {
                if (t.state === "failed") {
                  const secs = retrySeconds(t.nextRetryAt, now);
                  const meta = secs !== null ? `retry in ${secs} s` : truncate(t.error ?? "failed", 36);
                  return (
                    <span key={t.id} className="fchip fchip--err" title={t.error ?? undefined}>
                      <Warning size={11} />
                      <span className="fchip__name">{baseName(t.src)}</span>
                      <span className="fchip__meta">{meta}</span>
                    </span>
                  );
                }
                const pct = t.size > 0 ? Math.floor(Math.min(t.bytes / t.size, 1) * 100) : 0;
                const share = aggRate > 0 ? t.rate / aggRate : 0;
                return (
                  <span key={t.id} className="fchip" title={`${t.src} → ${t.dst}`}>
                    <span className="fchip__dot" style={{ opacity: 0.4 + 0.6 * share }} />
                    <span className="fchip__name">{baseName(t.src)}</span>
                    <span className="fchip__meta">
                      {pct}% · {formatSize(t.rate)}/s
                    </span>
                  </span>
                );
              })}
            </div>
          )}
        </section>

        <aside className="flight__rail" aria-label="Session log">
          <div className="flight__rail-head">Session Log</div>
          <div className="flight__rail-body">
            {sessionLog.length === 0 ? (
              <div className="flight__rail-empty">Quiet so far — events land here as they happen</div>
            ) : (
              sessionLog.map((e, i) => (
                <div key={`${e.at}-${i}`} className={`lrow lrow--${e.kind}`}>
                  <span className="lrow__time">{clock(e.at)}</span>
                  <span className="lrow__text">{e.text}</span>
                </div>
              ))
            )}
          </div>
          <div className="flight__rail-foot">
            Session: <strong>{formatSize(doneBytes) || "0 B"}</strong> · {done.length}{" "}
            {done.length === 1 ? "file" : "files"}
          </div>
        </aside>
      </div>

      <div className="flight__timeline" aria-label="Session timeline, last two minutes">
        <span className="flight__timeline-label">
          Session
          <br />
          Timeline
        </span>
        <canvas ref={canvasRef} aria-hidden />
        <span className="flight__timeline-scale">-2 min</span>
      </div>
    </div>
  );
}
