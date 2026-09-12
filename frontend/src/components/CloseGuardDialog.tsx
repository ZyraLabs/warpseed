import { useEffect, useState } from "react";
import {
  ackCloseDialog,
  cancelQuit,
  closeToPill,
  confirmQuit,
  on,
  setSetting,
  type CloseRequest,
} from "../ipc";
import { formatSize } from "../lib/format";
import { toast } from "../lib/toast";
import { useUiStore } from "../store";

/** Asked before closing warpseed while transfers are running.
 *
 * Deliberately calm: no warning icon, no error ring. Nothing here is
 * destroyed — every lane checkpoints, and unfinished transfers restart on the
 * next launch. The loud treatment stays reserved for the things that really
 * are irreversible.
 *
 * It never appears for an idle app: Go does not emit the event when nothing is
 * running, so there is no zero-transfer wording to write. */
export default function CloseGuardDialog() {
  const [req, setReq] = useState<CloseRequest | null>(null);
  const [dontAsk, setDontAsk] = useState(false);
  const transfers = useUiStore((s) => s.transfers);
  const progress = useUiStore((s) => s.progress);
  // Whether the queue starts on the other side of the restart is the
  // user's setting, not a promise this dialog can make on its own.
  const queuePaused = useUiStore((s) => s.queuePaused);

  useEffect(
    () =>
      on<CloseRequest>("app:close-requested", (p) => {
        setReq(p);
        setDontAsk(false); // never sticky across gestures
        useUiStore.getState().setMiniMode(false);
        useUiStore.getState().setCloseGuardOpen(true);
        // Immediately, inside the handler: Go force-quits two seconds after
        // asking if nothing acknowledges, which is the escape from a wedged
        // frontend. A late ack defeats it.
        void ackCloseDialog();
      }),
    [],
  );

  if (!req) return null;

  const active = transfers.filter((t) => t.state === "active");
  const queued = transfers.filter((t) => t.state === "pending").length;
  // progress entries are never deleted, so a rate must be gated on the row
  // still being active or a finished transfer keeps contributing.
  const aggRate = active.reduce((s, t) => s + (progress[t.id]?.rate ?? 0), 0);

  const n = req.running;
  const mb = req.checkpointMB;

  const close = (open: boolean) => useUiStore.getState().setCloseGuardOpen(open);

  const keep = () => {
    close(false);
    setReq(null);
    void cancelQuit();
  };
  const quit = () => {
    close(false);
    // Honoured ONLY here. Ticking the box and then choosing Keep or Minimize
    // must change nothing: the user never consented to that outcome.
    if (dontAsk) void setSetting("ui.close_action", "quit");
    void confirmQuit();
  };
  const pill = () => {
    close(false);
    setReq(null);
    void closeToPill()
      .then(() => useUiStore.getState().setMiniMode(true))
      .catch(() => toast("error", "Could not enter mini mode"));
  };

  return (
    <div className="scrim scrim--center">
      {/* No mousedown-to-dismiss: a close confirmation takes an explicit
          answer, and a stray click on the backdrop is not one. */}
      <div
        className="dialog dialog--closeguard"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="closeguard-title"
        aria-describedby="closeguard-desc"
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            keep(); // Escape is the safest answer, never one that stops a transfer
          }
        }}
      >
        <h2 id="closeguard-title">
          {n === 1
            ? "Closing warpseed stops the running transfer"
            : `Closing warpseed stops ${n} running transfers`}
          {aggRate > 0 && <span className="cg__rate">{formatSize(aggRate)}/s</span>}
        </h2>

        <p id="closeguard-desc">
          {n === 1
            ? `Its progress is saved. It is still queued the next time you open warpseed and picks up from the last checkpoint, so at most about ${mb} MB is re-sent.`
            : `Their progress is saved. They are still queued the next time you open warpseed and each picks up from its last checkpoint, so at most about ${mb} MB per connection is re-sent.`}
        </p>

        <p>
          Unfinished transfers keep their data in a placeholder file next to the
          destination, ending .wspart or .wschunk — on the server for uploads. A
          .wschunk already shows the final file size but is not finished. Leave
          these files alone; warpseed needs them to resume. If you later cancel a
          transfer, delete its placeholder yourself — warpseed leaves it behind.
        </p>

        {queued > 0 && (
          <p>
            {queued === 1
              ? `The queued transfer is untouched and ${queuePaused ? "waits until you resume the queue" : "starts when you're back"}.`
              : `${queued} queued transfers are untouched and ${queuePaused ? "wait until you resume the queue" : "start when you're back"}.`}
          </p>
        )}
        {queuePaused && (
          <p>The queue is paused, so nothing starts until you resume it.</p>
        )}

        <div className="dialog__actions dialog__actions--split">
          <label className="cg__dontask">
            <input
              type="checkbox"
              checked={dontAsk}
              onChange={(e) => setDontAsk(e.target.checked)}
            />
            Don&rsquo;t ask again — always close and keep the queue
          </label>
          <span className="grow" />
          <button className="btn" onClick={keep}>
            Keep warpseed open
          </button>
          <button className="btn" onClick={quit}>
            Close and pick up later
          </button>
          <button className="btn btn--primary" autoFocus onClick={pill}>
            Minimize to pill
          </button>
        </div>
      </div>
    </div>
  );
}
