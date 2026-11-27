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

	info, err := g.Bases(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	base, ok := info.Auto()
	if !ok || base != "origin/main" {
		t.Errorf("base = %q (auto %v)", base, ok)
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

	// Never fetched, and not fatal: the refusal is the fetch error to show me.
	if info, err := g.Bases(ctx, repo); err != nil || !strings.Contains(info.FetchErr, "sandbox") {
		t.Errorf("Bases with remote origin = %+v, %v, want a sandbox fetch error", info, err)
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
	base := "origin/main"

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

func TestBases(t *testing.T) {
	dir := testenv.Dir(t)
	testenv.IsolateGit(t, dir)
	repo := testenv.MakeRepo(t, dir, "svc")
	guard, _ := sandbox.NewGuard(dir)
	g := &Git{guard: guard}
	ctx := context.Background()

	// An unpushed local commit: local is ahead, so there is no automatic choice.
	testenv.Git(t, repo, "commit", "-q", "--allow-empty", "-m", "unpushed")
	info, err := g.Bases(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := info.Auto(); ok || info.Local != "main" || info.Origin != "origin/main" || info.Ahead != 1 || info.Behind != 0 || info.FetchErr != "" {
		t.Errorf("ahead: %+v", info)
	}

	// A commit on origin as well: diverged.
	other := filepath.Join(dir, "other")
	testenv.Git(t, dir, "clone", "-q", filepath.Join(dir, "origins", "svc.git"), other)
	testenv.Git(t, other, "commit", "-q", "--allow-empty", "-m", "upstream")
	testenv.Git(t, other, "push", "-q", "origin", "main")
	if info, _ = g.Bases(ctx, repo); info.Ahead != 1 || info.Behind != 1 {
		t.Errorf("diverged: %+v", info)
	}

	// A failing fetch is recorded, not returned, and keeps the local candidate.
	testenv.Git(t, repo, "remote", "set-url", "origin", filepath.Join(dir, "origins", "missing.git"))
	info, err = g.Bases(ctx, repo)
	if err != nil || info.FetchErr == "" || info.Local != "main" {
		t.Errorf("failed fetch: %+v, %v", info, err)
	}
	if _, ok := info.Auto(); ok {
		t.Error("a failed fetch must not be automatic")
	}
}

func TestBasesNoOrigin(t *testing.T) {
	dir := testenv.Dir(t)
	testenv.IsolateGit(t, dir)
	repo := filepath.Join(dir, "solo")
	os.MkdirAll(repo, 0o755)
	testenv.Git(t, repo, "init", "-q")
	testenv.Git(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	guard, _ := sandbox.NewGuard(dir)
	info, err := (&Git{guard: guard}).Bases(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if ref, ok := info.Auto(); !ok || ref != "main" {
		t.Errorf("auto = %q, %v", ref, ok)
	}
}

// mergeFixture: main has a.txt, b.txt, c.txt; branch feat modifies a.txt, deletes c.txt and adds d.txt.
func mergeFixture(t *testing.T) (*Git, string) {
	t.Helper()
	dir := testenv.Dir(t)
	testenv.IsolateGit(t, dir)
	repo := testenv.MakeRepo(t, dir, "svc")
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		os.WriteFile(filepath.Join(repo, f), []byte("base\n"), 0o644)
	}
	testenv.Git(t, repo, "add", "-A")
	testenv.Git(t, repo, "commit", "-qm", "files")
	testenv.Git(t, repo, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("feat\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "d.txt"), []byte("new\n"), 0o644)
	testenv.Git(t, repo, "rm", "-q", "c.txt")
	testenv.Git(t, repo, "add", "-A")
	testenv.Git(t, repo, "commit", "-qm", "feat")
	testenv.Git(t, repo, "checkout", "-q", "main")
	guard, _ := sandbox.NewGuard(dir)
	return &Git{guard: guard}, repo
}

func TestMergeNoCheckoutSyncsUntouchedFiles(t *testing.T) {
	g, repo := mergeFixture(t)
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("mine\n"), 0o644) // the merge does not touch b.txt
	if err := g.MergeNoCheckout(context.Background(), repo, "main", "feat", "msg"); err != nil {
		t.Fatal(err)
	}
	if got := testenv.Git(t, repo, "rev-parse", "main"); got != testenv.Git(t, repo, "rev-parse", "feat") {
		t.Error("not a fast-forward")
	}
	for f, want := range map[string]string{"a.txt": "feat\n", "b.txt": "mine\n", "d.txt": "new\n"} {
		if b, _ := os.ReadFile(filepath.Join(repo, f)); string(b) != want {
			t.Errorf("%s = %q, want %q", f, b, want)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "c.txt")); err == nil {
		t.Error("deleted file still in the working tree")
	}
	if st := testenv.Git(t, repo, "status", "--porcelain"); st != "M b.txt" {
		t.Errorf("status = %q", st)
	}
}

func TestMergeNoCheckoutRefusesTrackedOverlap(t *testing.T) {
	for _, f := range []string{"a.txt", "c.txt"} { // modified and deleted by the branch
		g, repo := mergeFixture(t)
		os.WriteFile(filepath.Join(repo, f), []byte("mine\n"), 0o644)
		main := testenv.Git(t, repo, "rev-parse", "main")
		err := g.MergeNoCheckout(context.Background(), repo, "main", "feat", "msg")
		var overlap *DirtyOverlapError
		if !errors.As(err, &overlap) || len(overlap.Files) != 1 || overlap.Files[0] != f {
			t.Fatalf("%s: err = %v", f, err)
		}
		if testenv.Git(t, repo, "rev-parse", "main") != main {
			t.Errorf("%s: main moved", f)
		}
	}
}

func TestMergeNoCheckoutRefusesBaseCheckedOutElsewhere(t *testing.T) {
	g, repo := mergeFixture(t)
	testenv.Git(t, repo, "checkout", "-q", "feat")
	wt := filepath.Join(filepath.Dir(repo), "other-wt")
	testenv.Git(t, repo, "worktree", "add", "-q", wt, "main")
	err := g.MergeNoCheckout(context.Background(), repo, "main", "feat", "msg")
	if err == nil || !strings.Contains(err.Error(), "checked out in") {
		t.Errorf("err = %v", err)
	}
}

func TestStashRestoreConflictKeepsStash(t *testing.T) {
	g, repo := mergeFixture(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("mine\n"), 0o644)
	sha, err := g.StashPush(ctx, repo, "test")
	if err != nil || sha == "" {
		t.Fatalf("StashPush = %q, %v", sha, err)
	}
	if sha2, _ := g.StashPush(ctx, repo, "nothing"); sha2 != "" {
		t.Error("stashed a clean tree")
	}
	if err := g.Merge(ctx, repo, "feat", "msg"); err != nil {
		t.Fatal(err)
	}
	if err := g.StashRestore(ctx, repo, sha); err == nil {
		t.Fatal("restore did not conflict")
	}
	if g.StashRef(ctx, repo, sha) != "stash@{0}" {
		t.Error("stash was dropped")
	}
	if st := testenv.Git(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("not clean: %q", st)
	}
}
