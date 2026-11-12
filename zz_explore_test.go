package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func postMove(t *testing.T, app *App, id int64, to string) {
	resp, err := http.Post(app.baseURL+"/api/cards/"+fmt.Sprint(id)+"/move", "application/json", bytes.NewBufferString(`{"status":"`+to+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("status %d body %s", resp.StatusCode, b)
	if resp.StatusCode != 200 {
		t.Errorf("move failed")
	}
}

func TestZZGit(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE", "svc/a")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	postMove(t, app, c.ID, StatusDone)
}
func TestZZNoChange(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "nothing", "svc/a")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	postMove(t, app, c.ID, StatusDone)
}
func TestZZLocal(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	c := readyCard(t, app, root, "LOCAL")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	postMove(t, app, c.ID, StatusDone)
}
func TestZZMixed(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE ADD:.", "svc/a")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	postMove(t, app, c.ID, StatusDone)
}
func TestZZSentBack(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE", "svc/a")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	if err := app.SendBack(c.ID, "more WRITE"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, StatusReview)
	postMove(t, app, c.ID, StatusDone)
}
