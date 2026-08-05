package main

import (
	"sort"
	"strings"
	"time"
)

// Credential kinds exposed by the protected inventory. They are stable,
// non-sensitive labels derived from the host account type.
const (
	CredentialKindOAuth   = "OAUTH"
	CredentialKindAPIKey  = "API_KEY"
	CredentialKindUnknown = "UNKNOWN"
)

// Inventory lifecycle states.
const (
	InventoryStateWaiting   = "waiting_inventory"
	InventoryStateConfirmed = "confirmed"
	InventoryStateStale     = "inventory_stale"
)

// Inventory quota cache states.
const (
	InventoryQuotaMissing   = "missing"
	InventoryQuotaKnown     = "known"
	InventoryQuotaStale     = "stale"
	InventoryQuotaExhausted = "exhausted"
)

// Stable, non-sensitive scheduling eligibility reasons. Each inventory entry
// has exactly one deterministic main reason plus an ordered set of additional
// reasons.
const (
	SchedulingReasonSchedulable               = "schedulable"
	SchedulingReasonDisabled                  = "disabled"
	SchedulingReasonUnavailable               = "unavailable"
	SchedulingReasonAuthFailure               = "auth_failure"
	SchedulingReasonAPIKeyCredential          = "api_key_credential"
	SchedulingReasonNotHighestTier            = "not_highest_priority_tier"
	SchedulingReasonOutsideActivePool         = "outside_active_pool"
	SchedulingReasonFreeExcluded              = "free_excluded"
	SchedulingReasonUnknownPlan               = "unknown_plan"
	SchedulingReasonQuotaMissing              = "quota_missing"
	SchedulingReasonIdentityConflict          = "identity_conflict"
	SchedulingReasonGroupReferenceUnavailable = "group_reference_unavailable"
)

// InventoryEntry is the safe Management-facing representation of one Codex
// host auth entry. It never carries authentication paths, raw auth JSON,
// tokens, cookies, authorization headers or unsanitized status text.
type InventoryEntry struct {
	AuthID            string        `json:"auth_id"`
	AuthIndex         string        `json:"auth_index"`
	Provider          string        `json:"provider"`
	Priority          int           `json:"priority"`
	CredentialKind    string        `json:"credential_kind"`
	AccountType       string        `json:"account_type,omitempty"`
	Status            string        `json:"status,omitempty"`
	StatusMessage     string        `json:"status_message,omitempty"`
	Email             string        `json:"email,omitempty"`
	Alias             string        `json:"alias,omitempty"`
	GroupID           string        `json:"group_id,omitempty"`
	Tags              []string      `json:"tags,omitempty"`
	SchedulingReason  string        `json:"scheduling_reason"`
	AdditionalReasons []string      `json:"additional_reasons,omitempty"`
	PlanType          string        `json:"plan_type,omitempty"`
	Family            AccountFamily `json:"family,omitempty"`
	QuotaState        string        `json:"quota_state,omitempty"`
	LastRefreshAt     *time.Time    `json:"last_refresh_at,omitempty"`
	LastSuccessAt     *time.Time    `json:"last_success_at,omitempty"`
}

// InventoryCounts summarizes the protected inventory by scheduling stage for
// the current active pool. It is presentation-only and never drives roster or
// pick authority.
type InventoryCounts struct {
	ActivePoolType   string `json:"active_pool_type"`
	Total            int    `json:"total"`
	HighestTier      int    `json:"highest_tier"`
	InPool           int    `json:"in_pool"`
	InPoolCandidates int    `json:"in_pool_candidates"`
	Schedulable      int    `json:"schedulable"`
}

// ManagementInventorySnapshot is the immutable, Management-only inventory
// derived from one host.auth.list publication. Entries are copied on build so
// the decoded Management payload can never mutate the authoritative roster.
type ManagementInventorySnapshot struct {
	Entries         []InventoryEntry `json:"entries"`
	Counts          InventoryCounts  `json:"counts"`
	State           string           `json:"state"`
	Confirmed       bool             `json:"confirmed"`
	FailClosed      bool             `json:"fail_closed"`
	LastConfirmedAt *time.Time       `json:"last_confirmed_at,omitempty"`
	LastSyncAt      *time.Time       `json:"last_sync_at,omitempty"`
}

// managementInventorySnapshot derives the full protected Codex inventory from
// the roster controller's single host.auth.list publication. It only reads
// in-memory state: no host.auth.get, quota HTTP or credential file access.
func managementInventorySnapshot(roster ActiveRoster, snapshot StateSnapshot) ManagementInventorySnapshot {
	out := ManagementInventorySnapshot{
		Entries:         make([]InventoryEntry, 0, len(roster.Inventory)),
		State:           inventoryState(roster),
		Confirmed:       roster.Confirmed,
		FailClosed:      roster.Health == RosterFailClosed,
		LastConfirmedAt: optionalTime(roster.ConfirmedAt),
		LastSyncAt:      optionalTime(roster.LastSyncAt),
	}
	authoritative := make(map[string]struct{}, len(roster.Instances))
	for _, authID := range roster.Instances {
		authoritative[authID] = struct{}{}
	}
	accounts := inventoryAccountsByID(snapshot.Accounts)
	conflicts := inventoryConflictAuthIDs(snapshot.Accounts, snapshot.Annotations)
	for _, entry := range roster.Inventory {
		item, ok := deriveInventoryEntry(entry, authoritative, snapshot, accounts, conflicts)
		if !ok {
			continue
		}
		out.Entries = append(out.Entries, item)
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].AuthID < out.Entries[j].AuthID })
	out.Counts = inventoryCounts(out.Entries, snapshot.Config.ActivePool)
	return out
}

func inventoryCounts(entries []InventoryEntry, activePool string) InventoryCounts {
	poolType := activePool
	if poolType == "" {
		poolType = ActivePoolAll
	}
	counts := InventoryCounts{ActivePoolType: poolType, Total: len(entries)}
	for _, entry := range entries {
		highestTier := entry.SchedulingReason != SchedulingReasonNotHighestTier
		inPool := inventoryInActivePool(activePool, entry.GroupID)
		if highestTier {
			counts.HighestTier++
		}
		if inPool {
			counts.InPool++
			if highestTier {
				counts.InPoolCandidates++
			}
			if entry.SchedulingReason == SchedulingReasonSchedulable {
				counts.Schedulable++
			}
		}
	}
	return counts
}

func inventoryState(roster ActiveRoster) string {
	if roster.Confirmed && roster.Health == RosterHealthy {
		return InventoryStateConfirmed
	}
	if len(roster.Inventory) > 0 || !roster.ConfirmedAt.IsZero() {
		return InventoryStateStale
	}
	return InventoryStateWaiting
}

func inventoryAccountsByID(accounts []AccountState) map[string]AccountState {
	byID := make(map[string]AccountState, len(accounts))
	for _, account := range accounts {
		if account.AuthID != "" {
			byID[account.AuthID] = account
			continue
		}
		if account.AuthIndex != "" {
			byID[account.AuthIndex] = account
		}
	}
	return byID
}

func inventoryConflictAuthIDs(accounts []AccountState, annotations AnnotationState) map[string]struct{} {
	_, conflicts := ApplyAnnotationsWithConflicts(accounts, annotations)
	out := make(map[string]struct{}, len(conflicts))
	for _, conflict := range conflicts {
		for _, authID := range conflict.AccountIDs {
			out[authID] = struct{}{}
		}
	}
	return out
}

func deriveInventoryEntry(entry RosterEntry, authoritative map[string]struct{}, snapshot StateSnapshot, accounts map[string]AccountState, conflicts map[string]struct{}) (InventoryEntry, bool) {
	id := strings.TrimSpace(entry.ID)
	if id == "" || !strings.EqualFold(strings.TrimSpace(entry.Provider), "codex") {
		return InventoryEntry{}, false
	}
	priority := 0
	if entry.Priority != nil {
		priority = *entry.Priority
	}
	_, inAuthoritative := authoritative[id]
	account, cached := accounts[id]
	if !cached {
		account = AccountState{AuthID: id, AuthIndex: strings.TrimSpace(entry.AuthIndex), Email: strings.TrimSpace(entry.Email)}
		applied := ApplyAnnotations([]AccountState{account}, snapshot.Annotations)
		account = applied[0]
	}
	item := InventoryEntry{
		AuthID:         id,
		AuthIndex:      strings.TrimSpace(entry.AuthIndex),
		Provider:       "codex",
		Priority:       priority,
		CredentialKind: inventoryCredentialKind(entry),
		AccountType:    strings.TrimSpace(entry.AccountType),
		Status:         strings.ToLower(strings.TrimSpace(entry.Status)),
		StatusMessage:  sanitizeResetProbeError(entry.StatusMessage),
		Email:          strings.TrimSpace(entry.Email),
		Alias:          account.Annotation.Alias,
		GroupID:        account.Annotation.GroupID,
		Tags:           append([]string(nil), account.Annotation.Tags...),
	}
	item.SchedulingReason, item.AdditionalReasons = inventorySchedulingReasons(entry, inAuthoritative, snapshot, account, cached, conflicts)
	item.PlanType, item.Family, item.QuotaState, item.LastRefreshAt, item.LastSuccessAt = inventoryQuotaFields(account, cached)
	return item, true
}

func inventoryCredentialKind(entry RosterEntry) string {
	switch strings.ToLower(strings.TrimSpace(entry.AccountType)) {
	case "oauth":
		return CredentialKindOAuth
	case "api_key":
		return CredentialKindAPIKey
	default:
		return CredentialKindUnknown
	}
}

func inventoryAuthFailureStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "failed", "auth_failure":
		return true
	default:
		return false
	}
}

// inventorySchedulingReasons returns the deterministic main reason and the
// stable additional reasons for one inventory entry. It never mutates roster
// authority: reasons are presentation-only.
func inventorySchedulingReasons(entry RosterEntry, inAuthoritative bool, snapshot StateSnapshot, account AccountState, cached bool, conflicts map[string]struct{}) (string, []string) {
	main := SchedulingReasonSchedulable
	switch {
	case entry.Disabled:
		main = SchedulingReasonDisabled
	case entry.Unavailable:
		main = SchedulingReasonUnavailable
	case inventoryAuthFailureStatus(entry.Status):
		main = SchedulingReasonAuthFailure
	case inventoryCredentialKind(entry) == CredentialKindAPIKey:
		main = SchedulingReasonAPIKeyCredential
	case !inAuthoritative:
		main = SchedulingReasonNotHighestTier
	case !inventoryInActivePool(snapshot.Config.ActivePool, account.Annotation.GroupID):
		main = SchedulingReasonOutsideActivePool
	default:
		switch activeSelectionEligibility(snapshot.Config, account.PlanType) {
		case "unknown_plan":
			main = SchedulingReasonUnknownPlan
		case "all_free_only":
			main = SchedulingReasonFreeExcluded
		}
	}
	additional := make([]string, 0, 3)
	if _, conflict := conflicts[entry.ID]; conflict {
		additional = append(additional, SchedulingReasonIdentityConflict)
	}
	if groupID := account.Annotation.GroupID; groupID != "" {
		if _, defined := snapshot.Annotations.Groups[groupID]; !defined {
			additional = append(additional, SchedulingReasonGroupReferenceUnavailable)
		}
	}
	if main != SchedulingReasonSchedulable && !cached {
		additional = append(additional, SchedulingReasonQuotaMissing)
	}
	return main, additional
}

func inventoryInActivePool(activePool, groupID string) bool {
	switch activePool {
	case ActivePoolAll, "":
		return true
	case ActivePoolUngrouped:
		return strings.TrimSpace(groupID) == ""
	default:
		return strings.HasPrefix(activePool, activePoolGroup) && groupID == strings.TrimPrefix(activePool, activePoolGroup)
	}
}

// inventoryQuotaFields merges existing non-sensitive plan/quota cache into an
// inventory entry. Entries without cache are marked missing and never trigger
// credential or quota I/O.
func inventoryQuotaFields(account AccountState, cached bool) (string, AccountFamily, string, *time.Time, *time.Time) {
	if !cached {
		return "", AccountFamilyUnknown, InventoryQuotaMissing, nil, nil
	}
	planType := normalizePlanType(account.PlanType)
	family := account.Family
	quotaState := InventoryQuotaKnown
	if quotaCacheKnown(account) {
		if quotaWindowsExhausted(account.Quota) {
			quotaState = InventoryQuotaExhausted
		}
		if account.Stale {
			quotaState = InventoryQuotaStale
		}
	} else {
		quotaState = InventoryQuotaMissing
	}
	return planType, family, quotaState, optionalTime(account.LastRefreshAt), optionalTime(account.LastSuccessAt)
}

func quotaCacheKnown(account AccountState) bool {
	quota := account.Quota
	return account.PlanType != "" ||
		quota.FiveHour != nil ||
		quota.LongWindow != nil ||
		len(quota.CodeReviewWindows) > 0 ||
		len(quota.AdditionalWindows) > 0 ||
		len(quota.ResetCredits) > 0
}

func quotaWindowsExhausted(quota ParsedQuota) bool {
	for _, window := range []*QuotaWindow{quota.FiveHour, quota.LongWindow} {
		if window != nil && window.Exhausted {
			return true
		}
	}
	for _, windows := range [][]QuotaWindow{quota.CodeReviewWindows, quota.AdditionalWindows} {
		for _, window := range windows {
			if window.Exhausted {
				return true
			}
		}
	}
	return false
}
