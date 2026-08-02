import { useEffect, useState } from "react";
import {
  deleteSite,
  getSettings,
  saveSite,
  setSetting,
  sites as fetchSites,
  type Site,
} from "../ipc";
import { formatSize } from "../lib/format";
import { forgetSource } from "../lib/recents";
import { applyTheme, type ThemePref } from "../lib/theme";
import { useUiStore } from "../store";

const MIB = 1024 * 1024;

interface SiteDraft {
  id: number;
  name: string;
  host: string;
  port: number;
  username: string;
  remotePath: string;
  maxTransfers: number;
  password: string; // empty = leave stored credential unchanged
}

function draftFrom(s: Site): SiteDraft {
  return {
    id: s.id,
    name: s.name,
    host: s.host,
    port: s.port,
    username: s.username,
    remotePath: s.remotePath ?? "",
    maxTransfers: s.maxTransfers ?? 0,
    password: "",
  };
}

/** Settings (Ctrl+,): Appearance, Transfers, Bandwidth, and the site editor
    (feedback batch items 1, 2, 7, 8, 9). Values save on change. */
export default function SettingsDialog() {
  const open = useUiStore((s) => s.settingsOpen);
  const setOpen = useUiStore((s) => s.setSettingsOpen);
  const siteList = useUiStore((s) => s.sites);
  const setSites = useUiStore((s) => s.setSites);

  const [cfg, setCfg] = useState<Record<string, string>>({});
  const [draft, setDraft] = useState<SiteDraft | null>(null);
  const [siteMsg, setSiteMsg] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    if (!open) return;
    setSiteMsg("");
    setConfirmDelete(false);
    void getSettings().then(setCfg).catch(() => setCfg({}));
    void fetchSites().then(setSites).catch(() => undefined);
  }, [open, setSites]);

  if (!open) return null;

  // The backend validates and rejects out-of-range values; surface that
  // rather than leaving the field showing something that was never saved.
  const put = (key: string, value: string) => {
    setCfg((c) => ({ ...c, [key]: value }));
    void setSetting(key, value).catch((err: unknown) => {
      setSiteMsg(String(err));
      void getSettings().then(setCfg).catch(() => undefined);
    });
  };

  const theme = (cfg["ui.theme"] as ThemePref) || "dark";
  const bwMode = cfg["bw.mode"] || "off";
  const observedMax = Number(cfg["bw.observed_max"] || 0);

  const pickTheme = (t: ThemePref) => {
    put("ui.theme", t);
    applyTheme(t);
  };

  const saveDraft = async () => {
    if (!draft) return;
    setSiteMsg("");
    try {
      await saveSite(
        {
          id: draft.id,
          name: draft.name.trim(),
          protocol: "sftp",
          host: draft.host.trim(),
          port: Number(draft.port) || 22,
          username: draft.username.trim(),
          remotePath: draft.remotePath.trim(),
          maxTransfers: Number(draft.maxTransfers) || 0,
        },
        draft.password,
      );
      setSites(await fetchSites());
      setDraft(null);
      setSiteMsg("Saved.");
    } catch (err) {
      setSiteMsg(String(err));
    }
  };

  const removeDraftSite = async () => {
    if (!draft) return;
    if (!confirmDelete) {
      setConfirmDelete(true);
      return;
    }
    try {
      await deleteSite(draft.id);
      // SQLite recycles rowids, so this site's jump list must go with it.
      forgetSource(draft.id);
      setSites(await fetchSites());
      setDraft(null);
      setConfirmDelete(false);
      setSiteMsg("Site deleted.");
    } catch (err) {
      setSiteMsg(String(err));
    }
  };

  return (
    <div className="scrim scrim--center" onMouseDown={() => setOpen(false)}>
      <div
        className="dialog dialog--settings"
        onMouseDown={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            setOpen(false);
          }
        }}
        role="dialog"
        aria-label="Settings"
      >
        <h2>Settings</h2>

        <section className="set-section">
          <h3>Appearance</h3>
          <div className="segmented" role="radiogroup" aria-label="Theme">
            {(["dark", "light", "system"] as ThemePref[]).map((t) => (
              <button
                key={t}
                className={theme === t ? "seg--on" : ""}
                role="radio"
                aria-checked={theme === t}
                onClick={() => pickTheme(t)}
              >
                {t[0].toUpperCase() + t.slice(1)}
              </button>
            ))}
          </div>
        </section>

        <section className="set-section">
          <h3>Transfers</h3>
          <div className="set-row">
            <label>Concurrent transfers (all sites)</label>
            <input
              type="number"
              min={1}
              max={16}
              value={cfg["transfers.global_max"] ?? "6"}
              onChange={(e) => put("transfers.global_max", e.target.value)}
            />
          </div>
          <div className="set-row">
            <label>Per-site default</label>
            <input
              type="number"
              min={1}
              max={8}
              value={cfg["transfers.site_max"] ?? "3"}
              onChange={(e) => put("transfers.site_max", e.target.value)}
            />
          </div>
        </section>

        <section className="set-section">
          <h3>Hyperlane</h3>
          <p className="set-note set-blurb">
            Splits one large file across several connections at once, so a server that
            caps the speed of each connection no longer caps the file.
          </p>
          <div className="set-row">
            <label>Lanes per file</label>
            <span className="set-inline">
              <input
                type="number"
                min={1}
                max={16}
                value={cfg["transfers.chunk_streams"] ?? "4"}
                onChange={(e) => put("transfers.chunk_streams", e.target.value)}
              />
              <span className="set-note">connections (1 = off)</span>
            </span>
          </div>
          <div className="set-row">
            <label>Engage above</label>
            <span className="set-inline">
              <input
                type="number"
                min={0}
                value={cfg["transfers.chunk_min_mb"] ?? "256"}
                onChange={(e) => put("transfers.chunk_min_mb", e.target.value)}
              />
              <span className="set-note">MB</span>
            </span>
          </div>
        </section>

        <section className="set-section">
          <h3>Bandwidth</h3>
          <div className="segmented" role="radiogroup" aria-label="Bandwidth limit mode">
            {[
              ["off", "Off"],
              ["fixed", "Fixed"],
              ["percent", "% of max"],
            ].map(([v, label]) => (
              <button
                key={v}
                className={bwMode === v ? "seg--on" : ""}
                role="radio"
                aria-checked={bwMode === v}
                onClick={() => put("bw.mode", v)}
              >
                {label}
              </button>
            ))}
          </div>
          {bwMode === "fixed" && (
            <div className="set-row">
              <label>Limit (MiB/s)</label>
              <input
                type="number"
                min={1}
                value={
                  Number(cfg["bw.limit_bytes"] || 0) > 0
                    ? Math.round(Number(cfg["bw.limit_bytes"]) / MIB)
                    : ""
                }
                placeholder="no limit"
                onChange={(e) => put("bw.limit_bytes", String(Number(e.target.value) * MIB))}
              />
            </div>
          )}
          {bwMode === "percent" && (
            <div className="set-row">
              <label>Throttle to</label>
              <span className="set-inline">
                <input
                  type="number"
                  min={10}
                  max={95}
                  value={cfg["bw.percent"] ?? "80"}
                  onChange={(e) => put("bw.percent", e.target.value)}
                />
                <span className="set-note">
                  % of measured max
                  {observedMax > 0 ? ` (${formatSize(observedMax)}/s so far)` : " (measuring…)"}
                </span>
              </span>
            </div>
          )}
        </section>

        <section className="set-section">
          <h3>Sites</h3>
          {!draft ? (
            <div className="site-list">
              {siteList.length === 0 && <span className="set-note">No saved sites yet.</span>}
              {siteList.map((s) => (
                <div key={s.id} className="site-row" onClick={() => setDraft(draftFrom(s))}>
                  <span className="name">{s.name}</span>
                  <span className="host">
                    {s.username}@{s.host}:{s.port}
                    {s.remotePath ? ` → ${s.remotePath}` : ""}
                  </span>
                  <span className="set-note">edit ›</span>
                </div>
              ))}
            </div>
          ) : (
            <>
              <div className="form-grid">
                <div className="field">
                  <label>Site name</label>
                  <input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
                </div>
                <div className="field">
                  <label>Host</label>
                  <input value={draft.host} onChange={(e) => setDraft({ ...draft, host: e.target.value })} />
                </div>
                <div className="field">
                  <label>Port</label>
                  <input
                    inputMode="numeric"
                    value={draft.port}
                    onChange={(e) => setDraft({ ...draft, port: Number(e.target.value) || 0 })}
                  />
                </div>
                <div className="field">
                  <label>Username</label>
                  <input
                    value={draft.username}
                    onChange={(e) => setDraft({ ...draft, username: e.target.value })}
                  />
                </div>
                <div className="field">
                  <label>Password</label>
                  <input
                    type="password"
                    placeholder="(unchanged)"
                    value={draft.password}
                    onChange={(e) => setDraft({ ...draft, password: e.target.value })}
                  />
                </div>
                <div className="field">
                  <label>Initial remote path</label>
                  <input
                    placeholder="(SFTP home)"
                    value={draft.remotePath}
                    onChange={(e) => setDraft({ ...draft, remotePath: e.target.value })}
                  />
                </div>
                <div className="field">
                  <label>Max transfers (0 = default)</label>
                  <input
                    type="number"
                    min={0}
                    max={8}
                    value={draft.maxTransfers}
                    onChange={(e) => setDraft({ ...draft, maxTransfers: Number(e.target.value) || 0 })}
                  />
                </div>
              </div>
              <div className="dialog__actions">
                <button className="btn btn--danger" onClick={() => void removeDraftSite()}>
                  {confirmDelete ? "Really delete?" : "Delete site"}
                </button>
                <span style={{ flex: 1 }} />
                <button className="btn" onClick={() => setDraft(null)}>
                  Back
                </button>
                <button className="btn btn--primary" onClick={() => void saveDraft()}>
                  Save site
                </button>
              </div>
            </>
          )}
          {siteMsg && <div className="set-note set-msg">{siteMsg}</div>}
        </section>

        <div className="dialog__actions">
          <button className="btn btn--primary" onClick={() => setOpen(false)}>
            Done
          </button>
        </div>
      </div>
    </div>
  );
}
