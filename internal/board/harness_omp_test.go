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

func TestOmpBinaryAtCallTime(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	app.ompBin = "/changed/omp"
	h, err := app.harness("omp")
	if err != nil {
		t.Fatal(err)
	}
	if cmd, _ := h.RefineCommand(context.Background(), harness.RefineSpec{}); cmd.Path != "/changed/omp" {
		t.Errorf("path = %q", cmd.Path)
	}
	app.ompBin = "/changed/again"
	h, _ = app.harness("omp")
	if cmd, _ := h.RefineCommand(context.Background(), harness.RefineSpec{}); cmd.Path != "/changed/again" {
		t.Errorf("omp path not read at call time: %q", cmd.Path)
	}
}

func TestOmpAvailability(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	app.ompBin = filepath.Join(dir, "no-such-omp")
	if st := app.harnessStatus("omp"); st.Available || !strings.Contains(st.Error, "does not exist") {
		t.Errorf("missing bin: %+v", st)
	}
	if err := app.requireHarness("omp"); err == nil || !strings.HasPrefix(err.Error(), "harness omp is not available: ") {
		t.Errorf("requireHarness = %v", err)
	}
	app.ompBin = testenv.FakeOmp()
	if st := app.harnessStatus("omp"); !st.Available || st.Version != "omp/18.20.0-fake" {
		t.Errorf("working fake: %+v", st)
	}
}

func TestOmpCardEndToEnd(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	st, _ := app.store.Settings()
	st.Harness, st.RefineModel, st.RefineEffort = "omp", "opus", ""
	app.store.SaveSettings(st)
	c := newCard(t, app, root, "WRITE hello")
	c.Harness, c.Effort, c.Model = "omp", "", "opus"
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

func TestOmpModelsAndEditAgent(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	app.ompBin = testenv.FakeOmp()
	st := app.harnessStatus("omp")
	if !st.Available || !slices.Contains(st.Models, "ollama/devstral:24b") {
		t.Fatalf("status = %+v", st)
	}
	c := newCard(t, app, dir, "work")
	c.Harness, c.Status = "omp", model.StatusTodo
	app.store.SaveCard(c)
	if _, err := app.EditAgent(c.ID, AgentParams{Model: strp("ollama/devstral:24b")}); err != nil {
		t.Fatalf("listed model refused: %v", err)
	}
	if saved, _ := app.store.GetCard(c.ID); saved.Model != "ollama/devstral:24b" {
		t.Errorf("saved model = %q", saved.Model)
	}
	if _, err := app.EditAgent(c.ID, AgentParams{Model: strp("nope/none")}); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("unlisted model: %v", err)
	}
	if saved, _ := app.store.GetCard(c.ID); saved.Model != "ollama/devstral:24b" {
		t.Errorf("refused edit changed the model: %q", saved.Model)
	}
}

func TestOmpNoModelList(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	app.ompBin = testenv.FakeOmp()
	t.Setenv("FAKE_OMP_NO_MODELS", "1")
	if st := app.harnessStatus("omp"); !st.Available || len(st.Models) != 0 {
		t.Fatalf("status = %+v", st)
	}
	if err := app.validateModel("omp", "anything/goes"); err != nil {
		t.Errorf("free choice refused: %v", err)
	}
}
