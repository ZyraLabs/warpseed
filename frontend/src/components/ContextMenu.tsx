import { useEffect, useRef } from "react";

export interface MenuItem {
  label: string;
  hint?: string;
  danger?: boolean;
  disabled?: boolean;
  run: () => void;
}

interface ContextMenuProps {
  x: number;
  y: number;
  items: MenuItem[];
  onClose: () => void;
}

/** Right-click menu (ux-spec §7.7): Level-1 surface, keyboard-dismissable,
    clamped to the viewport. */
export default function ContextMenu({ x, y, items, onClose }: ContextMenuProps) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (el) {
      // clamp inside the window
      const r = el.getBoundingClientRect();
      if (x + r.width > window.innerWidth) el.style.left = `${x - r.width}px`;
      if (y + r.height > window.innerHeight) el.style.top = `${y - r.height}px`;
    }
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    const click = () => onClose();
    window.addEventListener("keydown", key);
    window.addEventListener("mousedown", click);
    window.addEventListener("blur", click);
    return () => {
      window.removeEventListener("keydown", key);
      window.removeEventListener("mousedown", click);
      window.removeEventListener("blur", click);
    };
  }, [x, y, onClose]);

  return (
    <div
      ref={ref}
      className="ctxmenu"
      style={{ left: x, top: y }}
      role="menu"
      onMouseDown={(e) => e.stopPropagation()}
    >
      {items.map((it) => (
        <button
          key={it.label}
          className={`ctxmenu__item ${it.danger ? "ctxmenu__item--danger" : ""}`}
          disabled={it.disabled}
          role="menuitem"
          onClick={() => {
            onClose();
            it.run();
          }}
        >
          <span className="grow">{it.label}</span>
          {it.hint && <span className="ctxmenu__hint">{it.hint}</span>}
        </button>
      ))}
    </div>
  );
}
