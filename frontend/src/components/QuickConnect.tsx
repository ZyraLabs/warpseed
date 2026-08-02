import { useEffect, useState } from "react";
import {
  connectAndHome,
  deleteSite,
  saveSite,
  sites as fetchSites,
  type Site,
} from "../ipc";
import { forgetSource } from "../lib/recents";
import { useUiStore } from "../store";

const EMPTY = { name: "", host: "", port: 22, username: "", password: "" };

/** Quick-connect: saved sites + a new-site form (ux-spec §5.1, popover-not-
    wizard). Password goes to Windows Credential Manager, never the DB. */
export default function QuickConnect() {
  const { open, side } = useUiStore((s) => s.quickConnect);
  const setQuickConnect = useUiStore((s) => s.setQuickConnect);
  const setPane = useUiStore((s) => s.setPane);
  const siteList = useUiStore((s) => s.sites);
  const setSites = useUiStore((s) => s.setSites);

  const [form, setForm] = useState(EMPTY);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (open) {
      setForm(EMPTY);
      setError("");
      void fetchSites().then(setSites).catch(() => undefined);
    }
  }, [open, setSites]);

  if (!open) return null;

  const close = () => setQuickConnect(false);

  const connectExisting = async (s: Site) => {
    setBusy(true);
    setError("");
    try {
      const home = await connectAndHome(s.id, s.remotePath);
      setPane(side, s.id, home);
      close();
    } catch (err) {
      setError(String(err));
    } finally {
      setBusy(false);
    }
  };

  const saveAndConnect = async () => {
    setBusy(true);
    setError("");
    try {
      const saved = await saveSite(
        {
          name: form.name.trim() || form.host.trim(),
          protocol: "sftp",
          host: form.host.trim(),
          port: Number(form.port) || 22,
          username: form.username.trim(),
        },
        form.password,
      );
      setSites(await fetchSites());
      const home = await connectAndHome(saved.id);
      setPane(side, saved.id, home);
      close();
    } catch (err) {
      setError(String(err));
    } finally {
      setBusy(false);
    }
  };

  const removeSite = async (e: React.MouseEvent, s: Site) => {
    e.stopPropagation();
    try {
      await deleteSite(s.id);
      // SQLite recycles rowids, so this site's jump list must go with it.
      forgetSource(s.id);
      setSites(await fetchSites());
    } catch (err) {
      setError(String(err));
    }
  };

  return (
    <div className="scrim scrim--center" onMouseDown={close}>
      <div className="dialog" onMouseDown={(e) => e.stopPropagation()} role="dialog" aria-label="Connect">
        <h2>Connect</h2>

        {siteList.length > 0 && (
          <div className="site-list">
            {siteList.map((s) => (
              <div key={s.id} className="site-row" onClick={() => void connectExisting(s)}>
                <span className="name">{s.name}</span>
                <span className="host">
                  {s.username}@{s.host}:{s.port}
                </span>
                <button className="del" title="Delete site" onClick={(e) => void removeSite(e, s)}>
                  ✕
                </button>
              </div>
            ))}
          </div>
        )}

        <div className="form-grid">
          <div className="field wide">
            <label>Host</label>
            <input
              autoFocus
              value={form.host}
              placeholder="seedbox.example.com"
              onChange={(e) => setForm({ ...form, host: e.target.value })}
            />
          </div>
          <div className="field">
            <label>Port</label>
            <input
              value={form.port}
              inputMode="numeric"
              onChange={(e) => setForm({ ...form, port: Number(e.target.value) || 0 })}
            />
          </div>
          <div className="field">
            <label>Site name</label>
            <input
              value={form.name}
              placeholder="(defaults to host)"
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>
          <div className="field">
            <label>Username</label>
            <input
              value={form.username}
              onChange={(e) => setForm({ ...form, username: e.target.value })}
            />
          </div>
          <div className="field">
            <label>Password</label>
            <input
              type="password"
              value={form.password}
              onChange={(e) => setForm({ ...form, password: e.target.value })}
              onKeyDown={(e) => e.key === "Enter" && void saveAndConnect()}
            />
            <span className="note">stored in Windows Credential Manager, never on disk</span>
          </div>
        </div>

        {error && <div className="form-error">{error}</div>}

        <div className="dialog__actions">
          <button className="btn" onClick={close}>
            Cancel
          </button>
          <button
            className="btn btn--primary"
            disabled={busy || !form.host || !form.username}
            onClick={() => void saveAndConnect()}
          >
            {busy ? "Connecting…" : "Save & connect"}
          </button>
        </div>
      </div>
    </div>
  );
}
