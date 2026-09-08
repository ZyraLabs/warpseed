import { type PaneSource } from "../ipc";
import { isAtOrUnder, pathKey } from "./path";

/** One folder in the tree sidebar. */
export interface TreeNode {
  path: string;
  label: string;
}

/* Folder-tree memory, held for the life of the app run.
 *
 * It lives outside React on purpose. The tree used to keep children and the
 * expanded flag in per-node component state, which cost three re-listings:
 * collapsing a folder re-listed it on the next open; collapsing a PARENT
 * unmounted every descendant, so a whole opened branch had to be re-listed one
 * folder at a time; and closing the sidebar threw the entire tree away. On a
 * remote site every one of those is a network round trip on the single browse
 * connection.
 *
 * It is deliberately NOT persisted. A folder tree is a picture of something
 * that changes underneath us, and a stale one restored from disk would be
 * worse than an empty one.
 */

interface Entry {
  source: PaneSource;
  path: string;
  nodes: TreeNode[];
}

const entries = new Map<string, Entry>();
/* Expansion is scoped to the PANE as well as the source. Children are shared
   — the same folder on the same server holds the same subfolders, and sharing
   them is the whole saving — but which branches are open is one pane's idea,
   not the app's. Both panes start on "local", so a source-only key made the
   right sidebar mirror every expand in the left one and fire a duplicate
   listing for it, which is the opposite of what two panes are for. */
const expanded = new Map<string, { pane: PaneKey; source: PaneSource; path: string }>();
/* Folders whose listing failed. Held separately from an empty result so the
   two cannot be confused: caching a failure as "no subfolders" makes one
   network blip look like an empty folder, and not recording it at all makes
   an unreadable folder re-list on every mount. */
const failed = new Map<string, { source: PaneSource; path: string }>();
const listeners = new Set<() => void>();
let version = 0;

/** Bound on remembered folders. Each entry is a short list of names, so this
    is cheap — but an unbounded map on an app left running overnight is a leak. */
const MAX_ENTRIES = 500;

/** Which sidebar an expansion belongs to. */
export type PaneKey = number | string;

function key(source: PaneSource, path: string): string {
  return `${String(source)}|${pathKey(path, source)}`;
}

function expandKey(pane: PaneKey, source: PaneSource, path: string): string {
  return `${String(pane)}|${key(source, path)}`;
}

function bump(): void {
  version++;
  for (const l of listeners) l();
}

/** Subscribe for useSyncExternalStore. */
export function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** The snapshot is a version number, not the data: a primitive is stable by
    definition, so useSyncExternalStore cannot loop on a fresh object identity.
    Components read the maps directly once a change has been signalled. */
export function getVersion(): number {
  return version;
}

export function getChildren(source: PaneSource, path: string): TreeNode[] | null {
  return entries.get(key(source, path))?.nodes ?? null;
}

export function setChildren(source: PaneSource, path: string, nodes: TreeNode[]): void {
  const k = key(source, path);
  // Re-insert so the most recently used entry is last, which makes the oldest
  // key the first one Map iteration yields.
  entries.delete(k);
  entries.set(k, { source, path, nodes });
  while (entries.size > MAX_ENTRIES) {
    const oldest = entries.keys().next();
    if (oldest.done) break;
    entries.delete(oldest.value);
  }
  bump();
}

/** True when this folder's last listing failed and has not been retried. */
export function hasFailed(source: PaneSource, path: string): boolean {
  return failed.has(key(source, path));
}

export function markFailed(source: PaneSource, path: string): void {
  failed.set(key(source, path), { source, path });
  bump();
}

export function isExpanded(pane: PaneKey, source: PaneSource, path: string): boolean {
  return expanded.has(expandKey(pane, source, path));
}

export function setExpanded(
  pane: PaneKey,
  source: PaneSource,
  path: string,
  open: boolean,
): void {
  const k = expandKey(pane, source, path);
  if (open) {
    expanded.set(k, { pane, source, path });
    // Opening a folder is the user asking again, so a previous failure is
    // retried. This is also what stops a failed listing from re-firing on
    // every mount: nothing else clears the marker.
    failed.delete(key(source, path));
  } else {
    expanded.delete(k);
  }
  bump();
}

/** Forget one folder's children, everything beneath it, and its parents.
 *
 * Subtree-wide downwards because a completed folder transfer creates a whole
 * new branch at once. Upwards as well because a new folder changes the CHILD
 * LIST of its parent, which is the entry the tree actually renders — dropping
 * only the changed directory would leave the new folder invisible.
 *
 * Expansion is left alone on purpose: which branches the user has open should
 * survive a file landing in one of them. Only the listing is stale, not the
 * intent. */
export function invalidateDir(source: PaneSource, dir: string): void {
  let hit = false;
  for (const [k, e] of entries) {
    if (String(e.source) !== String(source)) continue;
    if (isAtOrUnder(e.path, dir, source) || isAtOrUnder(dir, e.path, source)) {
      entries.delete(k);
      hit = true;
    }
  }
  for (const [k, e] of failed) {
    if (String(e.source) !== String(source)) continue;
    if (isAtOrUnder(e.path, dir, source) || isAtOrUnder(dir, e.path, source)) {
      failed.delete(k);
      hit = true;
    }
  }
  if (hit) bump();
}

/** Drop everything remembered for one source.
 *
 * Called when a site disconnects AND when it connects again. A reconnect can
 * be to a server whose tree changed while we were away, and SQLite recycles
 * row ids, so a new site could otherwise inherit another server's folders —
 * the same reasoning that makes recents.forgetSource exist. */
export function purgeSource(source: PaneSource): void {
  let hit = false;
  for (const [k, e] of entries) {
    if (String(e.source) === String(source)) {
      entries.delete(k);
      hit = true;
    }
  }
  for (const [k, e] of expanded) {
    if (String(e.source) === String(source)) {
      expanded.delete(k);
      hit = true;
    }
  }
  for (const [k, e] of failed) {
    if (String(e.source) === String(source)) {
      failed.delete(k);
      hit = true;
    }
  }
  if (hit) bump();
}

/** Test seam: forget everything. */
export function clearAll(): void {
  entries.clear();
  expanded.clear();
  failed.clear();
  bump();
}
