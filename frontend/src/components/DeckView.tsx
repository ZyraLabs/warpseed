import { useEffect, useRef, useState } from "react";
import {
  cancelTransfer,
  diskSpace,
  localHome,
  resumeTransfer,
  transfersList,
  type DiskSpace,
} from "../ipc";
import { describeTransferError, formatSize } from "../lib/format";
import { baseName } from "../lib/path";
import { useUiStore } from "../store";
import { Check, ChevronRight, Play, Warning } from "./Icon";
import "../deck.css";

/** Samples of aggregate rate kept for the throughput chart (1/s ≈ 15 min). */
const CHART_SAMPLES = 900;

/** Directory part of a transfer destination (either separator). */
function dirName(p: string): string {
  const i = Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\"));
  // Keep the separator: "C:" without it means "current dir on C", not the
  // drive root, and a bare "/" must survive for root-level destinations.
  return i >= 0 ? p.slice(0, i + 1) : p;
}

function etaText(bytes: number, size: number, rate: number): string {
  if (rate <= 0 || size <= 0 || bytes >= size) return "";
  const s = Math.round((size - bytes) / rate);
  if (s < 60) return `${s} s remaining`;
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")} remaining`;
}

/** Bento home view (Deck): the night's work at a glance. Reads the store
    the always-mounted QueueDock keeps fresh; owns only its own sampling. */
export default function DeckView() {
  const transfers = useUiStore((s) => s.transfers);
  const progress = useUiStore((s) => s.progress);
  const sites = useUiStore((s) => s.sites);

  const [disk, setDisk] = useState<DiskSpace | null>(null);

  // One refresh on mount so the view is honest even if the queue was
  // changed while a dialog swallowed events; the dock keeps it live after.
  useEffect(() => {
    void transfersList()
      .then(useUiStore.getState().setTransfers)
      .catch(() => undefined);
  }, []);

  const live = transfers.map((t) => {
    const p = progress[t.id];
    const bytes = p && p.bytes > t.bytesDone ? p.bytes : t.bytesDone;
    return {
      ...t,
      bytes,
      rate: t.state === "active" ? p?.rate ?? 0 : 0,
      chunks: t.state === "active" ? p?.chunks : undefined,
    };
  });
  const active = live.filter((t) => t.state === "active").sort((a, b) => b.rate - a.rate);
  const queued = live.filter((t) => t.state === "pending" || t.state === "dispatched");
  const failed = live.filter((t) => t.state === "failed");
  const done = live.filter((t) => t.state === "completed");
  const aggRate = active.reduce((s, t) => s + t.rate, 0);
  const remaining = live
    .filter((t) => !["completed", "cancelled"].includes(t.state) && t.direction !== "upload")
    .reduce((s, t) => s + Math.max(t.size - t.bytes, 0), 0);
  const doneBytes = done.reduce((s, t) => s + Math.max(t.size, 0), 0);

  const hero = active[0];
  const heroPct = hero && hero.size > 0 ? Math.min(hero.bytes / hero.size, 1) : 0;
  const heroSite = hero ? sites.find((s) => s.id === hero.siteId)?.name ?? `site ${hero.siteId}` : "";

  // Destination gauge: the volume transfers are actually landing on.
  // Only downloads have a LOCAL dst — an upload's dst is a remote path and
  // must feed neither the gauge nor the capacity estimate.
  const isDownload = (t: { direction: string }) => t.direction !== "upload";
  const destPath =
    active.find(isDownload)?.dst ?? queued.find(isDownload)?.dst ?? done.find(isDownload)?.dst ?? null;
  useEffect(() => {
    let stale = false;
    const probe = () => {
      const target = destPath ? dirName(destPath) : null;
      const p = target ? diskSpace(target) : localHome().then(diskSpace);
      void p.then((d) => !stale && setDisk(d)).catch(() => !stale && setDisk(null));
    };
    probe();
    const iv = window.setInterval(probe, 30_000);
    return () => {
      stale = true;
      window.clearInterval(iv);
    };
  }, [destPath]);

  // Throughput chart: 1 s aggregate-rate samples, drawn straight to canvas
  // (same approach as Sparkline/FlightView — no react churn per sample).
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const buf = useRef<number[]>([]);
  const statsRef = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    const sample = () => {
      const st = useUiStore.getState();
      let rate = 0;
      for (const t of st.transfers) {
        if (t.state === "active") rate += st.progress[t.id]?.rate ?? 0;
      }
      const b = buf.current;
      b.push(rate);
      if (b.length > CHART_SAMPLES) b.shift();
      // DOM write, not state: a 1 Hz setState would re-render every card
      // just to move two numbers.
      if (statsRef.current) {
        const peak = Math.max(...b);
        const avg = b.reduce((s, v) => s + v, 0) / b.length;
        statsRef.current.textContent = `peak ${formatSize(peak)}/s · avg ${formatSize(avg)}/s`;
      }
      draw();
    };
    const draw = () => {
      const canvas = canvasRef.current;
      const ctx = canvas?.getContext("2d");
      if (!canvas || !ctx) return;
      const dpr = window.devicePixelRatio || 1;
      const w = canvas.clientWidth || 300;
      const h = canvas.clientHeight || 64;
      if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
        canvas.width = Math.round(w * dpr);
        canvas.height = Math.round(h * dpr);
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, h);
      const b = buf.current;
      if (b.length < 2) return;
      const max = Math.max(...b, 1);
      const accent =
        getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() || "#c65a33";
      const px = (i: number) => (i / (CHART_SAMPLES - 1)) * w;
      const py = (v: number) => h - 2 - (v / max) * (h - 6);
      ctx.beginPath();
      ctx.moveTo(px(0), h);
      for (let i = 0; i < b.length; i++) ctx.lineTo(px(i), py(b[i]));
      ctx.lineTo(px(b.length - 1), h);
      ctx.closePath();
      ctx.globalAlpha = 0.15;
      ctx.fillStyle = accent;
      ctx.fill();
      ctx.globalAlpha = 1;
      ctx.beginPath();
      for (let i = 0; i < b.length; i++) {
        if (i === 0) ctx.moveTo(px(i), py(b[i]));
        else ctx.lineTo(px(i), py(b[i]));
      }
      ctx.strokeStyle = accent;
      ctx.lineWidth = 2;
      ctx.lineJoin = "round";
      ctx.stroke();
    };
    sample();
    const iv = window.setInterval(sample, 1000);
    return () => window.clearInterval(iv);
  }, []);

  const ringOffset = 264 - 264 * heroPct;
  const fits = disk ? remaining <= disk.free : true;

  return (
    <div className="deck" role="region" aria-label="Deck overview">
      <div className="dcard dcard--hero">
        {hero ? (
          <>
            <svg className="dhero__ring" viewBox="0 0 100 100" aria-hidden="true">
              <circle className="dhero__track" cx="50" cy="50" r="42" />
              <circle
                className="dhero__arc"
                cx="50"
                cy="50"
                r="42"
                strokeDasharray="264"
                strokeDashoffset={ringOffset}
                transform="rotate(-90 50 50)"
              />
              <text className="dhero__pct" x="50" y="47" textAnchor="middle">
                {Math.floor(heroPct * 100)}%
              </text>
              <text className="dhero__lanes" x="50" y="64" textAnchor="middle">
                {hero.chunks && hero.chunks.length > 1 ? `${hero.chunks.length} lanes` : "1 lane"}
              </text>
            </svg>
            <div className="dhero__meta">
              <span className="dcard__label">
                Now transferring · {active.length} of {active.length + queued.length}
              </span>
              <span className="dhero__name" title={hero.src}>
                {baseName(hero.src)}
              </span>
              <span className="dhero__route">
                {heroSite} → {dirName(hero.dst)} · {etaText(hero.bytes, hero.size, hero.rate)}
              </span>
              {hero.chunks && hero.chunks.length > 1 ? (
                <span className="dhero__lanesbar">
                  {hero.chunks.map((f, i) => (
                    <span key={i} className="dhero__lane">
                      <span style={{ transform: `scaleX(${Math.min(Math.max(f, 0), 1)})` }} />
                    </span>
                  ))}
                </span>
              ) : (
                <span className="dhero__lanesbar">
                  <span className="dhero__lane">
                    <span style={{ transform: `scaleX(${heroPct})` }} />
                  </span>
                </span>
              )}
            </div>
            <span className="dhero__rate">{formatSize(hero.rate)}/s</span>
          </>
        ) : (
          <div className="dhero__idle">
            <span className="dcard__label">Nothing in flight</span>
            <span className="dhero__name">
              {queued.length > 0
                ? `${queued.length} queued and waiting for a slot`
                : "Queue something from Browse and it lands here"}
            </span>
            <span className="dhero__route">
              Session so far: {done.length} file{done.length === 1 ? "" : "s"} ·{" "}
              {formatSize(doneBytes)}
            </span>
          </div>
        )}
      </div>

      <div className="dcard">
        <span className="dcard__label dcard__label--accent">
          Up next · {queued.length}
        </span>
        {queued.length === 0 ? (
          <p className="dcard__note">Queue is clear.</p>
        ) : (
          <div className="dlist">
            {queued.slice(0, 5).map((t) => (
              <span key={t.id} className="dlist__row">
                <ChevronRight size={12} className="dlist__ico" />
                <span className="dlist__name" title={t.src}>
                  {baseName(t.src)}
                </span>
                <span className="dlist__size">{t.size > 0 ? formatSize(t.size) : "—"}</span>
              </span>
            ))}
            {queued.length > 5 && (
              <span className="dcard__note">+ {queued.length - 5} more in the queue</span>
            )}
          </div>
        )}
      </div>

      <div className="dcard">
        <span className="dcard__label">Throughput · this session</span>
        <span className="dcard__big">
          {formatSize(aggRate)}<span className="dcard__unit">/s</span>
        </span>
        <canvas ref={canvasRef} className="dchart" aria-label="Aggregate speed chart" />
        <span className="dcard__note" ref={statsRef}>
          measuring…
        </span>
      </div>

      <div className={`dcard${failed.length ? " dcard--warn" : ""}`}>
        <span className={`dcard__label${failed.length ? " dcard__label--err" : ""}`}>
          {failed.length ? (
            <>
              <Warning size={12} /> Needs attention · {failed.length}
            </>
          ) : (
            <>
              <Check size={12} /> Nothing needs attention
            </>
          )}
        </span>
        {failed.length === 0 ? (
          <p className="dcard__note">Failures land here with a one-click retry.</p>
        ) : (
          <div className="dlist">
            {failed.slice(0, 3).map((t) => (
              <div key={t.id} className="dfail">
                <span className="dlist__name" title={t.src}>
                  {baseName(t.src)}
                </span>
                <p className="dfail__why">{describeTransferError(t.error ?? "failed")}</p>
                <span className="dfail__actions">
                  <button className="btn btn--primary" onClick={() => void resumeTransfer(t.id)}>
                    <Play size={11} /> Retry now
                  </button>
                  <button className="btn" onClick={() => void cancelTransfer(t.id)}>
                    Skip
                  </button>
                </span>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="dcard">
        <span className="dcard__label">Destination drive</span>
        {disk ? (
          <>
            <span className="dcard__big">
              {formatSize(disk.free)}
              <span className="dcard__unit"> free of {formatSize(disk.total)}</span>
            </span>
            <span className="dgauge">
              <span
                className="dgauge__fill"
                style={{ width: `${Math.min(((disk.total - disk.free) / Math.max(disk.total, 1)) * 100, 100)}%` }}
              />
            </span>
            <span className={`dcard__note${fits ? "" : " dcard__note--err"}`}>
              {remaining > 0
                ? fits
                  ? `In-flight and queued work will add ~${formatSize(remaining)} · fits comfortably`
                  : `Queued work needs ~${formatSize(remaining)} — more than is free`
                : "Nothing queued against this drive"}
            </span>
          </>
        ) : (
          <p className="dcard__note">Free-space information is not available.</p>
        )}
        <span className="dcard__note">
          Session: {done.length} completed · {formatSize(doneBytes)} · {failed.length} failed
        </span>
      </div>
    </div>
  );
}
