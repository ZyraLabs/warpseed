import PromptDialog from "./PromptDialog";
import { useUiStore } from "../store";

/** The one confirmation dialog every destructive action shares. Rendered
    once at the app root so a component does not need to own a dialog to ask
    a question — Deck and Timeline raise cancel confirmations from inside a
    list row. Raise one with useUiStore's askConfirm. */
export default function ConfirmDialog() {
  const confirm = useUiStore((s) => s.confirm);
  const closeConfirm = useUiStore((s) => s.closeConfirm);
  return <PromptDialog spec={confirm} onClose={closeConfirm} />;
}
