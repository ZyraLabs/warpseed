import { useEffect, useState, useSyncExternalStore } from "react";
import { list, localRoots, remoteHome, type PaneSource } from "../ipc";
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
import { useColumnWidths, type ColumnSpec } from "../hooks/useColumnWidths";
import { useUiStore } from "../store";

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

/** One draggable dimension, reusing the pane and queue column machinery so
    the sidebar resizes and persists exactly the way those columns do. */
const TREE_COLUMN: ColumnSpec[] = [{ id: "tree-w", label: "Folder tree", min: 120, initial: 190 }];

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

  const currentPath = props.currentPath;
  useEffect(() => {
    const source = props.source;
    if (source === "local") {
      void localRoots()
        .then((rs) => setRoots(rs.map((r) => ({ path: r.path, label: r.label }))))
        .catch(() => setRoots([]));
      return;
    }
    // A remote tree used to start at "/", which on a seedbox is usually not
    // listable at all — the sidebar opened onto a folder that could never be
    // expanded. Start where the PANE starts instead: the site's configured
    // folder, then the account's home, then wherever the pane already is
    // (which is listable by definition, because it is on screen).
    let stale = false;
    const site = useUiStore.getState().sites.find((s) => s.id === source);
    const configured = site?.remotePath?.trim();
    const resolve = async (): Promise<string | null> => {
      if (configured) {
        try {
          await list(source, configured);
          return configured;
        } catch {
          // a stale configured path must not wedge the tree
        }
      }
      try {
        return await remoteHome(source);
      } catch {
        return currentPath || null;
      }
    };
    void resolve().then((root) => {
      if (stale) return;
      // No usable root is better than a dead "/" the user can only click at.
      setRoots(root ? [{ path: root, label: root }] : []);
    });
    return () => {
      stale = true;
    };
    // currentPath is the last-resort fallback only; re-resolving the root
    // every time the user navigates would move the tree under them.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.source]);

  // Same drag mechanics as the column grips: the width is a CSS variable, so
  // dragging never re-renders the tree underneath.
  const { style: treeStyle, startResize } = useColumnWidths(TREE_COLUMN, "ui.tree_width");

  return (
    <nav className="tree" aria-label="Folder tree" style={treeStyle}>
      <span
        className="tree__grip"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize folder tree"
        onMouseDown={(e) => startResize("tree-w", e)}
      />
      {roots.map((r) => (
        // The source is part of the key because every remote site's root is
        // "/". Without it, switching a pane from one site to another reuses
        // the same component instance and shows the previous server's folders.
        <TreeNode key={`${String(props.source)}:${r.path}`} node={r} depth={0} {...props} />
      ))}
    </nav>
  );
}
