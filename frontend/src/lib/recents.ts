/* Recently visited folders, per pane source, surviving restarts. Kept in
   localStorage rather than the queue database: it is disposable UI history,
   and reading it must never wait on a backend round trip. */
import type { PaneSource } from "../ipc";

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

const bucket = (source: PaneSource) => (source === "local" ? "local" : `site:${source}`);

/** Most recent first, excluding the folder currently shown. */
export function recentPaths(source: PaneSource, exclude?: string): string[] {
  const all = read()[bucket(source)] ?? [];
  return exclude ? all.filter((p) => p !== exclude) : all;
}

export function rememberPath(source: PaneSource, path: string) {
  if (!path) return;
  const store = read();
  const key = bucket(source);
  const next = [path, ...(store[key] ?? []).filter((p) => p !== path)].slice(0, MAX_PER_SOURCE);
  store[key] = next;
  try {
    localStorage.setItem(KEY, JSON.stringify(store));
  } catch {
    // A full or unavailable storage must never break navigation.
  }
}
