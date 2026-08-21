import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { connectAndHome, disconnectSite, getSettings, localStart } from "../ipc";
import { useUiStore } from "../store";
import type { PaneCmd } from "./FilePane";
import {
  ArrowUp,
  Check,
  ChevronRight,
  Close,
  Folder,
  File,
  Monitor,
  Refresh,
  Search,
  Sliders,
  Tree,
  Warning,
} from "./Icon";

interface Item {
  label: string;
  icon?: ReactNode;
  hint?: string;
  run: () => void | Promise<void>;
}

function paneCmd(side: 0 | 1, cmd: PaneCmd["cmd"]) {
  window.dispatchEvent(new CustomEvent("ws:panecmd", { detail: { side, cmd } }));
}

/** Ctrl+K command palette — ux-spec §7.10. Commands + sites, fuzzy-filtered. */
export default function CommandPalette() {
  const open = useUiStore((s) => s.paletteOpen);
  const setOpen = useUiStore((s) => s.setPaletteOpen);
  const active = useUiStore((s) => s.activePane);
  const sites = useUiStore((s) => s.sites);
  const connStates = useUiStore((s) => s.connStates);
  const setPane = useUiStore((s) => s.setPane);
  const setQuickConnect = useUiStore((s) => s.setQuickConnect);

  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) {
      setQuery("");
      setSel(0);
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  const items = useMemo<Item[]>(() => {
    const close = (fn: () => void | Promise<void>) => () => {
      setOpen(false);
      void fn();
    };
    const base: Item[] = [
      { label: "Go up", icon: <ArrowUp size={15} />, hint: "Backspace", run: close(() => paneCmd(active, "up")) },
      { label: "Edit path", icon: <Tree size={15} />, hint: "Ctrl+L", run: close(() => paneCmd(active, "editpath")) },
      { label: "Filter listing", icon: <Search size={15} />, hint: "Ctrl+F", run: close(() => paneCmd(active, "filter")) },
      { label: "Refresh listing", icon: <Refresh size={15} />, hint: "Ctrl+R", run: close(() => paneCmd(active, "reload")) },
      { label: "New folder…", icon: <Folder size={15} />, hint: "F7", run: close(() => paneCmd(active, "mkdir")) },
      { label: "Rename…", icon: <File size={15} />, hint: "F2", run: close(() => paneCmd(active, "rename")) },
      { label: "Delete selected", icon: <Warning size={15} />, hint: "Del", run: close(() => paneCmd(active, "delete")) },
      { label: "Invert marks", icon: <Check size={15} />, hint: "*", run: close(() => paneCmd(active, "invert")) },
      { label: "Deselect all", icon: <Close size={15} />, hint: "Ctrl+Shift+A", run: close(() => paneCmd(active, "clearmarks")) },
      {
        label: "Switch pane to This PC",
        icon: <Monitor size={15} />,
        run: close(async () => {
          const cfg = await getSettings().catch(() => ({}) as Record<string, string>);
          setPane(active, "local", await localStart(cfg["ui.local_default"]));
        }),
      },
      { label: "New connection…", icon: <ChevronRight size={15} />, run: close(() => setQuickConnect(true, active)) },
      {
        label: "Settings",
        icon: <Sliders size={15} />,
        hint: "Ctrl+,",
        run: close(() => useUiStore.getState().setSettingsOpen(true)),
      },
    ];
    for (const s of sites) {
      base.push({
        label: `Connect: ${s.name}`,
        icon: <ChevronRight size={15} />,
        hint: `${s.username}@${s.host}`,
        run: close(async () => {
          try {
            const home = await connectAndHome(s.id, s.remotePath);
            setPane(active, s.id, home);
          } catch (err) {
            window.dispatchEvent(
              new CustomEvent("ws:toast", { detail: { kind: "error", text: String(err) } }),
            );
          }
        }),
      });
      if (connStates[s.id] === "connected") {
        base.push({
          label: `Disconnect: ${s.name}`,
          icon: <Close size={15} />,
          run: close(() => disconnectSite(s.id)),
        });
      }
    }
    return base;
  }, [active, sites, connStates, setOpen, setPane, setQuickConnect]);

  const filtered = useMemo(() => {
    if (!query) return items;
    const q = query.toLowerCase();
    return items
      .map((it) => {
        const l = it.label.toLowerCase();
        let score = -1;
        if (l.startsWith(q)) score = 0;
        else if (l.includes(q)) score = 1;
        else if (it.hint?.toLowerCase().includes(q)) score = 2;
        return { it, score };
      })
      .filter((x) => x.score >= 0)
      .sort((a, b) => a.score - b.score)
      .map((x) => x.it);
  }, [items, query]);

  useEffect(() => setSel(0), [query]);

  if (!open) return null;

  return (
    <div className="scrim" onMouseDown={() => setOpen(false)}>
      <div className="palette" onMouseDown={(e) => e.stopPropagation()} role="dialog" aria-label="Command palette">
        <div className="palette__search">
          <Search size={15} />
          <input
            ref={inputRef}
            value={query}
            placeholder="Type a command or site name…"
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setOpen(false);
              else if (e.key === "ArrowDown") {
                e.preventDefault();
                setSel((s) => Math.min(s + 1, filtered.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setSel((s) => Math.max(s - 1, 0));
              } else if (e.key === "Enter" && filtered[sel]) {
                void filtered[sel].run();
              }
            }}
          />
        </div>
        <div className="palette__list">
          {filtered.length === 0 ? (
            <div className="palette__empty">No matching commands</div>
          ) : (
            filtered.map((it, i) => (
              <button
                key={it.label}
                className={`palette__item ${i === sel ? "palette__item--sel" : ""}`}
                onMouseEnter={() => setSel(i)}
                onClick={() => void it.run()}
              >
                <span className="palette__icon">{it.icon}</span>
                <span className="grow">{it.label}</span>
                {it.hint && <span className="palette__hint">{it.hint}</span>}
              </button>
            ))
          )}
        </div>
      </div>
    </div>
  );
}
