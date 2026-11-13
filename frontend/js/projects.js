import { api } from "./api.js";
import { renderBoard } from "./board.js";
import { closeDrawer, openDrawer } from "./drawer.js";
import { morphInto } from "./morph.js";
import { pathField } from "./picker.js";
import { cards, setOpenId } from "./state.js";
import { $, esc, lsGet, lsSet } from "./util.js";

// ---------- projects ----------
// A project is a board of its own. The view shows one project as a plain board, or several as
// stacked swimlanes. The choice lives in localStorage and in the URL hash (#p=1,3 or #p=all).
export let projects = new Map();
export let sel = [];            // chosen project ids; empty means all projects
const LS_SEL = "maestrello.projects", LS_FOLD = "maestrello.collapsed";
export let folded = new Set((() => { try { return JSON.parse(lsGet(LS_FOLD) || "[]"); } catch (e) { return []; } })());
// Per-project lane heights in px, set by dragging the dividers between lanes.
const LS_H = "maestrello.laneheights";
export const laneHeights = (() => { try { const o = JSON.parse(lsGet(LS_H) || "{}"); return o && typeof o === "object" ? o : {}; } catch (e) { return {}; } })();
export const saveLaneHeights = () => lsSet(LS_H, JSON.stringify(laneHeights));
const parseSel = (v) => v && v !== "all" ? v.split(",").map(Number).filter((n) => n > 0) : [];
export function loadSel() {
  const m = /^#p=([\w,]*)$/.exec(location.hash);
  sel = parseSel(m ? m[1] : lsGet(LS_SEL));
}
export function setSel(ids) {
  sel = ids;
  const v = sel.length ? sel.join(",") : "all";
  lsSet(LS_SEL, v);
  history.replaceState(null, "", "#p=" + v);
  renderProjects(); renderBoard();
}
window.addEventListener("hashchange", () => { loadSel(); renderProjects(); renderBoard(); });
// The projects on show, in id order; a selection of projects that no longer exist falls back to all.
export function visibleProjects() {
  const all = [...projects.values()].sort((a, b) => a.id - b.id);
  const chosen = all.filter((p) => sel.includes(p.id));
  return chosen.length ? chosen : all;
}
export const projectCards = (pid) => [...cards.values()].filter((c) => c.project_id === pid);
export function renderProjects() {
  const vis = visibleProjects(), all = projects.size;
  $("#proj-label").textContent = !all ? "No projects" : vis.length === all ? "All projects" : vis.length === 1 ? vis[0].name : vis.length + " projects";
  const t = $("#proj-title");
  t.hidden = vis.length !== 1;
  t.textContent = vis.length === 1 ? "· " + vis[0].name : "";
  if (!$("#proj-pop").hidden) renderProjPop();
}
function renderProjPop() {
  const vis = new Set(visibleProjects().map((p) => p.id)), all = [...projects.values()].sort((a, b) => a.id - b.id);
  morphInto($("#proj-pop"), `<label class="check"><input type="checkbox" ${vis.size === all.length ? "checked" : ""} onchange="setSel([])"> <span>All projects</span></label><div class="sep"></div>` +
    all.map((p) => `<label class="check"><input type="checkbox" ${vis.has(p.id) ? "checked" : ""} onchange="toggleProject(${p.id})"><span style="min-width:0"><span>${esc(p.name)}</span><span class="r">${esc(p.root_folder)}</span></span><span class="n">${projectCards(p.id).length || ""}</span><button class="edit-proj" title="Edit project" aria-label="Edit ${esc(p.name)}" onclick="openEditProject(event, ${p.id})">✎</button></label>`).join("")
    + `<div class="sep"></div><button onclick="openNewProject()">New project…</button>`);
}
export function toggleProject(id) {
  const cur = new Set(visibleProjects().map((p) => p.id));
  if (!cur.delete(id)) cur.add(id);
  if (!cur.size) return renderProjPop(); // keep at least one on show
  setSel(cur.size === projects.size ? [] : [...cur].sort((a, b) => a - b));
}
export function toggleProj(e) {
  e?.stopPropagation();
  const pop = $("#proj-pop"), open = pop.hidden;
  pop.hidden = !open; $("#proj").setAttribute("aria-expanded", String(open));
  if (open) { const r = $("#proj").getBoundingClientRect(); pop.style.top = r.bottom + 6 + "px"; pop.style.left = Math.max(8, Math.min(r.left, innerWidth - pop.offsetWidth - 8)) + "px"; renderProjPop(); }
}
document.addEventListener("click", (e) => {
  const pop = $("#proj-pop");
  if (!pop.hidden && !pop.contains(e.target) && !$("#proj").contains(e.target)) toggleProj();
});
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !$("#proj-pop").hidden) toggleProj(); });
export async function openNewProject() {
  if (!$("#proj-pop").hidden) toggleProj();
  setOpenId("project");
  const s = await api("GET", "/api/settings");
  openDrawer("New project", `<label>Name</label><input id="p-name">
    <label>Root folder</label>${pathField("p-root", s.default_root_folder, "/path/to/folder/with/repos")}
    <div class="meta">New cards in this project start with this root folder.</div>
    <div class="actions"><button class="primary" onclick="createProject()">Create</button></div>`);
  $("#p-name").focus();
}
export async function openEditProject(e, id) {
  e?.preventDefault(); e?.stopPropagation();
  const p = projects.get(id);
  if (!p) return;
  if (!$("#proj-pop").hidden) toggleProj();
  setOpenId("project");
  openDrawer("Edit project", `<label>Name</label><input id="p-name" value="${esc(p.name)}" onkeydown="if (event.key === 'Enter') saveProject(${id})">
    <label>Root folder</label>${pathField("p-root", p.root_folder, "/path/to/folder/with/repos")}
    <div class="meta">New cards in this project start with this root folder. Existing cards keep theirs.</div>
    <div class="actions"><button class="primary" onclick="saveProject(${id})">Save</button></div>`);
  $("#p-name").focus();
}
export async function saveProject(id) {
  try {
    const p = await api("PATCH", "/api/projects/" + id, { name: $("#p-name").value, root_folder: $("#p-root").value });
    projects.set(p.id, p);
    closeDrawer();
    renderProjects();
    renderBoard();
  } catch (e) { alert(e.message); }
}
export async function createProject() {
  try {
    const p = await api("POST", "/api/projects", { name: $("#p-name").value, root_folder: $("#p-root").value });
    projects.set(p.id, p);
    closeDrawer();
    setSel([p.id]); // show the new project
  } catch (e) { alert(e.message); }
}
export function toggleLane(pid) {
  if (!folded.delete(pid)) folded.add(pid);
  lsSet(LS_FOLD, JSON.stringify([...folded]));
  renderBoard();
}
export function setProjects(v) { projects = v; }
export function setSelIds(v) { sel = v; }
