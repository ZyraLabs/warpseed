import { useEffect } from "react";
import CommandPalette from "./components/CommandPalette";
import FilePane from "./components/FilePane";
import HostKeyDialog from "./components/HostKeyDialog";
import QueueDock from "./components/QueueDock";
import QuickConnect from "./components/QuickConnect";
import SettingsDialog from "./components/SettingsDialog";
import Sparkline from "./components/Sparkline";
import Toasts from "./components/Toasts";
import { applyTheme, type ThemePref } from "./lib/theme";
import { localStart, on, schemaVersion, sites as fetchSites, type ConnState } from "./ipc";
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

  // Boot: home dirs, schema health, saved sites, backend event subscriptions.
  useEffect(() => {
    void schemaVersion().then(setDbSchemaVersion).catch(() => setDbSchemaVersion(0));
    void fetchSites().then(setSites).catch(() => undefined);
    // Settings are the source of truth for the theme and the folder local
    // panes open in; both are read once at boot.
    void import("./ipc").then(({ getSettings }) =>
      getSettings()
        .then(async (cfg) => {
          const pref = cfg["ui.theme"] as ThemePref;
          if (pref === "dark" || pref === "light" || pref === "system") applyTheme(pref);

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
    return offConn;
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

  const connectedCount = Object.values(connStates).filter((s) => s === "connected").length;

  return (
    <div className="app">
      <header className="app__header">
        <span className="app__mark">
          warp<span className="app__mark-accent">seed</span>
        </span>
        <span className="app__spacer" />
        <span className="kbd">Ctrl+K</span>
        <button className="btn btn--primary" onClick={() => setQuickConnect(true, activePane)}>
          Connect
        </button>
        <button className="btn" title="Settings (Ctrl+,)" aria-label="Settings" onClick={() => setSettingsOpen(true)}>
          ⚙
        </button>
      </header>

      <main className="app__panes">
        <FilePane side={0} />
        <FilePane side={1} />
      </main>

      <QueueDock />

      <footer className="app__statusbar">
        <span>
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
        <span className={dbVersion > 0 ? "" : "status--warn"}>
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
