// Pure geometry for the draggable dividers between stacked project lanes.
export const MIN_LANE = 120; // px, smallest height a lane's columns may be dragged to

// Move the divider between two adjacent panes by delta px: a grows, b shrinks (or the reverse),
// their sum stays the same and neither goes below min. Returns [a, b].
export function resizePair(a, b, delta, min = MIN_LANE) {
  const total = a + b;
  if (total <= 2 * min) return [a, b]; // no room to move
  const na = Math.max(min, Math.min(total - min, a + delta));
  return [na, total - na];
}

// Lane order with the collapsed ones moved below the expanded ones; each group keeps its order.
export function collapsedLast(list, isCollapsed) {
  return [...list.filter((p) => !isCollapsed(p)), ...list.filter(isCollapsed)];
}
