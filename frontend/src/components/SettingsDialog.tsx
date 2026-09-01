import { useEffect, useState } from "react";
import {
  backupData,
  dataLocation,
  deleteSite,
  getSettings,
  openDataFolder,
  openExternal,
  type DataInfo,
  saveSite,
  setSetting,
  sites as fetchSites,
  type Site,
} from "../ipc";
import { APP_VERSION, COMPANY, DONATE_URL, WEBSITE_URL, bugReportUrl } from "../lib/branding";
import { formatSize } from "../lib/format";
import { forgetSource } from "../lib/recents";
import { applyTheme, coerceTheme, THEMES, type ThemePref } from "../lib/theme";
import { useUiStore } from "../store";
import { Bug, ChevronRight, Heart } from "./Icon";

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
  const [data, setData] = useState<DataInfo | null>(null);
  const [backingUp, setBackingUp] = useState(false);
  const [draft, setDraft] = useState<SiteDraft | null>(null);
  const [siteMsg, setSiteMsg] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    if (!open) return;
    setSiteMsg("");
    setConfirmDelete(false);
    void getSettings().then(setCfg).catch(() => setCfg({}));
    void fetchSites().then(setSites).catch(() => undefined);
    void dataLocation().then(setData).catch(() => setData(null));
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

  // Legacy stored ids (v3 themes, "dark"/"light", "system") coerce to v4.
  const theme: ThemePref = coerceTheme(cfg["ui.theme"] ?? null);
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
          <div className="themes" role="radiogroup" aria-label="Theme">
            {THEMES.map((t) => (
              <button
                key={t.id}
                className={`theme-card ${theme === t.id ? "theme-card--on" : ""}`}
                role="radio"
                aria-checked={theme === t.id}
                onClick={() => pickTheme(t.id)}
              >
                <span className="theme-card__swatch" aria-hidden>
                  {t.swatch.map((c) => (
                    <span key={c} style={{ background: c }} />
                  ))}
                </span>
                <span className="theme-card__name">{t.name}</span>
                <span className="theme-card__blurb">{t.blurb}</span>
              </button>
            ))}
          </div>
          <p className="set-note">
            Each theme carries its own palette, type pairing and row density.
          </p>
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
          <h3>Hyperlane · Downloads</h3>
          <p className="set-note set-blurb">
            Splits one large file across several connections at once, so a server that
            caps the speed of each connection no longer caps the file. Each direction
            is tuned separately — a link is rarely as fast up as it is down.
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
          <h3>Hyperlane · Uploads</h3>
          <div className="set-row">
            <label>Lanes per file</label>
            <span className="set-inline">
              <input
                type="number"
                min={1}
                max={16}
                value={cfg["transfers.upload_chunk_streams"] ?? "3"}
                onChange={(e) => put("transfers.upload_chunk_streams", e.target.value)}
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
                value={cfg["transfers.upload_chunk_min_mb"] ?? "128"}
                onChange={(e) => put("transfers.upload_chunk_min_mb", e.target.value)}
              />
              <span className="set-note">MB</span>
            </span>
          </div>
          <p className="set-note">
            Upload speed usually caps out around 3 lanes — more connections cost
            handshakes without adding throughput.
          </p>
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
                  <span className="set-note site-row__go">edit <ChevronRight size={11} /></span>
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

        <section className="set-section">
          <h3>Data</h3>
          <p className="set-note set-blurb">
            Sites, bookmarks, the transfer queue, pinned host keys and every setting
            here live in one file. Passwords are the exception — those stay in Windows
            Credential Manager and are not part of a backup.
          </p>
          <div className="set-row">
            <label>Settings file</label>
            <span className="set-inline">
              <code className="set-path" title={data?.path}>
                {data?.path ?? "…"}
              </code>
            </span>
          </div>
          <div className="dialog__actions" style={{ marginTop: "var(--sp-2)" }}>
            <button className="btn" onClick={() => void openDataFolder().catch(() => undefined)}>
              Open folder
            </button>
            <span style={{ flex: 1 }} />
            <button
              className="btn btn--primary"
              disabled={backingUp}
              onClick={() => {
                setBackingUp(true);
                void backupData()
                  .then((name) => setSiteMsg(`Backed up to ${name}`))
                  .catch((err: unknown) => setSiteMsg(String(err)))
                  .finally(() => {
                    setBackingUp(false);
                    void dataLocation().then(setData).catch(() => undefined);
                  });
              }}
            >
              {backingUp ? "Backing up…" : "Back up now"}
            </button>
          </div>
          {data && data.backups.length > 0 && (
            <p className="set-note" style={{ marginTop: "var(--sp-2)" }}>
              {data.backups.length} backup{data.backups.length === 1 ? "" : "s"} · newest{" "}
              {data.backups[0]}. To restore one: close warpseed, delete
              warpseed.db along with any warpseed.db-wal and warpseed.db-shm beside it,
              then rename the backup to warpseed.db. Leaving the -wal file behind
              replays old changes over the restored copy.
            </p>
          )}
        </section>

        <section className="set-section">
          <h3>About</h3>
          <p className="set-note set-blurb">
            warpseed {APP_VERSION} — a free, fast seedbox transfer client by {COMPANY}.
            It is free and always will be; if it saves you time, a coffee keeps the
            updates coming.
          </p>
          <div className="dialog__actions" style={{ marginTop: "var(--sp-2)" }}>
            <button className="btn" onClick={() => openExternal(WEBSITE_URL)}>
              zyralabs.tech
            </button>
            <button className="btn" onClick={() => openExternal(bugReportUrl())}>
              <Bug size={12} className="btn__ico" /> Report a bug
            </button>
            <span style={{ flex: 1 }} />
            <button className="btn btn--primary" onClick={() => openExternal(DONATE_URL)}>
              <Heart size={12} className="btn__ico" /> Support warpseed
            </button>
          </div>
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
