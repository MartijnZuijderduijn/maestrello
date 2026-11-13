package board

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"maestrello/internal/harness"
	"maestrello/internal/textutil"
)

// Account is who the board's claude CLI is logged in as, from `claude auth status`.
// It is shown in the board header only: never write it into card events or notes.
type Account struct {
	LoggedIn         bool   `json:"logged_in"`
	Email            string `json:"email,omitempty"`
	OrgName          string `json:"org_name,omitempty"`
	AuthMethod       string `json:"auth_method,omitempty"`
	APIProvider      string `json:"api_provider,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"`
}

// UsageLimit is the latest rate_limit_event the board has seen for one window
// (five_hour, seven_day, ...), whatever its status.
type UsageLimit struct {
	Type               string    `json:"type"`
	Status             string    `json:"status,omitempty"`      // allowed / allowed_warning / rejected
	ResetsAt           int64     `json:"resets_at,omitempty"`   // unix seconds
	Utilization        *float64  `json:"utilization,omitempty"` // fraction of the window used (may exceed 1)
	SurpassedThreshold *float64  `json:"surpassed_threshold,omitempty"`
	OverageStatus      string    `json:"overage_status,omitempty"`
	OverageResetsAt    int64     `json:"overage_resets_at,omitempty"`
	IsUsingOverage     *bool     `json:"is_using_overage,omitempty"`
	ObservedAt         time.Time `json:"observed_at"`
}

// usageSnapshot is what GET /api/account returns and the "usage" SSE event carries.
type usageSnapshot struct {
	Account      *Account     `json:"account"`
	AccountError string       `json:"account_error,omitempty"`
	CheckedAt    *time.Time   `json:"checked_at"`
	Limits       []UsageLimit `json:"limits"`
}

// usageState is the in-memory account and usage cache on App.
type usageState struct {
	mu        sync.Mutex
	account   *Account
	acctErr   string
	checkedAt time.Time
	limits    map[string]*UsageLimit

	refresh sync.Mutex // serialises `claude auth status` runs
}

const (
	accountTimeout  = 10 * time.Second
	usageSettingKey = "usage_limits" // last limits snapshot; not part of Settings
)

// windowOrder puts the common windows first in a fixed order.
var windowOrder = map[string]int{"five_hour": 0, "seven_day": 1, "seven_day_opus": 2, "seven_day_sonnet": 3, "seven_day_overage_included": 4, "overage": 5}

func (a *App) usageSnapshot() usageSnapshot {
	u := &a.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	s := usageSnapshot{AccountError: u.acctErr, Limits: []UsageLimit{}}
	if u.account != nil {
		acct := *u.account
		s.Account = &acct
	}
	if !u.checkedAt.IsZero() {
		t := u.checkedAt
		s.CheckedAt = &t
	}
	for _, l := range u.limits {
		s.Limits = append(s.Limits, *l)
	}
	sort.Slice(s.Limits, func(i, j int) bool {
		oi, iok := windowOrder[s.Limits[i].Type]
		oj, jok := windowOrder[s.Limits[j].Type]
		if iok != jok {
			return iok
		}
		if oi != oj {
			return oi < oj
		}
		return s.Limits[i].Type < s.Limits[j].Type
	})
	return s
}

// refreshAccount runs `claude auth status` once and caches the result. It never
// blocks longer than accountTimeout, and failures become an account_error.
func (a *App) refreshAccount() {
	u := &a.usage
	u.refresh.Lock()
	defer u.refresh.Unlock()
	acct, err := a.fetchAccount()
	u.mu.Lock()
	u.account, u.acctErr, u.checkedAt = acct, "", time.Now()
	if err != nil {
		u.acctErr = err.Error()
	}
	u.mu.Unlock()
	if err != nil {
		log.Printf("ERROR usage: could not fetch account info: %v", err)
	}
	a.hub.Publish("usage", a.usageSnapshot())
}

func (a *App) fetchAccount() (*Account, error) {
	ctx, cancel := context.WithTimeout(context.Background(), accountTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.claudeBin, "auth", "status", "--json")
	cmd.Env = harness.ClaudeEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	// A logged-out CLI may exit non-zero but still print its JSON status.
	if acct, ok := parseAccount(out.Bytes()); ok {
		return acct, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("claude auth status timed out after %s", accountTimeout)
	}
	if runErr != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg != "" {
			return nil, fmt.Errorf("claude auth status: %v: %s", runErr, textutil.Tail(msg, 300))
		}
		return nil, fmt.Errorf("claude auth status: %v", runErr)
	}
	return nil, errors.New("claude auth status: unrecognised output")
}

// parseAccount reads `claude auth status` JSON leniently: unknown fields are
// ignored, and a missing or mistyped field just stays empty.
func parseAccount(data []byte) (*Account, bool) {
	var m map[string]any
	if json.Unmarshal(bytes.TrimSpace(data), &m) != nil || m == nil {
		return nil, false
	}
	str := func(k string) string { s, _ := m[k].(string); return s }
	logged, hasLogged := m["loggedIn"].(bool)
	acct := &Account{
		LoggedIn: logged, Email: str("email"), OrgName: str("orgName"), AuthMethod: str("authMethod"),
		APIProvider: str("apiProvider"), SubscriptionType: str("subscriptionType"),
	}
	if acct.SubscriptionType == "" {
		acct.SubscriptionType = str("planTier")
	}
	if !hasLogged && acct.Email == "" && acct.AuthMethod == "" {
		return nil, false // some other JSON, not an auth status
	}
	if !hasLogged {
		acct.LoggedIn = true
	}
	if !acct.LoggedIn {
		return &Account{LoggedIn: false}, true
	}
	return acct, true
}

// recordRateLimit keeps the latest rate_limit_info of a stream line per window
// and publishes the new usage snapshot. It does not change any card: whether a
// run stops is decided by rateLimitRejected alone.
func (a *App) recordRateLimit(line []byte) {
	var ev struct {
		Info map[string]any `json:"rate_limit_info"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Info == nil {
		return
	}
	info := ev.Info
	now := time.Now().UTC()
	u := &a.usage
	u.mu.Lock()
	if u.limits == nil {
		u.limits = map[string]*UsageLimit{}
	}
	get := func(typ string) *UsageLimit {
		l := u.limits[typ]
		if l == nil {
			l = &UsageLimit{Type: typ}
			u.limits[typ] = l
		}
		l.ObservedAt = now
		return l
	}
	// Per-window detail, when this CLI version reports it.
	if wins, ok := info["unifiedWindows"].(map[string]any); ok {
		for typ, w := range wins {
			wm, ok := w.(map[string]any)
			if !ok || typ == "" {
				continue
			}
			l := get(typ)
			if s, ok := wm["status"].(string); ok {
				l.Status = s
			}
			if t := unixSecs(wm["resetsAt"]); t > 0 {
				l.ResetsAt = t
			}
			if f, ok := wm["utilization"].(float64); ok {
				l.Utilization = &f
			}
			if f, ok := wm["surpassedThreshold"].(float64); ok {
				l.SurpassedThreshold = &f
			}
		}
	}
	// The top-level info describes the window named by rateLimitType.
	typ, _ := info["rateLimitType"].(string)
	if typ == "" {
		typ = "unknown"
	}
	status, _ := info["status"].(string)
	if status != "" || typ != "unknown" {
		l := get(typ)
		if status != "" {
			l.Status = status
		}
		if t := unixSecs(info["resetsAt"]); t > 0 {
			l.ResetsAt = t
		}
		if f, ok := info["utilization"].(float64); ok {
			l.Utilization = &f
		}
		if f, ok := info["surpassedThreshold"].(float64); ok {
			l.SurpassedThreshold = &f
		}
		if s, ok := info["overageStatus"].(string); ok {
			l.OverageStatus = s
		}
		if t := unixSecs(info["overageResetsAt"]); t > 0 {
			l.OverageResetsAt = t
		}
		if b, ok := info["isUsingOverage"].(bool); ok {
			l.IsUsingOverage = &b
		}
	}
	u.mu.Unlock()

	snap := a.usageSnapshot()
	a.hub.Publish("usage", snap)
	if data, err := json.Marshal(snap.Limits); err == nil {
		if err := a.store.SetKV(usageSettingKey, string(data)); err != nil {
			log.Printf("ERROR usage: could not save usage limits: %v", err)
		}
	}
}

// unixSecs reads a reset time; milliseconds are converted to seconds.
func unixSecs(v any) int64 {
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return 0
	}
	if f > 1e12 {
		f /= 1000
	}
	return int64(f)
}

// loadUsage restores the last limits snapshot saved before a restart.
func (a *App) loadUsage() {
	raw, err := a.store.GetKV(usageSettingKey)
	if err != nil || raw == "" {
		return
	}
	var limits []UsageLimit
	if json.Unmarshal([]byte(raw), &limits) != nil {
		return
	}
	u := &a.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	u.limits = map[string]*UsageLimit{}
	for i := range limits {
		if limits[i].Type != "" {
			u.limits[limits[i].Type] = &limits[i]
		}
	}
}

// handleAccount returns the cached account and usage limits; ?refresh=1 re-runs
// `claude auth status` first.
func (a *App) handleAccount(w http.ResponseWriter, r *http.Request) {
	if v := r.URL.Query().Get("refresh"); v != "" && v != "0" && v != "false" {
		a.refreshAccount()
	}
	writeJSON(w, 200, a.usageSnapshot())
}
