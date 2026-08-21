import { useEffect } from "react";
import CommandPalette from "./components/CommandPalette";
import FilePane from "./components/FilePane";
import DeckView from "./components/DeckView";
import FlightView from "./components/FlightView";
import HostKeyDialog from "./components/HostKeyDialog";
import { Heart, Search, Sliders, Slipstream } from "./components/Icon";
import QueueDock from "./components/QueueDock";
import QuickConnect from "./components/QuickConnect";
import SettingsDialog from "./components/SettingsDialog";
import Sparkline from "./components/Sparkline";
import Toasts from "./components/Toasts";
import { COMPANY, DONATE_URL } from "./lib/branding";
import { applyTheme, type ThemePref } from "./lib/theme";
import {
  localStart,
  on,
  openExternal,
  schemaVersion,
  setSetting,
  sites as fetchSites,
  type ConnState,
  type TransferState,
} from "./ipc";
import { useUiStore } from "./store";
import "./App.css";

export default function App() {
  const setPane = useUiStore((s) => s.setPane);
  const activePane = useUiStore((s) => s.activePane);
  const setActivePane = useUiStore((s) => s.setActivePane);
  const dbVersion = useUiStore((s) => s.dbSchemaVersion);
  const setDbSchemaVersion = useUiStore((s) => s.setDbSchemaVersion);
  const setSites = useUiStore((s) => s.setSites);
  const setConnState = useUiStore((s) => s.setConnState);
  const setPaletteOpen = useUiStore((s) => s.setPaletteOpen);
  const setQuickConnect = useUiStore((s) => s.setQuickConnect);
  const setSettingsOpen = useUiStore((s) => s.setSettingsOpen);
  const connStates = useUiStore((s) => s.connStates);
  const siteList = useUiStore((s) => s.sites);
  const transfers = useUiStore((s) => s.transfers);
  const viewMode = useUiStore((s) => s.viewMode);
  const setViewMode = useUiStore((s) => s.setViewMode);

  // Boot: home dirs, schema health, saved sites, backend event subscriptions.
  useEffect(() => {
    void schemaVersion().then(setDbSchemaVersion).catch(() => setDbSchemaVersion(0));
    void fetchSites().then(setSites).catch(() => undefined);
    // null = still hydrating. A completion arriving before settings resolve
    // is buffered, so a transfer finishing at launch cannot swallow the
    // one-time nudge — while a failed settings read (donateNudged stays
    // null) still never re-nags a long-time user.
    let donateNudged: boolean | null = null;
    let sawCompletion = false;
    const maybeNudge = () => {
      if (donateNudged !== false || !sawCompletion) return;
      donateNudged = true;
      window.dispatchEvent(
        new CustomEvent("ws:toast", {
          detail: {
            kind: "success",
            text: "Enjoying warpseed? It's free forever — the ♥ in the status bar buys us a coffee.",
          },
        }),
      );
      void setSetting("ui.donate_nudged", "1").catch(() => undefined);
    };
    // Settings are the source of truth for the theme and the folder local
    // panes open in; both are read once at boot.
    void import("./lib/prefs").then(({ hydratePrefs }) =>
      hydratePrefs()
        .then(async (cfg) => {
          // applyTheme coerces legacy and unknown values itself.
          if (cfg["ui.theme"]) applyTheme(cfg["ui.theme"] as ThemePref);
          donateNudged = cfg["ui.donate_nudged"] === "1";
          maybeNudge();

          const start = await localStart(cfg["ui.local_default"]);
          setPane(0, "local", start);
          setPane(1, "local", start);
        })
        .catch(async () => {
          const home = await localStart();
          setPane(0, "local", home);
          setPane(1, "local", home);
        }),
    );
    const offConn = on<ConnState>("site:connstate", (c) => setConnState(c.siteId, c.state));
    // One-time nudge after the first transfer ever completes: point at the
    // status-bar heart, then never mention it again.
    const offDonate = on<TransferState>("transfer:state", (s) => {
      if (s.state !== "completed") return;
      sawCompletion = true;
      maybeNudge();
    });
    return () => {
      offConn();
      offDonate();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Global keys: Tab pane switch, Ctrl+K palette (ux-spec §3.1).
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const inField =
        e.target instanceof HTMLInputElement ||
        e.target instanceof HTMLSelectElement ||
        e.target instanceof HTMLTextAreaElement;
      if (e.ctrlKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen(!useUiStore.getState().paletteOpen);
      } else if (e.key === "Tab" && !inField && !useUiStore.getState().settingsOpen) {
        // Never hijack Tab while a dialog is open — that would trap keyboard
        // users inside it with no way to reach its buttons.
        e.preventDefault();
        setActivePane(useUiStore.getState().activePane === 0 ? 1 : 0);
      } else if (e.ctrlKey && e.key === ",") {
        e.preventDefault();
        setSettingsOpen(true);
      } else if (e.key === "Escape") {
        if (useUiStore.getState().settingsOpen) setSettingsOpen(false);
        else if (useUiStore.getState().quickConnect.open) setQuickConnect(false);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [setActivePane, setPaletteOpen, setQuickConnect, setSettingsOpen]);

  // The Browse/Flight toggle exists only while the queue holds live work.
  // Leaving flight mode is always the user's call — except when the toggle
  // itself disappears (queue emptied + cleared), which would strand them
  // on a view with no way back.
  const flightAvailable = transfers.some(
    (t) => t.state !== "completed" && t.state !== "cancelled",
  );
  const anyActive = transfers.some((t) => t.state === "active");
  useEffect(() => {
    if (!flightAvailable && useUiStore.getState().viewMode === "flight") {
      setViewMode("browse");
    }
  }, [flightAvailable, setViewMode]);

  const connectedCount = Object.values(connStates).filter((s) => s === "connected").length;

  return (
    <div className="app">
      <header className="app__header">
        <span className="app__mark">
          <Slipstream size={18} className="app__glyph" />
          warp<span className="app__mark-accent">seed</span>
        </span>
        <span className="app__spacer" />
        <div className="viewseg" aria-label="View mode">
          <button
            className={`viewseg__btn${viewMode === "deck" ? " viewseg__btn--active" : ""}`}
            aria-pressed={viewMode === "deck"}
            onClick={() => setViewMode("deck")}
          >
            Deck
          </button>
          <button
            className={`viewseg__btn${viewMode === "browse" ? " viewseg__btn--active" : ""}`}
            aria-pressed={viewMode === "browse"}
            onClick={() => setViewMode("browse")}
          >
            Browse
          </button>
          {flightAvailable && (
            <button
              className={`viewseg__btn${viewMode === "flight" ? " viewseg__btn--active" : ""}`}
              aria-pressed={viewMode === "flight"}
              onClick={() => setViewMode("flight")}
            >
              Flight
              {anyActive && <span className="viewseg__dot" aria-hidden="true" />}
            </button>
          )}
        </div>
        <button
          className="omnibar"
          onClick={() => setPaletteOpen(true)}
          aria-label="Search files, sites — or type a command (Ctrl+K)"
        >
          <Search size={14} className="omnibar__icon" />
          <span className="omnibar__hint">Search files, sites — or type a command…</span>
          <span className="kbd">Ctrl K</span>
        </button>
        <button className="btn btn--primary" onClick={() => setQuickConnect(true, activePane)}>
          Connect
        </button>
        <button
          className="btn btn--icon"
          title="Settings (Ctrl+,)"
          aria-label="Settings"
          onClick={() => setSettingsOpen(true)}
        >
          <Sliders size={15} />
        </button>
      </header>

      <main className="app__main">
        {/* Panes stay mounted (display:none) in flight mode so pane state,
            scroll position and virtualizer measurements survive the trip. */}
        <div className="app__panes" hidden={viewMode !== "browse"}>
          <FilePane side={0} />
          <FilePane side={1} />
        </div>
        {viewMode === "flight" && <FlightView />}
        {viewMode === "deck" && <DeckView />}
      </main>

      <QueueDock />

      <footer className="app__statusbar">
        <span className="statusbar__conn">
          {connectedCount > 0 ? (
            <>
              <span className="conn-dot conn-dot--connected" />
              {connectedCount} site{connectedCount > 1 ? "s" : ""} connected
            </>
          ) : (
            <>
              <span className="conn-dot" />
              offline · {siteList.length} saved site{siteList.length === 1 ? "" : "s"}
            </>
          )}
        </span>
        <Sparkline />
        <span className="spacer" />
        <button
          className="statusbar__heart"
          title={`Support warpseed — buy ${COMPANY} a coffee`}
          aria-label="Support warpseed development"
          onClick={() => openExternal(DONATE_URL)}
        >
          <Heart size={13} />
        </button>
        <span className={`statusbar__db${dbVersion > 0 ? "" : " status--warn"}`}>
          {dbVersion > 0 ? `db v${dbVersion}` : "db unavailable"}
        </span>
      </footer>

      <CommandPalette />
      <QuickConnect />
      <SettingsDialog />
      <HostKeyDialog />
      <Toasts />
    </div>
  );
}
