package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStart(t *testing.T) {
	o, err := parseStart(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.addr != defaultAddr || !o.open || o.claudeBin != "claude" {
		t.Errorf("defaults = %+v", o)
	}

	o, err = parseStart([]string{"-open=false", "-sandbox-root", "testdata", "-claude", "testdata/fake-claude.sh"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.open || o.sandboxRoot != "testdata" {
		t.Errorf("flags not applied: %+v", o)
	}
	if !filepath.IsAbs(o.claudeBin) || !strings.HasSuffix(o.claudeBin, "testdata/fake-claude.sh") {
		t.Errorf("relative claude path not made absolute: %s", o.claudeBin)
	}

	if _, err := parseStart([]string{"stray"}, io.Discard); err == nil {
		t.Error("stray argument accepted")
	}
	if _, err := parseStart([]string{"-nope"}, io.Discard); err == nil {
		t.Error("unknown flag accepted")
	}
}

func TestRunCLIUnknownCommand(t *testing.T) {
	if err := runCLI([]string{"frobnicate"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("err = %v", err)
	}
}

func TestBoardInfo(t *testing.T) {
	app := newTestApp(t, testDir(t))
	srv := httptest.NewServer(app.routes())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	info, err := boardInfo(addr)
	if err != nil {
		t.Fatal(err)
	}
	if info.DataDir != app.dataDir {
		t.Errorf("data dir = %q, want %q", info.DataDir, app.dataDir)
	}

	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if _, err := boardInfo(strings.TrimPrefix(other.URL, "http://")); err == nil {
		t.Error("a non-maestrello server was taken for a board")
	}
}

func TestOpenAndStartReuseRunningBoard(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	app.dataDir, _ = filepath.Abs(app.dataDir)
	srv := httptest.NewServer(app.routes())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	same := []string{"-addr", addr, "-sandbox-root", dir, "-data", app.dataDir}

	var opened []string
	old := openBrowser
	openBrowser = func(url string) error { opened = append(opened, url); return nil }
	defer func() { openBrowser = old }()

	if err := runCLI([]string{"open", "-addr", addr}); err != nil {
		t.Fatal(err)
	}
	// start returns at once instead of serving a second board on the same address
	if err := runCLI(append([]string{"start"}, same...)); err != nil {
		t.Fatal(err)
	}
	// a board with other settings on that address is not silently reused
	for _, args := range [][]string{{"-addr", addr}, {"-addr", addr, "-sandbox-root", dir}} {
		if err := runCLI(append([]string{"start"}, args...)); err == nil || !strings.Contains(err.Error(), "stop that board first") {
			t.Errorf("start %v = %v, want a refusal", args, err)
		}
	}
	if len(opened) != 2 || opened[0] != srv.URL || opened[1] != srv.URL {
		t.Errorf("opened = %v", opened)
	}
	if err := runCLI([]string{"status", "-addr", addr}); err != nil {
		t.Fatal(err)
	}

	srv.Close()
	if err := runCLI([]string{"open", "-addr", addr}); err == nil || !strings.Contains(err.Error(), "no board running") {
		t.Errorf("open with no board = %v", err)
	}
}
