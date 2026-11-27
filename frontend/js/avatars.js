// Animation phase per card, kept while its pose stays the same so re-renders produce identical markup.
const phases = new Map();
export function avatarHTML(c, live) {
  const h = Math.imul(c.id + 1, 2654435761) >>> 0, hue = h % 360, kind = (h >>> 9) % 3, eyeKind = (h >>> 12) % 3;
  const st = c.status === "failed" ? "bad" : c.status === "needs_input" ? "ask" : c.status === "done" ? "done" : c.status === "review" ? "ok"
    : live ? "live" : c.status === "todo" ? "sleep" : "idle";
  const period = { live: 700, ask: 900, sleep: 3000, idle: 3000 }[st] || 1, d0 = -(Date.now() % period), ph = phases.get(c.id), d = ph && ph.st === st ? ph.d : d0;
  phases.set(c.id, { st, d });
  const ex = eyeKind === 1 ? 5 : 6, er = eyeKind === 2 ? 2.2 : 1.6;
  const eye = (x) => st === "sleep" ? `<path class="ln" d="M${x - 2} 15h4"/>` : st === "bad" ? `<path class="ln" d="M${x - 1.8} 13.2l3.6 3.6m0-3.6l-3.6 3.6"/>`
    : st === "done" ? `<path class="ln" d="M${x - 2} 16q2-3.6 4 0"/>` : `<circle class="ink" cx="${x}" cy="15" r="${er}"/>`;
  const mouth = st === "live" ? `<ellipse class="ink" cx="14" cy="20.4" rx="2" ry="1.3"/>` : st === "ask" ? `<circle class="ln" cx="14" cy="20.4" r="1.3"/>`
    : st === "bad" ? `<path class="ln" d="M11 21.4q3-2.6 6 0"/>` : st === "done" || st === "ok" ? `<path class="ln" d="M10.5 19.5q3.5 3.4 7 0"/>` : `<path class="ln" d="M11.5 20.4h5"/>`;
  const top = kind === 0 ? `<path class="ln" d="M14 7.5V3.5"/><circle class="tip" cx="14" cy="3" r="1.9"/>`
    : kind === 1 ? `<path class="body" d="M6.5 9.5L5.5 3.5l5 3zM21.5 9.5l1-6-5 3z"/>` : `<circle class="body" cx="5" cy="6" r="2.6"/><circle class="body" cx="23" cy="6" r="2.6"/>`;
  const tentacles = `<path class="tent" d="M8 24q-2 2 0 3t0 2M12.5 24q-2 2 0 3t0 2M17 24q2 2 0 3t0 2M21.5 24q2 2 0 3t0 2"/>`;
  return `<svg class="av ${st}" style="--h:${hue};--d:${d}ms" viewBox="0 0 28 28" aria-hidden="true" draggable="false" focusable="false"><g class="fx">${top}${tentacles}<rect class="body" x="3.5" y="7.5" width="21" height="18" rx="6.5"/><g class="eyes">${eye(14 - ex)}${eye(14 + ex)}</g>${mouth}</g>${st === "ask" ? `<circle class="bub" cx="23.5" cy="5" r="4.6"/><path class="bubt" d="M22.9 2.6h1.2l-.3 3.3h-.6zM22.9 6.7h1.2v1.2h-1.2z"/>` : ""}</svg>`;
}

// Gravestone for the trash list: a gray stone (shape picked from the card id) with the agent's face engraved as an outline.
// Same hash as avatarHTML, so the engraved face matches the creature the card had on the board. Static, so re-renders are identical.
const STONES = [
  "M5 27V12a9 9 0 0 1 18 0v15z", // rounded top
  "M10 27V17H4.5V10.5H10V3.5h8v7h5.5V17H18v10z", // cross
  "M4.5 27V6.5h19V27z", // slab
  "M5 27V10l4.5-4.5h9L23 10v17z", // chamfered
];
export function gravestoneHTML(c) {
  const h = Math.imul(c.id + 1, 2654435761) >>> 0, v = (h >>> 5) % STONES.length, eyeKind = (h >>> 12) % 3, top = (h >>> 9) % 3;
  const ex = eyeKind === 1 ? 2.6 : 3;
  // Face box centered on the stone; on the cross it sits on the crossbar.
  const cy = v === 1 ? 13.5 : v === 0 ? 15 : 14.5, bw = v === 1 ? 8 : 10, bh = v === 1 ? 6 : 8.5, y0 = cy - bh / 2;
  const ear = top === 0 ? `<path d="M14 ${y0}v-2"/><circle cx="14" cy="${y0 - 2.6}" r=".9"/>`
    : top === 1 ? `<path d="M${14 - bw / 2 + .5} ${y0}l-.6-2.4 2.2 1.2M${14 + bw / 2 - .5} ${y0}l.6-2.4-2.2 1.2"/>`
    : `<circle cx="${14 - bw / 2}" cy="${y0}" r="1.3"/><circle cx="${14 + bw / 2}" cy="${y0}" r="1.3"/>`;
  const face = `<g class="engr"><rect x="${14 - bw / 2}" y="${y0}" width="${bw}" height="${bh}" rx="2.6"/>${v === 1 ? "" : ear}`
    + `<path d="M${14 - ex - 1} ${cy - 1}l2 2m0-2l-2 2M${14 + ex - 1} ${cy - 1}l2 2m0-2l-2 2"/>`
    + `<path d="M12.2 ${cy + bh / 2 - 1.6}h3.6"/>`
    + `<path d="M${14 - bw / 2 + 1.5} ${y0 + bh}q-.8 1.4 0 2M14 ${y0 + bh}q-.8 1.4 0 2M${14 + bw / 2 - 1.5} ${y0 + bh}q.8 1.4 0 2"/></g>`;
  const deco = [`<path class="moss" d="M5 21q2-2 3.5 0t3.5 0v6H5z"/>`, `<path class="crack" d="M18 9l-2 3 2 2"/>`, `<path class="crack" d="M6.5 10l3 2-1 3"/>`, `<path class="moss" d="M23 20q-2-2-3.5 0t-3.5 0v7h7z"/>`][v];
  return `<svg class="av grave" viewBox="0 0 28 28" aria-hidden="true" draggable="false" focusable="false"><path class="stone" d="${STONES[v]}"/>${deco}${face}<path class="grass" d="M2 27.3h24"/></svg>`;
}
