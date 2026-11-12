import { api } from "./api.js";
import { openCard } from "./card.js";
import { openDrawer, sec } from "./drawer.js";
import { harnessInfo, harnessSelect, harnesses, modelSelect } from "./harness.js";
import { KINDS, kindOf } from "./kinds.js";
import { FILE, FOLDER, pathField, pickPath } from "./picker.js";
import { projects } from "./projects.js";
import { cards, openId, setOpenId } from "./state.js";
import { $, esc, select } from "./util.js";

export function settingsFields(c) {
  const h = harnessInfo(c.harness);
  return `<div class="row">
      <div><label>Harness</label>${harnessSelect("f-harness", c.harness)}</div>
      <div><label>Model</label>${modelSelect("f-model", c.model, h, c.auto_model)}</div>
      <div><label>Effort</label>${select("f-effort", h.efforts, c.effort)}</div>
      <div><label>Permission mode</label>${select("f-mode", h.permission_modes, c.permission_mode)}</div>
    </div>
    <div class="hav note" id="harness-note">${h.available ? "" : esc(`${h.name} is not available: ${h.error}. Cards on it cannot run.`)}</div>
    <div class="row">
      <div><label>Max turns</label><input id="f-turns" type="number" min="0" value="${c.max_turns}"></div>
      <div><label>Max minutes</label><input id="f-minutes" type="number" min="0" value="${c.max_minutes}"></div>
      <div><label>Push default</label><select id="f-push">${[["", "decide at review"], ["local", "keep local"], ["push", "push branch"], ["pr", "push + PR"]]
        .map(([v, l]) => `<option value="${v}" ${v === c.push_default ? "selected" : ""}>${l}</option>`).join("")}</select></div>
    </div>`;
}
function projectSelect(value) {
  const all = [...projects.values()].sort((a, b) => a.id - b.id);
  if (!all.length) return `<select id="f-project"><option value="">default root folder</option></select>`;
  return `<select id="f-project" onchange="projectChanged()">${all.map((p) => `<option value="${p.id}" ${p.id === value ? "selected" : ""}>${esc(p.name)}</option>`).join("")}</select>`;
}
// Picking another project refills the root folder from it (cards otherwise keep their own).
export function projectChanged() {
  const p = projects.get(+$("#f-project").value);
  if (p) $("#f-root").value = p.root_folder;
}
// The title and description fields of a card that is being edited.
function titleFields(c) {
  return `<label>Title</label><input id="f-title" value="${esc(c.title)}">
    <label>Description</label><textarea id="f-desc" rows="7">${esc(c.description)}</textarea>`;
}
// Everything but the text: shared by the New card modal (under "Options") and the card detail.
// The new card form has no title; `isNew` words the skip box for a lone prompt.
export function cardForm(c, isNew = false) {
  return (isNew ? "" : titleFields(c)) + cardOptions(c, isNew);
}
// The toolbox catalog (GET /api/toolbox) and the settings' per-kind defaults (for new cards).
export let toolbox = [];
let toolDefaults = {};
// One checkbox per catalog entry; refreshToolSupport disables those the selected harness lacks.
function toolsGroup(c) {
  if (!toolbox.length) return "";
  const on = c.tools || [];
  return `<label>Tools</label><div class="toolbox" id="f-tools">${toolbox.map((t) =>
    `<label class="check" title="${esc(t.description)}"><input type="checkbox" data-tool="${esc(t.id)}" ${on.includes(t.id) ? "checked" : ""}> ${esc(t.label)}<span class="meta">${esc(t.description)}</span></label>`).join("")}</div>`;
}
export function refreshToolSupport() {
  const box = $("#f-tools"), hs = $("#f-harness"); if (!box || !hs) return;
  const h = hs.value;
  for (const el of box.querySelectorAll("input[data-tool]")) {
    const t = toolbox.find((x) => x.id === el.dataset.tool);
    const ok = !t || (t.harnesses || []).includes(h);
    el.disabled = !ok;
    if (!ok) el.checked = false;
    el.closest("label").title = ok ? (t ? t.description : "") : `${t.label} is not supported by ${h}`;
  }
}
const readTools = () => [...document.querySelectorAll("#f-tools input[data-tool]")].filter((el) => el.checked && !el.disabled).map((el) => el.dataset.tool);
// A new card starts with the settings' defaults for its kind; a card being edited keeps its own.
function applyToolDefaults() {
  if (openId !== "new") return;
  const on = toolDefaults[$("#f-kind").value] || [];
  for (const el of document.querySelectorAll("#f-tools input[data-tool]")) el.checked = on.includes(el.dataset.tool);
  refreshToolSupport();
}
// Settings: which tools new cards of each kind start with.
export function toolDefaultsGrid(defaults) {
  if (!toolbox.length) return "";
  return `<label>Default tools per kind</label><table class="toolgrid"><thead><tr><th></th>${toolbox.map((t) => `<th title="${esc(t.description)}">${esc(t.label)}</th>`).join("")}</tr></thead><tbody>${KINDS.map(([k, l]) =>
    `<tr><th>${l}</th>${toolbox.map((t) => `<td><input type="checkbox" data-kind="${k}" data-tool="${esc(t.id)}" aria-label="${esc(l + ": " + t.label)}" ${(defaults[k] || []).includes(t.id) ? "checked" : ""}></td>`).join("")}</tr>`).join("")}</tbody></table>`;
}
export function readToolDefaults() {
  const out = {};
  for (const el of document.querySelectorAll(".toolgrid input[data-kind]")) if (el.checked) (out[el.dataset.kind] ||= []).push(el.dataset.tool);
  return out;
}
// Additional context: folders and files the agent may read besides the repos. The list lives in the DOM
// (one .ctx-item per path, data-path / data-type) and readForm reads it back, so there is no other state.
// Live re-renders of the card detail rebuild this group from the saved card, so unsaved edits to it
// are reset, as they are for the other fields.
export const ctxIcon = (type) => type === "dir" ? FOLDER : type === "file" ? FILE : "";
export const ctxItem = (path, type) => `<div class="ctx-item" data-path="${esc(path)}" data-type="${esc(type || "")}"><span class="ctx-icon" title="${type === "dir" ? "Folder" : type === "file" ? "File" : "Folder or file"}">${ctxIcon(type)}</span><span class="ctx-path">${esc(path)}</span><button type="button" class="ctx-rm" onclick="removeContext(this)" aria-label="Remove ${esc(path)}" title="Remove">×</button></div>`;
function contextGroup(c) {
  return `<div id="ctx-group"><label>Additional context</label>
    <div class="ctx-list" id="f-ctx">${(c.context || []).map((i) => ctxItem(i.path, i.type)).join("")}</div>
    <div class="ctx-add">
      <button type="button" onclick="addContextPick('dir')">Add folder…</button>
      <button type="button" onclick="addContextPick('file')">Add file…</button>
    </div>
    <div class="path-field"><input id="f-ctx-new" placeholder="/absolute/path to add" onkeydown="if (event.key === 'Enter') { event.preventDefault(); addContextTyped(); }">
      <button type="button" onclick="addContextTyped()">Add</button></div></div>`;
}
function addContext(path, type) {
  path = path.trim();
  const list = $("#f-ctx");
  if (!path || !list) return;
  if ([...list.children].some((el) => el.dataset.path === path)) return; // no duplicates
  list.insertAdjacentHTML("beforeend", ctxItem(path, type));
}
export async function addContextPick(mode) {
  const path = await pickPath({ mode, title: mode === "dir" ? "Add a folder" : "Add a file", start: $("#f-root")?.value || "" });
  if (path != null) addContext(path, mode);
}
export function addContextTyped() {
  const input = $("#f-ctx-new");
  addContext(input.value, ""); // the server decides whether it is a folder or a file
  input.value = "";
  input.focus();
}
export function removeContext(btn) { btn.closest(".ctx-item").remove(); }
const readContext = () => [...document.querySelectorAll("#f-ctx .ctx-item")].map((el) => el.dataset.path);
function cardOptions(c, isNew) {
  const skipText = isNew ? "the prompt is used as-is" : "the title and description are the prompt";
  return `<label>Kind</label><select id="f-kind" onchange="kindChanged()">${KINDS.map(([v, l]) => `<option value="${v}" ${v === kindOf(c) ? "selected" : ""}>${l}</option>`).join("")}</select>
    <label>Project</label>${projectSelect(c.project_id)}
    <label>Root folder</label>${pathField("f-root", c.root_folder, "/path/to/folder/with/repos")}
    <div id="refine-opts">
    <label class="check"><input type="checkbox" id="f-autorun" ${c.auto_run ? "checked" : ""}> Run automatically after refinement (skip review in Ready)</label>
    <label class="check"><input type="checkbox" id="f-automodel" onchange="autoModelChanged()" ${c.auto_model ? "checked" : ""}> Automatic model: the refiner picks the smallest capable model and adapter (the manual model is the fallback)</label>
    <label class="check"><input type="checkbox" id="f-deep" ${c.deep_refinement ? "checked" : ""}> Deeper refinement (explore code)</label>
    ${!c.status || c.status === "todo" ? `<label class="check"><input type="checkbox" id="f-skip" ${c.skip_refinement ? "checked" : ""} onchange="skipChanged()"> Skip refinement (${skipText}; the card goes straight to Ready)</label>` : ""}
    </div>
    ${settingsFields(c)}
    ${toolsGroup(c)}
    ${contextGroup(c)}`;
}
export function readForm() {
  return {
    // The New card form has one prompt and no title: the server derives the title from it.
    ...($("#f-newprompt") ? { description: $("#f-newprompt").value } : { title: $("#f-title").value, description: $("#f-desc").value }),
    root_folder: $("#f-root").value,
    ...($("#f-project").value ? { project_id: +$("#f-project").value } : {}),
    auto_run: $("#f-autorun").checked, deep_refinement: $("#f-deep").checked, auto_model: $("#f-automodel").checked, harness: $("#f-harness").value, model: $("#f-model").value, effort: $("#f-effort").value,
    permission_mode: $("#f-mode").value, max_turns: +$("#f-turns").value, max_minutes: +$("#f-minutes").value,
    push_default: $("#f-push").value, kind: $("#f-kind").value,
    context: readContext(), // always sent, even when empty, so removals persist
    ...($("#f-tools") ? { tools: readTools() } : {}),
    ...($("#f-skip") ? { skip_refinement: $("#f-skip").checked } : {}), // only offered for new and Todo cards
  };
}
// A card that skips refinement has nothing to refine, so the refine button is off while the box is ticked.
// A chat never refines either, so its refinement options are hidden.
const noRefine = () => $("#f-kind")?.value === "chat" || !!$("#f-skip")?.checked;
export function skipChanged() { const b = $("#create-refine"); if (b) b.disabled = noRefine(); }
export function kindChanged() {
  const o = $("#refine-opts"); if (o) o.hidden = $("#f-kind").value === "chat";
  const g = $("#ctx-group"); if (g) g.hidden = $("#f-kind").value === "chat";
  applyToolDefaults();
  skipChanged();
}

// pid is the project whose "New card" button was used; without one (no projects yet) the card gets the default root folder.
export async function openNew(pid) {
  setOpenId("new");
  const s = await api("GET", "/api/settings");
  toolDefaults = s.default_tools || {};
  const p = projects.get(pid);
  const c = { title: "", description: "", project_id: p?.id, root_folder: p ? p.root_folder : s.default_root_folder, harness: s.harness, model: s.default_model, effort: s.default_effort,
    permission_mode: s.default_permission_mode, max_turns: s.default_max_turns, max_minutes: s.default_max_minutes, push_default: s.default_push_default,
    auto_run: s.default_auto_run, auto_model: s.auto_model, skip_refinement: s.default_skip_refinement, tools: toolDefaults.code || [] };
  openDrawer("New card", `<label>Prompt</label><textarea id="f-newprompt" rows="8" placeholder="Describe the task…" onkeydown="newPromptKey(event)"></textarea>
    ${sec("new", "options", "Options", cardForm(c, true), false)}
    <div class="actions"><button onclick="createCard(false)">Create</button><button class="primary" id="create-refine" onclick="createCard(true)">Create and refine</button></div>`);
  kindChanged();
  $("#f-newprompt").focus();
}
// Ctrl/Cmd+Enter in the prompt creates the card, and refines it unless refinement is skipped.
export function newPromptKey(e) {
  if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); createCard(!noRefine()); }
}
export async function createCard(refine) {
  if (!$("#f-newprompt").value.trim()) { alert("Write a prompt first."); $("#f-newprompt").focus(); return; }
  try {
    const c = await api("POST", "/api/cards", readForm());
    if (refine && !noRefine()) await api("POST", `/api/cards/${c.id}/refine`);
    openCard(c.id);
  } catch (e) { alert(e.message); }
}
export function setToolbox(v) { toolbox = v; }
