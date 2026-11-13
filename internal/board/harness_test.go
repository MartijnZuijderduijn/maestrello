package board

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/settings"
	"maestrello/internal/testenv"
)

func TestHarnessBinaryReadAtCallTime(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	app.claudeBin = "/changed/claude"
	h, err := app.harness("")
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := h.RefineCommand(context.Background(), harness.RefineSpec{})
	if cmd.Path != "/changed/claude" {
		t.Errorf("path = %q", cmd.Path)
	}
}

func TestHarnessOverHTTP(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	st := model.DefaultSettings()
	st.Harness = "nope"
	var e map[string]string
	if code := sendJSON(t, "PUT", app.baseURL+"/api/settings", st, &e); code != 400 {
		t.Errorf("PUT unknown harness = %d", code)
	}
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": "t", "root_folder": dir, "harness": "nope"}, nil); code != 400 {
		t.Errorf("POST unknown harness = %d", code)
	}
	// The web UI's PUT may leave harness out.
	if code := sendJSON(t, "PUT", app.baseURL+"/api/settings", map[string]any{
		"default_model": "sonnet", "default_effort": "low", "default_permission_mode": "auto",
		"refine_model": "sonnet", "refine_effort": "low"}, nil); code != 200 {
		t.Errorf("PUT without harness = %d", code)
	}
	if got, _ := app.store.Settings(); got.Harness != "claude" {
		t.Errorf("settings harness = %q", got.Harness)
	}
	var c model.Card
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": "t", "root_folder": dir}, &c); code != 201 || c.Harness != "claude" {
		t.Errorf("POST without harness = %d, %q", code, c.Harness)
	}
}

func TestHarnessesEndpoint(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	var got []harnessInfo
	if code := sendJSON(t, "GET", app.baseURL+"/api/harnesses", nil, &got); code != 200 {
		t.Fatalf("GET /api/harnesses = %d", code)
	}
	if len(got) != 4 || got[0].Name != "claude" || got[1].Name != "mistral" || got[2].Name != "omp" || got[3].Name != "opencode" {
		t.Fatalf("harnesses = %+v", got)
	}
	claude, _ := harness.For("claude")
	caps := claude.Caps()
	if !slices.Equal(got[0].Efforts, caps.Efforts) || !slices.Equal(got[0].PermissionModes, caps.PermissionModes) {
		t.Errorf("claude choices = %+v", got[0])
	}
	if !slices.Contains(got[0].Efforts, "medium") || !slices.Contains(got[0].PermissionModes, "auto") {
		t.Errorf("claude choices miss medium or auto: %+v", got[0])
	}
}

func TestCreateCardWithHarnessOverHTTP(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	var c model.Card
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": "t", "root_folder": dir, "harness": "claude"}, &c); code != 201 || c.Harness != "claude" {
		t.Errorf("POST with claude = %d, %q", code, c.Harness)
	}
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": "t", "root_folder": dir, "harness": "nope"}, nil); code != 400 {
		t.Errorf("POST with unknown harness = %d", code)
	}
}

func TestBoardMCPHarness(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	newCard(t, app, dir, "")
	text, isErr := boardCall(t, app, "list_cards", nil)
	var list []map[string]any
	json.Unmarshal([]byte(text), &list)
	if isErr || len(list) != 1 || list[0]["harness"] != "claude" {
		t.Errorf("list_cards = %s", text)
	}
	text, isErr = boardCall(t, app, "create_card", map[string]any{"title": "h", "root_folder": dir, "harness": "claude"})
	if isErr || !strings.Contains(text, `"harness": "claude"`) {
		t.Errorf("create_card with claude = %s", text)
	}
	if text, isErr := boardCall(t, app, "create_card", map[string]any{"title": "h", "root_folder": dir, "harness": "nope"}); !isErr || !strings.Contains(text, "unknown harness") {
		t.Errorf("create_card with unknown harness = %q, %v", text, isErr)
	}
	c := newCard(t, app, dir, "")
	if text, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "harness": "nope"}); !isErr {
		t.Errorf("update_card with unknown harness = %q", text)
	}
}

func TestHarnessInCardInputAndSettings(t *testing.T) {
	str := func(s string) *string { return &s }
	c := &model.Card{Title: "t", RootFolder: "/abs", PermissionMode: "auto", Effort: "medium"}
	if err := (&cardInput{}).apply(c); err != nil || c.Harness != "claude" {
		t.Errorf("empty harness: %v, %q", err, c.Harness)
	}
	if err := (&cardInput{Harness: str("claude")}).apply(c); err != nil {
		t.Errorf("claude rejected: %v", err)
	}
	err := (&cardInput{Harness: str("nope")}).apply(c)
	if err == nil || err.Error() != "unknown harness" {
		t.Errorf("unknown card harness: %v", err)
	}

	st := model.DefaultSettings()
	if st.Harness != "claude" || settings.Validate(st) != nil {
		t.Errorf("default settings: %+v", st)
	}
	st.Harness = ""
	if err := settings.Validate(st); err != nil {
		t.Errorf("empty settings harness: %v", err)
	}
	st.Harness = "codex"
	if err := settings.Validate(st); err == nil {
		t.Error("unknown settings harness accepted")
	}
}
