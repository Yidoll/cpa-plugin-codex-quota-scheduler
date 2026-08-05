package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func newGroupContractStore(t *testing.T) *PluginState {
	t.Helper()
	dir := t.TempDir()
	previous := defaultStatePath
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previous })
	return NewPluginState(DefaultConfig())
}

func publishSchedulerContractSnapshot(t *testing.T, snapshot *SchedulerSnapshot) {
	t.Helper()
	previous := publishedSchedulerSnapshot.Load()
	PublishSchedulerSnapshot(snapshot)
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previous) })
}

func strictPoolRequest(ids ...string) pluginapi.SchedulerPickRequest {
	candidates := make([]pluginapi.SchedulerAuthCandidate, 0, len(ids))
	for _, id := range ids {
		candidates = append(candidates, pluginapi.SchedulerAuthCandidate{ID: id, Provider: "codex", Priority: 10, Status: "active"})
	}
	return pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: candidates}
}

func TestHistoricalGroupsLoadWithoutMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := []byte(`{
  "config": {"active_pool": "group:legacy-a"},
  "accounts": {"auth:member": {"group_id": "legacy-a", "alias": "keep"}},
  "groups": {"legacy-a": {"name": "Legacy", "notes": "keep"}}
}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPluginDiskState(path)
	if err != nil {
		t.Fatalf("LoadPluginDiskState: %v", err)
	}
	if loaded.Config.ActivePool != "group:legacy-a" || loaded.Accounts["auth:member"].GroupID != "legacy-a" || loaded.Groups["legacy-a"].Name != "Legacy" {
		t.Fatalf("historical group data was not retained: %#v", loaded)
	}
}

func TestHistoricalGroupDataExportsVerbatimAndDoesNotAffectSelection(t *testing.T) {
	store := NewPluginState(Config{ActivePool: "group:legacy", HandleEnabled: true})
	store.SetAnnotations(AnnotationState{
		Accounts: map[string]AccountAnnotation{"auth:host": {GroupID: "outside", Alias: "kept"}},
		Groups:   map[string]GroupAnnotation{"legacy": {Name: "Legacy"}},
	})
	exported := handleExportState(store, time.Now())
	if exported.StatusCode != 200 || !containsJSONString(exported.Body, `"active_pool":"group:legacy"`) || !containsJSONString(exported.Body, `"group_id":"outside"`) {
		t.Fatalf("export lost historical fields: %s", exported.Body)
	}

	snapshot := SchedulerSnapshot{
		HandleEnabled:     true,
		ActivePool:        "group:legacy",
		AdmissionObserved: true,
		Accounts:          []AccountView{{ID: "auth:host", GroupID: "outside", Instance: 1, Cache: CacheFresh}},
		ActiveHighestTier: map[string]struct{}{"auth:host": {}},
	}
	decision := SelectAccount(snapshot, []Candidate{{ID: "auth:host", Provider: "codex"}}, time.Now())
	if decision.AuthID != "auth:host" {
		t.Fatalf("historical group affected selection: %#v", decision)
	}
	var decoded PluginDiskState
	if err := json.Unmarshal(exported.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Groups["legacy"].Name != "Legacy" {
		t.Fatalf("exported group did not round-trip: %#v", decoded.Groups)
	}
}
