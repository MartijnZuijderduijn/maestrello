package board

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/testenv"
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

// entryOf is one harness's entry of a snapshot; the test fails if it is missing.
func entryOf(t *testing.T, s usageSnapshot, name string) harnessUsage {
	t.Helper()
	for _, e := range s.Harnesses {
		if e.Harness == name {
			return e
		}
	}
	t.Fatalf("no %q entry in %+v", name, s)
	return harnessUsage{}
}

// getClaude is the claude entry of GET /api/account<query>.
func getClaude(t *testing.T, app *App, query string) harnessUsage {
	t.Helper()
	return entryOf(t, getAccount(t, app, query), "claude")
}

// usageTestAdapter is a claude adapter (on the fake CLI) under another name. With
// caps false it reports neither account nor usage, like a harness that cannot.
type usageTestAdapter struct {
	harness.Harness
	name string
	caps bool
}

func (a usageTestAdapter) Name() string { return a.name }
func (a usageTestAdapter) Caps() harness.Caps {
	c := a.Harness.Caps()
	c.Account, c.Usage = a.caps, a.caps
	return c
}
func (a usageTestAdapter) Account(ctx context.Context) (*harness.Account, error) {
	if !a.caps {
		return nil, harness.ErrNoAccount
	}
	return a.Harness.Account(ctx)
}
func (a usageTestAdapter) ParseUsage(line []byte) []harness.UsageLimit {
	if !a.caps {
		return nil
	}
	return a.Harness.ParseUsage(line)
}

// registerUsageAdapter adds a usageTestAdapter to the registry for the test.
func registerUsageAdapter(t *testing.T, name string, caps bool) {
	t.Helper()
	unregister := harness.Register(name, func(harness.HarnessConfig) harness.Harness {
		h, _ := harness.Build("claude", map[string]string{"claude": testenv.FakeClaude()})
		return usageTestAdapter{Harness: h, name: name, caps: caps}
	})
	t.Cleanup(unregister)
}

const rateLine = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour","resetsAt":1900000000,"utilization":0.6}}`

// streamFor feeds stream lines to readStream as the run of a card of the given harness.
func streamFor(t *testing.T, app *App, harnessName string, lines ...string) {
	t.Helper()
	c := newCard(t, app, app.dataDir, "usage")
	c.Harness = harnessName
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	h, err := app.harness(harnessName)
	if err != nil {
		t.Fatal(err)
	}
	app.readStream(c.ID, &Run{cardID: c.ID, turns: map[string]bool{}}, h, strings.NewReader(strings.Join(lines, "\n")+"\n"))
}

func TestAccountEndpoint(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))

	// Nothing is fetched per request: before a refresh the cache is empty. There is
	// one entry per registered harness, sorted by name like GET /api/harnesses.
	s := getAccount(t, app, "")
	var names []string
	for _, e := range s.Harnesses {
		names = append(names, e.Harness)
	}
	if !slices.Equal(names, harness.Names()) || !slices.IsSorted(names) {
		t.Fatalf("entries %v, want the registered harnesses %v", names, harness.Names())
	}
	c := entryOf(t, s, "claude")
	if c.Account != nil || c.CheckedAt != nil || c.Limits == nil || len(c.Limits) != 0 || !c.AccountSupported || !c.UsageSupported {
		t.Fatalf("unchecked snapshot = %+v", c)
	}

	c = getClaude(t, app, "?refresh=1")
	want := harness.Account{LoggedIn: true, Email: "test@maestrello.invalid", OrgName: "Fake Org",
		AuthMethod: "claude.ai", APIProvider: "firstParty", SubscriptionType: "max"}
	if c.Account == nil || *c.Account != want || c.AccountError != "" || c.CheckedAt == nil {
		t.Fatalf("snapshot = %+v (account %+v)", c, c.Account)
	}
	// The cached result is served without ?refresh.
	if c2 := getClaude(t, app, ""); c2.Account == nil || c2.Account.Email != want.Email {
		t.Errorf("cached snapshot = %+v", c2)
	}
	// opencode has no account lookup: it stays unknown, without an error.
	after := getAccount(t, app, "?refresh=1")
	if e := entryOf(t, after, "opencode"); e.Account != nil || e.AccountError != "" || e.CheckedAt != nil || e.AccountSupported || e.UsageSupported || len(e.Limits) != 0 {
		t.Errorf("opencode entry = %+v", e)
	}
	// mistral reports an account from its home dir (see TestMistralAccountEntry) but no usage.
	if e := entryOf(t, after, "mistral"); !e.AccountSupported || e.UsageSupported || e.AccountError != "" || e.Account == nil || len(e.Limits) != 0 {
		t.Errorf("mistral entry = %+v", e)
	}
	// omp reports an account from `omp usage --json` (the fake lists one) but no usage.
	if e := entryOf(t, after, "omp"); !e.AccountSupported || e.UsageSupported || e.AccountError != "" || e.Account == nil || !e.Account.LoggedIn || len(e.Limits) != 0 {
		t.Errorf("omp entry = %+v", e)
	}
}

func TestMistralAccountEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIBE_HOME", home)
	t.Setenv("MISTRAL_API_KEY", "")
	os.WriteFile(filepath.Join(home, ".env"), []byte("MISTRAL_API_KEY=sk-secret-xyz\n"), 0o600)
	app := newTestApp(t, testenv.Dir(t))
	e := entryOf(t, getAccount(t, app, "?harness=mistral&refresh=1"), "mistral")
	if !e.AccountSupported || e.UsageSupported || e.Account == nil || !e.Account.LoggedIn || e.Account.APIProvider != "mistral" || e.CheckedAt == nil {
		t.Fatalf("mistral entry = %+v", e)
	}
	b, _ := json.Marshal(getAccount(t, app, ""))
	if strings.Contains(string(b), "secret") {
		t.Errorf("key in the API response: %s", b)
	}
}

func TestAccountHarnessParam(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	c := getClaude(t, app, "?harness=claude&refresh=1")
	if c.Account == nil || c.Account.Email != "test@maestrello.invalid" || c.CheckedAt == nil {
		t.Fatalf("?harness=claude&refresh=1: %+v", c)
	}
	// The harness filter only picks what is refreshed: every harness is still listed.
	if s := getAccount(t, app, "?harness=claude"); len(s.Harnesses) != len(harness.Names()) {
		t.Errorf("?harness=claude lists %d entries", len(s.Harnesses))
	}
	// Refreshing one harness leaves the others alone.
	registerUsageAdapter(t, "other", true)
	s := getAccount(t, app, "?harness=other&refresh=1")
	if entryOf(t, s, "other").Account == nil {
		t.Errorf("other not refreshed: %+v", s)
	}
	t.Setenv("FAKE_CLAUDE_AUTH", "out")
	s = getAccount(t, app, "?harness=other&refresh=1")
	if a := entryOf(t, s, "claude").Account; a == nil || !a.LoggedIn {
		t.Errorf("claude was refreshed by ?harness=other: %+v", a)
	}
	if a := entryOf(t, s, "other").Account; a == nil || a.LoggedIn {
		t.Errorf("other not logged out: %+v", a)
	}

	for _, q := range []string{"?harness=nope", "?harness=nope&refresh=1"} {
		resp, err := http.Get(app.baseURL + "/api/account" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("GET /api/account%s = %d, want 400", q, resp.StatusCode)
		}
	}
}

func TestAccountLoggedOut(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	t.Setenv("FAKE_CLAUDE_AUTH", "out")
	c := getClaude(t, app, "?refresh=1")
	if c.Account == nil || c.Account.LoggedIn || c.Account.Email != "" || c.AccountError != "" {
		t.Fatalf("snapshot = %+v (account %+v)", c, c.Account)
	}
}

func TestAccountBrokenOrMissingCLI(t *testing.T) {
	dir := testenv.Dir(t)
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
		c := getClaude(t, app, "?refresh=1")
		if c.Account != nil || c.AccountError == "" || c.CheckedAt == nil {
			t.Errorf("%s CLI: snapshot = %+v", name, c)
		}
		if name == "missing" && !strings.Contains(c.AccountError, "no-such-claude") {
			t.Errorf("missing CLI error does not name it: %q", c.AccountError)
		}
	}
	// A failed refresh replaces an earlier good account: the header must not
	// keep showing someone the CLI no longer reports.
	app.claudeBin = testenv.FakeClaude()
	if c := getClaude(t, app, "?refresh=1"); c.Account == nil {
		t.Fatalf("fake CLI: %+v", c)
	}
	app.claudeBin = broken
	if c := getClaude(t, app, "?refresh=1"); c.Account != nil || c.AccountError == "" {
		t.Errorf("stale account after failure: %+v", c)
	}
}

func TestRefreshAccountsAtStartup(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	registerUsageAdapter(t, "nocaps", false)
	registerUsageAdapter(t, "other", true)
	app.refreshAccounts() // what serve runs in the background
	s := getAccount(t, app, "")
	if c := entryOf(t, s, "claude"); c.Account == nil || c.CheckedAt == nil {
		t.Errorf("claude not refreshed: %+v", c)
	}
	if c := entryOf(t, s, "other"); c.Account == nil || c.CheckedAt == nil {
		t.Errorf("other not refreshed: %+v", c)
	}
	for _, name := range []string{"nocaps", "opencode"} {
		if c := entryOf(t, s, name); c.Account != nil || c.CheckedAt != nil || c.AccountError != "" {
			t.Errorf("%s was refreshed without the capability: %+v", name, c)
		}
	}
	if c := entryOf(t, s, "mistral"); c.Account == nil || c.CheckedAt == nil {
		t.Errorf("mistral not refreshed: %+v", c)
	}
	if c := entryOf(t, s, "omp"); c.Account == nil || c.CheckedAt == nil {
		t.Errorf("omp not refreshed: %+v", c)
	}
}

func TestRateLimitEventRecorded(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	sub := app.hub.Subscribe()
	defer app.hub.Unsubscribe(sub)

	before := time.Now().Add(-time.Second)
	c := readyCard(t, app, app.dataDir, "RATEOK")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)
	if in, _ := app.store.OpenInput(c.ID); in != nil {
		t.Errorf("open input %+v for an allowed request", in)
	}

	s := getAccount(t, app, "")
	limits := entryOf(t, s, "claude").Limits
	if len(limits) != 2 {
		t.Fatalf("limits = %+v", limits)
	}
	five, week := limits[0], limits[1]
	if five.Type != "five_hour" || five.Status != "allowed" || five.OverageStatus != "rejected" ||
		five.Utilization == nil || *five.Utilization != 0.42 || five.ResetsAt < time.Now().Unix() || five.ObservedAt.Before(before) {
		t.Errorf("five_hour = %+v", five)
	}
	if week.Type != "seven_day" || week.Status != "allowed_warning" || week.Utilization == nil || *week.Utilization != 0.81 ||
		week.SurpassedThreshold == nil || week.ResetsAt <= five.ResetsAt {
		t.Errorf("seven_day = %+v", week)
	}
	for _, name := range []string{"opencode", "mistral", "omp"} {
		if l := entryOf(t, s, name).Limits; len(l) != 0 {
			t.Errorf("%s limits = %+v", name, l)
		}
	}

	// A usage event reached open boards: the whole snapshot, claude's limits in it.
	got := false
	for drained := false; !drained && !got; {
		select {
		case msg := <-sub:
			got = strings.HasPrefix(string(msg), "event: usage\n") && strings.Contains(string(msg), `"harnesses"`) && strings.Contains(string(msg), `"seven_day"`)
		default:
			drained = true
		}
	}
	if !got {
		t.Error("no usage event published")
	}

	// The snapshot survives a restart through the settings table, outside Settings,
	// as an object by harness name.
	raw, _ := app.store.GetKV(usageSettingKey)
	if !strings.HasPrefix(raw, `{"claude":[`) {
		t.Errorf("stored usage_limits = %q", raw)
	}
	app2 := &App{store: app.store, hub: NewHub()}
	app2.loadUsage()
	if l := entryOf(t, app2.usageSnapshot(), "claude").Limits; len(l) != 2 || l[0].Type != "five_hour" || *l[1].Utilization != 0.81 {
		t.Errorf("restored limits = %+v", l)
	}
	if _, err := app.store.Settings(); err != nil {
		t.Errorf("settings after saving usage: %v", err)
	}
}

func TestUsageStoredPerHarness(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	registerUsageAdapter(t, "other", true)

	// Limits go under the harness of the card whose run produced them.
	streamFor(t, app, "other", rateLine)
	s := getAccount(t, app, "")
	if l := entryOf(t, s, "other").Limits; len(l) != 1 || l[0].Type != "five_hour" || *l[0].Utilization != 0.6 {
		t.Errorf("other limits = %+v", l)
	}
	if l := entryOf(t, s, "claude").Limits; len(l) != 0 {
		t.Errorf("claude limits = %+v, want none from another harness's run", l)
	}
	streamFor(t, app, "claude", strings.Replace(rateLine, `"five_hour"`, `"seven_day"`, 1))
	s = getAccount(t, app, "")
	if l := entryOf(t, s, "claude").Limits; len(l) != 1 || l[0].Type != "seven_day" {
		t.Errorf("claude limits = %+v", l)
	}
	if l := entryOf(t, s, "other").Limits; len(l) != 1 || l[0].Type != "five_hour" {
		t.Errorf("other limits changed: %+v", l)
	}

	// The account is kept per harness as well.
	getAccount(t, app, "?harness=other&refresh=1")
	if s = getAccount(t, app, ""); entryOf(t, s, "other").Account == nil || entryOf(t, s, "claude").Account != nil {
		t.Errorf("accounts = %+v", s)
	}

	// Both harnesses' limits are stored together and restored after a restart.
	raw, _ := app.store.GetKV(usageSettingKey)
	var stored map[string][]harness.UsageLimit
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || len(stored["claude"]) != 1 || len(stored["other"]) != 1 {
		t.Fatalf("stored usage_limits = %q (%v)", raw, err)
	}
	app2 := &App{store: app.store, hub: NewHub()}
	app2.loadUsage()
	s = app2.usageSnapshot()
	if l := entryOf(t, s, "other").Limits; len(l) != 1 || l[0].Type != "five_hour" {
		t.Errorf("restored other limits = %+v", l)
	}
	if l := entryOf(t, s, "claude").Limits; len(l) != 1 || l[0].Type != "seven_day" {
		t.Errorf("restored claude limits = %+v", l)
	}
}

func TestLoadUsageLegacyArray(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	// Before limits were kept per harness, usage_limits was a plain array of claude's limits.
	legacy := `[{"type":"seven_day","status":"allowed_warning","utilization":0.81,"observed_at":"2026-01-02T03:04:05Z"},
		{"type":"five_hour","status":"allowed","utilization":0.42,"resets_at":1900000000,"observed_at":"2026-01-02T03:04:05Z"}]`
	if err := app.store.SetKV(usageSettingKey, legacy); err != nil {
		t.Fatal(err)
	}
	app.loadUsage()
	s := app.usageSnapshot()
	l := entryOf(t, s, "claude").Limits
	if len(l) != 2 || l[0].Type != "five_hour" || *l[0].Utilization != 0.42 || l[0].ResetsAt != 1900000000 || l[1].Type != "seven_day" || l[1].Status != "allowed_warning" {
		t.Errorf("legacy limits = %+v", l)
	}
	if o := entryOf(t, s, "opencode").Limits; len(o) != 0 {
		t.Errorf("opencode limits = %+v", o)
	}
	// Garbage is ignored, and the next event saves the new format over the old.
	app.recordUsage("claude", []harness.UsageLimit{{Type: "five_hour", Status: "rejected"}})
	if raw, _ := app.store.GetKV(usageSettingKey); !strings.HasPrefix(raw, `{"claude":[`) {
		t.Errorf("not converted: %q", raw)
	}
	l = entryOf(t, app.usageSnapshot(), "claude").Limits
	if l[0].Status != "rejected" || *l[0].Utilization != 0.42 {
		t.Errorf("update did not merge into the restored limit: %+v", l[0])
	}
	app.store.SetKV(usageSettingKey, "not json")
	app3 := &App{store: app.store, hub: NewHub()}
	app3.loadUsage()
	if l := entryOf(t, app3.usageSnapshot(), "claude").Limits; len(l) != 0 {
		t.Errorf("limits from garbage: %+v", l)
	}
}

func TestHarnessWithoutAccountOrUsage(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	registerUsageAdapter(t, "nocaps", false)

	// Neither a refresh nor a stream makes it fail or record anything.
	streamFor(t, app, "nocaps", rateLine)
	streamFor(t, app, "mistral", rateLine) // a built-in adapter without usage events
	streamFor(t, app, "omp", rateLine)
	app.recordUsage("claude", nil)
	for _, q := range []string{"", "?refresh=1", "?harness=nocaps&refresh=1"} {
		e := entryOf(t, getAccount(t, app, q), "nocaps")
		if e.Account != nil || e.AccountError != "" || e.CheckedAt != nil || len(e.Limits) != 0 || e.AccountSupported || e.UsageSupported || e.Limits == nil {
			t.Errorf("nocaps entry for %q = %+v", q, e)
		}
	}
	if e := entryOf(t, getAccount(t, app, "?harness=mistral&refresh=1"), "mistral"); e.UsageSupported || e.AccountError != "" || len(e.Limits) != 0 {
		t.Errorf("mistral entry = %+v", e)
	}
	if e := entryOf(t, getAccount(t, app, "?harness=omp&refresh=1"), "omp"); e.UsageSupported || e.AccountError != "" || len(e.Limits) != 0 {
		t.Errorf("omp entry = %+v", e)
	}
	if raw, _ := app.store.GetKV(usageSettingKey); raw != "" {
		t.Errorf("usage_limits written for a harness without usage: %q", raw)
	}
}

func TestRecordUsageMergesWindows(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	h, _ := harness.For("claude")
	for _, line := range []string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1900000000,
			"unifiedWindows":{"five_hour":{"utilization":1.02,"resetsAt":1900000000,"status":"rejected"},"seven_day":{"utilization":0.3,"resetsAt":1900500000}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":"nonsense"}`,
	} {
		app.recordUsage("claude", h.ParseUsage([]byte(line)))
	}
	l := getClaude(t, app, "").Limits
	if len(l) != 2 || l[0].Type != "five_hour" || l[0].Status != "rejected" || *l[0].Utilization != 1.02 ||
		l[1].Type != "seven_day" || l[1].ResetsAt != 1900500000 || *l[1].Utilization != 0.3 {
		t.Errorf("limits = %+v", l)
	}
}
