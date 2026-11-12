package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }
func bp(b bool) *bool     { return &b }

// base is the global layer the tests start from: claude, sonnet, medium.
func baseSettings() Settings { return defaultSettings() }

func TestResolveGlobalOnly(t *testing.T) {
	g := baseSettings()
	got, src := resolveLayers(g, nil, KindCode)
	if !reflect.DeepEqual(got, g) || len(src) != 0 {
		t.Errorf("no layers: got %+v sources %v, want the global settings untouched", got, src)
	}
	// Empty layers (and an empty kind name, which means code) change nothing either.
	got, _ = resolveLayers(g, &LayerSettings{}, "")
	if !reflect.DeepEqual(got, g) {
		t.Errorf("empty project layer changed the settings: %+v", got)
	}
}

func TestResolveProjectOverride(t *testing.T) {
	g := baseSettings()
	proj := &LayerSettings{Overrides: Overrides{Harness: sp("opencode"), Model: sp("ollama/qwen3-coder:30b"), MaxTurns: ip(10)}}
	got, src := resolveLayers(g, proj, KindCode)
	if got.Harness != "opencode" || got.DefaultModel != "ollama/qwen3-coder:30b" || got.DefaultMaxTurns != 10 {
		t.Errorf("project override not applied: %+v", got)
	}
	// Not overridden: falls through to the global value.
	if got.DefaultMaxMinutes != g.DefaultMaxMinutes || got.RefineModel != g.RefineModel {
		t.Errorf("unset values did not fall through: %+v", got)
	}
	// opencode has no effort: the global "medium" is dropped, not kept invalid.
	if got.DefaultEffort != "" {
		t.Errorf("effort = %q, want it fitted to opencode (empty)", got.DefaultEffort)
	}
	if src["harness"] != LayerProject || src["max_turns"] != LayerProject || src["max_minutes"] != "" {
		t.Errorf("sources = %v", src)
	}
}

func TestResolveGlobalKindOverride(t *testing.T) {
	g := baseSettings()
	g.Kinds = map[string]Overrides{KindResearch: {Model: sp("opus"), Effort: sp("high")}}
	research, _ := resolveLayers(g, nil, KindResearch)
	code, _ := resolveLayers(g, nil, KindCode)
	if research.DefaultModel != "opus" || research.DefaultEffort != "high" {
		t.Errorf("research = %+v", research)
	}
	if code.DefaultModel != g.DefaultModel || code.DefaultEffort != g.DefaultEffort {
		t.Errorf("another kind must not see the override: %+v", code)
	}
}

func TestResolveKindWithinProject(t *testing.T) {
	g := baseSettings()
	g.Kinds = map[string]Overrides{KindResearch: {Model: sp("opus"), MaxTurns: ip(5)}}
	proj := &LayerSettings{
		Overrides: Overrides{Model: sp("haiku"), PushDefault: sp("pr")},
		Kinds:     map[string]Overrides{KindResearch: {Model: sp("sonnet")}, KindChat: {Harness: sp("opencode")}},
	}
	// kind in project > project > global kind > global
	r, src := resolveLayers(g, proj, KindResearch)
	if r.DefaultModel != "sonnet" || src["model"] != LayerProjectKind {
		t.Errorf("research model = %q from %q, want sonnet from project kind", r.DefaultModel, src["model"])
	}
	if r.DefaultMaxTurns != 5 || src["max_turns"] != LayerGlobalKind {
		t.Errorf("max turns = %d from %q, want 5 from global kind", r.DefaultMaxTurns, src["max_turns"])
	}
	if r.DefaultPushDefault != "pr" || src["push_default"] != LayerProject {
		t.Errorf("push default = %q from %q, want pr from project", r.DefaultPushDefault, src["push_default"])
	}
	// A project value beats the global kind value when the project's kind layer is silent.
	p2 := &LayerSettings{Overrides: Overrides{Model: sp("haiku")}}
	if r, _ = resolveLayers(g, p2, KindResearch); r.DefaultModel != "haiku" {
		t.Errorf("project (haiku) must beat global kind (opus): got %q", r.DefaultModel)
	}
	// The same project resolves differently per kind.
	code, _ := resolveLayers(g, proj, KindCode)
	chat, _ := resolveLayers(g, proj, KindChat)
	if code.Harness != "claude" || code.DefaultModel != "haiku" {
		t.Errorf("code = %+v", code)
	}
	if chat.Harness != "opencode" || chat.DefaultModel != "haiku" {
		t.Errorf("chat = %+v", chat)
	}
}

func TestResolveFallThroughAndExplicitEmpty(t *testing.T) {
	g := baseSettings()
	g.DefaultMaxTurns = 50
	proj := &LayerSettings{
		Overrides: Overrides{MaxTurns: ip(20), AutoRun: bp(true)},
		Kinds:     map[string]Overrides{KindPlan: {Model: sp("opus")}}, // sets neither max_turns nor auto_run
	}
	got, _ := resolveLayers(g, proj, KindPlan)
	if got.DefaultMaxTurns != 20 || !got.DefaultAutoRun || got.DefaultModel != "opus" {
		t.Errorf("an unset kind value must fall through to the project: %+v", got)
	}
	// Explicit zero values are real settings: 0 turns = no limit, auto_run false, effort "".
	g.DefaultAutoRun = true
	proj = &LayerSettings{Overrides: Overrides{MaxTurns: ip(0), AutoRun: bp(false), Effort: sp("")}}
	got, _ = resolveLayers(g, proj, KindCode)
	if got.DefaultMaxTurns != 0 || got.DefaultAutoRun || got.DefaultEffort != "" {
		t.Errorf("explicit zero values must win: %+v", got)
	}
}

func TestResolveToolsAndNoAliasing(t *testing.T) {
	g := baseSettings()
	g.DefaultTools = map[string][]string{KindChat: {"web_search"}}
	proj := &LayerSettings{Kinds: map[string]Overrides{KindResearch: {Tools: &[]string{"web_search"}}}}
	r, _ := resolveLayers(g, proj, KindResearch)
	if !reflect.DeepEqual(r.DefaultTools[KindResearch], []string{"web_search"}) || !reflect.DeepEqual(r.DefaultTools[KindChat], []string{"web_search"}) {
		t.Errorf("tools = %v", r.DefaultTools)
	}
	if _, ok := g.DefaultTools[KindResearch]; ok {
		t.Error("resolving must not modify the global settings")
	}
}

func TestResolveMissingConfig(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	// No settings saved, no project settings: the built-in defaults.
	got, src, err := app.store.ResolveSettings(0, KindCode)
	if err != nil || !reflect.DeepEqual(got, defaultSettings()) || len(src) != 0 {
		t.Errorf("missing config: %+v %v %v", got, src, err)
	}
	// An unknown project resolves like none.
	if got, _, err = app.store.ResolveSettings(999, KindResearch); err != nil || got.Harness != "claude" {
		t.Errorf("unknown project: %+v %v", got, err)
	}
	// A settings blob saved before layers existed (no "kinds") still loads.
	if err := app.store.SetKV("other", "x"); err != nil {
		t.Fatal(err)
	}
	app.store.db.Exec(`INSERT INTO settings (key, value) VALUES ('settings', '{"default_model":"opus","harness":"claude"}')`)
	if got, _, _ = app.store.ResolveSettings(0, KindCode); got.DefaultModel != "opus" || got.Kinds != nil {
		t.Errorf("old settings blob: %+v", got)
	}
}

func TestProjectSettingsStorageAndAPI(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	p := mkProject(t, app, "Alpha", filepath.Join(dir, "a"))
	base := app.baseURL + "/api/projects/"

	var got Project
	patch := map[string]any{"settings": map[string]any{
		"model": "haiku",
		"kinds": map[string]any{"research": map[string]any{"harness": "opencode", "model": "ollama/x"}},
	}}
	if code := sendJSON(t, "PATCH", base+itoa(p.ID), patch, &got); code != 200 ||
		*got.Settings.Model != "haiku" || *got.Settings.Kinds["research"].Harness != "opencode" {
		t.Fatalf("patch = %d %+v", code, got.Settings)
	}
	// Persisted, and a PATCH without settings leaves them alone.
	sendJSON(t, "PATCH", base+itoa(p.ID), map[string]string{"name": "Alpha2"}, &got)
	if l, _ := app.store.ProjectSettings(p.ID); l.Model == nil || *l.Model != "haiku" {
		t.Errorf("settings lost by a rename: %+v", l)
	}
	// Invalid input is refused and changes nothing, even with a rename in the same request.
	var e map[string]string
	for _, bad := range []map[string]any{
		{"harness": "nope"},
		{"kinds": map[string]any{"bogus": map[string]any{"model": "x"}}},
		{"max_turns": -1},
		{"permission_mode": "whatever"},
		{"tools": []string{"no-such-tool"}},
	} {
		if code := sendJSON(t, "PATCH", base+itoa(p.ID), map[string]any{"name": "Renamed", "settings": bad}, &e); code != 400 {
			t.Errorf("settings %v = %d, want 400", bad, code)
		}
	}
	if q, _ := app.store.GetProject(p.ID); q.Name != "Alpha2" || *q.Settings.Model != "haiku" {
		t.Errorf("a refused PATCH changed the project: %+v", q)
	}
	// {} clears.
	sendJSON(t, "PATCH", base+itoa(p.ID), map[string]any{"settings": map[string]any{}}, &got)
	if l, _ := app.store.ProjectSettings(p.ID); !l.isEmpty() {
		t.Errorf("not cleared: %+v", l)
	}
}

func TestGlobalKindSettingsAPI(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	url := app.baseURL + "/api/settings"
	var st Settings
	sendJSON(t, "GET", url, nil, &st)
	st.Kinds = map[string]Overrides{KindResearch: {Model: sp("opus")}}
	if code := sendJSON(t, "PUT", url, st, &st); code != 200 {
		t.Fatalf("put = %d", code)
	}
	// A client that sends no "kinds" (the older UI) keeps them; {} clears them.
	var raw map[string]any
	sendJSON(t, "GET", url, nil, &raw)
	delete(raw, "kinds")
	sendJSON(t, "PUT", url, raw, &st)
	if g, _ := app.store.Settings(); g.Kinds[KindResearch].Model == nil {
		t.Error("PUT without kinds wiped them")
	}
	raw["kinds"] = map[string]any{}
	sendJSON(t, "PUT", url, raw, &st)
	if g, _ := app.store.Settings(); len(g.Kinds) != 0 {
		t.Errorf("kinds not cleared: %+v", g.Kinds)
	}
	var e map[string]string
	raw["kinds"] = map[string]any{"research": map[string]any{"harness": "nope"}}
	if code := sendJSON(t, "PUT", url, raw, &e); code != 400 {
		t.Errorf("bad kind override = %d, want 400", code)
	}
}

// New cards are seeded from the settings resolved for their project and kind; running them uses what
// they were seeded with.
func TestCreateCardUsesResolvedSettings(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	rootA, rootB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.MkdirAll(rootA, 0o755)
	os.MkdirAll(rootB, 0o755)
	a, b := mkProject(t, app, "A", rootA), mkProject(t, app, "B", rootB)

	g := defaultSettings()
	g.Kinds = map[string]Overrides{KindResearch: {Model: sp("opus")}}
	app.store.SaveSettings(g)
	app.SetProjectSettings(b.ID, LayerSettings{Kinds: map[string]Overrides{
		KindResearch: {Harness: sp("opencode"), Model: sp("ollama/qwen3-coder:30b")},
	}})
	app.SetProjectSettings(a.ID, LayerSettings{Overrides: Overrides{MaxTurns: ip(7)}})

	mk := func(p *Project, kind string) *Card {
		c, err := app.CreateCard(cardInput{Description: sp("x"), ProjectID: &p.ID, Kind: sp(kind), SkipRefinement: bp(false)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		p                    *Project
		kind, harness, model string
		turns                int
	}{
		{a, KindCode, "claude", "sonnet", 7},
		{a, KindResearch, "claude", "opus", 7},
		{b, KindCode, "claude", "sonnet", 50},
		{b, KindResearch, "opencode", "ollama/qwen3-coder:30b", 50},
	}
	for _, c := range cases {
		got := mk(c.p, c.kind)
		if got.Harness != c.harness || got.Model != c.model || got.MaxTurns != c.turns {
			t.Errorf("%s/%s: harness %s model %s turns %d, want %s %s %d", c.p.Name, c.kind, got.Harness, got.Model, got.MaxTurns, c.harness, c.model, c.turns)
		}
	}
	// Explicit input still wins over every layer.
	c, err := app.CreateCard(cardInput{Description: sp("x"), ProjectID: &b.ID, Kind: sp(KindResearch), Model: sp("manual")})
	if err != nil || c.Model != "manual" {
		t.Errorf("input must beat the layers: %+v %v", c, err)
	}
	// A card without a named project finds its project through the root folder.
	c, err = app.CreateCard(cardInput{Description: sp("x"), RootFolder: &rootB, Kind: sp(KindResearch)})
	if err != nil || c.Harness != "opencode" {
		t.Errorf("project via root folder: %+v %v", c, err)
	}
}

func TestResolvedSettingsEndpoint(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	p := mkProject(t, app, "A", filepath.Join(dir, "a"))
	app.SetProjectSettings(p.ID, LayerSettings{Kinds: map[string]Overrides{KindPlan: {Model: sp("opus")}}})
	var out struct {
		Settings Settings          `json:"settings"`
		Sources  map[string]string `json:"sources"`
	}
	if code := sendJSON(t, "GET", app.baseURL+"/api/settings/resolved?project="+itoa(p.ID)+"&kind=plan", nil, &out); code != 200 ||
		out.Settings.DefaultModel != "opus" || out.Sources["model"] != LayerProjectKind {
		t.Errorf("resolved = %d %+v %v", code, out.Settings.DefaultModel, out.Sources)
	}
	var e map[string]string
	if code := sendJSON(t, "GET", app.baseURL+"/api/settings/resolved?kind=bogus", nil, &e); code != 400 {
		t.Errorf("bogus kind = %d", code)
	}
}
