package board

import (
	"os"
	"path/filepath"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/settings"
	"maestrello/internal/testenv"
)

func TestProjectSettingsStorageAndAPI(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	p := mkProject(t, app, "Alpha", filepath.Join(dir, "a"))
	base := app.baseURL + "/api/projects/"

	var got model.Project
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
	if l, _ := app.store.ProjectSettings(p.ID); !l.IsEmpty() {
		t.Errorf("not cleared: %+v", l)
	}
}

func TestGlobalKindSettingsAPI(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	url := app.baseURL + "/api/settings"
	var st model.Settings
	sendJSON(t, "GET", url, nil, &st)
	st.Kinds = map[string]model.Overrides{model.KindResearch: {Model: sp("opus")}}
	if code := sendJSON(t, "PUT", url, st, &st); code != 200 {
		t.Fatalf("put = %d", code)
	}
	// A client that sends no "kinds" (the older UI) keeps them; {} clears them.
	var raw map[string]any
	sendJSON(t, "GET", url, nil, &raw)
	delete(raw, "kinds")
	sendJSON(t, "PUT", url, raw, &st)
	if g, _ := app.store.Settings(); g.Kinds[model.KindResearch].Model == nil {
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
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	rootA, rootB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.MkdirAll(rootA, 0o755)
	os.MkdirAll(rootB, 0o755)
	a, b := mkProject(t, app, "A", rootA), mkProject(t, app, "B", rootB)

	g := model.DefaultSettings()
	g.Kinds = map[string]model.Overrides{model.KindResearch: {Model: sp("opus")}}
	app.store.SaveSettings(g)
	app.SetProjectSettings(b.ID, model.LayerSettings{Kinds: map[string]model.Overrides{
		model.KindResearch: {Harness: sp("opencode"), Model: sp("ollama/qwen3-coder:30b")},
	}})
	app.SetProjectSettings(a.ID, model.LayerSettings{Overrides: model.Overrides{MaxTurns: ip(7)}})

	mk := func(p *model.Project, kind string) *model.Card {
		c, err := app.CreateCard(cardInput{Description: sp("x"), ProjectID: &p.ID, Kind: sp(kind), SkipRefinement: bp(false)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		p                    *model.Project
		kind, harness, model string
		turns                int
	}{
		{a, model.KindCode, "claude", "sonnet", 7},
		{a, model.KindResearch, "claude", "opus", 7},
		{b, model.KindCode, "claude", "sonnet", 50},
		{b, model.KindResearch, "opencode", "ollama/qwen3-coder:30b", 50},
	}
	for _, c := range cases {
		got := mk(c.p, c.kind)
		if got.Harness != c.harness || got.Model != c.model || got.MaxTurns != c.turns {
			t.Errorf("%s/%s: harness %s model %s turns %d, want %s %s %d", c.p.Name, c.kind, got.Harness, got.Model, got.MaxTurns, c.harness, c.model, c.turns)
		}
	}
	// Explicit input still wins over every layer.
	c, err := app.CreateCard(cardInput{Description: sp("x"), ProjectID: &b.ID, Kind: sp(model.KindResearch), Model: sp("manual")})
	if err != nil || c.Model != "manual" {
		t.Errorf("input must beat the layers: %+v %v", c, err)
	}
	// A card without a named project finds its project through the root folder.
	c, err = app.CreateCard(cardInput{Description: sp("x"), RootFolder: &rootB, Kind: sp(model.KindResearch)})
	if err != nil || c.Harness != "opencode" {
		t.Errorf("project via root folder: %+v %v", c, err)
	}
}

func TestResolvedSettingsEndpoint(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	p := mkProject(t, app, "A", filepath.Join(dir, "a"))
	app.SetProjectSettings(p.ID, model.LayerSettings{Kinds: map[string]model.Overrides{model.KindPlan: {Model: sp("opus")}}})
	var out struct {
		Settings model.Settings    `json:"settings"`
		Sources  map[string]string `json:"sources"`
	}
	if code := sendJSON(t, "GET", app.baseURL+"/api/settings/resolved?project="+itoa(p.ID)+"&kind=plan", nil, &out); code != 200 ||
		out.Settings.DefaultModel != "opus" || out.Sources["model"] != settings.LayerProjectKind {
		t.Errorf("resolved = %d %+v %v", code, out.Settings.DefaultModel, out.Sources)
	}
	var e map[string]string
	if code := sendJSON(t, "GET", app.baseURL+"/api/settings/resolved?kind=bogus", nil, &e); code != 400 {
		t.Errorf("bogus kind = %d", code)
	}
}

func sp(s string) *string { return &s }

func ip(i int) *int { return &i }

func bp(b bool) *bool { return &b }
