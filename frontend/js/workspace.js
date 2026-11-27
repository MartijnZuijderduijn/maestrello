import { api } from "./api.js";
import { morphInto } from "./morph.js";
import { visibleProjects } from "./projects.js";
import { cards } from "./state.js";
import { notice } from "./ui.js";
import { $, esc, lsGet, lsSet } from "./util.js";

// ---------- workspace panel ----------
// A right-hand panel listing the repos and folders the single visible project's cards work in.
const LS_KEY = "maestrello.workspace", POLL_MS = 30000, DEBOUNCE_MS = 300;
let open = lsGet(LS_KEY) === "1";
let shownPid = null;   // project id of the last fetch (or fetch in flight)
let data = null;       // last response for shownPid
let error = "";
let seq = 0, debounce = 0, poll = 0;

function applyOpen() {
  const el = $("#workspace"); if (el) el.hidden = !open;
  clearInterval(poll); poll = 0;
  if (open) poll = setInterval(() => { if (document.visibilityState === "visible" && shownPid != null) fetchWorkspace(shownPid); }, POLL_MS);
}
// Apply the saved state (call once at startup).
export function restoreWorkspace() { applyOpen(); renderWorkspace(); }

function soleProject() { const v = visibleProjects(); return v.length === 1 ? v[0] : null; }

export function renderWorkspace() {
  if (!open) return;
  const el = $("#workspace"); if (!el) return;
  const p = soleProject();
  if (!p) { shownPid = null; data = null; seq++; morphInto(el, notice("info", "Choose one project to see its workspace.")); return; }
  if (p.id !== shownPid) { data = null; error = ""; fetchWorkspace(p.id); }
  paint(el, p);
}
async function fetchWorkspace(pid) {
  shownPid = pid;
  const my = ++seq;
  try { const d = await api("GET", `/api/projects/${pid}/workspace`); if (my !== seq) return; data = d; error = ""; }
  catch (e) { if (my !== seq) return; error = e.message; }
  if (open) { const el = $("#workspace"), p = soleProject(); if (el && p && p.id === pid) paint(el, p); }
}
export function workspaceTouched(cardId) {
  if (!open || shownPid == null) return;
  if (cards.get(cardId)?.project_id !== shownPid) return;
  clearTimeout(debounce);
  debounce = setTimeout(() => { if (open && shownPid != null) fetchWorkspace(shownPid); }, DEBOUNCE_MS);
}
document.addEventListener("visibilitychange", () => { if (open && document.visibilityState === "visible" && shownPid != null) fetchWorkspace(shownPid); });

const link = (id, title) => `<a class="ws-card" href="#" onclick="openCard(${Number(id)}); return false">#${Number(id)} ${esc(title)}</a>`;
const hhmm = (s) => { const d = new Date(s); return isNaN(d) ? String(s ?? "") : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); };
const base = (p) => String(p).replace(/\/+$/, "").split("/").pop() || String(p);

function useHTML(u, kind) {
  const meta = [];
  if (u.session_id) meta.push(esc(u.session_id));
  const hm = [u.harness, u.model].filter(Boolean).join("·");
  if (hm) meta.push(esc(hm));
  if (u.activity) meta.push(esc(u.activity));
  if (u.planned) meta.push("planned");
  if (u.waiting_on) meta.push("waiting on " + esc(u.waiting_on));
  if (u.result) meta.push("result: " + esc(u.result));
  if (u.pr_url) meta.push(`<a href="${esc(u.pr_url)}" target="_blank" rel="noopener">PR</a>`);
  if (u.local_since) meta.push("local since " + esc(u.local_since));
  if (u.ahead != null) meta.push(`ahead ${Number(u.ahead)}`);
  if (u.dirty != null) meta.push(u.dirty ? "dirty" : "clean");
  return `<div class="ws-use${u.live ? " live" : ""}">${link(u.card_id, u.title)} <span class="ws-status ${esc(u.status)}">${esc(u.status)}</span>`
    + (u.branch ? ` <span class="ws-branch">${esc(u.branch)}${u.base_ref ? " from " + esc(u.base_ref) : ""}</span>` : "")
    + (u.worktree_path ? ` <span class="ws-wt meta">${esc(u.worktree_path)}</span>` : "")
    + (meta.length ? ` <span class="meta">${meta.join(" · ")}</span>` : "") + `</div>`;
}
function leaseHTML(l) {
  return `<div class="ws-lease${l.live ? "" : " stale"}">lease: ${l.other_board ? esc(l.title) : link(l.card_id, l.title)}`
    + (l.expires_at ? ` <span class="meta">expires ${esc(hhmm(l.expires_at))}</span>` : "")
    + (l.other_project ? ` <span class="ws-tag">other project</span>` : "") + `</div>`;
}
function entryHTML(e) {
  const kind = e.kind === "git" || e.kind === "local" || e.kind === "missing" ? e.kind : "";
  return `<div class="ws-entry"><div class="ws-path"><span title="${esc(e.abs)}">${esc(e.path === "." ? base(e.root) : e.path)}</span>`
    + (kind ? ` <span class="ws-kind ${kind}">${kind}</span>` : "") + (e.discovered ? ` <span class="ws-disc">discovered</span>` : "") + `</div>`
    + (e.uses || []).map((u) => useHTML(u, e.kind)).join("") + (e.leases || []).map(leaseHTML).join("")
    + (e.waiting?.length ? `<div class="ws-wait">waiting: ${e.waiting.map((w) => link(w.card_id, w.title)).join(", ")}</div>` : "") + `</div>`;
}
function paint(el, p) {
  let body;
  if (error && !data) body = notice("bad", "Could not load the workspace: " + esc(error));
  else if (!data) body = notice("info", "Loading workspace…");
  else {
    const es = data.entries || [];
    body = `<div class="ws-head"><b>${esc(p.name)}</b> <span class="meta">${esc(data.root_folder || p.root_folder)}</span></div>`
      + (es.length ? es.map(entryHTML).join("") : `<div class="ws-empty">No repos or folders in use yet.</div>`);
  }
  morphInto(el, body);
}
