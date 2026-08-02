/* Recently visited folders, per pane source, surviving restarts. Kept in
   localStorage rather than the queue database: it is disposable UI history,
   and reading it must never wait on a backend round trip. */
import type { PaneSource } from "../ipc";
import { pathKey } from "./path";

const KEY = "ws-recent-paths";
const MAX_PER_SOURCE = 5;

type Store = Record<string, string[]>;

function read(): Store {
  try {
    const raw = localStorage.getItem(KEY);
    return raw ? (JSON.parse(raw) as Store) : {};
  } catch {
    return {};
  }
}

function write(store: Store) {
  try {
    localStorage.setItem(KEY, JSON.stringify(store));
  } catch {
    // A full or unavailable storage must never break navigation.
  }
}

const bucket = (source: PaneSource) => (source === "local" ? "local" : `site:${source}`);

/** Most recent first, excluding the folder currently shown. */
export function recentPaths(source: PaneSource, exclude?: string): string[] {
  const all = read()[bucket(source)] ?? [];
  if (!exclude) return all;
  const skip = pathKey(exclude, source);
  return all.filter((p) => pathKey(p, source) !== skip);
}

export function rememberPath(source: PaneSource, path: string) {
  if (!path) return;
  const store = read();
  const key = bucket(source);
  const seen = pathKey(path, source);
  // Dedupe by path identity, not raw string: "D:\Media" and "d:/media" are
  // one folder and must not occupy two of the five slots.
  const rest = (store[key] ?? []).filter((p) => pathKey(p, source) !== seen);
  store[key] = [path, ...rest].slice(0, MAX_PER_SOURCE);
  write(store);
}

/** Drop a source's history. Called when a site is deleted: SQLite recycles
    rowids, so a new site could otherwise inherit another server's folders. */
export function forgetSource(source: PaneSource) {
  const store = read();
  delete store[bucket(source)];
  write(store);
}
