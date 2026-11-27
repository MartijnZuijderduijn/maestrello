import { act, api } from "./api.js";
import { loadDetail } from "./card.js";
import { closeDrawer, sec } from "./drawer.js";
import { readForm } from "./forms.js";
import { MERGE_DIRTY, baseBranch, mergeHint, repoActions } from "./repoactions.js";
import { kindOf } from "./kinds.js";
import { cards, detail } from "./state.js";
import { notice } from "./ui.js";
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
          ${d.error ? notice("bad", esc(d.error)) : ""}
          <div class="meta">${esc(d.log)}</div>
          ${d.diff ? `<pre>${esc(d.diff)}</pre>` : ""}</div>`;
      }
      const empty = !d.diff && !d.log;
      return `<div class="repo"><div class="head"><b>${esc(d.repo.repo_path)}</b>
        ${empty ? `<span class="meta">no changes — will be dropped</span>` : repoSelect(d.repo, c.push_default, d)}</div>
        <div class="meta">${esc(d.repo.branch)} from ${esc(d.repo.base_ref)}</div>
        ${d.repo.merge_pending ? notice("info", "Merge pending: the checkout has uncommitted changes. The board merges this branch once the checkout is clean (checked every half minute); approve again with another action to cancel.") : ""}
        <div id="md-hint-${d.repo.id}"></div>
        ${d.repo.error ? notice("bad", `Not closed: ${esc(d.repo.error)}. The worktree and branch are kept; choose an action and approve again.`) : ""}
        ${d.error ? notice("bad", esc(d.error)) : ""}
        ${d.log ? `<pre>${esc(d.log)}</pre>` : ""}
        ${d.diff ? sec(c.id, `diff:${d.repo.id}`, "Diff", `<pre class="diff">${colorDiff(d.diff)}</pre>`, false, "sub") : ""}</div>`;
    }).join("");
    el.querySelectorAll("select[data-repo]").forEach((s) => mergeChoiceChanged(s.dataset.repo));
  } catch (e) { el.textContent = e.message; }
  finally { el.removeAttribute("aria-busy"); }
}
function repoSelect(repo, pushDefault, d) {
  const { opts, selected } = repoActions(repo, pushDefault);
  const def = d.merge_dirty || "worktree";
  const hint = mergeHint(selected, def, d.checkout_dirty);
  // Strategy for a dirty checkout, preset to the resolved merge_dirty setting; this approval only.
  return `<select data-repo="${repo.id}" data-dirty="${d.checkout_dirty ? 1 : ""}" onchange="mergeChoiceChanged(${repo.id})">${opts.map(([v, l]) => `<option value="${v}" ${v === selected ? "selected" : ""}>${l}</option>`).join("")}</select>
    <select data-mdirty="${repo.id}" title="If the checkout has uncommitted changes" onchange="mergeChoiceChanged(${repo.id})" ${hint.show ? "" : "hidden"}>${MERGE_DIRTY.map(([v, l]) => `<option value="${v}" ${v === def ? "selected" : ""}>if dirty: ${l}</option>`).join("")}</select>`;
}
// Shows or hides the dirty-checkout strategy of a repo and the warning that Merge will not happen.
export function mergeChoiceChanged(id) {
  const act = document.querySelector(`select[data-repo="${id}"]`), md = document.querySelector(`select[data-mdirty="${id}"]`);
  if (!act || !md) return;
  const hint = mergeHint(act.value, md.value, !!act.dataset.dirty);
  md.hidden = !hint.show;
  const el = $(`#md-hint-${id}`);
  if (el) el.innerHTML = hint.warn ? notice("warn", `⚠ ${esc(hint.warn)}`) : "";
}
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
export function approve(refine = false) {
  const actions = {};
  const merge_dirty = {};
  document.querySelectorAll("select[data-repo]").forEach((s) => actions[s.dataset.repo] = s.value);
  document.querySelectorAll("select[data-mdirty]").forEach((s) => { if (actions[s.dataset.mdirty] === "merge") merge_dirty[s.dataset.mdirty] = s.value; });
  // A plan creates one card per ticked task.
  const tasks = [...document.querySelectorAll("input[data-task]:checked")].map((i) => +i.dataset.task);
  act(detail.card.id, "approve", kindOf(detail.card) === "plan" ? { actions, tasks, ...(refine ? { refine } : {}) } : { actions, merge_dirty });
}
// The plan's approve button says how many cards it will create.
export function planSelection() {
  const n = document.querySelectorAll("input[data-task]:checked").length;
  $("#approve-btn").textContent = `Create ${n} card${n === 1 ? "" : "s"}`;
  const b = $("#approve-refine-btn"); if (b) b.textContent = `Create ${n} and refine`;
}
export function discard() {
  if (confirm("Discard this card? Its worktrees and branches are deleted.")) act(detail.card.id, "discard");
}
