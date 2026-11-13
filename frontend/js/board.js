import { api } from "./api.js";
import { kindOf } from "./kinds.js";
import { avatarHTML } from "./avatars.js";
import { loadDetail, openCard } from "./card.js";
import { closeDrawer } from "./drawer.js";
import { harnessIcon } from "./harness.js";
import { kindMeta } from "./kinds.js";
import { morphInto } from "./morph.js";
import { idsInRect, normRect } from "./marquee.js";
import { folded, laneHeights, projectCards, projects, saveLaneHeights, toggleLane, visibleProjects } from "./projects.js";
import { collapsedLast, resizePair } from "./splitter.js";
import { COLUMNS, cards, openId } from "./state.js";
import { $, esc, flash } from "./util.js";

// ---------- board ----------
const selected = new Set(); // ids of Shift+click-selected cards; survives re-renders
function columnsHTML(p, h) {
  const mine = projectCards(p.id);
  return `<div class="lane-cols"${h ? ` style="height:${h}px"` : ""}>` + COLUMNS.map(([key, name]) => {
    const list = mine.filter((c) => c.status === key).sort((a, b) => key === "done" ? b.id - a.id : a.id - b.id);
    const clear = key === "done" && list.length ? `<button class="clear" title="Delete every card in Done${projects.size > 1 ? " of " + esc(p.name) : ""}" onclick="clearDone(event, ${p.id})">Clear</button>`
      : key === "ready" && list.length ? `<button class="clear start" title="Run every card in Ready (and Todo cards that skip refinement)${projects.size > 1 ? " of " + esc(p.name) : ""}" onclick="startAllReady(event, ${p.id})">Start all</button>` : "";
    const refineAll = key === "todo" && list.some((c) => !c.skip_refinement && kindOf(c) !== "chat") ? `<button class="clear start" title="Refine every Todo card${projects.size > 1 ? " of " + esc(p.name) : ""}" onclick="refineAllTodo(event, ${p.id})">Refine all</button>` : "";
    const add = key === "todo" ? `<button class="clear add" title="New card in ${esc(p.name)}" onclick="openNew(${p.id})">New card</button>` : "";
    return `<section class="column" data-status="${key}" data-project="${p.id}" ondragover="dragOver(event)" ondragleave="dragLeave(event)" ondrop="dropCard(event)"><h2><span>${name}</span><span class="tools">${add}${refineAll}${clear}<span>${list.length || ""}</span></span></h2>
      <div class="cards">${list.length ? list.map(cardHTML).join("") : `<div class="empty-col">${ICON_DROP}<span>Drop cards here</span></div>`}</div></section>`;
  }).join("") + `</div>`;
}
const svgIcon = (d) => `<svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${d}</svg>`;
const ICON_BOARD = svgIcon(`<rect x="3" y="4" width="5" height="16" rx="1.5"/><rect x="9.5" y="4" width="5" height="10" rx="1.5"/><rect x="16" y="4" width="5" height="13" rx="1.5"/>`);
const ICON_DROP = svgIcon(`<path d="M12 4v11"/><path d="M7.5 10.5L12 15l4.5-4.5"/><path d="M5 20h14"/>`);
const CHEVRON = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M6 9l6 6 6-6"/></svg>`;
// Patching keeps elements, so scroll offsets normally survive; these cover elements that do get recreated.
function scrollKey(el) { return el.classList.contains("cards") ? el.parentElement.dataset.project + "/" + el.parentElement.dataset.status : null; }
function saveScroll(board) {
  const cols = new Map();
  for (const el of board.querySelectorAll(".column .cards")) if (el.scrollTop) cols.set(scrollKey(el), el.scrollTop);
  const lanes = new Map();
  for (const el of board.querySelectorAll(".lane-cols")) if (el.scrollLeft) lanes.set(el.closest(".lane")?.dataset.project ?? "", el.scrollLeft);
  return { top: board.scrollTop, cols, lanes };
}
function restoreScroll(board, s) {
  board.scrollTop = s.top;
  for (const el of board.querySelectorAll(".lane-cols")) { const x = s.lanes.get(el.closest(".lane")?.dataset.project ?? ""); if (x) el.scrollLeft = x; }
  for (const el of board.querySelectorAll(".column .cards")) { const y = s.cols.get(scrollKey(el)); if (y) el.scrollTop = y; }
}
// Entry animation: remember each card's last-rendered status and flag only new cards or status changes.
// The board is patched in place (morph.js), so untouched elements keep identity, animations and focus.
// The first render with cards is the baseline (null = not yet seen), so a page load animates nothing.
const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");
let seenStatus = null; // Map<card id, status> as of the previous render
function enterClass(c) {
  return seenStatus && !reducedMotion.matches && seenStatus.get(c.id) !== c.status ? " enter" : "";
}
// Bursts of SSE events (and the several callers of one change) coalesce into one patch per frame.
let boardRaf = 0;
export function renderBoard() {
  if (boardRaf) return;
  boardRaf = requestAnimationFrame(() => { boardRaf = 0; renderBoardNow(); });
}
function renderBoardNow() {
  const board = $("#board"), saved = saveScroll(board);
  try { renderBoardInner(); } finally {
    restoreScroll(board, saved);
    // Track every card, rendered or not, so cards revealed later (lane unfolded) don't animate.
    if (seenStatus || cards.size) seenStatus = new Map([...cards.values()].map((c) => [c.id, c.status]));
  }
}
// First load, before any data arrives: skeleton columns and cards in the real board's layout.
export function showBoardSkeleton() {
  const board = $("#board");
  board.className = "board single"; board.setAttribute("aria-busy", "true");
  board.innerHTML = `<span class="sr-only" role="status">Loading board…</span><div class="lane-cols" aria-hidden="true">` + COLUMNS.map(([, name], i) =>
    `<section class="column sk"><h2><span>${name}</span></h2><div class="cards">${Array.from({ length: [3, 1, 2, 2, 1, 2, 1, 2][i] }, () =>
      `<div class="card sk"><span class="skeleton title"></span><span class="skeleton line"></span><span class="skeleton line short"></span></div>`).join("")}</div></section>`).join("") + `</div>`;
}
// Load failed: replace the skeleton with the message so it doesn't shimmer forever.
export function showBoardError(msg) {
  const board = $("#board");
  board.removeAttribute("aria-busy"); board.innerHTML = `<div class="notice bad" role="alert">Could not load the board: ${esc(msg)}</div>`;
}
function renderBoardInner() {
  $("#board").removeAttribute("aria-busy");
  for (const id of selected) if (!cards.has(id)) selected.delete(id);
  const vis = visibleProjects(), board = $("#board");
  $("#new-card").hidden = projects.size > 0; // with projects, each one has its own New card button
  board.className = "board " + (vis.length > 1 ? "multi" : "single");
  if (vis.length <= 1) { morphInto(board, vis.length ? columnsHTML(vis[0]) : `<div class="empty-state big">${ICON_BOARD}<span>No projects yet. Create a card or a project to get started.</span></div>`); return; }
  const ordered = collapsedLast(vis, (p) => folded.has(p.id)); // collapsed lanes sink to the bottom
  morphInto(board, ordered.map((p, i) => {
    const off = folded.has(p.id);
    const split = i > 0 && !off && !folded.has(ordered[i - 1].id)
      ? `<div class="lane-split" role="separator" aria-orientation="horizontal" aria-label="Resize ${esc(ordered[i - 1].name)} and ${esc(p.name)}" title="Drag to resize" tabindex="0" data-above="${ordered[i - 1].id}" data-below="${p.id}"></div>` : "";
    return split + `<section class="lane${off ? " collapsed" : ""}" data-project="${p.id}"><div class="lane-head">
      <button class="fold" onclick="toggleLane(${p.id})" aria-expanded="${!off}" aria-label="${off ? "Expand" : "Collapse"} ${esc(p.name)}" title="${off ? "Expand" : "Collapse"}">${CHEVRON}</button>
      <b>${esc(p.name)}</b><button class="edit-proj" title="Edit project" aria-label="Edit ${esc(p.name)}" onclick="openEditProject(event, ${p.id})">✎</button><span class="root" title="${esc(p.root_folder)}">${esc(p.root_folder)}</span><span class="n">${projectCards(p.id).length} card${projectCards(p.id).length === 1 ? "" : "s"}</span></div>
      ${columnsHTML(p, laneHeights[p.id])}</section>`;
  }).join(""));
}
// Deterministic decorative avatar: look from the card id, pose and animation from the status.
// The negative delay keeps the animation phase continuous when the board is re-rendered.
function cardHTML(c) {
  // A card waiting for a local folder another card is editing shows a bubble instead of the spinner.
  const live = (c.status === "running" || c.status === "refining") && !c.waiting_on;
  return `<div class="card ${c.warning ? "warn" : ""}${enterClass(c)}${selected.has(c.id) ? " selected" : ""}" draggable="true" tabindex="0" data-id="${c.id}"
    onmousedown="if (event.shiftKey) event.preventDefault()"
    ondragstart="dragStart(event)" ondragend="dragEnd()" onclick="cardClick(event, ${c.id})" onkeydown="cardKey(event, ${c.id})">
    <button class="del" title="Delete card" aria-label="Delete card #${c.id}" onpointerdown="blockDrag()" onclick="deleteDoneCard(event, ${c.id})">×</button>
    <div class="th">${avatarHTML(c, live)}<div class="t">${esc(c.title)}</div></div>
    <div class="m">#${c.id}${kindMeta(c)} · <span title="${esc(c.harness)}">${harnessIcon(c.harness)}${esc(c.harness)}</span> · ${esc(c.model)} · ${esc(c.effort)} · ${esc(c.permission_mode)}${c.turns_used ? ` · ${c.turns_used} turns` : ""}${c.auto_run && (c.status === "todo" || c.status === "refining" || c.status === "ready") ? ` · <span title="Runs automatically after refinement">auto</span>` : ""}${c.skip_refinement ? ` · <span title="Refinement was skipped: the card text is the prompt">unrefined</span>` : ""}</div>
    ${c.waiting_on ? `<div class="waiting" title="${esc(c.activity || "")}"><span>waiting on #${c.waiting_on}</span></div>` : ""}
    ${c.activity ? `<div class="act">${live ? '<span class="dot"></span>' : ""}${esc(c.activity)}</div>` : ""}
  </div>`;
}

// Cards in any column can be deleted (Done cards also all at once) straight from the board. Deleting only
// removes board data, never branches. The "deleted" SSE events update the board.
let dragBlocked = false; // pressing a card's delete button must not start a drag
for (const ev of ["pointerup", "pointercancel"]) document.addEventListener(ev, () => dragBlocked = false);
export async function deleteDoneCard(e, id) {
  e.stopPropagation();
  dragBlocked = false;
  const c = cards.get(id);
  if (!c) return;
  // A running card has a live agent (unless it only waits for a folder); a card needing input
  // has one too. Other cards with work keep it as branches; their worktrees are removed.
  const agent = (c.status === "running" && !c.waiting_on) || c.status === "needs_input";
  const msg = c.status === "refining" ? null
    : agent ? "The agent is still running. Stop it and delete the card? Its branches are kept."
    : c.status === "running" ? "Take the card out of the queue and delete it?"
    : c.status === "todo" || c.status === "ready" ? "Delete this card?"
    : "Delete this card? Its worktrees are removed; its branches are kept.";
  if (msg === null) return alert("The card is being refined; wait for it to finish.");
  if (!confirm(msg)) return;
  try { await api("DELETE", `/api/cards/${id}${c.status === "done" ? "" : "?stop=1"}`); if (openId === id) closeDrawer(); }
  catch (err) { alert(err.message); }
}
export async function clearDone(e, pid) {
  e.stopPropagation();
  const n = projectCards(pid).filter((c) => c.status === "done").length;
  if (!confirm(`Delete all ${n} done card${n === 1 ? "" : "s"}? Their branches are kept.`)) return;
  const openDone = typeof openId === "number" && cards.get(openId)?.status === "done";
  try { await api("DELETE", "/api/cards?status=done&project=" + pid); if (openDone) closeDrawer(); }
  catch (err) { alert(err.message); }
}

// Start all: run every Ready card, as if Run had been pressed on each. The "card" SSE
// events move them on the board; only per-card errors need reporting here.
export async function startAllReady(e, pid) {
  e.stopPropagation();
  const n = projectCards(pid).filter((c) => c.status === "ready" || c.status === "todo" && c.skip_refinement).length;
  if (!confirm(`Start all ${n} ready card${n === 1 ? "" : "s"}?`)) return;
  try {
    const res = await api("POST", "/api/cards/run?status=ready&project=" + pid);
    const errs = Object.entries(res?.errors || {});
    if (errs.length) alert(`Started ${res.started}. Not started:\n` + errs.map(([id, msg]) => `#${id}: ${msg}`).join("\n"));
  } catch (err) { alert(err.message); }
  if (typeof openId === "number") await loadDetail(openId);
}

// Refine all: refine every Todo card that needs it, as if Refine had been pressed on each.
export async function refineAllTodo(e, pid) {
  e.stopPropagation();
  const n = projectCards(pid).filter((c) => c.status === "todo" && !c.skip_refinement && kindOf(c) !== "chat").length;
  if (!confirm(`Refine all ${n} todo card${n === 1 ? "" : "s"}?`)) return;
  try {
    const res = await api("POST", "/api/cards/refine?status=todo&project=" + pid);
    const errs = Object.entries(res?.errors || {});
    if (errs.length) alert(`Refining ${res.refined}. Not refined:\n` + errs.map(([id, msg]) => `#${id}: ${msg}`).join("\n"));
  } catch (err) { alert(err.message); }
}

// Selection: Shift+click toggles a card; Escape or a click on empty board space clears it.
export function cardKey(e, id) {
  if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); cardClick(e, id); }
}
export function cardClick(e, id) {
  if (!e.shiftKey) return openCard(id);
  if (!selected.delete(id)) selected.add(id);
  e.currentTarget.classList.toggle("selected", selected.has(id));
}
function clearSelection() {
  if (!selected.size) return;
  selected.clear();
  document.querySelectorAll(".card.selected").forEach((el) => el.classList.remove("selected"));
}
document.addEventListener("keydown", (e) => { if (e.key === "Escape") clearSelection(); });
$("#board").addEventListener("animationend", (e) => { if (e.target.classList?.contains("enter")) e.target.classList.remove("enter"); });
$("#board").addEventListener("click", (e) => {
  if (swallowClick) { swallowClick = false; return; } // the click that ends a drag-box must not clear it
  if (!e.target.closest(".card")) clearSelection();
});

// Drag-box: press on empty board space and drag to select every card the rectangle touches.
// Shift/Ctrl/Cmd adds to the current selection; otherwise the box replaces it.
let swallowClick = false;
const boardEl = $("#board");
boardEl.addEventListener("pointerdown", (e) => {
  swallowClick = false;
  if (e.button !== 0 || e.target.closest(".card, .lane-split, button, a, input, select, textarea, label")) return;
  if (e.offsetX > e.target.clientWidth || e.offsetY > e.target.clientHeight) return; // a scrollbar
  const x0 = e.clientX, y0 = e.clientY, id = e.pointerId;
  const base = e.shiftKey || e.ctrlKey || e.metaKey ? new Set(selected) : new Set();
  let box = null;
  const move = (m) => {
    if (m.pointerId !== id) return;
    if (!box) {
      if (Math.hypot(m.clientX - x0, m.clientY - y0) < 4) return;
      box = document.createElement("div");
      box.className = "marquee";
      document.body.append(box);
    }
    const r = normRect(x0, y0, m.clientX, m.clientY);
    Object.assign(box.style, { left: r.left + "px", top: r.top + "px", width: r.right - r.left + "px", height: r.bottom - r.top + "px" });
    const els = [...boardEl.querySelectorAll(".card")];
    const hit = new Set(idsInRect(r, els.map((el) => ({ id: +el.dataset.id, rect: el.getBoundingClientRect() }))));
    selected.clear();
    for (const i of base) selected.add(i);
    for (const i of hit) selected.add(i);
    for (const el of els) el.classList.toggle("selected", selected.has(+el.dataset.id));
  };
  const end = (u) => {
    if (u.pointerId !== id) return;
    document.removeEventListener("pointermove", move);
    document.removeEventListener("pointerup", end);
    document.removeEventListener("pointercancel", end);
    if (box) { box.remove(); swallowClick = true; }
  };
  document.addEventListener("pointermove", move);
  document.addEventListener("pointerup", end);
  document.addEventListener("pointercancel", end);
  e.preventDefault(); // no text selection while dragging
});

// Lane dividers: drag the bar between two stacked projects to trade height between them (pointer
// events cover mouse and touch). The new heights are stored per project, like the folded lanes.
boardEl.addEventListener("pointerdown", (e) => {
  const bar = e.target.closest(".lane-split");
  if (!bar || e.button !== 0) return;
  const ca = boardEl.querySelector(`.lane[data-project="${bar.dataset.above}"] .lane-cols`);
  const cb = boardEl.querySelector(`.lane[data-project="${bar.dataset.below}"] .lane-cols`);
  if (!ca || !cb) return;
  e.preventDefault(); e.stopPropagation(); // no text selection, no drag-box
  const y0 = e.clientY, a0 = ca.offsetHeight, b0 = cb.offsetHeight, id = e.pointerId;
  bar.classList.add("active");
  document.body.classList.add("resizing");
  try { bar.setPointerCapture(id); } catch (err) {}
  const move = (m) => {
    if (m.pointerId !== id) return;
    const [a, b] = resizePair(a0, b0, m.clientY - y0);
    ca.style.height = a + "px"; cb.style.height = b + "px";
  };
  const end = (u) => {
    if (u.pointerId !== id) return;
    bar.removeEventListener("pointermove", move);
    bar.removeEventListener("pointerup", end);
    bar.removeEventListener("pointercancel", end);
    bar.removeEventListener("lostpointercapture", end);
    bar.classList.remove("active");
    document.body.classList.remove("resizing");
    laneHeights[bar.dataset.above] = ca.offsetHeight; laneHeights[bar.dataset.below] = cb.offsetHeight;
    saveLaneHeights();
  };
  // Pointer capture keeps the events coming even when released outside the window.
  bar.addEventListener("pointermove", move);
  bar.addEventListener("pointerup", end);
  bar.addEventListener("pointercancel", end);
  bar.addEventListener("lostpointercapture", end);
});
// Keyboard: arrows on a focused divider nudge it.
boardEl.addEventListener("keydown", (e) => {
  const bar = e.target.closest?.(".lane-split");
  if (!bar || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
  const ca = boardEl.querySelector(`.lane[data-project="${bar.dataset.above}"] .lane-cols`);
  const cb = boardEl.querySelector(`.lane[data-project="${bar.dataset.below}"] .lane-cols`);
  if (!ca || !cb) return;
  e.preventDefault();
  const [a, b] = resizePair(ca.offsetHeight, cb.offsetHeight, e.key === "ArrowUp" ? -20 : 20);
  laneHeights[bar.dataset.above] = a; laneHeights[bar.dataset.below] = b;
  ca.style.height = a + "px"; cb.style.height = b + "px";
  saveLaneHeights();
});

// Manual moves: drag a card (or the whole selection) onto any column. The server decides what the move does.
export function dragStart(e) {
  if (dragBlocked) { e.preventDefault(); return; }
  const id = +e.currentTarget.dataset.id;
  const ids = selected.has(id) ? [...selected] : [id];
  e.dataTransfer.setData("text/plain", String(id));
  e.dataTransfer.effectAllowed = "move";
  for (const el of document.querySelectorAll(".card")) if (ids.includes(+el.dataset.id)) el.classList.add("dragging");
  dragIds = ids;
}
let dragIds = [];
export function dragEnd() { dragIds = []; document.querySelectorAll(".card.dragging").forEach((el) => el.classList.remove("dragging")); }
export function dragOver(e) { e.preventDefault(); e.dataTransfer.dropEffect = "move"; e.currentTarget.classList.add("drop"); }
export function dragLeave(e) { if (!e.currentTarget.contains(e.relatedTarget)) e.currentTarget.classList.remove("drop"); }
export async function dropCard(e) {
  e.preventDefault();
  const col = e.currentTarget, target = col.dataset.status, colName = col.querySelector("h2 span").textContent;
  col.classList.remove("drop");
  const id = +e.dataTransfer.getData("text/plain");
  const group = dragIds.length > 1;
  const ids = group ? dragIds : [id];
  // Board order: column order, then the order shown within the column.
  const order = (c) => COLUMNS.findIndex(([k]) => k === c.status);
  const moving = ids.map((i) => cards.get(i)).filter((c) => c && c.status !== target)
    .sort((a, b) => order(a) - order(b) || (a.status === "done" ? b.id - a.id : a.id - b.id));
  if (group) clearSelection();
  if (!moving.length) return;
  // A running card (not one still waiting for a folder) has a live agent: ask before halting it.
  const live = moving.filter((c) => c.status === "running" && !c.waiting_on);
  if (live.length === 1 && moving.length === 1) {
    if (!confirm(`The agent is still running. Stop it and move the card to ${colName}?`)) return;
  } else if (live.length && !confirm(`The agent is still running on ${live.map((c) => "#" + c.id).join(", ")}. Stop ${live.length === 1 ? "it" : "them"} and move the cards to ${colName}?`)) return;
  const errs = [];
  for (const c of moving) {
    try { await api("POST", `/api/cards/${c.id}/move`, { status: target, stop: live.includes(c) }); }
    catch (err) { errs.push(`#${c.id}: ${err.message}`); }
  }
  if (group && errs.length) flash(`Not moved:\n` + errs.join("\n"));
  else if (errs.length) flash(errs[0].replace(/^#\d+: /, ""));
  if (typeof openId === "number" && moving.some((c) => c.id === openId)) await loadDetail(openId);
}
export function blockDrag() { dragBlocked = true; }
