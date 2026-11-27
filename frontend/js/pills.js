import { esc } from "./util.js";

// Colored pills for card types and states. The only place that maps a value to a color.
// A tone is a CSS class (.pill-<tone> in modal.css) that works in light and dark mode.
export const KIND_TONE = { code: "indigo", research: "lime", plan: "teal", chat: "pink" };
// Progression: idle gray, preparing violet/blue, active amber, blocked orange, waiting cyan, failed red, done green.
export const STATE_TONE = { todo: "gray", refining: "violet", ready: "blue", running: "amber", needs_input: "orange", review: "cyan", failed: "red", done: "green" };
export const FALLBACK_TONE = "gray";

export const toneOf = (map, value) => Object.hasOwn(map, value) ? map[value] : FALLBACK_TONE;

// The label always shows as text; color is only a second cue.
export function pill(label, tone, title = "") {
  return `<span class="badge pill pill-${tone}"${title ? ` title="${esc(title)}"` : ""}>${esc(label)}</span>`;
}
export const kindPill = (kind, title = "") => pill(kind, toneOf(KIND_TONE, kind), title);
export const statePill = (state, label = state) => pill(label, toneOf(STATE_TONE, state));
