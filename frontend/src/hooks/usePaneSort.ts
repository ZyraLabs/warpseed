import { useCallback, useState } from "react";
import type { FsEntry } from "../ipc";

export type SortKey = "name" | "size" | "modTime";
export interface SortState {
  key: SortKey;
  desc: boolean;
}

const KEY = "ws-pane-sort";
const DEFAULT: SortState = { key: "name", desc: false };

function load(): SortState {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return DEFAULT;
    const s = JSON.parse(raw) as SortState;
    return s.key === "name" || s.key === "size" || s.key === "modTime" ? s : DEFAULT;
  } catch {
    return DEFAULT;
  }
}

/** Listing sort, shared by both panes and remembered across restarts. */
export function usePaneSort() {
  const [sort, setSort] = useState<SortState>(load);

  const toggle = useCallback((key: SortKey) => {
    setSort((prev) => {
      // Same column flips direction; a new column starts ascending, except
      // size and date, where "biggest/newest first" is what you want.
      const next: SortState =
        prev.key === key ? { key, desc: !prev.desc } : { key, desc: key !== "name" };
      try {
        localStorage.setItem(KEY, JSON.stringify(next));
      } catch {
        // A saved sort is a convenience, never a hard requirement.
      }
      return next;
    });
  }, []);

  return { sort, toggle };
}

/** Directories always lead, whatever the sort — a folder is a place, not a
    file, and burying them under a size sort makes navigation worse. */
export function sortEntries(entries: FsEntry[], sort: SortState): FsEntry[] {
  const dir = sort.desc ? -1 : 1;
  const compare = (a: FsEntry, b: FsEntry): number => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    switch (sort.key) {
      case "size": {
        // Folders carry no size; keep them in name order among themselves.
        if (a.isDir && b.isDir) return a.name.localeCompare(b.name, undefined, { numeric: true });
        return (a.size - b.size) * dir;
      }
      case "modTime":
        return a.modTime.localeCompare(b.modTime) * dir;
      default:
        return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" }) * dir;
    }
  };
  return [...entries].sort(compare);
}
