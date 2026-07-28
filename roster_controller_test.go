package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type rosterTestHost struct {
	mu      sync.Mutex
	calls   int
	entries []RosterEntry
	err     error
	gate    chan struct{}
}

func (h *rosterTestHost) ListHostAuths(ctx context.Context) ([]RosterEntry, error) {
	h.mu.Lock()
	h.calls++
	gate, entries, err := h.gate, append([]RosterEntry(nil), h.entries...), h.err
	h.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return entries, err
}

func (h *rosterTestHost) callCount() int { h.mu.Lock(); defer h.mu.Unlock(); return h.calls }

func (h *rosterTestHost) update(entries []RosterEntry, err error, gate chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries, h.err, h.gate = append([]RosterEntry(nil), entries...), err, gate
}
func (h *rosterTestHost) setGate(gate chan struct{}) { h.mu.Lock(); defer h.mu.Unlock(); h.gate = gate }
func (h *rosterTestHost) setError(err error)         { h.mu.Lock(); defer h.mu.Unlock(); h.err = err }

func TestManagementDispatchUsesImmutableRosterSnapshotWithoutGlobalHostLock(t *testing.T) {
	now := time.Now()
	priority := 9
	gate := make(chan struct{})
	host := &rosterTestHost{entries: []RosterEntry{{ID: "active", Provider: "codex", Priority: &priority}}, gate: gate}
	controller := NewRosterController(RosterControllerOptions{Host: host})
	store := NewPluginState(DefaultConfig())
	store.UpsertQuota(weeklyAccount("active", priority, now.Add(24*time.Hour), false))
	store.UpsertQuota(weeklyAccount("removed-cache", priority, now.Add(24*time.Hour), false))

	refresherMu.Lock()
	previousState, previousController := globalState, globalRosterController
	globalState, globalRosterController = store, controller
	refresherMu.Unlock()
	t.Cleanup(func() {
		refresherMu.Lock()
		globalState, globalRosterController = previousState, previousController
		refresherMu.Unlock()
	})

	req, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: managementBasePath + "/status", Query: url.Values{"format": []string{"json"}}})
	done := make(chan []byte, 1)
	go func() { raw, _ := handleManagementHandle(req); done <- raw }()
	deadline := time.Now().Add(time.Second)
	for host.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if host.callCount() == 0 {
		t.Fatal("Management did not wake the roster controller")
	}
	lockAvailable := make(chan struct{})
	go func() { refresherMu.Lock(); refresherMu.Unlock(); close(lockAvailable) }()
	select {
	case <-lockAvailable:
	case <-time.After(time.Second):
		t.Fatal("Management held the global refresher lock through host.auth.list")
	}
	close(gate)

	var outer envelope
	if err := json.Unmarshal(<-done, &outer); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ManagementResponse
	if err := json.Unmarshal(outer.Result, &response); err != nil {
		t.Fatal(err)
	}
	var payload StatusPayload
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Accounts) != 1 || payload.Accounts[0].AuthID != "active" || payload.Roster.Capability != CapabilityA {
		t.Fatalf("dispatch payload did not use authoritative snapshot: %#v", payload)
	}

	before := host.callCount()
	resourceReq, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource" + managementBasePath + "/status"})
	if _, err := handleManagementHandle(resourceReq); err != nil {
		t.Fatal(err)
	}
	if host.callCount() != before {
		t.Fatal("Resource status triggered an authoritative roster host call")
	}
}

func TestManagementCredentialAmbiguityReadsDurableChains(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "runtime.json"), OSFileHooks(), nil)
	state := NewPersistentState()
	first := NewCredentialFingerprint("subject", "refresh-1", "meta")
	second := NewCredentialFingerprint("subject", "refresh-2", "meta")
	other := NewCredentialFingerprint("subject", "refresh-3", "meta")
	state.Bindings["active"] = RuntimeBinding{AuthID: "active", Instance: 1, Fingerprint: first}
	state.Bindings["removed"] = RuntimeBinding{AuthID: "removed", Instance: 2, Fingerprint: other}
	state.CredentialChains[1] = TransitionChain{Cursor: first}
	state.CredentialChains[2] = TransitionChain{Cursor: first, Transitions: []CredentialTransition{{Prev: first, Next: second, SaveSeq: 1, Phase: TransitionOutcomeUnknown, CreatedAt: time.Now()}}}
	if err := store.WriteThrough(state); err != nil {
		t.Fatal(err)
	}
	refresher := &QuotaRefresher{runtimeStore: store}
	if managementCredentialAmbiguous(refresher, ActiveRoster{Instances: []string{"active"}}, time.Now()) {
		t.Fatal("removed credential ambiguity leaked into the active roster")
	}
	if !managementCredentialAmbiguous(refresher, ActiveRoster{Instances: []string{"removed"}}, time.Now()) {
		t.Fatal("active ambiguous reconciliation was not exposed")
	}
	for _, staleBinding := range []struct {
		name        string
		fingerprint CredentialFingerprint
	}{
		{name: "prev", fingerprint: first},
		{name: "next", fingerprint: second},
		{name: "third", fingerprint: other},
	} {
		t.Run("unresolved-stale-binding-"+staleBinding.name, func(t *testing.T) {
			state.Bindings["active"] = RuntimeBinding{AuthID: "active", Instance: 1, Fingerprint: staleBinding.fingerprint}
			state.CredentialChains[1] = TransitionChain{Cursor: first, Transitions: []CredentialTransition{{Prev: first, Next: second, SaveSeq: 1, Phase: TransitionOutcomeUnknown, CreatedAt: time.Now()}}}
			if err := store.WriteThrough(state); err != nil {
				t.Fatal(err)
			}
			if !managementCredentialAmbiguous(refresher, ActiveRoster{Instances: []string{"active"}}, time.Now()) {
				t.Fatal("unresolved active transition was cleared from a stale durable binding without reconciliation")
			}
		})
	}
	state.Bindings["active"] = RuntimeBinding{AuthID: "active", Instance: 1, Fingerprint: second}
	state.CredentialChains[1] = TransitionChain{Cursor: first, Transitions: []CredentialTransition{{Prev: first, Next: second, SaveSeq: 1, Phase: TransitionApplied, CreatedAt: time.Now().Add(-25 * time.Hour)}}}
	if err := store.WriteThrough(state); err != nil {
		t.Fatal(err)
	}
	if !managementCredentialAmbiguous(refresher, ActiveRoster{Instances: []string{"active"}}, time.Now()) {
		t.Fatal("expired reachable credential generation was not exposed as ambiguous")
	}
}

func TestManagementCredentialAmbiguitySeparatesEmptyRosterAndStoreFailure(t *testing.T) {
	hooks := OSFileHooks()
	hooks.ReadFile = func(string) ([]byte, error) { return nil, errors.New("runtime store unavailable") }
	refresher := &QuotaRefresher{runtimeStore: NewStateStore(filepath.Join(t.TempDir(), "runtime.json"), hooks, nil)}
	if managementCredentialAmbiguous(refresher, ActiveRoster{}, time.Now()) {
		t.Fatal("empty active roster reported credential ambiguity")
	}
	if managementCredentialAmbiguous(refresher, ActiveRoster{Instances: []string{"active"}}, time.Now()) {
		t.Fatal("general runtime-store failure masqueraded as credential ambiguity")
	}
}

func TestManagementDispatchWithoutControllerIsWaitingRoster(t *testing.T) {
	now := time.Now()
	store := NewPluginState(DefaultConfig())
	store.UpsertQuota(weeklyAccount("cached", 9, now.Add(24*time.Hour), false))
	refresherMu.Lock()
	previousState, previousController, previousRefresher := globalState, globalRosterController, globalRefresher
	globalState, globalRosterController, globalRefresher = store, nil, nil
	refresherMu.Unlock()
	t.Cleanup(func() {
		refresherMu.Lock()
		globalState, globalRosterController, globalRefresher = previousState, previousController, previousRefresher
		refresherMu.Unlock()
	})
	req, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: managementBasePath + "/status", Query: url.Values{"format": []string{"json"}}})
	raw, err := handleManagementHandle(req)
	if err != nil {
		t.Fatal(err)
	}
	var outer envelope
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ManagementResponse
	if err := json.Unmarshal(outer.Result, &response); err != nil {
		t.Fatal(err)
	}
	var payload StatusPayload
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Roster.Capability != CapabilityB || payload.Roster.Health != RosterWaiting || !payload.Roster.WaitingRoster || len(payload.Accounts) != 0 {
		t.Fatalf("nil-controller Management payload = %#v", payload)
	}
}

func TestSuiteRosterManagement(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	p9, p1 := 9, 1
	host := &rosterTestHost{entries: []RosterEntry{
		{ID: "low", Provider: "codex", Priority: &p1},
		{ID: "high-a", Provider: "codex", Priority: &p9},
		{ID: "high-b", Provider: "codex", Priority: &p9},
	}}
	clock := now
	var published []ActiveRoster
	c := NewRosterController(RosterControllerOptions{
		Host: host, Now: func() time.Time { return clock },
		Publish: func(_ context.Context, roster ActiveRoster) (ActiveRoster, error) {
			published = append(published, roster)
			return roster, nil
		},
	})

	t.Run("startup, ttl, idle, probe and management wakes", func(t *testing.T) {
		got, err := c.Startup(context.Background())
		if err != nil || !got.Confirmed || got.HighestPriority != 9 || len(got.Instances) != 2 {
			t.Fatalf("startup = %#v, %v", got, err)
		}
		clock = now.Add(rosterActiveTTL - time.Nanosecond)
		if _, err := c.WakeForActivity(context.Background()); err != nil {
			t.Fatal(err)
		}
		if host.callCount() != 1 {
			t.Fatalf("fresh activity calls=%d", host.callCount())
		}
		clock = now.Add(rosterActiveTTL)
		if _, err := c.WakeForProbe(context.Background()); err != nil {
			t.Fatal(err)
		}
		if host.callCount() != 2 {
			t.Fatalf("probe prewake calls=%d", host.callCount())
		}
		clock = clock.Add(time.Nanosecond)
		if _, err := c.WakeForManagement(context.Background()); err != nil {
			t.Fatal(err)
		}
		if host.callCount() != 3 {
			t.Fatalf("management calls=%d", host.callCount())
		}
		if len(published) != 3 {
			t.Fatalf("published=%d", len(published))
		}
	})

	t.Run("singleflight", func(t *testing.T) {
		gate := make(chan struct{})
		host.setGate(gate)
		clock = clock.Add(rosterActiveTTL)
		before := host.callCount()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = c.WakeForManagement(context.Background()) }()
		}
		for host.callCount() == before {
			time.Sleep(time.Millisecond)
		}
		close(gate)
		host.setGate(nil)
		wg.Wait()
		if host.callCount() != before+1 {
			t.Fatalf("singleflight calls=%d want %d", host.callCount(), before+1)
		}
	})

	t.Run("degraded boundary, fail closed and recovery", func(t *testing.T) {
		host.setError(errors.New("offline"))
		generation := c.Snapshot().Generation
		firstFailure := clock.Add(time.Minute)
		clock = firstFailure
		got, err := c.WakeForManagement(context.Background())
		if err == nil || got.Health != RosterDegraded || !got.BackgroundAllowed {
			t.Fatalf("first failure=%#v err=%v", got, err)
		}
		clock = firstFailure.Add(rosterDegradedLimit)
		got, _ = c.WakeForManagement(context.Background())
		if got.Health != RosterFailClosed || got.BackgroundAllowed {
			t.Fatalf("at boundary=%#v", got)
		}
		clock = firstFailure.Add(rosterDegradedLimit + time.Nanosecond)
		got, _ = c.WakeForProbe(context.Background())
		if got.Health != RosterFailClosed || len(got.Instances) != 2 {
			t.Fatalf("after boundary=%#v", got)
		}
		host.setError(nil)
		got, err = c.WakeForManagement(context.Background())
		if err != nil || got.Health != RosterHealthy || !got.Confirmed || got.Generation != generation {
			t.Fatalf("recovery=%#v err=%v", got, err)
		}
	})
}

func TestConcurrentSameMomentRosterWakesSingleflight(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	priority := 9
	gate := make(chan struct{})
	host := &rosterTestHost{entries: []RosterEntry{{ID: "active", Provider: "codex", Priority: &priority}}, gate: gate}
	controller := NewRosterController(RosterControllerOptions{Host: host, Now: func() time.Time { return now }})
	const wakes = 8
	var wg sync.WaitGroup
	for i := 0; i < wakes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = controller.WakeForManagement(context.Background())
		}()
	}
	deadline := time.Now().Add(time.Second)
	for host.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := host.callCount(); calls != 1 {
		t.Fatalf("same-moment wakes started %d host syncs before release", calls)
	}
	close(gate)
	wg.Wait()
	if calls := host.callCount(); calls != 1 {
		t.Fatalf("same-moment wakes issued duplicate host syncs: %d", calls)
	}
}

func TestStartupCapabilityBRecoversThroughRosterSynchronization(t *testing.T) {
	p := 4
	host := &rosterTestHost{err: errors.New("not ready")}
	now := time.Now()
	c := NewRosterController(RosterControllerOptions{Host: host, Now: func() time.Time { return now }})
	got, err := c.Startup(context.Background())
	if err == nil || got.Capability != CapabilityB || got.Health != RosterWaiting {
		t.Fatalf("startup=%#v err=%v", got, err)
	}
	host.update([]RosterEntry{{ID: "a", Provider: "codex", Priority: &p}}, nil, nil)
	got, err = c.WakeForManagement(context.Background())
	if err != nil || got.Capability != CapabilityA || !got.Confirmed {
		t.Fatalf("recovery=%#v err=%v", got, err)
	}
}

func TestRosterSyncDiagnosticsClassifyFailureAndClearOnRecovery(t *testing.T) {
	priority := 4
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	host := &rosterTestHost{err: errors.New("Authorization: SECRET")}
	controller := NewRosterController(RosterControllerOptions{Host: host, Now: func() time.Time { return now }})

	failed, err := controller.Startup(context.Background())
	if err == nil || failed.LastSyncResult != RosterSyncFailed || failed.LastSyncError != RosterSyncErrorHostCall || !failed.LastSyncAt.Equal(now) {
		t.Fatalf("failed sync diagnostics = %#v err=%v", failed, err)
	}

	now = now.Add(time.Minute)
	host.update([]RosterEntry{{ID: "other", Provider: "openai", Priority: &priority}}, nil, nil)
	noTier, err := controller.WakeForManagement(context.Background())
	if err == nil || noTier.LastSyncResult != RosterSyncFailed || noTier.LastSyncError != RosterSyncErrorNoCodexTier || !noTier.LastSyncAt.Equal(now) {
		t.Fatalf("no-tier diagnostics = %#v err=%v", noTier, err)
	}

	now = now.Add(time.Minute)
	host.update([]RosterEntry{{ID: "codex-a", Provider: "codex", Priority: &priority}}, nil, nil)
	recovered, err := controller.WakeForManagement(context.Background())
	if err != nil || recovered.LastSyncResult != RosterSyncSucceeded || recovered.LastSyncError != "" || !recovered.LastSyncAt.Equal(now) {
		t.Fatalf("recovered diagnostics = %#v err=%v", recovered, err)
	}
}

func TestRosterSyncDiagnosticsClassifyMissingHost(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	controller := NewRosterController(RosterControllerOptions{Now: func() time.Time { return now }})
	got, err := controller.Startup(context.Background())
	if err == nil || got.LastSyncError != RosterSyncErrorHostUnavailable || got.LastSyncResult != RosterSyncFailed {
		t.Fatalf("missing-host diagnostics = %#v err=%v", got, err)
	}
}

func TestRosterSyncDiagnosticsClassifyPublishFailureAndExposeSafeStatus(t *testing.T) {
	priority := 4
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	host := &rosterTestHost{entries: []RosterEntry{{ID: "codex-a", Provider: "codex", Priority: &priority}}}
	failPublish := true
	controller := NewRosterController(RosterControllerOptions{
		Host: host,
		Now:  func() time.Time { return now },
		Publish: func(context.Context, ActiveRoster) (ActiveRoster, error) {
			if failPublish {
				return ActiveRoster{}, errors.New("Authorization: SECRET")
			}
			return ActiveRoster{}, nil
		},
	})

	failed, err := controller.Startup(context.Background())
	if err == nil || failed.LastSyncResult != RosterSyncFailed || failed.LastSyncError != RosterSyncErrorPublish {
		t.Fatalf("publish failure diagnostics = %#v err=%v", failed, err)
	}
	payload := rosterLifecyclePayload(ManagementLifecycleSnapshot{Roster: failed}, NewPluginState(DefaultConfig()).Snapshot(now), now)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"last_sync_result":"failed"`, `"last_sync_error_category":"publish_failed"`, `"last_sync_at":"2026-07-28T12:00:00Z"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("status missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, "SECRET") || strings.Contains(text, "Authorization") {
		t.Fatalf("status leaked publish error: %s", text)
	}

	failPublish = false
	now = now.Add(time.Minute)
	recovered, err := controller.WakeForManagement(context.Background())
	if err != nil || recovered.LastSyncResult != RosterSyncSucceeded || recovered.LastSyncError != "" {
		t.Fatalf("publish recovery diagnostics = %#v err=%v", recovered, err)
	}
}

func TestRosterCandidatesHaveNoRosterSideEffects(t *testing.T) {
	host := &rosterTestHost{err: errors.New("unavailable")}
	c := NewRosterController(RosterControllerOptions{Host: host, Candidates: func() []string { return []string{"candidate-only"} }})
	got, _ := c.Startup(context.Background())
	if len(got.Instances) != 0 || got.Confirmed {
		t.Fatalf("candidate became roster: %#v", got)
	}
}

func TestWaitingRosterRecoveryUsesPreviouslyLoadedStrategy(t *testing.T) {
	priority := 4
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.SelectionStrategy = SelectionStrategyQuotaLow
	lowUsed, highUsed := 10.0, 80.0
	store := NewPluginState(cfg)
	store.UpsertQuota(AccountState{AuthID: "more", Instance: 1, Family: AccountFamilyWeekly, PlanType: "plus", LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &lowUsed, ResetAt: now.Add(time.Hour)}}})
	store.UpsertQuota(AccountState{AuthID: "less", Instance: 2, Family: AccountFamilyWeekly, PlanType: "plus", LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &highUsed, ResetAt: now.Add(time.Hour)}}})
	previousSnapshot := publishedSchedulerSnapshot.Load()
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previousSnapshot) })
	publishSchedulerState(store, nil, now)

	host := &rosterTestHost{err: errors.New("not ready")}
	controller := NewRosterController(RosterControllerOptions{
		Host: host,
		Now:  func() time.Time { return now },
		Publish: func(_ context.Context, active ActiveRoster) (ActiveRoster, error) {
			ids := make(map[string]struct{}, len(active.Instances))
			for _, authID := range active.Instances {
				ids[authID] = struct{}{}
			}
			store.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, Priority: active.HighestPriority, AuthIDs: ids})
			publishSchedulerState(store, ids, now)
			return active, nil
		},
	})
	if _, err := controller.Startup(context.Background()); err == nil {
		t.Fatal("startup unexpectedly confirmed roster")
	}
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "more", Provider: "codex"}, {ID: "less", Provider: "codex"}}}
	before := schedulerPickPublished(req, now)
	if before.Reason != "waiting_roster" || before.Strategy != SelectionStrategyQuotaLow || before.AuthID != "" {
		t.Fatalf("waiting decision = %#v", before)
	}

	now = now.Add(time.Minute)
	host.update([]RosterEntry{{ID: "more", Provider: "codex", Priority: &priority}, {ID: "less", Provider: "codex", Priority: &priority}}, nil, nil)
	if _, err := controller.WakeForManagement(context.Background()); err != nil {
		t.Fatalf("roster recovery: %v", err)
	}
	after := schedulerPickPublished(req, now)
	if after.AuthID != "less" || after.Strategy != SelectionStrategyQuotaLow {
		t.Fatalf("recovered decision = %#v, want quota_low select less", after)
	}
}

func TestCapabilityBProvisionalProbeRequiresRiskOptionAgeAndFingerprint(t *testing.T) {
	now := time.Now()
	base := ActiveRoster{Instances: []string{"a"}, ConfirmedAt: now.Add(-time.Hour)}
	verified := false
	c := NewRosterController(RosterControllerOptions{Host: &rosterTestHost{err: errors.New("B")}, Now: func() time.Time { return now }, Provisional: &base, ProbeOnProvisional: true, VerifyProvisional: func(context.Context, ActiveRoster) bool { return verified }})
	got, _ := c.WakeForProbe(context.Background())
	if !got.Provisional || got.BackgroundAllowed {
		t.Fatalf("unverified=%#v", got)
	}
	verified = true
	got, _ = c.WakeForProbe(context.Background())
	if !got.Provisional || !got.BackgroundAllowed {
		t.Fatalf("verified=%#v", got)
	}
	expired := base
	expired.ConfirmedAt = now.Add(-provisionalMaxAge)
	c = NewRosterController(RosterControllerOptions{Provisional: &expired, Now: func() time.Time { return now }, ProbeOnProvisional: true, VerifyProvisional: func(context.Context, ActiveRoster) bool { return true }})
	if got := c.Snapshot(); got.Provisional || len(got.Instances) != 0 {
		t.Fatalf("expired=%#v", got)
	}
	future := base
	future.ConfirmedAt = now.Add(time.Nanosecond)
	c = NewRosterController(RosterControllerOptions{Provisional: &future, Now: func() time.Time { return now }, ProbeOnProvisional: true, VerifyProvisional: func(context.Context, ActiveRoster) bool { return true }})
	if got := c.Snapshot(); got.Provisional || len(got.Instances) != 0 {
		t.Fatalf("future=%#v", got)
	}
}

func TestProvisionalVerificationFailureRevokesPreviouslyPublishedProbeAccess(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	verified := true
	base := ActiveRoster{Capability: CapabilityB, Provisional: true, Generation: 4, ConfirmedAt: now.Add(-time.Hour), Health: RosterWaiting}
	var observed []ActiveRoster
	c := NewRosterController(RosterControllerOptions{
		Now:                func() time.Time { return now },
		Provisional:        &base,
		ProbeOnProvisional: true,
		VerifyProvisional:  func(context.Context, ActiveRoster) bool { return verified },
		Observe:            func(active ActiveRoster) { observed = append(observed, active) },
	})
	if got, _ := c.WakeForProbe(context.Background()); !got.BackgroundAllowed {
		t.Fatalf("first verified wake=%#v", got)
	}
	verified = false
	got, _ := c.WakeForProbe(context.Background())
	if got.BackgroundAllowed {
		t.Fatalf("failed verification retained access: %#v", got)
	}
	if len(observed) < 2 || observed[len(observed)-1].BackgroundAllowed {
		t.Fatalf("revocation not published: %#v", observed)
	}
}

func TestProvisionalRiskDisableFencesInFlightVerificationCommit(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.ProbeOnProvisionalRoster = true
	state := NewPluginState(cfg)
	entered := make(chan struct{})
	release := make(chan struct{})
	base := ActiveRoster{Capability: CapabilityB, Provisional: true, Generation: 4, ConfirmedAt: now.Add(-time.Hour), Health: RosterWaiting}
	c := NewRosterController(RosterControllerOptions{
		Now:                func() time.Time { return now },
		Provisional:        &base,
		ProbeOnProvisional: true,
		VerifyProvisional: func(context.Context, ActiveRoster) bool {
			close(entered)
			<-release
			return true
		},
		CommitProvisional: func(commit func()) bool {
			state.mu.RLock()
			defer state.mu.RUnlock()
			if !state.cfg.ProbeOnProvisionalRoster {
				return false
			}
			commit()
			return true
		},
	})
	done := make(chan ActiveRoster, 1)
	go func() { got, _ := c.WakeForProbe(context.Background()); done <- got }()
	<-entered
	cfg.ProbeOnProvisionalRoster = false
	state.ReplaceConfig(cfg)
	close(release)
	if got := <-done; got.BackgroundAllowed || c.Snapshot().BackgroundAllowed {
		t.Fatalf("disabled risk committed stale verification: got=%#v snapshot=%#v", got, c.Snapshot())
	}
}

func TestProvisionalVerificationCannotCommitAfterAgeExpires(t *testing.T) {
	confirmedAt := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	now := confirmedAt.Add(provisionalMaxAge - time.Nanosecond)
	base := ActiveRoster{Capability: CapabilityB, Provisional: true, Generation: 4, ConfirmedAt: confirmedAt, Health: RosterWaiting}
	c := NewRosterController(RosterControllerOptions{
		Now:                func() time.Time { return now },
		Provisional:        &base,
		ProbeOnProvisional: true,
		VerifyProvisional: func(context.Context, ActiveRoster) bool {
			now = confirmedAt.Add(provisionalMaxAge)
			return true
		},
	})
	got, _ := c.WakeForProbe(context.Background())
	if got.BackgroundAllowed || c.Snapshot().BackgroundAllowed {
		t.Fatalf("expired verification committed access: got=%#v snapshot=%#v", got, c.Snapshot())
	}
}

func TestProductionProbePrewakesRosterController(t *testing.T) {
	p := 1
	host := &rosterTestHost{entries: []RosterEntry{{ID: "a", Provider: "codex", Priority: &p}}}
	controller := NewRosterController(RosterControllerOptions{Host: host})
	refresherMu.Lock()
	previous := globalRosterController
	globalRosterController = controller
	refresherMu.Unlock()
	t.Cleanup(func() { refresherMu.Lock(); globalRosterController = previous; refresherMu.Unlock() })
	_ = (&QuotaRefresher{}).RunProbeDueOnce(context.Background())
	if host.callCount() != 1 {
		t.Fatalf("probe roster calls=%d", host.callCount())
	}
}

func TestRosterDegradedStartsAtFirstFailure(t *testing.T) {
	p := 7
	now := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	clock := now
	host := &rosterTestHost{entries: []RosterEntry{{ID: "a", Provider: "codex", Priority: &p}}}
	c := NewRosterController(RosterControllerOptions{Host: host, Now: func() time.Time { return clock }})
	if _, err := c.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.setError(errors.New("offline"))
	firstFailure := now.Add(20 * time.Minute)
	clock = firstFailure
	got, err := c.WakeForManagement(context.Background())
	if err == nil || got.Health != RosterDegraded || !got.DegradedSince.Equal(firstFailure) {
		t.Fatalf("first failure=%#v err=%v", got, err)
	}
	clock = firstFailure.Add(rosterDegradedLimit - time.Nanosecond)
	got, _ = c.WakeForManagement(context.Background())
	if got.Health != RosterDegraded || !got.DegradedSince.Equal(firstFailure) {
		t.Fatalf("before boundary=%#v", got)
	}
	clock = firstFailure.Add(rosterDegradedLimit)
	got, _ = c.WakeForManagement(context.Background())
	if got.Health != RosterFailClosed || !got.DegradedSince.Equal(firstFailure) {
		t.Fatalf("at boundary=%#v", got)
	}
}

func TestRosterGenerationChangesOnlyWithRoster(t *testing.T) {
	p := 7
	now := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	clock := now
	host := &rosterTestHost{entries: []RosterEntry{{ID: "a", AuthIndex: "idx-a", Provider: "codex", Priority: &p}}}
	c := NewRosterController(RosterControllerOptions{Host: host, Now: func() time.Time { return clock }})
	first, err := c.Startup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	identical, err := c.WakeForManagement(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identical.Generation != first.Generation {
		t.Fatalf("identical generation=%d want %d", identical.Generation, first.Generation)
	}
	host.update([]RosterEntry{{ID: "b", AuthIndex: "idx-b", Provider: "codex", Priority: &p}}, nil, nil)
	clock = clock.Add(time.Minute)
	changed, err := c.WakeForManagement(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed.Generation != first.Generation+1 {
		t.Fatalf("changed generation=%d want %d", changed.Generation, first.Generation+1)
	}
}

func TestRosterObserverSeesEveryTransition(t *testing.T) {
	p := 7
	now := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	clock := now
	host := &rosterTestHost{entries: []RosterEntry{{ID: "a", Provider: "codex", Priority: &p}}}
	var observed []ActiveRoster
	c := NewRosterController(RosterControllerOptions{
		Host: host,
		Now:  func() time.Time { return clock },
		Observe: func(active ActiveRoster) {
			observed = append(observed, cloneActiveRoster(active))
		},
	})
	if _, err := c.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.setError(errors.New("offline"))
	clock = clock.Add(rosterActiveTTL)
	_, _ = c.WakeForManagement(context.Background())
	host.setError(nil)
	clock = clock.Add(time.Minute)
	if _, err := c.WakeForManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 3 {
		t.Fatalf("observed=%d want 3: %#v", len(observed), observed)
	}
	if observed[0].Health != RosterHealthy || observed[1].Health != RosterDegraded || observed[2].Health != RosterHealthy {
		t.Fatalf("observer transitions=%#v", observed)
	}
}

func TestRosterObserverRejectsOutOfOrderTransition(t *testing.T) {
	p := 7
	clock := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	host := &rosterTestHost{entries: []RosterEntry{{ID: "a", Provider: "codex", Priority: &p}}}
	runtime := NewQuotaRefresher(&fakeHostClient{}, NewPluginState(DefaultConfig()), func() time.Time { return clock })
	degradedEntered := make(chan struct{})
	releaseDegraded := make(chan struct{})
	var once sync.Once
	c := NewRosterController(RosterControllerOptions{
		Host: host,
		Now:  func() time.Time { return clock },
		Observe: func(active ActiveRoster) {
			if active.Health == RosterDegraded {
				once.Do(func() { close(degradedEntered) })
				<-releaseDegraded
			}
			runtime.ObserveRosterLifecycle(active)
		},
	})
	if _, err := c.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.setError(errors.New("offline"))
	clock = clock.Add(time.Minute)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = c.WakeForManagement(context.Background())
	}()
	<-degradedEntered
	clock = clock.Add(rosterDegradedLimit)
	if got, _ := c.WakeForManagement(context.Background()); got.Health != RosterFailClosed {
		t.Fatalf("second transition=%#v", got)
	}
	close(releaseDegraded)
	<-firstDone
	if got := runtime.runtimeRoster(); got.Health != RosterFailClosed {
		t.Fatalf("stale observer overwrote fail-closed lifecycle: %#v", got)
	}
}

func TestRosterProvisionalObserverOnlyAfterGuardCommit(t *testing.T) {
	p := 7
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	base := ActiveRoster{Instances: []string{"a"}, Entries: []RosterEntry{{ID: "a", Provider: "codex", Priority: &p}}, ConfirmedAt: now.Add(-time.Hour), Generation: 3}
	host := &rosterTestHost{err: errors.New("offline")}
	var c *RosterController
	var observed []ActiveRoster
	c = NewRosterController(RosterControllerOptions{
		Host:               host,
		Now:                func() time.Time { return now },
		Provisional:        &base,
		ProbeOnProvisional: true,
		Observe: func(active ActiveRoster) {
			observed = append(observed, cloneActiveRoster(active))
		},
		VerifyProvisional: func(ctx context.Context, _ ActiveRoster) bool {
			_, _ = c.OnSyncResult(ctx, []RosterEntry{{ID: "a", Provider: "codex", Priority: &p}}, nil)
			return true
		},
	})
	_, _ = c.WakeForProbe(context.Background())
	if got := c.Snapshot(); got.Health != RosterHealthy || got.Provisional {
		t.Fatalf("current=%#v", got)
	}
	if len(observed) != 2 || observed[len(observed)-1].Health != RosterHealthy {
		t.Fatalf("lost guard published stale provisional transition: %#v", observed)
	}
}
