import { useEffect } from "react";
import FilePane from "./components/FilePane";
import { localHome, schemaVersion } from "./ipc";
import { useUiStore } from "./store";
import "./App.css";

export default function App() {
  const setPath = useUiStore((s) => s.setPath);
  const panes = useUiStore((s) => s.panes);
  const dbVersion = useUiStore((s) => s.dbSchemaVersion);
  const setDbSchemaVersion = useUiStore((s) => s.setDbSchemaVersion);

  useEffect(() => {
    localHome()
      .then((home) => {
        if (!panes[0].path) setPath(0, home);
        if (!panes[1].path) setPath(1, home);
      })
      .catch(() => {
        if (!panes[0].path) setPath(0, "/");
        if (!panes[1].path) setPath(1, "/");
      });
    schemaVersion().then(setDbSchemaVersion).catch(() => setDbSchemaVersion(0));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className="app">
      <header className="app__header">
        <span className="app__mark">
          warp<span className="app__mark-accent">seed</span>
        </span>
        <span className="app__phase">local shell · Phase 0</span>
      </header>

      <main className="app__panes">
        <FilePane side={0} />
        <FilePane side={1} />
      </main>

      <footer className="app__statusbar">
        <span>queue: idle</span>
        <span className={dbVersion > 0 ? "" : "status--warn"}>
          {dbVersion > 0 ? `db schema v${dbVersion}` : "db unavailable"}
        </span>
      </footer>
    </div>
  );
}
