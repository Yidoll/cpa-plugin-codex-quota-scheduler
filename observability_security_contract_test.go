package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Batch 7 RED/GREEN contract tests: observability and security verification.

func TestObservabilityStatusActivePoolTypeAndCounts(t *testing.T) {
	requireManagementInventorySchema(t)
	state, _, listCalls := installManagementInventoryFixture(t)
	annotations := state.Annotations()
	annotations.Accounts["auth:highest-oauth"] = AccountAnnotation{GroupID: "core"}
	state.SetAnnotations(annotations)
	cfg := state.Config()
	cfg.ActivePool = "group:core"
	cfg.ExcludeFreeAccounts = false
	state.ReplaceConfig(cfg)

	payload := readManagementInventoryStatus(t)
	counts := reflect.ValueOf(payload).FieldByName("Inventory").FieldByName("Counts")
	if !counts.IsValid() || counts.Kind() != reflect.Struct {
		t.Fatalf("management inventory counts = %#v, want struct", payload.Inventory)
	}
	for _, field := range []string{"ActivePoolType", "Total", "HighestTier", "InPool", "InPoolCandidates", "Schedulable"} {
		value := counts.FieldByName(field)
		if !value.IsValid() {
			t.Fatalf("inventory count field %s missing; fields=%s", field, inventoryCountsFieldNames(counts))
		}
	}
	if got := counts.FieldByName("ActivePoolType").String(); got != "group:core" {
		t.Fatalf("inventory active pool type = %q, want group:core", got)
	}
	if *listCalls != 1 {
		t.Fatalf("observability status caused host.auth.list calls = %d, want 1 shared publication", *listCalls)
	}
}

func inventoryCountsFieldNames(value reflect.Value) string {
	names := make([]string, 0, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		names = append(names, value.Type().Field(index).Name)
	}
	return strings.Join(names, ",")
}

func TestObservabilityFixedErrorCodeVocabulary(t *testing.T) {
	// Inventory eligibility reasons are a fixed, non-sensitive vocabulary.
	eligibility := map[string]bool{}
	for _, reason := range []string{
		SchedulingReasonSchedulable,
		SchedulingReasonDisabled,
		SchedulingReasonUnavailable,
		SchedulingReasonAuthFailure,
		SchedulingReasonAPIKeyCredential,
		SchedulingReasonNotHighestTier,
		SchedulingReasonOutsideActivePool,
		SchedulingReasonFreeExcluded,
		SchedulingReasonUnknownPlan,
		SchedulingReasonQuotaMissing,
		SchedulingReasonIdentityConflict,
		SchedulingReasonGroupReferenceUnavailable,
	} {
		if reason == "" {
			t.Fatal("inventory eligibility reason must never be empty")
		}
		eligibility[reason] = true
	}
	for _, entry := range inventoryEligibilityReasons(t) {
		if !eligibility[entry] {
			t.Fatalf("inventory eligibility reason %q is not part of the fixed vocabulary", entry)
		}
	}

	// Conflict and batch codes remain fixed; plugin group management has a
	// single removal code instead of strict pool failure codes.
	for _, code := range []string{
		accountIdentityConflict,
		groupIsActive,
		groupNotEmpty,
		inventoryNotConfirmed,
		pluginGroupManagementRemoved,
	} {
		if code == "" {
			t.Fatal("fixed error code must never be empty")
		}
	}
}

func inventoryEligibilityReasons(t *testing.T) []string {
	t.Helper()
	payload := readManagementInventoryStatus(t)
	entries := managementInventoryEntries(t, payload)
	reasons := make([]string, 0, len(entries))
	for _, entry := range entries {
		reasons = append(reasons, managementInventoryEntryString(t, entry, "SchedulingReason"))
	}
	return reasons
}

func TestLegacyActivePoolDoesNotProduceStrictFailureLog(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	store := NewPluginState(DefaultConfig())
	cfg := store.Config()
	cfg.ActivePool = "group:core"
	store.ReplaceConfig(cfg)
	store.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, Priority: 10})

	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActivePool:        "group:core",
		Accounts: []AccountView{
			{ID: "core-auth-failed", Instance: 1, Cache: CacheFresh, AuthBlocked: true},
			{ID: "core-exhausted", Instance: 2, Cache: CacheFresh, Exhausted: true, ResetAt: now.Add(time.Hour)},
			{ID: "outside-available", Instance: 3, Cache: CacheFresh},
		},
		ActiveHighestTier: map[string]struct{}{
			"core-auth-failed":  {},
			"core-exhausted":    {},
			"outside-available": {},
		},
	}
	PublishSchedulerSnapshot(snapshot)
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(nil) })

	decision := schedulerPickPublished(strictPoolRequest("core-auth-failed", "core-exhausted", "outside-available"), now)
	logSchedulerDecision(store, strictPoolRequest("core-auth-failed", "core-exhausted", "outside-available"), decision, now)

	logs := store.Snapshot(now).Logs
	if len(logs) == 0 {
		t.Fatal("scheduler decision produced no log entry")
	}
	entry := logs[len(logs)-1]
	fields := entry.Fields
	if entry.Event == "scheduler.strict_failure" {
		t.Fatalf("legacy active_pool produced removed strict event: %#v", entry)
	}
	for _, key := range []string{"active_pool", "cpa_tier", "active_pool_bypassed"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("legacy active_pool diagnostic %q leaked into log: %#v", key, fields)
		}
	}
	for key, value := range fields {
		text, ok := value.(string)
		if !ok {
			continue
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "bearer ") || strings.Contains(lower, "cookie") || strings.Contains(lower, "access_token") {
			t.Fatalf("scheduler log leaked credential marker in %q=%v", key, value)
		}
	}
}

func TestPublicResourceShellNeverExposesInventoryEmailsGroupsOrIdentities(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	state, _, _ := installManagementInventoryFixture(t)
	annotations := state.Annotations()
	annotations.Accounts["auth:highest-oauth"] = AccountAnnotation{GroupID: "core", Alias: "High OAuth"}
	state.SetAnnotations(annotations)
	cfg := state.Config()
	cfg.ActivePool = "group:core"
	state.ReplaceConfig(cfg)

	resp := HandleManagementRequest(state, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/codex-quota-scheduler/status",
	}, now)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resource status = %d, want 200", resp.StatusCode)
	}
	body := string(resp.Body)
	for _, forbidden := range []string{"highest-oauth", "low-oauth", "api-key", "h@example.com", "High OAuth", "auth:core"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("public resource shell leaked %q: %s", forbidden, body)
		}
	}
	for _, want := range []string{`"shell":true`, "managementKey", "logList"} {
		if !strings.Contains(body, want) {
			t.Fatalf("public resource shell missing %q", want)
		}
	}
}

func TestSensitiveFieldScanAcrossInventoryAndStatusPayloads(t *testing.T) {
	requireManagementInventorySchema(t)
	forbiddenKeys := []string{"path", "rawjson", "token", "cookie", "authorization", "password", "access_token", "refresh_token", "id_token"}
	forbiddenValues := []string{"Bearer ", "Authorization", "access_token", "refresh_token", "id_token", "cookie", "auth.json", "~/.codex"}

	_, _, _ = installManagementInventoryFixture(t)
	payload := readManagementInventoryStatus(t)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal management status: %v", err)
	}
	lowerRaw := strings.ToLower(string(raw))
	for _, value := range forbiddenValues {
		if strings.Contains(lowerRaw, strings.ToLower(value)) {
			t.Fatalf("management status payload contains sensitive value %q", value)
		}
	}
	entries := reflect.ValueOf(payload).FieldByName("Inventory").FieldByName("Entries")
	for index := 0; index < entries.Len(); index++ {
		entry := entries.Index(index)
		entryType := entry.Type()
		for fieldIndex := 0; fieldIndex < entry.NumField(); fieldIndex++ {
			name := strings.ToLower(entryType.Field(fieldIndex).Name)
			for _, key := range forbiddenKeys {
				if strings.Contains(name, key) {
					t.Fatalf("inventory entry field %q matches forbidden key %q", entryType.Field(fieldIndex).Name, key)
				}
			}
		}
	}
}

func TestInventoryStatusMessageIsSanitizedBeforeExposure(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	raw := `{"files":[
		{"id":"healthy-oauth","auth_index":"idx-healthy","provider":"codex","account_type":"oauth","status":"active","priority":9},
		{"id":"sensitive-oauth","auth_index":"idx-sensitive","provider":"codex","account_type":"oauth","status":"error","status_message":"failed with Authorization Bearer abc123 at /home/u/.codex/auth.json cookie=sk-xyz access_token=tok","priority":9}
	]}`
	lister := ABIHostAuthLister{call: func(method string, _ any) (json.RawMessage, error) {
		if method != "host.auth.list" {
			t.Fatalf("host method = %q, want host.auth.list", method)
		}
		return json.RawMessage(raw), nil
	}}
	state := NewPluginState(DefaultConfig())
	controller := NewRosterController(RosterControllerOptions{
		Host: lister,
		Now:  func() time.Time { return now },
		Publish: func(_ context.Context, active ActiveRoster) (ActiveRoster, error) {
			ids := make(map[string]struct{}, len(active.Instances))
			for _, authID := range active.Instances {
				ids[authID] = struct{}{}
			}
			state.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, Priority: active.HighestPriority, AuthIDs: ids})
			publishSchedulerState(state, ids, now)
			return active, nil
		},
	})
	installManagementInventoryGlobals(t, state, controller)

	payload := readManagementInventoryStatus(t)
	byID := managementInventoryEntriesByAuthID(t, managementInventoryEntries(t, payload))
	entry := byID["sensitive-oauth"]
	if message := managementInventoryEntryString(t, entry, "StatusMessage"); strings.Contains(strings.ToLower(message), "bearer") ||
		strings.Contains(strings.ToLower(message), "token") || strings.Contains(strings.ToLower(message), "cookie") {
		t.Fatalf("inventory status message not sanitized: %q", message)
	}
}

func TestFailurePathsDoNotOpenCircuitPanicOrFallback(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	store := NewPluginState(DefaultConfig())
	cfg := store.Config()
	cfg.ActivePool = "group:core"
	store.ReplaceConfig(cfg)
	store.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, Priority: 10})

	snapshot := &SchedulerSnapshot{
		HandleEnabled:     true,
		Fallback:          FallbackFillFirst,
		AdmissionObserved: true,
		ActivePool:        "group:core",
		Accounts: []AccountView{
			{ID: "core-exhausted", Instance: 1, Cache: CacheFresh, Exhausted: true, ResetAt: now.Add(time.Hour)},
			{ID: "outside-available", Instance: 2, Cache: CacheFresh},
		},
		ActiveHighestTier: map[string]struct{}{"core-exhausted": {}, "outside-available": {}},
	}
	PublishSchedulerSnapshot(snapshot)
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(nil) })

	decision := func() (out PickDecision) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("strict failure panicked: %v", recovered)
			}
		}()
		out = schedulerPickPublished(strictPoolRequest("core-exhausted", "outside-available"), now)
		return out
	}()
	if decision.AuthID != "outside-available" || decision.DelegateBuiltin != "" {
		t.Fatalf("legacy active_pool changed host candidate selection: %#v", decision)
	}

	// Management persistence failure and stale inventory stay failure-only.
	resp := HandleManagementRequest(store, pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   managementBasePath + "/annotations/groups/batch",
		Body:   json.RawMessage(`{"account_ids":["core-exhausted"],"group_id":"core"}`),
	}, now)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("legacy batch = %d, want 410 removal response", resp.StatusCode)
	}

	// Management auth failure (nil store / unknown key boundary) must not
	// panic and must not open the plugin circuit.
	if resp := func() (out pluginapi.ManagementResponse) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("management failure panicked: %v", recovered)
			}
		}()
		return HandleManagementRequest(nil, pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   managementBasePath + "/status",
		}, now)
	}(); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("nil store status = %d, want 500", resp.StatusCode)
	}

	// Persistence failure rolls back without panic or snapshot republish.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	previousPath := defaultStatePath
	defaultStatePath = func() string { return filepath.Join(blocker, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousPath })
	persistStore := NewPluginState(DefaultConfig())
	groupID := "grp_fixed"
	groups := persistStore.Annotations()
	groups.Groups[groupID] = GroupAnnotation{Name: "Fixed"}
	persistStore.SetAnnotations(groups)
	lifecycle := &ManagementLifecycleSnapshot{Roster: ActiveRoster{
		Capability: CapabilityA, Confirmed: true, Health: RosterHealthy,
		Instances: []string{"auth-a"},
		Inventory: []RosterEntry{{ID: "auth-a", AuthIndex: "idx-a", Provider: "codex", Priority: intPtr(9)}},
	}}
	previousSnapshot := publishedSchedulerSnapshot.Load()
	if resp := func() (out pluginapi.ManagementResponse) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("persistence failure panicked: %v", recovered)
			}
		}()
		return HandleManagementRequestWithLifecycle(persistStore, pluginapi.ManagementRequest{
			Method: http.MethodPost,
			Path:   managementBasePath + "/annotations/groups/batch",
			Body:   json.RawMessage(`{"account_ids":["auth-a"],"group_id":"grp_fixed"}`),
		}, now, *lifecycle)
	}(); resp.StatusCode != http.StatusGone {
		t.Fatalf("legacy batch persistence attempt status = %d, want 410", resp.StatusCode)
	}
	if got := persistStore.Annotations().Accounts["auth:auth-a"].GroupID; got != "" {
		t.Fatalf("failed batch write changed GroupID to %q", got)
	}
	if publishedSchedulerSnapshot.Load() != previousSnapshot {
		t.Fatal("failed batch write republished the scheduler snapshot")
	}
}
