package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testCands = []Candidate{
	{"claude", "haiku", ModelMeta{1, 1}},
	{"claude", "sonnet", ModelMeta{3, 3}},
	{"claude", "opus", ModelMeta{5, 15}},
	{"opencode", "ollama/free-small", ModelMeta{2, 0}},
	{"opencode", "ollama/free-big", ModelMeta{4, 0}},
}

func TestPickModelWeighsCapabilityAgainstCost(t *testing.T) {
	cases := []struct {
		complexity int
		want       string
	}{
		{1, "opencode/ollama/free-small"}, // simple: a free adapter is capable enough and beats haiku on cost
		{2, "opencode/ollama/free-small"},
		{3, "opencode/ollama/free-big"}, // free beats sonnet, but only as capable as needed
		{4, "opencode/ollama/free-big"},
		{5, "claude/opus"}, // hard: only opus is capable enough
	}
	for _, c := range cases {
		got, ok := pickModel(testCands, c.complexity)
		if !ok || got.Harness+"/"+got.Model != c.want {
			t.Errorf("complexity %d: got %v, want %s", c.complexity, got, c.want)
		}
	}
	// Without the free adapter: simple → small and cheap, complex → capable.
	claude := testCands[:3]
	if got, _ := pickModel(claude, 1); got.Model != "haiku" {
		t.Errorf("simple task got %v, want haiku", got)
	}
	if got, _ := pickModel(claude, 3); got.Model != "sonnet" {
		t.Errorf("medium task got %v, want sonnet", got)
	}
	if got, _ := pickModel(claude, 5); got.Model != "opus" {
		t.Errorf("complex task got %v, want opus", got)
	}
	// Nothing capable enough: the most capable one.
	if got, _ := pickModel(claude[:2], 5); got.Model != "sonnet" {
		t.Errorf("too hard for the pool got %v, want the most capable (sonnet)", got)
	}
	if _, ok := pickModel(nil, 3); ok {
		t.Error("empty pool picked something")
	}
}

func TestResolveModelFallbacks(t *testing.T) {
	// A valid choice is used as given.
	if c, _, ok := resolveModel(testCands, modelChoice{Model: "sonnet", Harness: "claude", Complexity: 3}); !ok || c.Model != "sonnet" {
		t.Errorf("valid choice: %v %v", c, ok)
	}
	// Harness omitted: matched by model name.
	if c, _, ok := resolveModel(testCands, modelChoice{Model: "opus"}); !ok || c.Harness != "claude" {
		t.Errorf("harness omitted: %v %v", c, ok)
	}
	// Unknown model but a complexity: the board's rule picks.
	if c, why, ok := resolveModel(testCands[:3], modelChoice{Model: "gpt-9", Complexity: 5}); !ok || c.Model != "opus" || !strings.Contains(why, "not available") {
		t.Errorf("unavailable model: %v %q %v", c, why, ok)
	}
	// Model on the wrong harness is unavailable.
	if _, _, ok := resolveModel(testCands, modelChoice{Model: "opus", Harness: "opencode"}); ok {
		t.Error("opus on opencode accepted")
	}
	// Nothing usable: not ok, so the caller keeps the manual model.
	if _, _, ok := resolveModel(testCands, modelChoice{}); ok {
		t.Error("empty choice accepted")
	}
	if _, _, ok := resolveModel(testCands, modelChoice{Complexity: 9}); ok {
		t.Error("out of range complexity accepted")
	}
}

func TestRefineSchemaFor(t *testing.T) {
	if refineSchemaFor(false) != refineSchema {
		t.Error("schema changed with automatic selection off")
	}
	s := refineSchemaFor(true)
	for _, k := range []string{`"model"`, `"harness"`, `"complexity"`, `"notes"`, `"required"`} {
		if !strings.Contains(s, k) {
			t.Errorf("schema lacks %s", k)
		}
	}
}

func TestAutoModelSettings(t *testing.T) {
	if defaultSettings().AutoModel {
		t.Fatal("auto_model must default to off")
	}
	app := newTestApp(t, testDir(t))
	st, _ := app.store.Settings()
	st.AutoModel = true
	st.ModelInfo = map[string]ModelMeta{"opencode/ollama/x": {Tier: 2, Cost: 0}}
	if err := st.validate(); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	got, _ := app.store.Settings()
	if !got.AutoModel || got.ModelInfo["opencode/ollama/x"].Tier != 2 {
		t.Errorf("not persisted: %+v", got)
	}
	for _, bad := range []map[string]ModelMeta{
		{"nokey": {Tier: 1}}, {"nope/x": {Tier: 1}}, {"claude/x": {Tier: 0}}, {"claude/x": {Tier: 6}}, {"claude/x": {Tier: 1, Cost: -1}},
	} {
		st.ModelInfo = bad
		if st.validate() == nil {
			t.Errorf("model_info %v accepted", bad)
		}
	}
}

// refinerScript writes a fake claude whose refinement answers with the given extra JSON fields; it records the prompt.
func refinerScript(t *testing.T, dir, extra string) (script, promptFile string) {
	t.Helper()
	script = filepath.Join(dir, "auto-claude.sh")
	promptFile = filepath.Join(dir, "refine-prompt.txt")
	body := "#!/usr/bin/env bash\ncat > '" + promptFile + "'\n" +
		`echo '{"type":"result","is_error":false,"result":"","structured_output":{"title":"T","refined_prompt":"P","repos":[],"notes":"n"` + extra + `},"total_cost_usd":0}'` + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return
}

// refineWith runs a refinement with settings tweaked by cfg and the refiner answering extra.
func refineWith(t *testing.T, cfg func(*Settings, *Card), extra string) (*App, *Card, string) {
	t.Helper()
	dir := testDir(t)
	app := newTestApp(t, dir)
	script, promptFile := refinerScript(t, dir, extra)
	app.claudeBin = script
	st, _ := app.store.Settings()
	c := newCard(t, app, dir, "task")
	cfg(&st, c)
	if err := app.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	c.Model = "manual-model"
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, app, c.ID, StatusReady)
	prompt, _ := os.ReadFile(promptFile)
	return app, got, string(prompt)
}

func boardNotes(t *testing.T, app *App, id int64) string {
	t.Helper()
	evs, _ := app.store.Events(id, 100)
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(string(e.Data) + "\n")
	}
	return b.String()
}

// autoOn switches automatic selection on for the card (the option is per card).
func autoOn(_ *Settings, c *Card) { c.AutoModel = true }

func TestAutoModelSimpleTaskGetsSmallModel(t *testing.T) {
	app, c, prompt := refineWith(t, autoOn, `,"complexity":1,"model":"haiku","harness":"claude","model_reason":"tiny edit"`)
	if c.Model != "haiku" || c.Harness != "claude" {
		t.Errorf("card runs %s/%s, want claude/haiku", c.Harness, c.Model)
	}
	if !strings.Contains(prompt, "model=haiku tier=1") || !strings.Contains(prompt, "model=opus tier=5") {
		t.Errorf("refiner prompt lacks the model list: %q", prompt)
	}
	if n := boardNotes(t, app, c.ID); !strings.Contains(n, "automatic model selection: claude/haiku") || !strings.Contains(n, "tiny edit") {
		t.Errorf("choice and reason not logged: %s", n)
	}
}

func TestAutoModelComplexTaskGetsCapableModel(t *testing.T) {
	_, c, _ := refineWith(t, autoOn, `,"complexity":5,"model":"opus","harness":"claude","model_reason":"large refactor"`)
	if c.Model != "opus" {
		t.Errorf("card runs %s, want opus", c.Model)
	}
}

func TestAutoModelPrefersFreeAdapter(t *testing.T) {
	cfg := func(s *Settings, c *Card) {
		c.AutoModel = true
		s.ModelInfo = map[string]ModelMeta{"opencode/opencode/fake-free": {Tier: 3, Cost: 0}}
	}
	// The refiner names the free model: the card moves to the opencode adapter, with settings that adapter accepts.
	app, c, prompt := refineWith(t, cfg, `,"complexity":3,"model":"opencode/fake-free","harness":"opencode","model_reason":"free and capable enough"`)
	if c.Harness != "opencode" || c.Model != "opencode/fake-free" {
		t.Errorf("card runs %s/%s, want opencode/opencode/fake-free", c.Harness, c.Model)
	}
	if c.Effort != "" {
		t.Errorf("effort %q is not valid for opencode", c.Effort)
	}
	if !strings.Contains(prompt, "harness=opencode model=opencode/fake-free tier=3 cost=0") {
		t.Errorf("free adapter not offered: %q", prompt)
	}
	_ = app
	// The refiner only rates the task: the board's rule prefers the free adapter over sonnet.
	_, c, _ = refineWith(t, cfg, `,"complexity":3`)
	if c.Harness != "opencode" || c.Model != "opencode/fake-free" {
		t.Errorf("by complexity card runs %s/%s, want the free adapter", c.Harness, c.Model)
	}
	// A task harder than the free model can do goes back to claude.
	_, c, _ = refineWith(t, cfg, `,"complexity":5`)
	if c.Harness != "claude" || !strings.Contains(c.Model, "opus") {
		t.Errorf("hard task runs %s/%s, want claude/opus", c.Harness, c.Model)
	}
}

func TestAutoModelFallsBackToManual(t *testing.T) {
	for name, extra := range map[string]string{
		"no choice":           ``,
		"unavailable model":   `,"model":"gpt-9","harness":"claude"`,
		"unavailable harness": `,"model":"haiku","harness":"opencode"`,
	} {
		app, c, _ := refineWith(t, autoOn, extra)
		if c.Model != "manual-model" || c.Harness != "claude" {
			t.Errorf("%s: card runs %s/%s, want the manual claude/manual-model", name, c.Harness, c.Model)
		}
		if n := boardNotes(t, app, c.ID); !strings.Contains(n, "keeping claude/manual-model (manual setting)") {
			t.Errorf("%s: fallback not logged: %s", name, n)
		}
	}
}

func TestAutoModelOffChangesNothing(t *testing.T) {
	app, c, prompt := refineWith(t, func(*Settings, *Card) {}, `,"complexity":1,"model":"haiku","harness":"claude"`)
	if c.Model != "manual-model" || c.Harness != "claude" {
		t.Errorf("toggle off but card runs %s/%s", c.Harness, c.Model)
	}
	if strings.Contains(prompt, "Available models") || strings.Contains(boardNotes(t, app, c.ID), "automatic model selection") {
		t.Error("toggle off but automatic selection ran")
	}
}

// The option is per card: the setting alone does not switch a card on, and a card can be on while the setting is off.
func TestAutoModelIsPerCard(t *testing.T) {
	choice := `,"complexity":1,"model":"haiku","harness":"claude"`
	_, c, _ := refineWith(t, func(s *Settings, c *Card) { s.AutoModel = true }, choice) // setting on, card off
	if c.Model != "manual-model" {
		t.Errorf("card without the option ran %s", c.Model)
	}
	_, c, _ = refineWith(t, func(s *Settings, c *Card) { c.AutoModel = true }, choice) // setting off, card on
	if c.Model != "haiku" {
		t.Errorf("card with the option ran %s", c.Model)
	}
}

// New cards are seeded from the setting; the API can set and change the option per card.
func TestAutoModelCardOption(t *testing.T) {
	app := newTestApp(t, testDir(t))
	root, x, y := app.guard.root, "x", "y"
	c, err := app.CreateCard(cardInput{Description: &x, RootFolder: &root})
	if err != nil || c.AutoModel {
		t.Fatalf("default card: %v auto=%v", err, c != nil && c.AutoModel)
	}
	on := true
	c, err = app.CreateCard(cardInput{Description: &x, RootFolder: &root, AutoModel: &on})
	if err != nil || !c.AutoModel {
		t.Fatalf("card created with the option: %v", err)
	}
	if got, _ := app.store.GetCard(c.ID); !got.AutoModel {
		t.Error("option not persisted")
	}
	off := false
	if c, err = app.EditCard(c.ID, cardInput{AutoModel: &off}); err != nil || c.AutoModel {
		t.Errorf("option not switched off: %v", err)
	}
	st, _ := app.store.Settings()
	st.AutoModel = true
	app.store.SaveSettings(st)
	if c, err = app.CreateCard(cardInput{Description: &y, RootFolder: &root}); err != nil || !c.AutoModel {
		t.Errorf("new card not seeded from the setting: %v", err)
	}
}
