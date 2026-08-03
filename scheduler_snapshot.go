package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type SchedulerSnapshot struct {
	HandleEnabled       bool
	ExcludeFreeAccounts bool
	Fallback            FallbackMode
	MonthlyMode         MonthlyMode
	SelectionStrategy   SelectionStrategy
	SubscriptionRanks   map[string]int
	Accounts            []AccountView
	ActiveHighestTier   map[string]struct{}
	AdmissionObserved   bool
	Trials              *TrialRegistry
	EvidenceIntents     chan<- EvidenceIntent
	AdmissionVersion    uint64
	Activity            func(pluginapi.SchedulerPickRequest, uint64, time.Time)
	Observation         func(pluginapi.SchedulerPickRequest, PickDecision, time.Time)
}

type EvidenceIntent struct {
	AuthID   string
	Instance AuthInstanceID
	BeganAt  time.Time
}

var publishedSchedulerSnapshot atomic.Pointer[SchedulerSnapshot]
var schedulerStatePublicationMu sync.Mutex

func PublishSchedulerSnapshot(snapshot *SchedulerSnapshot) {
	if snapshot == nil {
		return
	}
	copy := cloneSchedulerSnapshot(*snapshot)
	publishedSchedulerSnapshot.Store(&copy)
}
func cloneSchedulerSnapshot(s SchedulerSnapshot) SchedulerSnapshot {
	s.Accounts = append([]AccountView(nil), s.Accounts...)
	s.ActiveHighestTier = cloneStringSet(s.ActiveHighestTier)
	s.SubscriptionRanks = cloneStringIntMap(s.SubscriptionRanks)
	return s
}

func cloneStringIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for k := range in {
		out[k] = struct{}{}
	}
	return out
}

func schedulerPickPublished(req pluginapi.SchedulerPickRequest, now time.Time) PickDecision {
	snapshot := publishedSchedulerSnapshot.Load()
	if snapshot == nil {
		return PickDecision{Reason: "handle_disabled", StrategyValue: "unknown", CandidateCount: codexCandidateCount(req)}
	}
	if !requestIncludesCodex(req) {
		return PickDecision{Reason: "provider_not_codex", Strategy: snapshot.SelectionStrategy, StrategyValue: "unknown", CandidateCount: codexCandidateCount(req)}
	}
	hostSupportsEmergencyDelegate := schedulerSupportsBuiltinDelegate(req.Options, pluginapi.SchedulerBuiltinEmergencyProviderFillFirst)
	if !snapshot.HandleEnabled {
		return observeSchedulerDecision(snapshot, req, PickDecision{Reason: "handle_disabled", Strategy: snapshot.SelectionStrategy, StrategyValue: "unknown", CandidateCount: codexCandidateCount(req), HostSupportsEmergencyDelegate: hostSupportsEmergencyDelegate}, now)
	}
	if snapshot.Activity != nil {
		snapshot.Activity(req, snapshot.AdmissionVersion, now)
	}
	candidates := make([]Candidate, 0, len(req.Candidates))
	for _, c := range req.Candidates {
		candidates = append(candidates, Candidate{ID: c.ID, Provider: c.Provider})
	}
	result := selectAccountSkipping(*snapshot, candidates, now, nil, snapshot.Trials)
	var skipped map[AuthInstanceID]struct{}
	for result.AuthID != "" && result.Class == Opportunistic && (snapshot.Trials == nil || !snapshot.Trials.TryBegin(result.Instance, now)) {
		if skipped == nil {
			skipped = make(map[AuthInstanceID]struct{})
		}
		skipped[result.Instance] = struct{}{}
		result = selectAccountSkipping(*snapshot, candidates, now, skipped, snapshot.Trials)
	}
	if result.AuthID != "" && result.Class == Opportunistic {
		select {
		case snapshot.EvidenceIntents <- EvidenceIntent{AuthID: result.AuthID, Instance: result.Instance, BeganAt: now}:
			snapshot.Trials.MarkEvidencePending(result.Instance, true)
		default:
		}
	}
	if result.AuthID != "" {
		known, value := strategySortValue(result.Ordered[0], SelectionPolicy{Strategy: snapshot.SelectionStrategy, MonthlyMode: snapshot.MonthlyMode, SubscriptionRanks: snapshot.SubscriptionRanks, Now: now})
		return observeSchedulerDecision(snapshot, req, selectionPickDecision(snapshot, result, PickDecision{AuthID: result.AuthID, Handled: true, Reason: "selected", StrategyKnown: known, StrategyValue: value, HostSupportsEmergencyDelegate: hostSupportsEmergencyDelegate}), now)
	}
	if snapshot.Fallback == FallbackFillFirst {
		delegate := pluginapi.SchedulerBuiltinFillFirst
		compatibilityReason := ""
		activePoolBypassed := false
		if !schedulerRequestHasPinnedAuth(req) {
			if hostSupportsEmergencyDelegate {
				delegate = pluginapi.SchedulerBuiltinEmergencyProviderFillFirst
				activePoolBypassed = true
			} else {
				compatibilityReason = "emergency_delegate_unsupported"
			}
		}
		return observeSchedulerDecision(snapshot, req, selectionPickDecision(snapshot, result, PickDecision{
			Handled:                       true,
			DelegateBuiltin:               delegate,
			Reason:                        result.Reason,
			CompatibilityReason:           compatibilityReason,
			HostSupportsEmergencyDelegate: hostSupportsEmergencyDelegate,
			OAuthStageReason:              result.Reason,
			ActivePoolBypassed:            activePoolBypassed,
		}), now)
	}
	return observeSchedulerDecision(snapshot, req, selectionPickDecision(snapshot, result, PickDecision{Reason: result.Reason, HostSupportsEmergencyDelegate: hostSupportsEmergencyDelegate}), now)
}

func schedulerSupportsBuiltinDelegate(options pluginapi.SchedulerOptions, delegate string) bool {
	delegate = strings.TrimSpace(delegate)
	if delegate == "" {
		return false
	}
	for _, supported := range options.SupportedBuiltinDelegates {
		if strings.TrimSpace(supported) == delegate {
			return true
		}
	}
	return false
}

func schedulerRequestHasPinnedAuth(req pluginapi.SchedulerPickRequest) bool {
	if len(req.Options.Metadata) == 0 {
		return false
	}
	raw := req.Options.Metadata["pinned_auth_id"]
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []byte:
		return strings.TrimSpace(string(value)) != ""
	default:
		return false
	}
}

func selectionPickDecision(snapshot *SchedulerSnapshot, result SelectionResult, decision PickDecision) PickDecision {
	decision.Strategy = snapshot.SelectionStrategy
	if decision.StrategyValue == "" {
		decision.StrategyValue = "unknown"
	}
	decision.CandidateCount = result.CandidateCount
	decision.AdmittedCount = result.AdmittedCount
	decision.OrderedCount = len(result.Ordered)
	decision.ActiveSelectionCount = result.ActiveSelectionCount
	decision.PlanFilterContext = result.PlanFilterContext
	if decision.AuthID == "" {
		decision.UnavailableSummary = selectionUnavailableSummary(result)
	}
	return decision
}

func selectionUnavailableSummary(result SelectionResult) string {
	if len(result.Unavailable) == 0 {
		return result.Reason
	}
	counts := make(map[string]int, len(result.Unavailable))
	for _, unavailable := range result.Unavailable {
		counts[unavailable.Reason]++
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s=%d", reason, counts[reason]))
	}
	return strings.Join(parts, "; ")
}

func observeSchedulerDecision(snapshot *SchedulerSnapshot, req pluginapi.SchedulerPickRequest, decision PickDecision, now time.Time) PickDecision {
	if snapshot != nil && snapshot.Observation != nil {
		snapshot.Observation(req, decision, now)
	}
	return decision
}

func schedulerSnapshotFromState(state StateSnapshot, trials *TrialRegistry) *SchedulerSnapshot {
	active := cloneStringSet(state.CPAAdmission.AuthIDs)
	accounts := make([]AccountView, 0, len(state.Accounts))
	for _, a := range state.Accounts {
		accounts = append(accounts, accountViewFromState(a, state.Config, state.Now, trials))
	}
	var activity func(pluginapi.SchedulerPickRequest, uint64, time.Time)
	var observation func(pluginapi.SchedulerPickRequest, PickDecision, time.Time)
	if pump := globalPickActivityPump.Load(); pump != nil {
		activity = pump.enqueue
		observation = pump.enqueueObservation
	}
	return &SchedulerSnapshot{HandleEnabled: state.Config.HandleEnabled, ExcludeFreeAccounts: state.Config.ExcludeFreeAccounts, Fallback: state.Config.Fallback, MonthlyMode: state.Config.MonthlyMode, SelectionStrategy: state.Config.SelectionStrategy, SubscriptionRanks: subscriptionRanks(state.Config.SubscriptionOrder), Accounts: accounts, ActiveHighestTier: active, AdmissionObserved: state.CPAAdmission.Observed, Trials: trials, EvidenceIntents: globalEvidenceIntents, Activity: activity, Observation: observation}
}

func accountViewFromState(a AccountState, cfg Config, now time.Time, trials *TrialRegistry) AccountView {
	cache := CacheFresh
	if a.LastSuccessAt.IsZero() {
		cache = CacheUnknown
	} else if a.Stale {
		cache = CacheStale
	} else if now.Sub(a.LastSuccessAt) > cfg.QuotaRefreshInterval {
		cache = CacheAging
	}
	exhausted, reset := accountExhaustion(a, now)
	trial := TrialNone
	if trials != nil {
		trial = trials.State(a.Instance, now)
	}
	circuit := effectiveCircuitState(a.Circuit, now).EffectiveState
	quotaScore := a.BottleneckQuota
	if !quotaScore.Known {
		quotaScore = bottleneckQuotaScore(a.Quota)
	}
	circuitClass := CircuitClosed
	if circuit == CircuitStateOpen {
		circuitClass = CircuitOpen
	} else if circuit == CircuitStateHalfOpen {
		circuitClass = CircuitHalfOpen
	}
	return AccountView{
		ID: a.AuthID, AuthIndex: a.AuthIndex, Instance: a.Instance,
		PluginPriority: a.Annotation.SchedulerPriority, Family: a.Family,
		Cache: cache, LastKnownAvailable: a.LastError == "", Exhausted: exhausted,
		ResetAt: reset, AuthBlocked: a.Refresh.AuthFailure, Circuit: circuitClass,
		TemporaryUnavailable: a.TemporaryExhausted && a.TemporaryResetAt.After(now),
		Trial:                trial, Expiry: accountSortTime(a), RemainingQuota: remainingQuota(a),
		PlanType: normalizePlanType(a.PlanType), SubscriptionExpiresAt: a.SubscriptionExpiresAt,
		QuotaScore: quotaScore,
	}
}

func publishSchedulerState(state *PluginState, active map[string]struct{}, now time.Time) {
	schedulerStatePublicationMu.Lock()
	defer schedulerStatePublicationMu.Unlock()
	publishSchedulerStateLocked(state, active, now)
}

func publishSchedulerStateLocked(state *PluginState, active map[string]struct{}, now time.Time) {
	if state == nil {
		return
	}
	s := state.Snapshot(now)
	if active != nil {
		s.CPAAdmission = CPAAdmissionState{Observed: true, AuthIDs: cloneStringSet(active)}
	}
	snapshot := schedulerSnapshotFromState(s, globalTrials)
	snapshot.AdmissionVersion = s.CPAAdmissionVersion
	PublishSchedulerSnapshot(snapshot)
}
func accountExhaustion(a AccountState, now time.Time) (bool, time.Time) {
	if windowExhausted(a.Quota.LongWindow, now) {
		return true, a.Quota.LongWindow.ResetAt
	}
	if windowExhausted(a.Quota.FiveHour, now) {
		return true, a.Quota.FiveHour.ResetAt
	}
	return false, time.Time{}
}
func remainingQuota(a AccountState) float64 {
	for _, w := range []*QuotaWindow{a.Quota.LongWindow, a.Quota.FiveHour} {
		if w != nil && w.UsedPercent != nil {
			return 100 - *w.UsedPercent
		}
	}
	return 0
}
