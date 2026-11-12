package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Guard confines every path the board touches to a sandbox root.
// With an empty root it allows everything (normal use).
type Guard struct {
	root string // resolved absolute path, or ""
}

func NewGuard(root string) (*Guard, error) {
	if root == "" {
		return &Guard{}, nil
	}
	resolved, err := resolvePath(root)
	if err != nil {
		return nil, fmt.Errorf("sandbox root: %w", err)
	}
	return &Guard{root: resolved}, nil
}

func (g *Guard) Enabled() bool { return g.root != "" }

// Check returns an error unless path resolves (following symlinks) inside the sandbox.
func (g *Guard) Check(path string) error {
	if g.root == "" {
		return nil
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return fmt.Errorf("sandbox: cannot resolve %q: %w", path, err)
	}
	if resolved != g.root && !strings.HasPrefix(resolved, g.root+string(filepath.Separator)) {
		return fmt.Errorf("sandbox: %q is outside %q", path, g.root)
	}
	return nil
}

// CheckRemoteURL allows only local remotes inside the sandbox when the sandbox is on.
func (g *Guard) CheckRemoteURL(url string) error {
	if g.root == "" {
		return nil
	}
	local := strings.TrimPrefix(url, "file://")
	if strings.Contains(local, "://") || (!filepath.IsAbs(local) && strings.Contains(local, ":")) {
		return fmt.Errorf("sandbox: remote %q is not a local path", url)
	}
	return g.Check(local)
}

// resolvePath makes path absolute and resolves symlinks. For paths that don't
// exist yet, the deepest existing ancestor is resolved and the rest appended.
func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rest := ""
	cur := abs
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			return filepath.Join(real, rest), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
