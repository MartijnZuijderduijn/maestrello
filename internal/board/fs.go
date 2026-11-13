package board

import (
	"maestrello/internal/sandbox"

	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const fsListCap = 2000

type fsEntry struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	IsRepo bool   `json:"is_repo"`
}

type fsListing struct {
	Path      string    `json:"path"`
	Parent    string    `json:"parent,omitempty"`
	Entries   []fsEntry `json:"entries"`
	Truncated bool      `json:"truncated"`
}

// localHost reports whether a Host header names the loopback interface
// (DNS-rebinding protection). It accepts host and host:port forms.
func localHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// inside reports whether p is dir or lies within it.
func inside(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// inDataDir reports whether p (resolved) lies in the board's data directory.
func (a *App) inDataDir(p string) bool {
	if a.dataDir == "" {
		return false
	}
	real, err := sandbox.ResolvePath(p)
	if err != nil {
		return false
	}
	data, err := sandbox.ResolvePath(a.dataDir)
	if err != nil {
		return false
	}
	return inside(real, data)
}

// fsStart picks the directory to list when no path is given.
func (a *App) fsStart() string {
	if st, err := a.store.Settings(); err == nil && st.DefaultRootFolder != "" {
		return st.DefaultRootFolder
	}
	if a.guard.Root() != "" {
		return a.guard.Root()
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return string(filepath.Separator)
}

// handleFSList lists one directory (names and types only) for the folder picker.
func (a *App) handleFSList(w http.ResponseWriter, r *http.Request) {
	if !localHost(r.Host) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: host must be localhost"})
		return
	}
	q := r.URL.Query()
	path := q.Get("path")
	if path == "" {
		path = a.fsStart()
	}
	if !filepath.IsAbs(path) {
		writeErr(w, fmt.Errorf("path %q must be absolute", path))
		return
	}
	path = filepath.Clean(path)
	if err := a.guard.Check(path); err != nil {
		writeErr(w, err)
		return
	}
	if a.inDataDir(path) {
		writeErr(w, errors.New("path is inside the data directory"))
		return
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		writeErr(w, fmt.Errorf("%q is not a directory", path))
		return
	}
	files, hidden := q.Get("files") == "1", q.Get("hidden") == "1"

	des, err := os.ReadDir(path)
	if err != nil {
		writeErr(w, fmt.Errorf("cannot read %q", path))
		return
	}
	var dirs, regular []fsEntry
	for _, de := range des {
		name := de.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		if de.Type()&os.ModeSymlink != 0 {
			real, err := filepath.EvalSymlinks(full)
			if err != nil || a.guard.Check(real) != nil {
				continue
			}
		}
		if a.inDataDir(full) {
			continue
		}
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		if st.IsDir() {
			dirs = append(dirs, fsEntry{name, "dir", a.git.IsRepo(full)})
		} else if files {
			regular = append(regular, fsEntry{Name: name, Type: "file"})
		}
	}
	byName := func(s []fsEntry) { sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name }) }
	byName(dirs)
	byName(regular)
	out := fsListing{Path: path, Entries: append(dirs, regular...)}
	if len(out.Entries) > fsListCap {
		out.Entries, out.Truncated = out.Entries[:fsListCap], true
	}
	if parent := filepath.Dir(path); parent != path && a.guard.Check(parent) == nil {
		out.Parent = parent
	}
	writeJSON(w, 200, out)
}
