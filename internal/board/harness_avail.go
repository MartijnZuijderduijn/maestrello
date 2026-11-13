package board

import (
	"fmt"
	"sync"
	"time"

	"maestrello/internal/harness"
)

// harnessBins is the binary path each adapter was configured with, read at call time.
func (a *App) harnessBins() map[string]string {
	return map[string]string{"claude": a.claudeBin, "opencode": a.opencodeBin, "mistral": a.mistralBin}
}

// harness resolves a harness name to an adapter that starts processes. It reads
// the per-adapter binaries (a.claudeBin, a.opencodeBin, a.mistralBin) at call time, so changing
// one after construction takes effect.
func (a *App) harness(name string) (harness.Harness, error) {
	return harness.Build(name, a.harnessBins())
}

// HarnessStatus says whether a harness's CLI can be used on this machine.
type HarnessStatus struct {
	Available bool
	Version   string // first line of `<bin> --version`
	Error     string // why it is not available
	Bin       string // the binary that was checked
	Models    []string
	CheckedAt time.Time
}

// availState caches the latest check per harness (see refreshAvailability).
// Requests only read the cache; a check runs at startup, on ?refresh=1, and once
// when a harness has no cached result for its current binary.
type availState struct {
	mu      sync.Mutex
	status  map[string]*HarnessStatus
	refresh sync.Mutex // one check at a time
}

// checkHarness runs the check for one harness and returns its status.
func (a *App) checkHarness(name string) *HarnessStatus {
	h, err := a.harness(name)
	if err != nil {
		return &HarnessStatus{Error: err.Error(), CheckedAt: time.Now()}
	}
	bin := a.harnessBins()[name]
	if bin == "" {
		bin = name
	}
	st := &HarnessStatus{Bin: bin, CheckedAt: time.Now()}
	st.Version, err = harness.CheckBinary(bin)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Available = true
	if lm, ok := h.(harness.ModelLister); ok {
		st.Models = lm.ListModels()
	}
	return st
}

// refreshAvailability re-checks every harness, caches the results and tells the
// UI. It never runs per request.
func (a *App) refreshAvailability() {
	s := &a.avail
	s.refresh.Lock()
	defer s.refresh.Unlock()
	res := map[string]*HarnessStatus{}
	for _, name := range harness.Names() {
		res[name] = a.checkHarness(name)
	}
	s.mu.Lock()
	s.status = res
	s.mu.Unlock()
	a.hub.Publish("harnesses", nil)
}

// harnessStatus is the cached status of a harness. With no cached result (or one
// for another binary, as the path is read at call time) it checks once.
func (a *App) harnessStatus(name string) *HarnessStatus {
	bin := a.harnessBins()[name]
	if bin == "" {
		bin = name
	}
	s := &a.avail
	s.mu.Lock()
	st := s.status[name]
	s.mu.Unlock()
	if st != nil && st.Bin == bin {
		return st
	}
	s.refresh.Lock()
	defer s.refresh.Unlock()
	s.mu.Lock()
	if st = s.status[name]; st != nil && st.Bin == bin {
		s.mu.Unlock()
		return st
	}
	s.mu.Unlock()
	st = a.checkHarness(name)
	s.mu.Lock()
	if s.status == nil {
		s.status = map[string]*HarnessStatus{}
	}
	s.status[name] = st
	s.mu.Unlock()
	return st
}

// requireHarness returns an error that explains why a harness cannot be used, or nil.
func (a *App) requireHarness(name string) error {
	if name == "" {
		name = harness.Default
	}
	if st := a.harnessStatus(name); !st.Available {
		return fmt.Errorf("harness %s is not available: %s", name, st.Error)
	}
	return nil
}
