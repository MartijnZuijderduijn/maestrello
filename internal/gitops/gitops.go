package gitops

import (
	"bytes"
	"context"
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
	if err := g.guard.Check(dir); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
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
