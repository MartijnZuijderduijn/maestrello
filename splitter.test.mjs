// Run with: node --test splitter.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { MIN_LANE, collapsedLast, resizePair } from "./web/js/splitter.js";

test("dragging down grows the upper pane and shrinks the lower one", () => {
  assert.deepEqual(resizePair(300, 300, 50), [350, 250]);
  assert.deepEqual(resizePair(300, 300, -80), [220, 380]);
});
test("neither pane goes below the minimum", () => {
  assert.deepEqual(resizePair(300, 300, 1000), [600 - MIN_LANE, MIN_LANE]);
  assert.deepEqual(resizePair(300, 300, -1000), [MIN_LANE, 600 - MIN_LANE]);
  assert.deepEqual(resizePair(300, 300, 100, 50), [400, 200]);
});
test("the total is preserved and tiny panes stay put", () => {
  const [a, b] = resizePair(301, 222, 37);
  assert.equal(a + b, 523);
  assert.deepEqual(resizePair(100, 100, 30), [100, 100]);
});
test("collapsed lanes sink to the bottom, keeping relative order", () => {
  const ids = (l, off) => collapsedLast(l.map((id) => ({ id })), (p) => off.includes(p.id)).map((p) => p.id);
  assert.deepEqual(ids([1, 2, 3, 4], [1, 3]), [2, 4, 1, 3]);
  assert.deepEqual(ids([1, 2, 3], []), [1, 2, 3]);
  assert.deepEqual(ids([1, 2, 3], [1, 2, 3]), [1, 2, 3]);
});
