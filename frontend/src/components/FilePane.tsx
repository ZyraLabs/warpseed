import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useColumnWidths, type ColumnSpec } from "../hooks/useColumnWidths";
import { usePaneNav } from "../hooks/usePaneNav";
import { sortEntries, usePaneSort, type SortKey } from "../hooks/usePaneSort";
import {
  addBookmark,
  bookmarksFor,
  connectAndHome,
  deleteBookmark,
  deleteEntries,
  enqueueDownloads,
  enqueueUploads,
  getSettings,
  localStart,
  makeDir,
  on,
  renameEntry,
  setSetting,
  setSiteRemotePath,
  sites as fetchSites,
  type Bookmark,
  type FsChanged,
  type FsEntry,
} from "../ipc";
import { formatSize, formatTime } from "../lib/format";
import {
  isAtOrUnder,
  looksWindows,
  normalizeTypedLocal,
  pathKey,
  samePath,
  shortenPath,
} from "../lib/path";
import { recentPaths, rememberPath } from "../lib/recents";
import { useUiStore, type PaneSide } from "../store";
import Breadcrumb from "./Breadcrumb";
import ContextMenu, { type MenuItem } from "./ContextMenu";
import DirTree from "./DirTree";
import {
  Archive,
  ArrowUp,
  ChevronLeft,
  ChevronRight,
  Disc,
  File as FileIcon,
  Folder,
  Monitor,
  Refresh,
  Search,
  Tree as TreeIcon,
  Warning,
} from "./Icon";
import PromptDialog, { type PromptSpec } from "./PromptDialog";

function toast(kind: "info" | "error" | "success", text: string) {
  window.dispatchEvent(new CustomEvent("ws:toast", { detail: { kind, text } }));
}

/** Bound on how often a pane re-lists in response to filesystem events. */
const FS_REFRESH_COALESCE_MS = 500;

/** Type-ahead buffer lifetime. Microsoft documents "a time-out period" per
    character without publishing the value; 1s is our approximation. */
const TYPEAHEAD_MS = 1000;

/** Row height in px — must equal --row-h in tokens.css: the virtualizer
    positions rows by this number, the CSS only paints them. */
const ROW_H = 30;

/** File-type icon per row. Matched on the final extension only, so a
    "disc image inside an archive" name like .img.xz reads as an archive. */
const DISC_RE = /\.(iso|img)$/i;
const ARCHIVE_RE = /\.(zip|rar|7z|tar|gz|xz|zst)$/i;
function typeIcon(e: FsEntry) {
  if (e.isDir) return Folder;
  if (DISC_RE.test(e.name)) return Disc;
  if (ARCHIVE_RE.test(e.name)) return Archive;
  return FileIcon;
}

/** Size and date are resizable; the name column takes what is left, so
    narrowing these two is how you give a long filename more room. */
const PANE_COLUMNS: ColumnSpec[] = [
  { id: "psize", label: "Size", min: 48, initial: 84 },
  { id: "pdate", label: "Modified", min: 60, initial: 112 },
];

const SORT_LABEL: Record<SortKey, string> = { name: "Name", size: "Size", modTime: "Modified" };

export interface PaneCmd {
  side: PaneSide;
  cmd:
    | "up"
    | "editpath"
    | "filter"
    | "invert"
    | "clearmarks"
    | "reload"
    | "mkdir"
    | "rename"
    | "delete";
}

function matches(name: string, filter: string): boolean {
  const f = filter.toLowerCase();
  if (/[*?]/.test(f)) {
    const re = new RegExp(
      "^" + f.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".") + "$",
      "i",
    );
    return re.test(name);
  }
  return name.toLowerCase().includes(f);
}

export default function FilePane({ side }: { side: PaneSide }) {
  const { source, path } = useUiStore((s) => s.panes[side]);
  const isActive = useUiStore((s) => s.activePane === side);
  const setActivePane = useUiStore((s) => s.setActivePane);
  const setPane = useUiStore((s) => s.setPane);
  const sites = useUiStore((s) => s.sites);
  const connStates = useUiStore((s) => s.connStates);
  const setQuickConnect = useUiStore((s) => s.setQuickConnect);

  const nav = usePaneNav(side);
  const { sort, toggle: toggleSort } = usePaneSort();
  const { style: colStyle, startResize } = useColumnWidths(PANE_COLUMNS, "ui.pane_columns");
  const [cursor, setCursor] = useState(0);
  const [marks, setMarks] = useState<Set<string>>(new Set());
  const [filter, setFilter] = useState<string | null>(null);
  const [editReq, setEditReq] = useState(0);
  const [treeOpen, setTreeOpen] = useState(false);
  const [menu, setMenu] = useState<{ x: number; y: number; entry: FsEntry } | null>(null);
  const [pathMenu, setPathMenu] = useState<{ x: number; y: number } | null>(null);
  const [bgMenu, setBgMenu] = useState<{ x: number; y: number } | null>(null);
  const [bookmarks, setBookmarks] = useState<Bookmark[]>([]);
  const [prompt, setPrompt] = useState<PromptSpec | null>(null);
  const [taHint, setTaHint] = useState("");

  const scrollRef = useRef<HTMLDivElement>(null);
  const filterRef = useRef<HTMLInputElement>(null);
  /** The scroller is unmounted when the listing is empty, so the empty-state
      panel carries the key handler — and the focus — in its place. */
  const emptyRef = useRef<HTMLDivElement>(null);
  /** Range-select anchor: shift+click and shift+arrow both extend from here. */
  const anchor = useRef(0);
  /** Name to put the cursor on once the next listing lands — set when going
      up, so "up, look around, back down" returns to the folder you left. */
  const landOn = useRef<string | null>(null);
  /** Same idea across a re-sort: `cursor` is an index into `entries`, and a
      sort rebuilds that array in a new order. Left alone, the cursor keeps
      the number and silently changes file — and F5/Del act on selection(). */
  const keepOn = useRef<string | null>(null);
  const typeahead = useRef<{ buf: string; at: number }>({ buf: "", at: 0 });
  const taTimer = useRef<number | undefined>(undefined);

  const refocusList = useCallback(() => (scrollRef.current ?? emptyRef.current)?.focus(), []);

  const all = nav.listing?.entries ?? [];
  const entries = useMemo(() => {
    const visible = filter ? all.filter((e) => matches(e.name, filter)) : all;
    return sortEntries(visible, sort);
  }, [all, filter, sort]);

  // Returning from flight mode (or gaining the pane via Tab) lands keyboard
  // navigation back on this list — display:none dropped focus to <body>,
  // which would otherwise dead-key arrows/Space/Backspace until a click.
  const viewMode = useUiStore((s) => s.viewMode);
  const miniMode = useUiStore((s) => s.miniMode);
  useEffect(() => {
    if (viewMode === "browse" && isActive && !miniMode) refocusList();
  }, [viewMode, isActive, miniMode, refocusList]);

  useEffect(() => {
    if (filter !== null) filterRef.current?.focus();
  }, [filter !== null]); // eslint-disable-line react-hooks/exhaustive-deps

  const virtualizer = useVirtualizer({
    count: entries.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_H,
    overscan: 20,
  });

  const moveCursor = useCallback(
    (n: number) => {
      setCursor(n);
      virtualizer.scrollToIndex(n);
    },
    [virtualizer],
  );

  // Move the cursor, optionally dragging a marked range behind it. A plain
  // arrow resets the anchor but does NOT collapse existing marks: commander
  // behaviour (ux-spec §3.6), deliberately not Explorer's collapse.
  const moveTo = useCallback(
    (n: number, extend: boolean) => {
      if (!entries.length) return;
      const idx = Math.max(0, Math.min(n, entries.length - 1));
      moveCursor(idx);
      if (extend) {
        // A filter can shrink the listing under a stale anchor.
        const a = Math.max(0, Math.min(anchor.current, entries.length - 1));
        const [lo, hi] = idx < a ? [idx, a] : [a, idx];
        setMarks(new Set(entries.slice(lo, hi + 1).map((x) => x.name)));
      } else {
        anchor.current = idx;
      }
    },
    [entries, moveCursor],
  );

  // Explorer type-ahead: typing JUMPS the cursor to the next matching name.
  // It used to open a substring filter, which hid rows, moved focus out of
  // the list on the first keystroke and left no keyboard way back out
  // (ux-spec §3.5, amended in this build). Ctrl+F still opens the filter.
  const jumpTo = useCallback(
    (ch: string) => {
      const now = Date.now();
      const t = typeahead.current;
      const buf = now - t.at > TYPEAHEAD_MS ? ch : t.buf + ch;
      typeahead.current = { buf, at: now };
      setTaHint(buf);
      if (taTimer.current !== undefined) window.clearTimeout(taTimer.current);
      taTimer.current = window.setTimeout(() => setTaHint(""), TYPEAHEAD_MS);
      if (entries.length === 0) return;
      // Explorer's dual mode: repeating ONE letter steps to the next entry
      // beginning with it; typing different letters switches to prefix mode
      // and selects the first entry matching the whole accumulated prefix.
      const sameLetter = buf.length > 1 && /^(.)\1*$/.test(buf);
      const probe = (sameLetter ? buf[0] : buf).toLowerCase();
      const step = buf.length === 1 || sameLetter ? 1 : 0;
      for (let i = 0; i < entries.length; i++) {
        const idx = (cursor + step + i) % entries.length;
        // Prefix, never substring: a substring hit on "r" lands somewhere
        // unpredictable in a folder of release names.
        if (entries[idx].name.toLowerCase().startsWith(probe)) {
          moveCursor(idx);
          anchor.current = idx;
          return;
        }
      }
      // No match: keep the buffer accumulating and leave the cursor alone,
      // exactly as Explorer does. Never hide rows.
    },
    [entries, cursor, moveCursor],
  );

  // Remember the folder we are leaving so the navigation-reset effect can
  // put the cursor back on it.
  const goUp = useCallback(() => {
    const here = nav.listing?.path ?? path;
    const s = here.includes("\\") ? "\\" : "/";
    const trimmed = here.length > 1 && here.endsWith(s) ? here.slice(0, -1) : here;
    const i = trimmed.lastIndexOf(s);
    landOn.current = i >= 0 && i < trimmed.length - 1 ? trimmed.slice(i + 1) : null;
    // Drop the filter HERE, not in the reset effect below: that effect reads
    // an `entries` memoized with whatever filter was still open, so landing
    // on a name found in the filtered subset addresses a different row once
    // the filter clears.
    setFilter(null);
    nav.up();
  }, [nav, path]);

  // Reset selection state on navigation, landing on the folder we just left
  // when this was an "up". "Up, look around, back down" is the dominant
  // browsing loop; on a high-RTT pane, re-finding the row costs real time.
  useEffect(() => {
    const want = landOn.current;
    landOn.current = null;
    setMarks(new Set());
    setFilter(null);
    setMenu(null); // a menu left open would act on the previous directory
    setBgMenu(null);
    typeahead.current = { buf: "", at: 0 };
    setTaHint("");
    const idx = want ? entries.findIndex((x) => x.name === want) : -1;
    setCursor(idx >= 0 ? idx : 0);
    anchor.current = idx >= 0 ? idx : 0;
    if (idx >= 0) virtualizer.scrollToIndex(idx, { align: "center" });
    else scrollRef.current?.scrollTo({ top: 0 });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nav.listing?.path]);

  // A header click must leave the pane usable: it takes the pane and hands
  // focus back to the list. Without that, arrows and Space go dead until
  // the user clicks a row again — which is how a user learns to never
  // touch the header a second time.
  const sortHeader = useCallback(
    (k: SortKey) => {
      setActivePane(side);
      keepOn.current = entries[cursor]?.name ?? null;
      toggleSort(k);
      refocusList();
    },
    [setActivePane, side, toggleSort, refocusList, entries, cursor],
  );

  // Follow that row into the new order. `entries` is a useMemo keyed on the
  // sort, so by the time this runs it has already been rebuilt.
  useEffect(() => {
    const want = keepOn.current;
    keepOn.current = null;
    if (want === null) return;
    const idx = entries.findIndex((x) => x.name === want);
    if (idx < 0) {
      // The listing changed under the sort — keep the cursor in range.
      setCursor((c) => Math.min(c, Math.max(0, entries.length - 1)));
      return;
    }
    setCursor(idx);
    anchor.current = idx;
    virtualizer.scrollToIndex(idx, { align: "center" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sort]);

  // enqueueItems queues remote files for download into the OTHER pane's
  // local directory (commander F5 semantics).
  const enqueueItems = useCallback(
    async (items: FsEntry[]) => {
      if (typeof source !== "number" || !nav.listing) return;
      const other = useUiStore.getState().panes[side === 0 ? 1 : 0];
      if (other.source !== "local") {
        toast("error", "The other pane must be This PC to receive downloads");
        return;
      }
      if (items.length === 0) {
        toast("error", "Nothing selected to transfer");
        return;
      }
      const base = nav.listing.path.endsWith("/") ? nav.listing.path : nav.listing.path + "/";
      try {
        await enqueueDownloads(
          source,
          items.map((e) => ({ src: base + e.name, size: e.size, isDir: e.isDir })),
          other.path,
        );
        setMarks(new Set());
        useUiStore.getState().setQueueOpen(true);
        toast("success", `Queued ${items.length} item${items.length > 1 ? "s" : ""} for download`);
      } catch (err) {
        toast("error", String(err));
      }
    },
    [source, side, nav.listing],
  );

  // uploadItems queues local files/folders for upload into the OTHER pane's
  // remote directory.
  const uploadItems = useCallback(
    async (items: FsEntry[]) => {
      if (source !== "local" || !nav.listing) return;
      const other = useUiStore.getState().panes[side === 0 ? 1 : 0];
      if (typeof other.source !== "number") {
        toast("error", "The other pane must be a connected site to receive uploads");
        return;
      }
      if (items.length === 0) {
        toast("error", "Nothing selected to transfer");
        return;
      }
      const sep = nav.listing.path.includes("\\") ? "\\" : "/";
      const base = nav.listing.path.endsWith(sep) ? nav.listing.path : nav.listing.path + sep;
      try {
        await enqueueUploads(
          other.source,
          items.map((e) => ({ src: base + e.name, size: e.size, isDir: e.isDir })),
          other.path,
        );
        useUiStore.getState().setQueueOpen(true);
        toast("success", `Queued ${items.length} item${items.length > 1 ? "s" : ""} for upload`);
      } catch (err) {
        toast("error", String(err));
      }
    },
    [source, side, nav.listing],
  );

  const open = useCallback(
    (e: FsEntry) => {
      if (!nav.listing) return;
      if (e.isDir) {
        const sep = nav.listing.path.includes("\\") ? "\\" : "/";
        const base = nav.listing.path.endsWith(sep) ? nav.listing.path : nav.listing.path + sep;
        nav.navigate(base + e.name);
      } else if (typeof source === "number") {
        void enqueueItems([e]); // double-click a remote file = download it
      } else {
        const other = useUiStore.getState().panes[side === 0 ? 1 : 0];
        if (typeof other.source === "number") {
          void uploadItems([e]); // double-click a local file = upload it
        } else {
          toast("info", "Connect a site in the other pane to transfer");
        }
      }
    },
    [nav, source, side, enqueueItems, uploadItems],
  );

  const toggleMark = useCallback((name: string) => {
    setMarks((m) => {
      const next = new Set(m);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }, []);

  const invertMarks = useCallback(() => {
    setMarks((m) => {
      const next = new Set<string>();
      for (const e of entries) if (!m.has(e.name)) next.add(e.name);
      return next;
    });
  }, [entries]);

  // A completed transfer or a file operation elsewhere changes what this
  // pane should be showing — reload when the affected directory is ours.
  // The subscription is registered once; a ref carries the current state in
  // so progress-driven re-renders don't churn listeners.
  const fsState = useRef({ source, here: path, reload: nav.reload });
  fsState.current = { source, here: nav.listing?.path ?? path, reload: nav.reload };

  useEffect(() => {
    let timer: number | undefined;
    const off = on<FsChanged>("fs:changed", (ev) => {
      const { source: src, here } = fsState.current;
      const mine = ev.source === "local" ? src === "local" : src === ev.siteId;
      if (!mine || !here || !ev.dir) return;

      // Also match changes BELOW this directory: a folder transfer writes
      // into a new subtree, and the pane showing the parent is exactly where
      // the user is waiting for that folder to appear.
      if (!isAtOrUnder(ev.dir, here, src)) return;

      // Coalesce: a folder transfer completes one file at a time, and every
      // reload is a real listing round trip — on a remote pane they queue on
      // the single browse connection and would stall navigation.
      if (timer !== undefined) return;
      timer = window.setTimeout(() => {
        timer = undefined;
        fsState.current.reload();
      }, FS_REFRESH_COALESCE_MS);
    });
    return () => {
      off();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, []);

  // Remember where we've been, per source, for the path menu.
  useEffect(() => {
    if (nav.listing?.path) rememberPath(source, nav.listing.path);
  }, [source, nav.listing?.path]);

  useEffect(() => {
    void bookmarksFor(source).then(setBookmarks).catch(() => setBookmarks([]));
  }, [source, pathMenu]);

  // A Windows path TYPED into a pane that is browsing a site belongs to This
  // PC — sending "D:\" to an SFTP server just fails confusingly. This only
  // applies to input the user typed: crumb clicks, recents and bookmarks are
  // already scoped to this source, and a Windows SFTP host legitimately
  // serves drive-letter paths, so reclassifying those would jump the pane to
  // the operator's own disk at the same path.
  const navigateTyped = useCallback(
    (target: string) => {
      const serverSpeaksWindows = looksWindows(nav.listing?.path ?? path);
      if (looksWindows(target) && typeof source === "number" && !serverSpeaksWindows) {
        setPane(side, "local", normalizeTypedLocal(target));
        toast("info", "Switched this pane to This PC");
        return;
      }
      nav.navigate(target);
    },
    [source, side, setPane, nav, path],
  );

  const sep = (p: string) => (p.includes("\\") ? "\\" : "/");
  const joinHere = useCallback(
    (name: string) => {
      const base = nav.listing?.path ?? path;
      const s = sep(base);
      return (base.endsWith(s) ? base : base + s) + name;
    },
    [nav.listing, path],
  );

  const selection = useCallback((): FsEntry[] => {
    if (marks.size) return entries.filter((e) => marks.has(e.name));
    return entries[cursor] ? [entries[cursor]] : [];
  }, [marks, entries, cursor]);

  // Explorer mouse selection: a plain click REPLACES the selection with that
  // one row (previously it only moved the cursor, so "click A, ctrl+click B"
  // left A unselected and acted on B alone), Ctrl adds/removes, Shift takes
  // the range from the anchor. Insert/Space keep the commander behaviour.
  const selectAt = useCallback(
    (index: number, ev: React.MouseEvent) => {
      const entry = entries[index];
      if (!entry) return;
      setCursor(index);
      if (ev.shiftKey) {
        const a = Math.max(0, Math.min(anchor.current, entries.length - 1));
        const [lo, hi] = index < a ? [index, a] : [a, index];
        const range = entries.slice(lo, hi + 1).map((x) => x.name);
        // Ctrl+Shift adds the range; plain Shift replaces. Without the Ctrl
        // branch, Ctrl+Shift+click silently discarded everything already
        // selected — the opposite of what the modifier means.
        if (ev.ctrlKey || ev.metaKey) setMarks((m) => new Set([...m, ...range]));
        else setMarks(new Set(range));
        return;
      }
      anchor.current = index;
      if (ev.ctrlKey || ev.metaKey) {
        toggleMark(entry.name);
      } else {
        setMarks(new Set([entry.name]));
      }
    },
    [entries, toggleMark],
  );

  const doDelete = useCallback(
    (items: FsEntry[]) => {
      if (items.length === 0) return;
      const names = items.length === 1 ? `“${items[0].name}”` : `${items.length} items`;
      setPrompt({
        title: `Delete ${names}?`,
        body: items.some((e) => e.isDir)
          ? "Folders are deleted with everything inside them. This cannot be undone."
          : "This cannot be undone.",
        confirmLabel: "Delete",
        danger: true,
        onConfirm: () => {
          void deleteEntries(
            source,
            items.map((e) => joinHere(e.name)),
            nav.listing?.path ?? path,
          )
            .then((n) => {
              setMarks(new Set());
              toast("success", `Deleted ${n} item${n === 1 ? "" : "s"}`);
            })
            .catch((err: unknown) => toast("error", String(err)))
            .finally(() => nav.reload());
        },
      });
    },
    [source, joinHere, nav, path],
  );

  const doRename = useCallback(
    (entry: FsEntry) => {
      setPrompt({
        title: "Rename",
        initialValue: entry.name,
        confirmLabel: "Rename",
        onConfirm: (name) => {
          if (!name || name === entry.name) return;
          void renameEntry(source, joinHere(entry.name), name, nav.listing?.path ?? path)
            .catch((err: unknown) => toast("error", String(err)))
            .finally(() => nav.reload());
        },
      });
    },
    [source, joinHere, nav, path],
  );

  const doMkdir = useCallback(() => {
    setPrompt({
      title: "New folder",
      initialValue: "",
      confirmLabel: "Create",
      onConfirm: (name) => {
        if (!name) return;
        void makeDir(source, nav.listing?.path ?? path, name)
          .catch((err: unknown) => toast("error", String(err)))
          .finally(() => nav.reload());
      },
    });
  }, [source, nav, path]);

  // Commands from the palette / global shortcuts.
  useEffect(() => {
    const handler = (ev: Event) => {
      const { side: s, cmd } = (ev as CustomEvent<PaneCmd>).detail;
      if (s !== side) return;
      {
        const st = useUiStore.getState();
        if (st.viewMode !== "browse" || st.miniMode) return; // panes hidden
      }
      if (cmd === "up") goUp();
      else if (cmd === "editpath") setEditReq((n) => n + 1);
      else if (cmd === "filter") setFilter((f) => (f === null ? "" : f));
      else if (cmd === "invert") invertMarks();
      else if (cmd === "clearmarks") setMarks(new Set());
      else if (cmd === "reload") nav.reload();
      else if (cmd === "mkdir") doMkdir();
      else if (cmd === "rename") {
        const sel = selection();
        if (sel.length === 1) doRename(sel[0]);
        else toast("error", "Select exactly one item to rename");
      } else if (cmd === "delete") doDelete(selection());
    };
    window.addEventListener("ws:panecmd", handler);
    return () => window.removeEventListener("ws:panecmd", handler);
  }, [side, nav, goUp, invertMarks, doMkdir, doRename, doDelete, selection]);

  // Active-pane shortcuts that must work outside the list focus.
  useEffect(() => {
    if (!isActive) return;
    const handler = (e: KeyboardEvent) => {
      // Flight/Deck/mini hide the panes; their shortcuts must sleep with
      // them — F5 would silently re-queue old marks, F7/F8 would open
      // invisible dialogs inside a display:none subtree.
      const st = useUiStore.getState();
      if (st.viewMode !== "browse" || st.miniMode) return;
      const inField =
        e.target instanceof HTMLInputElement ||
        e.target instanceof HTMLSelectElement ||
        e.target instanceof HTMLTextAreaElement;
      if (e.ctrlKey && e.key.toLowerCase() === "l") {
        e.preventDefault();
        setEditReq((n) => n + 1);
      } else if (e.ctrlKey && e.key.toLowerCase() === "f") {
        e.preventDefault();
        setFilter((f) => (f === null ? "" : f));
      } else if (e.altKey && e.key === "ArrowLeft" && !inField) {
        e.preventDefault();
        nav.back();
      } else if (e.altKey && e.key === "ArrowRight" && !inField) {
        e.preventDefault();
        nav.forward();
      } else if (e.altKey && e.key === "ArrowUp" && !inField) {
        e.preventDefault();
        goUp();
      } else if (e.key === "F5" && !e.ctrlKey && !inField) {
        // Ctrl+F5 is "sort by modified"; plain F5 inside the filter strip or
        // the path box must not fire a transfer either.
        e.preventDefault(); // F5 transfers — never reloads the webview
        if (typeof source === "number") void enqueueItems(selection());
        else void uploadItems(selection());
      } else if (e.ctrlKey && e.key.toLowerCase() === "r") {
        e.preventDefault(); // never let the webview reload itself
        nav.reload();
      } else if (e.key === "F2" && !inField) {
        e.preventDefault();
        const sel = selection();
        if (sel.length === 1) doRename(sel[0]);
      } else if (e.key === "F7" && !inField) {
        e.preventDefault();
        doMkdir();
      } else if ((e.key === "Delete" || e.key === "F8") && !inField) {
        e.preventDefault();
        doDelete(selection());
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [
    isActive,
    nav,
    goUp,
    source,
    enqueueItems,
    uploadItems,
    selection,
    doRename,
    doMkdir,
    doDelete,
  ]);

  // Right-click on the path bar: make this folder the default, remember it,
  // and jump to recent or bookmarked folders.
  const buildPathMenu = (): MenuItem[] => {
    // Only a folder that actually listed may be saved as a default or a
    // bookmark — persisting a path that just failed is how a bad default
    // gets baked in.
    const listed = nav.listing?.path ?? null;
    const here = listed ?? path;
    const siteName =
      typeof source === "number"
        ? useUiStore.getState().sites.find((s) => s.id === source)?.name ?? "this site"
        : null;
    const items: MenuItem[] = [];

    items.push({
      label: siteName ? `Open ${siteName} here by default` : "Open This PC here by default",
      disabled: !listed,
      run: () => {
        const done = () => toast("success", `Default folder set to ${here}`);
        const fail = (err: unknown) => toast("error", String(err));
        if (typeof source === "number") {
          // Refresh the sites cache: connect reads remotePath from there, so
          // without this the new default silently does nothing this session.
          void setSiteRemotePath(source, here)
            .then(() => fetchSites().then(useUiStore.getState().setSites))
            .then(done)
            .catch(fail);
        } else {
          void setSetting("ui.local_default", here).then(done).catch(fail);
        }
      },
    });

    const bookmarked = bookmarks.find((b) => samePath(b.path, here, source));
    items.push(
      bookmarked
        ? {
            label: "Remove bookmark",
            run: () =>
              void deleteBookmark(bookmarked.id)
                .then(() => bookmarksFor(source).then(setBookmarks))
                .catch((e: unknown) => toast("error", String(e))),
          }
        : {
            label: "Remember this folder",
            disabled: !listed,
            run: () =>
              void addBookmark(source, here, "")
                .then(() => bookmarksFor(source).then(setBookmarks))
                .then(() => toast("success", "Folder bookmarked"))
                .catch((e: unknown) => toast("error", String(e))),
          },
    );

    // A bookmarked folder is listed once, as a bookmark — not twice, which
    // would also collide as duplicate menu keys.
    const saved = bookmarks.filter((b) => !samePath(b.path, here, source)).slice(0, 8);
    const savedKeys = new Set(saved.map((b) => pathKey(b.path, source)));
    for (const p of recentPaths(source, here).slice(0, 5)) {
      if (savedKeys.has(pathKey(p, source))) continue;
      items.push({ label: shortenPath(p), hint: "recent", run: () => nav.navigate(p) });
    }
    for (const b of saved) {
      items.push({
        label: b.label || shortenPath(b.path),
        hint: "saved",
        run: () => nav.navigate(b.path),
      });
    }
    return items;
  };

  // ContextMenu has no submenus, so the active sort is marked with a check
  // prefix in a flat list.
  const sortItems = (): MenuItem[] => {
    const tick = (k: SortKey) => (sort.key === k ? "✓ " : "   ");
    return [
      { label: `${tick("name")}Sort by name`, hint: "Ctrl+F3", run: () => sortHeader("name") },
      { label: `${tick("size")}Sort by size`, hint: "Ctrl+F6", run: () => sortHeader("size") },
      {
        label: `${tick("modTime")}Sort by modified`,
        hint: "Ctrl+F5",
        run: () => sortHeader("modTime"),
      },
    ];
  };

  // Right-click on empty space below the last row: the reflex of every file
  // manager user who cannot find a control, so it must not come up empty.
  const buildBgMenu = (): MenuItem[] => [
    { label: "New folder…", hint: "F7", run: doMkdir },
    { label: "Refresh", hint: "Ctrl+R", run: nav.reload },
    {
      label: "Select all",
      hint: "Ctrl+A",
      run: () => setMarks(new Set(entries.map((x) => x.name))),
    },
    ...sortItems(),
  ];

  // Right-click menu items for the entry under the pointer (acting on the
  // full mark set when the entry is part of it).
  const buildMenu = (entry: FsEntry): MenuItem[] => {
    const sel =
      marks.size && marks.has(entry.name) ? entries.filter((x) => marks.has(x.name)) : [entry];
    const count = sel.length > 1 ? ` (${sel.length})` : "";
    const other = useUiStore.getState().panes[side === 0 ? 1 : 0];
    const items: MenuItem[] = [];
    if (typeof source === "number") {
      items.push({
        label: `Download${count}`,
        hint: "F5",
        disabled: other.source !== "local",
        run: () => void enqueueItems(sel),
      });
    } else {
      items.push({
        label: `Upload${count}`,
        hint: "F5",
        disabled: typeof other.source !== "number",
        run: () => void uploadItems(sel),
      });
    }
    if (entry.isDir) {
      items.push({ label: "Open", hint: "Enter", run: () => open(entry) });
    }
    items.push({
      label: "Rename…",
      hint: "F2",
      disabled: sel.length !== 1,
      run: () => doRename(entry),
    });
    items.push({ label: "New folder…", hint: "F7", run: doMkdir });
    items.push({
      label: "Copy path",
      run: () => void navigator.clipboard.writeText(joinHere(entry.name)),
    });
    items.push(...sortItems());
    items.push({ label: "Refresh", hint: "Ctrl+R", run: nav.reload });
    items.push({
      label: `Delete${count}`,
      hint: "Del",
      danger: true,
      run: () => doDelete(sel),
    });
    return items;
  };

  const onListKeyDown = (ev: React.KeyboardEvent) => {
    // Escape first, and before the empty-listing bail: it is the only way
    // out of a filter that matched nothing.
    if (ev.key === "Escape") {
      ev.preventDefault();
      typeahead.current = { buf: "", at: 0 };
      setTaHint("");
      if (filter !== null) setFilter(null);
      return;
    }
    if (!entries.length && ev.key !== "Backspace") return;
    const e = entries[cursor];
    // Page by what is actually on screen: the pane height is user-driven,
    // so a hardcoded 20 rows overshoots on a short window and undershoots
    // on a tall one. The -1 keeps a row of context, as Explorer does.
    const page = Math.max(
      1,
      Math.floor((scrollRef.current?.clientHeight ?? ROW_H * 20) / ROW_H) - 1,
    );
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      moveTo(cursor + 1, ev.shiftKey);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      moveTo(cursor - 1, ev.shiftKey);
    } else if (ev.key === "Home") {
      ev.preventDefault();
      moveTo(0, ev.shiftKey);
    } else if (ev.key === "End") {
      ev.preventDefault();
      moveTo(entries.length - 1, ev.shiftKey);
    } else if (ev.key === "PageDown") {
      ev.preventDefault();
      moveTo(cursor + page, ev.shiftKey);
    } else if (ev.key === "PageUp") {
      ev.preventDefault();
      moveTo(cursor - page, ev.shiftKey);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      if (e) open(e);
    } else if (ev.key === "Backspace") {
      ev.preventDefault();
      goUp();
    } else if (ev.key === "Insert") {
      ev.preventDefault();
      if (e) {
        toggleMark(e.name);
        moveTo(cursor + 1, false); // anchor follows, so a later shift+click ranges from here
      }
    } else if (ev.key === " ") {
      ev.preventDefault();
      // Space feeds the prefix search only while the buffer is still warm,
      // so "The Big Short" stays reachable; a bare Space still marks
      // (ux-spec §3.1). Insert remains the unambiguous mark key.
      const t = typeahead.current;
      if (t.buf !== "" && Date.now() - t.at <= TYPEAHEAD_MS) jumpTo(" ");
      else if (e) toggleMark(e.name);
    } else if (ev.key === "*") {
      ev.preventDefault();
      invertMarks();
    } else if (ev.ctrlKey && ev.shiftKey && ev.key.toLowerCase() === "a") {
      ev.preventDefault();
      setMarks(new Set());
    } else if (ev.ctrlKey && ev.key.toLowerCase() === "a") {
      ev.preventDefault();
      setMarks(new Set(entries.map((x) => x.name)));
    } else if (ev.ctrlKey && (ev.key === "F3" || ev.key === "F5" || ev.key === "F6")) {
      // WinSCP sort hotkeys (ux-spec §3.1); there is no "ext" key to bind F4.
      ev.preventDefault();
      sortHeader(ev.key === "F3" ? "name" : ev.key === "F5" ? "modTime" : "size");
    } else if (ev.key.length === 1 && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
      ev.preventDefault();
      jumpTo(ev.key);
    }
  };

  const markedSize = useMemo(() => {
    let total = 0;
    for (const e of all) if (marks.has(e.name) && e.size > 0) total += e.size;
    return total;
  }, [all, marks]);

  const connState = typeof source === "number" ? connStates[source] : undefined;
  const siteName =
    typeof source === "number" ? sites.find((s) => s.id === source)?.name ?? `site ${source}` : null;

  /** Re-dial a remote pane's site and reload. The backend evicts dead
      sessions, so this works after an idle timeout — unlike a plain Retry,
      which re-lists over a connection that no longer exists. */
  const reconnect = async () => {
    if (typeof source !== "number") return;
    try {
      const site = useUiStore.getState().sites.find((s) => s.id === source);
      const home = await connectAndHome(source, site?.remotePath);
      // The user may have switched this pane's source while the re-dial was
      // in flight; a stale reconnect must not yank the pane back.
      if (useUiStore.getState().panes[side].source !== source) return;
      setPane(side, source, home);
      nav.reload();
    } catch (err) {
      toast("error", String(err));
    }
  };

  /** True when a scroller click landed on a row, or on the scrollbar itself —
      dragging the scrollbar must not wipe the selection. */
  const onRowOrScrollbar = (ev: React.MouseEvent<HTMLDivElement>) => {
    if ((ev.target as HTMLElement).closest(".row")) return true;
    const el = ev.currentTarget;
    return ev.clientX - el.getBoundingClientRect().left > el.clientWidth;
  };

  // Every column reserves a chevron slot and reveals it on hover, so the
  // labels stop shifting sideways and the sort gesture advertises itself.
  const chevClass = (k: SortKey) =>
    `phead__dir ${sort.key === k ? (sort.desc ? "phead__dir--desc" : "phead__dir--asc") : "phead__dir--idle"}`;
  const ariaSort = (k: SortKey): "ascending" | "descending" | "none" =>
    sort.key === k ? (sort.desc ? "descending" : "ascending") : "none";

  const switchSource = async (value: string) => {
    if (value === "local") {
      // Honour the saved default folder, falling back to home when it is
      // gone (localStart probes it rather than trusting the setting).
      const cfg = await getSettings().catch(() => ({}) as Record<string, string>);
      setPane(side, "local", await localStart(cfg["ui.local_default"]));
      refocusList(); // the native <select> keeps the arrow keys otherwise
    } else if (value === "__new__") {
      setQuickConnect(true, side); // the dialog wants the focus, not the list
    } else {
      const id = Number(value);
      try {
        const site = useUiStore.getState().sites.find((s) => s.id === id);
        const home = await connectAndHome(id, site?.remotePath);
        setPane(side, id, home);
      } catch (err) {
        window.dispatchEvent(
          new CustomEvent("ws:toast", { detail: { kind: "error", text: String(err) } }),
        );
      }
      refocusList();
    }
  };

  return (
    <section
      className={`pane ${isActive ? "pane--active" : ""}`}
      onMouseDown={() => setActivePane(side)}
      aria-label={`File pane ${side + 1}`}
    >
      <header className="pane__bar">
        <button
          className={`pane__btn ${treeOpen ? "pane__btn--on" : ""}`}
          onClick={() => setTreeOpen(!treeOpen)}
          title="Folder tree"
          aria-pressed={treeOpen}
        >
          <TreeIcon size={14} />
        </button>
        <span className={`pane__srcpill ${siteName ? "pane__srcpill--site" : ""}`}>
          {siteName ? (
            <span
              className={`conn-dot conn-dot--${connState ?? "disconnected"}`}
              title={`${siteName}: ${connState ?? "disconnected"}`}
            />
          ) : (
            <Monitor size={13} />
          )}
          <select
            className="pane__source"
            value={typeof source === "number" ? String(source) : "local"}
            onChange={(e) => void switchSource(e.target.value)}
            aria-label="Pane source"
          >
            <option value="local">This PC</option>
            {sites.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
            <option value="__new__">+ Connect…</option>
          </select>
        </span>
        <button className="pane__btn" onClick={nav.back} disabled={!nav.canBack} title="Back (Alt+←)">
          <ChevronLeft size={13} />
        </button>
        <button
          className="pane__btn"
          onClick={nav.forward}
          disabled={!nav.canForward}
          title="Forward (Alt+→)"
        >
          <ChevronRight size={13} />
        </button>
        <button
          className="pane__btn"
          onClick={goUp}
          disabled={!nav.listing?.parent}
          title="Up (Backspace)"
        >
          <ArrowUp size={13} />
        </button>
        <button className="pane__btn" onClick={nav.reload} title="Refresh (Ctrl+R)">
          <Refresh size={13} />
        </button>
        <span
          className="pane__crumbwrap"
          onContextMenu={(ev) => {
            ev.preventDefault();
            setActivePane(side);
            setPathMenu({ x: ev.clientX, y: ev.clientY });
          }}
        >
          <Breadcrumb
            path={nav.listing?.path ?? path}
            onNavigate={nav.navigate}
            onSubmitPath={navigateTyped}
            editReq={editReq}
          />
        </span>
      </header>

      {filter !== null && (
        <div className="pane__filter">
          <Search size={13} className="pane__filter-icon" />
          <input
            ref={filterRef}
            value={filter}
            placeholder="filter… (* and ? glob)"
            onChange={(e) => setFilter(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setFilter(null);
                refocusList();
              } else if (e.key === "Enter") {
                // A filter matching nothing unmounts the scroller — hand
                // focus to the empty panel so Escape still has a listener.
                refocusList();
              }
              e.stopPropagation();
            }}
          />
          <span>
            {entries.length} / {all.length}
          </span>
        </div>
      )}

      <div className="pane__body">
        {treeOpen && (
          <DirTree
            source={source}
            currentPath={nav.listing?.path ?? path}
            onNavigate={nav.navigate}
          />
        )}
        <div className="pane__content">
      {nav.error ? (
        <div>
          <div className="pane__error" role="alert">
            <Warning size={15} className="pane__error-icon" />
            <p>{nav.error}</p>
            <div className="pane__error-actions">
              {typeof source === "number" && (
                <button className="btn btn--primary" onClick={() => void reconnect()}>
                  Reconnect
                </button>
              )}
              <button className="btn" onClick={nav.reload}>
                Retry
              </button>
              <button className="btn" onClick={goUp}>
                Go up
              </button>
            </div>
          </div>
        </div>
      ) : nav.loading && !nav.listing ? (
        <div className="pane__skeleton" aria-hidden>
          {Array.from({ length: 8 }, (_, i) => (
            <div key={i} style={{ width: `${90 - i * 7}%` }} />
          ))}
        </div>
      ) : (
        <div
          className="pane__list"
          style={colStyle}
          role="grid"
          aria-rowcount={entries.length + 1}
          aria-multiselectable="true"
        >
          {/* Sortable, resizable headings: click to sort, drag the divider
              beside Size or Modified to give the name column more room. The
              grip is a SIBLING of the button, and pane.css reserves the
              padding it sits in — overlapping the button by even a few px
              hands those clicks to the grip and the sort never fires. */}
          <div role="rowgroup">
            <div className="row row--head" role="row">
              <div
                className="phead-cell"
                role="columnheader"
                aria-colindex={1}
                aria-sort={ariaSort("name")}
              >
                <button
                  className={`phead ${sort.key === "name" ? "phead--on" : ""}`}
                  onClick={() => sortHeader("name")}
                  title="Sort by name — click again to reverse"
                >
                  {SORT_LABEL.name}
                  <ChevronRight size={9} className={chevClass("name")} />
                </button>
              </div>
              {PANE_COLUMNS.map((c, i) => {
                const key: SortKey = c.id === "psize" ? "size" : "modTime";
                return (
                  <div
                    key={c.id}
                    className="phead-cell"
                    role="columnheader"
                    aria-colindex={i + 2}
                    aria-sort={ariaSort(key)}
                  >
                    <button
                      className={`phead phead--num ${sort.key === key ? "phead--on" : ""}`}
                      onClick={() => sortHeader(key)}
                      title={`Sort by ${c.label.toLowerCase()} — click again to reverse`}
                    >
                      <ChevronRight size={9} className={chevClass(key)} />
                      {c.label}
                    </button>
                    <span
                      className="phead__grip"
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={`Resize ${c.label}`}
                      onMouseDown={(e) => startResize(c.id, e)}
                    />
                  </div>
                );
              })}
            </div>
          </div>

        {entries.length === 0 ? (
          // Explorer keeps the header row over an empty folder, and a filter
          // that matched nothing is where the user most needs Escape — so
          // this branch has to carry the key handler too.
          <div
            className="pane__empty"
            ref={emptyRef}
            tabIndex={0}
            onKeyDown={onListKeyDown}
            onContextMenu={(ev) => {
              ev.preventDefault();
              setActivePane(side);
              setBgMenu({ x: ev.clientX, y: ev.clientY });
            }}
          >
            <div>{filter ? `No matches for “${filter}”` : "Empty directory"}</div>
            <div>{filter ? "Esc to clear" : ""}</div>
          </div>
        ) : (
        <div
          className="pane__scroll"
          ref={scrollRef}
          tabIndex={0}
          onKeyDown={onListKeyDown}
          role="rowgroup"
          onMouseDown={(ev) => {
            if (onRowOrScrollbar(ev)) return;
            setActivePane(side);
            setMarks(new Set());
          }}
          onContextMenu={(ev) => {
            if (onRowOrScrollbar(ev)) return;
            ev.preventDefault();
            setActivePane(side);
            setBgMenu({ x: ev.clientX, y: ev.clientY });
          }}
        >
          {/* Presentational: the virtualizer's spacer must not sit between
              the rowgroup and its rows as far as a screen reader is told. */}
          <div
            role="presentation"
            style={{ height: virtualizer.getTotalSize(), position: "relative" }}
          >
            {virtualizer.getVirtualItems().map((vi) => {
              const e = entries[vi.index];
              const RowIcon = typeIcon(e);
              const cls = [
                "row",
                e.isDir ? "row--dir" : "",
                vi.index === cursor ? "row--cursor" : "",
                marks.has(e.name) ? "row--marked" : "",
              ]
                .filter(Boolean)
                .join(" ");
              return (
                <div
                  key={vi.key}
                  className={cls}
                  style={{ transform: `translateY(${vi.start}px)` }}
                  onClick={(ev) => selectAt(vi.index, ev)}
                  onDoubleClick={() => open(e)}
                  onContextMenu={(ev) => {
                    ev.preventDefault();
                    setActivePane(side);
                    setCursor(vi.index);
                    // Right-clicking outside the current selection targets
                    // just that row, as every file manager does.
                    if (marks.size && !marks.has(e.name)) setMarks(new Set([e.name]));
                    setMenu({ x: ev.clientX, y: ev.clientY, entry: e });
                  }}
                  role="row"
                  aria-rowindex={vi.index + 2}
                  aria-selected={marks.has(e.name)}
                >
                  <span className="row__name" title={e.name}>
                    <span className={e.isDir ? "row__icon row__icon--dir" : "row__icon"}>
                      <RowIcon size={15} />
                    </span>
                    {e.name}
                  </span>
                  {/* Folders carry the -1 size sentinel, which formats to an
                      empty string — a whole column of nothing on a seedbox
                      root reads as a column that cannot be sorted. */}
                  <span className="row__size">{e.isDir ? "—" : formatSize(e.size)}</span>
                  <span className="row__time">{formatTime(e.modTime)}</span>
                </div>
              );
            })}
          </div>
        </div>
        )}
        </div>
      )}
        </div>
      </div>

      {/* Every dismissable surface hands focus back: without it the list is
          keyboard-dead after F7, F2, a delete confirm or any menu action. */}
      {menu && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          items={buildMenu(menu.entry)}
          onClose={() => {
            setMenu(null);
            refocusList();
          }}
        />
      )}
      {bgMenu && (
        <ContextMenu
          x={bgMenu.x}
          y={bgMenu.y}
          items={buildBgMenu()}
          onClose={() => {
            setBgMenu(null);
            refocusList();
          }}
        />
      )}
      {pathMenu && (
        <ContextMenu
          x={pathMenu.x}
          y={pathMenu.y}
          items={buildPathMenu()}
          onClose={() => {
            setPathMenu(null);
            refocusList();
          }}
        />
      )}
      <PromptDialog
        spec={prompt}
        onClose={() => {
          setPrompt(null);
          refocusList();
        }}
      />

      <footer className="pane__status">
        <span>{all.length} items</span>
        {taHint && <span className="pane__typeahead">{taHint}</span>}
        {marks.size > 0 && (
          <span className="marked">
            {marks.size} marked · {formatSize(markedSize)}
          </span>
        )}
        {filter && <span>filtered</span>}
      </footer>
    </section>
  );
}
