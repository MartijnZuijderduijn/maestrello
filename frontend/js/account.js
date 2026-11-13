import { api } from "./api.js";
import { harnessIcon } from "./hicon.js";
import { morphInto } from "./morph.js";
import { $, esc } from "./util.js";

// ---------- account / usage indicator ----------
let usage = null; // last GET /api/account snapshot (or "usage" SSE event): {harnesses: [one entry per harness]}
const WINDOWS = { five_hour: "5h", seven_day: "7d", seven_day_opus: "7d Opus", seven_day_sonnet: "7d Sonnet", seven_day_overage_included: "7d incl. overage", overage: "overage" };
const STATUS_WORD = { allowed: "ok", allowed_warning: "warning", rejected: "limit reached" };
const winLabel = (t) => WINDOWS[t] || t;
const limClass = (l) => l.status === "rejected" ? "bad" : l.status === "allowed_warning" ? "warn" : "";
const pct = (u) => Math.round(u * 100) + "%"; // utilization is a fraction of the window
// fmtTime: "14:30" (time), "Mon 14:30" (day), "Mon, Nov 16, 14:30" (full).
function fmtTime(d, mode) {
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (mode === "time") return time;
  if (mode === "day") return d.toLocaleDateString([], { weekday: "short" }) + " " + time;
  return d.toLocaleDateString([], { weekday: "short", month: "short", day: "numeric" }) + " " + time;
}
function resetText(l, full) {
  if (!l.resets_at) return "";
  const d = new Date(l.resets_at * 1000), left = d - new Date();
  const mode = full ? "full" : left > 0 && left < 20 * 3600e3 ? "time" : left > 0 && left < 6 * 86400e3 ? "day" : "full";
  return (left <= 0 ? "reset " : "resets ") + fmtTime(d, mode);
}
// Gauges: fill is clamped to 0-100%; no utilization gives an empty dashed ring/bar, never a 0% fill.
const fillPct = (u) => Math.round(Math.min(1, Math.max(0, u)) * 1000) / 10;
const ring = (l) => l.utilization != null ? `<span class="ring" style="--p:${fillPct(l.utilization)}" aria-hidden="true"></span>` : `<span class="ring na" aria-hidden="true"></span>`;
const bar = (l) => l.utilization == null ? `<div class="bar na" aria-hidden="true"></div>` :
  `<div class="bar" role="progressbar" aria-label="${esc(winLabel(l.type))} used" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(l.utilization * 100)}"><i style="--f:${fillPct(l.utilization) / 100}"></i>` +
  (l.surpassed_threshold != null ? `<b style="left:${fillPct(l.surpassed_threshold)}%"></b>` : "") + `</div>`;
// limChip: ring + "5h 42% · resets 14:30"; the reset part is hidden on narrow screens.
function limChip(l) {
  const used = l.utilization != null ? pct(l.utilization) : (STATUS_WORD[l.status] || l.status || "?");
  const r = resetText(l, false);
  return `<span class="lim ${limClass(l)}">${ring(l)}${esc(winLabel(l.type) + " " + used)}${r ? `<span class="rs"> · ${esc(r)}</span>` : ""}</span>`;
}
const ACCT_LOADING = `<span class="skeleton acct" aria-hidden="true"></span><span class="sr-only">checking account…</span>`;
// What each harness's account refresh runs, for the Refresh button's tooltip.
const ACCOUNT_CMD = { claude: "claude auth status", mistral: "check for a Mistral API key and Vibe's cached account (~/.vibe)", omp: "omp usage --json (the accounts omp has logins or keys for)" };
const refreshTitle = (name) => "Re-run " + (ACCOUNT_CMD[name] || name + " account check");
// A harness that reports neither an account nor usage has nothing to show.
const shownHarnesses = () => (usage?.harnesses || []).filter((h) => h.account_supported || h.usage_supported);
function acctLine(h) {
  if (!h.account_supported) return { text: "account unknown", off: true };
  if (!h.checked_at && !h.account) return { text: "checking account…", off: true, loading: true };
  if (h.account && !h.account.logged_in) return { text: "not logged in", off: true };
  if (h.account) return { text: h.account.email || "logged in", off: !h.account.email };
  return { text: "account unknown", off: true };
}
// harnessGroup: icon + email/status + that harness's usage chips.
function harnessGroup(h) {
  const a = acctLine(h), limits = h.limits || [];
  return `<span class="agrp" data-harness="${esc(h.harness)}">${harnessIcon(h.harness)}` +
    `<span class="email${a.off ? " off" : ""}">${a.loading ? ACCT_LOADING : esc(a.text)}</span>` +
    (limits.length ? `<span class="lims">${limits.map(limChip).join("")}</span>` : "") + `</span>`;
}
function harnessTip(h) {
  const a = acctLine(h), limits = h.limits || [];
  const tip = [h.harness + ": " + a.text];
  if (h.account?.org_name) tip.push("org: " + h.account.org_name);
  if (h.account?.subscription_type) tip.push("plan: " + h.account.subscription_type);
  limits.forEach((l) => tip.push(winLabel(l.type) + ": " + (STATUS_WORD[l.status] || l.status || "?") + (l.utilization != null ? ", " + pct(l.utilization) + " used" : "") + (l.resets_at ? ", " + resetText(l, true) : "")));
  if (h.usage_supported && !limits.length) tip.push("no usage data yet; updates when an agent runs");
  return tip.join("\n");
}
export function renderAcct() {
  const btn = $("#acct"), hs = shownHarnesses();
  if (!usage) return; // the markup in index.html is the loading state until the first answer
  btn.setAttribute("aria-busy", String(hs.some((h) => acctLine(h).loading)));
  if (!hs.length) { // nothing reported, or the account call failed before any answer
    morphInto(btn, `<span class="email off">account unknown</span>`);
    btn.title = (usage.error || "no harness reports an account") + "\n(click for details)";
  } else {
    morphInto(btn, hs.map(harnessGroup).join(""));
    btn.title = hs.map(harnessTip).join("\n\n") + "\n(click for details)";
  }
  if (!$("#acct-pop").hidden) renderAcctPop();
}
function harnessSection(h) {
  const acct = h.account, a = acctLine(h), limits = h.limits || [];
  const row = (k, v) => v ? `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>` : "";
  let html = `<section class="hsec" data-harness="${esc(h.harness)}"><div class="head"><h4>${harnessIcon(h.harness)}${esc(h.harness)} · Account</h4>` +
    (h.account_supported ? `<button onclick="refreshAcct(this, '${esc(h.harness)}')" title="${esc(refreshTitle(h.harness))}">↻ Refresh</button>` : "") + `</div><dl>`;
  if (acct && acct.logged_in) {
    html += row("Email", acct.email) + row("Organisation", acct.org_name) + row("Auth method", acct.auth_method) +
      row("Provider", acct.api_provider) + row("Plan", acct.subscription_type);
  } else {
    html += a.loading ? `<dt>Status</dt><dd>${ACCT_LOADING}</dd>` : row("Status", a.text);
  }
  if (h.account_error) html += row("Error", h.account_error);
  if (h.checked_at) html += row("Checked", fmtTime(new Date(h.checked_at), "full"));
  html += `</dl><h4>Usage limits</h4>`;
  if (!h.usage_supported) html += `<div class="meta">This harness reports no usage limits.</div>`;
  else if (!limits.length) html += `<div class="meta">No usage data yet; updates when an agent runs.</div>`;
  for (const l of limits) {
    const extra = [];
    if (l.surpassed_threshold != null) extra.push("past the " + pct(l.surpassed_threshold) + " warning threshold");
    if (l.overage_status) extra.push("overage: " + l.overage_status + (l.is_using_overage ? " (in use)" : ""));
    html += `<div class="win ${limClass(l)}"><span class="badge ${limClass(l)}">${esc(winLabel(l.type))}</span>` +
      `<span class="${limClass(l)}">${esc(STATUS_WORD[l.status] || l.status || "unknown")}</span>` +
      (l.utilization != null ? ` · ${esc(pct(l.utilization))} used` : "") + bar(l) +
      `<div class="meta">${l.resets_at ? esc(resetText(l, true)) + " · " : ""}seen ${esc(fmtTime(new Date(l.observed_at), "full"))}` +
      (extra.length ? "<br>" + esc(extra.join(" · ")) : "") + `</div></div>`;
  }
  return html + `</section>`;
}
function renderAcctPop() {
  const hs = shownHarnesses();
  let html = hs.map(harnessSection).join("");
  if (!hs.length) html = `<div class="meta">${esc(usage?.error || "No harness reports an account or usage limits.")}</div>`;
  morphInto($("#acct-pop"), html);
}
export function toggleAcct(e) {
  e?.stopPropagation();
  const pop = $("#acct-pop"), open = pop.hidden;
  pop.hidden = !open; $("#acct").setAttribute("aria-expanded", String(open));
  if (open) renderAcctPop();
}
export async function loadAcct() {
  try { usage = await api("GET", "/api/account"); }
  catch (e) { // keep what we had, but say the account is unknown
    const at = new Date().toISOString();
    usage = { harnesses: (usage?.harnesses || []).map((h) => ({ ...h, account: null, account_error: e.message, checked_at: at })), error: e.message };
  }
  renderAcct();
}
// refreshAcct re-runs one harness's account lookup (the popover's ↻ Refresh).
export async function refreshAcct(btn, name) {
  if (btn) { // keep the width so the popover doesn't jump while the spinner replaces the label
    btn.style.minWidth = btn.offsetWidth + "px"; btn.disabled = true; btn.setAttribute("aria-busy", "true");
    btn.innerHTML = `<span class="spinner" aria-hidden="true"></span><span class="sr-only">Checking…</span>`;
  }
  try { usage = await api("GET", "/api/account?harness=" + encodeURIComponent(name) + "&refresh=1"); renderAcct(); }
  catch (e) { alert(e.message); }
  finally { if (btn && btn.isConnected) { btn.disabled = false; btn.removeAttribute("aria-busy"); btn.style.minWidth = ""; btn.textContent = "↻ Refresh"; } }
}
document.addEventListener("click", (e) => {
  const pop = $("#acct-pop");
  if (!pop.hidden && !pop.contains(e.target) && !$("#acct").contains(e.target)) toggleAcct();
});
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !$("#acct-pop").hidden) toggleAcct(); });
setInterval(() => { if (usage?.harnesses?.some((h) => h.limits?.length)) renderAcct(); }, 60e3); // keep "resets"/"reset" current
export function setUsage(v) { usage = v; }
