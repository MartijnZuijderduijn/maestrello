package board

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"maestrello/internal/model"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// leases lists the lease files in the board's data dir.
func leases(t *testing.T, app *App) []*Lease {
	t.Helper()
	entries, _ := os.ReadDir(app.leaseDir())
	var out []*Lease
	for _, e := range entries {
		if l := readLease(filepath.Join(app.leaseDir(), e.Name())); l != nil {
			out = append(out, l)
		}
	}
	return out
}

func getCard(t *testing.T, app *App, id int64) *model.Card {
	t.Helper()
	c, err := app.store.GetCard(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// waitWaiting waits until the card sits in Running, queued behind holder.
func waitWaiting(t *testing.T, app *App, id, holder int64) {
	t.Helper()
	waitFor(t, "card to wait on #"+itoa(holder), func() bool {
		c := getCard(t, app, id)
		return c.Status == model.StatusRunning && c.WaitingOn == holder && app.isWaiting(id)
	})
	c := getCard(t, app, id)
	if app.liveRun(id) != nil {
		t.Fatalf("waiting card %d started a session", id)
	}
	if !strings.Contains(c.Activity, "waiting on card #"+itoa(holder)) {
		t.Errorf("activity = %q", c.Activity)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// answerOpen answers the card's open question.
func answerOpen(t *testing.T, app *App, id int64) {
	t.Helper()
	in := waitOpenInput(t, app, id)
	if err := app.Answer(id, in.ID, "blue", false); err != nil {
		t.Fatal(err)
	}
}

// holdRoot runs a card that edits the plain root and then blocks on a question,
// so its session (and lease) stays live until answered.
func holdRoot(t *testing.T, app *App, root string, repos ...string) *model.Card {
	t.Helper()
	c := readyCard(t, app, root, "LOCAL ASK", repos...)
	// Runs before the test server closes, which would otherwise wait on the open question.
	t.Cleanup(func() { app.Shutdown(10 * time.Second) })
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	waitOpenInput(t, app, c.ID)
	if app.liveRun(c.ID) == nil {
		t.Fatal("holder session not live")
	}
	return c
}

func TestLeaseQueuesSecondCard(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	first := holdRoot(t, app, root)
	ls := leases(t, app)
	if len(ls) != 1 || ls[0].CardID != first.ID || ls[0].Folder != leaseKey(root) || ls[0].SessionID == "" ||
		ls[0].PID != os.Getpid() || !ls[0].ExpiresAt.After(time.Now()) {
		t.Fatalf("leases = %+v", ls)
	}
	if insideDir(ls[0].file, root) {
		t.Fatal("lease file inside the leased folder")
	}

	second := readyCard(t, app, root, "LOCAL")
	if err := app.Run(second.ID); err != nil {
		t.Fatal(err)
	}
	waitWaiting(t, app, second.ID, first.ID)
	if repos := mustRepos(t, app, second.ID); len(repos) != 0 {
		t.Errorf("waiting card checked out %+v", repos)
	}

	answerOpen(t, app, first.ID)
	waitStatus(t, app, first.ID, model.StatusReview)
	c := waitStatus(t, app, second.ID, model.StatusReview)
	if c.WaitingOn != 0 || c.SessionID == "" {
		t.Errorf("second card after its run: %+v", c)
	}
	// Its review covers only its own time in the folder, which began after the first card's edits.
	r1, r2 := mustRepos(t, app, first.ID)[0], mustRepos(t, app, second.ID)[0]
	if r2.LocalSince <= r1.LocalSince {
		t.Errorf("second card's LocalSince %s not after first's %s", r2.LocalSince, r1.LocalSince)
	}
	if ls := leases(t, app); len(ls) != 0 {
		t.Errorf("leases left after both finished: %+v", ls)
	}
}

func TestLeaseQueueIsFIFO(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	first := holdRoot(t, app, root)
	second := readyCard(t, app, root, "LOCAL ASK")
	third := readyCard(t, app, root, "LOCAL")
	app.Run(second.ID)
	waitWaiting(t, app, second.ID, first.ID)
	app.Run(third.ID)
	waitWaiting(t, app, third.ID, first.ID)

	answerOpen(t, app, first.ID)
	waitOpenInput(t, app, second.ID) // second goes before third
	waitWaiting(t, app, third.ID, second.ID)
	if ls := leases(t, app); len(ls) != 1 || ls[0].CardID != second.ID {
		t.Fatalf("leases = %+v", ls)
	}

	answerOpen(t, app, second.ID)
	waitStatus(t, app, third.ID, model.StatusReview)
	if c := getCard(t, app, second.ID); c.Status != model.StatusReview {
		t.Errorf("second card %s", c.Status)
	}
}

func TestLeaseNestedFoldersConflict(t *testing.T) {
	t.Run("root blocks subfolder", func(t *testing.T) {
		dir, root := plainRoot(t)
		app := newTestApp(t, dir)
		first := holdRoot(t, app, root) // "." planned implicitly
		second := readyCard(t, app, root, "LOCAL", "src")
		app.Run(second.ID)
		waitWaiting(t, app, second.ID, first.ID)
		answerOpen(t, app, first.ID)
		waitStatus(t, app, second.ID, model.StatusReview)
	})
	t.Run("subfolder blocks root", func(t *testing.T) {
		dir, root := plainRoot(t)
		app := newTestApp(t, dir)
		first := holdRoot(t, app, root, "src")
		second := readyCard(t, app, root, "LOCAL")
		app.Run(second.ID)
		waitWaiting(t, app, second.ID, first.ID)
		answerOpen(t, app, first.ID)
		waitStatus(t, app, second.ID, model.StatusReview)
	})
	t.Run("siblings do not conflict", func(t *testing.T) {
		dir, root := plainRoot(t)
		os.MkdirAll(filepath.Join(root, "docs"), 0o755)
		app := newTestApp(t, dir)
		holdRoot(t, app, root, "src")
		second := readyCard(t, app, root, "LOCAL", "docs")
		app.Run(second.ID)
		waitStatus(t, app, second.ID, model.StatusReview)
	})
}

func TestStaleLeaseIsTakenOver(t *testing.T) {
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]Lease{
		"previous board, process gone": {Board: "gone", PID: dead.Process.Pid, ExpiresAt: time.Now().Add(time.Hour)},
		"expired":                      {Board: "gone", PID: os.Getppid(), ExpiresAt: time.Now().Add(-time.Second)},
		"card no longer running":       {CardID: -1, ExpiresAt: time.Now().Add(time.Hour)}, // board and card filled in below
	}
	for name, stale := range cases {
		t.Run(name, func(t *testing.T) {
			dir, root := plainRoot(t)
			app := newTestApp(t, dir)
			if stale.CardID == -1 {
				done := readyCard(t, app, root, "LOCAL")
				app.Run(done.ID)
				waitStatus(t, app, done.ID, model.StatusReview)
				app.leaseMu.Lock()
				stale.Board = app.boardIdentity()
				app.leaseMu.Unlock()
				stale.CardID, stale.PID = done.ID, os.Getpid()
			} else {
				stale.CardID = 999
			}
			stale.Folder, stale.file = leaseKey(root), app.leaseFile(leaseKey(root))
			os.MkdirAll(app.leaseDir(), 0o755)
			if err := writeLease(&stale, false); err != nil {
				t.Fatal(err)
			}

			c := readyCard(t, app, root, "LOCAL")
			app.Run(c.ID)
			waitStatus(t, app, c.ID, model.StatusReview)
			evs, _ := app.store.Events(c.ID, 1000)
			var took bool
			for _, e := range evs {
				took = took || strings.Contains(string(e.Data), "took over the lease")
			}
			if !took {
				t.Error("no note about taking over the lease")
			}
		})
	}
}

func TestRecoverDropsStaleLeases(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	os.MkdirAll(app.leaseDir(), 0o755)
	l := &Lease{Folder: root, CardID: 5, Board: "previous", PID: -1, ExpiresAt: time.Now().Add(time.Hour), file: app.leaseFile(root)}
	if err := writeLease(l, false); err != nil {
		t.Fatal(err)
	}
	if err := app.recover(); err != nil {
		t.Fatal(err)
	}
	if ls := leases(t, app); len(ls) != 0 {
		t.Errorf("leases after recover: %+v", ls)
	}
}

func TestHeartbeatKeepsLeaseAlive(t *testing.T) {
	for name, prompt := range map[string]string{"waiting on me": "LOCAL ASK", "streaming": "LOCAL LOOP"} {
		t.Run(name, func(t *testing.T) {
			dir, root := plainRoot(t)
			app := newTestApp(t, dir)
			app.leaseTTL = 300 * time.Millisecond
			first := readyCard(t, app, root, prompt)
			first.MaxTurns = 1000
			app.store.SaveCard(first)
			app.Run(first.ID)
			waitFor(t, "first session", func() bool { return app.liveRun(first.ID) != nil })
			second := readyCard(t, app, root, "LOCAL")
			app.Run(second.ID)
			waitWaiting(t, app, second.ID, first.ID)

			time.Sleep(5 * app.leaseTTL)
			if app.liveRun(first.ID) == nil {
				t.Fatal("holder session ended")
			}
			waitWaiting(t, app, second.ID, first.ID)
			if ls := leases(t, app); len(ls) != 1 || ls[0].CardID != first.ID || !ls[0].ExpiresAt.After(time.Now()) {
				t.Fatalf("leases = %+v", ls)
			}

			if prompt == "LOCAL ASK" {
				answerOpen(t, app, first.ID)
			} else if err := app.Stop(first.ID); err != nil {
				t.Fatal(err)
			}
			waitStatus(t, app, second.ID, model.StatusReview)
		})
	}
}

func TestHungHolderLosesLease(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	app.leaseTTL = 400 * time.Millisecond
	first := readyCard(t, app, root, "LOCAL HANG")
	app.Run(first.ID)
	waitFor(t, "first session", func() bool { return app.liveRun(first.ID) != nil })
	second := readyCard(t, app, root, "LOCAL")
	app.Run(second.ID)

	waitStatus(t, app, second.ID, model.StatusReview)
	c := waitStatus(t, app, first.ID, model.StatusFailed)
	if !strings.Contains(c.FailReason, "lease") {
		t.Errorf("hung holder fail reason = %q", c.FailReason)
	}
}

func TestWaitingCardCanBeCancelled(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	first := holdRoot(t, app, root)
	stopped := readyCard(t, app, root, "LOCAL")
	moved := readyCard(t, app, root, "LOCAL")
	discarded := readyCard(t, app, root, "LOCAL")
	last := readyCard(t, app, root, "LOCAL")
	for _, c := range []*model.Card{stopped, moved, discarded, last} {
		app.Run(c.ID)
		waitWaiting(t, app, c.ID, first.ID)
	}

	if err := app.Stop(stopped.ID); err != nil {
		t.Fatal(err)
	}
	if c := getCard(t, app, stopped.ID); c.Status != model.StatusFailed || c.WaitingOn != 0 || app.isWaiting(c.ID) {
		t.Errorf("stopped: %+v", c)
	}
	if err := app.Move(moved.ID, model.StatusReady); err != nil {
		t.Fatal(err)
	}
	if c := getCard(t, app, moved.ID); c.Status != model.StatusReady || c.WaitingOn != 0 || app.isWaiting(c.ID) {
		t.Errorf("moved: %+v", c)
	}
	if err := app.Discard(discarded.ID); err != nil {
		t.Fatal(err)
	}
	if c := getCard(t, app, discarded.ID); c.Status != model.StatusDone || c.WaitingOn != 0 || app.isWaiting(c.ID) {
		t.Errorf("discarded: %+v", c)
	}

	answerOpen(t, app, first.ID)
	waitStatus(t, app, last.ID, model.StatusReview)
	for _, id := range []int64{stopped.ID, moved.ID, discarded.ID} {
		if c := getCard(t, app, id); c.SessionID != "" || c.TurnsUsed != 0 || len(mustRepos(t, app, id)) != 0 {
			t.Errorf("cancelled card %d started anyway: %+v", id, c)
		}
	}
	// A card taken back to Ready runs normally later.
	if err := app.Run(moved.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, moved.ID, model.StatusReview)
}

func TestLeaseReleasedOnFinishAndFail(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	c := readyCard(t, app, root, "LOCAL")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	if ls := leases(t, app); len(ls) != 0 {
		t.Errorf("lease kept in review: %+v", ls)
	}

	f := readyCard(t, app, root, "LOCAL FAIL")
	app.Run(f.ID)
	waitStatus(t, app, f.ID, model.StatusFailed)
	if ls := leases(t, app); len(ls) != 0 {
		t.Errorf("lease kept after failure: %+v", ls)
	}

	// Sending it back takes the lease again for the resumed session, and queues if needed.
	holder := holdRoot(t, app, root)
	if err := app.SendBack(c.ID, "one more thing"); err != nil {
		t.Fatal(err)
	}
	waitWaiting(t, app, c.ID, holder.ID)
	answerOpen(t, app, holder.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	if ls := leases(t, app); len(ls) != 0 {
		t.Errorf("leases left: %+v", ls)
	}
	app.releaseLeases(c.ID) // idempotent
}

func TestAddRepoOnLeasedFolder(t *testing.T) {
	dir, root, app := repoFixture(t)
	notes := filepath.Join(root, "notes")
	os.MkdirAll(notes, 0o755)
	holder := holdRoot(t, app, root, "notes")

	other := newCard(t, app, root, "")
	text, isErr := app.callTool(nil, other.ID, "add_repo", json.RawMessage(`{"path":"notes"}`))
	if !isErr || !strings.Contains(text, "card #"+itoa(holder.ID)) || !strings.Contains(text, "later") {
		t.Errorf("add_repo on a leased folder = %v %q", isErr, text)
	}
	if repos := mustRepos(t, app, other.ID); len(repos) != 0 {
		t.Errorf("refused add_repo left repos %+v", repos)
	}
	// Overlapping: the root contains the leased folder.
	if _, isErr := app.callTool(nil, other.ID, "add_repo", json.RawMessage(`{"path":"."}`)); !isErr {
		t.Error("add_repo of the root while a folder inside it is leased succeeded")
	}
	_ = dir
}

func TestGitCardsTakeNoLease(t *testing.T) {
	_, root, app := repoFixture(t)
	os.MkdirAll(filepath.Join(root, "notes"), 0o755)
	holder := holdRoot(t, app, root, "notes")

	c := readyCard(t, app, root, "WRITE ADD:svc/b", "svc/a")
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, model.StatusReview)
	if c.WaitingOn != 0 {
		t.Errorf("git card waited on #%d", c.WaitingOn)
	}
	for _, l := range leases(t, app) {
		if l.CardID != holder.ID {
			t.Errorf("git card took a lease: %+v", l)
		}
	}
	answerOpen(t, app, holder.ID)
	waitStatus(t, app, holder.ID, model.StatusReview)
}

func TestShutdownEndsWaitingCards(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	first := holdRoot(t, app, root)
	second := readyCard(t, app, root, "LOCAL")
	app.Run(second.ID)
	waitWaiting(t, app, second.ID, first.ID)

	start := time.Now()
	app.Shutdown(10 * time.Second)
	if time.Since(start) > 8*time.Second {
		t.Error("shutdown waited for the timeout")
	}
	if c := getCard(t, app, second.ID); c.Status != model.StatusFailed || app.isWaiting(c.ID) || c.WaitingOn != 0 {
		t.Errorf("waiting card after shutdown: %+v", c)
	}
	if ls := leases(t, app); len(ls) != 0 {
		t.Errorf("leases after shutdown: %+v", ls)
	}
}
