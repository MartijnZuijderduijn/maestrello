import { morphInto } from "./morph.js";
import { detail, openId, setDetail, setOpenId } from "./state.js";
import { $, esc } from "./util.js";

// ---------- modal ("drawer") ----------
// openDrawer is called on every live re-render of the open card: it only animates,
// moves focus and resets scroll when the modal goes from closed to open.
let closeTimer = null, lastFocus = null;
const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");
function setBackgroundInert(on) { for (const el of [$("header"), $("#board")]) el.inert = on; }
// Open/closed state of the detail sections, per card id and section key; survives live re-renders.
const secState = new Map();
const secOpen = (id, key, def) => secState.get(id)?.get(key) ?? def;
// A collapsible section; `def` is its default state, `cls` an extra class.
export function sec(id, key, title, inner, def = true, cls = "") {
  return `<details class="sec ${cls}" data-sec="${esc(key)}" ${secOpen(id, key, def) ? "open" : ""}><summary>${title}</summary>${inner}</details>`;
}
document.addEventListener("DOMContentLoaded", () => {
  $("#drawer-body").addEventListener("toggle", (e) => {
    const el = e.target, key = el.dataset?.sec;
    if (!key || !detail || typeof openId !== "number") return;
    if (!secState.has(openId)) secState.set(openId, new Map());
    secState.get(openId).set(key, el.open);
  }, true);
});
export function openDrawer(title, html, avatar = "", meta = "") {
  const ov = $("#overlay"), wasOpen = ov.classList.contains("open") && !ov.classList.contains("closing");
  clearTimeout(closeTimer); ov.classList.remove("closing");
  $("#drawer-title").textContent = title;
  morphInto($("#drawer-av"), avatar);
  morphInto($("#drawer-meta"), meta);
  morphInto($("#drawer-body"), html);
  $("#drawer-body").removeAttribute("aria-busy"); // set by openCard while the card loads
  if (wasOpen) { if (!$("#drawer").contains(document.activeElement)) $("#drawer").focus({ preventScroll: true }); return; }
  if (!$("#drawer").contains(document.activeElement)) lastFocus = document.activeElement;
  ov.classList.add("open"); setBackgroundInert(true);
  $("#drawer-body").scrollTop = 0;
  $("#drawer").focus({ preventScroll: true });
}
export function closeDrawer() {
  setOpenId(null); setDetail(null);
  const ov = $("#overlay");
  if (!ov.classList.contains("open") || ov.classList.contains("closing")) return;
  setBackgroundInert(false);
  const done = () => { ov.classList.remove("open", "closing"); $("#drawer-av").innerHTML = ""; $("#drawer-meta").innerHTML = ""; };
  if (reducedMotion.matches) done(); else { ov.classList.add("closing"); closeTimer = setTimeout(done, 180); }
  if (lastFocus?.isConnected) lastFocus.focus({ preventScroll: true });
  if ($("#drawer").contains(document.activeElement)) document.activeElement.blur();
  lastFocus = null;
}
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && openId !== null) closeDrawer(); });
