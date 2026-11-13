// Minimal DOM patching: make `parent`'s children match the markup in `html` while keeping every
// element that did not change (same node, same animations, focus, scroll offsets, hover state).
// Elements are matched by a key (data-id / data-project+status / lane divider) or else by position and tag.
const KEEP = ["enter", "dragging", "drop"]; // classes owned by script/animations, not by the markup
const keyOf = (n) => {
  if (n.nodeType !== 1) return null;
  const d = n.dataset;
  if (d.id) return "id:" + d.id;
  if (d.above) return "split:" + d.above + "/" + d.below;
  if (d.project) return (d.status ? "col:" : "lane:") + d.project + "/" + (d.status || "");
  return null;
};
function syncAttrs(from, to) {
  for (const { name, value } of [...from.attributes]) if (!to.hasAttribute(name) && !(name === "class" && KEEP.some((k) => from.classList.contains(k)))) from.removeAttribute(name);
  for (const { name, value } of [...to.attributes]) {
    if (name === "class") {
      const keep = KEEP.filter((k) => from.classList.contains(k) && !to.classList.contains(k));
      const v = [value, ...keep].filter(Boolean).join(" ");
      if (from.getAttribute("class") !== v) from.setAttribute("class", v);
    } else if (from.getAttribute(name) !== value) from.setAttribute(name, value);
  }
}
function syncValue(from, to) { // live form state: match what a fresh render would show
  if (from.tagName === "TEXTAREA") { if (from.value !== to.value) from.value = to.value; }
  else if (from.tagName === "INPUT") {
    if (from.type === "checkbox" || from.type === "radio") from.checked = to.checked;
    else if (from.value !== to.value) from.value = to.value;
  } else if (from.tagName === "OPTION") from.selected = to.selected;
}
function patch(from, to) {
  if (from.isEqualNode(to) && !/^(INPUT|TEXTAREA|SELECT)$/.test(from.tagName) && !from.querySelector("input,textarea,select")) return;
  syncAttrs(from, to);
  if (from.tagName === "TEXTAREA") { if (from.defaultValue !== to.defaultValue) from.defaultValue = to.defaultValue; }
  else patchChildren(from, to);
  syncValue(from, to);
}
function patchChildren(parent, next) {
  const old = [...parent.childNodes], byKey = new Map(), used = new Set();
  for (const n of old) { const k = keyOf(n); if (k) byKey.set(k, n); }
  let cur = 0; // cursor over old nodes for unkeyed matching
  const want = [...next.childNodes];
  want.forEach((n, i) => {
    const k = keyOf(n);
    let m = null;
    if (k) m = byKey.get(k) || null;
    else {
      while (cur < old.length && (used.has(old[cur]) || keyOf(old[cur]))) cur++;
      if (cur < old.length && old[cur].nodeName === n.nodeName) m = old[cur++];
    }
    if (m && !used.has(m)) {
      used.add(m);
      if (m.nodeType === 1) patch(m, n); else if (m.nodeValue !== n.nodeValue) m.nodeValue = n.nodeValue;
    } else m = n.cloneNode(true);
    if (parent.childNodes[i] !== m) parent.insertBefore(m, parent.childNodes[i] || null);
  });
  for (const n of old) if (!used.has(n) && n.parentNode === parent) n.remove();
}
const tpl = document.createElement("template");
export function morphInto(parent, html) {
  tpl.innerHTML = html;
  patchChildren(parent, tpl.content);
  tpl.innerHTML = "";
}
