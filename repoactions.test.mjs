// Run with: node --test repoactions.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { repoActions } from "./frontend/js/repoactions.js";

const values = (r, d) => repoActions(r, d).opts.map(([v]) => v);

test("a fresh repo offers the four actions and the push default", () => {
  const r = { base_ref: "origin/main" };
  assert.deepEqual(values(r), ["merge", "local", "push", "pr"]);
  assert.equal(repoActions(r).selected, "local");
  assert.equal(repoActions(r, "merge").selected, "merge");
  assert.equal(repoActions(r).opts[0][1], "Merge into main");
});

test("a pushed branch without a PR offers Create PR and defaults to it", () => {
  const r = { base_ref: "origin/main", pushed: true };
  assert.deepEqual(values(r), ["merge", "local", "push", "pr", "create_pr"]);
  assert.equal(repoActions(r, "merge").selected, "create_pr");
});

test("a pushed branch that has its PR offers no Create PR", () => {
  assert.ok(!values({ pushed: true, pr_url: "https://x/pr/1" }).includes("create_pr"));
});
