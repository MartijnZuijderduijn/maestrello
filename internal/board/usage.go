package board

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"maestrello/internal/harness"
)

// usageSnapshot is what GET /api/account returns and the "usage" SSE event carries:
// one entry per registered harness, sorted by name.
type usageSnapshot struct {
	Harnesses []harnessUsage `json:"harnesses"`
}

// harnessUsage is one harness's account and usage limits. The two *Supported flags
// are the harness's Caps: a harness that has neither is left out of the header.
type harnessUsage struct {
	Harness          string               `json:"harness"`
	AccountSupported bool                 `json:"account_supported"`
	UsageSupported   bool                 `json:"usage_supported"`
	Account          *harness.Account     `json:"account"`
	AccountError     string               `json:"account_error,omitempty"`
	CheckedAt        *time.Time           `json:"checked_at"`
	Limits           []harness.UsageLimit `json:"limits"`
}

// harnessCache is the in-memory cache of one harness, in usageState.by.
type harnessCache struct {
	account   *harness.Account
	acctErr   string
	checkedAt time.Time
	limits    map[string]*harness.UsageLimit

	refresh sync.Mutex // serialises this harness's account lookups
}

// usageState is the in-memory account and usage cache on App, by harness name.
type usageState struct {
	mu sync.Mutex
	by map[string]*harnessCache
}

// cache returns the cache of a harness, creating it. The caller holds u.mu.
func (u *usageState) cache(name string) *harnessCache {
	if u.by == nil {
		u.by = map[string]*harnessCache{}
	}
	c := u.by[name]
	if c == nil {
		c = &harnessCache{}
		u.by[name] = c
	}
	return c
}

const (
	accountTimeout  = 10 * time.Second
	usageSettingKey = "usage_limits" // last limits per harness ({"claude": [...]}); not part of Settings
)

// windowOrder puts the common windows first in a fixed order.
var windowOrder = map[string]int{"five_hour": 0, "seven_day": 1, "seven_day_opus": 2, "seven_day_sonnet": 3, "seven_day_overage_included": 4, "overage": 5}

// sortedLimits copies a harness's limits in display order.
func sortedLimits(m map[string]*harness.UsageLimit) []harness.UsageLimit {
	out := []harness.UsageLimit{}
	for _, l := range m {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, iok := windowOrder[out[i].Type]
		oj, jok := windowOrder[out[j].Type]
		if iok != jok {
			return iok
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func (a *App) usageSnapshot() usageSnapshot {
	u := &a.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	s := usageSnapshot{Harnesses: []harnessUsage{}}
	for _, name := range harness.Names() {
		hu := harnessUsage{Harness: name, Limits: []harness.UsageLimit{}}
		if h, err := harness.For(name); err == nil {
			hu.AccountSupported, hu.UsageSupported = h.Caps().Account, h.Caps().Usage
		}
		if c := u.by[name]; c != nil {
			hu.AccountError = c.acctErr
			if c.account != nil {
				acct := *c.account
				hu.Account = &acct
			}
			if !c.checkedAt.IsZero() {
				t := c.checkedAt
				hu.CheckedAt = &t
			}
			hu.Limits = sortedLimits(c.limits)
		}
		s.Harnesses = append(s.Harnesses, hu)
	}
	return s
}

// refreshAccount asks one harness who its CLI is logged in as and caches the
// answer. It never blocks longer than accountTimeout, and a failure becomes an
// account_error that replaces the earlier account. A harness without the account
// capability (or an unknown name) is left alone: the header says "account unknown".
func (a *App) refreshAccount(name string) {
	if name == "" {
		name = harness.Default
	}
	h, err := a.harness(name)
	if err != nil || !h.Caps().Account {
		return
	}
	u := &a.usage
	u.mu.Lock()
	c := u.cache(name)
	u.mu.Unlock()
	c.refresh.Lock()
	defer c.refresh.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), accountTimeout)
	defer cancel()
	acct, err := h.Account(ctx)
	u.mu.Lock()
	c.account, c.acctErr, c.checkedAt = acct, "", time.Now()
	if err != nil {
		c.account, c.acctErr = nil, err.Error()
	}
	u.mu.Unlock()
	if err != nil {
		log.Printf("ERROR usage: could not fetch %s account info: %v", name, err)
	}
	a.hub.Publish("usage", a.usageSnapshot())
}

// refreshAccounts refreshes every registered harness that has the account
// capability, in parallel so one slow CLI does not delay the others.
func (a *App) refreshAccounts() {
	var wg sync.WaitGroup
	for _, name := range harness.Names() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.refreshAccount(name)
		}()
	}
	wg.Wait()
}

// recordUsage merges usage-limit updates a harness read from its stream into that
// harness's latest limit per window, publishes the new snapshot and saves it. It
// does not change any card: whether a run stops is decided by RateLimited alone.
func (a *App) recordUsage(name string, updates []harness.UsageLimit) {
	if name == "" {
		name = harness.Default
	}
	now := time.Now().UTC()
	u := &a.usage
	u.mu.Lock()
	c := u.cache(name)
	if c.limits == nil {
		c.limits = map[string]*harness.UsageLimit{}
	}
	n := 0
	for _, up := range updates {
		if up.Type == "" {
			continue
		}
		l := c.limits[up.Type]
		if l == nil {
			l = &harness.UsageLimit{Type: up.Type}
			c.limits[up.Type] = l
		}
		mergeLimit(l, up)
		l.ObservedAt = now
		n++
	}
	u.mu.Unlock()
	if n == 0 {
		return
	}

	a.hub.Publish("usage", a.usageSnapshot())
	a.saveUsage()
}

// mergeLimit applies the fields an update reports; a field it leaves out keeps
// its earlier value.
func mergeLimit(l *harness.UsageLimit, up harness.UsageLimit) {
	if up.Status != "" {
		l.Status = up.Status
	}
	if up.ResetsAt > 0 {
		l.ResetsAt = up.ResetsAt
	}
	if up.Utilization != nil {
		l.Utilization = up.Utilization
	}
	if up.SurpassedThreshold != nil {
		l.SurpassedThreshold = up.SurpassedThreshold
	}
	if up.OverageStatus != "" {
		l.OverageStatus = up.OverageStatus
	}
	if up.OverageResetsAt > 0 {
		l.OverageResetsAt = up.OverageResetsAt
	}
	if up.IsUsingOverage != nil {
		l.IsUsingOverage = up.IsUsingOverage
	}
}

// saveUsage stores every harness's limits under usageSettingKey.
func (a *App) saveUsage() {
	a.usage.mu.Lock()
	all := map[string][]harness.UsageLimit{}
	for name, c := range a.usage.by {
		if len(c.limits) > 0 {
			all[name] = sortedLimits(c.limits)
		}
	}
	a.usage.mu.Unlock()
	data, err := json.Marshal(all)
	if err == nil {
		err = a.store.SetKV(usageSettingKey, string(data))
	}
	if err != nil {
		log.Printf("ERROR usage: could not save usage limits: %v", err)
	}
}

// loadUsage restores the limits saved before a restart. Before harnesses were
// pluggable the value was a plain array of Claude's limits: that is read as
// claude's.
func (a *App) loadUsage() {
	raw, err := a.store.GetKV(usageSettingKey)
	if err != nil || raw == "" {
		return
	}
	var all map[string][]harness.UsageLimit
	if json.Unmarshal([]byte(raw), &all) != nil {
		var legacy []harness.UsageLimit
		if json.Unmarshal([]byte(raw), &legacy) != nil {
			return
		}
		all = map[string][]harness.UsageLimit{harness.Default: legacy}
	}
	u := &a.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	for name, limits := range all {
		c := u.cache(name)
		c.limits = map[string]*harness.UsageLimit{}
		for i := range limits {
			if limits[i].Type != "" {
				c.limits[limits[i].Type] = &limits[i]
			}
		}
	}
}

// handleAccount returns the cached account and usage limits of every harness.
// ?refresh=1 re-runs the account lookup first: for all harnesses, or only for
// ?harness=<name> (an unknown name is a 400).
func (a *App) handleAccount(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("harness")
	if name != "" {
		if _, err := harness.For(name); err != nil {
			writeErr(w, err)
			return
		}
	}
	if v := q.Get("refresh"); v != "" && v != "0" && v != "false" {
		if name != "" {
			a.refreshAccount(name)
		} else {
			a.refreshAccounts()
		}
	}
	writeJSON(w, 200, a.usageSnapshot())
}
