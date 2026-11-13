// Package testenv is the environment the tests run in: the module's testdata folder with its
// fixtures (fake agent CLIs, scratch dirs, git repos). Tests keep to it and never touch anything
// outside the project folder, so it is the sandbox the tests run in (see internal/sandbox).
package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Root is the module's root folder: the nearest folder holding go.mod above this file.
func Root() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("testenv: cannot locate the module root")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("testenv: no go.mod above " + file)
		}
		dir = parent
	}
}

// Testdata is the folder with the fixtures and the scratch directories of the tests.
func Testdata() string { return filepath.Join(Root(), "testdata") }

// FakeClaude is the stand-in for the claude CLI.
func FakeClaude() string { return filepath.Join(Testdata(), "fake-claude.sh") }

// FakeOpencode is the stand-in for the opencode CLI.
func FakeOpencode() string { return filepath.Join(Testdata(), "fake-opencode.sh") }

// FakeMistral is the stand-in for the Mistral Vibe CLI.
func FakeMistral() string { return filepath.Join(Testdata(), "fake-mistral.sh") }

// FakeOmp is the stand-in for the omp CLI.
func FakeOmp() string { return filepath.Join(Testdata(), "fake-omp.sh") }

// Dir creates a scratch directory inside testdata/, never in the system temp dir:
// tests must not touch anything outside the project folder.
func Dir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(Testdata(), "test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// IsolateGit keeps the user's global and system git config out of tests.
func IsolateGit(t *testing.T, dir string) {
	t.Helper()
	cfg := filepath.Join(dir, "gitconfig")
	os.WriteFile(cfg, []byte("[user]\n\tname = maestrello-test\n\temail = test@maestrello.invalid\n[init]\n\tdefaultBranch = main\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// Git runs git in dir and returns its trimmed output; a failure fails the test.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// MakeRepo creates <dir>/origins/<name>.git (bare) and clones it to <dir>/project/<name>.
func MakeRepo(t *testing.T, dir, name string) string {
	t.Helper()
	seed := filepath.Join(dir, "seed", name)
	os.MkdirAll(seed, 0o755)
	Git(t, seed, "init", "-q")
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("# "+name+"\n"), 0o644)
	Git(t, seed, "add", "-A")
	Git(t, seed, "commit", "-qm", "initial")
	origin := filepath.Join(dir, "origins", name+".git")
	Git(t, dir, "clone", "-q", "--bare", seed, origin)
	clone := filepath.Join(dir, "project", name)
	Git(t, dir, "clone", "-q", origin, clone)
	return clone
}
