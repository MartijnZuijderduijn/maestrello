// Run with: node --test marquee.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { idsInRect, intersects, normRect } from "./web/js/marquee.js";

const r = (left, top, right, bottom) => ({ left, top, right, bottom });

test("normRect handles any drag direction", () => {
  assert.deepEqual(normRect(50, 60, 10, 20), r(10, 20, 50, 60));
});
test("overlap, containment and disjoint", () => {
  assert.ok(intersects(r(0, 0, 10, 10), r(5, 5, 15, 15)));
  assert.ok(intersects(r(0, 0, 100, 100), r(10, 10, 20, 20)));
  assert.ok(intersects(r(10, 10, 20, 20), r(0, 0, 100, 100)));
  assert.ok(!intersects(r(0, 0, 10, 10), r(20, 20, 30, 30)));
});
test("touching edges and hidden (empty) rects do not intersect", () => {
  assert.ok(!intersects(r(0, 0, 10, 10), r(10, 0, 20, 10)));
  assert.ok(!intersects(r(0, 0, 10, 10), r(0, 0, 0, 0)));
});
test("idsInRect picks cards across columns", () => {
  const items = [{ id: 1, rect: r(0, 0, 100, 50) }, { id: 2, rect: r(110, 0, 210, 50) }, { id: 3, rect: r(220, 0, 320, 50) }];
  assert.deepEqual(idsInRect(r(50, 10, 150, 20), items), [1, 2]);
  assert.deepEqual(idsInRect(r(400, 0, 500, 10), items), []);
});
