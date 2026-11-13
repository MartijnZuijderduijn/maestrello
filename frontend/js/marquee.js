// Pure geometry for drag-box selection; rects are {left, top, right, bottom} in the same coordinate space.
export function normRect(x1, y1, x2, y2) {
  return { left: Math.min(x1, x2), top: Math.min(y1, y2), right: Math.max(x1, x2), bottom: Math.max(y1, y2) };
}
// Rects that merely touch do not intersect; an empty (hidden) rect never does.
export function intersects(a, b) {
  if (b.right <= b.left && b.bottom <= b.top) return false;
  return a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;
}
// items: [{id, rect}]. Returns the ids whose rect meets the box.
export function idsInRect(box, items) {
  return items.filter((it) => intersects(box, it.rect)).map((it) => it.id);
}
