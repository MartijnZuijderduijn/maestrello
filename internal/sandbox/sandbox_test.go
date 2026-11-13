package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuard(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "sandbox")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "repo"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	g, err := NewGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	ok := []string{root, filepath.Join(root, "repo"), filepath.Join(root, "not", "yet", "created")}
	for _, p := range ok {
		if err := g.Check(p); err != nil {
			t.Errorf("Check(%q) = %v, want nil", p, err)
		}
	}
	bad := []string{
		outside,
		filepath.Join(root, "..", "outside"),
		filepath.Join(root, "escape"),
		filepath.Join(root, "escape", "new"),
		root + "-sibling",
	}
	for _, p := range bad {
		if err := g.Check(p); err == nil {
			t.Errorf("Check(%q) = nil, want error", p)
		}
	}

	for _, u := range []string{"https://github.com/x/y.git", "git@github.com:x/y.git", "ssh://host/repo", outside} {
		if err := g.CheckRemoteURL(u); err == nil {
			t.Errorf("CheckRemoteURL(%q) = nil, want error", u)
		}
	}
	if err := g.CheckRemoteURL(filepath.Join(root, "origins", "a.git")); err != nil {
		t.Errorf("local remote rejected: %v", err)
	}

	off, _ := NewGuard("")
	if err := off.Check(outside); err != nil {
		t.Errorf("disabled guard rejected path: %v", err)
	}
}
