package board

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func addDirs(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--add-dir" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestContextInstructions(t *testing.T) {
	app := &App{}
	ctx := []model.ContextItem{{Path: "/ref/docs", Type: "dir"}, {Path: "/ref/a.txt", Type: "file"}}
	for _, k := range []string{model.KindCode, model.KindResearch, model.KindPlan} {
		got := app.sessionInstructions(&model.Card{Kind: k, RootFolder: "/r", Context: ctx}, "")
		for _, w := range []string{"/ref/docs (dir)", "/ref/a.txt (file)", "READ-ONLY", "outside the workspace", "never edit"} {
			if !strings.Contains(got, w) {
				t.Errorf("%s: instructions lack %q", k, w)
			}
		}
		if strings.Contains(app.sessionInstructions(&model.Card{Kind: k, RootFolder: "/r"}, ""), "Additional context") {
			t.Errorf("%s: context note without context", k)
		}
	}
	if got := app.sessionInstructions(&model.Card{Kind: model.KindChat, Context: ctx}, ""); got != chatInstructions {
		t.Error("chat instructions mention context")
	}
	if contextNote(&model.Card{}) != "" {
		t.Error("contextNote not empty without context")
	}
}

func TestRunSkipsMissingContext(t *testing.T) {
	app, dir, file := ctxFixture(t)
	c := ctxCard(t, app, []string{dir, file})
	c.Status = model.StatusReady
	c.RefinedPrompt = "do it"
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	os.Remove(file)
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusReview)
	if !hasEvent(t, app, c.ID, "context path "+file+" not found; skipped") {
		t.Error("no note about the missing context path")
	}
	if hasEvent(t, app, c.ID, "context path "+dir) {
		t.Error("existing context dir was skipped")
	}
}

// recordingClaude wraps the fake claude: it records the arguments and stdin.
func recordingClaude(t *testing.T, dir string) (bin, argsFile, stdinFile string) {
	t.Helper()
	fake := testenv.FakeClaude()
	bin, argsFile, stdinFile = filepath.Join(dir, "rec.sh"), filepath.Join(dir, "rec-args.txt"), filepath.Join(dir, "rec-stdin.txt")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\ntee '" + stdinFile + "' | '" + fake + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRefineContext(t *testing.T) {
	for _, deep := range []bool{false, true} {
		app, ctxDir, file := ctxFixture(t)
		bin, argsFile, stdinFile := recordingClaude(t, testenv.Dir(t))
		app.claudeBin = bin
		c := ctxCard(t, app, []string{ctxDir, file})
		c.DeepRefinement = deep
		if err := app.store.SaveCard(c); err != nil {
			t.Fatal(err)
		}
		if err := app.Refine(c.ID); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, app, c.ID, model.StatusReady)
		raw, _ := os.ReadFile(argsFile)
		args := strings.Split(strings.TrimSpace(string(raw)), "\n")
		in, _ := os.ReadFile(stdinFile)
		for _, w := range []string{"Additional context (read-only reference):", "- " + ctxDir + " (dir)", "- " + file + " (file)"} {
			if !strings.Contains(string(in), w) {
				t.Errorf("deep=%v: prompt lacks %q", deep, w)
			}
		}
		// Both items live under the root, so even deep refinement needs no extra dirs here.
		if got := addDirs(args); len(got) != 0 {
			t.Errorf("deep=%v: --add-dir %v for context under the root", deep, got)
		}
	}
}

func TestRefineContextOutsideRoot(t *testing.T) {
	for _, deep := range []bool{false, true} {
		app, _, _ := ctxFixture(t)
		sub := filepath.Join(app.guard.Root(), "proj")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		refDir := filepath.Join(app.guard.Root(), "docs")
		bin, argsFile, _ := recordingClaude(t, testenv.Dir(t))
		app.claudeBin = bin
		c, err := app.CreateCard(cardInput{Title: ptr("c"), RootFolder: &sub, Context: &[]string{refDir}})
		if err != nil {
			t.Fatal(err)
		}
		c.DeepRefinement = deep
		if err := app.store.SaveCard(c); err != nil {
			t.Fatal(err)
		}
		if err := app.Refine(c.ID); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, app, c.ID, model.StatusReady)
		raw, _ := os.ReadFile(argsFile)
		got := addDirs(strings.Split(strings.TrimSpace(string(raw)), "\n"))
		if deep && !slices.Equal(got, []string{refDir}) {
			t.Errorf("deep: --add-dir = %v, want [%s]", got, refDir)
		}
		if !deep && len(got) != 0 {
			t.Errorf("non-deep: --add-dir = %v", got)
		}
	}
}

func TestPlanApprovalInheritsContext(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	ref := filepath.Join(dir, "ref.txt")
	if err := os.WriteFile(ref, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := kindCard(t, app, root, model.KindPlan, "PLAN a")
	c.Context = []model.ContextItem{{Path: ref, Type: "file"}}
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	if err := app.Approve(c.ID, nil); err != nil {
		t.Fatal(err)
	}
	all, _ := app.store.ListCards()
	if len(all) != 3 || len(all[1].Context) != 1 || all[1].Context[0] != c.Context[0] {
		t.Errorf("created cards' context: %v", all)
	}
}
