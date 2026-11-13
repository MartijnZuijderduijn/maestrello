import { api } from "./api.js";
import { $, esc } from "./util.js";

// ---------- folder / file picker ----------
// pickPath({mode, start, title}) opens a modal over everything else (including the card drawer) and
// resolves with the chosen absolute path, or null when it is dismissed.
const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");
export const FOLDER = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>';
export const FILE = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/></svg>';
let active = null; // the open picker's state; only one at a time

export function pickPath({ mode = "dir", start = "", title = "" } = {}) {
  if (active) active.finish(null);
  const file = mode === "file";
  const root = $("#picker");
  return new Promise((resolve) => {
    const st = { cur: "", parent: "", seq: 0, hidden: false, closeTimer: null };
    const lastFocus = document.activeElement;
    // Everything behind the picker goes inert; remember what already was so it is restored exactly.
    const bg = [$("header"), $("#board"), $("#overlay")].map((el) => [el, el.inert]);
    root.innerHTML = `<div class="picker-bg"></div>
      <section class="picker-box" role="dialog" aria-modal="true" aria-labelledby="picker-title" tabindex="-1">
        <div class="modal-head"><h3 id="picker-title"></h3><button class="close" type="button" data-x aria-label="Cancel">×</button></div>
        <div class="picker-bar">
          <button type="button" data-up aria-label="Up one folder" title="Up one folder">↑ Up</button>
          <input data-path type="text" spellcheck="false" autocomplete="off" aria-label="Path (Enter to go)">
        </div>
        <div class="picker-err notice bad" role="alert" hidden></div>
        <div class="picker-list" role="listbox" aria-label="${file ? "Folders and files" : "Folders"}"></div>
        <div class="notice warn picker-trunc" hidden>Only the first entries are shown; this folder has more.</div>
        <div class="picker-foot">
          <label class="picker-hid"><input type="checkbox" data-hidden> Show hidden</label>
          <span class="spacer"></span>
          <button type="button" data-x>Cancel</button>
          ${file ? "" : '<button type="button" class="primary" data-sel>Select this folder</button>'}
        </div>
      </section>`;
    const box = root.querySelector(".picker-box"), list = root.querySelector(".picker-list"), input = root.querySelector("[data-path]");
    const err = root.querySelector(".picker-err"), up = root.querySelector("[data-up]"), sel = root.querySelector("[data-sel]");
    root.querySelector("#picker-title").textContent = title || (file ? "Choose a file" : "Choose a folder");

    function finish(value) {
      if (active !== st) return;
      active = null;
      window.removeEventListener("keydown", onKey, true);
      for (const [el, was] of bg) el.inert = was;
      const done = () => { root.classList.remove("open", "closing"); root.innerHTML = ""; };
      if (reducedMotion.matches) done(); else { root.classList.add("closing"); st.closeTimer = setTimeout(done, 180); }
      if (lastFocus?.isConnected) lastFocus.focus({ preventScroll: true });
      resolve(value);
    }
    st.finish = finish;

    function rows() { return [...list.querySelectorAll(".picker-row")]; }
    function onKey(e) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(null); return; }
      if (e.key === "Tab") { // keep focus inside the dialog
        const f = [...box.querySelectorAll("button:not(:disabled), input")].filter((x) => x.offsetParent !== null);
        if (!f.length) return;
        const first = f[0], last = f[f.length - 1];
        if (e.shiftKey && (document.activeElement === first || document.activeElement === box)) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
        return;
      }
      const r = rows(), i = r.indexOf(document.activeElement);
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        if (!r.length || document.activeElement === input && e.key === "ArrowUp") return;
        e.preventDefault();
        const n = e.key === "ArrowDown" ? (i < 0 ? 0 : Math.min(i + 1, r.length - 1)) : (i <= 0 ? 0 : i - 1);
        r[n].focus();
      } else if (e.key === "Home" || e.key === "End") {
        if (i >= 0) { e.preventDefault(); r[e.key === "Home" ? 0 : r.length - 1].focus(); }
      } else if (e.key === "Backspace" && i >= 0 && st.parent) { e.preventDefault(); go(st.parent, true); }
    }

    function showErr(msg) { err.textContent = msg; err.hidden = !msg; }
    // Navigate to `path` ("" = the server's default start). On failure stay put and show the error;
    // with `fallback` an unlistable path retries the server default instead.
    async function go(path, focusList = false, fallback = false) {
      const seq = ++st.seq;
      const q = new URLSearchParams();
      if (path) q.set("path", path);
      if (file) q.set("files", "1");
      if (st.hidden) q.set("hidden", "1");
      let l;
      try { l = await api("GET", "/api/fs/list?" + q); }
      catch (e) {
        if (seq !== st.seq || active !== st) return;
        if (fallback && path) return go("", focusList);
        showErr(e.message); input.value = st.cur || path; return;
      }
      if (seq !== st.seq || active !== st) return;
      showErr("");
      st.cur = l.path; st.parent = l.parent || "";
      input.value = l.path; up.disabled = !st.parent;
      if (sel) sel.disabled = false;
      root.querySelector(".picker-trunc").hidden = !l.truncated;
      const entries = l.entries || [];
      list.innerHTML = entries.length ? entries.map((en, i) => en.type === "dir"
        ? `<button type="button" class="picker-row dir" role="option" tabindex="-1" data-i="${i}">${FOLDER}<span class="nm">${esc(en.name)}</span>${en.is_repo ? '<span class="badge">repo</span>' : ""}</button>`
        : `<button type="button" class="picker-row file" role="option" tabindex="-1" data-i="${i}">${FILE}<span class="nm">${esc(en.name)}</span></button>`).join("")
        : `<div class="meta picker-empty">${file ? "No folders or files here." : "No subfolders here."}</div>`;
      list.scrollTop = 0; st.entries = entries;
      if (focusList) (rows()[0] || input).focus({ preventScroll: true });
    }
    const join = (n) => st.cur.replace(/\/+$/, "") + "/" + n;

    list.addEventListener("click", (e) => {
      const b = e.target.closest(".picker-row"); if (!b) return;
      const en = st.entries[+b.dataset.i];
      if (en.type === "dir") go(join(en.name), true); else finish(join(en.name));
    });
    up.addEventListener("click", () => { if (st.parent) go(st.parent, true); });
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); go(input.value.trim()); } });
    root.querySelector("[data-hidden]").addEventListener("change", (e) => { st.hidden = e.target.checked; go(st.cur); });
    root.querySelector(".picker-bg").addEventListener("click", () => finish(null));
    for (const x of root.querySelectorAll("[data-x]")) x.addEventListener("click", () => finish(null));
    if (sel) { sel.disabled = true; sel.addEventListener("click", () => st.cur && finish(st.cur)); }

    active = st;
    clearTimeout(st.closeTimer);
    for (const [el] of bg) el.inert = true;
    root.classList.remove("closing"); root.classList.add("open");
    window.addEventListener("keydown", onKey, true); // window capture runs before drawer.js's document listener
    box.focus({ preventScroll: true });
    go(start || "", true, true);
  });
}

// A text input plus a "Browse…" button that fills it through pickPath. Typing a path still works;
// callers keep reading the input's .value.
export function pathField(id, value, placeholder = "", title = "Choose a folder") {
  return `<div class="path-field"><input id="${id}" value="${esc(value)}"${placeholder ? ` placeholder="${esc(placeholder)}"` : ""}>
    <button type="button" onclick="browsePath('${id}', '${esc(title)}')">Browse…</button></div>`;
}
export async function browsePath(id, title) {
  const input = $("#" + id);
  const path = await pickPath({ mode: "dir", start: input.value, title });
  if (path == null) return; // cancelled: leave the value alone
  input.value = path;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  input.dispatchEvent(new Event("change", { bubbles: true }));
}
