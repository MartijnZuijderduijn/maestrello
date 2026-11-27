package board

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"maestrello/internal/model"
)

// WorkspaceView is the per-project workspace: the folders its cards work in,
// who uses them, who holds leases on them and who waits.
type WorkspaceView struct {
	ProjectID  int64             `json:"project_id"`
	RootFolder string            `json:"root_folder"`
	Entries    []*WorkspaceEntry `json:"entries"`
}

// WorkspaceEntry is one folder, with kind "git", "local" or "missing".
type WorkspaceEntry struct {
	Root       string             `json:"root"`
	Path       string             `json:"path"`
	Abs        string             `json:"abs"`
	Kind       string             `json:"kind"`
	Uses       []*WorkspaceUse    `json:"uses"`
	Leases     []*WorkspaceLease  `json:"leases"`
	Waiting    []*WorkspaceWaiter `json:"waiting"`
	Discovered bool               `json:"discovered"`
}

// WorkspaceUse is one card's use of a folder.
type WorkspaceUse struct {
	CardID       int64  `json:"card_id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	SessionID    string `json:"session_id"`
	Activity     string `json:"activity"`
	Harness      string `json:"harness"`
	Model        string `json:"model"`
	Live         bool   `json:"live"`
	WaitingOn    int64  `json:"waiting_on"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	BaseRef      string `json:"base_ref"`
	Result       string `json:"result"`
	PRURL        string `json:"pr_url"`
	LocalSince   string `json:"local_since"`
	Planned      bool   `json:"planned"`
	Ahead        *int   `json:"ahead,omitempty"`
	Dirty        *bool  `json:"dirty,omitempty"`
}

// WorkspaceLease is a lease file touching a folder.
type WorkspaceLease struct {
	CardID       int64     `json:"card_id"`
	Title        string    `json:"title"`
	SessionID    string    `json:"session_id"`
	AcquiredAt   time.Time `json:"acquired_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Live         bool      `json:"live"`
	OtherBoard   bool      `json:"other_board"`
	OtherProject bool      `json:"other_project"`
}

// WorkspaceWaiter is a card queued for a folder.
type WorkspaceWaiter struct {
	CardID    int64  `json:"card_id"`
	Title     string `json:"title"`
	BlockedBy int64  `json:"blocked_by"`
}

// Workspace builds the workspace view of a project. It reads the store and the
// lease files only, unless withGit is set: then ahead/dirty are filled for
// worktree uses, concurrently and best effort (nil on error or ctx expiry).
func (a *App) Workspace(ctx context.Context, projectID int64, includeDone, withGit bool) (*WorkspaceView, error) {
	p, err := a.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	all, err := a.store.ListCards()
	if err != nil {
		return nil, err
	}
	cards := cardsInProject(all, projectID)
	inProj := map[int64]bool{}
	for _, c := range cards {
		inProj[c.ID] = true
	}

	byAbs := map[string]*WorkspaceEntry{}
	var entries []*WorkspaceEntry
	newEntry := func(root, path, abs, kind string) *WorkspaceEntry {
		e := &WorkspaceEntry{Root: root, Path: path, Abs: abs, Kind: kind,
			Uses: []*WorkspaceUse{}, Leases: []*WorkspaceLease{}, Waiting: []*WorkspaceWaiter{}}
		byAbs[abs] = e
		entries = append(entries, e)
		return e
	}

	for _, c := range cards {
		if c.Status == model.StatusDone && !includeDone {
			continue
		}
		repos, err := a.store.Repos(c.ID)
		if err != nil {
			continue
		}
		for _, r := range repos {
			rel, orig, err := a.resolveRepo(c, r.RepoPath)
			if err != nil {
				continue
			}
			key := leaseKey(orig)
			kind, planned := "git", false
			switch {
			case r.Local():
				kind = "local"
			case r.WorktreePath != "":
			default:
				planned = true
				if a.git.IsRepo(orig) {
					kind = "git"
				} else if st, err := os.Stat(orig); err == nil && st.IsDir() {
					kind = "local"
				} else {
					kind = "missing"
				}
			}
			e := byAbs[key]
			if e == nil {
				e = newEntry(c.RootFolder, rel, key, kind)
			}
			e.Uses = append(e.Uses, &WorkspaceUse{
				CardID: c.ID, Title: c.Title, Status: c.Status, SessionID: c.SessionID, Activity: c.Activity,
				Harness: c.Harness, Model: c.Model, Live: a.liveRun(c.ID) != nil, WaitingOn: c.WaitingOn,
				WorktreePath: r.WorktreePath, Branch: r.Branch, BaseRef: r.BaseRef, Result: r.Result,
				PRURL: r.PRURL, LocalSince: r.LocalSince, Planned: planned,
			})
		}
	}

	type waiter struct {
		cardID, blockedBy int64
		folders           []string
	}
	a.leaseMu.Lock()
	live, stale := a.readLeases()
	board := a.boardIdentity()
	queue := make([]waiter, 0, len(a.queue))
	for _, w := range a.queue {
		queue = append(queue, waiter{w.cardID, w.blockedBy, append([]string(nil), w.folders...)})
	}
	a.leaseMu.Unlock()

	addLease := func(l *Lease, isLive bool) {
		other := l.Board != board
		wl := &WorkspaceLease{CardID: l.CardID, SessionID: l.SessionID, AcquiredAt: l.AcquiredAt,
			ExpiresAt: l.ExpiresAt, Live: isLive, OtherBoard: other, OtherProject: !inProj[l.CardID], Title: "an earlier board"}
		if !other {
			if c, err := a.store.GetCard(l.CardID); err == nil {
				wl.Title = c.Title
			}
		}
		matched := false
		for _, e := range entries {
			if overlaps(l.Folder, e.Abs) {
				e.Leases = append(e.Leases, wl)
				matched = true
			}
		}
		if !matched && !wl.OtherProject {
			path, err := filepath.Rel(p.RootFolder, l.Folder)
			if err != nil {
				path = l.Folder
			}
			e := newEntry(p.RootFolder, path, l.Folder, "local")
			e.Leases = append(e.Leases, wl)
		}
	}
	for _, l := range live {
		addLease(l, true)
	}
	for _, l := range stale {
		addLease(l, false)
	}

	for _, w := range queue {
		title := ""
		if c, err := a.store.GetCard(w.cardID); err == nil {
			title = c.Title
		}
		ww := &WorkspaceWaiter{CardID: w.cardID, Title: title, BlockedBy: w.blockedBy}
		for _, e := range entries {
			for _, f := range w.folders {
				if overlaps(f, e.Abs) {
					e.Waiting = append(e.Waiting, ww)
					break
				}
			}
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Root != entries[j].Root {
			return entries[i].Root < entries[j].Root
		}
		return entries[i].Path < entries[j].Path
	})
	for _, e := range entries {
		sort.SliceStable(e.Uses, func(i, j int) bool {
			if e.Uses[i].Live != e.Uses[j].Live {
				return e.Uses[i].Live
			}
			return e.Uses[i].CardID < e.Uses[j].CardID
		})
	}
	if entries == nil {
		entries = []*WorkspaceEntry{}
	}
	if withGit {
		a.fillGitState(ctx, entries)
	}
	return &WorkspaceView{ProjectID: p.ID, RootFolder: p.RootFolder, Entries: entries}, nil
}

// fillGitState sets Ahead and Dirty on every use with a real worktree, running
// at most 4 git calls at a time. Failures leave the fields nil.
func (a *App) fillGitState(ctx context.Context, entries []*WorkspaceEntry) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, e := range entries {
		if e.Kind != "git" {
			continue
		}
		for _, u := range e.Uses {
			if u.Planned || u.WorktreePath == "" {
				continue
			}
			wg.Add(1)
			go func(u *WorkspaceUse) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				if n, err := a.git.AheadCount(ctx, u.WorktreePath, u.BaseRef); err == nil {
					u.Ahead = &n
				}
				if st, err := a.git.Status(ctx, u.WorktreePath); err == nil {
					d := strings.TrimSpace(st) != ""
					u.Dirty = &d
				}
			}(u)
		}
	}
	wg.Wait()
}

// handleWorkspace serves GET /api/projects/{id}/workspace; ?done=1 includes done
// cards. Later optional params (?git=1, ?discovered=1) will be read here too.
func (a *App) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	done := r.URL.Query().Get("done")
	git := r.URL.Query().Get("git")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	view, err := a.Workspace(ctx, id, done != "" && done != "0", git != "" && git != "0")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
