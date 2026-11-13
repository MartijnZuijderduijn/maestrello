// Kept apart from harness.js (which pulls in most of the UI) so the account indicator can use it too.
// Small inline icons per harness adapter (presentational); unknown names get a terminal glyph.
const HARNESS_ICONS = {
  claude: '<path d="M12 2v20M2 12h20M5 5l14 14M19 5L5 19"/>',
  mistral: '<path d="M4 5h4v4H4zM16 5h4v4h-4zM4 10h16v4H4zM4 15h4v4H4zM16 15h4v4h-4z"/>',
  omp: '<circle cx="12" cy="12" r="8.5"/><path d="M8 12h8M12 8v8"/>',
  opencode: '<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="M8 10l3 2-3 2M13 15h4"/>',
};
const HARNESS_FALLBACK_ICON = '<path d="M4 6l6 6-6 6M12 19h8"/>';
export const harnessIcon = (name) => {
  const known = Object.prototype.hasOwnProperty.call(HARNESS_ICONS, name);
  return `<svg class="hicon${known ? ' hicon-' + name : ''}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false" draggable="false">${known ? HARNESS_ICONS[name] : HARNESS_FALLBACK_ICON}</svg>`;
};
