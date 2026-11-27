// Small HTML-string components shared by the modals, so like controls get like markup in every card state.
import { sec } from "./drawer.js";
import { harnessInfo, modelSelect } from "./harness.js";
import { esc } from "./util.js";

// A labelled control; `html` is the control's markup.
export const field = (label, html) => `<label>${label}</label>${html}`;
// A labelled textarea; `attrs` are extra attributes (placeholder, onkeydown…).
export const textField = (id, label, value = "", rows = 4, attrs = "") => field(label, `<textarea id="${id}" rows="${rows}" ${attrs}>${esc(value)}</textarea>`);
// A labelled number input.
const numField = (id, label, value, min) => field(label, `<input id="${id}" type="number" min="${min}" value="${value}">`);
// A coloured message box (kind info / warn / bad); `html` must already be escaped.
export const notice = (kind, html) => `<div class="notice ${kind}">${html}</div>`;
// An info notice with the pulsing dot, for work in progress.
export const busy = (text) => notice("info", `<span class="dot"></span>${esc(text)}`);
// A button; `variant` is primary / danger, `attrs` extra attributes (id, disabled…).
export const button = (label, onclick, variant = "", attrs = "") => `<button${variant ? ` class="${variant}"` : ""} onclick="${onclick}" ${attrs}>${label}</button>`;
// A button that asks `question` first (no double quotes in it).
export const confirmButton = (label, question, onclick, variant = "danger") => button(label, `if (confirm('${question}')) ${onclick}`, variant);
// A row of buttons; falsy entries are left out.
export const actions = (...btns) => `<div class="actions">${btns.filter(Boolean).join("")}</div>`;
// The prompt textarea of the New card form, the Ready card and the agent panel.
export const promptField = (id, value, attrs = "") => textField(id, "Prompt", value, 8, attrs);
// The free-text reply to the agent (answer, reply or note); answer() and replyChat() read #f-answer.
export const answerField = (label, attrs = "") => textField("f-answer", label, "", 4, attrs);
// "Send back with feedback" and its button row; sendBack() reads #f-feedback.
export const feedbackBox = (variant, ...more) => textField("f-feedback", "Send back with feedback", "", 3) + actions(button("Send back", "sendBack()", variant), ...more);
// Model, max turns and max minutes, for ids `${p}-model` etc.; `min` 0 means unlimited is allowed.
export const agentParams = (p, c, min = 0) => `<div class="row">
    <div>${field("Model", modelSelect(`${p}-model`, c.model, harnessInfo(c.harness), c.auto_model))}</div>
    <div>${numField(`${p}-turns`, "Max turns", c.max_turns, min)}</div>
    <div>${numField(`${p}-minutes`, "Max minutes", c.max_minutes, min)}</div>
  </div>`;
// A card's repos, read-only: path, then branch, result and PR when there are any.
export const reposList = (repos) => repos.map((r) => `<div class="repo"><b>${esc(r.repo_path)}</b> — ${[
  r.branch && esc(`${r.branch} from ${r.base_ref}`), r.result && esc(r.result), r.error && `<span style="color:var(--bad)">failed: ${esc(r.error)}</span>`,
  r.pr_url && `<a href="${esc(r.pr_url)}" target="_blank" rel="noopener">${esc(r.pr_url)}</a>`].filter(Boolean).join(" · ") || "not checked out"}</div>`).join("");
// A card's repos, one path per line; saveReady() reads #f-repos.
export const reposEditor = (repos) => `<textarea id="f-repos" rows="4">${esc(repos.map((r) => r.repo_path).join("\n"))}</textarea>`;
// A collapsible section of plain text (description, summary, report, plan, agent message); `after` is appended markup.
export const textSection = (id, key, title, text, after = "", cls = "") => sec(id, key, title, `<pre${cls ? ` class="${cls}"` : ""}>${esc(text)}</pre>${after}`);
// The event log section; `rows` is the rendered events.
export const logSection = (id, rows) => sec(id, "log", "Log", `<div class="log" id="log">${rows || '<span class="meta">nothing yet</span>'}</div>`);
