import { $, esc } from "./util.js";

// Small markdown renderer for refined prompts. Every text fragment is escaped first, so raw HTML shows as text.
const inline = (s) => {
  const codes = [];
  s = esc(s).replace(/`([^`]+)`/g, (_, c) => `\u0000${codes.push(`<code>${c}</code>`) - 1}\u0000`);
  s = s.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')
    .replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[^*\w])\*([^*\s](?:[^*]*[^*\s])?)\*(?!\w)/g, "$1<em>$2</em>");
  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => codes[i]);
};
const ITEM = /^(\s*)([-*]|\d+\.)\s+(.*)$/;
// Renders consecutive list lines starting at lines[i]; returns [html, nextIndex]. Deeper indentation nests.
function list(lines, i) {
  const first = lines[i].match(ITEM), base = first[1].length, tag = /\d/.test(first[2]) ? "ol" : "ul";
  let out = `<${tag}>`;
  while (i < lines.length) {
    const m = lines[i].match(ITEM);
    if (!m || m[1].length < base) break;
    out += `<li>${inline(m[3])}`; i++;
    while (i < lines.length && ITEM.test(lines[i]) && lines[i].match(ITEM)[1].length > base) {
      const [html, next] = list(lines, i);
      out += html; i = next;
    }
    out += "</li>";
  }
  return [`${out}</${tag}>`, i];
}
export function md(text) {
  const lines = String(text ?? "").replace(/\r\n?/g, "\n").split("\n"), out = [];
  let i = 0, para = [];
  const flush = () => { if (para.length) out.push(`<p>${para.map(inline).join("<br>")}</p>`); para = []; };
  while (i < lines.length) {
    const l = lines[i];
    let m;
    if ((m = l.match(/^\s*(```+|~~~+)/))) {
      flush();
      const code = []; i++;
      while (i < lines.length && !lines[i].trim().startsWith(m[1])) code.push(lines[i++]);
      i++;
      out.push(`<pre><code>${esc(code.join("\n"))}</code></pre>`);
    } else if ((m = l.match(/^(#{1,6})\s+(.*?)\s*#*\s*$/))) {
      flush(); out.push(`<h${m[1].length}>${inline(m[2])}</h${m[1].length}>`); i++;
    } else if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(l)) {
      flush(); out.push("<hr>"); i++;
    } else if (l.startsWith(">")) {
      flush();
      const q = [];
      while (i < lines.length && lines[i].startsWith(">")) q.push(lines[i++].replace(/^>\s?/, ""));
      out.push(`<blockquote>${md(q.join("\n"))}</blockquote>`);
    } else if (ITEM.test(l)) {
      flush();
      const [html, next] = list(lines, i);
      out.push(html); i = next;
    } else if (!l.trim()) { flush(); i++; } else { para.push(l.trim()); i++; }
  }
  flush();
  return out.join("");
}
// Keep the preview next to the editor in sync while typing.
document.addEventListener("input", (e) => {
  if (e.target.id === "f-prompt") $("#prompt-md").innerHTML = md(e.target.value);
});
