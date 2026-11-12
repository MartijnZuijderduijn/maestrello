import { loadAcct, renderAcct, setUsage } from "./account.js";
import { renderBoard } from "./board.js";
import { eventHTML, loadDetail } from "./card.js";
import { setHarnesses } from "./harness.js";
import { projects, renderProjects, sel, setProjects, setSelIds } from "./projects.js";
import { cards, detail, openId, setCards } from "./state.js";
import { $ } from "./util.js";

export async function api(method, path, body) {
  const res = await fetch(path, { method, headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(data?.error || res.statusText);
  return data;
}
export async function act(id, action, body) {
  try { await api("POST", `/api/cards/${id}/${action}`, body); await loadDetail(id); }
  catch (e) { alert(e.message); }
}

// ---------- live updates ----------
let refreshTimer = null;
function scheduleDetailRefresh() {
  if (typeof openId !== "number") return;
  // Don't clobber text the user is typing.
  const typing = document.activeElement && ["TEXTAREA", "INPUT", "SELECT"].includes(document.activeElement.tagName);
  if (typing && detail?.card.status === cards.get(openId)?.status) return;
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => loadDetail(openId), 150);
}
export function connect() {
  const es = new EventSource("/api/events");
  es.addEventListener("card", (m) => {
    const c = JSON.parse(m.data); cards.set(c.id, c); renderBoard();
    if (c.id === openId) scheduleDetailRefresh();
  });
  es.addEventListener("harnesses", async () => { try { setHarnesses(await api("GET", "/api/harnesses")); } catch (e) {} });
  es.addEventListener("usage", (m) => { setUsage(JSON.parse(m.data)); renderAcct(); });
  es.addEventListener("project", (m) => { const p = JSON.parse(m.data); projects.set(p.id, p); renderProjects(); renderBoard(); });
  es.addEventListener("project_deleted", (m) => {
    const id = JSON.parse(m.data).id; projects.delete(id); setSelIds(sel.filter((x) => x !== id)); renderProjects(); renderBoard();
  });
  es.addEventListener("deleted", (m) => { cards.delete(JSON.parse(m.data).id); renderBoard(); });
  es.addEventListener("event", (m) => {
    const e = JSON.parse(m.data);
    if (e.card_id !== openId || !detail) return;
    const log = $("#log"); if (!log) return;
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    if (log.querySelector(".meta")) log.innerHTML = "";
    const before = log.childElementCount;
    log.insertAdjacentHTML("beforeend", eventHTML(e));
    fadeInNew(log, before);
    if (atBottom) followBottom(log);
  });
  es.onopen = () => { loadCards(); loadAcct(); }; // catches an account check that finished before we subscribed
}
const FADE_MAX = 50; // fade in at most this many lines per append; older lines never animate
const SMOOTH_GAP = 250; // ms; appends arriving faster than this jump instead of queueing smooth scrolls
let lastSmooth = 0, scrollRaf = 0;
const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");
// Only the append path marks lines "new"; a full render of the log never does. One delegated listener per log clears the class.
function fadeInNew(log, from) {
  if (reducedMotion.matches) return;
  if (!log.dataset.fade) { log.dataset.fade = "1"; log.addEventListener("animationend", (ev) => ev.target.classList.remove("new")); }
  for (let el = log.lastElementChild, n = 0; el && n < FADE_MAX && n < log.childElementCount - from; el = el.previousElementSibling, n++) el.classList.add("new");
}
function followBottom(log) {
  const now = performance.now();
  if (reducedMotion.matches || now - lastSmooth < SMOOTH_GAP) { log.scrollTop = log.scrollHeight; lastSmooth = now - SMOOTH_GAP; return; }
  lastSmooth = now;
  if (scrollRaf) return;
  scrollRaf = requestAnimationFrame(() => { scrollRaf = 0; log.scrollTo({ top: log.scrollHeight, behavior: "smooth" }); });
}
export async function loadCards() {
  const [list, ps] = await Promise.all([api("GET", "/api/cards"), api("GET", "/api/projects")]);
  setCards(new Map(list.map((c) => [c.id, c])));
  setProjects(new Map(ps.map((p) => [p.id, p])));
  renderProjects(); renderBoard();
}
