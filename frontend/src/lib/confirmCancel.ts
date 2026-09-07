import { cancelTransfer, type Transfer } from "../ipc";
import { formatSize } from "./format";
import { baseName } from "./path";
import { useUiStore } from "../store";

/** Ask before cancelling, because cancel deletes the part-transferred data.
 *
 * That deletion is the point of cancel — it is what stops a cancelled 50 GB
 * upload sitting on a seedbox quota — but it means one click can throw away
 * hours of transfer. The queue dock calls the button "Cancel"; Deck and
 * Timeline call it "Skip", which sounds like "leave it for later" and is the
 * more dangerous wording of the two.
 *
 * Nothing transferred yet means nothing to lose, so those cancel straight
 * away rather than nagging. When there is progress at stake the dialog names
 * the alternative, since Pause is usually what was wanted.
 */
export function confirmCancel(id: number) {
  const { transfers, progress, askConfirm } = useUiStore.getState();
  const t = transfers.find((x) => x.id === id);
  // Live progress runs ahead of the row's last checkpoint, so prefer it.
  const moved = Math.max(progress[id]?.bytes ?? 0, t?.bytesDone ?? 0, 0);
  if (!t || moved <= 0) {
    void cancelTransfer(id);
    return;
  }
  askConfirm({
    title: `Cancel ${baseName(t.src)}?`,
    body: bodyFor(t, moved),
    confirmLabel: "Cancel transfer",
    danger: true,
    suppressKey: "cancel-transfer",
    onConfirm: () => void cancelTransfer(id),
  });
}

function bodyFor(t: Transfer, moved: number) {
  const lost = `The ${formatSize(moved)} already transferred is deleted, so this file would start from the beginning if you queue it again.`;
  // A paused or failed row is not going anywhere; leaving it alone is the
  // real alternative. A running one can be paused instead.
  return t.state === "paused" || t.state === "failed"
    ? `${lost} Leaving it in the queue keeps that progress for a retry.`
    : `${lost} Pause instead to stop it and keep the progress.`;
}
