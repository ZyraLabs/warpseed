import { useEffect } from "react";
import CommandPalette from "./components/CommandPalette";
import FilePane from "./components/FilePane";
import HostKeyDialog from "./components/HostKeyDialog";
import QuickConnect from "./components/QuickConnect";
import Toasts from "./components/Toasts";
import { localHome, on, schemaVersion, sites as fetchSites, type ConnState } from "./ipc";
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
  const connStates = useUiStore((s) => s.connStates);
  const siteList = useUiStore((s) => s.sites);

  // Boot: home dirs, schema health, saved sites, backend event subscriptions.
  useEffect(() => {
    void localHome()
      .then((home) => {
        setPane(0, "local", home);
        setPane(1, "local", home);
      })
      .catch(() => {
        setPane(0, "local", "/");
        setPane(1, "local", "/");
      });
    void schemaVersion().then(setDbSchemaVersion).catch(() => setDbSchemaVersion(0));
    void fetchSites().then(setSites).catch(() => undefined);
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
      } else if (e.key === "Tab" && !inField) {
        e.preventDefault();
        setActivePane(useUiStore.getState().activePane === 0 ? 1 : 0);
      } else if (e.key === "Escape" && useUiStore.getState().quickConnect.open) {
        setQuickConnect(false);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [setActivePane, setPaletteOpen, setQuickConnect]);

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
      </header>

      <main className="app__panes">
        <FilePane side={0} />
        <FilePane side={1} />
      </main>

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
        <span>queue: idle</span>
        <span className="spacer" />
        <span className={dbVersion > 0 ? "" : "status--warn"}>
          {dbVersion > 0 ? `db v${dbVersion}` : "db unavailable"}
        </span>
      </footer>

      <CommandPalette />
      <QuickConnect />
      <HostKeyDialog />
      <Toasts />
    </div>
  );
}
