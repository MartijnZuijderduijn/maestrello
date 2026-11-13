package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"maestrello/internal/sandbox"
	"maestrello/internal/testenv"
)

func TestDiscoverRepos(t *testing.T) {
	dir := testenv.Dir(t)
	g := &Git{guard: &sandbox.Guard{}}
	for _, p := range []string{
		"a/.git", "group/b/.git", "group/b/nested/.git", // nested repo inside b is not listed
		"node_modules/x/.git", ".hidden/y/.git", "skipme/z/.git",
		"d1/d2/d3/d4/deep/.git", // beyond max depth
	} {
		os.MkdirAll(filepath.Join(dir, p), 0o755)
	}
	repos, err := g.DiscoverRepos(dir, filepath.Join(dir, "skipme"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(repos)
	if strings.Join(repos, ",") != "a,group/b" {
		t.Errorf("repos = %v", repos)
	}
}

func TestDiscoverReposRespectsSandbox(t *testing.T) {
	dir := testenv.Dir(t)
	guard, _ := sandbox.NewGuard(filepath.Join(dir, "inner"))
	g := &Git{guard: guard}
	if _, err := g.DiscoverRepos(dir, ""); err == nil {
		t.Error("discovery outside the sandbox should fail")
	}
}

func TestWorktreeFlow(t *testing.T) {
	dir := testenv.Dir(t)
	testenv.IsolateGit(t, dir)
	repo := testenv.MakeRepo(t, dir, "svc")
	guard, _ := sandbox.NewGuard(dir)
	g := &Git{guard: guard}
	ctx := context.Background()

	base, err := g.FetchBase(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if base != "origin/main" {
		t.Errorf("base = %q", base)
	}

	wt := filepath.Join(dir, "work", "1", "svc")
	if err := g.AddWorktree(ctx, repo, wt, "maestrello/1-x", base); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.AheadCount(ctx, wt, base); n != 0 {
		t.Errorf("ahead before changes = %d", n)
	}

	os.WriteFile(filepath.Join(wt, "new.txt"), []byte("hi\n"), 0o644)
	_, diff, err := g.Diff(ctx, wt, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "new.txt") {
		t.Errorf("uncommitted file missing from diff:\n%s", diff)
	}

	committed, err := g.CommitAll(ctx, wt, "add new.txt")
	if err != nil || !committed {
		t.Fatalf("CommitAll = %v, %v", committed, err)
	}
	if again, _ := g.CommitAll(ctx, wt, "nothing"); again {
		t.Error("CommitAll committed with a clean tree")
	}
	if n, _ := g.AheadCount(ctx, wt, base); n != 1 {
		t.Errorf("ahead after commit = %d", n)
	}
	log, _, _ := g.Diff(ctx, wt, base)
	if !strings.Contains(log, "add new.txt") {
		t.Errorf("log = %q", log)
	}

	if st, _ := g.Status(ctx, repo); st != "" {
		t.Errorf("original checkout changed: %q", st)
	}

	if err := g.Push(ctx, repo, "maestrello/1-x"); err != nil {
		t.Fatal(err)
	}
	testenv.Git(t, filepath.Join(dir, "origins", "svc.git"), "rev-parse", "--verify", "maestrello/1-x")

	if err := g.RemoveWorktree(ctx, repo, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree dir still exists")
	}
	if err := g.DeleteBranch(ctx, repo, "maestrello/1-x"); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxRefusesRealRemotes(t *testing.T) {
	dir := testenv.Dir(t)
	testenv.IsolateGit(t, dir)
	repo := testenv.MakeRepo(t, dir, "svc")
	testenv.Git(t, repo, "remote", "set-url", "origin", "https://example.invalid/svc.git")
	guard, _ := sandbox.NewGuard(dir)
	g := &Git{guard: guard}
	ctx := context.Background()

	if _, err := g.FetchBase(ctx, repo); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("FetchBase with remote origin = %v, want sandbox error", err)
	}
	if err := g.Push(ctx, repo, "main"); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("Push with remote origin = %v, want sandbox error", err)
	}
	url, err := g.CreatePR(ctx, repo, "b", "origin/main", "t", "body")
	if err != nil || !strings.HasPrefix(url, "sandbox://pr/") {
		t.Errorf("CreatePR in sandbox = %q, %v; want stub", url, err)
	}
}

func TestGitRefusesPathsOutsideSandbox(t *testing.T) {
	dir := testenv.Dir(t)
	guard, _ := sandbox.NewGuard(filepath.Join(dir, "inner"))
	g := &Git{guard: guard}
	if _, err := g.Status(context.Background(), dir); err == nil {
		t.Error("git ran outside the sandbox")
	}
}

func TestAddWorktreeExisting(t *testing.T) {
	dir := testenv.Dir(t)
	testenv.IsolateGit(t, dir)
	repo := testenv.MakeRepo(t, dir, "svc")
	guard, _ := sandbox.NewGuard(dir)
	g := &Git{guard: guard}
	ctx := context.Background()
	base, _ := g.FetchBase(ctx, repo)

	if g.BranchExists(ctx, repo, "maestrello/1-x") {
		t.Fatal("branch exists before creation")
	}
	wt := filepath.Join(dir, "work", "1", "svc")
	if err := g.AddWorktree(ctx, repo, wt, "maestrello/1-x", base); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, "new.txt"), []byte("hi\n"), 0o644)
	if _, err := g.CommitAll(ctx, wt, "add new.txt"); err != nil {
		t.Fatal(err)
	}
	if err := g.RemoveWorktree(ctx, repo, wt); err != nil {
		t.Fatal(err)
	}
	if !g.BranchExists(ctx, repo, "maestrello/1-x") {
		t.Fatal("branch lost with the worktree")
	}
	if err := g.AddWorktreeExisting(ctx, repo, wt, "maestrello/1-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "new.txt")); err != nil {
		t.Errorf("earlier commit missing: %v", err)
	}
}

func TestMergeHelpers(t *testing.T) {
	dir := testenv.Dir(t)
	testenv.IsolateGit(t, dir)
	repo := testenv.MakeRepo(t, dir, "svc")
	guard, _ := sandbox.NewGuard(dir)
	g := &Git{guard: guard}
	ctx := context.Background()

	if b, err := g.CurrentBranch(ctx, repo); err != nil || b != "main" {
		t.Fatalf("CurrentBranch = %q, %v", b, err)
	}
	if ok, err := g.TrackedClean(ctx, repo); err != nil || !ok {
		t.Fatalf("TrackedClean = %v, %v", ok, err)
	}
	os.WriteFile(filepath.Join(repo, "untracked.txt"), nil, 0o644)
	if ok, _ := g.TrackedClean(ctx, repo); !ok {
		t.Error("an untracked file made the checkout dirty")
	}
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("edit\n"), 0o644)
	if ok, _ := g.TrackedClean(ctx, repo); ok {
		t.Error("a modified tracked file left the checkout clean")
	}
	testenv.Git(t, repo, "checkout", "-q", "README.md")

	// Conflicting branches: the merge aborts and leaves everything as it was.
	testenv.Git(t, repo, "checkout", "-q", "-b", "side")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("side\n"), 0o644)
	testenv.Git(t, repo, "commit", "-qam", "side")
	testenv.Git(t, repo, "checkout", "-q", "main")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("main\n"), 0o644)
	testenv.Git(t, repo, "commit", "-qam", "main")
	head := testenv.Git(t, repo, "rev-parse", "HEAD")
	err := g.Merge(ctx, repo, "side", "merge side")
	if !errors.Is(err, ErrMergeConflict) || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("Merge = %v, want a conflict in README.md", err)
	}
	if testenv.Git(t, repo, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved")
	}
	if ok, _ := g.TrackedClean(ctx, repo); !ok {
		t.Error("conflicted merge left changes behind")
	}
}
