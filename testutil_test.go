package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testDir creates a scratch directory inside testdata/, never in the system temp dir:
// tests must not touch anything outside the project folder.
func testDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("testdata", "test-")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(abs) })
	return abs
}

// isolateGit keeps the user's global and system git config out of tests.
func isolateGit(t *testing.T, dir string) {
	t.Helper()
	cfg := filepath.Join(dir, "gitconfig")
	os.WriteFile(cfg, []byte("[user]\n\tname = maestrello-test\n\temail = test@maestrello.invalid\n[init]\n\tdefaultBranch = main\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// makeRepo creates <dir>/origins/<name>.git (bare) and clones it to <dir>/project/<name>.
func makeRepo(t *testing.T, dir, name string) string {
	t.Helper()
	seed := filepath.Join(dir, "seed", name)
	os.MkdirAll(seed, 0o755)
	gitT(t, seed, "init", "-q")
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("# "+name+"\n"), 0o644)
	gitT(t, seed, "add", "-A")
	gitT(t, seed, "commit", "-qm", "initial")
	origin := filepath.Join(dir, "origins", name+".git")
	gitT(t, dir, "clone", "-q", "--bare", seed, origin)
	clone := filepath.Join(dir, "project", name)
	gitT(t, dir, "clone", "-q", origin, clone)
	return clone
}

func newTestApp(t *testing.T, dir string) *App {
	t.Helper()
	guard, err := NewGuard(dir)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "data")
	os.MkdirAll(filepath.Join(dataDir, "work"), 0o755)
	store, err := OpenStore(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	fake, _ := filepath.Abs("testdata/fake-claude.sh")
	fakeOC, _ := filepath.Abs("testdata/fake-opencode.sh")
	app := &App{
		store: store, hub: NewHub(), guard: guard, git: &Git{guard: guard},
		dataDir: dataDir, claudeBin: fake, opencodeBin: fakeOC,
		runs: map[int64]*Run{}, waiters: map[int64]chan inputReply{}, quit: make(chan struct{}),
	}
	srv := httptest.NewServer(app.routes())
	t.Cleanup(srv.Close)
	app.baseURL = srv.URL
	return app
}

func newCard(t *testing.T, app *App, root, desc string) *Card {
	t.Helper()
	c := &Card{Title: "Test card", Description: desc, RootFolder: root, Model: "fake", Effort: "low",
		PermissionMode: "auto", MaxTurns: 50, MaxMinutes: 10}
	if err := app.store.CreateCard(c); err != nil {
		t.Fatal(err)
	}
	return c
}

// waitStatus polls until the card reaches status, failing the test after a timeout.
func waitStatus(t *testing.T, app *App, id int64, status string) *Card {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, err := app.store.GetCard(id)
		if err != nil {
			t.Fatal(err)
		}
		if c.Status == status && app.liveRun(id) == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("card %d: status %q (fail: %q), want %q", id, c.Status, c.FailReason, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitOpenInput polls until the card has an unanswered input.
func waitOpenInput(t *testing.T, app *App, id int64) *Input {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if in, _ := app.store.OpenInput(id); in != nil {
			return in
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("card %d: no open input", id)
	return nil
}
