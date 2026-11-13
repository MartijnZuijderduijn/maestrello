// Run with: node --test exclusive.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { exclusiveFlags, syncExclusive } from "./frontend/js/exclusive.js";

test("ticking skip clears automatic model", () => {
  assert.deepEqual(exclusiveFlags(true, true, "skip"), { skip: true, auto: false });
});
test("ticking automatic model clears skip", () => {
  assert.deepEqual(exclusiveFlags(true, true, "auto"), { skip: false, auto: true });
});
test("valid combinations are left alone", () => {
  for (const [s, a] of [[false, false], [true, false], [false, true]]) {
    assert.deepEqual(exclusiveFlags(s, a, "skip"), { skip: s, auto: a });
    assert.deepEqual(exclusiveFlags(s, a, "auto"), { skip: s, auto: a });
  }
});
test("syncExclusive updates the checkboxes", () => {
  const skip = { checked: true }, auto = { checked: true };
  syncExclusive(skip, auto, "auto");
  assert.deepEqual([skip.checked, auto.checked], [false, true]);
  syncExclusive(null, auto, "skip"); // a form without the skip box is fine
});
