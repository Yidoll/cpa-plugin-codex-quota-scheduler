package main

import (
	"context"
	"sort"
	"strings"
	"time"
)

type HostCapability uint8

type HostAuthEligibilityReason uint8

const (
	CapabilityB HostCapability = iota
	CapabilityA
)

const (
	HostAuthEligible HostAuthEligibilityReason = iota
	HostAuthExcludedNonCodex
	HostAuthExcludedDisabled
	HostAuthExcludedUnavailable
	HostAuthExcludedMissingID
	HostAuthExcludedMissingIndex
)

func normalizeEligibleCodexAuth(id, authIndex, provider string, disabled, unavailable bool) (string, string, HostAuthEligibilityReason) {
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return "", "", HostAuthExcludedNonCodex
	}
	if disabled {
		return "", "", HostAuthExcludedDisabled
	}
	if unavailable {
		return "", "", HostAuthExcludedUnavailable
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", "", HostAuthExcludedMissingID
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return "", "", HostAuthExcludedMissingIndex
	}
	return id, authIndex, HostAuthEligible
}

// RosterEntry is the normalized host.auth.list boundary shape. Priority is a
// pointer because the v7.2.42 typed ABI uses int and cannot distinguish a
// missing JSON field from an explicit zero.
type RosterEntry struct {
	ID        string
	AuthIndex string
	Provider  string
	Priority  *int
	// Inventory-only safety fields. They describe the protected Codex
	// inventory and are never authoritative scheduling truth.
	AccountType   string
	Status        string
	StatusMessage string
	Email         string
	Disabled      bool
	Unavailable   bool
}

type HostRosterSnapshot struct {
	Capability        HostCapability
	Confirmed         bool
	Provisional       bool
	BackgroundAllowed bool
	Health            RosterHealth
	Generation        uint64
	LifecycleRevision uint64
	Entries           []RosterEntry
	ConfirmedAt       time.Time
	DegradedSince     time.Time
}

func hostRosterSnapshotFromActive(active ActiveRoster) HostRosterSnapshot {
	return HostRosterSnapshot{
		Capability: active.Capability, Confirmed: active.Confirmed, Provisional: active.Provisional,
		BackgroundAllowed: active.BackgroundAllowed, Health: active.Health, Generation: active.Generation, LifecycleRevision: active.LifecycleRevision,
		Entries: append([]RosterEntry(nil), active.Entries...), ConfirmedAt: active.ConfirmedAt, DegradedSince: active.DegradedSince,
	}
}

func normalizeHostRosterLifecycle(roster HostRosterSnapshot) HostRosterSnapshot {
	if roster.Capability == CapabilityA && roster.Health == "" {
		roster.Confirmed = true
		roster.BackgroundAllowed = true
		roster.Health = RosterHealthy
	}
	return roster
}

type HostAuthLister interface {
	ListHostAuths(context.Context) ([]RosterEntry, error)
}

type HostAuthListerFunc func(context.Context) ([]RosterEntry, error)

func (f HostAuthListerFunc) ListHostAuths(ctx context.Context) ([]RosterEntry, error) {
	return f(ctx)
}

func DetectHostRoster(ctx context.Context, host HostAuthLister, now time.Time) HostRosterSnapshot {
	if host == nil {
		return HostRosterSnapshot{Capability: CapabilityB}
	}
	entries, err := host.ListHostAuths(ctx)
	if err != nil || len(entries) == 0 {
		return HostRosterSnapshot{Capability: CapabilityB}
	}
	for _, entry := range entries {
		if entry.Priority == nil {
			return HostRosterSnapshot{Capability: CapabilityB}
		}
	}
	return HostRosterSnapshot{
		Capability:  CapabilityA,
		Entries:     append([]RosterEntry(nil), entries...),
		ConfirmedAt: now,
	}
}

func HighestCodexTier(entries []RosterEntry) (priority int, ids []string, ok bool) {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.ID == "" || entry.Provider != "codex" || entry.Priority == nil {
			continue
		}
		if !ok || *entry.Priority > priority {
			priority = *entry.Priority
			ids = ids[:0]
			clear(seen)
			ok = true
		}
		if *entry.Priority == priority {
			if _, exists := seen[entry.ID]; !exists {
				seen[entry.ID] = struct{}{}
				ids = append(ids, entry.ID)
			}
		}
	}
	if !ok {
		return 0, nil, false
	}
	sort.Strings(ids)
	return priority, ids, true
}

// eligibleRosterEntries keeps only codex entries that may participate in the
// authoritative roster. Inventory-only entries (disabled, unavailable, API-key
// credentials and auth-failure statuses) stay in the full inventory snapshot
// but never enter scheduling truth.
func eligibleRosterEntries(entries []RosterEntry) []RosterEntry {
	out := make([]RosterEntry, 0, len(entries))
	for _, entry := range entries {
		if !rosterEntrySchedulable(entry) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// rosterEntrySchedulable reports whether a retained Codex inventory entry may
// participate in the authoritative roster. API-key credentials and entries
// whose host status indicates authentication failure stay inventory-only.
func rosterEntrySchedulable(entry RosterEntry) bool {
	if entry.Disabled || entry.Unavailable {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(entry.AccountType), "api_key") {
		return false
	}
	return !inventoryAuthFailureStatus(entry.Status)
}
