package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSchedulerPickABIPathSnapshotOnly(t *testing.T) {
	now := time.Now()
	s := &SchedulerSnapshot{HandleEnabled: true, Accounts: []AccountView{{ID: "a", Instance: 1, Cache: CacheFresh}, {ID: "outside", Instance: 2, Cache: CacheFresh, PluginPriority: 99}}, ActiveHighestTier: map[string]struct{}{"a": {}}}
	PublishSchedulerSnapshot(s)
	before := globalState.CPAAdmission()
	raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "outside", Provider: "codex"}, {ID: "a", Provider: "codex"}}})
	encoded, err := handleSchedulerPick(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(encoded, &env); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.SchedulerPickResponse
	if err := json.Unmarshal(env.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.AuthID != "a" {
		t.Fatalf("selected %q", response.AuthID)
	}
	if after := globalState.CPAAdmission(); !equalCPAAdmission(before, after) {
		t.Fatalf("ABI pick mutated roster: before=%#v after=%#v", before, after)
	}
	_ = now
}

func TestSchedulerPickPublishesManagementObservationAsynchronously(t *testing.T) {
	store := NewPluginState(DefaultConfig())

	refresherMu.Lock()
	previousState := globalState
	globalState = store
	refresherMu.Unlock()
	replaceGlobalPickActivityPump(nil, nil)
	t.Cleanup(func() {
		stopGlobalPickActivityPump()
		refresherMu.Lock()
		globalState = previousState
		refresherMu.Unlock()
	})

	pump := globalPickActivityPump.Load()
	if pump == nil {
		t.Fatal("pick activity pump missing")
	}
	PublishSchedulerSnapshot(&SchedulerSnapshot{
		HandleEnabled:     true,
		Accounts:          []AccountView{{ID: "a", Instance: 1, Cache: CacheFresh}},
		ActiveHighestTier: map[string]struct{}{"a": {}},
		Activity:          pump.enqueue,
		Observation:       pump.enqueueObservation,
	})
	raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{
		Provider:   "codex",
		Model:      "gpt-5-codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "codex"}},
	})

	encoded, err := handleSchedulerPick(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(encoded, &env); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.SchedulerPickResponse
	if err := json.Unmarshal(env.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.AuthID != "a" || !response.Handled {
		t.Fatalf("response=%#v", response)
	}

	if !waitForCondition(t, time.Second, func() bool {
		snapshot := store.Snapshot(time.Now())
		if snapshot.LastSelected != "a" || snapshot.LastReason != "selected" || snapshot.LastCodexActivityAt.IsZero() {
			return false
		}
		for _, entry := range snapshot.Logs {
			if entry.Event == "scheduler.selected" && entry.Fields["auth_id"] == "a" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("management observation missing after scheduler pick: %#v", store.Snapshot(time.Now()))
	}
}

func TestDisabledSchedulerStillObservesCodexRequestAsUnhandled(t *testing.T) {
	store := NewPluginState(DefaultConfig())

	refresherMu.Lock()
	previousState := globalState
	globalState = store
	refresherMu.Unlock()
	replaceGlobalPickActivityPump(nil, nil)
	t.Cleanup(func() {
		stopGlobalPickActivityPump()
		refresherMu.Lock()
		globalState = previousState
		refresherMu.Unlock()
	})

	pump := globalPickActivityPump.Load()
	PublishSchedulerSnapshot(&SchedulerSnapshot{
		HandleEnabled: false,
		Observation:   pump.enqueueObservation,
	})
	raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{
		Provider:   "codex",
		Model:      "gpt-5-codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "codex"}},
	})

	if _, err := handleSchedulerPick(raw); err != nil {
		t.Fatal(err)
	}
	if !waitForCondition(t, time.Second, func() bool {
		snapshot := store.Snapshot(time.Now())
		if snapshot.LastCodexActivityAt.IsZero() {
			return false
		}
		for _, entry := range snapshot.Logs {
			if entry.Event == "scheduler.unhandled" && entry.Fields["reason"] == "handle_disabled" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("disabled Codex observation missing: %#v", store.Snapshot(time.Now()))
	}
}

func TestProductionEvidenceQueueAndDynamicTrial(t *testing.T) {
	now := time.Now()
	trials := NewTrialRegistry()
	intents := make(chan EvidenceIntent, 1)
	PublishSchedulerSnapshot(&SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, Trials: trials, EvidenceIntents: intents, Accounts: []AccountView{{ID: "a", Instance: 11, Cache: CacheUnknown}}, ActiveHighestTier: map[string]struct{}{"a": {}}})
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "codex"}}}
	if got := schedulerPickPublished(req, now); got.AuthID != "a" {
		t.Fatalf("first=%#v", got)
	}
	trials.Advance(11, now.Add(60*time.Second))
	if trials.State(11, now) != TrialActive {
		t.Fatal("enqueued but unconsumed evidence was not marked pending before pick returned")
	}
	select {
	case intent := <-intents:
		if intent.Instance != 11 || intent.AuthID != "a" {
			t.Fatalf("intent=%#v", intent)
		}
	default:
		t.Fatal("trial intent missing")
	}
	if got := schedulerPickPublished(req, now.Add(time.Second)); got.AuthID != "" {
		t.Fatalf("active trial selected again: %#v", got)
	}
	trials.ObserveEvidence(11, Evidence{Kind: EvidenceRequestSuccess, At: now.Add(2 * time.Second)})
	if got := schedulerPickPublished(req, now.Add(3*time.Second)); got.AuthID != "a" {
		t.Fatalf("evidence not immediately visible: %#v", got)
	}
}

func TestUsageSuccessHandlerClearsTrialButProbeSuccessDoesNot(t *testing.T) {
	now := time.Now()
	trials := NewTrialRegistry()
	intents := make(chan EvidenceIntent, 2)
	PublishSchedulerSnapshot(&SchedulerSnapshot{HandleEnabled: true, Trials: trials, EvidenceIntents: intents, Accounts: []AccountView{{ID: "success", Instance: 41, Cache: CacheUnknown}}, ActiveHighestTier: map[string]struct{}{"success": {}}})
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "success", Provider: "codex"}}}
	if got := schedulerPickPublished(req, now); got.AuthID != "success" {
		t.Fatalf("begin=%#v", got)
	}
	raw, _ := json.Marshal(pluginapi.UsageRecord{Provider: "codex", AuthID: "success", Failed: false})
	if _, err := handleUsageHandle(raw); err != nil {
		t.Fatal(err)
	}
	if trials.State(41, now) != TrialNone {
		t.Fatal("actual successful usage handler did not clear trial")
	}
	if got := schedulerPickPublished(req, now.Add(time.Second)); got.AuthID != "success" {
		t.Fatalf("next ABI pick remained stale-excluded: %#v", got)
	}
	trials.TryBegin(42, now)
	_ = markResetProbeVerified(ResetProbeState{}, now)
	if trials.State(42, now) != TrialActive {
		t.Fatal("probe success cleared business-request trial evidence")
	}
}

func TestQuotaLimitFeedbackRepublishesExhaustionWithoutRosterMutation(t *testing.T) {
	now := time.Now()
	store := NewPluginState(DefaultConfig())
	store.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, Priority: 9, AuthIDs: map[string]struct{}{"limited": {}}})
	store.UpsertQuota(AccountState{AuthID: "limited", AuthIndex: "idx-limited", Instance: 61, Provider: "codex", Priority: 9, PlanType: "plus"})
	withGlobalRefresherForTest(t, store, nil)
	globalTrials.ObserveEvidence(61, Evidence{Kind: EvidenceRequestSuccess, At: now})
	publishSchedulerState(store, map[string]struct{}{"limited": {}}, now)
	pickRaw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "limited", Provider: "codex", Priority: 9}}})
	firstRaw, err := handleSchedulerPick(pickRaw)
	if err != nil {
		t.Fatal(err)
	}
	var firstEnv envelope
	_ = json.Unmarshal(firstRaw, &firstEnv)
	var first pluginapi.SchedulerPickResponse
	_ = json.Unmarshal(firstEnv.Result, &first)
	if first.AuthID != "limited" {
		t.Fatalf("first pick=%#v", first)
	}
	usageRaw, _ := json.Marshal(pluginapi.UsageRecord{Provider: "codex", AuthID: "limited", AuthIndex: "idx-limited", Failed: true, Failure: pluginapi.UsageFailure{StatusCode: 429, Body: `{"type":"usage_limit_reached","resets_in_seconds":3600}`}})
	if _, err := handleUsageHandle(usageRaw); err != nil {
		t.Fatal(err)
	}
	after := publishedSchedulerSnapshot.Load()
	if after == nil {
		t.Fatal("snapshot missing")
	}
	if _, ok := after.ActiveHighestTier["limited"]; !ok || len(after.ActiveHighestTier) != 1 {
		t.Fatalf("active tier changed: %#v", after.ActiveHighestTier)
	}
	if len(after.Accounts) != 1 || !after.Accounts[0].TemporaryUnavailable {
		t.Fatalf("usage mutation not republished: %#v", after.Accounts)
	}
	secondRaw, err := handleSchedulerPick(pickRaw)
	if err != nil {
		t.Fatal(err)
	}
	var secondEnv envelope
	_ = json.Unmarshal(secondRaw, &secondEnv)
	var second pluginapi.SchedulerPickResponse
	_ = json.Unmarshal(secondEnv.Result, &second)
	if second.AuthID != "" || !second.Handled || second.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst {
		t.Fatalf("limited account retrialed: %#v", second)
	}
	if globalTrials.State(61, now) != TrialNone {
		t.Fatal("excluded quota-limit account started a new trial")
	}
	admission := store.CPAAdmission()
	if !admission.Observed || admission.Priority != 9 || len(admission.AuthIDs) != 1 {
		t.Fatalf("admission mutated: %#v", admission)
	}
}

func TestEvidenceConsumerMarksPendingAndQueueFullIsNonblocking(t *testing.T) {
	now := time.Now()
	previous := globalTrials
	globalTrials = NewTrialRegistry()
	t.Cleanup(func() { globalTrials = previous })
	globalTrials.TryBegin(21, now)
	consumeEvidenceIntent(EvidenceIntent{AuthID: "a", Instance: 21, BeganAt: now})
	globalTrials.Advance(21, now.Add(60*time.Second))
	if globalTrials.State(21, now) != TrialActive {
		t.Fatal("consumer did not retain pending trial at 60s")
	}
	trials := NewTrialRegistry()
	full := make(chan EvidenceIntent, 1)
	full <- EvidenceIntent{AuthID: "occupied"}
	PublishSchedulerSnapshot(&SchedulerSnapshot{HandleEnabled: true, Trials: trials, EvidenceIntents: full, Accounts: []AccountView{{ID: "b", Instance: 22, Cache: CacheUnknown}}, ActiveHighestTier: map[string]struct{}{"b": {}}})
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "b", Provider: "codex"}}}
	done := make(chan PickDecision, 1)
	go func() { done <- schedulerPickPublished(req, now) }()
	select {
	case got := <-done:
		if got.AuthID != "b" {
			t.Fatalf("pick=%#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("full evidence queue blocked pick")
	}
	trials.Advance(22, now.Add(60*time.Second))
	if trials.State(22, now) != TrialUnknown {
		t.Fatal("dropped intent trial not governed by timeout")
	}
}

func TestSchedulerPickObservesOneImmutablePublication(t *testing.T) {
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "codex"}, {ID: "b", Provider: "codex"}}}
	a := &SchedulerSnapshot{HandleEnabled: true, Accounts: []AccountView{{ID: "a", Instance: 31, Cache: CacheFresh}}, ActiveHighestTier: map[string]struct{}{"a": {}}}
	b := &SchedulerSnapshot{HandleEnabled: true, Accounts: []AccountView{{ID: "b", Instance: 32, Cache: CacheFresh}}, ActiveHighestTier: map[string]struct{}{"b": {}}}
	PublishSchedulerSnapshot(a)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			if i%2 == 0 {
				PublishSchedulerSnapshot(a)
			} else {
				PublishSchedulerSnapshot(b)
			}
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		got := schedulerPickPublished(req, time.Now())
		if got.AuthID != "a" && got.AuthID != "b" {
			t.Fatalf("mixed/torn snapshot decision=%#v", got)
		}
	}
	<-done
}

func TestPlanUpdateRepublishesEligibilityWithoutRosterSync(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	previousSnapshot := publishedSchedulerSnapshot.Load()
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previousSnapshot) })
	store := NewPluginState(DefaultConfig())
	store.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, Priority: 10, AuthIDs: map[string]struct{}{"free": {}, "paid": {}}})
	store.UpsertQuota(AccountState{AuthID: "free", Instance: 1, PlanType: "free", LastSuccessAt: now})
	store.UpsertQuota(AccountState{AuthID: "paid", Instance: 2, PlanType: "plus", LastSuccessAt: now})
	beforeVersion := store.Snapshot(now).CPAAdmissionVersion
	publishSchedulerState(store, nil, now)
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "free", Provider: "codex"}, {ID: "paid", Provider: "codex"}}}
	if decision := schedulerPickPublished(req, now); decision.AuthID != "paid" {
		t.Fatalf("initial decision = %#v, want paid", decision)
	}

	store.UpsertQuota(AccountState{AuthID: "paid", Instance: 2, PlanType: "", LastSuccessAt: now.Add(time.Minute)})
	publishSchedulerState(store, nil, now.Add(time.Minute))
	decision := schedulerPickPublished(req, now.Add(time.Minute))
	if decision.AuthID != "" || decision.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst || decision.Reason != "no_selectable_account" {
		t.Fatalf("updated decision = %#v, want existing fallback after unknown plan", decision)
	}
	if afterVersion := store.Snapshot(now.Add(time.Minute)).CPAAdmissionVersion; afterVersion != beforeVersion {
		t.Fatalf("plan update changed admission version from %d to %d", beforeVersion, afterVersion)
	}
}

func TestStrategyPickReadsPublishedSnapshotWithoutHostCallbacks(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	host := &countingProductionHost{}
	previousSnapshot := publishedSchedulerSnapshot.Load()
	previousStatePath := defaultStatePath
	invalidStatePath := filepath.Join(t.TempDir()+string(rune(0)), "state.json")
	defaultStatePath = func() string { return invalidStatePath }
	refresherMu.Lock()
	previousRefresher := globalRefresher
	globalRefresher = &QuotaRefresher{host: host}
	refresherMu.Unlock()
	t.Cleanup(func() {
		defaultStatePath = previousStatePath
		publishedSchedulerSnapshot.Store(previousSnapshot)
		refresherMu.Lock()
		globalRefresher = previousRefresher
		refresherMu.Unlock()
	})

	snapshot := &SchedulerSnapshot{
		HandleEnabled:       true,
		ExcludeFreeAccounts: true,
		SelectionStrategy:   SelectionStrategySubscriptionHigh,
		SubscriptionRanks:   map[string]int{"free": 0, "plus": 1},
		Accounts: []AccountView{
			{ID: "free", Instance: 1, Cache: CacheFresh, PlanType: "free"},
			{ID: "plus", Instance: 2, Cache: CacheFresh, PlanType: "plus"},
		},
		ActiveHighestTier: map[string]struct{}{"free": {}, "plus": {}},
	}
	PublishSchedulerSnapshot(snapshot)
	snapshot.SubscriptionRanks["free"] = 99
	snapshot.Accounts[1].PlanType = "unknown"

	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "free", Provider: "codex"}, {ID: "plus", Provider: "codex"}}}
	decision := schedulerPickPublished(req, now)
	if decision.AuthID != "plus" {
		t.Fatalf("decision = %#v, want immutable published plus selection", decision)
	}
	PublishSchedulerSnapshot(&SchedulerSnapshot{
		HandleEnabled:       true,
		ExcludeFreeAccounts: true,
		Fallback:            FallbackFillFirst,
		Accounts: []AccountView{
			{ID: "free", Instance: 1, Cache: CacheFresh, PlanType: "free"},
			{ID: "unknown", Instance: 2, Cache: CacheFresh},
		},
		ActiveHighestTier: map[string]struct{}{"free": {}, "unknown": {}},
	})
	mixedReq := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "free", Provider: "codex"}, {ID: "unknown", Provider: "codex"}}}
	if mixed := schedulerPickPublished(mixedReq, now); mixed.AuthID != "" || mixed.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst {
		t.Fatalf("mixed plan decision = %#v, want in-memory fallback", mixed)
	}
	PublishSchedulerSnapshot(&SchedulerSnapshot{
		HandleEnabled:       true,
		ExcludeFreeAccounts: true,
		Accounts: []AccountView{
			{ID: "free-a", Instance: 3, Cache: CacheFresh, PlanType: "free"},
			{ID: "free-b", Instance: 4, Cache: CacheFresh, PlanType: "free"},
		},
		ActiveHighestTier: map[string]struct{}{"free-a": {}, "free-b": {}},
	})
	freeReq := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "free-a", Provider: "codex"}, {ID: "free-b", Provider: "codex"}}}
	if allFree := schedulerPickPublished(freeReq, now); allFree.AuthID == "" || allFree.PlanFilterContext != "all_free_relaxed" {
		t.Fatalf("all-free decision = %#v, want in-memory relaxed selection", allFree)
	}
	if calls := host.total(); calls != 0 {
		t.Fatalf("strategy pick made %d Auth/HTTP host callbacks", calls)
	}
}
