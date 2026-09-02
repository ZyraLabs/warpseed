export function formatSize(size: number): string {
  if (size < 0) return "";
  if (size < 1024) return `${size} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = size;
  let u = -1;
  do {
    v /= 1024;
    u++;
  } while (v >= 1024 && u < units.length - 1);
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[u]}`;
}

/** "2026-08-01T12:00:00Z" (both backends emit UTC) → local "2026-08-01 13:00",
    so the column agrees with what Explorer shows. Sorting is unaffected: the
    pane sorts the raw fixed-width UTC string, not this. Unparseable values fall
    back to string surgery rather than rendering "Invalid Date". */
export function formatTime(rfc3339: string): string {
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return rfc3339.replace("T", " ").replace(/:\d\dZ?$/, "");
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** Compact elapsed time: "8s", "4m 12s", "1h 06m". Seconds are dropped past
    an hour, where they are noise rather than information. */
export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

/** Translate the engine's raw error strings into plain language where the
    pattern is known; unknown errors pass through untouched. */
export function describeTransferError(err: string): string {
  const e = err.toLowerCase();
  if (e.includes("connection reset")) return "Connection was reset by the server — resume to retry.";
  if (e.includes("timed out") || e.includes("timeout"))
    return "The server stopped responding — resume to retry.";
  if (e.includes("permission denied")) return "The server refused access to this file (permission denied).";
  if (e.includes("no space")) return "The destination disk is full — free up space, then resume.";
  if (e.includes("no such file")) return "The file no longer exists on the server.";
  return err;
}
