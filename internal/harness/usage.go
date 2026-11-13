package harness

import (
	"errors"
	"time"
)

// Account is who a harness's CLI is logged in as. It is shown in the board header
// only: never write it into card events or notes.
type Account struct {
	LoggedIn         bool   `json:"logged_in"`
	Email            string `json:"email,omitempty"`
	OrgName          string `json:"org_name,omitempty"`
	AuthMethod       string `json:"auth_method,omitempty"`
	APIProvider      string `json:"api_provider,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"`
}

// UsageLimit is one usage window (five_hour, seven_day, ...) a harness reported in
// its stream, whatever its status. An adapter fills only the fields a line says
// something about (zero or nil means "not reported"): the board merges the update
// into the latest value it has for the window and sets ObservedAt.
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

// ErrNoAccount is what Account returns for a harness that has no account lookup
// (Caps.Account false). The board never calls it for such a harness.
var ErrNoAccount = errors.New("this harness reports no account")
