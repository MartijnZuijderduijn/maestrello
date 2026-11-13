package board

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"maestrello/internal/gitops"
	"maestrello/internal/model"
	"maestrello/internal/sandbox"
	"maestrello/internal/store"
	"maestrello/internal/testenv"
)

func newTestApp(t *testing.T, dir string) *App {
	t.Helper()
	guard, err := sandbox.NewGuard(dir)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "data")
	os.MkdirAll(filepath.Join(dataDir, "work"), 0o755)
	store, err := store.OpenStore(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	fake := testenv.FakeClaude()
	fakeOC := testenv.FakeOpencode()
	fakeMV := testenv.FakeMistral()
	fakeOmp := testenv.FakeOmp()
	app := &App{
		store: store, hub: NewHub(), guard: guard, git: gitops.New(guard),
		dataDir: dataDir, claudeBin: fake, opencodeBin: fakeOC, mistralBin: fakeMV, ompBin: fakeOmp,
		runs: map[int64]*Run{}, waiters: map[int64]chan inputReply{}, quit: make(chan struct{}),
	}
	srv := httptest.NewServer(app.routes())
	t.Cleanup(srv.Close)
	app.baseURL = srv.URL
	return app
}

func newCard(t *testing.T, app *App, root, desc string) *model.Card {
	t.Helper()
	c := &model.Card{Title: "Test card", Description: desc, RootFolder: root, Model: "fake", Effort: "low",
		PermissionMode: "auto", MaxTurns: 50, MaxMinutes: 10}
	if err := app.store.CreateCard(c); err != nil {
		t.Fatal(err)
	}
	return c
}

// waitStatus polls until the card reaches status, failing the test after a timeout.
func waitStatus(t *testing.T, app *App, id int64, status string) *model.Card {
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
func waitOpenInput(t *testing.T, app *App, id int64) *model.Input {
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
