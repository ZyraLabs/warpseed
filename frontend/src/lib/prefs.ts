/* UI preferences — column widths, sort, recent folders.
   Stored in the settings database so a single backup captures them, with a
   localStorage mirror so startup can read them synchronously (no waiting on
   a binding, no flash of the wrong layout). Reads hit the mirror; writes go
   to both. */
import { getSettings, setSetting } from "../ipc";

export type PrefKey =
  | "ui.queue_columns"
  | "ui.pane_columns"
  | "ui.pane_sort"
  | "ui.recents";

const ALL: PrefKey[] = [
  "ui.queue_columns",
  "ui.pane_columns",
  "ui.pane_sort",
  "ui.recents",
];

/** Pre-database key names. Carried across once so an upgrade does not throw
    away a layout the user tuned. */
const LEGACY: Record<PrefKey, string> = {
  "ui.queue_columns": "ws-queue-columns",
  "ui.pane_columns": "ws-pane-columns",
  "ui.pane_sort": "ws-pane-sort",
  "ui.recents": "ws-recent-paths",
};

const cache = new Map<string, string>();

// Runs at import, before any component reads a preference — a migration
// that awaited anything would land after the hooks had already defaulted.
(function importLegacy() {
  try {
    for (const key of ALL) {
      if (localStorage.getItem(key) !== null) continue;
      const old = localStorage.getItem(LEGACY[key]);
      if (old !== null) {
        localStorage.setItem(key, old);
        localStorage.removeItem(LEGACY[key]);
      }
    }
  } catch {
    // No storage: the defaults are a fine place to start.
  }
})();

let hydrated = false;
const pending = new Set<PrefKey>();
const listeners = new Set<() => void>();

/** Subscribe to hydration, so a hook that read a cold mirror can pick up
    what the database actually held (including after a restore). */
export function onPrefsHydrated(cb: () => void): () => void {
  if (hydrated) {
    cb();
    return () => undefined;
  }
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function isHydrated(): boolean {
  return hydrated;
}

export function getPref(key: PrefKey): string | null {
  if (cache.has(key)) return cache.get(key) ?? null;
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function setPref(key: PrefKey, value: string) {
  cache.set(key, value);
  try {
    localStorage.setItem(key, value);
  } catch {
    // The database copy below is the durable one; a full mirror is fine.
  }
  if (!hydrated) {
    // Writing now would race hydration and could persist a default over the
    // stored value. Remember the intent and flush once we know what's there.
    pending.add(key);
    return;
  }
  void setSetting(key, value).catch(() => undefined);
}

/** Pull the stored preferences into the mirror at boot. Anything the
    database holds wins — unless the user already changed it in the moment
    before hydration, in which case their action wins and is flushed. */
export async function hydratePrefs(): Promise<Record<string, string>> {
  const cfg = await getSettings().catch(() => ({}) as Record<string, string>);
  for (const key of ALL) {
    if (pending.has(key)) continue;
    const value = cfg[key];
    if (value) {
      cache.set(key, value);
      try {
        localStorage.setItem(key, value);
      } catch {
        // mirror is best-effort
      }
    }
  }
  hydrated = true;

  for (const key of pending) {
    const value = cache.get(key);
    if (value !== undefined) void setSetting(key, value).catch(() => undefined);
  }
  pending.clear();

  for (const cb of listeners) cb();
  listeners.clear();
  return cfg;
}
