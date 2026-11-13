package board

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func TestEachHarnessOwnBinaryAtCallTime(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	app.claudeBin, app.opencodeBin = "/changed/claude", "/changed/opencode"
	for name, want := range map[string]string{"claude": "/changed/claude", "opencode": "/changed/opencode", "": "/changed/claude"} {
		h, err := app.harness(name)
		if err != nil {
			t.Fatal(err)
		}
		cmd, _ := h.RefineCommand(context.Background(), harness.RefineSpec{})
		if cmd.Path != want {
			t.Errorf("%q: path = %q, want %q", name, cmd.Path, want)
		}
	}
	app.opencodeBin = "/changed/again"
	h, _ := app.harness("opencode")
	if cmd, _ := h.RefineCommand(context.Background(), harness.RefineSpec{}); cmd.Path != "/changed/again" {
		t.Errorf("opencode path not read at call time: %q", cmd.Path)
	}
}

func TestHarnessAvailability(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	app.opencodeBin = filepath.Join(dir, "no-such-opencode")
	st := app.harnessStatus("opencode")
	if st.Available || !strings.Contains(st.Error, "does not exist") {
		t.Errorf("missing bin: %+v", st)
	}
	if err := app.requireHarness("opencode"); err == nil || !strings.HasPrefix(err.Error(), "harness opencode is not available: ") {
		t.Errorf("requireHarness = %v", err)
	}
	app.opencodeBin = "definitely-not-on-path-opencode"
	if st := app.harnessStatus("opencode"); st.Available || !strings.Contains(st.Error, "not found in PATH") {
		t.Errorf("PATH miss: %+v", st) // the cache notices the changed binary
	}
	bad := filepath.Join(dir, "bad-opencode.sh")
	os.WriteFile(bad, []byte("#!/usr/bin/env bash\necho nope >&2\nexit 2\n"), 0o755)
	app.opencodeBin = bad
	if st := app.harnessStatus("opencode"); st.Available || !strings.Contains(st.Error, "nope") {
		t.Errorf("failing --version: %+v", st)
	}

	fake := testenv.FakeOpencode()
	app.opencodeBin = fake
	st = app.harnessStatus("opencode")
	if !st.Available || st.Version != "1.2.10-fake" || st.Error != "" || !slices.Contains(st.Models, "ollama/fake-model:1b") {
		t.Errorf("working fake: %+v", st)
	}
	if err := app.requireHarness("opencode"); err != nil {
		t.Errorf("requireHarness = %v", err)
	}
	// The result is cached: breaking the script does not show until a refresh.
	os.WriteFile(bad, []byte("#!/usr/bin/env bash\nexit 1\n"), 0o755)
	if !app.harnessStatus("opencode").Available {
		t.Error("status not cached")
	}
}

func TestHarnessesEndpointAvailability(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	var got []harnessInfo
	sendJSON(t, "GET", app.baseURL+"/api/harnesses", nil, &got)
	if len(got) != 4 {
		t.Fatalf("harnesses = %+v", got)
	}
	if !got[0].Available || got[0].Version == "" || !slices.Equal(got[0].Models, knownModels) {
		t.Errorf("claude = %+v", got[0])
	}
	if !got[3].Available || got[3].Version != "1.2.10-fake" || !slices.Contains(got[3].Models, "opencode/fake-free") {
		t.Errorf("opencode = %+v", got[3])
	}
	// Point opencode at nothing: cached until ?refresh=1 (the changed path triggers a re-check anyway).
	app.opencodeBin = filepath.Join(dir, "gone")
	sendJSON(t, "GET", app.baseURL+"/api/harnesses?refresh=1", nil, &got)
	if got[3].Available || got[3].Error == "" || len(got[3].Models) != 0 || got[3].Models == nil {
		t.Errorf("unavailable opencode = %+v", got[3])
	}
	if !got[0].Available {
		t.Errorf("claude lost: %+v", got[0])
	}
}

func TestUnavailableHarnessFailsCard(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	marker := filepath.Join(dir, "started")
	bin := filepath.Join(dir, "touchy-opencode.sh")
	// --version fails, and any other call would leave a marker: it must never run.
	os.WriteFile(bin, []byte("#!/usr/bin/env bash\n[ \"${1:-}\" = --version ] && exit 1\ntouch "+marker+"\n"), 0o755)
	app.opencodeBin = bin

	// A registered but unavailable harness is still accepted for cards and settings.
	var c model.Card
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{
		"title": "t", "root_folder": dir, "harness": "opencode", "model": "ollama/x", "effort": "", "permission_mode": "auto"}, &c); code != 201 {
		t.Fatalf("create opencode card = %d", code)
	}
	card, _ := app.store.GetCard(c.ID)
	card.Status, card.RefinedPrompt = model.StatusReady, "do it"
	app.store.SaveCard(card)
	app.Run(c.ID)
	card = waitStatus(t, app, c.ID, model.StatusFailed)
	if !strings.HasPrefix(card.FailReason, "harness opencode is not available: ") {
		t.Errorf("fail reason = %q", card.FailReason)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a process was started")
	}

	// Refinement uses the settings' harness.
	st, _ := app.store.Settings()
	st.Harness = "opencode"
	app.store.SaveSettings(st)
	c2 := newCard(t, app, dir, "refine me")
	app.Refine(c2.ID)
	card = waitStatus(t, app, c2.ID, model.StatusFailed)
	if !strings.Contains(card.FailReason, "harness opencode is not available: ") {
		t.Errorf("refine fail reason = %q", card.FailReason)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a refinement process was started")
	}
}

func TestOpencodeCardEndToEnd(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	// Refine through opencode too: settings pick the harness.
	st, _ := app.store.Settings()
	st.Harness, st.RefineModel, st.RefineEffort = "opencode", "ollama/fake-model:1b", ""
	app.store.SaveSettings(st)
	c := newCard(t, app, root, "WRITE hello")
	c.Harness, c.Effort, c.Model = "opencode", "", "ollama/fake-model:1b"
	app.store.SaveCard(c)
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, model.StatusReady)
	if !strings.Contains(c.RefinedPrompt, "hello") {
		t.Fatalf("refined prompt = %q", c.RefinedPrompt)
	}
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)
	if !strings.HasPrefix(c.SessionID, "ses_fake") {
		t.Errorf("session id %q was not taken from the stream", c.SessionID)
	}
	if c.TurnsUsed < 1 {
		t.Errorf("turns used = %d", c.TurnsUsed)
	}
	if !strings.Contains(c.Summary, "fake work done") {
		t.Errorf("summary = %q", c.Summary)
	}
	if _, err := os.Stat(filepath.Join(root, "CHANGED.txt")); err != nil {
		t.Errorf("agent did not work in the root folder: %v", err)
	}
	// Send back: resumes the same opencode session.
	id := c.SessionID
	if err := app.SendBack(c.ID, "more please"); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, model.StatusReview)
	if c.SessionID != id {
		t.Errorf("session id changed on resume: %q -> %q", id, c.SessionID)
	}
}
