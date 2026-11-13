import { act, api } from "./api.js";
import { loadDetail } from "./card.js";
import { closeDrawer, sec } from "./drawer.js";
import { readForm } from "./forms.js";
import { kindOf } from "./kinds.js";
import { cards, detail } from "./state.js";
import { $, esc, select } from "./util.js";

const ICON_REPO = `<svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"/></svg>`;
export async function loadDiffs(c) {
  const el = $("#diffs");
  try {
    const diffs = await api("GET", `/api/cards/${c.id}/diff`);
    if (!diffs.length) { el.className = ""; el.innerHTML = `<div class="empty-state">${ICON_REPO}<span>No repos checked out.</span></div>`; return; }
    el.className = "";
    el.innerHTML = diffs.map((d) => {
      if (d.repo.local_since) {
        return `<div class="repo"><div class="head"><b>${esc(d.repo.repo_path)}</b>
          <span class="meta">local work, edited in place; stays as is on approve or discard</span></div>
          ${d.error ? `<div class="notice bad">${esc(d.error)}</div>` : ""}
          <div class="meta">${esc(d.log)}</div>
          ${d.diff ? `<pre>${esc(d.diff)}</pre>` : ""}</div>`;
      }
      const empty = !d.diff && !d.log;
      return `<div class="repo"><div class="head"><b>${esc(d.repo.repo_path)}</b>
        ${empty ? `<span class="meta">no changes — will be dropped</span>` : `<select data-repo="${d.repo.id}">${[["merge", `Merge into ${baseBranch(d.repo.base_ref)}`], ["local", "keep local"], ["push", "push branch"], ["pr", "push + PR"]]
          .map(([v, l]) => `<option value="${v}" ${v === (c.push_default || "local") ? "selected" : ""}>${l}</option>`).join("")}</select>`}</div>
        <div class="meta">${esc(d.repo.branch)} from ${esc(d.repo.base_ref)}</div>
        ${d.error ? `<div class="notice bad">${esc(d.error)}</div>` : ""}
        ${d.log ? `<pre>${esc(d.log)}</pre>` : ""}
        ${d.diff ? sec(c.id, `diff:${d.repo.id}`, "Diff", `<pre class="diff">${colorDiff(d.diff)}</pre>`, false, "sub") : ""}</div>`;
    }).join("");
  } catch (e) { el.textContent = e.message; }
  finally { el.removeAttribute("aria-busy"); }
}
// The local branch a base ref such as origin/main stands for.
function baseBranch(ref) { return (ref || "").replace(/^origin\//, ""); }
function colorDiff(s) {
  return s.split("\n").map((l) => {
    const e = esc(l);
    if (l.startsWith("+") && !l.startsWith("+++")) return `<span class="add">${e}</span>`;
    if (l.startsWith("-") && !l.startsWith("---")) return `<span class="del">${e}</span>`;
    return e;
  }).join("\n");
}

export async function saveCard() {
  try { await api("PATCH", `/api/cards/${detail.card.id}`, readForm()); }
  catch (e) { alert(e.message); throw e; }
}
export async function saveReady() {
  const body = { refined_prompt: $("#f-prompt").value, repos: $("#f-repos").value.split("\n"), ...readForm() };
  try { await api("PATCH", `/api/cards/${detail.card.id}`, body); }
  catch (e) { alert(e.message); throw e; }
}
export async function saveAgent() {
  const body = { prompt: $("#a-prompt").value, model: $("#a-model").value, max_turns: +$("#a-turns").value, max_minutes: +$("#a-minutes").value };
  try { await api("PATCH", `/api/cards/${detail.card.id}/agent`, body); await loadDetail(detail.card.id); }
  catch (e) { alert(e.message); }
}
export async function deleteCard() {
  if (!confirm("Delete this card?")) return;
  try { await api("DELETE", `/api/cards/${detail.card.id}`); closeDrawer(); } catch (e) { alert(e.message); }
}
export function answer(inputId, allow = false, text) {
  act(detail.card.id, "answer", { input_id: inputId, allow, text: text ?? ($("#f-answer")?.value || "") });
}
export function replyChat(inputId) {
  if (!$("#f-answer").value.trim()) return alert("Write a reply first.");
  answer(inputId);
}
export function chatKey(e, inputId) {
  if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); replyChat(inputId); }
}
export function sendBack() {
  const text = $("#f-feedback").value.trim();
  if (!text) return alert("Write some feedback first.");
  act(detail.card.id, "send-back", { text });
}
export function approve() {
  const actions = {};
  document.querySelectorAll("select[data-repo]").forEach((s) => actions[s.dataset.repo] = s.value);
  // A plan creates one card per ticked task.
  const tasks = [...document.querySelectorAll("input[data-task]:checked")].map((i) => +i.dataset.task);
  act(detail.card.id, "approve", kindOf(detail.card) === "plan" ? { actions, tasks } : { actions });
}
// The plan's approve button says how many cards it will create.
export function planSelection() {
  const n = document.querySelectorAll("input[data-task]:checked").length;
  $("#approve-btn").textContent = `Create ${n} card${n === 1 ? "" : "s"}`;
}
export function discard() {
  if (confirm("Discard this card? Its worktrees and branches are deleted.")) act(detail.card.id, "discard");
}
