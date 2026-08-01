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

/** "2026-08-01T12:00:00Z" → "2026-08-01 12:00" */
export function formatTime(rfc3339: string): string {
  return rfc3339.replace("T", " ").replace(/:\d\dZ?$/, "");
}
