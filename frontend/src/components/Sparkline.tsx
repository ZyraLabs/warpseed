import { useEffect, useRef } from "react";
import { formatSize } from "../lib/format";
import { useUiStore } from "../store";

const SAMPLES = 60; // one per second — a minute of history (ux-spec §8.4)

/** Status-bar throughput sparkline: aggregate transfer rate, drawn on a
    96×16 canvas from the same progress events feeding the queue dock. */
export default function Sparkline() {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const buf = useRef<number[]>(Array(SAMPLES).fill(0));
  const label = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    // Progress events arrive every ~250ms while bytes move, so a sample
    // older than this means the transfer has stalled — report zero rather
    // than a comforting flat line.
    const STALE_MS = 2500;
    const tick = () => {
      const { transfers, progress } = useUiStore.getState();
      const now = performance.now();
      let agg = 0;
      for (const t of transfers) {
        if (t.state !== "active") continue;
        const p = progress[t.id];
        if (p && now - p.at < STALE_MS) agg += p.rate;
      }
      const b = buf.current;
      b.push(agg);
      if (b.length > SAMPLES) b.shift();
      draw();
      if (label.current) {
        label.current.textContent = agg > 0 ? `${formatSize(agg)}/s` : "";
      }
    };

    const draw = () => {
      const canvas = canvasRef.current;
      if (!canvas) return;
      const ctx = canvas.getContext("2d");
      if (!ctx) return;
      const dpr = window.devicePixelRatio || 1;
      const w = 96;
      const h = 16;
      if (canvas.width !== w * dpr) {
        canvas.width = w * dpr;
        canvas.height = h * dpr;
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, h);

      const b = buf.current;
      const max = Math.max(...b, 1);
      const styles = getComputedStyle(document.documentElement);
      const accent = styles.getPropertyValue("--accent").trim() || "#c65a33";

      ctx.beginPath();
      for (let i = 0; i < b.length; i++) {
        const x = (i / (SAMPLES - 1)) * w;
        const y = h - 1.5 - (b[i] / max) * (h - 4);
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      }
      ctx.strokeStyle = accent;
      ctx.lineWidth = 1.5;
      ctx.stroke();
      // area fill under the line
      ctx.lineTo(w, h);
      ctx.lineTo(0, h);
      ctx.closePath();
      ctx.globalAlpha = 0.15;
      ctx.fillStyle = accent;
      ctx.fill();
      ctx.globalAlpha = 1;
    };

    const id = setInterval(tick, 1000);
    draw();
    return () => clearInterval(id);
  }, []);

  return (
    <span className="sparkline" title="Aggregate transfer speed, last 60s">
      <canvas ref={canvasRef} style={{ width: 96, height: 16 }} aria-hidden />
      <span ref={label} className="sparkline__label" />
    </span>
  );
}
