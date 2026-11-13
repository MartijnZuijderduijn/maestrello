import { api } from "./api.js";
import { harnessIcon } from "./hicon.js";
import { refreshToolSupport } from "./forms.js";
import { openSettings } from "./settings.js";
import { EFFORTS, MODES, openId } from "./state.js";
import { $, esc, select } from "./util.js";

let models = ["sonnet", "opus", "haiku"];
// The registered harnesses with the efforts and permission modes each accepts (GET /api/harnesses).
// This is the fallback if the call fails; it is what claude accepts.
export let harnesses = [
  { name: "claude", efforts: EFFORTS, permission_modes: MODES, available: true, version: "", error: "", models: [] },
  { name: "opencode", efforts: [], permission_modes: ["auto", "plan"], available: true, version: "", error: "", models: [] },
];
export { harnessIcon };
export const harnessInfo = (name) => harnesses.find((h) => h.name === name) || harnesses[0];
const harnessLabel = (h) => h.name + (h.available ? "" : " (not available)");
// Always keep the current value selectable, even if the harness is no longer registered.
// A harness whose CLI is missing is marked, with the reason as tooltip; it stays selectable.
export function harnessSelect(id, value) {
  const list = harnesses.slice();
  if (value && !list.some((h) => h.name === value)) list.unshift({ name: value, available: true });
  return `<select id="${id}" onchange="harnessChanged()">${list
    .map((h) => `<option value="${esc(h.name)}" title="${esc(h.available ? h.version : h.error)}" ${h.name === value ? "selected" : ""}>${esc(harnessLabel(h))}</option>`).join("")}</select>`;
}
// The models a harness offers: its own list, or (claude) the board's known aliases.
function modelsOf(h) { return h.models && h.models.length ? h.models : h.name === "claude" ? models : []; }
// Refills a select from opts, keeping the current value when it is among them. With
// `orKeep` and no opts (nothing known to offer) the current value stays as the only option.
function fillSelect(el, opts, orKeep) {
  const cur = el.value;
  const list = !opts.length && orKeep && cur ? [cur] : opts;
  el.innerHTML = list.map((o) => `<option ${o === cur ? "selected" : ""}>${esc(o)}</option>`).join("");
}
// A harness change refills model, effort and permission mode from what that harness offers,
// keeping the current choice when the new harness knows it too.
export function harnessChanged() {
  const h = harnessInfo($("#f-harness").value);
  fillSelect($("#f-model"), modelsOf(h), true);
  if ($("#s-rmodel")) fillSelect($("#s-rmodel"), modelsOf(h), true); // the settings' harness runs refinements
  for (const id of ["#f-effort", "#s-reffort"]) if ($(id)) fillSelect($(id), h.efforts);
  fillSelect($("#f-mode"), h.permission_modes);
  refreshToolSupport();
  const note = $("#harness-note");
  if (note) note.textContent = h.available ? "" : `${h.name} is not available: ${h.error}. Cards on it cannot run.`;
}
// Always keep the current value selectable, even if it is not in the list.
// With automatic model selection on for the card, the refiner picks the model, so the manual select is disabled.
export function modelSelect(id, value, h = harnesses[0], auto = false) {
  const list = modelsOf(h);
  const html = select(id, list.includes(value) || !value ? list : [value, ...list], value);
  return auto ? html.replace("<select", `<select disabled title="${AUTO_MODEL_HINT}"`) : html;
}
const AUTO_MODEL_HINT = "Automatic model selection is on for this card: the refiner picks the model (the manual model is only the fallback)";
// Ticking the card's box disables or enables its manual model select at once.
export function autoModelChanged() {
  const on = $("#f-automodel").checked, el = $("#f-model");
  el.disabled = on; el.title = on ? AUTO_MODEL_HINT : "";
}
// Availability per adapter, with a re-check (GET /api/harnesses?refresh=1).
export function harnessAvailability() {
  return `<label>Harness availability</label>${harnesses.map((h) => `<div class="hav">${harnessIcon(h.name)}<b>${esc(h.name)}</b>
      ${h.available ? `<span>available${h.version ? " · " + esc(h.version) : ""}</span>` : `<span class="bad">not available: ${esc(h.error)}</span>`}</div>`).join("")}
    <div class="hav"><button onclick="recheckHarnesses()" title="Check the harness CLIs again">↻ re-check</button></div>`;
}
export async function recheckHarnesses() {
  try { harnesses = await api("GET", "/api/harnesses?refresh=1"); } catch (e) { alert(e.message); return; }
  if (openId === "settings") openSettings();
}
export function setModels(v) { models = v; }
export function setHarnesses(v) { harnesses = v; }
