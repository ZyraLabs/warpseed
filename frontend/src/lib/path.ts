/* One definition of "the same folder", shared by everything that compares
   paths. Comparing raw strings produces duplicate bookmarks and missed
   refreshes, because the same folder arrives spelled differently depending
   on whether the user typed it or the filesystem returned it. */
import type { PaneSource } from "../ipc";

/** Windows shapes: "D:", "D:\x", "D:/x", "\\server\share", "\Users\x". */
export const WIN_ABSOLUTE = /^([A-Za-z]:([\\/].*)?|\\\\[^\\]+.*|\\[^\\].*)$/;

export function looksWindows(p: string): boolean {
  return WIN_ABSOLUTE.test(p);
}

/** "D:" alone means the drive root, not the drive's current directory. */
export function normalizeTypedLocal(p: string): string {
  return /^[A-Za-z]:$/.test(p) ? p + "\\" : p;
}

/** Comparison form. Windows paths are case-insensitive; POSIX remotes are
    not, so case is only folded for the local filesystem. */
export function pathKey(p: string, source: PaneSource): string {
  const unified = p.replace(/\\/g, "/").replace(/\/+$/, "");
  return source === "local" ? unified.toLowerCase() : unified;
}

export function samePath(a: string, b: string, source: PaneSource): boolean {
  return pathKey(a, source) === pathKey(b, source);
}

/** True when `child` is at or below `parent`. */
export function isAtOrUnder(child: string, parent: string, source: PaneSource): boolean {
  const c = pathKey(child, source);
  const p = pathKey(parent, source);
  return c === p || c.startsWith(p + "/");
}

/** Keep a menu row readable: drop middle segments of a long path. */
export function shortenPath(p: string): string {
  if (p.length <= 44) return p;
  const parts = p.split(/[\\/]/).filter(Boolean);
  if (parts.length <= 2) return p;
  const sep = p.includes("\\") ? "\\" : "/";
  return `${parts[0]}${sep}…${sep}${parts.slice(-2).join(sep)}`;
}
