import { useEffect, useState } from "react";
import { on, resolvePrompt, type HostKeyPrompt } from "../ipc";
import { Warning } from "./Icon";

/** TOFU host-key dialog (ux-spec §5.3): calm, informative, copyable
    fingerprint. Timeout on the Go side denies automatically. */
export default function HostKeyDialog() {
  const [queue, setQueue] = useState<HostKeyPrompt[]>([]);

  useEffect(
    () => on<HostKeyPrompt>("prompt:hostkey", (p) => setQueue((q) => [...q, p])),
    [],
  );

  const current = queue[0];
  if (!current) return null;

  const answer = (ok: boolean) => {
    void resolvePrompt(current.promptId, ok);
    setQueue((q) => q.slice(1));
  };

  return (
    <div className="scrim scrim--center">
      <div className="dialog dialog--hostkey" role="alertdialog" aria-label="Unknown host key">
        <h2>
          <span className="hk-mark">
            <Warning size={17} />
          </span>
          First connection to {current.host}
        </h2>
        <p>
          The server presented a <b>{current.algo}</b> key warpseed hasn’t seen before. Verify
          this fingerprint against one your provider published, then trust it to pin it for
          this site.
        </p>
        <code className="fingerprint">{current.fingerprint}</code>
        <div className="dialog__actions">
          <button className="btn" onClick={() => answer(false)}>
            Cancel
          </button>
          <button className="btn btn--primary" autoFocus onClick={() => answer(true)}>
            Trust &amp; connect
          </button>
        </div>
      </div>
    </div>
  );
}
