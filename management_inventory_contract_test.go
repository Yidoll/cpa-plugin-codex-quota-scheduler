package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagementInventorySchemaRequiresProtectedEntrySafetyFields(t *testing.T) {
	if missing := managementInventorySchemaMissing(); len(missing) != 0 {
		t.Fatalf("management inventory schema missing required fields: %s", strings.Join(missing, ", "))
	}
}

func TestManagementInventoryPublishesEveryCodexEntryWithoutPollutingRoster(t *testing.T) {
	requireManagementInventorySchema(t)

	state, controller, listCalls := installManagementInventoryFixture(t)
	payload := readManagementInventoryStatus(t)
	if *listCalls != 1 {
		t.Fatalf("host.auth.list calls = %d, want one shared inventory/roster input", *listCalls)
	}
	active := controller.Snapshot()
	if active.HighestPriority != 9 || !reflect.DeepEqual(active.Instances, []string{"highest-oauth"}) {
		t.Fatalf("active roster = %#v, want only highest eligible OAuth tier", active)
	}
	assertExactStringSet(t, state.CPAAdmission().AuthIDs, []string{"highest-oauth"}, "CPAAdmission")
	scheduler := publishedSchedulerSnapshot.Load()
	if scheduler == nil {
		t.Fatal("scheduler snapshot missing after authoritative roster publication")
	}
	assertExactStringSet(t, scheduler.ActiveHighestTier, []string{"highest-oauth"}, "SchedulerSnapshot authoritative candidates")

	entries := managementInventoryEntries(t, payload)
	if got, want := inventoryAuthIDs(t, entries), []string{"api-key", "auth-failure", "disabled", "highest-oauth", "low-oauth", "unavailable"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory auth IDs = %v, want complete protected Codex inventory %v", got, want)
	}
	byID := managementInventoryEntriesByAuthID(t, entries)
	if got := managementInventoryEntryString(t, byID["api-key"], "CredentialKind"); got != "API_KEY" {
		t.Fatalf("API key credential kind = %q, want API_KEY", got)
	}
	for _, authID := range []string{"low-oauth", "disabled", "unavailable", "auth-failure", "api-key"} {
		if got := managementInventoryEntryString(t, byID[authID], "SchedulingReason"); got == "" {
			t.Fatalf("inventory entry %q has no stable non-scheduling reason", authID)
		}
		if state.IsAuthAdmitted(authID) {
			t.Fatalf("protected inventory entry %q leaked into CPAAdmission", authID)
		}
		if _, candidate := scheduler.ActiveHighestTier[authID]; candidate {
			t.Fatalf("protected inventory entry %q leaked into SchedulerSnapshot", authID)
		}
	}
}

func TestManagementInventoryStatusSnapshotsAreIndependent(t *testing.T) {
	requireManagementInventorySchema(t)

	_, _, listCalls := installManagementInventoryFixture(t)
	first := readManagementInventoryStatus(t)
	firstEntries := managementInventoryEntries(t, first)
	byID := managementInventoryEntriesByAuthID(t, firstEntries)
	for _, authID := range []string{"disabled", "unavailable", "auth-failure"} {
		if reason := managementInventoryEntryString(t, byID[authID], "SchedulingReason"); reason == "" {
			t.Fatalf("%q has no protected inventory reason", authID)
		}
	}

	mutateDecodedManagementInventoryAuthID(t, &first, "highest-oauth", "mutated-client-copy")
	second := readManagementInventoryStatus(t)
	if *listCalls != 1 {
		t.Fatalf("second management status caused host.auth.list calls = %d, want cached single publication", *listCalls)
	}
	if got := inventoryAuthIDs(t, managementInventoryEntries(t, second)); !reflect.DeepEqual(got, []string{"api-key", "auth-failure", "disabled", "highest-oauth", "low-oauth", "unavailable"}) {
		t.Fatalf("decoded inventory mutation leaked into a later management response: %v", got)
	}
}

func installManagementInventoryFixture(t *testing.T) (*PluginState, *RosterController, *int) {
	t.Helper()
	now := time.Now().UTC()
	listCalls := 0
	lister := ABIHostAuthLister{call: func(method string, _ any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostAuthList {
			t.Fatalf("host method = %q, want host.auth.list", method)
		}
		listCalls++
		return json.RawMessage(`{"files":[
			{"id":"highest-oauth","auth_index":"idx-high","provider":"codex","account_type":"oauth","status":"active","priority":9},
			{"id":"low-oauth","auth_index":"idx-low","provider":"codex","account_type":"oauth","status":"active","priority":1},
			{"id":"disabled","auth_index":"idx-disabled","provider":"codex","account_type":"oauth","status":"active","priority":9,"disabled":true},
			{"id":"unavailable","auth_index":"idx-unavailable","provider":"codex","account_type":"oauth","status":"unavailable","priority":9,"unavailable":true},
			{"id":"auth-failure","auth_index":"idx-auth-failure","provider":"codex","account_type":"oauth","status":"error","status_message":"authentication failed","priority":9},
			{"id":"api-key","auth_index":"idx-api-key","provider":"codex","account_type":"api_key","status":"active","priority":9},
			{"id":"other-provider","auth_index":"idx-other","provider":"claude","account_type":"oauth","status":"active","priority":99}
		]}`), nil
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
	return state, controller, &listCalls
}

func installManagementInventoryGlobals(t *testing.T, state *PluginState, controller *RosterController) {
	t.Helper()
	previousSnapshot := publishedSchedulerSnapshot.Load()
	refresherMu.Lock()
	previousState, previousRefresher, previousController := globalState, globalRefresher, globalRosterController
	globalState, globalRefresher, globalRosterController = state, nil, controller
	refresherMu.Unlock()
	t.Cleanup(func() {
		refresherMu.Lock()
		globalState, globalRefresher, globalRosterController = previousState, previousRefresher, previousController
		refresherMu.Unlock()
		publishedSchedulerSnapshot.Store(previousSnapshot)
	})
}

func readManagementInventoryStatus(t *testing.T) StatusPayload {
	t.Helper()
	rawRequest, err := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   managementBasePath + "/status",
		Query:  url.Values{"format": []string{"json"}},
	})
	if err != nil {
		t.Fatalf("encode management request: %v", err)
	}
	rawResponse, err := handleManagementHandle(rawRequest)
	if err != nil {
		t.Fatalf("management status: %v", err)
	}
	var outer envelope
	if err := json.Unmarshal(rawResponse, &outer); err != nil {
		t.Fatalf("decode management envelope: %v", err)
	}
	var response pluginapi.ManagementResponse
	if err := json.Unmarshal(outer.Result, &response); err != nil {
		t.Fatalf("decode management response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("management status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var payload StatusPayload
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatalf("decode management payload: %v", err)
	}
	return payload
}

func managementInventorySchemaMissing() []string {
	statusType := reflect.TypeFor[StatusPayload]()
	inventoryField, ok := statusType.FieldByName("Inventory")
	if !ok || inventoryField.Type.Kind() != reflect.Struct {
		return []string{"StatusPayload.Inventory"}
	}
	inventoryType := inventoryField.Type
	missing := make([]string, 0, 7)
	entriesField, entriesOK := inventoryType.FieldByName("Entries")
	if !entriesOK || entriesField.Type.Kind() != reflect.Slice || entriesField.Type.Elem().Kind() != reflect.Struct {
		return append(missing, "StatusPayload.Inventory.Entries[]")
	}
	entryType := entriesField.Type.Elem()
	for _, field := range []struct {
		name string
		kind reflect.Kind
	}{
		{name: "AuthID", kind: reflect.String},
		{name: "AuthIndex", kind: reflect.String},
		{name: "Provider", kind: reflect.String},
		{name: "Priority", kind: reflect.Int},
		{name: "CredentialKind", kind: reflect.String},
		{name: "SchedulingReason", kind: reflect.String},
	} {
		value, exists := entryType.FieldByName(field.name)
		if !exists || value.Type.Kind() != field.kind {
			missing = append(missing, "StatusPayload.Inventory.Entries."+field.name)
		}
	}
	return missing
}

func requireManagementInventorySchema(t *testing.T) {
	t.Helper()
	if missing := managementInventorySchemaMissing(); len(missing) != 0 {
		t.Skipf("schema contract RED 后激活：missing %s", strings.Join(missing, ", "))
	}
}

func managementInventoryEntries(t *testing.T, payload StatusPayload) []reflect.Value {
	t.Helper()
	entries := reflect.ValueOf(payload).FieldByName("Inventory").FieldByName("Entries")
	if !entries.IsValid() || entries.Kind() != reflect.Slice {
		t.Fatalf("management inventory entries = %#v, want a slice", payload)
	}
	result := make([]reflect.Value, entries.Len())
	for index := range result {
		result[index] = entries.Index(index)
	}
	return result
}

func managementInventoryEntriesByAuthID(t *testing.T, entries []reflect.Value) map[string]reflect.Value {
	t.Helper()
	byID := make(map[string]reflect.Value, len(entries))
	for _, entry := range entries {
		authID := managementInventoryEntryString(t, entry, "AuthID")
		if _, duplicate := byID[authID]; duplicate {
			t.Fatalf("duplicate inventory auth ID %q", authID)
		}
		byID[authID] = entry
	}
	return byID
}

func managementInventoryEntryString(t *testing.T, entry reflect.Value, fieldName string) string {
	t.Helper()
	field := entry.FieldByName(fieldName)
	if !field.IsValid() || field.Kind() != reflect.String {
		t.Fatalf("inventory entry field %s is not a string", fieldName)
	}
	return field.String()
}

func inventoryAuthIDs(t *testing.T, entries []reflect.Value) []string {
	t.Helper()
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, managementInventoryEntryString(t, entry, "AuthID"))
	}
	sort.Strings(ids)
	return ids
}

func assertExactStringSet(t *testing.T, got map[string]struct{}, want []string, label string) {
	t.Helper()
	actual := make([]string, 0, len(got))
	for value := range got {
		actual = append(actual, value)
	}
	sort.Strings(actual)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	if !reflect.DeepEqual(actual, sortedWant) {
		t.Fatalf("%s = %v, want %v", label, actual, sortedWant)
	}
}

func mutateDecodedManagementInventoryAuthID(t *testing.T, payload *StatusPayload, from, to string) {
	t.Helper()
	entries := reflect.ValueOf(payload).Elem().FieldByName("Inventory").FieldByName("Entries")
	for index := 0; index < entries.Len(); index++ {
		entry := entries.Index(index)
		if managementInventoryEntryString(t, entry, "AuthID") == from {
			entry.FieldByName("AuthID").SetString(to)
			return
		}
	}
	t.Fatalf("inventory entry %q not found for mutation", from)
}

func TestManagementInventoryMergesQuotaCacheAndMarksMissing(t *testing.T) {
	requireManagementInventorySchema(t)

	now := time.Now().UTC()
	used := 42.0
	state, _, listCalls := installManagementInventoryFixture(t)
	// The first status read publishes the authoritative roster; admission
	// publication prunes non-admitted accounts from the in-memory cache,
	// matching production. A cache present afterwards is merged below.
	_ = readManagementInventoryStatus(t)
	state.UpsertQuota(AccountState{
		AuthID:        "low-oauth",
		AuthIndex:     "idx-low",
		PlanType:      "plus",
		Family:        AccountFamilyMonthly,
		LastSuccessAt: now.Add(-time.Hour),
		Quota:         ParsedQuota{FiveHour: &QuotaWindow{Kind: WindowFiveHour, UsedPercent: &used}},
	})
	state.UpsertQuota(AccountState{
		AuthID:        "highest-oauth",
		AuthIndex:     "idx-high",
		PlanType:      "pro",
		Family:        AccountFamilyWeekly,
		LastSuccessAt: now.Add(-time.Minute),
		Quota:         ParsedQuota{LongWindow: &QuotaWindow{Kind: WindowWeekly, UsedPercent: &used}},
	})

	payload := readManagementInventoryStatus(t)
	if *listCalls != 1 {
		t.Fatalf("host.auth.list calls = %d after cached merge read, want 1", *listCalls)
	}
	entries := managementInventoryTypedEntries(payload)
	if got := entries["low-oauth"].PlanType; got != "plus" {
		t.Fatalf("low-oauth merged PlanType = %q, want plus", got)
	}
	if got := entries["low-oauth"].Family; got != AccountFamilyMonthly {
		t.Fatalf("low-oauth merged Family = %q, want monthly", got)
	}
	if got := entries["low-oauth"].QuotaState; got != "known" {
		t.Fatalf("low-oauth QuotaState = %q, want known", got)
	}
	if entries["low-oauth"].LastSuccessAt == nil || !entries["low-oauth"].LastSuccessAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("low-oauth LastSuccessAt = %v, want cached success time", entries["low-oauth"].LastSuccessAt)
	}
	if got := entries["disabled"].QuotaState; got != "missing" {
		t.Fatalf("disabled QuotaState = %q, want missing (no cache, no quota I/O)", got)
	}
	if !containsString(entries["disabled"].AdditionalReasons, "quota_missing") {
		t.Fatalf("disabled AdditionalReasons = %v, want quota_missing", entries["disabled"].AdditionalReasons)
	}
	if got := entries["highest-oauth"].QuotaState; got != "known" {
		t.Fatalf("highest-oauth QuotaState = %q, want known", got)
	}
}

func TestManagementInventoryWaitingState(t *testing.T) {
	requireManagementInventorySchema(t)

	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	state := NewPluginState(DefaultConfig())
	controller := NewRosterController(RosterControllerOptions{
		Host: ABIHostAuthLister{call: func(string, any) (json.RawMessage, error) {
			return nil, errors.New("host unavailable")
		}},
		Now: func() time.Time { return now },
	})
	installManagementInventoryGlobals(t, state, controller)

	payload := readManagementInventoryStatus(t)
	if payload.Inventory.State != "waiting_inventory" {
		t.Fatalf("inventory state = %q, want waiting_inventory", payload.Inventory.State)
	}
	if payload.Inventory.Confirmed {
		t.Fatal("inventory confirmed before any successful sync")
	}
	if payload.Inventory.FailClosed {
		t.Fatal("inventory fail_closed before any confirmed roster")
	}
	if payload.Inventory.LastConfirmedAt != nil {
		t.Fatalf("LastConfirmedAt = %v, want nil before first confirmation", payload.Inventory.LastConfirmedAt)
	}
	if len(payload.Inventory.Entries) != 0 {
		t.Fatalf("waiting inventory entries = %d, want none", len(payload.Inventory.Entries))
	}
}

func TestManagementInventoryStaleAndFailClosedStates(t *testing.T) {
	requireManagementInventorySchema(t)

	start := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	current := start
	hostUp := true
	lister := ABIHostAuthLister{call: func(string, any) (json.RawMessage, error) {
		if !hostUp {
			return nil, errors.New("host unavailable")
		}
		return json.RawMessage(`{"files":[
			{"id":"highest-oauth","auth_index":"idx-high","provider":"codex","account_type":"oauth","status":"active","priority":9}
		]}`), nil
	}}
	state := NewPluginState(DefaultConfig())
	controller := NewRosterController(RosterControllerOptions{
		Host: lister,
		Now:  func() time.Time { mu.Lock(); defer mu.Unlock(); return current },
	})
	installManagementInventoryGlobals(t, state, controller)

	first := readManagementInventoryStatus(t)
	if first.Inventory.State != "confirmed" || !first.Inventory.Confirmed || first.Inventory.LastConfirmedAt == nil || !first.Inventory.LastConfirmedAt.Equal(start) {
		t.Fatalf("confirmed inventory = %#v, want confirmed at %v", first.Inventory, start)
	}
	if len(first.Inventory.Entries) != 1 {
		t.Fatalf("confirmed entries = %d, want 1", len(first.Inventory.Entries))
	}

	mu.Lock()
	current = start.Add(10 * time.Minute)
	mu.Unlock()
	hostUp = false
	stale := readManagementInventoryStatus(t)
	if stale.Inventory.State != "inventory_stale" {
		t.Fatalf("stale inventory state = %q, want inventory_stale", stale.Inventory.State)
	}
	if !stale.Inventory.Confirmed || stale.Inventory.FailClosed {
		t.Fatalf("degraded inventory flags = confirmed:%v fail_closed:%v, want confirmed without fail closed", stale.Inventory.Confirmed, stale.Inventory.FailClosed)
	}
	if stale.Inventory.LastConfirmedAt == nil || !stale.Inventory.LastConfirmedAt.Equal(start) {
		t.Fatalf("stale LastConfirmedAt = %v, want retained %v", stale.Inventory.LastConfirmedAt, start)
	}
	if len(stale.Inventory.Entries) != 1 {
		t.Fatalf("stale inventory lost last confirmed entries: %d", len(stale.Inventory.Entries))
	}

	mu.Lock()
	current = start.Add(41 * time.Minute)
	mu.Unlock()
	closed := readManagementInventoryStatus(t)
	if closed.Inventory.State != "inventory_stale" || !closed.Inventory.FailClosed {
		t.Fatalf("fail closed inventory = %#v, want inventory_stale with fail_closed", closed.Inventory)
	}
	if len(closed.Inventory.Entries) != 1 {
		t.Fatalf("fail closed inventory lost last confirmed entries: %d", len(closed.Inventory.Entries))
	}
}

func TestManagementInventoryStatusPollingIsCacheOnly(t *testing.T) {
	requireManagementInventorySchema(t)

	listCalls := 0
	getCalls := 0
	lister := ABIHostAuthLister{call: func(method string, _ any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			listCalls++
			return json.RawMessage(`{"files":[
				{"id":"highest-oauth","auth_index":"idx-high","provider":"codex","account_type":"oauth","status":"active","priority":9},
				{"id":"disabled","auth_index":"idx-disabled","provider":"codex","account_type":"oauth","status":"active","priority":9,"disabled":true}
			]}`), nil
		case pluginabi.MethodHostAuthGet:
			getCalls++
			return json.RawMessage(`{"auth_index":"idx-disabled","json":{"type":"codex","access_token":"SECRET"}}`), nil
		default:
			t.Fatalf("unexpected host method %q", method)
			return nil, errors.New("unexpected method")
		}
	}}
	state := NewPluginState(DefaultConfig())
	controller := NewRosterController(RosterControllerOptions{Host: lister, Now: func() time.Time { return time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC) }})
	installManagementInventoryGlobals(t, state, controller)

	for i := 0; i < 3; i++ {
		payload := readManagementInventoryStatus(t)
		if len(payload.Inventory.Entries) != 2 {
			t.Fatalf("read %d inventory entries = %d, want 2", i+1, len(payload.Inventory.Entries))
		}
	}
	if listCalls != 1 {
		t.Fatalf("host.auth.list calls = %d after 3 page polls, want 1 shared inventory/roster input", listCalls)
	}
	if getCalls != 0 {
		t.Fatalf("host.auth.get calls = %d, inventory must never fetch credentials for excluded entries", getCalls)
	}
}

func managementInventoryTypedEntries(payload StatusPayload) map[string]InventoryEntry {
	byID := make(map[string]InventoryEntry, len(payload.Inventory.Entries))
	for _, entry := range payload.Inventory.Entries {
		byID[entry.AuthID] = entry
	}
	return byID
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
