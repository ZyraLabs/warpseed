import { useCallback, useEffect, useRef, useState } from "react";

/* Draggable column widths, persisted so a layout the user tuned survives
   restarts. Widths are applied through CSS custom properties, so dragging
   never re-renders the virtualized rows underneath. */

const KEY = "ws-queue-columns";

export interface ColumnSpec {
  id: string;
  label: string;
  min: number;
  initial: number;
}

export function useColumnWidths(columns: ColumnSpec[]) {
  const defaults = () =>
    Object.fromEntries(columns.map((c) => [c.id, c.initial])) as Record<string, number>;

  const [widths, setWidths] = useState<Record<string, number>>(() => {
    try {
      const raw = localStorage.getItem(KEY);
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

  const persist = useCallback((next: Record<string, number>) => {
    try {
      localStorage.setItem(KEY, JSON.stringify(next));
    } catch {
      // Layout preferences are not worth failing a render over.
    }
  }, []);

  const startResize = useCallback(
    (id: string, event: React.MouseEvent) => {
      event.preventDefault();
      event.stopPropagation();
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
