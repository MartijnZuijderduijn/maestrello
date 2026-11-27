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
