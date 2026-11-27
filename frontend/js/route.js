// The URL hash holds the page and the project selection: #workspace&p=1,3. No page means the board;
// the old #p=1,3 bookmarks are the board with a selection. Kept apart so pages.js and projects.js share it.
export const DEFAULT_PAGE = "board";
export function parseHash(hash = location.hash) {
  const m = /^#(?:([a-z][\w-]*)(?:&|$))?(?:p=([\w,]*))?$/.exec(hash);
  return { page: m?.[1] || DEFAULT_PAGE, p: m?.[2] ?? null };
}
// `p` null leaves the selection out of the hash.
export function buildHash(page, p) {
  return "#" + [page && page !== DEFAULT_PAGE ? page : "", p != null ? "p=" + p : ""].filter(Boolean).join("&");
}
