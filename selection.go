package main

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

type AvailabilityClass uint8

const (
	Preferred AvailabilityClass = iota
	Opportunistic
	Excluded
)

type CacheClass uint8

const (
	CacheFresh CacheClass = iota
	CacheAging
	CacheUnknown
	CacheStale
)

type TrialState uint8

const (
	TrialNone TrialState = iota
	TrialActive
	TrialUnknown
)

type CircuitClass uint8

const (
	CircuitClosed CircuitClass = iota
	CircuitOpen
	CircuitHalfOpen
)

type AccountView struct {
	ID                    string
	AuthIndex             string
	Instance              AuthInstanceID
	PluginPriority        int
	Family                AccountFamily
	Cache                 CacheClass
	LastKnownAvailable    bool
	Exhausted             bool
	ResetAt               time.Time
	AuthBlocked           bool
	Circuit               CircuitClass
	TemporaryUnavailable  bool
	Trial                 TrialState
	Expiry                time.Time
	RemainingQuota        float64
	PlanType              string
	SubscriptionExpiresAt time.Time
	QuotaScore            QuotaScore
}

func effectivePlanType(quotaPlan, identityPlan string) string {
	if normalized := normalizePlanType(quotaPlan); normalized != "" {
		return normalized
	}
	return normalizePlanType(identityPlan)
}

func futureSubscriptionExpiry(expiresAt, now time.Time) (time.Time, bool) {
	return expiresAt, !expiresAt.IsZero() && expiresAt.After(now)
}

type Candidate struct{ ID, Provider string }

type SelectionResult struct {
	AuthID         string
	Instance       AuthInstanceID
	Class          AvailabilityClass
	Trial          bool
	Fallback       bool
	EvidenceSource string
	Reason         string
	Ordered        []AccountView
}

func ClassifyAccount(a AccountView, now time.Time) AvailabilityClass {
	if a.AuthBlocked || a.Circuit == CircuitOpen || a.TemporaryUnavailable || a.Trial != TrialNone {
		return Excluded
	}
	if a.Exhausted && a.ResetAt.After(now) {
		return Excluded
	}
	if a.Cache == CacheFresh || a.Cache == CacheAging {
		if !a.Exhausted {
			return Preferred
		}
		return Opportunistic
	}
	if a.Cache == CacheUnknown || (a.Cache == CacheStale && a.LastKnownAvailable) {
		return Opportunistic
	}
	return Excluded
}

func SelectAccount(snapshot SchedulerSnapshot, candidates []Candidate, now time.Time) SelectionResult {
	return selectAccountSkipping(snapshot, candidates, now, nil, nil)
}

func selectAccountSkipping(snapshot SchedulerSnapshot, candidates []Candidate, now time.Time, skip map[AuthInstanceID]struct{}, trials *TrialRegistry) SelectionResult {
	eligible := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		if c.ID != "" && c.Provider == "codex" {
			if _, ok := snapshot.ActiveHighestTier[c.ID]; ok {
				eligible[c.ID] = struct{}{}
			}
		}
	}
	byClass := map[AvailabilityClass][]AccountView{Preferred: {}, Opportunistic: {}}
	for _, a := range snapshot.Accounts {
		if _, blocked := skip[a.Instance]; blocked {
			continue
		}
		if _, ok := eligible[a.ID]; !ok {
			continue
		}
		if trials != nil {
			trials.Advance(a.Instance, now)
			a.Trial = trials.State(a.Instance, now)
		}
		class := ClassifyAccount(a, now)
		if class != Excluded {
			byClass[class] = append(byClass[class], a)
		}
	}
	for _, class := range []AvailabilityClass{Preferred, Opportunistic} {
		accounts := byClass[class]
		policy := SelectionPolicy{Strategy: snapshot.SelectionStrategy, MonthlyMode: snapshot.MonthlyMode, SubscriptionRanks: snapshot.SubscriptionRanks, Now: now}
		sort.Slice(accounts, func(i, j int) bool { return accountViewLess(accounts[i], accounts[j], policy) })
		if len(accounts) > 0 {
			result := SelectionResult{AuthID: accounts[0].ID, Instance: accounts[0].Instance, Class: class, Trial: class == Opportunistic, Reason: "selected", Ordered: accounts}
			if result.Trial {
				result.EvidenceSource = "trial_evidence"
			}
			return result
		}
	}
	return SelectionResult{Reason: "no_selectable_account", Fallback: snapshot.Fallback == FallbackFillFirst}
}

type SelectionPolicy struct {
	Strategy          SelectionStrategy
	MonthlyMode       MonthlyMode
	SubscriptionRanks map[string]int
	Now               time.Time
}

func subscriptionRanks(order []string) map[string]int {
	if len(order) == 0 {
		return nil
	}
	ranks := make(map[string]int, len(order))
	for rank, plan := range order {
		ranks[normalizePlanType(plan)] = rank
	}
	return ranks
}

func accountViewLess(a, b AccountView, policy SelectionPolicy) bool {
	if a.PluginPriority != b.PluginPriority {
		return a.PluginPriority > b.PluginPriority
	}
	switch policy.Strategy {
	case SelectionStrategyQuotaHigh, SelectionStrategyQuotaLow:
		if a.QuotaScore.Known != b.QuotaScore.Known {
			return a.QuotaScore.Known
		}
		if a.QuotaScore.Known && a.QuotaScore.Remaining != b.QuotaScore.Remaining {
			if policy.Strategy == SelectionStrategyQuotaHigh {
				return a.QuotaScore.Remaining > b.QuotaScore.Remaining
			}
			return a.QuotaScore.Remaining < b.QuotaScore.Remaining
		}
	case SelectionStrategySubscriptionHigh, SelectionStrategySubscriptionLow:
		aRank, aKnown := policy.SubscriptionRanks[normalizePlanType(a.PlanType)]
		bRank, bKnown := policy.SubscriptionRanks[normalizePlanType(b.PlanType)]
		if aKnown != bKnown {
			return aKnown
		}
		if aKnown && aRank != bRank {
			if policy.Strategy == SelectionStrategySubscriptionHigh {
				return aRank > bRank
			}
			return aRank < bRank
		}
	case SelectionStrategyExpirySoon:
		aExpiry, aKnown := futureSubscriptionExpiry(a.SubscriptionExpiresAt, policy.Now)
		bExpiry, bKnown := futureSubscriptionExpiry(b.SubscriptionExpiresAt, policy.Now)
		if aKnown != bKnown {
			return aKnown
		}
		if aKnown && !aExpiry.Equal(bExpiry) {
			return aExpiry.Before(bExpiry)
		}
	default:
		return legacyAccountViewLess(a, b, policy.MonthlyMode)
	}
	return a.ID < b.ID
}

func strategySortValue(account AccountView, policy SelectionPolicy) (bool, string) {
	switch policy.Strategy {
	case SelectionStrategyQuotaHigh, SelectionStrategyQuotaLow:
		if account.QuotaScore.Known {
			return true, strconv.FormatFloat(account.QuotaScore.Remaining, 'f', -1, 64) + "%"
		}
	case SelectionStrategySubscriptionHigh, SelectionStrategySubscriptionLow:
		plan := normalizePlanType(account.PlanType)
		if rank, known := policy.SubscriptionRanks[plan]; known {
			return true, fmt.Sprintf("%s (rank %d)", plan, rank)
		}
	case SelectionStrategyExpirySoon:
		if expiresAt, known := futureSubscriptionExpiry(account.SubscriptionExpiresAt, policy.Now); known {
			return true, expiresAt.UTC().Format(time.RFC3339)
		}
	}
	return false, "unknown"
}

func legacyAccountViewLess(a, b AccountView, mode MonthlyMode) bool {
	if mode == MonthlyModePriority && a.Family != b.Family {
		return a.Family == AccountFamilyMonthly
	}
	if !a.Expiry.Equal(b.Expiry) {
		if a.Expiry.IsZero() {
			return false
		}
		if b.Expiry.IsZero() {
			return true
		}
		return a.Expiry.Before(b.Expiry)
	}
	if a.RemainingQuota != b.RemainingQuota {
		return a.RemainingQuota > b.RemainingQuota
	}
	return a.ID < b.ID
}
