// Run with: node --test pills.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { KIND_TONE, STATE_TONE, toneOf, kindPill, statePill, FALLBACK_TONE } from "./frontend/js/pills.js";
import { COLUMNS } from "./frontend/js/state.js";
import { KINDS } from "./frontend/js/kinds.js";

test("every state and kind has its own color", () => {
  for (const [s] of COLUMNS) assert.ok(STATE_TONE[s], s);
  for (const [k] of KINDS) assert.ok(KIND_TONE[k], k);
  assert.equal(new Set(Object.values(STATE_TONE)).size, COLUMNS.length);
  assert.equal(new Set(Object.values(KIND_TONE)).size, KINDS.length);
});
test("done is green, failed is red, unknown falls back to neutral", () => {
  assert.equal(STATE_TONE.done, "green");
  assert.equal(STATE_TONE.failed, "red");
  assert.equal(toneOf(STATE_TONE, "weird"), FALLBACK_TONE);
  assert.equal(toneOf(KIND_TONE, "constructor"), FALLBACK_TONE);
});
test("pills keep the text label and escape it", () => {
  assert.match(statePill("done", "Done"), /pill-green[^>]*>Done</);
  assert.match(kindPill("<b>"), /pill-gray[^>]*>&lt;b&gt;</);
});
