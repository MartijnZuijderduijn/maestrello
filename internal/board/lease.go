package board

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"maestrello/internal/model"
)

// defaultLeaseTTL outlasts any real gap between an agent's stream events.
const defaultLeaseTTL = 30 * time.Minute

// Lease is the content of one lease file.
type Lease struct {
	Folder     string    `json:"folder"`
	CardID     int64     `json:"card_id"`
	SessionID  string    `json:"session_id"`
	Board      string    `json:"board"` // id of the board process that wrote it
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`

	file string
}

// leaseWaiter is a card queued for one or more leased folders.
type leaseWaiter struct {
	cardID    int64
	folders   []string
	wake      chan struct{} // signalled when state changes
	state     string        // waiting, granted, cancelled
	blockedBy int64
}

// LeaseBusyError says a folder is leased by another card.
type LeaseBusyError struct {
	Folder string
	Holder int64
	Title  string
}

func (e *LeaseBusyError) Error() string {
	return fmt.Sprintf("%s is in use by card #%d (%s)", e.Folder, e.Holder, e.Title)
}

func (a *App) leaseTTLOrDefault() time.Duration {
	if a.leaseTTL > 0 {
		return a.leaseTTL
	}
	return defaultLeaseTTL
}

// leaseBeat is how often leases are renewed and waiters re-checked.
func (a *App) leaseBeat() time.Duration {
	if b := a.leaseTTLOrDefault() / 4; b < time.Minute {
		return b
	}
	return time.Minute
}

func (a *App) leaseDir() string { return filepath.Join(a.dataDir, "leases") }

// boardIdentity identifies this board process in the lease files it writes. Call with leaseMu held.
func (a *App) boardIdentity() string {
	if a.boardID == "" {
		a.boardID = newUUID()
	}
	return a.boardID
}

// leaseKey normalises a folder path for comparing and naming leases.
func leaseKey(folder string) string {
	folder = filepath.Clean(folder)
	if p, err := filepath.EvalSymlinks(folder); err == nil {
		folder = p
	}
	return folder
}

func (a *App) leaseFile(folder string) string {
	sum := sha256.Sum256([]byte(folder))
	return filepath.Join(a.leaseDir(), hex.EncodeToString(sum[:8])+"-"+slug(filepath.Base(folder))+".json")
}

func overlaps(x, y string) bool { return insideDir(x, y) || insideDir(y, x) }

// readLeases loads every lease file and splits them into live and stale ones.
// Call with leaseMu held.
func (a *App) readLeases() (live, stale []*Lease) {
	entries, err := os.ReadDir(a.leaseDir())
	if err != nil {
		return nil, nil
	}
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(a.leaseDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		l := &Lease{file: path}
		if json.Unmarshal(data, l) != nil || l.Folder == "" {
			l.Folder = path // unreadable: stale, and overlaps nothing
			stale = append(stale, l)
			continue
		}
		if a.leaseAlive(l, now) {
			live = append(live, l)
		} else {
			stale = append(stale, l)
		}
	}
	return live, stale
}

func (a *App) leaseAlive(l *Lease, now time.Time) bool {
	if !now.Before(l.ExpiresAt) {
		return false
	}
	if l.Board != a.boardIdentity() {
		// Written by an earlier board (or another one sharing the data dir).
		return l.PID != os.Getpid() && pidAlive(l.PID)
	}
	c, err := a.store.GetCard(l.CardID)
	if err != nil {
		return false
	}
	return c.Status == model.StatusRunning || c.Status == model.StatusNeedsInput || a.liveRun(l.CardID) != nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// conflict returns the first live lease of another card that overlaps one of
// folders. board is this board's id: a lease is the card's own only if this board wrote it
// (queue reservations have no board).
func conflict(folders []string, cardID int64, board string, leases []*Lease) *Lease {
	for _, l := range leases {
		if l.CardID == cardID && (l.Board == board || l.Board == "") {
			continue
		}
		for _, f := range folders {
			if overlaps(f, l.Folder) {
				return l
			}
		}
	}
	return nil
}

// grantLeases writes lease files for all folders, taking over stale ones. The
// caller has checked that no live lease of another card conflicts. Call with leaseMu held.
func (a *App) grantLeases(cardID int64, folders []string, stale []*Lease) ([]*Lease, error) {
	card, err := a.store.GetCard(cardID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(a.leaseDir(), 0o755); err != nil {
		return nil, err
	}
	for _, s := range stale {
		for _, f := range folders {
			if s.CardID != cardID && overlaps(f, s.Folder) {
				a.takeOver(card, s)
				break
			}
		}
	}
	now := time.Now().UTC()
	var granted []*Lease
	for _, f := range folders {
		l := &Lease{Folder: f, CardID: cardID, SessionID: card.SessionID, Board: a.boardIdentity(), PID: os.Getpid(),
			AcquiredAt: now, ExpiresAt: now.Add(a.leaseTTLOrDefault()), file: a.leaseFile(f)}
		if old := readLease(l.file); old != nil && old.CardID == cardID && old.Board == l.Board {
			l.AcquiredAt = old.AcquiredAt // already ours: renew
			if err := writeLease(l, true); err != nil {
				return granted, err
			}
		} else if err := writeLease(l, false); err != nil {
			for _, g := range granted {
				os.Remove(g.file)
			}
			return nil, err
		}
		granted = append(granted, l)
	}
	return granted, nil
}

// takeOver removes a stale lease, stopping its session if it still runs here.
func (a *App) takeOver(card *model.Card, s *Lease) {
	why := "its board is gone"
	switch {
	case !time.Now().Before(s.ExpiresAt):
		why = fmt.Sprintf("it expired at %s", s.ExpiresAt.Local().Format("15:04:05"))
	case s.Board == a.boardIdentity():
		why = "that card is no longer running"
	}
	os.Remove(s.file)
	a.note(card.ID, "took over the lease on %s from card #%d: %s", s.Folder, s.CardID, why)
	if s.Board == a.boardIdentity() {
		a.note(s.CardID, "lost the lease on %s to card #%d: %s", s.Folder, card.ID, why)
		if run := a.liveRun(s.CardID); run != nil {
			run.stop("lease_expired")
		}
	}
}

func readLease(path string) *Lease {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	l := &Lease{file: path}
	if json.Unmarshal(data, l) != nil {
		return nil
	}
	return l
}

// writeLease writes l atomically. Unless replace is set, it fails if the file exists.
func writeLease(l *Lease, replace bool) error {
	data, _ := json.MarshalIndent(l, "", "  ")
	tmp, err := os.CreateTemp(filepath.Dir(l.file), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if replace {
		return os.Rename(tmp.Name(), l.file)
	}
	return os.Link(tmp.Name(), l.file) // exclusive: fails if another writer won
}

// acquireNow takes a lease on folder for a card with a live session (add_repo),
// without queueing. It returns a *LeaseBusyError if another card holds it. Cards
// still queued for the folder do not block it: they cannot start before this
// session ends anyway when they wait on the same holder, and a live session must
// not wait on cards that have not started.
func (a *App) acquireNow(card *model.Card, folder string) error {
	folder = leaseKey(folder)
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	live, stale := a.readLeases()
	if l := conflict([]string{folder}, card.ID, a.boardIdentity(), live); l != nil {
		return a.busyError(l)
	}
	_, err := a.grantLeases(card.ID, []string{folder}, stale)
	return err
}

func (a *App) busyError(l *Lease) *LeaseBusyError {
	e := &LeaseBusyError{Folder: l.Folder, Holder: l.CardID}
	if c, err := a.store.GetCard(l.CardID); err == nil && l.Board == a.boardIdentity() {
		e.Title = c.Title
	} else {
		e.Title = "an earlier board"
	}
	return e
}

// releaseLeases drops every lease the card holds and lets waiters move up. Idempotent.
func (a *App) releaseLeases(cardID int64) {
	a.leaseMu.Lock()
	live, stale := a.readLeases()
	released := false
	for _, l := range append(live, stale...) {
		if l.CardID == cardID && l.Board == a.boardIdentity() {
			if err := os.Remove(l.file); err == nil {
				released = true
				a.note(cardID, "released the lease on %s", l.Folder)
			}
		}
	}
	a.leaseMu.Unlock()
	if released || a.hasWaiters() {
		a.grantWaiters()
	}
}

// renewLeases pushes the expiry of the card's leases to until and records its session.
func (a *App) renewLeases(cardID int64, sessionID string, until time.Time) {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	live, _ := a.readLeases()
	for _, l := range live {
		if l.CardID == cardID && l.Board == a.boardIdentity() && (until.After(l.ExpiresAt) || l.SessionID != sessionID) {
			if until.After(l.ExpiresAt) {
				l.ExpiresAt = until.UTC()
			}
			l.SessionID = sessionID
			if err := writeLease(l, true); err != nil {
				log.Printf("ERROR lease: card %d: could not renew lease on %s: %v", cardID, l.Folder, err)
			}
		}
	}
}

// heartbeat renews the session's leases until done. The lease runs leaseTTL past
// the last stream event, or past now while the agent waits on me.
func (a *App) heartbeat(id int64, sessionID string, run *Run, done chan struct{}) {
	t := time.NewTicker(a.leaseBeat())
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-a.quit:
			return
		case <-t.C:
			a.renewLeases(id, sessionID, run.lastSign().Add(a.leaseTTLOrDefault()))
		}
	}
}

// localFolders lists the folders that are, or will become, local work for the
// card's open repos. It is what the card must lease before its session starts.
func (a *App) localFolders(card *model.Card, repos []*model.CardRepo) []string {
	var out []string
	for _, r := range repos {
		if r.Result != "" {
			continue
		}
		switch {
		case r.Local():
			out = append(out, leaseKey(r.WorktreePath))
		case r.WorktreePath == "":
			_, orig, err := a.resolveRepo(card, r.RepoPath)
			if err != nil || insideDir(orig, a.dataDir) || a.git.IsRepo(orig) {
				continue // checkout reports the problem, or it is a git repo
			}
			if st, err := os.Stat(orig); err == nil && st.IsDir() {
				out = append(out, leaseKey(orig))
			}
		}
	}
	return out
}

func (a *App) hasWaiters() bool {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	return len(a.queue) > 0
}

// isWaiting reports whether the card is queued for a folder.
func (a *App) isWaiting(id int64) bool {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	for _, w := range a.queue {
		if w.cardID == id {
			return true
		}
	}
	return false
}

// claimLocal blocks until the card holds leases on all folders, serving cards
// first in, first out. While it waits the card stays in Running with WaitingOn
// set; once served its activity goes back to activity. It returns
// errQueueCancelled if the card was taken out of the queue (Stop, Move, Discard)
// and errBoardStopping if the board shuts down.
func (a *App) claimLocal(card *model.Card, folders []string, activity string) error {
	if len(folders) == 0 {
		return nil
	}
	select {
	case <-a.quit:
		return errBoardStopping
	default:
	}
	w := &leaseWaiter{cardID: card.ID, folders: folders, wake: make(chan struct{}, 1), state: "waiting"}
	a.leaseMu.Lock()
	for _, q := range a.queue {
		if q.cardID == card.ID {
			a.leaseMu.Unlock()
			return fmt.Errorf("the card is already waiting for a folder")
		}
	}
	a.queue = append(a.queue, w)
	a.leaseMu.Unlock()
	a.busy.Add(1)
	defer a.busy.Done()

	a.grantWaiters()
	t := time.NewTicker(a.leaseBeat())
	defer t.Stop()
	for {
		a.leaseMu.Lock()
		state := w.state
		a.leaseMu.Unlock()
		switch state {
		case "granted":
			if w.blockedBy != 0 {
				a.note(card.ID, "the folder is free; starting")
				a.update(card.ID, func(c *model.Card) { c.WaitingOn = 0; c.Activity = activity })
			}
			return nil
		case "cancelled":
			return errQueueCancelled
		}
		select {
		case <-w.wake:
		case <-t.C:
			a.grantWaiters() // expiry frees leases without a release
		case <-a.quit:
			if !a.dequeue(card.ID) {
				a.releaseLeases(card.ID) // granted just now
			}
			return errBoardStopping
		}
	}
}

var (
	errQueueCancelled = errors.New("removed from the folder queue")
	errBoardStopping  = errors.New("board stopped while the card waited for a folder")
)

// grantWaiters hands free folders to queued cards in order. A folder that an
// earlier card is waiting for is reserved for it, so later cards cannot jump ahead.
func (a *App) grantWaiters() {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	if len(a.queue) == 0 {
		return
	}
	live, stale := a.readLeases()
	var reserved []*Lease // folders of earlier waiters, as pseudo-leases
	var rest []*leaseWaiter
	for _, w := range a.queue {
		blocker := conflict(w.folders, w.cardID, a.boardIdentity(), live)
		if blocker == nil {
			blocker = conflict(w.folders, w.cardID, a.boardIdentity(), reserved)
		}
		if blocker == nil {
			granted, err := a.grantLeases(w.cardID, w.folders, stale)
			if err == nil {
				live = append(live, granted...)
				w.state = "granted"
				notify(w.wake)
				continue
			}
			log.Printf("ERROR lease: card %d: could not acquire lease: %v", w.cardID, err)
		} else if blocker.CardID != w.blockedBy {
			w.blockedBy = blocker.CardID
			title := "an earlier board"
			if c, err := a.store.GetCard(blocker.CardID); err == nil && (blocker.Board == a.boardIdentity() || blocker.Board == "") {
				title = c.Title
			}
			a.note(w.cardID, "waiting on card #%d (%s) for %s", blocker.CardID, title, blocker.Folder)
			a.update(w.cardID, func(c *model.Card) {
				c.WaitingOn = blocker.CardID
				c.Activity = fmt.Sprintf("waiting on card #%d (%s)", blocker.CardID, title)
			})
		}
		rest = append(rest, w)
		for _, f := range w.folders {
			reserved = append(reserved, &Lease{Folder: f, CardID: w.cardID})
		}
	}
	a.queue = rest
}

// dequeue takes a waiting card out of the queue. It reports whether the card was waiting.
func (a *App) dequeue(id int64) bool {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	for i, w := range a.queue {
		if w.cardID == id {
			a.queue = append(a.queue[:i:i], a.queue[i+1:]...)
			w.state = "cancelled"
			notify(w.wake)
			return true
		}
	}
	return false
}

// cancelWait removes a waiting card from the queue and lets the cards behind it move up.
func (a *App) cancelWait(id int64) bool {
	if !a.dequeue(id) {
		return false
	}
	a.update(id, func(c *model.Card) { c.WaitingOn = 0 })
	a.note(id, "removed from the folder queue")
	a.grantWaiters()
	return true
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// cleanStaleLeases runs at startup: leases left by a board that is gone are dropped.
func (a *App) cleanStaleLeases() {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	_, stale := a.readLeases()
	for _, s := range stale {
		if os.Remove(s.file) == nil {
			log.Printf("INFO  lease: dropped stale lease on %s (held by card %d)", s.Folder, s.CardID)
		}
	}
}
