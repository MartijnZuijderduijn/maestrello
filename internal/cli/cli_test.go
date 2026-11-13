package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"maestrello/internal/board"
	"maestrello/internal/testenv"
)

// runningBoard serves a board sandboxed in dir on a free port, and stops it when the test ends.
// It returns the address and the data dir.
func runningBoard(t *testing.T, dir string) (addr, dataDir string) {
	t.Helper()
	dataDir = filepath.Join(dir, "data")
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- board.Serve(board.Options{Addr: "127.0.0.1:0", DataDir: dataDir, SandboxRoot: dir,
			ClaudeBin: testenv.FakeClaude(), OpencodeBin: testenv.FakeOpencode(), MistralBin: testenv.FakeMistral()}, func(url string) { ready <- url })
	}()
	select {
	case url := <-ready:
		addr = strings.TrimPrefix(url, "http://")
	case err := <-done:
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if resp, err := http.Post("http://"+addr+"/api/shutdown", "application/json", nil); err == nil {
			resp.Body.Close()
		}
		<-done
	})
	return addr, dataDir
}

func TestParseStart(t *testing.T) {
	t.Chdir(testenv.Root())
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
	if err := Run([]string{"frobnicate"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("err = %v", err)
	}
}

func TestParseStartOpencodeFlag(t *testing.T) {
	t.Chdir(testenv.Root())
	o, err := parseStart(nil, io.Discard)
	if err != nil || o.opencodeBin != "opencode" || o.claudeBin != "claude" {
		t.Fatalf("defaults: %+v, %v", o, err)
	}
	o, err = parseStart([]string{"-opencode", "testdata/fake-opencode.sh"}, io.Discard)
	if err != nil || !filepath.IsAbs(o.opencodeBin) || !strings.HasSuffix(o.opencodeBin, "testdata/fake-opencode.sh") {
		t.Errorf("relative path not made absolute: %+v, %v", o, err)
	}
}

func TestParseStartMistralFlag(t *testing.T) {
	t.Chdir(testenv.Root())
	o, err := parseStart(nil, io.Discard)
	if err != nil || o.mistralBin != "vibe" {
		t.Fatalf("defaults: %+v, %v", o, err)
	}
	o, err = parseStart([]string{"-mistral", "testdata/fake-mistral.sh"}, io.Discard)
	if err != nil || !filepath.IsAbs(o.mistralBin) || !strings.HasSuffix(o.mistralBin, "testdata/fake-mistral.sh") {
		t.Errorf("relative path not made absolute: %+v, %v", o, err)
	}
}

func TestBoardInfo(t *testing.T) {
	addr, dataDir := runningBoard(t, testenv.Dir(t))

	info, err := boardInfo(addr)
	if err != nil {
		t.Fatal(err)
	}
	if info.DataDir != dataDir {
		t.Errorf("data dir = %q, want %q", info.DataDir, dataDir)
	}

	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if _, err := boardInfo(strings.TrimPrefix(other.URL, "http://")); err == nil {
		t.Error("a non-maestrello server was taken for a board")
	}
}

func TestOpenAndStartReuseRunningBoard(t *testing.T) {
	dir := testenv.Dir(t)
	addr, dataDir := runningBoard(t, dir)
	same := []string{"-addr", addr, "-sandbox-root", dir, "-data", dataDir}

	var opened []string
	old := openBrowser
	openBrowser = func(url string) error { opened = append(opened, url); return nil }
	defer func() { openBrowser = old }()

	if err := Run([]string{"open", "-addr", addr}); err != nil {
		t.Fatal(err)
	}
	// start returns at once instead of serving a second board on the same address
	if err := Run(append([]string{"start"}, same...)); err != nil {
		t.Fatal(err)
	}
	// a board with other settings on that address is not silently reused
	for _, args := range [][]string{{"-addr", addr}, {"-addr", addr, "-sandbox-root", dir}} {
		if err := Run(append([]string{"start"}, args...)); err == nil || !strings.Contains(err.Error(), "stop that board first") {
			t.Errorf("start %v = %v, want a refusal", args, err)
		}
	}
	url := "http://" + addr
	if len(opened) != 2 || opened[0] != url || opened[1] != url {
		t.Errorf("opened = %v", opened)
	}
	if err := Run([]string{"status", "-addr", addr}); err != nil {
		t.Fatal(err)
	}

	if resp, err := http.Post(url+"/api/shutdown", "application/json", nil); err == nil {
		resp.Body.Close()
	}
}

func TestStopCommandStopsServe(t *testing.T) {
	dir := testenv.Dir(t)
	o := board.Options{Addr: "127.0.0.1:0", SandboxRoot: dir, DataDir: filepath.Join(dir, "data"), ClaudeBin: "claude"}
	addrc := make(chan string, 1)
	served := make(chan error, 1)
	go func() {
		served <- board.Serve(o, func(url string) { addrc <- strings.TrimPrefix(url, "http://") })
	}()
	var addr string
	select {
	case addr = <-addrc:
	case err := <-served:
		t.Fatal(err)
	}

	if err := Run([]string{"stop", "-addr", addr}); err != nil {
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
	if err := Run([]string{"stop", "-addr", addr}); err == nil || !strings.Contains(err.Error(), "no board running") {
		t.Errorf("second stop = %v", err)
	}
}
