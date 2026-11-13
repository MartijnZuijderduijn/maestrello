package board

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"maestrello/internal/testenv"
)

func fsGet(t *testing.T, app *App, query, host string) (int, fsListing) {
	t.Helper()
	req, _ := http.NewRequest("GET", app.baseURL+"/api/fs/list?"+query, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var l fsListing
	json.NewDecoder(resp.Body).Decode(&l)
	return resp.StatusCode, l
}

func names(l fsListing) []string {
	var n []string
	for _, e := range l.Entries {
		n = append(n, e.Name+":"+e.Type)
	}
	return n
}

func fsFixture(t *testing.T) (*App, string) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	root := app.guard.Root()
	os.MkdirAll(filepath.Join(root, "b-dir"), 0o755)
	os.MkdirAll(filepath.Join(root, "z-dir"), 0o755)
	os.MkdirAll(filepath.Join(root, "a-repo", ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, ".hid"), 0o755)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, ".dot"), []byte("x"), 0o644)
	return app, root
}

func TestFSListFixture(t *testing.T) {
	app := newTestApp(t, testenv.Root()) // sandbox = project folder, so the fixture is inside
	root := filepath.Join(testenv.Testdata(), "project")
	code, l := fsGet(t, app, "path="+root, "")
	if code != 200 || len(l.Entries) != 2 || l.Entries[0].Name != "libs" || l.Entries[1].Name != "services" {
		t.Fatalf("code %d listing %+v", code, l)
	}
	if l.Parent == "" {
		t.Fatal("parent should be set inside sandbox")
	}
}

func TestFSListOrderAndHidden(t *testing.T) {
	app, root := fsFixture(t)
	_, l := fsGet(t, app, "files=1&path="+root, "")
	// the board's data dir is never listed
	want := []string{"a-repo:dir", "b-dir:dir", "z-dir:dir", "a.txt:file"}
	got := names(l)
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if !l.Entries[0].IsRepo || l.Entries[1].IsRepo {
		t.Fatalf("is_repo wrong: %+v", l.Entries)
	}
	_, l = fsGet(t, app, "files=1&hidden=1&path="+root, "")
	if got := names(l); len(got) != 6 || got[0] != ".hid:dir" || got[len(got)-1] != "a.txt:file" {
		t.Fatalf("hidden listing %v", got)
	}
}

func TestFSListFilesOff(t *testing.T) {
	app, root := fsFixture(t)
	_, l := fsGet(t, app, "files=0&path="+root, "")
	for _, e := range l.Entries {
		if e.Type == "file" {
			t.Fatalf("file listed: %v", names(l))
		}
	}
}

func TestFSListRejects(t *testing.T) {
	app, root := fsFixture(t)
	if code, _ := fsGet(t, app, "path=/", ""); code != 400 {
		t.Errorf("outside sandbox = %d", code)
	}
	if code, _ := fsGet(t, app, "path=rel/dir", ""); code != 400 {
		t.Errorf("relative = %d", code)
	}
	if code, _ := fsGet(t, app, "path="+filepath.Join(root, "a.txt"), ""); code != 400 {
		t.Errorf("file path = %d", code)
	}
	if code, _ := fsGet(t, app, "path="+filepath.Join(root, "nope"), ""); code != 400 {
		t.Errorf("missing = %d", code)
	}
	if code, _ := fsGet(t, app, "path="+app.dataDir, ""); code != 400 {
		t.Errorf("data dir = %d", code)
	}
}

func TestFSListSymlinkOutside(t *testing.T) {
	app, root := fsFixture(t)
	outside := testenv.Dir(t) // a sibling scratch dir, outside this app's sandbox
	os.Symlink(outside, filepath.Join(root, "escape"))
	os.Symlink(filepath.Join(root, "b-dir"), filepath.Join(root, "inner"))
	_, l := fsGet(t, app, "path="+root, "")
	for _, e := range l.Entries {
		if e.Name == "escape" {
			t.Fatalf("escaping symlink listed")
		}
	}
	found := false
	for _, e := range l.Entries {
		found = found || e.Name == "inner"
	}
	if !found {
		t.Fatalf("inner symlink missing: %v", names(l))
	}
}

func TestFSListParent(t *testing.T) {
	app, root := fsFixture(t)
	code, l := fsGet(t, app, "path="+root, "")
	if code != 200 || l.Parent != "" {
		t.Fatalf("root parent = %q", l.Parent)
	}
	_, l = fsGet(t, app, "path="+filepath.Join(root, "b-dir"), "")
	if l.Parent != root {
		t.Fatalf("parent = %q want %q", l.Parent, root)
	}
}

func TestFSListDefaultStart(t *testing.T) {
	app, root := fsFixture(t)
	code, l := fsGet(t, app, "", "")
	if code != 200 || l.Path != root {
		t.Fatalf("code %d path %q want %q", code, l.Path, root)
	}
}

func TestFSListHost(t *testing.T) {
	app, root := fsFixture(t)
	for _, h := range []string{"evil.example.com", "evil.example.com:80", "localhost.evil.com", "127.0.0.2"} {
		if code, _ := fsGet(t, app, "path="+root, h); code != 403 {
			t.Errorf("host %q = %d, want 403", h, code)
		}
	}
	for _, h := range []string{"localhost", "localhost:8080", "127.0.0.1:1234", "[::1]", "[::1]:8080"} {
		if code, _ := fsGet(t, app, "path="+root, h); code != 200 {
			t.Errorf("host %q = %d, want 200", h, code)
		}
	}
}

func TestFSListTruncated(t *testing.T) {
	app, root := fsFixture(t)
	big := filepath.Join(root, "big")
	os.MkdirAll(big, 0o755)
	for i := 0; i < fsListCap+5; i++ {
		os.Mkdir(filepath.Join(big, fmt.Sprintf("d%d", i)), 0o755)
	}
	_, l := fsGet(t, app, "path="+big, "")
	if !l.Truncated || len(l.Entries) != fsListCap {
		t.Fatalf("truncated=%v len=%d", l.Truncated, len(l.Entries))
	}
}
