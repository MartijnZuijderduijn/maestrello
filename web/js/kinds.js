import { esc } from "./util.js";

// The kind of a card, shown on the board and in the detail unless it is the default.
export const KINDS = [["code", "Code"], ["research", "Research"], ["plan", "Plan"], ["chat", "Chat"]];
const KIND_HINT = { research: "Read-only: writes a report", plan: "Read-only: writes a plan; approving it creates cards", chat: "Conversation without tools" };
export const kindOf = (c) => c.kind || "code";
export const kindMeta = (c) => kindOf(c) === "code" ? "" : ` · <span title="${esc(KIND_HINT[kindOf(c)] || "")}">${esc(kindOf(c))}</span>`;
