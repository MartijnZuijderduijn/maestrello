import { renderBoard } from "./board.js";
import { morphInto } from "./morph.js";
import { showWorkspace } from "./workspace.js";
import { $, esc, lsGet, lsSet } from "./util.js";
import { buildHash, DEFAULT_PAGE, parseHash } from "./route.js";

// ---------- pages and left navigation ----------
// Each page is a container in #pages; the nav lists the registry and only the current page's container shows.
// To add a page: add its container to index.html and an entry here.
const svg = (d) => `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${d}</svg>`;
export const pages = [
  { id: "board", label: "Board", el: "#board", icon: svg('<rect x="3" y="4" width="5" height="16" rx="1"/><rect x="10" y="4" width="5" height="10" rx="1"/><rect x="17" y="4" width="4" height="13" rx="1"/>'), render: renderBoard },
  { id: "workspace", label: "Workspace", el: "#page-workspace", icon: svg('<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>'), render: showWorkspace },
];
const LS_NAV = "maestrello.nav";           // "1" collapsed, "0" expanded
const narrow = matchMedia("(max-width: 700px)");
let collapsed = true;

export const currentPage = () => pages.find((p) => p.id === parseHash().page) || pages[0];
function showPage() {
  const cur = currentPage();
  for (const p of pages) $(p.el).hidden = p !== cur;
  cur.render($(cur.el));
  renderNav();
}
// Go to a page, keeping the project selection; a history entry, so back/forward switch pages.
export function setPage(id) {
  const next = buildHash(id, parseHash().p);
  if (next === (location.hash || "#")) return;
  history.pushState(null, "", next);
  showPage();
  if (narrow.matches) setCollapsed(true);
}
// On a narrow screen the nav starts collapsed and the choice is not remembered; it overlays the content.
function setCollapsed(v, save = false) {
  collapsed = v;
  if (save && !narrow.matches) lsSet(LS_NAV, v ? "1" : "0");
  renderNav();
}
const loadCollapsed = () => narrow.matches || lsGet(LS_NAV) === "1";
export function toggleNav() { setCollapsed(!collapsed, true); }
function renderNav() {
  const nav = $("#nav"), cur = currentPage().id;
  nav.classList.toggle("collapsed", collapsed);
  morphInto(nav, `<div class="nav-in"><button class="nav-toggle" onclick="toggleNav()" aria-expanded="${!collapsed}" aria-controls="nav" aria-label="${collapsed ? "Expand" : "Collapse"} navigation" title="${collapsed ? "Expand" : "Collapse"} navigation">${svg('<path d="M3 6h18M3 12h18M3 18h18"/>')}</button>`
    + pages.map((p) => `<button class="nav-item" onclick="setPage('${esc(p.id)}')" title="${esc(p.label)}" aria-label="${esc(p.label)}"${p.id === cur ? ' aria-current="page"' : ""}>${p.icon}<span class="nav-label">${esc(p.label)}</span></button>`).join("") + `</div>`);
}
export function initPages() {
  collapsed = loadCollapsed();
  window.addEventListener("hashchange", showPage);
  narrow.addEventListener("change", () => setCollapsed(loadCollapsed()));
  document.addEventListener("click", (e) => { if (narrow.matches && !collapsed && !$("#nav").contains(e.target)) setCollapsed(true); });
  showPage();
}
export { DEFAULT_PAGE };
