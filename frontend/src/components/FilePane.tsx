import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { usePaneNav } from "../hooks/usePaneNav";
import {
  connectAndHome,
  deleteEntries,
  enqueueDownloads,
  enqueueUploads,
  localHome,
  makeDir,
  on,
  renameEntry,
  type FsChanged,
  type FsEntry,
} from "../ipc";
import { formatSize, formatTime } from "../lib/format";
import { useUiStore, type PaneSide } from "../store";
import Breadcrumb from "./Breadcrumb";
import ContextMenu, { type MenuItem } from "./ContextMenu";
import DirTree from "./DirTree";
import PromptDialog, { type PromptSpec } from "./PromptDialog";

function toast(kind: "info" | "error" | "success", text: string) {
  window.dispatchEvent(new CustomEvent("ws:toast", { detail: { kind, text } }));
}

/** Bound on how often a pane re-lists in response to filesystem events. */
const FS_REFRESH_COALESCE_MS = 500;

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
  const [cursor, setCursor] = useState(0);
  const [marks, setMarks] = useState<Set<string>>(new Set());
  const [filter, setFilter] = useState<string | null>(null);
  const [editReq, setEditReq] = useState(0);
  const [treeOpen, setTreeOpen] = useState(false);
  const [menu, setMenu] = useState<{ x: number; y: number; entry: FsEntry } | null>(null);
  const [prompt, setPrompt] = useState<PromptSpec | null>(null);

  const scrollRef = useRef<HTMLDivElement>(null);
  const filterRef = useRef<HTMLInputElement>(null);

  const all = nav.listing?.entries ?? [];
  const entries = useMemo(
    () => (filter ? all.filter((e) => matches(e.name, filter)) : all),
    [all, filter],
  );

  // Reset selection state on navigation.
  useEffect(() => {
    setCursor(0);
    setMarks(new Set());
    setFilter(null);
    setMenu(null); // a menu left open would act on the previous directory
    scrollRef.current?.scrollTo({ top: 0 });
  }, [nav.listing?.path]);

  useEffect(() => {
    if (filter !== null) filterRef.current?.focus();
  }, [filter !== null]); // eslint-disable-line react-hooks/exhaustive-deps

  const virtualizer = useVirtualizer({
    count: entries.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 26,
    overscan: 20,
  });

  const moveCursor = useCallback(
    (n: number) => {
      setCursor(n);
      virtualizer.scrollToIndex(n);
    },
    [virtualizer],
  );

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

      // Compare paths, not strings: the two sides can disagree on separator
      // style and trailing slashes, and Windows paths are case-insensitive.
      const norm = (p: string) => p.replace(/\\/g, "/").replace(/\/+$/, "").toLowerCase();
      const shown = norm(here);
      const changed = norm(ev.dir);
      // Also match changes BELOW this directory: a folder transfer writes
      // into a new subtree, and the pane showing the parent is exactly where
      // the user is waiting for that folder to appear.
      if (changed !== shown && !changed.startsWith(shown + "/")) return;

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
      if (cmd === "up") nav.up();
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
  }, [side, nav, invertMarks, doMkdir, doRename, doDelete, selection]);

  // Active-pane shortcuts that must work outside the list focus.
  useEffect(() => {
    if (!isActive) return;
    const handler = (e: KeyboardEvent) => {
      const inField =
        e.target instanceof HTMLInputElement || e.target instanceof HTMLSelectElement;
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
        nav.up();
      } else if (e.key === "F5") {
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
    source,
    enqueueItems,
    uploadItems,
    selection,
    doRename,
    doMkdir,
    doDelete,
  ]);

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
    if (!entries.length && !["Backspace"].includes(ev.key)) return;
    const e = entries[cursor];
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      moveCursor(Math.min(cursor + 1, entries.length - 1));
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      moveCursor(Math.max(cursor - 1, 0));
    } else if (ev.key === "Home") {
      ev.preventDefault();
      moveCursor(0);
    } else if (ev.key === "End") {
      ev.preventDefault();
      moveCursor(entries.length - 1);
    } else if (ev.key === "PageDown") {
      ev.preventDefault();
      moveCursor(Math.min(cursor + 20, entries.length - 1));
    } else if (ev.key === "PageUp") {
      ev.preventDefault();
      moveCursor(Math.max(cursor - 20, 0));
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      if (e) open(e);
    } else if (ev.key === "Backspace") {
      ev.preventDefault();
      nav.up();
    } else if (ev.key === "Insert") {
      ev.preventDefault();
      if (e) {
        toggleMark(e.name);
        moveCursor(Math.min(cursor + 1, entries.length - 1));
      }
    } else if (ev.key === " ") {
      ev.preventDefault();
      if (e) toggleMark(e.name);
    } else if (ev.key === "*") {
      ev.preventDefault();
      invertMarks();
    } else if (ev.ctrlKey && ev.shiftKey && ev.key.toLowerCase() === "a") {
      ev.preventDefault();
      setMarks(new Set());
    } else if (ev.ctrlKey && ev.key.toLowerCase() === "a") {
      ev.preventDefault();
      setMarks(new Set(entries.map((x) => x.name)));
    } else if (ev.key.length === 1 && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
      // typing opens the filter strip (ux-spec §3.5)
      ev.preventDefault();
      setFilter((f) => (f ?? "") + ev.key);
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

  const switchSource = async (value: string) => {
    if (value === "local") {
      const home = await localHome().catch(() => "/");
      setPane(side, "local", home);
    } else if (value === "__new__") {
      setQuickConnect(true, side);
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
          ≡
        </button>
        <select
          className="pane__source"
          value={typeof source === "number" ? String(source) : "local"}
          onChange={(e) => void switchSource(e.target.value)}
          aria-label="Pane source"
        >
          <option value="local">This PC</option>
          {sites.map((s) => (
            <option key={s.id} value={s.id}>
              ⇅ {s.name}
            </option>
          ))}
          <option value="__new__">+ Connect…</option>
        </select>
        {siteName && (
          <span
            className={`conn-dot conn-dot--${connState ?? "disconnected"}`}
            title={`${siteName}: ${connState ?? "disconnected"}`}
          />
        )}
        <button className="pane__btn" onClick={nav.back} disabled={!nav.canBack} title="Back (Alt+←)">
          ‹
        </button>
        <button
          className="pane__btn"
          onClick={nav.forward}
          disabled={!nav.canForward}
          title="Forward (Alt+→)"
        >
          ›
        </button>
        <button
          className="pane__btn"
          onClick={nav.up}
          disabled={!nav.listing?.parent}
          title="Up (Backspace)"
        >
          ↑
        </button>
        <button className="pane__btn" onClick={nav.reload} title="Refresh (Ctrl+R)">
          ↻
        </button>
        <Breadcrumb path={nav.listing?.path ?? path} onNavigate={nav.navigate} editReq={editReq} />
      </header>

      {filter !== null && (
        <div className="pane__filter">
          <input
            ref={filterRef}
            value={filter}
            placeholder="filter… (* and ? glob)"
            onChange={(e) => setFilter(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setFilter(null);
                scrollRef.current?.focus();
              } else if (e.key === "Enter") {
                scrollRef.current?.focus();
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
            <p>{nav.error}</p>
            <div>
              <button className="btn" onClick={nav.reload}>
                Retry
              </button>
              <button className="btn" onClick={nav.up}>
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
      ) : entries.length === 0 ? (
        <div className="pane__empty">
          <div>{filter ? `No matches for “${filter}”` : "Empty directory"}</div>
          <div>{filter ? "Esc to clear" : ""}</div>
        </div>
      ) : (
        <div className="pane__scroll" ref={scrollRef} tabIndex={0} onKeyDown={onListKeyDown} role="grid">
          <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
            {virtualizer.getVirtualItems().map((vi) => {
              const e = entries[vi.index];
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
                  onClick={(ev) => {
                    if (ev.ctrlKey) toggleMark(e.name);
                    setCursor(vi.index);
                  }}
                  onDoubleClick={() => open(e)}
                  onContextMenu={(ev) => {
                    ev.preventDefault();
                    setActivePane(side);
                    setCursor(vi.index);
                    setMenu({ x: ev.clientX, y: ev.clientY, entry: e });
                  }}
                  role="row"
                  aria-selected={marks.has(e.name)}
                >
                  <span className="row__name">
                    <span className="row__icon">{e.isDir ? "▸" : "·"}</span>
                    {e.name}
                  </span>
                  <span className="row__size">{formatSize(e.size)}</span>
                  <span className="row__time">{formatTime(e.modTime)}</span>
                </div>
              );
            })}
          </div>
        </div>
      )}
        </div>
      </div>

      {menu && (
        <ContextMenu x={menu.x} y={menu.y} items={buildMenu(menu.entry)} onClose={() => setMenu(null)} />
      )}
      <PromptDialog spec={prompt} onClose={() => setPrompt(null)} />

      <footer className="pane__status">
        <span>{all.length} items</span>
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
