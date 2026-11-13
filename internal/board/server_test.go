package board

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func TestModelsEndpoint(t *testing.T) {
	app := newTestApp(t, t.TempDir())
	resp, err := http.Get(app.baseURL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0] != "sonnet" {
		t.Errorf("unexpected models: %v", got)
	}
}

func sendJSON(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, url, bytes.NewReader(data))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestPutSettingsValidation(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	url := app.baseURL + "/api/settings"
	good := model.DefaultSettings()
	good.DefaultModel, good.DefaultEffort, good.DefaultPushDefault = "opus", "high", "pr"
	good.DefaultRootFolder = dir
	if code := sendJSON(t, "PUT", url, good, nil); code != 200 {
		t.Fatalf("good PUT = %d", code)
	}
	bad := map[string]func(*model.Settings){
		"model":        func(s *model.Settings) { s.DefaultModel = " " },
		"refine model": func(s *model.Settings) { s.RefineModel = "" },
		"effort":       func(s *model.Settings) { s.DefaultEffort = "extreme" },
		"refine eff":   func(s *model.Settings) { s.RefineEffort = "" },
		"mode":         func(s *model.Settings) { s.DefaultPermissionMode = "yolo" },
		"push":         func(s *model.Settings) { s.DefaultPushDefault = "force" },
		"turns":        func(s *model.Settings) { s.DefaultMaxTurns = -1 },
		"minutes":      func(s *model.Settings) { s.DefaultMaxMinutes = -5 },
	}
	for name, mut := range bad {
		s := good
		mut(&s)
		var e map[string]string
		if code := sendJSON(t, "PUT", url, s, &e); code != 400 || e["error"] == "" {
			t.Errorf("%s: code %d err %q", name, code, e["error"])
		}
	}
	got, _ := app.store.Settings()
	if !reflect.DeepEqual(got, good) { // Settings holds a map now, so no ==
		t.Errorf("settings changed by rejected PUT: %+v", got)
	}
}

func TestCardSeededFromSettings(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	st := model.DefaultSettings()
	st.DefaultModel, st.DefaultEffort, st.DefaultPushDefault = "opus", "max", "push"
	st.DefaultRootFolder = dir
	if code := sendJSON(t, "PUT", app.baseURL+"/api/settings", st, nil); code != 200 {
		t.Fatalf("PUT = %d", code)
	}
	var c model.Card
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": "t", "description": "d"}, &c); code != 201 {
		t.Fatalf("POST = %d", code)
	}
	if c.Model != "opus" || c.Effort != "max" || c.PushDefault != "push" || c.RootFolder != dir {
		t.Errorf("card not seeded: %+v", c)
	}
	var got model.Settings
	sendJSON(t, "GET", app.baseURL+"/api/settings", nil, &got)
	if got.DefaultPushDefault != "push" {
		t.Errorf("push default round trip = %q", got.DefaultPushDefault)
	}
}

// The page, its stylesheets and the ES modules under /js/ are served with the right content types.
func TestStaticAssets(t *testing.T) {
	app := newTestApp(t, t.TempDir())
	for _, c := range []struct{ path, typ, has string }{
		{"/", "text/html", `<script type="module" src="js/main.js">`},
		{"/style.css", "text/css", ":root"},
		{"/js/main.js", "text/javascript", "import "},
		{"/js/api.js", "text/javascript", "export "},
	} {
		resp, err := http.Get(app.baseURL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d", c.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, c.typ) {
			t.Errorf("%s: content type %q, want %s", c.path, ct, c.typ)
		}
		if !strings.Contains(string(body), c.has) {
			t.Errorf("%s: body lacks %q", c.path, c.has)
		}
	}
}
