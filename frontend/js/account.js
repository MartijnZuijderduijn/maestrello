import { api } from "./api.js";
import { morphInto } from "./morph.js";
import { $, esc } from "./util.js";

// ---------- account / usage indicator ----------
let usage = null; // last GET /api/account snapshot (or "usage" SSE event)
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
function acctLine(u) {
  if (!u || (!u.checked_at && !u.account)) return { text: "checking account…", off: true, loading: true };
  if (u.account && !u.account.logged_in) return { text: "not logged in", off: true };
  if (u.account) return { text: u.account.email || "logged in", off: !u.account.email };
  return { text: "account unknown", off: true };
}
export function renderAcct() {
  const u = usage, btn = $("#acct"), a = acctLine(u), limits = u?.limits || [];
  btn.setAttribute("aria-busy", String(!!a.loading));
  morphInto(btn, `<span class="email${a.off ? " off" : ""}">${a.loading ? ACCT_LOADING : esc(a.text)}</span>` +
    (limits.length ? `<span class="lims">${limits.map(limChip).join("")}</span>` : ""));
  const tip = [a.text];
  if (u?.account?.org_name) tip.push("org: " + u.account.org_name);
  if (u?.account?.subscription_type) tip.push("plan: " + u.account.subscription_type);
  limits.forEach((l) => tip.push(winLabel(l.type) + ": " + (STATUS_WORD[l.status] || l.status || "?") + (l.utilization != null ? ", " + pct(l.utilization) + " used" : "") + (l.resets_at ? ", " + resetText(l, true) : "")));
  if (!limits.length) tip.push("no usage data yet; updates when an agent runs");
  btn.title = tip.join("\n") + "\n(click for details)";
  if (!$("#acct-pop").hidden) renderAcctPop();
}
function renderAcctPop() {
  const u = usage, acct = u?.account, a = acctLine(u), limits = u?.limits || [];
  const row = (k, v) => v ? `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>` : "";
  let html = `<div class="head"><h4>Account</h4><button onclick="refreshAcct(this)" title="Re-run claude auth status">↻ Refresh</button></div><dl>`;
  if (acct && acct.logged_in) {
    html += row("Email", acct.email) + row("Organisation", acct.org_name) + row("Auth method", acct.auth_method) +
      row("Provider", acct.api_provider) + row("Plan", acct.subscription_type);
  } else {
    html += a.loading ? `<dt>Status</dt><dd>${ACCT_LOADING}</dd>` : row("Status", a.text);
  }
  if (u?.account_error) html += row("Error", u.account_error);
  if (u?.checked_at) html += row("Checked", fmtTime(new Date(u.checked_at), "full"));
  html += `</dl><h4>Usage limits</h4>`;
  if (!limits.length) html += `<div class="meta">No usage data yet; updates when an agent runs.</div>`;
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
  catch (e) { usage = { account: null, account_error: e.message, checked_at: new Date().toISOString(), limits: usage?.limits || [] }; }
  renderAcct();
}
export async function refreshAcct(btn) {
  if (btn) { // keep the width so the popover doesn't jump while the spinner replaces the label
    btn.style.minWidth = btn.offsetWidth + "px"; btn.disabled = true; btn.setAttribute("aria-busy", "true");
    btn.innerHTML = `<span class="spinner" aria-hidden="true"></span><span class="sr-only">Checking…</span>`;
  }
  try { usage = await api("GET", "/api/account?refresh=1"); renderAcct(); }
  catch (e) { alert(e.message); }
  finally { if (btn && btn.isConnected) { btn.disabled = false; btn.removeAttribute("aria-busy"); btn.style.minWidth = ""; btn.textContent = "↻ Refresh"; } }
}
document.addEventListener("click", (e) => {
  const pop = $("#acct-pop");
  if (!pop.hidden && !pop.contains(e.target) && !$("#acct").contains(e.target)) toggleAcct();
});
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !$("#acct-pop").hidden) toggleAcct(); });
setInterval(() => { if (usage?.limits?.length) renderAcct(); }, 60e3); // keep "resets"/"reset" current
export function setUsage(v) { usage = v; }
