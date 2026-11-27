import { $, esc } from "./util.js";

// ---------- choice dialog ----------
// choose({title, body, choices}) opens a small modal (same look and behaviour as the picker: .modal-head,
// backdrop click and Escape dismiss, Tab stays inside, the page behind is inert) and resolves with the
// `value` of the clicked choice, or null when dismissed. Choices: {label, value, variant?}. Focus starts on
// the first non-danger choice, so a stray Enter never deletes.
const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");
let active = null; // only one at a time

export function choose({ title, body = "", choices }) {
  if (active) active.finish(null);
  const root = $("#choice");
  return new Promise((resolve) => {
    const lastFocus = document.activeElement;
    const bg = [$("header"), $("#shell"), $("#overlay")].map((el) => [el, el.inert]);
    const lines = (Array.isArray(body) ? body : [body]).filter(Boolean);
    root.innerHTML = `<div class="choice-bg"></div>
      <section class="choice-box" role="dialog" aria-modal="true" aria-labelledby="choice-title" aria-describedby="choice-body" tabindex="-1">
        <div class="modal-head"><h3 id="choice-title">${esc(title)}</h3><button class="close" type="button" data-x aria-label="Cancel">×</button></div>
        <div class="choice-body" id="choice-body">${lines.map((l) => `<p>${esc(l)}</p>`).join("")}</div>
        <div class="choice-foot">
          <button type="button" data-x>Cancel</button>
          ${choices.map((c, i) => `<button type="button" class="${c.variant || ""}" data-i="${i}">${esc(c.label)}</button>`).join("")}
        </div>
      </section>`;
    const box = root.querySelector(".choice-box");
    const st = { closeTimer: null };
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
    function onKey(e) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(null); return; }
      if (e.key === "Tab") {
        const f = [...box.querySelectorAll("button:not(:disabled)")];
        const first = f[0], last = f[f.length - 1];
        if (e.shiftKey && (document.activeElement === first || document.activeElement === box)) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      }
    }
    root.querySelector(".choice-bg").addEventListener("click", () => finish(null));
    for (const x of root.querySelectorAll("[data-x]")) x.addEventListener("click", () => finish(null));
    for (const b of root.querySelectorAll("[data-i]")) b.addEventListener("click", () => finish(choices[+b.dataset.i].value));
    window.addEventListener("keydown", onKey, true);
    clearTimeout(st.closeTimer);
    active = st;
    root.classList.remove("closing");
    root.classList.add("open");
    (box.querySelector("[data-i]:not(.danger)") || box.querySelector("[data-x]:not(.close)")).focus({ preventScroll: true });
  });
}
