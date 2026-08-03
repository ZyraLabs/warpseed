import { useCallback, useEffect, useRef, useState } from "react";
import { getPref, onPrefsHydrated, setPref, type PrefKey } from "../lib/prefs";

/* Draggable column widths, persisted so a layout the user tuned survives
   restarts. Widths are applied through CSS custom properties, so dragging
   never re-renders the virtualized rows underneath. */

export interface ColumnSpec {
  id: string;
  label: string;
  min: number;
  initial: number;
}

/** storageKey scopes the saved layout, so the queue and the file panes each
    remember their own column widths. */
export function useColumnWidths(columns: ColumnSpec[], storageKey: PrefKey) {
  const KEY = storageKey;
  const defaults = () =>
    Object.fromEntries(columns.map((c) => [c.id, c.initial])) as Record<string, number>;

  const [widths, setWidths] = useState<Record<string, number>>(() => {
    try {
      const raw = getPref(KEY);
      if (!raw) return defaults();
      const saved = JSON.parse(raw) as Record<string, number>;
      // Merge rather than replace: a column added in a later version must
      // still get its default instead of collapsing to zero.
      return { ...defaults(), ...saved };
    } catch {
      return defaults();
    }
  });

  const drag = useRef<{ id: string; startX: number; startWidth: number } | null>(null);
  const touched = useRef(false);

  // The first render reads a possibly-cold mirror; adopt what the database
  // held once it arrives, unless the user has already dragged something.
  useEffect(
    () =>
      onPrefsHydrated(() => {
        if (touched.current) return;
        const raw = getPref(KEY);
        if (!raw) return;
        try {
          setWidths({ ...defaults(), ...(JSON.parse(raw) as Record<string, number>) });
        } catch {
          // keep what we have
        }
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [KEY],
  );

  const persist = useCallback(
    (next: Record<string, number>) => setPref(KEY, JSON.stringify(next)),
    [KEY],
  );

  const startResize = useCallback(
    (id: string, event: React.MouseEvent) => {
      event.preventDefault();
      event.stopPropagation();
      touched.current = true;
      drag.current = { id, startX: event.clientX, startWidth: widths[id] };
    },
    [widths],
  );

  useEffect(() => {
    const move = (e: MouseEvent) => {
      const d = drag.current;
      if (!d) return;
      const spec = columns.find((c) => c.id === d.id);
      const min = spec?.min ?? 40;
      const next = Math.max(min, d.startWidth + (e.clientX - d.startX));
      setWidths((w) => (w[d.id] === next ? w : { ...w, [d.id]: next }));
    };
    const up = () => {
      if (!drag.current) return;
      drag.current = null;
      setWidths((w) => {
        persist(w);
        return w;
      });
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
    return () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
    };
  }, [columns, persist]);

  const reset = useCallback(() => {
    const d = defaults();
    setWidths(d);
    persist(d);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [persist]);

  /** CSS variables consumed by the row grid template. */
  const style = Object.fromEntries(
    columns.map((c) => [`--col-${c.id}`, `${widths[c.id]}px`]),
  ) as React.CSSProperties;

  return { widths, style, startResize, reset };
}
