import { act, api } from "./api.js";
import { avatarHTML } from "./avatars.js";
import { closeDrawer, openDrawer, sec } from "./drawer.js";
import { cardForm, ctxIcon, refreshToolSupport, toolbox } from "./forms.js";
import { harnessIcon, harnessInfo, modelSelect } from "./harness.js";
import { kindOf } from "./kinds.js";
import { md } from "./markdown.js";
import { projects } from "./projects.js";
import { answer, chatKey, discard, loadDiffs, planSelection, replyChat, saveCard, saveReady } from "./review.js";
import { COLUMNS, cards, detail, openId, setDetail, setOpenId } from "./state.js";
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

  if (c.warning) body += `<div class="notice warn">⚠ ${esc(c.warning)}</div>`;
  if (c.status === "failed" && c.fail_reason) body += `<div class="notice bad">${esc(c.fail_reason)}</div>`;

  switch (c.status) {
    case "todo":
      body += sec(c.id, "card", "Card", cardForm(c)) + `<div class="actions"><button onclick="saveCard()">Save</button>
        ${kindOf(c) === "chat" ? "" : `<button class="primary" onclick="saveCard().then(() => act(${c.id}, 'refine'))">Refine</button>`}
        <button class="danger" onclick="deleteCard()">Delete</button></div>`;
      break;
    case "refining":
      body += `<div class="notice info"><span class="dot"></span>Refining…</div>` + descBlock(c);
      break;
    case "ready":
      body += `${c.refine_notes ? `<div class="notice info">${esc(c.refine_notes)}</div>` : ""}
        ${sec(c.id, "prompt", "Refined prompt", `<div class="md" id="prompt-md">${md(c.refined_prompt)}</div>`
          + sec(c.id, "prompt-edit", "Edit", `<textarea id="f-prompt" rows="14">${esc(c.refined_prompt)}</textarea>`, false, "sub"))}
        ${sec(c.id, "repos", `Repos (one per line, relative to ${esc(c.root_folder)}${kindOf(c) === "code" ? "" : "; informational: this card only reads"})`, `<textarea id="f-repos" rows="4">${esc(repos.map((r) => r.repo_path).join("\n"))}</textarea>`)}
        ${sec(c.id, "settings", "Card and run settings", cardForm(c), false)}
        <div class="actions"><button onclick="saveReady()">Save</button>
          <button class="primary" onclick="saveReady().then(() => act(${c.id}, 'run'))">Run</button>
          ${kindOf(c) === "chat" ? "" : `<button onclick="act(${c.id}, 'refine')">Refine again</button>`}
          <button class="danger" onclick="deleteCard()">Delete</button></div>`;
      break;
    case "running":
      if (c.waiting_on) {
        body += `<div class="notice warn">${esc(c.activity || `waiting on card #${c.waiting_on}`)}. Only one card at a time may edit a local folder; this one starts on its own when <a href="#" onclick="event.preventDefault(); openCard(${c.waiting_on})">#${c.waiting_on}</a> is done with it.</div>
          <div class="actions"><button class="danger" onclick="if (confirm('Take the card out of the queue? It goes to Failed.')) act(${c.id}, 'stop')">Stop waiting</button></div>`;
        break;
      }
      body += `<div class="notice info"><span class="dot"></span>${esc(c.activity || "working")}</div>
        <div class="actions"><button class="danger" onclick="if (confirm('Stop the agent? The card goes to Failed.')) act(${c.id}, 'stop')">Stop</button></div>`;
      break;
    case "needs_input":
      body += inputPanel(c, inputs.find((i) => !i.answered_at), live);
      break;
    case "review": {
      const kind = kindOf(c);
      const tasks = c.plan_tasks || [];
      const ro = kind !== "code"; // research and plan cards have no changes to review
      if (kind === "plan") {
        body += sec(c.id, "summary", "Plan", `<pre>${esc(c.summary || "(no plan)")}</pre>`)
          + sec(c.id, "tasks", "Proposed tasks", tasks.map((t, i) => `<label class="check task"><input type="checkbox" data-task="${i}" checked onchange="planSelection()">
            <span><b>${esc(t.title)}</b>${t.kind && t.kind !== "code" ? ` <span class="badge">${esc(t.kind)}</span>` : ""}
            ${(t.repos || []).length ? `<span class="meta"> · ${esc(t.repos.join(", "))}</span>` : ""}<pre>${esc(t.description)}</pre></span></label>`).join("") || `<div class="meta">no tasks</div>`);
      } else {
        body += sec(c.id, "summary", kind === "research" ? "Report" : "Summary", `<pre>${esc(c.summary || (ro ? "(no report)" : "(no summary)"))}</pre>`);
        if (!ro) body += sec(c.id, "changes", "Changes", `<div id="diffs" class="meta" aria-busy="true"><span class="skeleton line" aria-hidden="true"></span><span class="skeleton line short" aria-hidden="true"></span><span class="skeleton line" aria-hidden="true"></span><span class="sr-only" role="status">Loading diffs…</span></div>`);
      }
      body += `
        <div class="actions"><button class="primary" id="approve-btn" onclick="approve()" ${live ? "disabled title='agent still finishing'" : ""}>${kind === "plan" ? `Create ${tasks.length} cards` : "Approve"}</button>
          ${kind === "plan" ? `<button class="primary" id="approve-refine-btn" onclick="approve(true)" ${live ? "disabled title='agent still finishing'" : ""}>Create ${tasks.length} and refine</button>` : ""}
          <button class="danger" onclick="discard()">Discard</button></div>
        <label>Send back with feedback</label><textarea id="f-feedback" rows="3"></textarea>
        <div class="actions"><button onclick="sendBack()">Send back</button></div>`;
      break;
    }
    case "failed":
      body += `<label>Send back with feedback</label><textarea id="f-feedback" rows="3"></textarea>
        <div class="actions"><button class="primary" onclick="sendBack()">Send back</button>
          ${!c.session_id && kindOf(c) !== "chat" ? `<button onclick="act(${c.id}, 'refine')">Refine again</button>` : ""}
          <button class="danger" onclick="discard()">Discard</button></div>`;
      break;
    case "done":
      body += c.summary ? sec(c.id, "summary", { research: "Report", plan: "Plan" }[kindOf(c)] || "Summary", `<pre>${esc(c.summary)}</pre>`) : "";
      body += sec(c.id, "repos", "Repos", repos.map((r) => `<div class="repo"><b>${esc(r.repo_path)}</b> — ${esc(r.result || "not checked out")}
        ${r.pr_url ? ` · <a href="${esc(r.pr_url)}" target="_blank" rel="noopener">${esc(r.pr_url)}</a>` : ""}</div>`).join("") || `<div class="meta">none</div>`);
      body += `<div class="actions"><button class="danger" onclick="deleteCard()">Delete card</button></div>`;
      break;
  }

  // Todo and Ready cards edit these in their forms above; running, refining and waiting cards cannot be edited.
  if (["review", "failed", "done"].includes(c.status) && !live) body += agentPanel(c);
  if (!["todo", "ready", "done", "review"].includes(c.status) && repos.length) {
    body += sec(c.id, "repos", "Repos", repos.map((r) => `<div class="meta">${esc(r.repo_path)}${r.branch ? ` · ${esc(r.branch)} from ${esc(r.base_ref)}` : " · not checked out"}</div>`).join(""));
  }
  if (c.refined_prompt && !["ready", "todo"].includes(c.status)) {
    body += sec(c.id, "prompt", "Refined prompt", `<div class="md">${md(c.refined_prompt)}</div>`, false);
  }
  body += sec(c.id, "log", "Log", `<div class="log" id="log">${events.map(eventHTML).join("") || '<span class="meta">nothing yet</span>'}</div>`);

  const avLive = (c.status === "running" || c.status === "refining") && !c.waiting_on; // same rule as the board card
  const sid = c.session_id ? `<span title="${esc(c.session_id)}">session ${esc(c.session_id.slice(0, 8))}…</span>` : "";
  const editable = c.status === "todo" || c.status === "ready"; // these show the context in their form

  // Create metadata for the modal header: status badge, card #, and type badge
  const meta = `<span class="badge">${label}</span><b>#${c.id}</b>${kindOf(c) !== "code" ? `<span class="badge">${esc(kindOf(c))}</span>` : ""}`;

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
  if (c.status === "review" && kindOf(c) === "code") loadDiffs(c);
}

// The prompt, model and budget of a card that is not running; the board refuses the edit of a running one too.
function agentPanel(c) {
  return sec(c.id, "agent", "Agent parameters", `<label>Prompt</label><textarea id="a-prompt" rows="8">${esc(c.refined_prompt)}</textarea>
    <div class="row">
      <div><label>Model</label>${modelSelect("a-model", c.model, harnessInfo(c.harness), c.auto_model)}</div>
      <div><label>Max turns</label><input id="a-turns" type="number" min="1" value="${c.max_turns}"></div>
      <div><label>Max minutes</label><input id="a-minutes" type="number" min="1" value="${c.max_minutes}"></div>
    </div>
    <div class="actions"><button onclick="saveAgent()">Save agent parameters</button></div>`, false);
}

function descBlock(c) { return sec(c.id, "description", "Description", `<pre>${esc(c.description || "(none)")}</pre>`); }

function inputPanel(c, input, live) {
  if (!input) return `<div class="notice info">Waiting for input, but none is open.</div>`;
  const p = input.payload || {};
  const via = live ? "" : `<div class="meta">The session isn't live; answering resumes it.</div>`;
  switch (input.kind) {
    case "question":
      return sec(c.id, "input", "The agent asks", `<pre>${esc(p.question)}</pre>${via}`) + `
        <label>Your answer</label><textarea id="f-answer" rows="4"></textarea>
        <div class="actions"><button class="primary" onclick="answer(${input.id})">Send answer</button>
        ${live ? `<button class="danger" onclick="act(${c.id}, 'stop')">Stop agent</button>` : `<button class="danger" onclick="discard()">Discard</button>`}</div>`;
    case "permission":
      return sec(c.id, "input", `Permission request: ${esc(p.tool_name)}`, `<pre>${esc(JSON.stringify(p.input, null, 2))}</pre>${via}`) + `
        <label>Note to the agent (optional)</label><input id="f-answer">
        <div class="actions"><button class="primary" onclick="answer(${input.id}, true)">Allow</button>
          <button onclick="answer(${input.id}, false)">Deny</button></div>`;
    case "chat":
      return sec(c.id, "input", "The agent replies", `<pre class="chat-reply">${esc(p.text || "(no reply)")}</pre>${via}`) + `
        <label>Your reply</label><textarea id="f-answer" rows="4" placeholder="Ctrl/Cmd+Enter to send" onkeydown="chatKey(event, ${input.id})"></textarea>
        <div class="actions"><button class="primary" onclick="replyChat(${input.id})">Reply</button>
          <button onclick="act(${c.id}, 'move', { status: 'done' })">Close chat</button></div>`;
    case "limit":
      return `<div class="notice warn">${p.reason === "turn_limit" ? `Turn limit (${p.max_turns}) reached.` : `Time limit (${p.max_minutes} min) reached.`}
Continue with a fresh budget, or discard the work.</div>
        <div class="actions"><button class="primary" onclick="answer(${input.id}, false, 'continue')">Continue</button>
          <button class="danger" onclick="answer(${input.id}, false, 'discard')">Discard</button></div>`;
    case "usage_limit":
      return `<div class="notice warn">Usage limit hit.\n${esc(p.message)}</div>
        <div class="actions"><button class="primary" onclick="answer(${input.id}, false, 'retry')">Retry</button>
          <button class="danger" onclick="answer(${input.id}, false, 'discard')">Discard</button></div>`;
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
