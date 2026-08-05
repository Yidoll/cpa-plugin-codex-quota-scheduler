package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func emergencyDelegateRequest(candidates ...pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerPickRequest {
	return pluginapi.SchedulerPickRequest{
		Provider:   "codex",
		Model:      "gpt-emergency",
		Candidates: candidates,
		Options: pluginapi.SchedulerOptions{SupportedBuiltinDelegates: []string{
			pluginapi.SchedulerBuiltinRoundRobin,
			pluginapi.SchedulerBuiltinFillFirst,
			pluginapi.SchedulerBuiltinEmergencyProviderFillFirst,
		}},
	}
}

func publishEmergencyDelegateSnapshot(t *testing.T, snapshot *SchedulerSnapshot) {
	t.Helper()
	previous := publishedSchedulerSnapshot.Load()
	PublishSchedulerSnapshot(snapshot)
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previous) })
}

func TestSchedulerDelegatesEmergencyProviderForEveryOAuthUnavailableState(t *testing.T) {
	now := time.Now()
	resetAt := now.Add(time.Hour)
	tests := []struct {
		name     string
		snapshot SchedulerSnapshot
		req      pluginapi.SchedulerPickRequest
		want     string
	}{
		{
			name: "no oauth roster",
			snapshot: SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, AdmissionObserved: true,
				ActiveHighestTier: map[string]struct{}{}},
			req:  emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "provider-only", Provider: "codex"}),
			want: "no_admitted_candidates",
		},
		{
			name: "waiting roster",
			snapshot: SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst,
				ActiveHighestTier: map[string]struct{}{}},
			req:  emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "oauth-waiting", Provider: "codex"}),
			want: "waiting_roster",
		},
		{
			name: "quota exhausted",
			snapshot: SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, AdmissionObserved: true,
				ActiveHighestTier: map[string]struct{}{"oauth-exhausted": {}},
				Accounts:          []AccountView{{ID: "oauth-exhausted", Instance: 1, Cache: CacheFresh, Exhausted: true, ResetAt: resetAt}}},
			req:  emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "oauth-exhausted", Provider: "codex"}),
			want: "no_selectable_account",
		},
		{
			name: "circuit open",
			snapshot: SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, AdmissionObserved: true,
				ActiveHighestTier: map[string]struct{}{"oauth-circuit": {}},
				Accounts:          []AccountView{{ID: "oauth-circuit", Instance: 2, Cache: CacheFresh, Circuit: CircuitOpen}}},
			req:  emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "oauth-circuit", Provider: "codex"}),
			want: "no_selectable_account",
		},
		{
			name: "authentication failed",
			snapshot: SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, AdmissionObserved: true,
				ActiveHighestTier: map[string]struct{}{"oauth-auth-failed": {}},
				Accounts:          []AccountView{{ID: "oauth-auth-failed", Instance: 3, Cache: CacheFresh, AuthBlocked: true}}},
			req:  emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "oauth-auth-failed", Provider: "codex"}),
			want: "no_selectable_account",
		},
		{
			name: "plan eligibility excluded",
			snapshot: SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, AdmissionObserved: true, ExcludeFreeAccounts: true,
				ActiveHighestTier: map[string]struct{}{"oauth-unknown-plan": {}},
				Accounts:          []AccountView{{ID: "oauth-unknown-plan", Instance: 4, Cache: CacheFresh}}},
			req:  emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "oauth-unknown-plan", Provider: "codex"}),
			want: "no_selectable_account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publishEmergencyDelegateSnapshot(t, &tt.snapshot)
			decision := schedulerPickPublished(tt.req, now)
			if !decision.Handled || decision.AuthID != "" || decision.DelegateBuiltin != pluginapi.SchedulerBuiltinEmergencyProviderFillFirst {
				t.Fatalf("decision = %#v, want emergency delegate", decision)
			}
			if decision.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", decision.Reason, tt.want)
			}
		})
	}
}

func TestSchedulerKeepsAvailableOAuthAheadOfEmergencyProvider(t *testing.T) {
	now := time.Now()
	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActiveHighestTier: map[string]struct{}{"oauth-available": {}},
		Accounts:          []AccountView{{ID: "oauth-available", Instance: 1, Cache: CacheFresh}},
	}
	publishEmergencyDelegateSnapshot(t, snapshot)
	req := emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "oauth-available", Provider: "codex", Priority: 1})
	req.Options.Metadata = map[string]any{"emergency_provider_priority": 100}

	decision := schedulerPickPublished(req, now)
	if !decision.Handled || decision.AuthID != "oauth-available" || decision.DelegateBuiltin != "" {
		t.Fatalf("decision = %#v, want available OAuth", decision)
	}
}

func TestSchedulerKeepsAllowedOpportunisticOAuthAheadOfEmergencyProvider(t *testing.T) {
	now := time.Now()
	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActiveHighestTier: map[string]struct{}{"oauth-opportunistic": {}},
		Accounts:          []AccountView{{ID: "oauth-opportunistic", Instance: 2, Cache: CacheUnknown}},
		Trials:            NewTrialRegistry(),
		EvidenceIntents:   make(chan EvidenceIntent, 1),
	}
	publishEmergencyDelegateSnapshot(t, snapshot)
	req := emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "oauth-opportunistic", Provider: "codex", Priority: 1})
	req.Options.Metadata = map[string]any{"emergency_provider_priority": 100}

	decision := schedulerPickPublished(req, now)
	if !decision.Handled || decision.AuthID != "oauth-opportunistic" || decision.DelegateBuiltin != "" {
		t.Fatalf("decision = %#v, want allowed opportunistic OAuth", decision)
	}
}

func TestSchedulerEmergencyDelegateRespectsFallbackHandleAndPinnedControls(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		snapshot     SchedulerSnapshot
		configureReq func(*pluginapi.SchedulerPickRequest)
		wantHandled  bool
		wantDelegate string
	}{
		{
			name: "fallback empty",
			snapshot: SchedulerSnapshot{HandleEnabled: true, AdmissionObserved: true,
				ActiveHighestTier: map[string]struct{}{}},
		},
		{
			name: "handling disabled",
			snapshot: SchedulerSnapshot{HandleEnabled: false, Fallback: FallbackFillFirst,
				ActiveHighestTier: map[string]struct{}{}},
		},
		{
			name: "pinned auth keeps ordinary fallback",
			snapshot: SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, AdmissionObserved: true,
				ActiveHighestTier: map[string]struct{}{}},
			configureReq: func(req *pluginapi.SchedulerPickRequest) {
				req.Options.Metadata = map[string]any{"pinned_auth_id": "fixed-auth"}
			},
			wantHandled:  true,
			wantDelegate: pluginapi.SchedulerBuiltinFillFirst,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publishEmergencyDelegateSnapshot(t, &tt.snapshot)
			req := emergencyDelegateRequest()
			if tt.configureReq != nil {
				tt.configureReq(&req)
			}
			decision := schedulerPickPublished(req, now)
			if decision.Handled != tt.wantHandled || decision.DelegateBuiltin != tt.wantDelegate {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func TestSchedulerOldHostKeepsFillFirstAndRecordsUnsupportedEmergencyCapability(t *testing.T) {
	now := time.Now()
	snapshot := &SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, AdmissionObserved: true, ActiveHighestTier: map[string]struct{}{}}
	publishEmergencyDelegateSnapshot(t, snapshot)
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-emergency"}

	decision := schedulerPickPublished(req, now)
	if !decision.Handled || decision.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst {
		t.Fatalf("decision = %#v, want legacy fill-first", decision)
	}
	store := NewPluginState(DefaultConfig())
	logSchedulerDecision(store, req, decision, now)
	logs := store.Snapshot(now).Logs
	if len(logs) != 1 || logs[0].Fields["compatibility_reason"] != "emergency_delegate_unsupported" {
		t.Fatalf("logs = %#v, want emergency_delegate_unsupported", logs)
	}
}

func TestEmergencyDecisionDiagnosticsAndManagementStatusAreSafe(t *testing.T) {
	now := time.Now()
	snapshot := &SchedulerSnapshot{HandleEnabled: true, Fallback: FallbackFillFirst, AdmissionObserved: true, ActiveHighestTier: map[string]struct{}{}}
	publishEmergencyDelegateSnapshot(t, snapshot)
	req := emergencyDelegateRequest()
	req.Options.Headers = map[string][]string{
		"Authorization":   {"Bearer secret-authorization"},
		"Cookie":          {"session=secret-cookie"},
		"X-Custom-Secret": {"secret-custom-header"},
	}
	req.Options.Metadata = map[string]any{
		"api_key":       "secret-api-key",
		"access_token":  "secret-access-token",
		"refresh_token": "secret-refresh-token",
		"id_token":      "secret-id-token",
		"base_url":      "https://secret-base-url.invalid",
		"auth_path":     "/secret-auth-path/credential.json",
		"raw_auth":      "secret-auth-raw",
	}
	req.Candidates = []pluginapi.SchedulerAuthCandidate{{
		ID:       "non-codex-candidate",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "secret-candidate-api-key",
			"base_url": "https://secret-candidate-base-url.invalid",
		},
		Metadata: map[string]any{"access_token": "secret-candidate-access-token"},
	}}
	decision := schedulerPickPublished(req, now)
	store := NewPluginState(DefaultConfig())
	logSchedulerDecision(store, req, decision, now)

	state := store.Snapshot(now)
	if len(state.Logs) != 1 {
		t.Fatalf("logs = %#v", state.Logs)
	}
	fields := state.Logs[0].Fields
	if fields["host_supports_emergency_delegate"] != true || fields["delegate_builtin"] != pluginapi.SchedulerBuiltinEmergencyProviderFillFirst {
		t.Fatalf("capability/delegate diagnostics = %#v", fields)
	}
	if fields["oauth_stage_reason"] != "no_codex_candidates" {
		t.Fatalf("OAuth stage diagnostics = %#v", fields)
	}
	if _, exists := fields["active_pool_bypassed"]; exists {
		t.Fatalf("legacy active_pool diagnostic = %#v", fields)
	}
	status := BuildStatusPayload(state, nil)
	if !status.HostSupportsEmergencyDelegate || status.LastEmergencyResult == nil {
		t.Fatalf("status emergency diagnostics = %#v", status.LastEmergencyResult)
	}
	if status.LastEmergencyResult.Delegate != pluginapi.SchedulerBuiltinEmergencyProviderFillFirst || status.LastEmergencyResult.OAuthStageReason != "no_codex_candidates" || status.LastEmergencyResult.ActivePoolBypassed {
		t.Fatalf("last emergency result = %#v", status.LastEmergencyResult)
	}
	raw, errMarshal := json.Marshal(status)
	if errMarshal != nil {
		t.Fatalf("marshal status: %v", errMarshal)
	}
	for _, forbidden := range []string{
		"secret-authorization",
		"secret-cookie",
		"secret-custom-header",
		"secret-api-key",
		"secret-access-token",
		"secret-refresh-token",
		"secret-id-token",
		"secret-base-url",
		"secret-auth-path",
		"secret-auth-raw",
		"secret-candidate-api-key",
		"secret-candidate-base-url",
		"secret-candidate-access-token",
		"Authorization",
		"X-Custom-Secret",
		"emergency_provider_fallback",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("status contains forbidden value %q: %s", forbidden, raw)
		}
	}

	logSchedulerDecision(store, req, PickDecision{Handled: true, AuthID: "oauth-selected", Reason: "selected"}, now.Add(time.Second))
	latest := store.Snapshot(now.Add(time.Second)).Logs
	if _, exists := latest[len(latest)-1].Fields["active_pool_bypassed"]; exists {
		t.Fatalf("ordinary OAuth log contains active_pool_bypassed: %#v", latest[len(latest)-1])
	}
}

func TestSchedulerEmergencyHotPathUsesPublishedSnapshotOnly(t *testing.T) {
	now := time.Now()
	activityCalls := 0
	observationCalls := 0
	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActiveHighestTier: map[string]struct{}{},
		Activity: func(pluginapi.SchedulerPickRequest, uint64, time.Time) {
			activityCalls++
		},
		Observation: func(pluginapi.SchedulerPickRequest, PickDecision, time.Time) {
			observationCalls++
		},
	}
	publishEmergencyDelegateSnapshot(t, snapshot)

	for range 100 {
		decision := schedulerPickPublished(emergencyDelegateRequest(), now)
		if decision.DelegateBuiltin != pluginapi.SchedulerBuiltinEmergencyProviderFillFirst {
			t.Fatalf("decision = %#v, want emergency delegate", decision)
		}
	}
	if activityCalls != 100 || observationCalls != 100 {
		t.Fatalf("hot-path callbacks = activity:%d observation:%d, want one in-memory publication callback per pick", activityCalls, observationCalls)
	}
}

func TestOrdinaryOAuthSelectionPublishesHostEmergencyCapabilityWithoutEmergencyResult(t *testing.T) {
	now := time.Now()
	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActiveHighestTier: map[string]struct{}{"oauth-available": {}},
		Accounts:          []AccountView{{ID: "oauth-available", Instance: 1, Cache: CacheFresh}},
	}
	publishEmergencyDelegateSnapshot(t, snapshot)
	req := emergencyDelegateRequest(pluginapi.SchedulerAuthCandidate{ID: "oauth-available", Provider: "codex", Priority: 10})
	decision := schedulerPickPublished(req, now)
	if decision.AuthID != "oauth-available" || !decision.HostSupportsEmergencyDelegate {
		t.Fatalf("decision = %#v, want OAuth selection with advertised host capability", decision)
	}

	store := NewPluginState(DefaultConfig())
	logSchedulerDecision(store, req, decision, now)
	state := store.Snapshot(now)
	status := BuildStatusPayload(state, nil)
	if !status.HostSupportsEmergencyDelegate || status.LastEmergencyResult != nil {
		t.Fatalf("status = %#v, want host capability without emergency result", status)
	}
	fields := state.Logs[len(state.Logs)-1].Fields
	if fields["host_supports_emergency_delegate"] != true {
		t.Fatalf("ordinary OAuth diagnostics = %#v, want host capability", fields)
	}
	if _, exists := fields["active_pool_bypassed"]; exists {
		t.Fatalf("ordinary OAuth diagnostics contain active_pool_bypassed: %#v", fields)
	}
}
