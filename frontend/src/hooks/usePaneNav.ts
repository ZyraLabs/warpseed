import { useCallback, useEffect, useRef, useState } from "react";
import { list, type Listing, type PaneSource } from "../ipc";
import { useUiStore, type PaneSide } from "../store";

const SKELETON_DELAY_MS = 150; // ux-spec §7.12: no skeleton on fast listings

// Stale-while-revalidate listing cache (web patterns rule): revisited
// directories render instantly from cache while a fresh listing loads —
// the main perceived-speed fix for high-RTT remote browsing.
const listingCache = new Map<string, Listing>();
const CACHE_MAX = 300;

function cachePut(key: string, l: Listing) {
  if (listingCache.size >= CACHE_MAX) {
    const oldest = listingCache.keys().next().value;
    if (oldest !== undefined) listingCache.delete(oldest);
  }
  listingCache.delete(key);
  listingCache.set(key, l);
}

interface PaneNav {
  listing: Listing | null;
  error: string | null;
  loading: boolean;
  navigate: (path: string) => void;
  up: () => void;
  back: () => void;
  forward: () => void;
  canBack: boolean;
  canForward: boolean;
  reload: () => void;
}

/** Loads listings for one pane and keeps its 50-entry history (ux-spec §3.3). */
export function usePaneNav(side: PaneSide): PaneNav {
  const { source, path } = useUiStore((s) => s.panes[side]);
  const setPath = useUiStore((s) => s.setPath);

  // The loaded listing is tagged with the source that produced it and
  // matched during render, not in an effect. A pane must never expose a
  // remote listing while its source already says "local": every path the UI
  // builds from it — including the ones Delete acts on — would be wrong.
  const [loaded, setLoaded] = useState<{ source: PaneSource; listing: Listing } | null>(null);
  const [failure, setFailure] = useState<{ source: PaneSource; message: string } | null>(null);
  const [loading, setLoading] = useState(false);
  const [nonce, setNonce] = useState(0);

  const listing = loaded && loaded.source === source ? loaded.listing : null;
  const error = failure && failure.source === source ? failure.message : null;

  const setListing = useCallback(
    (l: Listing, from: PaneSource) => setLoaded({ source: from, listing: l }),
    [],
  );

  const hist = useRef<{ stack: string[]; idx: number }>({ stack: [], idx: -1 });
  const traveling = useRef(false);

  useEffect(() => {
    // Source switch resets history; the effect below loads the new path.
    hist.current = { stack: [], idx: -1 };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source]);

  useEffect(() => {
    if (!path) return;
    let stale = false;
    const key = `${String(source)}|${path}`;
    const cached = listingCache.get(key);
    if (cached) {
      // SWR: show the cached listing immediately, refresh in background.
      setListing(cached, source);
      setFailure(null);
    }
    const skeleton = setTimeout(() => !stale && !cached && setLoading(true), SKELETON_DELAY_MS);

    list(source, path)
      .then((l) => {
        if (stale) return;
        setListing(l, source);
        setFailure(null);
        cachePut(key, l);
        cachePut(`${String(source)}|${l.path}`, l);
        const h = hist.current;
        if (traveling.current) {
          traveling.current = false;
        } else if (h.stack[h.idx] !== l.path) {
          h.stack = [...h.stack.slice(0, h.idx + 1), l.path].slice(-50);
          h.idx = h.stack.length - 1;
        }
      })
      .catch((e: unknown) => {
        if (!stale) setFailure({ source, message: String(e) });
      })
      .finally(() => {
        if (!stale) {
          clearTimeout(skeleton);
          setLoading(false);
        }
      });
    return () => {
      stale = true;
      clearTimeout(skeleton);
    };
  }, [source, path, nonce]);

  const navigate = useCallback((p: string) => setPath(side, p), [setPath, side]);

  const up = useCallback(() => {
    if (listing?.parent) navigate(listing.parent);
  }, [listing, navigate]);

  const travel = useCallback(
    (delta: number) => {
      const h = hist.current;
      const idx = h.idx + delta;
      if (idx < 0 || idx >= h.stack.length) return;
      h.idx = idx;
      traveling.current = true;
      navigate(h.stack[idx]);
    },
    [navigate],
  );

  return {
    listing,
    error,
    loading,
    navigate,
    up,
    back: () => travel(-1),
    forward: () => travel(1),
    canBack: hist.current.idx > 0,
    canForward: hist.current.idx < hist.current.stack.length - 1,
    reload: () => setNonce((n) => n + 1),
  };
}
