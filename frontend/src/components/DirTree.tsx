import { useEffect, useState } from "react";
import { list, localRoots, type PaneSource } from "../ipc";

interface DirTreeProps {
  source: PaneSource;
  currentPath: string;
  onNavigate: (path: string) => void;
}

interface Node {
  path: string;
  label: string;
}

function joinPath(parent: string, name: string): string {
  const sep = parent.includes("\\") ? "\\" : "/";
  return parent.endsWith(sep) ? parent + name : parent + sep + name;
}

function TreeNode({
  node,
  depth,
  source,
  currentPath,
  onNavigate,
}: DirTreeProps & { node: Node; depth: number }) {
  const [expanded, setExpanded] = useState(depth === 0);
  const [children, setChildren] = useState<Node[] | null>(null);

  useEffect(() => {
    if (!expanded || children !== null) return;
    let stale = false;
    list(source, node.path)
      .then((l) => {
        if (stale) return;
        setChildren(
          l.entries
            .filter((e) => e.isDir)
            .map((e) => ({ path: joinPath(l.path, e.name), label: e.name })),
        );
      })
      .catch(() => !stale && setChildren([]));
    return () => {
      stale = true;
    };
  }, [expanded, children, source, node.path]);

  const isCurrent = currentPath === node.path;
  return (
    <>
      <button
        className={`tree__node ${isCurrent ? "tree__node--current" : ""}`}
        style={{ paddingLeft: 6 + depth * 12 }}
        onClick={() => {
          setExpanded(true);
          onNavigate(node.path);
        }}
        title={node.path}
      >
        <span
          className="tree__chevron"
          onClick={(e) => {
            e.stopPropagation();
            setExpanded((x) => !x);
            if (expanded) setChildren(null); // re-list on next expand
          }}
        >
          {children === null && !expanded ? "▸" : expanded ? "▾" : "▸"}
        </span>
        <span className="tree__label">{node.label}</span>
      </button>
      {expanded &&
        children?.map((c) => (
          <TreeNode
            key={c.path}
            node={c}
            depth={depth + 1}
            source={source}
            currentPath={currentPath}
            onNavigate={onNavigate}
          />
        ))}
    </>
  );
}

/** Lazy folder tree sidebar — whole-drive structure at a glance (user
    request; CuteFTP lineage). Works identically for local drives and
    remote sites. */
export default function DirTree(props: DirTreeProps) {
  const [roots, setRoots] = useState<Node[]>([]);

  useEffect(() => {
    if (props.source === "local") {
      void localRoots()
        .then((rs) => setRoots(rs.map((r) => ({ path: r.path, label: r.label }))))
        .catch(() => setRoots([]));
    } else {
      setRoots([{ path: "/", label: "/" }]);
    }
  }, [props.source]);

  return (
    <nav className="tree" aria-label="Folder tree">
      {roots.map((r) => (
        <TreeNode key={r.path} node={r} depth={0} {...props} />
      ))}
    </nav>
  );
}
