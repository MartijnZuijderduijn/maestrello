// Run with: node --test route.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { buildHash, parseHash } from "./frontend/js/route.js";

test("hash parse and build", () => {
  assert.deepEqual(parseHash(""), { page: "board", p: null });
  assert.deepEqual(parseHash("#p=1,3"), { page: "board", p: "1,3" });      // old bookmarks
  assert.deepEqual(parseHash("#p=all"), { page: "board", p: "all" });
  assert.deepEqual(parseHash("#workspace&p=1,3"), { page: "workspace", p: "1,3" });
  assert.deepEqual(parseHash("#workspace"), { page: "workspace", p: null });
  assert.deepEqual(parseHash("#board&p=2"), { page: "board", p: "2" });
  assert.deepEqual(parseHash("#junk!"), { page: "board", p: null });
  assert.equal(buildHash("board", "1,3"), "#p=1,3");
  assert.equal(buildHash("workspace", "1,3"), "#workspace&p=1,3");
  assert.equal(buildHash("workspace", null), "#workspace");
  assert.equal(buildHash("board", null), "#");
});
