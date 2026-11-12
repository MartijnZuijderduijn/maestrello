export const COLUMNS = [
  ["todo", "Todo"], ["refining", "Refining"], ["ready", "Ready"], ["running", "Running"],
  ["needs_input", "Needs input"], ["review", "Review"], ["failed", "Failed"], ["done", "Done"],
];
export const MODES = ["auto", "acceptEdits", "manual", "dontAsk", "plan", "bypassPermissions"];
export const EFFORTS = ["low", "medium", "high", "xhigh", "max"];
export let cards = new Map();
export let openId = null;      // card id shown in the modal, or "new" / "settings"
export let detail = null;
// Importers cannot assign to a binding, so the shared state is set through these.
export function setCards(v) { cards = v; }
export function setOpenId(v) { openId = v; }
export function setDetail(v) { detail = v; }
