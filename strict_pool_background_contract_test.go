package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jeffery/codex-quota-scheduler/internal/refactorgate"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagementQueueUsesHostCandidatesWithoutLegacyPoolFiltering(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	used := 20.0
	state := StateSnapshot{
		Config: Config{ActivePool: "group:legacy"},
		Accounts: []AccountState{
			{AuthID: "host-only", Provider: "codex", Priority: 10, Family: AccountFamilyWeekly, Annotation: AccountAnnotation{GroupID: "other"}, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &used, ResetAt: now.Add(time.Hour)}}},
			{AuthID: "legacy", Provider: "codex", Priority: 10, Family: AccountFamilyWeekly, Annotation: AccountAnnotation{GroupID: "legacy"}, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &used, ResetAt: now.Add(time.Hour)}}},
		},
		CPAAdmission: CPAAdmissionState{Observed: true, AuthIDs: map[string]struct{}{"host-only": {}, "legacy": {}}},
		Now:          now,
	}
	ordered := BuildOrderedAccounts(pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "host-only", Provider: "codex", Priority: 10, Status: "active"},
		},
	}, state, now)
	if len(ordered) != 1 || ordered[0].AuthID != "host-only" {
		t.Fatalf("ordered accounts = %#v, want only host-only", ordered)
	}
}

func TestSchedulerPickPathStaticZeroIOGate(t *testing.T) {
	violations, err := refactorgate.Analyze(".", "handleSchedulerPick")
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("scheduler.pick path violates zero-I/O gate: %#v", violations)
	}
}

func TestSchedulerPickDoesNotReadStatePathOrHost(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	previousPath := defaultStatePath
	defaultStatePath = func() string { return filepath.Join(t.TempDir(), "missing", "state.json") }
	t.Cleanup(func() { defaultStatePath = previousPath })
	previous := publishedSchedulerSnapshot.Load()
	PublishSchedulerSnapshot(&SchedulerSnapshot{
		HandleEnabled: true, AdmissionObserved: true,
		Accounts:          []AccountView{{ID: "auth", Instance: 1, Cache: CacheFresh}},
		ActiveHighestTier: map[string]struct{}{"auth": {}},
	})
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previous) })
	decision := schedulerPickPublished(pluginapi.SchedulerPickRequest{
		Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth", Provider: "codex"}},
	}, now)
	if decision.AuthID != "auth" {
		t.Fatalf("decision = %#v, want in-memory OAuth selection", decision)
	}
}
