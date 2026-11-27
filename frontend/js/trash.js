import { api } from "./api.js";
import { morphInto } from "./morph.js";
import { $, esc } from "./util.js";

// ---------- trash page ----------
// Archived cards: hidden from the board, kept in the database. Restore puts one back in Done; Delete removes it for good.
let list = [], error = "", loaded = false;
const shown = () => { const el = $("#page-trash"); return el && !el.hidden ? el : null; };

// Page renderer (pages.js): show what we have and refresh it.
export function showTrash() { paint(); fetchTrash(); }
export const trashTouched = () => { if (shown()) fetchTrash(); };
async function fetchTrash() {
  try { list = await api("GET", "/api/cards?archived=1"); error = ""; loaded = true; }
  catch (e) { error = e.message; }
  paint();
}
function paint() {
  const el = shown();
  if (!el) return;
  const rows = list.map((c) => `<li class="trash-row"><span class="trash-title">#${c.id} ${esc(c.title)}</span>
    <button onclick="restoreCard(${c.id})">Restore</button><button class="danger" onclick="deleteTrashCard(${c.id})">Delete</button></li>`).join("");
  morphInto(el, `<h2>Trash</h2><p class="trash-note">Archived cards are hidden from the board. Restore one to bring it back in Done.</p>`
    + (error ? `<p class="trash-note">${esc(error)}</p>` : loaded && !list.length ? `<p class="trash-note">No archived cards.</p>` : `<ul class="trash-list">${rows}</ul>`));
}
export async function restoreCard(id) {
  try { await api("POST", `/api/cards/${id}/unarchive`); } catch (e) { alert(e.message); }
  fetchTrash();
}
export async function deleteTrashCard(id) {
  if (!confirm("Delete this card for good?")) return;
  try { await api("DELETE", `/api/cards/${id}`); } catch (e) { alert(e.message); }
  fetchTrash();
}
