package cli

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"maestrello/internal/gitops"
)

// sourceDir is the folder this binary was built from: the module root above this
// file at compile time (kept unless built with -trimpath), if it still holds the
// module. Agents improving the board edit this folder.
func sourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		return ""
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// staleSource lists the source files under src changed after exe was built.
func staleSource(src, exe string) []string {
	st, err := os.Stat(exe)
	if err != nil {
		return nil
	}
	built := st.ModTime()
	var stale []string
	filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			if path != src && (rel == "testdata" || strings.HasPrefix(d.Name(), ".") || gitops.SkipDirs[d.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		source := strings.HasPrefix(rel, "frontend"+string(filepath.Separator)) ||
			rel == "go.mod" || rel == "go.sum" ||
			strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
		if info, err := d.Info(); source && err == nil && info.ModTime().After(built) {
			stale = append(stale, rel)
		}
		return nil
	})
	return stale
}

func currentExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// warnIfStale tells the user when the board's source changed since this build,
// since those changes (an agent's, say) only show up after a rebuild.
func warnIfStale() {
	src := sourceDir()
	exe, err := currentExe()
	if src == "" || err != nil {
		return
	}
	if stale := staleSource(src, exe); len(stale) > 0 {
		more := ""
		if len(stale) > 3 {
			stale, more = stale[:3], fmt.Sprintf(" and %d more", len(stale)-3)
		}
		fmt.Fprintf(os.Stderr, "note: the source in %s changed since this build (%s%s).\n      Run \"maestrello restart\" to rebuild and restart with those changes.\n",
			src, strings.Join(stale, ", "), more)
	}
}

// rebuild builds the board from src over the running executable and returns its path.
func rebuild(src string) (string, error) {
	exe, err := currentExe()
	if err != nil {
		return "", err
	}
	tmp := exe + ".new"
	fmt.Printf("building %s from %s...\n", exe, src)
	cmd := exec.Command("go", "build", "-o", tmp, "./cmd/maestrello")
	cmd.Dir = src
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("build failed, the running board is left alone: %w", err)
	}
	return exe, os.Rename(tmp, exe)
}

// restart rebuilds the board from source, stops the one running on the
// address, and replaces this process with the new build's "start".
func restart(o *startOpts, args []string) error {
	src := o.src
	if src == "" {
		src = sourceDir()
	}
	if src == "" {
		return fmt.Errorf("cannot tell where the source is; pass -src <folder>")
	}
	exe, err := rebuild(src)
	if err != nil {
		return err
	}
	if _, err := boardInfo(o.addr); err == nil {
		if err := stopBoard(o.addr); err != nil {
			return err
		}
	}
	// The browser already has the board open; an explicit -open in args still wins.
	return syscall.Exec(exe, append([]string{exe, "start", "-open=false"}, args...), os.Environ())
}
