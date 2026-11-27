import { act, api } from "./api.js";
import { avatarHTML } from "./avatars.js";
import { closeDrawer, openDrawer, sec } from "./drawer.js";
import { cardForm, ctxIcon, refreshToolSupport, toolbox } from "./forms.js";
import { harnessIcon } from "./harness.js";
import { kindOf } from "./kinds.js";
import { kindPill, statePill } from "./pills.js";
import { md } from "./markdown.js";
import { projects } from "./projects.js";
import { loadDiffs } from "./review.js";
import { COLUMNS, cards, detail, openId, setDetail, setOpenId } from "./state.js";
import { actions, agentParams, answerField, busy, button, confirmButton, feedbackBox, logSection, notice, promptField, reposEditor, reposList, textSection } from "./ui.js";
import { $, esc } from "./util.js";

// While the card loads, the modal opens with placeholder lines; renderDetail's openDrawer swaps in the real body.
export async function openCard(id) {
  setOpenId(id);
  openDrawer(cards.get(id)?.title || `Card #${id}`, DETAIL_LOADING);
  $("#drawer-body").setAttribute("aria-busy", "true");
  try { await loadDetail(id); }
  catch (e) { if (openId === id) closeDrawer(); alert(e.message); } // no stuck placeholder
}
const DETAIL_LOADING = `<span class="skeleton title" aria-hidden="true"></span><span class="skeleton line" aria-hidden="true"></span><span class="skeleton line" aria-hidden="true"></span><span class="skeleton line short" aria-hidden="true"></span><span class="sr-only" role="status">Loading card…</span>`;
export async function loadDetail(id) {
  if (openId !== id) return;
  const d = await api("GET", `/api/cards/${id}`);
  if (openId !== id) return; // closed or switched while loading
  setDetail(d);
  renderDetail();
}

let shownId = null; // card id last rendered by renderDetail, for scroll restore
function renderDetail() {
  const { card: c, repos, inputs, events, live } = detail;
  // Keep the scroll position across live re-renders of the same card; start at the top for another one.
  const scroll = shownId === c.id && $("#overlay").classList.contains("open") ? $("#drawer-body").scrollTop : 0;
  shownId = c.id;
  const label = Object.fromEntries(COLUMNS)[c.status];
  let body = "";

  if (c.warning) body += notice("warn", `⚠ ${esc(c.warning)}`);
  if (c.status === "failed" && c.fail_reason) body += notice("bad", esc(c.fail_reason));
  const kind = kindOf(c);
  const refine = (label, variant, wrap = (a) => a) => kind === "chat" ? "" : button(label, wrap(`act(${c.id}, 'refine')`), variant);

  switch (c.status) {
    case "todo":
      body += sec(c.id, "card", "Card", cardForm(c))
        + actions(button("Save", "saveCard()"), refine("Refine", "primary", (a) => `saveCard().then(() => ${a})`), delBtn());
      break;
    case "refining":
      body += busy("Refining…") + textSection(c.id, "description", "Description", c.description || "(none)");
      break;
    case "ready":
      body += (c.refine_notes ? notice("info", esc(c.refine_notes)) : "")
        + promptSection(c, true)
        + sec(c.id, "repos", `Repos (one per line, relative to ${esc(c.root_folder)}${kind === "code" ? "" : "; informational: this card only reads"})`, reposEditor(repos))
        + sec(c.id, "settings", "Card and run settings", cardForm(c), false)
        + actions(button("Save", "saveReady()"), button("Run", `saveReady().then(() => act(${c.id}, 'run'))`, "primary"), refine("Refine again"), delBtn());
      break;
    case "running":
      body += c.waiting_on
        ? notice("warn", `${esc(c.activity || `waiting on card #${c.waiting_on}`)}. Only one card at a time may edit a local folder; this one starts on its own when <a href="#" onclick="event.preventDefault(); openCard(${c.waiting_on})">#${c.waiting_on}</a> is done with it.`)
          + actions(confirmButton("Stop waiting", "Take the card out of the queue? It goes to Failed.", `act(${c.id}, 'stop')`))
        : busy(c.activity || "working") + actions(stop(c));
      break;
    case "needs_input":
      body += inputPanel(c, inputs.find((i) => !i.answered_at), live);
      break;
    case "review": {
      const tasks = c.plan_tasks || [];
      const idle = live ? `disabled title="agent still finishing"` : "";
      body += summarySection(c);
      if (kind === "plan") {
        body += sec(c.id, "tasks", "Proposed tasks", tasks.map((t, i) => `<label class="check task"><input type="checkbox" data-task="${i}" checked onchange="planSelection()">
            <span><b>${esc(t.title)}</b>${t.kind && t.kind !== "code" ? ` ${kindPill(t.kind)}` : ""}
            ${(t.repos || []).length ? `<span class="meta"> · ${esc(t.repos.join(", "))}</span>` : ""}<pre>${esc(t.description)}</pre></span></label>`).join("") || `<div class="meta">no tasks</div>`);
      } else if (kind === "code") {
        body += sec(c.id, "changes", "Changes", `<div id="diffs" class="meta" aria-busy="true"><span class="skeleton line" aria-hidden="true"></span><span class="skeleton line short" aria-hidden="true"></span><span class="skeleton line" aria-hidden="true"></span><span class="sr-only" role="status">Loading diffs…</span></div>`);
      }
      body += actions(button(kind === "plan" ? `Create ${tasks.length} cards` : "Approve", "approve()", "primary", `id="approve-btn" ${idle}`),
          kind === "plan" && button(`Create ${tasks.length} and refine`, "approve(true)", "primary", `id="approve-refine-btn" ${idle}`), discardBtn())
        + feedbackBox("");
      break;
    }
    case "failed":
      body += feedbackBox("primary", !c.session_id && refine("Refine again"), discardBtn());
      break;
    case "done":
      body += (c.summary ? summarySection(c) : "") + actions(delBtn());
      break;
  }

  // Todo and Ready cards edit these in their forms above; running, refining and waiting cards cannot be edited.
  if (["review", "failed", "done"].includes(c.status) && !live) body += agentPanel(c);
  if (!["todo", "ready", "review"].includes(c.status) && repos.length) body += sec(c.id, "repos", "Repos", reposList(repos));
  if (c.refined_prompt && !["ready", "todo"].includes(c.status)) body += promptSection(c);
  body += logSection(c.id, events.map(eventHTML).join(""));

  const avLive = (c.status === "running" || c.status === "refining") && !c.waiting_on; // same rule as the board card
  const sid = c.session_id ? `<span title="${esc(c.session_id)}">session ${esc(c.session_id.slice(0, 8))}…</span>` : "";
  const editable = c.status === "todo" || c.status === "ready"; // these show the context in their form

  // Create metadata for the modal header: status badge, card #, and type badge
  const meta = `${statePill(c.status, label || c.status)}<b>#${c.id}</b>${kind !== "code" ? kindPill(kind) : ""}`;

  // Agent info without the card #, type, and status (they're now in the header)
  const info = `<div class="agent-info">
      <div class="row"><span title="Harness">${harnessIcon(c.harness)}${esc(c.harness)}</span><span>${esc(c.model)}</span><span>effort ${esc(c.effort)}</span><span>${esc(c.permission_mode)}</span>${c.auto_run && (c.status === "todo" || c.status === "refining" || c.status === "ready") ? `<span title="Runs automatically after refinement">auto</span>` : ""}${c.skip_refinement ? `<span title="Refinement was skipped: the card text is the prompt">unrefined</span>` : ""}</div>
      <div class="row"><span>${c.turns_used} / ${c.max_turns || "∞"} turns</span><span>${c.max_minutes || "∞"} min limit</span>${c.cost_usd ? `<span>$${c.cost_usd.toFixed(2)}</span>` : ""}${sid}</div>
      ${c.waiting_on ? `<div class="row wait" title="${esc(c.activity || "")}"><span>waiting on #${c.waiting_on}</span></div>` : ""}
      <div class="row loc"><span title="Project">${esc(projects.get(c.project_id)?.name || "no project")}</span><span>${esc(c.root_folder)}</span></div>
      ${!editable && (c.context || []).length ? sec(c.id, "context", `Additional context (${c.context.length})`, `<div class="ctx-list">${c.context.map((i) => `<div class="ctx-item"><span class="ctx-icon" title="${i.type === "dir" ? "Folder" : "File"}">${ctxIcon(i.type)}</span><span class="ctx-path">${esc(i.path)}</span></div>`).join("")}</div>`, true, "sub") : ""}
    </div>`;
  const toolsSec = (c.tools || []).length ? sec(c.id, "tools", `Tools (${c.tools.length})`, `<div class="meta" title="Toolbox">${esc(c.tools.map((id) => toolbox.find((t) => t.id === id)?.label || id).join(", "))}</div>`, c.tools.length <= 5) : "";
  openDrawer(c.title, info + toolsSec + body, avatarHTML(c, avLive), meta);
  refreshToolSupport();
  $("#drawer-body").scrollTop = scroll;
  const log = $("#log"); if (log) log.scrollTop = log.scrollHeight;
  if (c.status === "review" && kind === "code") loadDiffs(c);
}

// Functions, not constants: the module cycle through ui.js may run this file before ui.js.
const delBtn = () => button("Delete", "deleteCard()", "danger"), discardBtn = () => button("Discard", "discard()", "danger");
const stop = (c) => confirmButton("Stop", "Stop the agent? The card goes to Failed.", `act(${c.id}, 'stop')`);
// The summary, report or plan the agent left.
function summarySection(c) {
  const title = { research: "Report", plan: "Plan" }[kindOf(c)] || "Summary";
  return textSection(c.id, "summary", title, c.summary || `(no ${title.toLowerCase()})`);
}
// The refined prompt as markdown; a Ready card edits it in a sub-section that previews into #prompt-md.
function promptSection(c, edit = false) {
  return sec(c.id, "prompt", "Refined prompt", `<div class="md"${edit ? ` id="prompt-md"` : ""}>${md(c.refined_prompt)}</div>`
    + (edit ? sec(c.id, "prompt-edit", "Edit", promptField("f-prompt", c.refined_prompt), false, "sub") : ""), edit);
}
// The prompt, model and budget of a card that is not running; the board refuses the edit of a running one too.
function agentPanel(c) {
  return sec(c.id, "agent", "Agent parameters", promptField("a-prompt", c.refined_prompt) + agentParams("a", c, 1)
    + actions(button("Save agent parameters", "saveAgent()")), false);
}

function inputPanel(c, input, live) {
  if (!input) return notice("info", "Waiting for input, but none is open.");
  const p = input.payload || {}, id = input.id;
  const via = live ? "" : `<div class="meta">The session isn't live; answering resumes it.</div>`;
  const reply = (verb, label, variant) => button(label, `answer(${id}, false, '${verb}')`, variant);
  switch (input.kind) {
    case "question":
      return textSection(c.id, "input", "The agent asks", p.question, via) + answerField("Your answer")
        + actions(button("Send answer", `answer(${id})`, "primary"), live ? stop(c) : discardBtn());
    case "permission":
      return textSection(c.id, "input", `Permission request: ${esc(p.tool_name)}`, JSON.stringify(p.input, null, 2), via) + answerField("Note to the agent (optional)")
        + actions(button("Allow", `answer(${id}, true)`, "primary"), button("Deny", `answer(${id}, false)`));
    case "chat":
      return textSection(c.id, "input", "The agent replies", p.text || "(no reply)", via, "chat-reply")
        + answerField("Your reply", `placeholder="Ctrl/Cmd+Enter to send" onkeydown="chatKey(event, ${id})"`)
        + actions(button("Reply", `replyChat(${id})`, "primary"), button("Close chat", `act(${c.id}, 'move', { status: 'done' })`));
    case "limit":
      return notice("warn", `${p.reason === "turn_limit" ? `Turn limit (${p.max_turns}) reached.` : `Time limit (${p.max_minutes} min) reached.`}\nContinue with a fresh budget, or discard the work.`)
        + actions(reply("continue", "Continue", "primary"), reply("discard", "Discard", "danger"));
    case "usage_limit":
      return notice("warn", `Usage limit hit.\n${esc(p.message)}`) + actions(reply("retry", "Retry", "primary"), reply("discard", "Discard", "danger"));
  }
  return `<pre>${esc(JSON.stringify(input))}</pre>`;
}

export function eventHTML(e) {
  const t = new Date(e.ts).toLocaleTimeString();
  const line = (cls, s) => `<div class="${cls}">${t} ${esc(s)}</div>`;
  const d = e.data || {};
  switch (e.kind) {
    case "board": return line("k-board", "▸ " + d.text);
    case "system": return d.subtype === "init" ? line("k-board", `session ${d.session_id} · ${d.model}`) : "";
    case "assistant":
      return (d.message?.content || []).map((b) =>
        b.type === "text" ? line("", b.text) :
        b.type === "tool_use" ? line("k-tool", `→ ${b.name} ${JSON.stringify(b.input).slice(0, 300)}`) : "").join("");
    case "user":
      return (d.message?.content || []).filter((b) => b.type === "tool_result").map((b) => {
        const text = Array.isArray(b.content) ? b.content.map((x) => x.text || "").join(" ") : String(b.content ?? "");
        return line(b.is_error ? "k-err" : "", `  ← ${text.slice(0, 300)}${text.length > 300 ? "…" : ""}`);
      }).join("");
    case "result": return line(d.is_error ? "k-err" : "k-result", `■ ${d.subtype}${d.is_error ? ": " + d.result : ""} · ${d.num_turns} turns · $${(d.total_cost_usd || 0).toFixed(3)}`);
    case "refine_result": return line(d.is_error ? "k-err" : "k-result", `■ refinement ${d.is_error ? "failed: " + d.result : "done"}`);
    default: return "";
  }
}
