package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPlanEligibilityDiagnosticsCountsAndAllFreeContext(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	allFree := SchedulerSnapshot{
		ExcludeFreeAccounts: true,
		Fallback:            FallbackFillFirst,
		Accounts: []AccountView{
			{ID: "free-a", Instance: 1, Cache: CacheFresh, PlanType: "free"},
			{ID: "free-b", Instance: 2, Cache: CacheFresh, PlanType: "free"},
		},
		ActiveHighestTier: map[string]struct{}{"free-a": {}, "free-b": {}},
	}
	result := SelectAccount(allFree, []Candidate{{ID: "free-a", Provider: "codex"}, {ID: "free-b", Provider: "codex"}}, now)
	if got := result.ActiveSelectionCount; got != 2 {
		t.Fatalf("active selection count = %d, want 2", got)
	}
	if got := result.PlanFilterContext; got != "all_free_relaxed" {
		t.Fatalf("plan filter context = %q, want all_free_relaxed", got)
	}

	mixed := allFree
	mixed.Accounts[1].PlanType = ""
	result = SelectAccount(mixed, []Candidate{{ID: "free-a", Provider: "codex"}, {ID: "free-b", Provider: "codex"}}, now)
	if got := result.ActiveSelectionCount; got != 0 {
		t.Fatalf("mixed active selection count = %d, want 0", got)
	}
}

func TestPlanEligibilityLogIsAggregateAndNonSensitive(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := NewPluginState(DefaultConfig())
	secretFree := "user@example.com/Authorization:SECRET"
	secretUnknown := "auth_index=private/Cookie:secret"
	snapshot := SchedulerSnapshot{
		ExcludeFreeAccounts: true,
		Fallback:            FallbackFillFirst,
		Accounts: []AccountView{
			{ID: secretFree, Instance: 1, Cache: CacheFresh, PlanType: "free"},
			{ID: secretUnknown, Instance: 2, Cache: CacheFresh},
		},
		ActiveHighestTier: map[string]struct{}{secretFree: {}, secretUnknown: {}},
	}
	result := SelectAccount(snapshot, []Candidate{{ID: secretFree, Provider: "codex"}, {ID: secretUnknown, Provider: "codex"}}, now)
	decision := selectionPickDecision(&snapshot, result, PickDecision{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst, Reason: result.Reason})
	logSchedulerDecision(store, pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5.6-sol"}, decision, now)
	fields := store.Snapshot(now).Logs[0].Fields
	if fields["candidate_count"] != 2 || fields["admitted_count"] != 2 || fields["active_selection_count"] != 0 {
		t.Fatalf("diagnostic counts = %#v", fields)
	}
	if _, exists := fields["plan_filter_context"]; exists {
		t.Fatalf("mixed free/unknown unexpectedly has plan filter context: %#v", fields)
	}
	newDiagnostics := map[string]any{
		"active_selection_count": fields["active_selection_count"],
		"plan_filter_context":    fields["plan_filter_context"],
		"unavailable_summary":    fields["unavailable_summary"],
	}
	raw, err := json.Marshal(newDiagnostics)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"user@example.com", "auth_index", "access_token", "refresh_token", "id_token", "Authorization", "Cookie"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("plan diagnostics leaked %q: %s", forbidden, raw)
		}
	}

	allFree := snapshot
	allFree.Accounts = []AccountView{
		{ID: "free-a", Instance: 3, Cache: CacheStale, PlanType: "free"},
		{ID: "free-b", Instance: 4, Cache: CacheStale, PlanType: "free"},
	}
	allFree.ActiveHighestTier = map[string]struct{}{"free-a": {}, "free-b": {}}
	result = SelectAccount(allFree, []Candidate{{ID: "free-a", Provider: "codex"}, {ID: "free-b", Provider: "codex"}}, now)
	decision = selectionPickDecision(&allFree, result, PickDecision{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst, Reason: result.Reason})
	logSchedulerDecision(store, pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5.6-sol"}, decision, now.Add(time.Second))
	fields = store.Snapshot(now.Add(time.Second)).Logs[1].Fields
	if fields["plan_filter_context"] != "all_free_relaxed" || fields["active_selection_count"] != 2 {
		t.Fatalf("all-free log fields = %#v", fields)
	}
}

func TestManagementStatusMarksActiveSelectionEligibility(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	snapshot := StateSnapshot{Config: cfg, Now: now, Accounts: []AccountState{
		{AuthID: "free", PlanType: "free", LastSuccessAt: now},
		{AuthID: "unknown", PlanType: "", LastSuccessAt: now},
		{AuthID: "paid", PlanType: "plus", LastSuccessAt: now},
	}}
	ordered := []ScheduledAccount{{AuthID: "free"}, {AuthID: "unknown"}, {AuthID: "paid"}}
	status := BuildStatusPayload(snapshot, ordered)
	want := map[string]string{"free": "all_free_only", "unknown": "unknown_plan", "paid": "eligible"}
	for _, account := range status.Accounts {
		if got := account.ActiveSelectionEligibility; got != want[account.AuthID] {
			t.Fatalf("%s active eligibility = %q, want %q", account.AuthID, got, want[account.AuthID])
		}
	}
	html := string(RenderStatusHTML(status))
	for _, marker := range []string{"active_selection_eligibility", "主动选择资格", "Active selection eligibility"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("Management UI missing %q", marker)
		}
	}
}

func TestManagementNextAccountAppliesPlanEligibility(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	free := weeklyAccount("a-free", 1, now.Add(24*time.Hour), false)
	free.PlanType = "free"
	paid := weeklyAccount("z-paid", 1, now.Add(24*time.Hour), false)
	paid.PlanType = "plus"
	snapshot := StateSnapshot{Config: DefaultConfig(), Now: now, Accounts: []AccountState{free, paid}}
	ordered := BuildOrderedAccounts(syntheticStatusRequest(snapshot), snapshot, now)
	if len(ordered) != 2 || ordered[0].AuthID != "a-free" {
		t.Fatalf("fixture order = %#v, want free first before plan filtering", ordered)
	}
	payload := BuildStatusPayload(snapshot, ordered)
	if payload.NextAuthID != "z-paid" {
		t.Fatalf("next_auth_id = %q, want eligible paid account", payload.NextAuthID)
	}
}
