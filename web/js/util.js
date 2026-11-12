export const $ = (s) => document.querySelector(s);
export const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
export const lsGet = (k) => { try { return localStorage.getItem(k); } catch (e) { return null; } };
export const lsSet = (k, v) => { try { localStorage.setItem(k, v); } catch (e) {} };
export function select(id, options, value) {
  return `<select id="${id}">${options.map((o) => `<option ${o === value ? "selected" : ""}>${o}</option>`).join("")}</select>`;
}
// A transient inline message at the top of the page, for errors that should not interrupt with a browser dialog.
export function flash(msg) {
  document.querySelector(".flash")?.remove();
  const d = document.createElement("div");
  d.className = "notice bad flash";
  d.setAttribute("role", "alert");
  d.textContent = msg;
  document.body.append(d);
  setTimeout(() => {
    if (!d.isConnected) return;
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) return d.remove();
    d.classList.add("closing");
    d.addEventListener("animationend", () => d.remove(), { once: true });
    setTimeout(() => d.remove(), 500); // fallback if animationend never fires
  }, 6000);
}
