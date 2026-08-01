import { useCallback, useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { listLocal, localRoots, type FsEntry, type FsRoot, type Listing } from "../ipc";
import { useUiStore, type PaneSide } from "../store";

function formatSize(size: number): string {
  if (size < 0) return "";
  if (size < 1024) return `${size} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = size;
  let u = -1;
  do {
    v /= 1024;
    u++;
  } while (v >= 1024 && u < units.length - 1);
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[u]}`;
}

function formatTime(rfc3339: string): string {
  return rfc3339.replace("T", " ").replace(/:\d\dZ$/, "");
}

export default function FilePane({ side }: { side: PaneSide }) {
  const path = useUiStore((s) => s.panes[side].path);
  const setPath = useUiStore((s) => s.setPath);
  const isActive = useUiStore((s) => s.activePane === side);
  const setActivePane = useUiStore((s) => s.setActivePane);

  const [listing, setListing] = useState<Listing | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [roots, setRoots] = useState<FsRoot[]>([]);
  const [cursor, setCursor] = useState(0);
  const [pathDraft, setPathDraft] = useState("");

  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    localRoots().then(setRoots).catch(() => setRoots([]));
  }, []);

  useEffect(() => {
    if (!path) return;
    let stale = false;
    listLocal(path)
      .then((l) => {
        if (stale) return;
        setListing(l);
        setError(null);
        setCursor(0);
        setPathDraft(l.path);
        scrollRef.current?.scrollTo({ top: 0 });
      })
      .catch((e: unknown) => {
        if (!stale) setError(String(e));
      });
    return () => {
      stale = true;
    };
  }, [path]);

  const entries = listing?.entries ?? [];
  const virtualizer = useVirtualizer({
    count: entries.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 26,
    overscan: 20,
  });

  const open = useCallback(
    (e: FsEntry) => {
      if (!listing) return;
      if (e.isDir) {
        const sep = listing.path.includes("\\") ? "\\" : "/";
        const base = listing.path.endsWith(sep) ? listing.path : listing.path + sep;
        setPath(side, base + e.name);
      }
      // files: transfer enqueueing arrives in Phase 1
    },
    [listing, setPath, side],
  );

  const goUp = useCallback(() => {
    if (listing?.parent) setPath(side, listing.parent);
  }, [listing, setPath, side]);

  const onKeyDown = (ev: React.KeyboardEvent) => {
    if (!entries.length) return;
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      const n = Math.min(cursor + 1, entries.length - 1);
      setCursor(n);
      virtualizer.scrollToIndex(n);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      const n = Math.max(cursor - 1, 0);
      setCursor(n);
      virtualizer.scrollToIndex(n);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      open(entries[cursor]);
    } else if (ev.key === "Backspace") {
      ev.preventDefault();
      goUp();
    }
  };

  return (
    <section
      className={`pane ${isActive ? "pane--active" : ""}`}
      onMouseDown={() => setActivePane(side)}
      aria-label={`File pane ${side + 1}`}
    >
      <header className="pane__bar">
        {roots.length > 1 && (
          <select
            className="pane__roots"
            value=""
            onChange={(ev) => ev.target.value && setPath(side, ev.target.value)}
            aria-label="Drive"
          >
            <option value="">◈</option>
            {roots.map((r) => (
              <option key={r.path} value={r.path}>
                {r.label}
              </option>
            ))}
          </select>
        )}
        <button className="pane__up" onClick={goUp} disabled={!listing?.parent} aria-label="Parent directory">
          ↑
        </button>
        <input
          className="pane__path"
          value={pathDraft}
          onChange={(ev) => setPathDraft(ev.target.value)}
          onKeyDown={(ev) => ev.key === "Enter" && setPath(side, pathDraft)}
          spellCheck={false}
          aria-label="Path"
        />
      </header>

      {error ? (
        <div className="pane__error" role="alert">
          {error}
        </div>
      ) : (
        <div className="pane__scroll" ref={scrollRef} tabIndex={0} onKeyDown={onKeyDown}>
          <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
            {virtualizer.getVirtualItems().map((vi) => {
              const e = entries[vi.index];
              return (
                <div
                  key={vi.key}
                  className={`row ${e.isDir ? "row--dir" : ""} ${vi.index === cursor ? "row--cursor" : ""}`}
                  style={{ transform: `translateY(${vi.start}px)` }}
                  onClick={() => setCursor(vi.index)}
                  onDoubleClick={() => open(e)}
                >
                  <span className="row__name">
                    {e.isDir ? "▸ " : "  "}
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

      <footer className="pane__status">
        {listing ? `${entries.length} items` : "…"}
      </footer>
    </section>
  );
}
