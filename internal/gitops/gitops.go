package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"maestrello/internal/sandbox"
)

// Git runs git and gh against repos, checking every path with the guard first.
type Git struct {
	guard *sandbox.Guard
}

// New returns a Git that checks every path with guard.
func New(guard *sandbox.Guard) *Git { return &Git{guard: guard} }

func (g *Git) run(ctx context.Context, dir string, args ...string) (string, error) {
	out, _, err := g.runCode(ctx, dir, args...)
	return out, err
}

// runCode is run that also reports git's exit code (-1 when git did not run or was killed),
// for commands whose exit status carries an answer (merge-tree: 1 = conflicts).
func (g *Git) runCode(ctx context.Context, dir string, args ...string) (string, int, error) {
	if err := g.guard.Check(dir); err != nil {
		return "", -1, err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return strings.TrimRight(out.String(), "\n"), code, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), 0, nil
}

var SkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true,
	".venv": true, "venv": true, "__pycache__": true, ".idea": true,
}

// DiscoverRepos returns the git repos under root as paths relative to root.
// It does not descend into repos, and skips the board's own data dir.
func (g *Git) DiscoverRepos(root, skip string) ([]string, error) {
	if err := g.guard.Check(root); err != nil {
		return nil, err
	}
	const maxDepth = 4 // repos at most this many path segments below root
	var repos []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && (SkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") || path == skip) {
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			rel, _ := filepath.Rel(root, path)
			repos = append(repos, rel)
			return filepath.SkipDir
		}
		if rel, _ := filepath.Rel(root, path); rel != "." && strings.Count(rel, string(filepath.Separator))+1 >= maxDepth {
			return filepath.SkipDir
		}
		return nil
	})
	return repos, err
}

func (g *Git) IsRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// OriginURL returns the origin remote URL, or "" when there is none.
func (g *Git) OriginURL(ctx context.Context, repo string) string {
	url, err := g.run(ctx, repo, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return url
}

// FetchBase fetches origin and returns the ref to branch from (e.g. "origin/main").
// Repos without an origin branch from their local HEAD.
func (g *Git) FetchBase(ctx context.Context, repo string) (string, error) {
	url := g.OriginURL(ctx, repo)
	if url == "" {
		head, err := g.run(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return "", err
		}
		return head, nil
	}
	if err := g.guard.CheckRemoteURL(url); err != nil {
		return "", err
	}
	if _, err := g.run(ctx, repo, "fetch", "--quiet", "origin"); err != nil {
		return "", err
	}
	if ref, err := g.run(ctx, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return ref, nil
	}
	for _, cand := range []string{"origin/main", "origin/master"} {
		if _, err := g.run(ctx, repo, "rev-parse", "--verify", "--quiet", cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("cannot determine default branch of %s", repo)
}

func (g *Git) AddWorktree(ctx context.Context, repo, path, branch, base string) error {
	if err := g.guard.Check(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := g.run(ctx, repo, "worktree", "add", "--quiet", "-b", branch, path, base)
	return err
}

// BranchExists reports whether a local branch exists in repo.
func (g *Git) BranchExists(ctx context.Context, repo, branch string) bool {
	_, err := g.run(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// AddWorktreeExisting checks out an existing branch in a new worktree at path.
func (g *Git) AddWorktreeExisting(ctx context.Context, repo, path, branch string) error {
	if err := g.guard.Check(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := g.run(ctx, repo, "worktree", "add", "--quiet", path, branch)
	return err
}

func (g *Git) RemoveWorktree(ctx context.Context, repo, path string) error {
	if err := g.guard.Check(path); err != nil {
		return err
	}
	_, err := g.run(ctx, repo, "worktree", "remove", "--force", path)
	return err
}

func (g *Git) DeleteBranch(ctx context.Context, repo, branch string) error {
	_, err := g.run(ctx, repo, "branch", "-D", branch)
	return err
}

func (g *Git) Status(ctx context.Context, dir string) (string, error) {
	return g.run(ctx, dir, "status", "--porcelain")
}

// CommitAll commits any uncommitted work in a worktree. Returns false if there was nothing to commit.
func (g *Git) CommitAll(ctx context.Context, dir, msg string) (bool, error) {
	st, err := g.Status(ctx, dir)
	if err != nil || st == "" {
		return false, err
	}
	if _, err := g.run(ctx, dir, "add", "-A"); err != nil {
		return false, err
	}
	_, err = g.run(ctx, dir, "commit", "--quiet", "-m", msg)
	return err == nil, err
}

func (g *Git) AheadCount(ctx context.Context, dir, base string) (int, error) {
	out, err := g.run(ctx, dir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Diff shows committed changes since base plus anything uncommitted in the worktree.
func (g *Git) Diff(ctx context.Context, dir, base string) (log, diff string, err error) {
	log, err = g.run(ctx, dir, "log", "--oneline", base+"..HEAD")
	if err != nil {
		return "", "", err
	}
	if _, err := g.run(ctx, dir, "add", "-A", "--intent-to-add"); err != nil {
		return "", "", err
	}
	diff, err = g.run(ctx, dir, "diff", "--stat", "--patch", base)
	return log, diff, err
}

// Push pushes branch from the original repo to origin.
func (g *Git) Push(ctx context.Context, repo, branch string) error {
	url := g.OriginURL(ctx, repo)
	if url == "" {
		return fmt.Errorf("%s has no origin remote", repo)
	}
	if err := g.guard.CheckRemoteURL(url); err != nil {
		return err
	}
	_, err := g.run(ctx, repo, "push", "--quiet", "-u", "origin", branch)
	return err
}

// CreatePR opens a pull request with gh. In sandbox mode it is a stub.
func (g *Git) CreatePR(ctx context.Context, repo, branch, base, title, body string) (string, error) {
	if err := g.guard.Check(repo); err != nil {
		return "", err
	}
	base = strings.TrimPrefix(base, "origin/")
	if g.guard.Enabled() {
		return "sandbox://pr/" + branch + "?base=" + base, nil
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "create", "--head", branch, "--base", base, "--title", title, "--body", body)
	cmd.Dir = repo
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh pr create: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// CurrentBranch returns the branch checked out in repo, or "" on a detached HEAD.
func (g *Git) CurrentBranch(ctx context.Context, repo string) (string, error) {
	out, err := g.run(ctx, repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if _, herr := g.run(ctx, repo, "rev-parse", "--verify", "--quiet", "HEAD"); herr == nil {
			return "", nil // detached
		}
		return "", err
	}
	return out, nil
}

// TrackedClean reports whether repo has no uncommitted changes to tracked files
// (untracked files do not count).
func (g *Git) TrackedClean(ctx context.Context, repo string) (bool, error) {
	out, err := g.run(ctx, repo, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// ErrMergeConflict is returned by Merge when the merge conflicted and was aborted.
var ErrMergeConflict = errors.New("merge conflict")

// Merge merges branch into the branch checked out in repo: a fast-forward when possible,
// otherwise a merge commit with msg. On a conflict the merge is aborted, leaving repo as
// it was, and the error wraps ErrMergeConflict with the conflicting files.
func (g *Git) Merge(ctx context.Context, repo, branch, msg string) error {
	_, err := g.run(ctx, repo, "merge", "--quiet", "--ff", "-m", msg, branch)
	if err == nil {
		return nil
	}
	if _, perr := g.run(ctx, repo, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); perr != nil {
		return err // failed before a merge started
	}
	files, _ := g.run(ctx, repo, "diff", "--name-only", "--diff-filter=U")
	if _, aerr := g.run(ctx, repo, "merge", "--abort"); aerr != nil {
		return fmt.Errorf("merge conflict, and aborting failed: %w", aerr)
	}
	return fmt.Errorf("%w in %s", ErrMergeConflict, strings.Join(strings.Fields(files), ", "))
}

// DirtyOverlapError is returned by MergeNoCheckout when the merge would change files that have
// uncommitted changes (or untracked files in the way) in the checkout. Nothing was changed.
type DirtyOverlapError struct{ Files []string }

func (e *DirtyOverlapError) Error() string {
	return "the merge touches files you changed in the checkout: " + strings.Join(e.Files, ", ")
}

// FileChange is one path a merge changes: Status is A (added), M (modified) or D (deleted).
type FileChange struct{ Status, Path string }

// changedFiles lists what going from commit a to commit b changes.
func (g *Git) changedFiles(ctx context.Context, repo, a, b string) ([]FileChange, error) {
	out, err := g.run(ctx, repo, "diff", "--name-status", "-z", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	f := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	var changes []FileChange
	for i := 0; i+1 < len(f); i += 2 {
		changes = append(changes, FileChange{f[i][:1], f[i+1]})
	}
	return changes, nil
}

// dirtyFiles lists the tracked files whose index or working tree differs from HEAD.
func (g *Git) dirtyFiles(ctx context.Context, repo string) (map[string]bool, error) {
	out, err := g.run(ctx, repo, "diff", "--name-only", "-z", "--no-renames", "HEAD")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			set[p] = true
		}
	}
	return set, nil
}

// checkedOutAt returns where branch is checked out, or "" when nowhere.
func (g *Git) checkedOutAt(ctx context.Context, repo, branch string) (string, error) {
	out, err := g.run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	var path string
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(l, "worktree "); ok {
			path = p
		} else if l == "branch refs/heads/"+branch {
			return path, nil
		}
	}
	return "", nil
}

func (g *Git) isAncestor(ctx context.Context, repo, a, b string) bool {
	_, err := g.run(ctx, repo, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// MergeNoCheckout merges branch into the local branch base without running a merge in the user's
// checkout: it builds the result with merge-tree (a fast-forward when possible, else a commit with
// msg), then moves the ref of base. When repo has base checked out, the files the merge changed
// are then updated in the index and working tree, so they match a normal merge; if the merge
// touches a file with uncommitted changes (or an untracked file is in the way) nothing is done and
// a *DirtyOverlapError says which. A conflict gives ErrMergeConflict. Without base checked out
// only the ref moves, unless another worktree has it checked out (refused).
func (g *Git) MergeNoCheckout(ctx context.Context, repo, base, branch, msg string) error {
	cur, err := g.CurrentBranch(ctx, repo)
	if err != nil {
		return err
	}
	onBase := cur == base
	if !onBase {
		if at, err := g.checkedOutAt(ctx, repo, base); err != nil {
			return err
		} else if at != "" {
			return fmt.Errorf("%s is checked out in %s, which cannot be updated without touching it", base, at)
		}
	}
	old, err := g.run(ctx, repo, "rev-parse", "--verify", "refs/heads/"+base)
	if err != nil {
		return err
	}
	tip, err := g.run(ctx, repo, "rev-parse", "--verify", branch)
	if err != nil {
		return err
	}
	if g.isAncestor(ctx, repo, tip, old) {
		return nil // already merged
	}
	next := tip
	if !g.isAncestor(ctx, repo, old, tip) {
		if next, err = g.mergeCommit(ctx, repo, old, tip, msg); err != nil {
			return err
		}
	}
	changes, err := g.changedFiles(ctx, repo, old, next)
	if err != nil {
		return err
	}
	if onBase {
		if err := g.checkOverlap(ctx, repo, changes); err != nil {
			return err
		}
	}
	ref := "refs/heads/" + base
	if _, err := g.run(ctx, repo, "update-ref", "-m", "maestrello: merge "+branch, ref, next, old); err != nil {
		return err
	}
	if !onBase {
		return nil
	}
	if err := g.syncPaths(ctx, repo, next, changes); err != nil {
		g.run(ctx, repo, "update-ref", ref, old, next)
		g.syncPaths(ctx, repo, old, reverse(changes))
		return fmt.Errorf("updating the checkout failed, the merge was rolled back: %w", err)
	}
	return nil
}

// mergeCommit builds the merge of tip into old with merge-tree and commits it, without moving any ref.
func (g *Git) mergeCommit(ctx context.Context, repo, old, tip, msg string) (string, error) {
	out, code, err := g.runCode(ctx, repo, "merge-tree", "--write-tree", "--name-only", old, tip)
	if err != nil {
		if code != 1 {
			return "", err
		}
		lines := strings.Split(out, "\n")
		var files []string
		for _, l := range lines[min(1, len(lines)):] {
			if l == "" {
				break
			}
			files = append(files, l)
		}
		return "", fmt.Errorf("%w in %s", ErrMergeConflict, strings.Join(files, ", "))
	}
	tree, _, _ := strings.Cut(out, "\n")
	return g.run(ctx, repo, "commit-tree", tree, "-p", old, "-p", tip, "-m", msg)
}

// checkOverlap refuses changes that touch a file the user changed or an untracked file in the way.
func (g *Git) checkOverlap(ctx context.Context, repo string, changes []FileChange) error {
	dirty, err := g.dirtyFiles(ctx, repo)
	if err != nil {
		return err
	}
	var hit []string
	for _, c := range changes {
		_, statErr := os.Lstat(filepath.Join(repo, c.Path))
		if dirty[c.Path] || c.Status == "A" && statErr == nil {
			hit = append(hit, c.Path)
		}
	}
	if len(hit) > 0 {
		return &DirtyOverlapError{hit}
	}
	return nil
}

func reverse(changes []FileChange) []FileChange {
	out := make([]FileChange, len(changes))
	for i, c := range changes {
		switch c.Status {
		case "A":
			c.Status = "D"
		case "D":
			c.Status = "A"
		}
		out[i] = c
	}
	return out
}

// syncPaths brings the index and working tree of the given paths to commit rev (added and modified
// ones are checked out, deleted ones removed), leaving every other file alone.
func (g *Git) syncPaths(ctx context.Context, repo, rev string, changes []FileChange) error {
	var put, del []string
	for _, c := range changes {
		if c.Status == "D" {
			del = append(del, c.Path)
		} else {
			put = append(put, c.Path)
		}
	}
	for len(put) > 0 {
		n := min(len(put), 200)
		if _, err := g.run(ctx, repo, append([]string{"checkout", rev, "--"}, put[:n]...)...); err != nil {
			return err
		}
		put = put[n:]
	}
	for len(del) > 0 {
		n := min(len(del), 200)
		if _, err := g.run(ctx, repo, append([]string{"rm", "--quiet", "--force", "--ignore-unmatch", "--"}, del[:n]...)...); err != nil {
			return err
		}
		del = del[n:]
	}
	return nil
}

// StashPush stashes the uncommitted changes to tracked files and returns the stash commit,
// or "" when there was nothing to stash.
func (g *Git) StashPush(ctx context.Context, repo, msg string) (string, error) {
	before := g.stashTop(ctx, repo)
	if _, err := g.run(ctx, repo, "stash", "push", "--quiet", "-m", msg); err != nil {
		return "", err
	}
	if after := g.stashTop(ctx, repo); after != before {
		return after, nil
	}
	return "", nil
}

func (g *Git) stashTop(ctx context.Context, repo string) string {
	out, _ := g.run(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/stash")
	return out
}

// StashRef returns the current name (stash@{n}) of the stash commit sha, or "" when it is gone.
func (g *Git) StashRef(ctx context.Context, repo, sha string) string {
	out, _ := g.run(ctx, repo, "stash", "list", "--format=%H")
	for i, h := range strings.Fields(out) {
		if h == sha {
			return fmt.Sprintf("stash@{%d}", i)
		}
	}
	return ""
}

// StashRestore applies the stash commit sha (keeping the staged state) and drops that stash. If it
// does not apply cleanly the stash is kept, the checkout is reset to HEAD so no conflict
// markers are left, and an error says so.
func (g *Git) StashRestore(ctx context.Context, repo, sha string) error {
	if _, err := g.run(ctx, repo, "stash", "apply", "--index", sha); err != nil {
		g.run(ctx, repo, "reset", "--hard", "--quiet", "HEAD")
		return fmt.Errorf("applying the stash conflicts: %w", err)
	}
	if ref := g.StashRef(ctx, repo, sha); ref != "" {
		g.run(ctx, repo, "stash", "drop", "--quiet", ref)
	}
	return nil
}
