package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func getAccount(t *testing.T, app *App, query string) usageSnapshot {
	t.Helper()
	resp, err := http.Get(app.baseURL + "/api/account" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/account%s: status %d", query, resp.StatusCode)
	}
	var s usageSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAccountEndpoint(t *testing.T) {
	app := newTestApp(t, testDir(t))

	// Nothing is fetched per request: before a refresh the cache is empty.
	s := getAccount(t, app, "")
	if s.Account != nil || s.CheckedAt != nil || s.Limits == nil || len(s.Limits) != 0 {
		t.Fatalf("unchecked snapshot = %+v", s)
	}

	s = getAccount(t, app, "?refresh=1")
	want := Account{LoggedIn: true, Email: "test@maestrello.invalid", OrgName: "Fake Org",
		AuthMethod: "claude.ai", APIProvider: "firstParty", SubscriptionType: "max"}
	if s.Account == nil || *s.Account != want || s.AccountError != "" || s.CheckedAt == nil {
		t.Fatalf("snapshot = %+v (account %+v)", s, s.Account)
	}
	// The cached result is served without ?refresh.
	if s2 := getAccount(t, app, ""); s2.Account == nil || s2.Account.Email != want.Email {
		t.Errorf("cached snapshot = %+v", s2)
	}
}

func TestAccountLoggedOut(t *testing.T) {
	app := newTestApp(t, testDir(t))
	t.Setenv("FAKE_CLAUDE_AUTH", "out")
	s := getAccount(t, app, "?refresh=1")
	if s.Account == nil || s.Account.LoggedIn || s.Account.Email != "" || s.AccountError != "" {
		t.Fatalf("snapshot = %+v (account %+v)", s, s.Account)
	}
}

func TestAccountBrokenOrMissingCLI(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	broken := filepath.Join(dir, "broken-claude.sh")
	if err := os.WriteFile(broken, []byte("#!/usr/bin/env bash\necho 'error: unknown command auth' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage-claude.sh")
	if err := os.WriteFile(garbage, []byte("#!/usr/bin/env bash\necho 'You are logged in.'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, bin := range map[string]string{
		"broken":  broken,
		"garbage": garbage,
		"missing": filepath.Join(dir, "no-such-claude"),
	} {
		app.claudeBin = bin
		s := getAccount(t, app, "?refresh=1")
		if s.Account != nil || s.AccountError == "" || s.CheckedAt == nil {
			t.Errorf("%s CLI: snapshot = %+v", name, s)
		}
		if name == "missing" && !strings.Contains(s.AccountError, "no-such-claude") {
			t.Errorf("missing CLI error does not name it: %q", s.AccountError)
		}
	}
	// A failed refresh replaces an earlier good account: the header must not
	// keep showing someone the CLI no longer reports.
	app.claudeBin, _ = filepath.Abs("testdata/fake-claude.sh")
	if s := getAccount(t, app, "?refresh=1"); s.Account == nil {
		t.Fatalf("fake CLI: %+v", s)
	}
	app.claudeBin = broken
	if s := getAccount(t, app, "?refresh=1"); s.Account != nil || s.AccountError == "" {
		t.Errorf("stale account after failure: %+v", s)
	}
}

func TestRateLimitEventRecorded(t *testing.T) {
	app := newTestApp(t, testDir(t))
	sub := app.hub.Subscribe()
	defer app.hub.Unsubscribe(sub)

	before := time.Now().Add(-time.Second)
	c := readyCard(t, app, app.dataDir, "RATEOK")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	waitIdle(t, app, c.ID)
	if in, _ := app.store.OpenInput(c.ID); in != nil {
		t.Errorf("open input %+v for an allowed request", in)
	}

	s := getAccount(t, app, "")
	if len(s.Limits) != 2 {
		t.Fatalf("limits = %+v", s.Limits)
	}
	five, week := s.Limits[0], s.Limits[1]
	if five.Type != "five_hour" || five.Status != "allowed" || five.OverageStatus != "rejected" ||
		five.Utilization == nil || *five.Utilization != 0.42 || five.ResetsAt < time.Now().Unix() || five.ObservedAt.Before(before) {
		t.Errorf("five_hour = %+v", five)
	}
	if week.Type != "seven_day" || week.Status != "allowed_warning" || week.Utilization == nil || *week.Utilization != 0.81 ||
		week.SurpassedThreshold == nil || week.ResetsAt <= five.ResetsAt {
		t.Errorf("seven_day = %+v", week)
	}

	// A usage event reached open boards.
	got := false
	for drained := false; !drained && !got; {
		select {
		case msg := <-sub:
			got = strings.HasPrefix(string(msg), "event: usage\n") && strings.Contains(string(msg), `"seven_day"`)
		default:
			drained = true
		}
	}
	if !got {
		t.Error("no usage event published")
	}

	// The snapshot survives a restart through the settings table, outside Settings.
	app2 := &App{store: app.store, hub: NewHub()}
	app2.loadUsage()
	if l := app2.usageSnapshot().Limits; len(l) != 2 || l[0].Type != "five_hour" || *l[1].Utilization != 0.81 {
		t.Errorf("restored limits = %+v", l)
	}
	if _, err := app.store.Settings(); err != nil {
		t.Errorf("settings after saving usage: %v", err)
	}
}

func TestRecordRateLimitUnifiedWindows(t *testing.T) {
	app := newTestApp(t, testDir(t))
	app.recordRateLimit([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1900000000,
		"unifiedWindows":{"five_hour":{"utilization":1.02,"resetsAt":1900000000,"status":"rejected"},"seven_day":{"utilization":0.3,"resetsAt":1900500000}}}}`))
	app.recordRateLimit([]byte(`{"type":"rate_limit_event","rate_limit_info":"nonsense"}`))
	l := app.usageSnapshot().Limits
	if len(l) != 2 || l[0].Type != "five_hour" || l[0].Status != "rejected" || *l[0].Utilization != 1.02 ||
		l[1].Type != "seven_day" || l[1].ResetsAt != 1900500000 || *l[1].Utilization != 0.3 {
		t.Errorf("limits = %+v", l)
	}
}

func TestParseAccount(t *testing.T) {
	cases := map[string]*Account{
		`{"loggedIn":true,"email":"a@b.invalid","authMethod":"claude.ai","planTier":"pro","extra":{"x":1}}`: {LoggedIn: true, Email: "a@b.invalid", AuthMethod: "claude.ai", SubscriptionType: "pro"},
		`{"loggedIn":false,"email":"a@b.invalid"}`:                                                          {LoggedIn: false},
		`{"loggedIn":true,"email":42}`:                                                                      {LoggedIn: true},
		`{"something":"else"}`:                                                                              nil,
		`Logged in as a@b.invalid`:                                                                          nil,
		``:                                                                                                  nil,
	}
	for in, want := range cases {
		got, ok := parseAccount([]byte(in))
		if (want == nil) != !ok || (want != nil && *got != *want) {
			t.Errorf("parseAccount(%q) = %+v, %v; want %+v", in, got, ok, want)
		}
	}
}
