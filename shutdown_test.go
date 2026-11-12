package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShutdownEndsSessions(t *testing.T) {
	app := newTestApp(t, testDir(t))
	looping := readyCard(t, app, app.dataDir, "LOOP")
	asking := readyCard(t, app, app.dataDir, "ASK")
	app.Run(looping.ID)
	app.Run(asking.ID)
	waitOpenInput(t, app, asking.ID)
	for app.liveRun(looping.ID) == nil {
	}

	app.Shutdown(10 * time.Second)
	if app.liveRun(looping.ID) != nil || app.liveRun(asking.ID) != nil {
		t.Fatal("sessions still live after shutdown")
	}
	c, _ := app.store.GetCard(looping.ID)
	if c.Status != StatusFailed || !strings.Contains(c.FailReason, "board stopped") {
		t.Errorf("looping card: %s %q", c.Status, c.FailReason)
	}
	c, _ = app.store.GetCard(asking.ID)
	if in, _ := app.store.OpenInput(asking.ID); c.Status != StatusNeedsInput || in == nil {
		t.Errorf("asking card: %s, open input %v; the question should survive", c.Status, in)
	}
}

func TestStopCommandStopsServe(t *testing.T) {
	dir := testDir(t)
	o := &startOpts{addr: "127.0.0.1:0", sandboxRoot: dir, dataDir: filepath.Join(dir, "data"), claudeBin: "claude"}
	addrc := make(chan string, 1)
	served := make(chan error, 1)
	go func() {
		served <- serve(o, func(url string) { addrc <- strings.TrimPrefix(url, "http://") })
	}()
	var addr string
	select {
	case addr = <-addrc:
	case err := <-served:
		t.Fatal(err)
	}

	if err := runCLI([]string{"stop", "-addr", addr}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("serve returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after stop")
	}
	if err := runCLI([]string{"stop", "-addr", addr}); err == nil || !strings.Contains(err.Error(), "no board running") {
		t.Errorf("second stop = %v", err)
	}
}
