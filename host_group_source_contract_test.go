package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestLegacyActivePoolDoesNotFilterHostProvidedCodexCandidates(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActivePool:        "group:legacy",
		Accounts: []AccountView{
			{ID: "host-outside-legacy", Instance: 1, GroupID: "host-group", Cache: CacheFresh, PluginPriority: 10},
			{ID: "host-inside-legacy", Instance: 2, GroupID: "legacy", Cache: CacheFresh, PluginPriority: 0},
		},
		ActiveHighestTier: map[string]struct{}{
			"host-outside-legacy": {},
			"host-inside-legacy":  {},
		},
	}
	publishSchedulerContractSnapshot(t, snapshot)

	decision := schedulerPickPublished(pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "host-outside-legacy", Provider: "codex", Priority: 10, Status: "active"},
			{ID: "host-inside-legacy", Provider: "codex", Priority: 10, Status: "active"},
		},
	}, now)
	if decision.AuthID != "host-outside-legacy" {
		t.Fatalf("decision = %#v, want host-provided candidate outside legacy plugin group", decision)
	}
}

func TestNoCodexCandidatesUsesProviderFallbackWithoutLegacyPoolFiltering(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActivePool:        "group:legacy",
	}
	publishSchedulerContractSnapshot(t, snapshot)

	decision := schedulerPickPublished(pluginapi.SchedulerPickRequest{
		Provider:  "codex",
		Providers: []string{"codex", "openai-compatibility"},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "provider-only", Provider: "openai-compatibility", Priority: 1, Status: "active"},
		},
		Options: pluginapi.SchedulerOptions{
			SupportedBuiltinDelegates: []string{pluginapi.SchedulerBuiltinEmergencyProviderFillFirst},
		},
	}, now)
	if !decision.Handled || decision.DelegateBuiltin != pluginapi.SchedulerBuiltinEmergencyProviderFillFirst {
		t.Fatalf("decision = %#v, want existing provider fallback", decision)
	}
}

func TestLegacyActivePoolDoesNotFilterManagementQueue(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	snapshot := StateSnapshot{
		Config: Config{ActivePool: "group:legacy"},
		Accounts: []AccountState{
			{AuthID: "queue-outside-legacy", Provider: "codex", Priority: 10, Family: AccountFamilyWeekly, Annotation: AccountAnnotation{GroupID: "host-group"}, Quota: ParsedQuota{LongWindow: &QuotaWindow{ResetAt: now.Add(time.Hour)}}},
			{AuthID: "queue-inside-legacy", Provider: "codex", Priority: 10, Family: AccountFamilyWeekly, Annotation: AccountAnnotation{GroupID: "legacy"}, Quota: ParsedQuota{LongWindow: &QuotaWindow{ResetAt: now.Add(time.Hour)}}},
		},
	}
	ordered := BuildOrderedAccounts(pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "queue-outside-legacy", Provider: "codex", Priority: 10, Status: "active"},
			{ID: "queue-inside-legacy", Provider: "codex", Priority: 10, Status: "active"},
		},
	}, snapshot, now)
	if len(ordered) != 2 {
		t.Fatalf("ordered accounts = %#v, want both host-provided candidates", ordered)
	}
}
