package board

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"maestrello/internal/gitops"
	"maestrello/internal/model"
	"maestrello/internal/sandbox"
	"maestrello/internal/store"
)

type App struct {
	store       *store.Store
	hub         *Hub
	guard       *sandbox.Guard
	git         *gitops.Git
	dataDir     string
	baseURL     string // where agents reach the board's MCP endpoint
	claudeBin   string // per-adapter CLI paths, see harnessBins
	opencodeBin string

	mu       sync.Mutex
	runs     map[int64]*Run            // live agent sessions by card
	waiters  map[int64]chan inputReply // blocked MCP tool calls by input id
	busy     sync.WaitGroup            // refinements and sessions in flight
	quit     chan struct{}             // closed when the board shuts down
	stopReq  chan struct{}             // asks serve to shut the board down
	stopOnce sync.Once

	// Leases on local-work folders (see lease.go). Lock order: leaseMu before mu.
	leaseMu  sync.Mutex
	queue    []*leaseWaiter // cards waiting for a leased folder, first come first served
	leaseTTL time.Duration  // 0 means defaultLeaseTTL
	boardID  string         // identifies this board process in lease files

	// Who each harness's CLI is logged in as and its latest usage limits (usage.go).
	usage usageState

	// Whether each harness's CLI is usable on this machine (harness_avail.go).
	avail availState
}

type inputReply struct {
	Answer string
	Allow  bool
}

func (a *App) workDir(cardID int64) string {
	return filepath.Join(a.dataDir, "work", fmt.Sprint(cardID))
}

// update loads a card, applies fn, saves it and broadcasts the change.
func (a *App) update(id int64, fn func(c *model.Card)) (*model.Card, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.store.GetCard(id)
	if err != nil {
		return nil, err
	}
	fn(c)
	if err := a.store.SaveCard(c); err != nil {
		return nil, err
	}
	a.hub.Publish("card", c)
	return c, nil
}

func (a *App) setStatus(id int64, status string) {
	if _, err := a.update(id, func(c *model.Card) { c.Status = status }); err != nil {
		log.Printf("ERROR board: card %d: could not set status to %q: %v", id, status, err)
	}
}

func (a *App) fail(id int64, reason string) {
	log.Printf("WARN  board: card %d marked failed: %s", id, reason)
	a.update(id, func(c *model.Card) {
		c.Status = model.StatusFailed
		c.FailReason = reason
		c.Activity = ""
		c.WaitingOn = 0
	})
	a.releaseLeases(id)
}

// note records a board-generated event in the card's log.
func (a *App) note(id int64, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	data, _ := json.Marshal(map[string]string{"text": msg})
	a.event(id, "board", data)
}

func (a *App) event(id int64, kind string, data json.RawMessage) {
	ev, err := a.store.AddEvent(id, kind, data)
	if err != nil {
		log.Printf("ERROR board: card %d: could not store %q event: %v", id, kind, err)
		return
	}
	a.hub.Publish("event", ev)
}

// recover runs at startup: sessions that were live when the board stopped are gone,
// and so are the leases they held. Cards that were waiting for a folder fail too.
func (a *App) recover() error {
	a.cleanStaleLeases()
	cards, err := a.store.CardsWithStatus(model.StatusRunning, model.StatusRefining, model.StatusNeedsInput)
	if err != nil {
		return err
	}
	for _, c := range cards {
		switch c.Status {
		case model.StatusRefining:
			a.fail(c.ID, "board restarted during refinement")
		case model.StatusRunning:
			a.fail(c.ID, "board restarted while the agent was running")
		case model.StatusNeedsInput:
			// The input stays open; answering it resumes the session.
			a.note(c.ID, "board restarted; answering will resume the session")
		}
	}
	return nil
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "task"
	}
	return s
}

func branchName(c *model.Card) string {
	return fmt.Sprintf("maestrello/%d-%s", c.ID, slug(c.Title))
}

// Hub fans out board events to SSE subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]bool
}

func NewHub() *Hub { return &Hub{subs: map[chan []byte]bool{}} }

func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 256)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) Publish(kind string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", kind, data))
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default: // slow client; it refetches on reconnect
		}
	}
}
