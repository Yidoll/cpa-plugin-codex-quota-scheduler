package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestDisabledNonOAuthEntryDoesNotBlockRosterPublication(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	lister := ABIHostAuthLister{call: func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"files":[
			{"id":"disabled-api-key","auth_index":"idx-disabled","provider":"codex","priority":9,"disabled":true},
			{"id":"active-oauth","auth_index":"idx-active","provider":"codex","priority":9}
		]}`), nil
	}}
	authJSON := map[string]json.RawMessage{
		"disabled-api-key": json.RawMessage(`{"type":"codex","api_key":"API_KEY_SECRET","disabled":true}`),
		"active-oauth":     json.RawMessage(`{"type":"codex","access_token":"ACCESS_SECRET","refresh_token":"REFRESH_SECRET","account_id":"acct"}`),
	}
	controller := NewRosterController(RosterControllerOptions{
		Host: lister,
		Now:  func() time.Time { return now },
		Publish: func(_ context.Context, active ActiveRoster) (ActiveRoster, error) {
			for _, entry := range active.Entries {
				if _, err := ExtractCodexCredentials(authJSON[entry.ID]); err != nil {
					return active, err
				}
			}
			return active, nil
		},
	})

	got, err := controller.Startup(context.Background())
	if err != nil {
		t.Fatalf("disabled non-OAuth entry blocked roster publication: %v", err)
	}
	if !got.Confirmed || got.Health != RosterHealthy || !reflect.DeepEqual(got.Instances, []string{"active-oauth"}) {
		t.Fatalf("roster = %#v, want only active OAuth entry", got)
	}
}

func TestABIHostAuthListerRetainsCompleteCodexEntriesAndExcludesMissing(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantID  string
		wantLen int
	}{
		{name: "eligible", file: `{"id":"eligible","auth_index":"idx","provider":"codex","priority":7}`, wantID: "eligible", wantLen: 1},
		{name: "missing priority defaults to zero", file: `{"id":"default-priority","auth_index":"idx","provider":"codex"}`, wantID: "default-priority", wantLen: 1},
		{name: "disabled", file: `{"id":"disabled","auth_index":"idx","provider":"codex","disabled":true}`, wantID: "disabled", wantLen: 1},
		{name: "unavailable", file: `{"id":"unavailable","auth_index":"idx","provider":"codex","unavailable":true}`, wantID: "unavailable", wantLen: 1},
		{name: "missing id", file: `{"auth_index":"idx","provider":"codex"}`},
		{name: "missing auth index", file: `{"id":"missing-index","provider":"codex"}`},
		{name: "non codex", file: `{"id":"claude","auth_index":"idx","provider":"claude"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lister := ABIHostAuthLister{call: func(string, any) (json.RawMessage, error) {
				return json.RawMessage(`{"files":[` + tt.file + `]}`), nil
			}}
			entries, err := lister.ListHostAuths(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != tt.wantLen {
				t.Fatalf("entries = %#v, want len %d", entries, tt.wantLen)
			}
			if tt.wantLen == 0 {
				return
			}
			if entries[0].ID != tt.wantID || entries[0].AuthIndex == "" || entries[0].Priority == nil {
				t.Fatalf("entry = %#v, want retained codex entry %q", entries[0], tt.wantID)
			}
			if tt.name == "missing priority defaults to zero" && *entries[0].Priority != 0 {
				t.Fatalf("priority = %d, want 0", *entries[0].Priority)
			}
			if tt.name == "disabled" && !entries[0].Disabled {
				t.Fatalf("disabled entry lost Disabled flag: %#v", entries[0])
			}
			if tt.name == "unavailable" && !entries[0].Unavailable {
				t.Fatalf("unavailable entry lost Unavailable flag: %#v", entries[0])
			}
		})
	}
}

func TestEligibleHighestTierMovesDownAndRecovers(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{"files":[
		{"id":"high-disabled","auth_index":"idx-high","provider":"codex","priority":9,"disabled":true},
		{"id":"low-active","auth_index":"idx-low","provider":"codex","priority":1}
	]}`)
	lister := ABIHostAuthLister{call: func(string, any) (json.RawMessage, error) {
		return append(json.RawMessage(nil), payload...), nil
	}}
	controller := NewRosterController(RosterControllerOptions{Host: lister, Now: func() time.Time { return now }})

	initial, err := controller.Startup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if initial.HighestPriority != 1 || !reflect.DeepEqual(initial.Instances, []string{"low-active"}) {
		t.Fatalf("initial roster = %#v, want lower eligible tier", initial)
	}

	payload = json.RawMessage(`{"files":[
		{"id":"high-restored","auth_index":"idx-high","provider":"codex","priority":9},
		{"id":"low-active","auth_index":"idx-low","provider":"codex","priority":1}
	]}`)
	now = now.Add(time.Minute)
	recovered, err := controller.WakeForManagement(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.HighestPriority != 9 || !reflect.DeepEqual(recovered.Instances, []string{"high-restored"}) || recovered.Generation <= initial.Generation {
		t.Fatalf("recovered roster = %#v, want restored higher eligible tier after generation %d", recovered, initial.Generation)
	}
}

func TestFilteredRosterPublishesOnlyEligibleProductionState(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	legacyPath := filepath.Join(t.TempDir(), "state.json")
	host := &countingProductionHost{auth: map[string]pluginapi.HostAuthGetResponse{
		"idx-active": {AuthIndex: "idx-active", Name: "active.json", JSON: json.RawMessage(`{"access_token":"ACCESS_SECRET","refresh_token":"REFRESH_SECRET","account_id":"acct"}`)},
	}}
	state := NewPluginState(DefaultConfig())
	adapter := &rosterCredentialHost{host: host, roster: HostRosterSnapshot{Capability: CapabilityB}}
	runtime, err := NewProductionQuotaRefresher(host, state, adapter, HostRosterSnapshot{Capability: CapabilityB}, legacyPath, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.coordinator.Close()
	adapter.bindings = runtime.bindings
	previousSnapshot := publishedSchedulerSnapshot.Load()
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previousSnapshot) })

	lister := ABIHostAuthLister{call: func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"files":[
			{"id":"disabled","auth_index":"idx-disabled","provider":"codex","priority":9,"disabled":true},
			{"id":"unavailable","auth_index":"idx-unavailable","provider":"codex","priority":9,"unavailable":true},
			{"id":"active","auth_index":"idx-active","provider":"codex","priority":9}
		]}`), nil
	}}
	controller := NewRosterController(RosterControllerOptions{
		Host: lister,
		Now:  func() time.Time { return now },
		Publish: func(ctx context.Context, active ActiveRoster) (ActiveRoster, error) {
			if err := runtime.PublishAuthoritativeRoster(ctx, hostRosterSnapshotFromActive(active)); err != nil {
				return active, err
			}
			active.Generation = runtime.runtimeRoster().Generation
			return active, nil
		},
		Observe: runtime.ObserveRosterLifecycle,
	})

	confirmed, err := controller.Startup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(confirmed.Instances, []string{"active"}) || host.get != 1 {
		t.Fatalf("confirmed=%#v host.get=%d, want only one eligible binding", confirmed, host.get)
	}
	admission := state.CPAAdmission()
	if !admission.Observed || len(admission.AuthIDs) != 1 {
		t.Fatalf("admission = %#v, want one eligible account", admission)
	}
	if _, ok := admission.AuthIDs["active"]; !ok {
		t.Fatalf("admission = %#v, want active", admission)
	}
	persisted, err := runtime.runtimeStore.PersistentSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LastConfirmedRoster == nil || len(persisted.LastConfirmedRoster.Entries) != 1 || persisted.LastConfirmedRoster.Entries[0].ID != "active" || len(persisted.Bindings) != 1 {
		t.Fatalf("persistent roster/bindings = %#v / %#v", persisted.LastConfirmedRoster, persisted.Bindings)
	}
	snapshot := publishedSchedulerSnapshot.Load()
	if snapshot == nil || len(snapshot.ActiveHighestTier) != 1 {
		t.Fatalf("scheduler snapshot = %#v, want one eligible active account", snapshot)
	}
	if _, ok := snapshot.ActiveHighestTier["active"]; !ok {
		t.Fatalf("scheduler active tier = %#v, want active", snapshot.ActiveHighestTier)
	}
}

func TestRosterFilterSummaryIsAggregateAndNonSensitive(t *testing.T) {
	var got RosterFilterSummary
	lister := ABIHostAuthLister{
		call: func(string, any) (json.RawMessage, error) {
			return json.RawMessage(`{"files":[
				{"id":"SECRET_DISABLED_ID","auth_index":"SECRET_DISABLED_INDEX","provider":"codex","disabled":true,"name":"SECRET_FILE.json","email":"SECRET_EMAIL"},
				{"id":"unavailable","auth_index":"idx-unavailable","provider":"codex","unavailable":true},
				{"id":"missing-index","provider":"codex"},
				{"auth_index":"idx-missing-id","provider":"codex"},
				{"id":"other","auth_index":"idx-other","provider":"claude"},
				{"id":"active","auth_index":"idx-active","provider":"codex"}
			]}`), nil
		},
		observe: func(summary RosterFilterSummary) { got = summary },
	}
	entries, err := lister.ListHostAuths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := RosterFilterSummary{Received: 6, Eligible: 1, ExcludedNonCodex: 1, ExcludedDisabled: 1, ExcludedUnavailable: 1, ExcludedMissingID: 1, ExcludedMissingIndex: 1}
	if got != want {
		t.Fatalf("summary=%#v, want %#v", got, want)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"SECRET_DISABLED_ID", "active", "unavailable"}) {
		t.Fatalf("entries = %v, want complete protected codex entries", ids)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET_DISABLED_ID", "SECRET_DISABLED_INDEX", "SECRET_FILE", "SECRET_EMAIL"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("aggregate summary leaked %q: %s", secret, raw)
		}
	}
}

func TestBackgroundRefreshUsesRosterEligibilityNormalization(t *testing.T) {
	auths := []pluginapi.HostAuthFileEntry{
		{ID: " active ", AuthIndex: " idx-active ", Provider: " CodEx "},
		{ID: "blank-index", AuthIndex: "   ", Provider: "codex"},
		{ID: "   ", AuthIndex: "idx-blank-id", Provider: "codex"},
		{ID: "disabled", AuthIndex: "idx-disabled", Provider: "codex", Disabled: true},
		{ID: "unavailable", AuthIndex: "idx-unavailable", Provider: "codex", Unavailable: true},
	}
	admission := CPAAdmissionState{Observed: true, AuthIDs: map[string]struct{}{"active": {}, "blank-index": {}, "disabled": {}, "unavailable": {}}}

	got := filterAdmittedAuths(auths, admission)
	if len(got) != 1 || got[0].ID != "active" || got[0].AuthIndex != "idx-active" || got[0].Provider != "codex" {
		t.Fatalf("filtered auths = %#v, want one normalized active Codex auth", got)
	}
}

func TestPublishRosterFilterSummaryDeduplicatesConcurrentLogs(t *testing.T) {
	previousState := globalState
	previousSummary := hostRosterFilterPublished.Load()
	globalState = NewPluginState(DefaultConfig())
	hostRosterFilterPublished.Store(nil)
	t.Cleanup(func() {
		globalState = previousState
		hostRosterFilterPublished.Store(previousSummary)
	})

	summary := RosterFilterSummary{Received: 2, Eligible: 1, ExcludedDisabled: 1}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publishRosterFilterSummary(summary)
		}()
	}
	wg.Wait()

	count := 0
	for _, entry := range globalState.Snapshot(time.Now()).Logs {
		if entry.Event == "roster.filtered" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("roster.filtered logs = %d, want 1", count)
	}
}

func TestFreeAccountEligibilityPolicy(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	fresh := func(id, plan string) AccountView {
		return AccountView{ID: id, Instance: AuthInstanceID(len(id)), Cache: CacheFresh, PlanType: plan}
	}
	unavailable := fresh("paid", "plus")
	unavailable.Cache = CacheStale
	unavailable.LastKnownAvailable = false

	tests := []struct {
		name        string
		exclude     bool
		accounts    []AccountView
		candidates  []Candidate
		active      []string
		wantAuthID  string
		wantReason  string
		wantFree    int
		wantUnknown int
	}{
		{
			name: "known non-free excludes free and unknown", exclude: true,
			accounts:   []AccountView{fresh("free", "free"), fresh("unknown", ""), fresh("paid", "plus")},
			candidates: []Candidate{{ID: "free", Provider: "codex"}, {ID: "unknown", Provider: "codex"}, {ID: "paid", Provider: "codex"}},
			wantAuthID: "paid", wantFree: 1, wantUnknown: 1,
		},
		{
			name: "all free relaxes filter", exclude: true,
			accounts:   []AccountView{fresh("free-b", "free"), fresh("free-a", "FREE")},
			candidates: []Candidate{{ID: "free-b", Provider: "codex"}, {ID: "free-a", Provider: "codex"}},
			wantAuthID: "free-a", wantReason: "selected",
		},
		{
			name: "paid outside request does not prevent all-free relaxation", exclude: true,
			accounts:   []AccountView{fresh("free", "free"), fresh("off-request-paid", "plus")},
			candidates: []Candidate{{ID: "free", Provider: "codex"}},
			active:     []string{"free", "off-request-paid"},
			wantAuthID: "free", wantReason: "selected",
		},
		{
			name: "unadmitted paid candidate does not prevent all-free relaxation", exclude: true,
			accounts:   []AccountView{fresh("free", "free"), fresh("unadmitted-paid", "plus")},
			candidates: []Candidate{{ID: "free", Provider: "codex"}, {ID: "unadmitted-paid", Provider: "codex"}},
			active:     []string{"free"},
			wantAuthID: "free", wantReason: "selected",
		},
		{
			name: "all unknown delegates fallback", exclude: true,
			accounts:   []AccountView{fresh("unknown-a", ""), fresh("unknown-b", "")},
			candidates: []Candidate{{ID: "unknown-a", Provider: "codex"}, {ID: "unknown-b", Provider: "codex"}},
			wantReason: "no_selectable_account", wantUnknown: 2,
		},
		{
			name: "free and unknown delegates fallback", exclude: true,
			accounts:   []AccountView{fresh("free", "free"), fresh("unknown", "")},
			candidates: []Candidate{{ID: "free", Provider: "codex"}, {ID: "unknown", Provider: "codex"}},
			wantReason: "no_selectable_account", wantFree: 1, wantUnknown: 1,
		},
		{
			name: "unavailable paid does not relax free", exclude: true,
			accounts:   []AccountView{unavailable, fresh("free", "free")},
			candidates: []Candidate{{ID: "paid", Provider: "codex"}, {ID: "free", Provider: "codex"}},
			wantReason: "no_selectable_account", wantFree: 1,
		},
		{
			name: "disabled config preserves old behavior", exclude: false,
			accounts:   []AccountView{fresh("a-free", "free"), fresh("z-paid", "plus")},
			candidates: []Candidate{{ID: "a-free", Provider: "codex"}, {ID: "z-paid", Provider: "codex"}},
			wantAuthID: "a-free", wantReason: "selected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			active := make(map[string]struct{}, len(tt.active)+len(tt.candidates))
			if tt.active == nil {
				for _, candidate := range tt.candidates {
					active[candidate.ID] = struct{}{}
				}
			} else {
				for _, authID := range tt.active {
					active[authID] = struct{}{}
				}
			}
			snapshot := SchedulerSnapshot{Fallback: FallbackFillFirst, Accounts: tt.accounts, ActiveHighestTier: active, AdmissionObserved: true}
			setFutureBoolField(t, &snapshot, "ExcludeFreeAccounts", tt.exclude)
			result := SelectAccount(snapshot, tt.candidates, now)
			if result.AuthID != tt.wantAuthID {
				t.Fatalf("AuthID = %q, want %q; result=%#v", result.AuthID, tt.wantAuthID, result)
			}
			if tt.wantReason != "" && result.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q; result=%#v", result.Reason, tt.wantReason, result)
			}
			if result.AuthID == "" && !result.Fallback {
				t.Fatalf("result = %#v, want configured fallback", result)
			}
			if got := unavailableReasonCount(result, "free_account"); got != tt.wantFree {
				t.Fatalf("free_account count = %d, want %d; result=%#v", got, tt.wantFree, result)
			}
			if got := unavailableReasonCount(result, "unknown_plan"); got != tt.wantUnknown {
				t.Fatalf("unknown_plan count = %d, want %d; result=%#v", got, tt.wantUnknown, result)
			}
		})
	}
}

func setFutureBoolField(t *testing.T, target any, name string, value bool) {
	t.Helper()
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	if !field.IsValid() {
		t.Fatalf("production type is missing required field %s", name)
	}
	if field.Kind() != reflect.Bool || !field.CanSet() {
		t.Fatalf("production field %s is not a settable bool", name)
	}
	field.SetBool(value)
}

func unavailableReasonCount(result SelectionResult, reason string) int {
	count := 0
	for _, item := range result.Unavailable {
		if item.Reason == reason {
			count++
		}
	}
	return count
}
