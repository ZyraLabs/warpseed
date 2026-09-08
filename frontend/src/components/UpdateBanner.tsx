import { useEffect, useState } from "react";
import { checkForUpdate, dismissUpdate, on, type UpdateInfo } from "../ipc";
import { Close, Warning } from "./Icon";

/** "An update is available" strip, in the WizTree shape the user asked for:
    a calm advisory across the top with the action on the left and a dismiss on
    the right. Deliberately NOT a dialog — an update is never urgent enough to
    take the window, and a modal on launch is the thing people learn to close
    without reading.

    It only ever appears when Go says there is genuinely a newer release. A
    failed check shows nothing at all. */
export default function UpdateBanner({
  onShownChange,
}: {
  /** Told when the strip appears or goes, so the app grid can add its row. */
  onShownChange: (shown: boolean) => void;
}) {
  const [info, setInfo] = useState<UpdateInfo | null>(null);

  useEffect(() => {
    onShownChange(info !== null);
  }, [info, onShownChange]);

  useEffect(() => {
    // Go does the automatic check and emits only on a real find, so there is
    // no polling and no "checking…" state to render here.
    const off = on<UpdateInfo>("update:available", (u) => {
      if (u.available && !u.dismissed) setInfo(u);
    });
    return off;
  }, []);

  if (!info) return null;

  return (
    <div className="updbar" role="status">
      <button
        className="updbar__cta"
        onClick={() => {
          // Opening the releases page is the whole action. warpseed is a
          // portable exe with no installer; it must not try to replace the
          // file it is running from.
          window.open(info.url, "_blank", "noopener");
        }}
      >
        View update
      </button>
      <Warning size={13} className="updbar__icon" />
      <span className="updbar__text">
        warpseed {info.latest} is available — you have {info.current}.
      </span>
      <span className="grow" />
      <button
        className="updbar__dismiss"
        title="Hide this until the next release"
        onClick={() => {
          void dismissUpdate(info.latest).catch(() => undefined);
          setInfo(null);
        }}
      >
        <Close size={12} />
        Dismiss
      </button>
    </div>
  );
}

/** Settings' "Check now": returns the result so the caller can report it,
    including the "you are up to date" case an automatic check stays silent
    about. */
export async function checkNow(): Promise<UpdateInfo> {
  return checkForUpdate();
}
