import { useEffect, useState, useSyncExternalStore } from "react";
import { list, localRoots, type PaneSource } from "../ipc";
import {
  getChildren,
  getVersion,
  hasFailed,
  markFailed,
  isExpanded,
  setChildren,
  setExpanded,
  subscribe,
  type TreeNode as CachedNode,
} from "../lib/treeCache";
import { ChevronRight } from "./Icon";

interface DirTreeProps {
  /** Which sidebar this is. Expansion is remembered per pane; children are
      shared, because the same folder on the same server has the same
      subfolders whichever pane is looking at it. */
  pane: number | string;
  source: PaneSource;
  currentPath: string;
  onNavigate: (path: string) => void;
}

type Node = CachedNode;

function joinPath(parent: string, name: string): string {
  const sep = parent.includes("\\") ? "\\" : "/";
  return parent.endsWith(sep) ? parent + name : parent + sep + name;
}

function TreeNode({
  node,
  depth,
  pane,
  source,
  currentPath,
  onNavigate,
}: DirTreeProps & { node: Node; depth: number }) {
  // Everything starts collapsed, roots included: opening the sidebar should
  // show the shape of the drive, not fire a listing per root. Both the flag
  // and the children live in the module cache, so a branch the user opened
  // survives collapsing its parent and closing the sidebar entirely.
  useSyncExternalStore(subscribe, getVersion);
  const expanded = isExpanded(pane, source, node.path);
  const children = getChildren(source, node.path);
  // Read into render state, not just inside the effect: clearing the marker
  // has to be able to trigger a retry, and an effect cannot react to a value
  // it never lists as a dependency.
  const failed = hasFailed(source, node.path);

  useEffect(() => {
    if (!expanded || children !== null || failed) return;
    let stale = false;
    void list(source, node.path)
      .then((l) => {
        if (stale) return;
        setChildren(
          source,
          node.path,
          l.entries
            .filter((e) => e.isDir)
            .map((e) => ({ path: joinPath(l.path, e.name), label: e.name })),
        );
      })
      .catch(() => {
        // Recorded as a failure, NOT as an empty folder. Caching [] used to
        // make one network blip look like a folder with no subfolders; not
        // recording it at all makes an unreadable folder re-list on every
        // mount. The marker is cleared when the user opens the folder again,
        // or when anything invalidates it.
        if (!stale) markFailed(source, node.path);
      });
    return () => {
      stale = true;
    };
  }, [expanded, children, failed, source, node.path]);

  const isCurrent = currentPath === node.path;
  return (
    <>
      <button
        className={`tree__node ${isCurrent ? "tree__node--current" : ""}`}
        style={{ paddingLeft: 6 + depth * 12 }}
        onClick={() => {
          setExpanded(pane, source, node.path, true);
          onNavigate(node.path);
        }}
        title={node.path}
      >
        <span
          className="tree__chevron"
          onClick={(e) => {
            e.stopPropagation();
            // Collapsing keeps the children: re-opening a folder should not
            // cost a listing, which on a remote site is a round trip.
            setExpanded(pane, source, node.path, !expanded);
          }}
        >
          <ChevronRight
            size={11}
            className={`tree__chev ${expanded ? "tree__chev--open" : ""}`}
          />
        </span>
        <span className="tree__label">{node.label}</span>
      </button>
      {expanded &&
        children?.map((c) => (
          <TreeNode
            key={c.path}
            node={c}
            depth={depth + 1}
            pane={pane}
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
        // The source is part of the key because every remote site's root is
        // "/". Without it, switching a pane from one site to another reuses
        // the same component instance and shows the previous server's folders.
        <TreeNode key={`${String(props.source)}:${r.path}`} node={r} depth={0} {...props} />
      ))}
    </nav>
  );
}
