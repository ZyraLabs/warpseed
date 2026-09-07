/* The dispatcher's claim order, mirrored for every "up next" surface:
   highest priority first, then oldest first — and a pending row sitting in
   retry backoff is not next, whatever its id says. The queue list arrives
   newest first, which is exactly the wrong end for "what goes next". */
import type { Transfer } from "../ipc";

/** Sort comparator matching PendingTransfers' ORDER BY. */
export const claimOrder = (a: Transfer, b: Transfer): number =>
  b.priority - a.priority || a.id - b.id;

const inBackoff = (t: Transfer, now: number): boolean =>
  t.nextRetryAt != null && Date.parse(t.nextRetryAt) > now;

/** Pending/dispatched rows in the order they will be picked up; rows whose
    retry deadline has not passed sink to the end.
 *
 * Rows held for an overwrite decision are excluded. They are 'pending' in the
 * database but the dispatcher will never claim them, so counting them as
 * queued makes a queue full of held files read as "200 armed for transfer"
 * with nothing moving — indistinguishable from a stall. */
export function nextUp(rows: Transfer[], now = Date.now()): Transfer[] {
  return rows
    .filter((t) => !t.conflict && (t.state === "pending" || t.state === "dispatched"))
    .sort((a, b) => Number(inBackoff(a, now)) - Number(inBackoff(b, now)) || claimOrder(a, b));
}
