import { useCallback, useEffect, useState } from "react";
import type { FsEntry } from "../ipc";
import { getPref, onPrefsHydrated, setPref } from "../lib/prefs";

export type SortKey = "name" | "size" | "modTime";
export interface SortState {
  key: SortKey;
  desc: boolean;
}

const KEY = "ui.pane_sort" as const;
const DEFAULT: SortState = { key: "name", desc: false };

function load(): SortState {
  try {
    const raw = getPref(KEY);
    if (!raw) return DEFAULT;
    const s = JSON.parse(raw) as SortState;
    return s.key === "name" || s.key === "size" || s.key === "modTime" ? s : DEFAULT;
  } catch {
    return DEFAULT;
  }
}

// One value for every mounted pane. Both panes stay mounted (App.tsx hides
// rather than unmounts), so per-instance state let them drift apart until a
// restart made them agree again.
let current: SortState = load();
const subs = new Set<(s: SortState) => void>();
let touched = false;
let hydrationHooked = false;

function publish(next: SortState) {
  current = next;
  for (const cb of subs) cb(next);
}

/** Listing sort, shared by both panes (one module-level value, every mounted
    pane subscribes) and remembered across restarts. */
export function usePaneSort() {
  const [sort, setSort] = useState<SortState>(current);

  useEffect(() => {
    subs.add(setSort);
    setSort(current); // catch a change made before this mount
    if (!hydrationHooked) {
      hydrationHooked = true;
      // Adopt the stored sort once hydration lands, unless the user already
      // clicked a heading. Registered once at module scope so two mounted
      // panes cannot install two competing listeners.
      onPrefsHydrated(() => {
        if (!touched) publish(load());
      });
    }
    return () => {
      subs.delete(setSort);
    };
  }, []);

  const toggle = useCallback((key: SortKey) => {
    touched = true;
    const prev = current;
    // Same column flips direction; a new column starts ascending, except
    // size and date, where "biggest/newest first" is what you want.
    const next: SortState =
      prev.key === key ? { key, desc: !prev.desc } : { key, desc: key !== "name" };
    setPref(KEY, JSON.stringify(next));
    publish(next);
  }, []);

  return { sort, toggle };
}

// Hoisted: rebuilding collation per comparison allocates once per compare on
// listings that reach 20k entries.
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "variant" });

/** Directories always lead, whatever the sort — a folder is a place, not a
    file, and burying them under a size sort makes navigation worse. This
    matches Windows Explorer, which pins folders above files in BOTH
    directions; do not "fix" it. */
export function sortEntries(entries: FsEntry[], sort: SortState): FsEntry[] {
  const dir = sort.desc ? -1 : 1;
  const byName = (a: FsEntry, b: FsEntry) => collator.compare(a.name, b.name);
  const compare = (a: FsEntry, b: FsEntry): number => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    switch (sort.key) {
      case "size": {
        // Folders carry no size; order them by name among themselves, but
        // still honour the direction — without it a folder-only listing (a
        // seedbox root) is byte-identical before and after the click and the
        // sort reads as broken.
        if (a.isDir && b.isDir) return byName(a, b) * dir;
        const c = a.size - b.size;
        return c !== 0 ? (c < 0 ? -1 : 1) * dir : byName(a, b);
      }
      case "modTime": {
        // Both backends emit a fixed-width UTC layout, so a plain string
        // comparison is chronologically correct and far cheaper than
        // localeCompare.
        const c = a.modTime < b.modTime ? -1 : a.modTime > b.modTime ? 1 : 0;
        return c !== 0 ? c * dir : byName(a, b);
      }
      default:
        return byName(a, b) * dir;
    }
  };
  return [...entries].sort(compare);
}
