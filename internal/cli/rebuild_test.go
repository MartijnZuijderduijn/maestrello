package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"maestrello/internal/testenv"
)

func TestSourceDirIsThisModule(t *testing.T) {
	if got, want := sourceDir(), testenv.Root(); got != want {
		t.Errorf("sourceDir = %q, want %q", got, want)
	}
}

func TestStaleSource(t *testing.T) {
	src := testenv.Dir(t)
	exe := filepath.Join(src, "bin")
	old := time.Now().Add(-time.Hour)
	for _, f := range []string{"cmd/maestrello/main.go", "frontend/index.html", "internal/x/x_test.go", "testdata/x.go", ".claude/a.go", "README.md", "bin"} {
		p := filepath.Join(src, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, old, old)
	}
	if stale := staleSource(src, exe); len(stale) != 0 {
		t.Fatalf("fresh build reported stale: %v", stale)
	}

	now := time.Now()
	for _, f := range []string{"cmd/maestrello/main.go", "frontend/index.html", "internal/x/x_test.go", "testdata/x.go", ".claude/a.go", "README.md"} {
		os.Chtimes(filepath.Join(src, f), now, now)
	}
	stale := staleSource(src, exe)
	sort.Strings(stale)
	if strings.Join(stale, ",") != filepath.Join("cmd", "maestrello", "main.go")+","+filepath.Join("frontend", "index.html") {
		t.Errorf("stale = %v, want only the built sources", stale)
	}
}

func TestRestartTakesStartFlags(t *testing.T) {
	o, err := parseStart([]string{"-src", "/somewhere", "-addr", "127.0.0.1:9"}, os.Stderr)
	if err != nil || o.src != "/somewhere" {
		t.Errorf("parseStart -src = %+v, %v", o, err)
	}
}
