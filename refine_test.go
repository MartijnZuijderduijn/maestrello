package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRefineRunsWithoutTools: refinement never reads code. The claude command gets an
// empty tool list and a turn cap, runs outside the root folder, the prompt still lists the
// repos, the fake CLI's structured result is parsed, and a planned repo that is not
// on the list is dropped, restrict_refinement or not.
func TestRefineRunsWithoutTools(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	fake, _ := filepath.Abs("testdata/fake-claude.sh")
	argsFile := filepath.Join(dir, "refine-args.txt")
	cwdFile := filepath.Join(dir, "refine-cwd.txt")
	wrap := filepath.Join(dir, "recording-claude.sh")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\npwd > '" + cwdFile + "'\nexec '" + fake + "' \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	app.claudeBin = wrap

	c := newCard(t, app, dir, "do the thing")
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, app, c.ID, StatusReady)
	if !strings.Contains(got.RefinedPrompt, "do the thing") {
		t.Errorf("structured result not parsed: %q", got.RefinedPrompt)
	}
	raw, _ := os.ReadFile(argsFile)
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	i := slices.Index(args, "--tools")
	if i < 0 || i+1 >= len(args) || args[i+1] != "" {
		t.Errorf("refinement tools not empty: %q", args)
	}
	if i := slices.Index(args, "--max-turns"); i < 0 || args[i+1] != "4" {
		t.Errorf("refinement turns not capped: %q", args)
	}
	if cwd, _ := os.ReadFile(cwdFile); strings.TrimSpace(string(cwd)) == dir {
		t.Errorf("refinement ran in the root folder %s", cwd)
	}
}

// TestRefineValidatesRepos: planned repos still come from the known list.
func TestRefineValidatesRepos(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	script := filepath.Join(dir, "repos-claude.sh")
	body := `#!/usr/bin/env bash
cat > /dev/null
echo '{"type":"result","is_error":false,"result":"","structured_output":{"title":"T","refined_prompt":"P","repos":["nope","."],"notes":"n"},"total_cost_usd":0}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	app.claudeBin = script
	c := newCard(t, app, dir, "x")
	c.RestrictRefinement = true // the old option changes nothing now
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, app, c.ID, StatusReady)
	if !strings.Contains(got.RefineNotes, `Dropped unknown repo "nope"`) {
		t.Errorf("unknown repo kept: notes %q", got.RefineNotes)
	}
	if repos, _ := app.store.Repos(c.ID); len(repos) != 1 || repos[0].RepoPath != "." {
		t.Errorf("planned repos = %+v", repos)
	}
}

// TestRefineDeepExploresCode: with deep_refinement the refiner gets read-only tools, runs in
// the root folder, a higher turn cap, and a prompt that allows reading code.
func TestRefineDeepExploresCode(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	fake, _ := filepath.Abs("testdata/fake-claude.sh")
	argsFile := filepath.Join(dir, "refine-args.txt")
	cwdFile := filepath.Join(dir, "refine-cwd.txt")
	wrap := filepath.Join(dir, "recording-claude.sh")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\npwd > '" + cwdFile + "'\nexec '" + fake + "' \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	app.claudeBin = wrap

	c := newCard(t, app, dir, "do the thing")
	c.DeepRefinement = true
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, StatusReady)
	raw, _ := os.ReadFile(argsFile)
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	i := slices.Index(args, "--tools")
	if i < 0 || i+1 >= len(args) || args[i+1] != refineDeepTools {
		t.Errorf("deep refinement tools = %q", args)
	}
	if i := slices.Index(args, "--max-turns"); i < 0 || args[i+1] != "20" {
		t.Errorf("deep refinement turns = %q", args)
	}
	if cwd, _ := os.ReadFile(cwdFile); strings.TrimSpace(string(cwd)) != dir {
		t.Errorf("deep refinement ran in %s, want the root folder %s", cwd, dir)
	}
	if !slices.Contains(args, "--permission-mode") || !slices.Contains(args, "plan") {
		t.Errorf("deep refinement is not read-only: %q", args)
	}
}

// TestRefineInstructionsDeep: the deep prompt swaps the no-code paragraph for the explore one.
func TestRefineInstructionsDeep(t *testing.T) {
	for _, k := range []string{KindCode, KindResearch, KindPlan} {
		off := refineInstructionsFor(k)
		if !strings.Contains(off, refineNoCode) || strings.Contains(off, refineDeepCode) {
			t.Errorf("%s: off prompt changed", k)
		}
	}
}
