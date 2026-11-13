package board

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func TestMistralBinaryAtCallTime(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	app.mistralBin = "/changed/vibe"
	h, err := app.harness("mistral")
	if err != nil {
		t.Fatal(err)
	}
	if cmd, _ := h.RefineCommand(context.Background(), harness.RefineSpec{}); cmd.Path != "/changed/vibe" {
		t.Errorf("path = %q", cmd.Path)
	}
	app.mistralBin = "/changed/again"
	h, _ = app.harness("mistral")
	if cmd, _ := h.RefineCommand(context.Background(), harness.RefineSpec{}); cmd.Path != "/changed/again" {
		t.Errorf("mistral path not read at call time: %q", cmd.Path)
	}
}

func TestMistralAvailability(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	app.mistralBin = filepath.Join(dir, "no-such-vibe")
	if st := app.harnessStatus("mistral"); st.Available || !strings.Contains(st.Error, "does not exist") {
		t.Errorf("missing bin: %+v", st)
	}
	if err := app.requireHarness("mistral"); err == nil || !strings.HasPrefix(err.Error(), "harness mistral is not available: ") {
		t.Errorf("requireHarness = %v", err)
	}
	app.mistralBin = testenv.FakeMistral()
	if st := app.harnessStatus("mistral"); !st.Available || st.Version != "2.4.2-fake" {
		t.Errorf("working fake: %+v", st)
	}
}

func TestMistralCardEndToEnd(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	st, _ := app.store.Settings()
	st.Harness, st.RefineModel, st.RefineEffort = "mistral", "devstral-2", ""
	app.store.SaveSettings(st)
	c := newCard(t, app, root, "WRITE hello")
	c.Harness, c.Effort, c.Model = "mistral", "", "devstral-2"
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
	if c.TurnsUsed < 1 || !strings.Contains(c.Summary, "fake work done") {
		t.Errorf("turns %d, summary %q", c.TurnsUsed, c.Summary)
	}
	if _, err := os.Stat(filepath.Join(root, "CHANGED.txt")); err != nil {
		t.Errorf("agent did not work in the root folder: %v", err)
	}
}
