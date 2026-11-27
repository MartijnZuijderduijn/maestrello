// The local branch a base ref such as origin/main stands for.
export function baseBranch(ref) { return (ref || "").replace(/^origin\//, ""); }
// The actions offered for a repo at review. A branch that is pushed but has no PR also offers
// "Create PR" (runs only the PR step, no second push) and defaults to it.
export function repoActions(repo, pushDefault) {
  const pushedNoPR = !!repo.pushed && !repo.pr_url;
  const opts = [["merge", `Merge into ${baseBranch(repo.base_ref)}`], ["local", "keep local"], ["push", "push branch"], ["pr", "push + PR"]];
  if (pushedNoPR) opts.push(["create_pr", "Create PR"]);
  return { opts, selected: pushedNoPR ? "create_pr" : (pushDefault || "local") };
}

// What Merge does on a checkout with uncommitted changes (merge_dirty), as [value, label].
export const MERGE_DIRTY = [
  ["worktree", "merge without touching my changes"],
  ["stash", "stash, merge, restore"],
  ["queue", "wait until the checkout is clean"],
  ["refuse", "do not merge"],
];
// The merge-strategy choice of one repo at review: whether to offer it (only for Merge), and the
// warning when Merge will not happen because the checkout is dirty and the strategy refuses.
export function mergeHint(action, strategy, dirty) {
  if (action !== "merge") return { show: false, warn: "" };
  const warn = dirty && strategy === "refuse"
    ? "The checkout has uncommitted changes to tracked files, so Merge will not happen: the branch is kept and the card stays in Review. Commit or stash them first, or choose another strategy."
    : "";
  return { show: true, warn };
}
