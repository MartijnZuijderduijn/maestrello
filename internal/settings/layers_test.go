package settings

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"path/filepath"
	"reflect"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/store"
	"maestrello/internal/testenv"
)

func sp(s string) *string { return &s }

func ip(i int) *int { return &i }

func bp(b bool) *bool { return &b }

// base is the global layer the tests start from: claude, sonnet, medium.
func baseSettings() model.Settings { return model.DefaultSettings() }

func TestResolveGlobalOnly(t *testing.T) {
	g := baseSettings()
	got, src := resolveLayers(g, nil, model.KindCode)
	if !reflect.DeepEqual(got, g) || len(src) != 0 {
		t.Errorf("no layers: got %+v sources %v, want the global settings untouched", got, src)
	}
	// Empty layers (and an empty kind name, which means code) change nothing either.
	got, _ = resolveLayers(g, &model.LayerSettings{}, "")
	if !reflect.DeepEqual(got, g) {
		t.Errorf("empty project layer changed the settings: %+v", got)
	}
}

func TestResolveProjectOverride(t *testing.T) {
	g := baseSettings()
	proj := &model.LayerSettings{Overrides: model.Overrides{Harness: sp("opencode"), Model: sp("ollama/qwen3-coder:30b"), MaxTurns: ip(10)}}
	got, src := resolveLayers(g, proj, model.KindCode)
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
	g.Kinds = map[string]model.Overrides{model.KindResearch: {Model: sp("opus"), Effort: sp("high")}}
	research, _ := resolveLayers(g, nil, model.KindResearch)
	code, _ := resolveLayers(g, nil, model.KindCode)
	if research.DefaultModel != "opus" || research.DefaultEffort != "high" {
		t.Errorf("research = %+v", research)
	}
	if code.DefaultModel != g.DefaultModel || code.DefaultEffort != g.DefaultEffort {
		t.Errorf("another kind must not see the override: %+v", code)
	}
}

func TestResolveKindWithinProject(t *testing.T) {
	g := baseSettings()
	g.Kinds = map[string]model.Overrides{model.KindResearch: {Model: sp("opus"), MaxTurns: ip(5)}}
	proj := &model.LayerSettings{
		Overrides: model.Overrides{Model: sp("haiku"), PushDefault: sp("pr")},
		Kinds:     map[string]model.Overrides{model.KindResearch: {Model: sp("sonnet")}, model.KindChat: {Harness: sp("opencode")}},
	}
	// kind in project > project > global kind > global
	r, src := resolveLayers(g, proj, model.KindResearch)
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
	p2 := &model.LayerSettings{Overrides: model.Overrides{Model: sp("haiku")}}
	if r, _ = resolveLayers(g, p2, model.KindResearch); r.DefaultModel != "haiku" {
		t.Errorf("project (haiku) must beat global kind (opus): got %q", r.DefaultModel)
	}
	// The same project resolves differently per kind.
	code, _ := resolveLayers(g, proj, model.KindCode)
	chat, _ := resolveLayers(g, proj, model.KindChat)
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
	proj := &model.LayerSettings{
		Overrides: model.Overrides{MaxTurns: ip(20), AutoRun: bp(true)},
		Kinds:     map[string]model.Overrides{model.KindPlan: {Model: sp("opus")}}, // sets neither max_turns nor auto_run
	}
	got, _ := resolveLayers(g, proj, model.KindPlan)
	if got.DefaultMaxTurns != 20 || !got.DefaultAutoRun || got.DefaultModel != "opus" {
		t.Errorf("an unset kind value must fall through to the project: %+v", got)
	}
	// Explicit zero values are real settings: 0 turns = no limit, auto_run false, effort "".
	g.DefaultAutoRun = true
	proj = &model.LayerSettings{Overrides: model.Overrides{MaxTurns: ip(0), AutoRun: bp(false), Effort: sp("")}}
	got, _ = resolveLayers(g, proj, model.KindCode)
	if got.DefaultMaxTurns != 0 || got.DefaultAutoRun || got.DefaultEffort != "" {
		t.Errorf("explicit zero values must win: %+v", got)
	}
}

func TestResolveToolsAndNoAliasing(t *testing.T) {
	g := baseSettings()
	g.DefaultTools = map[string][]string{model.KindChat: {"web_search"}}
	proj := &model.LayerSettings{Kinds: map[string]model.Overrides{model.KindResearch: {Tools: &[]string{"web_search"}}}}
	r, _ := resolveLayers(g, proj, model.KindResearch)
	if !reflect.DeepEqual(r.DefaultTools[model.KindResearch], []string{"web_search"}) || !reflect.DeepEqual(r.DefaultTools[model.KindChat], []string{"web_search"}) {
		t.Errorf("tools = %v", r.DefaultTools)
	}
	if _, ok := g.DefaultTools[model.KindResearch]; ok {
		t.Error("resolving must not modify the global settings")
	}
}

func TestResolveMissingConfig(t *testing.T) {
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "test.db")
	st, err := store.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// No settings saved, no project settings: the built-in defaults.
	got, src, err := Resolve(st, 0, model.KindCode)
	if err != nil || !reflect.DeepEqual(got, model.DefaultSettings()) || len(src) != 0 {
		t.Errorf("missing config: %+v %v %v", got, src, err)
	}
	// An unknown project resolves like none.
	if got, _, err = Resolve(st, 999, model.KindResearch); err != nil || got.Harness != "claude" {
		t.Errorf("unknown project: %+v %v", got, err)
	}
	// A settings blob saved before layers existed (no "kinds") still loads.
	if err := st.SetKV("other", "x"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`INSERT INTO settings (key, value) VALUES ('settings', '{"default_model":"opus","harness":"claude"}')`)
	if got, _, _ = Resolve(st, 0, model.KindCode); got.DefaultModel != "opus" || got.Kinds != nil {
		t.Errorf("old settings blob: %+v", got)
	}
}
