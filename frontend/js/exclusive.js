// "Skip refinement" and "Automatic model" exclude each other: the refiner picks the model, so a card that
// skips refinement has nothing to pick it. Ticking one unticks the other (the server enforces the same rule).
// changed is "skip" or "auto", the box the user just ticked or unticked; returns the resulting pair.
export function exclusiveFlags(skip, auto, changed) {
  if (skip && auto) return changed === "skip" ? { skip, auto: false } : { skip: false, auto };
  return { skip, auto };
}
// Applies that to two checkboxes (ids, with #) after the user toggled one of them.
export function syncExclusive(skipEl, autoEl, changed) {
  if (!skipEl || !autoEl) return;
  const r = exclusiveFlags(skipEl.checked, autoEl.checked, changed);
  skipEl.checked = r.skip; autoEl.checked = r.auto;
}
