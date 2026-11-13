import test from "node:test";
import assert from "node:assert/strict";
import { avatarHTML } from "./frontend/js/avatars.js";

test("avatars have tentacles in every status", () => {
  for (const status of ["todo", "running", "review", "done", "failed", "needs_input"]) {
    const html = avatarHTML({ id: 3, status }, false);
    assert.match(html, /class="tent"/, status);
  }
});
