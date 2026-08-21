import { setMiniMode as ipcSetMiniMode } from "../ipc";
import { formatSize } from "../lib/format";
import { baseName } from "../lib/path";
import { useUiStore } from "../store";
import { Expand, Slipstream } from "./Icon";
import "../mini.css";

/** Ambient pill: what the window becomes in mini mode — glanceable transfer
    state in an always-on-top strip, one click back to the full app. */
export default function MiniView() {
  const transfers = useUiStore((s) => s.transfers);
  const progress = useUiStore((s) => s.progress);
  const setMiniMode = useUiStore((s) => s.setMiniMode);

  const active = transfers.filter((t) => t.state === "active");
  const failed = transfers.filter((t) => t.state === "failed").length;
  const queuedCount = transfers.filter(
    (t) => t.state === "pending" || t.state === "dispatched",
  ).length;
  const aggRate = active.reduce((s, t) => s + (progress[t.id]?.rate ?? 0), 0);

  const top = active
    .map((t) => {
      const p = progress[t.id];
      const bytes = p && p.bytes > t.bytesDone ? p.bytes : t.bytesDone;
      return { ...t, pct: t.size > 0 ? Math.min(bytes / t.size, 1) : 0, chunks: p?.chunks };
    })
    .sort((a, b) => (progress[b.id]?.rate ?? 0) - (progress[a.id]?.rate ?? 0))[0];

  const restore = () => {
    // Full UI first, always — a stuck pill is worse than a window the user
    // has to resize by hand after a failed restore call.
    setMiniMode(false);
    void ipcSetMiniMode(false).catch(() =>
      window.dispatchEvent(
        new CustomEvent("ws:toast", {
          detail: { kind: "error", text: "Could not restore the window size" },
        }),
      ),
    );
  };

  return (
    <div className="mini">
      <Slipstream size={16} className="mini__glyph" />
      <div className="mini__body">
        {top ? (
          <>
            <span className="mini__name" title={top.src}>
              {baseName(top.src)}
            </span>
            <span className="mini__lanes">
              {(top.chunks && top.chunks.length > 1 ? top.chunks : [top.pct]).map((f, i) => (
                <span key={i} className="mini__lane">
                  <span style={{ transform: `scaleX(${Math.min(Math.max(f, 0), 1)})` }} />
                </span>
              ))}
            </span>
          </>
        ) : (
          <span className="mini__name mini__name--idle">
            {queuedCount > 0 ? `${queuedCount} queued — waiting for a slot` : "warpseed · idle"}
          </span>
        )}
      </div>
      <span className="mini__rate">{aggRate > 0 ? `${formatSize(aggRate)}/s` : ""}</span>
      {failed > 0 && <span className="mini__err" title={`${failed} failed`} />}
      <button className="mini__restore" onClick={restore} title="Back to warpseed" aria-label="Restore window">
        <Expand size={13} />
      </button>
    </div>
  );
}
