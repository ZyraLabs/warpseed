import { useEffect, useRef, useState } from "react";

interface BreadcrumbProps {
  path: string;
  /** Navigate to a segment of the path already shown — always this source. */
  onNavigate: (path: string) => void;
  /** A path the user typed, which may not belong to this pane's source. */
  onSubmitPath: (path: string) => void;
  /** Increment to enter edit mode (Ctrl+L). */
  editReq: number;
}

interface Segment {
  label: string;
  target: string;
}

function split(path: string): Segment[] {
  const win = /^[A-Za-z]:[\\/]/.test(path);
  if (win) {
    const root = path.slice(0, 3).replace("/", "\\");
    const rest = path.slice(3).split(/[\\/]/).filter(Boolean);
    const segs: Segment[] = [{ label: root.slice(0, 2), target: root }];
    let acc = root;
    for (const part of rest) {
      acc = acc.endsWith("\\") ? acc + part : acc + "\\" + part;
      segs.push({ label: part, target: acc });
    }
    return segs;
  }
  const parts = path.split("/").filter(Boolean);
  const segs: Segment[] = [{ label: "/", target: "/" }];
  let acc = "";
  for (const part of parts) {
    acc += "/" + part;
    segs.push({ label: part, target: acc });
  }
  return segs;
}

/** Breadcrumb with inline edit (ux-spec §3.2). Overflow keeps root + last two. */
export default function Breadcrumb({ path, onNavigate, onSubmitPath, editReq }: BreadcrumbProps) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(path);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (editReq > 0) {
      setDraft(path);
      setEditing(true);
    }
  }, [editReq, path]);

  useEffect(() => {
    if (editing) {
      inputRef.current?.focus();
      inputRef.current?.select();
    }
  }, [editing]);

  if (editing) {
    return (
      <input
        ref={inputRef}
        className="crumbs__edit"
        value={draft}
        spellCheck={false}
        aria-label="Path"
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => setEditing(false)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            setEditing(false);
            if (draft.trim()) onSubmitPath(draft.trim());
          } else if (e.key === "Escape") {
            setEditing(false);
          }
          e.stopPropagation();
        }}
      />
    );
  }

  const segs = split(path);
  const collapsed = segs.length > 5;
  const shown = collapsed ? [segs[0], ...segs.slice(-2)] : segs;

  return (
    <div
      className="crumbs"
      onClick={(e) => {
        if (e.target === e.currentTarget) {
          setDraft(path);
          setEditing(true);
        }
      }}
      title={path}
    >
      {shown.map((seg, i) => (
        <span key={seg.target}>
          {i > 0 && <span className="crumb-sep">▸</span>}
          {collapsed && i === 1 && <span className="crumb-sep">…▸</span>}
          <button
            className={`crumb ${i === shown.length - 1 ? "crumb--last" : ""}`}
            onClick={() => onNavigate(seg.target)}
          >
            {seg.label}
          </button>
        </span>
      ))}
    </div>
  );
}
