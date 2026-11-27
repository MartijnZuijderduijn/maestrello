import { api } from "./api.js";
import { avatarHTML } from "./avatars.js";
import { gitIcon } from "./gicon.js";
import { morphInto } from "./morph.js";
import { visibleProjects } from "./projects.js";
import { cards } from "./state.js";
import { notice } from "./ui.js";
import { $, esc, lsGet, lsSet } from "./util.js";

// ---------- workspace page ----------
// A tree per visible project: its repos and folders, each with the worktrees, leases and waiting cards on it.
const LS_KEY = "maestrello.workspace.nodes", POLL_MS = 30000, DEBOUNCE_MS = 300;
const state = new Map();   // project id -> { data, error, seq }
const nodes = (() => { try { const o = JSON.parse(lsGet(LS_KEY) || "{}"); return o && typeof o === "object" ? o : {}; } catch (e) { return {}; } })();
let debounce = 0;

const pageEl = () => $("#page-workspace");
const shown = () => { const el = pageEl(); return el && !el.hidden ? el : null; };
const isOpen = (key) => nodes[key] ?? true;   // everything starts expanded; only toggled nodes are stored
function toggleNode(key) { nodes[key] = !isOpen(key); lsSet(LS_KEY, JSON.stringify(nodes)); paint(); }

// Page renderer (pages.js): show what we have and refresh it.
export function showWorkspace() { paint(); for (const p of visibleProjects()) fetchWorkspace(p.id); }
// Re-render from the data we have (board re-renders and project changes); fetch projects not seen yet.
export function renderWorkspace() {
  if (!shown()) return;
  for (const p of visibleProjects()) if (!state.has(p.id)) fetchWorkspace(p.id);
  paint();
}
async function fetchWorkspace(pid) {
  const s = state.get(pid) || { data: null, error: "", seq: 0 };
  state.set(pid, s);
  const my = ++s.seq;
  try { const d = await api("GET", `/api/projects/${pid}/workspace?git=1&discovered=1`); if (my !== s.seq) return; s.data = d; s.error = ""; }
  catch (e) { if (my !== s.seq) return; s.error = e.message; }
  paint();
}
export function workspaceTouched(cardId) {
  if (!shown()) return;
  const pid = cards.get(cardId)?.project_id;
  if (pid == null || !state.has(pid)) return;
  clearTimeout(debounce);
  debounce = setTimeout(() => { if (shown()) fetchWorkspace(pid); }, DEBOUNCE_MS);
}
const refreshAll = () => { if (shown() && document.visibilityState === "visible") for (const p of visibleProjects()) fetchWorkspace(p.id); };
setInterval(refreshAll, POLL_MS);
document.addEventListener("visibilitychange", refreshAll);
document.addEventListener("click", (e) => { const b = e.target.closest?.("#page-workspace [data-node]"); if (b) toggleNode(b.dataset.node); });

const hhmm = (s) => { const d = new Date(s); return isNaN(d) ? String(s ?? "") : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); };
const base = (p) => String(p).replace(/\/+$/, "").split("/").pop() || String(p);
const chip = (cls, icon, label, title = "") => `<span class="ws-chip ${cls}"${title ? ` title="${esc(title)}"` : ""}>${gitIcon(icon)}${esc(label)}</span>`;
const link = (id, title) => `<a class="ws-card" href="#" onclick="openCard(${Number(id)}); return false">#${Number(id)} ${esc(title)}</a>`;

// The outcome of a use: the result text is free-form, so match on its words.
function outcome(u) {
  const r = String(u.result || "");
  if (u.pr_url) return `<a class="ws-chip pr" href="${esc(u.pr_url)}" target="_blank" rel="noopener" title="${esc(r)}">${gitIcon("pr")}PR</a>`;
  if (/fail/i.test(r)) return chip("bad", "failed", "failed", r);
  if (/merged/i.test(r)) return chip("ok", "merge", "merged", r);
  if (/pushed/i.test(r)) return chip("ok", "push", "pushed", r);
  if (/kept/i.test(r)) return chip("local", "local", "kept local", r);
  if (r) return chip("", "commit", r, r);
  if (u.local_since) return chip("local", "local", "local work", "since " + u.local_since);
  if (u.planned) return chip("pending", "clock", "pending", "planned, no worktree yet");
  return u.status === "failed" ? chip("bad", "failed", "failed") : "";
}
function useHTML(u) {
  const c = cards.get(u.card_id) || { id: u.card_id, status: u.status };
  const meta = [];
  if (u.branch) meta.push(`<span class="ws-chip branch" title="${esc(u.worktree_path || "")}">${gitIcon(u.worktree_path ? "worktree" : "branch")}${esc(u.branch)}</span>`);
  if (u.base_ref) meta.push(`<span class="ws-base">from ${esc(u.base_ref)}</span>`);
  if (u.ahead != null) meta.push(chip("", "commit", `${Number(u.ahead)} ahead`, "commits ahead of the base"));
  if (u.dirty) meta.push(chip("warn", "dirty", "dirty", "uncommitted changes"));
  if (u.waiting_on) meta.push(chip("warn", "clock", "waiting on #" + Number(u.waiting_on)));
  meta.push(outcome(u));
  return `<li class="ws-use"><a class="ws-card" href="#" onclick="openCard(${Number(u.card_id)}); return false">${avatarHTML(c, !!u.live)}<span class="ws-t">#${Number(u.card_id)} ${esc(u.title)}</span></a><span class="ws-meta">${meta.join("")}</span></li>`;
}
function leaseHTML(l) {
  return `<li class="ws-extra${l.live ? "" : " stale"}">${gitIcon("lock")}<span>lease: ${l.other_board ? esc(l.title) : link(l.card_id, l.title)}`
    + (l.expires_at ? ` <span class="meta">expires ${esc(hhmm(l.expires_at))}</span>` : "") + (l.other_project ? ` <span class="meta">other project</span>` : "") + `</span></li>`;
}
const waitingHTML = (ws) => `<li class="ws-extra wait">${gitIcon("clock")}<span>waiting: ${ws.map((w) => link(w.card_id, w.title)).join(", ")}</span></li>`;

// A branch of the tree: a button (aria-expanded) over a list; collapsed nodes render no children.
function node(key, label, kids, cls = "") {
  const open = isOpen(key);
  return `<li class="ws-node${cls ? " " + cls : ""}"><button class="ws-toggle" data-node="${esc(key)}" aria-expanded="${open}">${gitIcon("chevron")}${label}</button>${open ? `<ul>${kids}</ul>` : ""}</li>`;
}
function entryHTML(pid, e) {
  const git = e.kind === "git", name = e.path === "." ? base(e.root) : e.path;
  const kids = (e.uses || []).map(useHTML).join("") + (e.leases || []).map(leaseHTML).join("") + (e.waiting?.length ? waitingHTML(e.waiting) : "");
  const label = `${gitIcon(git ? "repo" : "folder")}<span class="ws-name" title="${esc(e.abs)}">${esc(name)}</span><span class="ws-kind">${git ? "git" : e.kind === "missing" ? "missing" : "folder"}</span>${e.discovered ? `<span class="ws-kind">discovered</span>` : ""}`;
  const cls = "ws-entry" + (e.discovered && !kids ? " disc" : "");
  return kids ? node(`r:${pid}:${e.abs}`, label, kids, cls) : `<li class="ws-node ${cls}"><span class="ws-leaf">${label}</span></li>`;
}
function projectHTML(p) {
  const s = state.get(p.id);
  let kids;
  if (s?.error && !s.data) kids = notice("bad", "Could not load the workspace: " + esc(s.error));
  else if (!s?.data) kids = `<span class="sr-only" role="status">Loading workspace…</span><div aria-hidden="true"><span class="skeleton line"></span><span class="skeleton line short"></span></div>`;
  else {
    const es = s.data.entries || [];
    kids = es.length ? `<ul>${es.map((e) => entryHTML(p.id, e)).join("")}</ul>` : notice("info", "No repos or folders in use yet.");
  }
  const key = `p:${p.id}`, open = isOpen(key);
  const label = `${gitIcon("chevron")}<span class="ws-name">${esc(p.name)}</span><span class="meta">${esc(s?.data?.root_folder || p.root_folder || "")}</span>`;
  return `<li class="ws-project"><button class="ws-toggle" data-node="${esc(key)}" aria-expanded="${open}">${label}</button>${open ? kids : ""}</li>`;
}
function paint() {
  const el = shown(); if (!el) return;
  const ps = visibleProjects();
  morphInto(el, ps.length ? `<ul class="ws-tree">${ps.map(projectHTML).join("")}</ul>` : notice("info", "No projects yet."));
}
