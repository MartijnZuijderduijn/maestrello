package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

func TestClaudeSessionContextDirs(t *testing.T) {
	spec := testSpec() // root /root
	spec.Context = []ContextItem{
		{"/ref/docs", "dir"}, {"/ref/a.txt", "file"}, {"/ref/b.txt", "file"}, // parent /ref/ once
		{"/ref/docs", "dir"},    // duplicate
		{"/root/inner", "dir"},  // under the root: skipped
		{"/root/x.txt", "file"}, // parent is the root: skipped
	}
	args, _, _ := sessionArgs(t, spec)
	if got, want := addDirs(args), []string{"/root", "/ref/docs", "/ref"}; !slices.Equal(got, want) {
		t.Errorf("--add-dir = %v, want %v", got, want)
	}
	if d := addDirs(args); slices.Index(args, "--append-system-prompt") < slices.Index(args, d[len(d)-1]) {
		t.Errorf("--add-dir after --append-system-prompt: %v", args)
	}

	spec.Card.Kind = KindChat
	args, _, _ = sessionArgs(t, spec)
	if got := addDirs(args); len(got) != 0 {
		t.Errorf("chat got --add-dir %v", got)
	}
	for _, k := range []string{KindResearch, KindPlan} {
		spec.Card.Kind = k
		args, _, _ = sessionArgs(t, spec)
		if !slices.Contains(addDirs(args), "/ref/docs") {
			t.Errorf("%s: context dir missing: %v", k, args)
		}
	}
}

func TestClaudeRefineExtraDirs(t *testing.T) {
	h := claudeHarness{bin: "/bin/claude"}
	cmd, err := h.RefineCommand(t.Context(), RefineSpec{Model: "m", Tools: "Read", Schema: "{}", ExtraDirs: []string{"/a", "/b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := addDirs(cmd.Args); !slices.Equal(got, []string{"/a", "/b"}) {
		t.Errorf("--add-dir = %v", got)
	}
	cmd, _ = h.RefineCommand(t.Context(), RefineSpec{Model: "m", Schema: "{}"})
	if got := addDirs(cmd.Args); len(got) != 0 {
		t.Errorf("no ExtraDirs but --add-dir %v", got)
	}
}

func TestOpencodeContextPermissions(t *testing.T) {
	spec := ocSpec(KindChat)
	spec.Context = []ContextItem{{"/ref", "dir"}}
	_, _, _, cfg := ocSession(t, spec)
	if _, perm := ocAgent(t, cfg); perm["external_directory"] != "deny" {
		t.Errorf("chat external_directory = %v", perm["external_directory"])
	}
	spec = ocSpec(KindResearch)
	_, _, _, cfg = ocSession(t, spec)
	if _, perm := ocAgent(t, cfg); perm["external_directory"] != "allow" {
		t.Errorf("research external_directory = %v", perm["external_directory"])
	}

	h := newOpencodeHarness("/bin/opencode")
	for _, c := range []struct {
		extra []string
		want  string
	}{{nil, "deny"}, {[]string{"/ref"}, "allow"}} {
		cmd, err := h.RefineCommand(t.Context(), RefineSpec{Model: "ollama/x", Tools: "Read", Schema: "{}", ExtraDirs: c.extra})
		if err != nil {
			t.Fatal(err)
		}
		var env string
		for _, e := range cmd.Env {
			if v, ok := strings.CutPrefix(e, "OPENCODE_CONFIG_CONTENT="); ok {
				env = v
			}
		}
		if !strings.Contains(env, `"external_directory":"`+c.want+`"`) || !strings.Contains(env, `"edit":"deny"`) ||
			!strings.Contains(env, `"bash":"deny"`) || !strings.Contains(env, `"webfetch":"deny"`) {
			t.Errorf("extra %v: config %s", c.extra, env)
		}
	}
}

func TestContextInstructions(t *testing.T) {
	app := &App{}
	ctx := []ContextItem{{"/ref/docs", "dir"}, {"/ref/a.txt", "file"}}
	for _, k := range []string{KindCode, KindResearch, KindPlan} {
		got := app.sessionInstructions(&Card{Kind: k, RootFolder: "/r", Context: ctx}, "")
		for _, w := range []string{"/ref/docs (dir)", "/ref/a.txt (file)", "READ-ONLY", "outside the workspace", "never edit"} {
			if !strings.Contains(got, w) {
				t.Errorf("%s: instructions lack %q", k, w)
			}
		}
		if strings.Contains(app.sessionInstructions(&Card{Kind: k, RootFolder: "/r"}, ""), "Additional context") {
			t.Errorf("%s: context note without context", k)
		}
	}
	if got := app.sessionInstructions(&Card{Kind: KindChat, Context: ctx}, ""); got != chatInstructions {
		t.Error("chat instructions mention context")
	}
	if contextNote(&Card{}) != "" {
		t.Error("contextNote not empty without context")
	}
}

func TestRunSkipsMissingContext(t *testing.T) {
	app, dir, file := ctxFixture(t)
	c := ctxCard(t, app, []string{dir, file})
	c.Status = StatusReady
	c.RefinedPrompt = "do it"
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	os.Remove(file)
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, StatusReview)
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
	fake, _ := filepath.Abs("testdata/fake-claude.sh")
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
		bin, argsFile, stdinFile := recordingClaude(t, testDir(t))
		app.claudeBin = bin
		c := ctxCard(t, app, []string{ctxDir, file})
		c.DeepRefinement = deep
		if err := app.store.SaveCard(c); err != nil {
			t.Fatal(err)
		}
		if err := app.Refine(c.ID); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, app, c.ID, StatusReady)
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
		sub := filepath.Join(app.guard.root, "proj")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		refDir := filepath.Join(app.guard.root, "docs")
		bin, argsFile, _ := recordingClaude(t, testDir(t))
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
		waitStatus(t, app, c.ID, StatusReady)
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
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	ref := filepath.Join(dir, "ref.txt")
	if err := os.WriteFile(ref, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := kindCard(t, app, root, KindPlan, "PLAN a")
	c.Context = []ContextItem{{ref, "file"}}
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	if err := app.Approve(c.ID, nil); err != nil {
		t.Fatal(err)
	}
	all, _ := app.store.ListCards()
	if len(all) != 3 || len(all[1].Context) != 1 || all[1].Context[0] != c.Context[0] {
		t.Errorf("created cards' context: %v", all)
	}
}
