package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func withPersistedStrategyConfig(t *testing.T, persisted Config) *PluginState {
	t.Helper()
	dir := t.TempDir()
	previousPath := defaultStatePath
	previousState := globalState
	previousSnapshot := publishedSchedulerSnapshot.Load()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	globalState = NewPluginState(DefaultConfig())
	t.Cleanup(func() {
		defaultStatePath = previousPath
		globalState = previousState
		publishedSchedulerSnapshot.Store(previousSnapshot)
	})
	if err := SaveUserData(semanticStatePaths(defaultStatePath()).UserData, PluginDiskState{Config: persisted}); err != nil {
		t.Fatalf("save persisted config: %v", err)
	}
	return globalState
}

func TestConfigureLifecycleStrategyOverridesPersistedStrategy(t *testing.T) {
	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategyExpirySoon
	store := withPersistedStrategyConfig(t, persisted)

	if err := configure(lifecyclePayload(t, "selection_strategy: quota_low\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := store.Config().SelectionStrategy; got != SelectionStrategyQuotaLow {
		t.Fatalf("runtime strategy = %q, want %q", got, SelectionStrategyQuotaLow)
	}
	if snapshot := publishedSchedulerSnapshot.Load(); snapshot == nil || snapshot.SelectionStrategy != SelectionStrategyQuotaLow {
		t.Fatalf("published snapshot = %#v, want strategy %q", snapshot, SelectionStrategyQuotaLow)
	}
}

func TestConfigureMissingLifecycleStrategyRestoresPersistedStrategy(t *testing.T) {
	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategyExpirySoon
	store := withPersistedStrategyConfig(t, persisted)

	if err := configure(lifecyclePayload(t, "quota_refresh_interval: 1h\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := store.Config().SelectionStrategy; got != SelectionStrategyExpirySoon {
		t.Fatalf("runtime strategy = %q, want persisted %q", got, SelectionStrategyExpirySoon)
	}
}

func TestConfigureExplicitEmptyLifecycleStrategyRestoresLegacy(t *testing.T) {
	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategyExpirySoon
	store := withPersistedStrategyConfig(t, persisted)

	if err := configure(lifecyclePayload(t, "selection_strategy: \"\"\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := store.Config().SelectionStrategy; got != "" {
		t.Fatalf("runtime strategy = %q, want legacy empty strategy", got)
	}
}

func TestConfigureExplicitNullLifecycleStrategyRestoresLegacy(t *testing.T) {
	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategyExpirySoon
	store := withPersistedStrategyConfig(t, persisted)

	if err := configure(lifecyclePayload(t, "selection_strategy: null\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := store.Config().SelectionStrategy; got != "" {
		t.Fatalf("runtime strategy = %q, want legacy empty strategy", got)
	}
}

func TestConfigureExplicitEmptySubscriptionOrderOverridesPersistedOrder(t *testing.T) {
	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategyQuotaLow
	persisted.SubscriptionOrder = []string{"free", "plus"}
	store := withPersistedStrategyConfig(t, persisted)

	if err := configure(lifecyclePayload(t, "subscription_order: []\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := store.Config().SubscriptionOrder; len(got) != 0 {
		t.Fatalf("subscription order = %#v, want explicit empty order", got)
	}
}

func TestConfigureExplicitNullSubscriptionOrderOverridesPersistedOrder(t *testing.T) {
	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategyQuotaLow
	persisted.SubscriptionOrder = []string{"free", "plus"}
	store := withPersistedStrategyConfig(t, persisted)

	if err := configure(lifecyclePayload(t, "subscription_order: null\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := store.Config().SubscriptionOrder; len(got) != 0 {
		t.Fatalf("subscription order = %#v, want explicit null order", got)
	}
}

func TestConfigureExplicitSubscriptionOrderOverridesPersistedOrder(t *testing.T) {
	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategySubscriptionHigh
	persisted.SubscriptionOrder = []string{"free", "plus"}
	store := withPersistedStrategyConfig(t, persisted)

	if err := configure(lifecyclePayload(t, "subscription_order: [free, pro]\n")); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := store.Config().SubscriptionOrder; !reflect.DeepEqual(got, []string{"free", "pro"}) {
		t.Fatalf("subscription order = %#v, want explicit order", got)
	}
}

func TestConfigureValidatesMergedSubscriptionStrategy(t *testing.T) {
	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategySubscriptionHigh
	persisted.SubscriptionOrder = []string{"free", "plus"}
	store := withPersistedStrategyConfig(t, persisted)
	before := store.Config()

	if err := configure(lifecyclePayload(t, "subscription_order: []\n")); err == nil {
		t.Fatal("configure accepted merged subscription strategy without subscription_order")
	}
	if got := store.Config(); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed configure changed runtime config: before=%#v after=%#v", before, got)
	}
}

func TestInvalidMergedLifecycleConfigDoesNotMigrateLegacyState(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "state.json")
	previousPath := defaultStatePath
	previousState := globalState
	previousSnapshot := publishedSchedulerSnapshot.Load()
	defaultStatePath = func() string { return legacyPath }
	globalState = NewPluginState(DefaultConfig())
	t.Cleanup(func() {
		defaultStatePath = previousPath
		globalState = previousState
		publishedSchedulerSnapshot.Store(previousSnapshot)
	})

	persisted := DefaultConfig()
	persisted.SelectionStrategy = SelectionStrategySubscriptionHigh
	persisted.SubscriptionOrder = []string{"free", "plus"}
	if err := SavePluginDiskState(legacyPath, PluginDiskState{Config: persisted}); err != nil {
		t.Fatalf("save legacy state: %v", err)
	}

	if err := configure(lifecyclePayload(t, "subscription_order: []\n")); err == nil {
		t.Fatal("configure accepted merged subscription strategy without subscription_order")
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("invalid configure changed legacy state: %v", err)
	}
	paths := semanticStatePaths(legacyPath)
	if _, err := os.Stat(paths.UserData); !os.IsNotExist(err) {
		t.Fatalf("invalid configure created user data: %v", err)
	}
	if _, err := os.Stat(paths.Legacy + ".migrated"); !os.IsNotExist(err) {
		t.Fatalf("invalid configure renamed legacy state: %v", err)
	}
}

func TestWaitingRosterFallbackKeepsStrategyAndCandidateContext(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := NewPluginState(DefaultConfig())
	previousSnapshot := publishedSchedulerSnapshot.Load()
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previousSnapshot) })
	PublishSchedulerSnapshot(&SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		SelectionStrategy: SelectionStrategyQuotaLow,
		ActiveHighestTier: map[string]struct{}{},
	})
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-a", Provider: "codex"},
			{ID: "auth-a", Provider: "codex"},
			{ID: "other", Provider: "openai"},
		},
	}

	decision := schedulerPickPublished(req, now)
	logSchedulerDecision(store, req, decision, now)
	logs := store.Snapshot(now).Logs
	if len(logs) != 1 {
		t.Fatalf("logs len = %d, want 1", len(logs))
	}
	fields := logs[0].Fields
	if fields["selection_strategy"] != string(SelectionStrategyQuotaLow) {
		t.Fatalf("selection_strategy = %#v, want %q; fields=%#v", fields["selection_strategy"], SelectionStrategyQuotaLow, fields)
	}
	if fields["strategy_known"] != false || fields["strategy_value"] != "unknown" {
		t.Fatalf("strategy diagnostics = %#v/%#v, want false/unknown; fields=%#v", fields["strategy_known"], fields["strategy_value"], fields)
	}
	if fields["candidate_count"] != 1 || fields["admitted_count"] != 0 {
		t.Fatalf("candidate context = %#v/%#v, want 1/0; fields=%#v", fields["candidate_count"], fields["admitted_count"], fields)
	}
	if fields["ordered_count"] != 0 || fields["reason"] != "waiting_roster" {
		t.Fatalf("fallback context = ordered:%#v reason:%#v, want 0/waiting_roster; fields=%#v", fields["ordered_count"], fields["reason"], fields)
	}
	if fields["unavailable_summary"] != "waiting_roster" {
		t.Fatalf("unavailable_summary = %#v, want waiting_roster; fields=%#v", fields["unavailable_summary"], fields)
	}
	if fields["fallback"] != pluginapi.SchedulerBuiltinFillFirst {
		t.Fatalf("fallback = %#v, want fill-first; fields=%#v", fields["fallback"], fields)
	}
}

func TestFallbackDiagnosticsDistinguishCandidateAndAvailabilityFailures(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	previousSnapshot := publishedSchedulerSnapshot.Load()
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previousSnapshot) })

	t.Run("no codex candidates", func(t *testing.T) {
		PublishSchedulerSnapshot(&SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, SelectionStrategy: SelectionStrategyQuotaLow, ActiveHighestTier: map[string]struct{}{"auth-a": {}}})
		req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "other", Provider: "openai"}}}
		decision := schedulerPickPublished(req, now)
		if decision.Reason != "no_codex_candidates" || decision.CandidateCount != 0 || decision.AdmittedCount != 0 {
			t.Fatalf("decision = %#v", decision)
		}
	})

	t.Run("no admitted candidates", func(t *testing.T) {
		PublishSchedulerSnapshot(&SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, SelectionStrategy: SelectionStrategyQuotaLow, ActiveHighestTier: map[string]struct{}{"auth-b": {}}})
		req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex"}}}
		decision := schedulerPickPublished(req, now)
		if decision.Reason != "no_admitted_candidates" || decision.CandidateCount != 1 || decision.AdmittedCount != 0 {
			t.Fatalf("decision = %#v", decision)
		}
	})

	t.Run("all admitted accounts unavailable", func(t *testing.T) {
		store := NewPluginState(DefaultConfig())
		secretID := "user@example.com/Authorization:SECRET"
		PublishSchedulerSnapshot(&SchedulerSnapshot{
			HandleEnabled:     true,
			Fallback:          FallbackFillFirst,
			SelectionStrategy: SelectionStrategyQuotaLow,
			ActiveHighestTier: map[string]struct{}{secretID: {}, "auth-b": {}},
			Accounts: []AccountView{
				{ID: secretID, Instance: 1, Cache: CacheFresh, AuthBlocked: true},
				{ID: "auth-b", Instance: 2, Cache: CacheFresh, Circuit: CircuitOpen},
			},
		})
		req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: secretID, Provider: "codex"}, {ID: "auth-b", Provider: "codex"}}}
		decision := schedulerPickPublished(req, now)
		logSchedulerDecision(store, req, decision, now)
		fields := store.Snapshot(now).Logs[0].Fields
		if fields["reason"] != "no_selectable_account" || fields["candidate_count"] != 2 || fields["admitted_count"] != 2 || fields["ordered_count"] != 0 {
			t.Fatalf("fields = %#v", fields)
		}
		if fields["unavailable_summary"] != "auth_failure=1; circuit_open=1" {
			t.Fatalf("unavailable_summary = %#v", fields["unavailable_summary"])
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "Authorization") || strings.Contains(string(raw), "user@example.com") {
			t.Fatalf("fallback diagnostics leaked candidate identifier: %s", raw)
		}
	})
}

func TestObservedEmptyAdmissionIsNotReportedAsWaitingRoster(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	result := SelectAccount(SchedulerSnapshot{AdmissionObserved: true, ActiveHighestTier: map[string]struct{}{}}, []Candidate{{ID: "auth-a", Provider: "codex"}}, now)
	if result.Reason != "no_admitted_candidates" {
		t.Fatalf("observed empty admission reason = %q", result.Reason)
	}
}

func TestLegacyPickFallbackSummaryDoesNotLeakAuthID(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	secretID := "user@example.com/Authorization:SECRET"
	cfg := DefaultConfig()
	snapshot := StateSnapshot{
		Config:   cfg,
		Accounts: []AccountState{{AuthID: secretID, Instance: 1, Family: AccountFamilyWeekly, Refresh: AccountRefreshState{AuthFailure: true}}},
		Now:      now,
	}
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: secretID, Provider: "codex"}}}
	decision := PickCodexAccount(req, snapshot, now)
	store := NewPluginState(cfg)
	logSchedulerDecision(store, req, decision, now)
	raw, err := json.Marshal(store.Snapshot(now).Logs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "SECRET") || strings.Contains(text, "Authorization") || strings.Contains(text, "user@example.com") {
		t.Fatalf("legacy fallback diagnostics leaked auth id: %s", text)
	}
}

func TestLegacyFallbackIsExplicitInDiagnostics(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	previousSnapshot := publishedSchedulerSnapshot.Load()
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previousSnapshot) })
	store := NewPluginState(DefaultConfig())
	PublishSchedulerSnapshot(&SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, ActiveHighestTier: map[string]struct{}{}})
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex"}}}
	logSchedulerDecision(store, req, schedulerPickPublished(req, now), now)
	fields := store.Snapshot(now).Logs[0].Fields
	if fields["selection_strategy"] != "legacy" || fields["strategy_known"] != false || fields["strategy_value"] != "unknown" {
		t.Fatalf("legacy diagnostics = %#v", fields)
	}
}

func TestSkippedTrialAccountKeepsProbeWaitReason(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	snapshot := SchedulerSnapshot{
		Fallback:          FallbackFillFirst,
		ActiveHighestTier: map[string]struct{}{"auth-a": {}},
		Accounts:          []AccountView{{ID: "auth-a", Instance: 1, Cache: CacheUnknown}},
	}
	result := selectAccountSkipping(snapshot, []Candidate{{ID: "auth-a", Provider: "codex"}}, now, map[AuthInstanceID]struct{}{1: {}}, nil)
	if result.Reason != "no_selectable_account" || len(result.Unavailable) != 1 || result.Unavailable[0].Reason != "quota_probe_wait" {
		t.Fatalf("skipped trial diagnostics = %#v", result)
	}
}
