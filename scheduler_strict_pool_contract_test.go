package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestLegacyActivePoolCannotOverrideHostCandidateSet(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		AdmissionObserved: true,
		ActivePool:        "group:legacy",
		Accounts: []AccountView{
			{ID: "host-only", Instance: 1, GroupID: "other", Cache: CacheFresh, PluginPriority: 10},
			{ID: "legacy", Instance: 2, GroupID: "legacy", Cache: CacheFresh, PluginPriority: 0},
		},
		ActiveHighestTier: map[string]struct{}{"host-only": {}, "legacy": {}},
	}
	PublishSchedulerSnapshot(snapshot)
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(nil) })

	decision := schedulerPickPublished(pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{{
			ID: "host-only", Provider: "codex", Priority: 10, Status: "active",
		}},
	}, now)
	if decision.AuthID != "host-only" || decision.DelegateBuiltin != "" {
		t.Fatalf("decision = %#v, want the only host-provided candidate", decision)
	}
}

func TestLegacyActivePoolDoesNotCreateStrictFailureReason(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActivePool:        "group:legacy",
		Accounts:          []AccountView{{ID: "legacy", Instance: 1, GroupID: "legacy", Cache: CacheFresh, Exhausted: true, ResetAt: now.Add(time.Hour)}},
		ActiveHighestTier: map[string]struct{}{"legacy": {}},
	}
	PublishSchedulerSnapshot(snapshot)
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(nil) })

	decision := schedulerPickPublished(pluginapi.SchedulerPickRequest{
		Provider:   "codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "legacy", Provider: "codex", Status: "active"}},
	}, now)
	if decision.Reason == "active_pool_no_candidates" || decision.Reason == "active_group_unavailable" {
		t.Fatalf("legacy active_pool produced removed strict reason: %#v", decision)
	}
	if decision.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst {
		t.Fatalf("decision = %#v, want ordinary fallback", decision)
	}
}
