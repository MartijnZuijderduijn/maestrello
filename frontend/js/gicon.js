// Small git icon set for the Workspace page: 24px stroke icons in currentColor, decorative (aria-hidden):
// callers always put a text label or title next to them.
const ICONS = {
  repo: '<path d="M6 3h12a1 1 0 0 1 1 1v13H7a2 2 0 0 0-2 2V4a1 1 0 0 1 1-1z"/><path d="M5 19a2 2 0 0 0 2 2h12v-4"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  branch: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="8" r="2"/><path d="M6 7v10M18 10c0 4-6 3-12 7"/>',
  worktree: '<rect x="3" y="3" width="8" height="8" rx="1.5"/><rect x="13" y="13" width="8" height="8" rx="1.5"/><path d="M7 11v5a2 2 0 0 0 2 2h4"/>',
  commit: '<circle cx="12" cy="12" r="3.5"/><path d="M3 12h5.5M15.5 12H21"/>',
  merge: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="14" r="2"/><path d="M6 7v10M16 14c-6 0-10-1-10-5"/>',
  pr: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="19" r="2"/><path d="M6 7v10M18 17V9a2 2 0 0 0-2-2h-3M15 4l-3 3 3 3"/>',
  push: '<path d="M12 19V5M6 11l6-6 6 6"/>',
  local: '<rect x="3" y="5" width="18" height="12" rx="2"/><path d="M8 21h8M12 17v4"/>',
  dirty: '<rect x="3" y="3" width="18" height="18" rx="3"/><circle cx="12" cy="12" r="2.5" fill="currentColor"/>',
  lock: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  failed: '<circle cx="12" cy="12" r="9"/><path d="M9 9l6 6M15 9l-6 6"/>',
  chevron: '<path d="M9 6l6 6-6 6"/>',
};
export const gitIcon = (name) =>
  `<svg class="gi gi-${name}" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false" draggable="false">${ICONS[name] || ""}</svg>`;
