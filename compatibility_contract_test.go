package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompatibilityOldUserDataWithAccountsLoadsGracefully(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".user-data.json")
	raw := []byte(`{
  "schema_version": 1,
  "config": {},
  "accounts": {"auth:old": {"alias": "legacy-alias", "group_id": "old-group"}}
}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	state, loaded, err := loadUserData(path)
	if err != nil || !loaded {
		t.Fatalf("loadUserData: loaded=%v err=%v", loaded, err)
	}
	if got := activePoolConfigValue(t, state.Config); got != "all" {
		t.Fatalf("active_pool = %q, want legacy default all", got)
	}
	annotation, ok := state.Accounts["auth:old"]
	if !ok || annotation.Alias != "legacy-alias" || annotation.GroupID != "old-group" {
		t.Fatalf("legacy account annotation lost: %#v", state.Accounts)
	}
	if len(state.Groups) != 0 {
		t.Fatalf("missing groups field should stay empty, got %#v", state.Groups)
	}
}

func TestCompatibilityUnknownFieldsInUserDataAndImportAreIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".user-data.json")
	raw := []byte(`{
  "schema_version": 1,
  "config": {"active_pool": "ungrouped"},
  "future_top_level": {"anything": 1},
  "accounts": {"auth:old": {"alias": "kept", "future_account_field": "ignored"}},
  "groups": {"grp_1": {"name": "One", "future_group_field": true}}
}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	state, loaded, err := loadUserData(path)
	if err != nil || !loaded {
		t.Fatalf("loadUserData: loaded=%v err=%v", loaded, err)
	}
	if got := activePoolConfigValue(t, state.Config); got != "ungrouped" {
		t.Fatalf("active_pool = %q, want ungrouped", got)
	}
	if state.Accounts["auth:old"].Alias != "kept" {
		t.Fatalf("known account field lost: %#v", state.Accounts)
	}
	if state.Groups["grp_1"].Name != "One" {
		t.Fatalf("known group field lost: %#v", state.Groups)
	}

	previousPath := defaultStatePath
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousPath })
	target := NewPluginState(DefaultConfig())
	importBody := []byte(`{
  "config": {"active_pool": "group:grp_1"},
  "accounts": {"auth:old": {"alias": "kept", "future_account_field": "ignored"}},
  "groups": {"grp_1": {"name": "One", "future_group_field": true}}
}`)
	response := handleImportState(target, importBody, time.Now())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("import with unknown fields status = %d body=%s, want 200", response.StatusCode, response.Body)
	}
	if got := activePoolConfigValue(t, target.Config()); got != "group:grp_1" {
		t.Fatalf("import active_pool = %q, want group:grp_1", got)
	}
	if target.Annotations().Accounts["auth:old"].Alias != "kept" {
		t.Fatalf("import known account field lost: %#v", target.Annotations().Accounts)
	}
}

func TestCompatibilityBackupRestorePreservesGroupsAndActivePool(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".user-data.json")
	firstConfig := DefaultConfig()
	setActivePoolConfigValue(t, &firstConfig, "group:grp_1")
	first := PluginDiskState{
		Config: firstConfig,
		Accounts: map[string]AccountAnnotation{
			"auth:member": {Alias: "first", GroupID: "grp_1", Tags: []string{"team"}},
		},
		Groups: map[string]GroupAnnotation{
			"grp_1": {Name: "One", Notes: "keep", Tags: []string{"g"}, Color: "#ffffff"},
		},
	}
	if err := SaveUserData(path, first); err != nil {
		t.Fatal(err)
	}
	secondConfig := DefaultConfig()
	setActivePoolConfigValue(t, &secondConfig, "all")
	second := PluginDiskState{
		Config:   secondConfig,
		Accounts: map[string]AccountAnnotation{"auth:member": {Alias: "second"}},
	}
	if err := SaveUserData(path, second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{truncated"), 0600); err != nil {
		t.Fatal(err)
	}

	got, loaded, err := loadUserData(path)
	if err != nil || !loaded {
		t.Fatalf("loadUserData: loaded=%v err=%v", loaded, err)
	}
	if value := activePoolConfigValue(t, got.Config); value != "group:grp_1" {
		t.Fatalf("recovered active_pool = %q, want group:grp_1", value)
	}
	group, ok := got.Groups["grp_1"]
	if !ok || group.Name != "One" || group.Notes != "keep" || !reflect.DeepEqual(group.Tags, []string{"g"}) || group.Color != "#ffffff" {
		t.Fatalf("recovered group metadata = %#v", got.Groups)
	}
	account, ok := got.Accounts["auth:member"]
	if !ok || account.Alias != "first" || account.GroupID != "grp_1" || !reflect.DeepEqual(account.Tags, []string{"team"}) {
		t.Fatalf("recovered account annotation = %#v", got.Accounts)
	}
}

func TestCompatibilityExportImportPreservesFullGroupModel(t *testing.T) {
	previousPath := defaultStatePath
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousPath })

	cfg := DefaultConfig()
	setActivePoolConfigValue(t, &cfg, "group:grp_1")
	source := NewPluginState(cfg)
	source.SetAnnotations(AnnotationState{
		Accounts: map[string]AccountAnnotation{
			"auth:member": {Alias: "A", GroupID: "grp_1", Tags: []string{"team"}},
		},
		Groups: map[string]GroupAnnotation{
			"grp_1": {Name: "One", Notes: "notes", Tags: []string{"g"}, Color: "#ffffff"},
		},
	})
	exported := handleExportState(source, time.Now())
	if exported.StatusCode != http.StatusOK {
		t.Fatalf("export status = %d body=%s", exported.StatusCode, exported.Body)
	}

	target := NewPluginState(DefaultConfig())
	imported := handleImportState(target, exported.Body, time.Now())
	if imported.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d body=%s", imported.StatusCode, imported.Body)
	}
	if value := activePoolConfigValue(t, target.Config()); value != "group:grp_1" {
		t.Fatalf("import active_pool = %q, want group:grp_1", value)
	}
	group, ok := target.Annotations().Groups["grp_1"]
	if !ok || group.Name != "One" || group.Notes != "notes" || !reflect.DeepEqual(group.Tags, []string{"g"}) || group.Color != "#ffffff" {
		t.Fatalf("import group metadata = %#v", target.Annotations().Groups)
	}
	account, ok := target.Annotations().Accounts["auth:member"]
	if !ok || account.Alias != "A" || account.GroupID != "grp_1" || !reflect.DeepEqual(account.Tags, []string{"team"}) {
		t.Fatalf("import account annotation = %#v", target.Annotations().Accounts)
	}
}

func TestCompatibilityUndefinedGroupReferenceIsMarkedWithoutBlockingAllPool(t *testing.T) {
	payload := BuildStatusPayload(StateSnapshot{
		Config: DefaultConfig(),
		Accounts: []AccountState{{
			AuthID:     "auth:member",
			Annotation: AccountAnnotation{Alias: "member", GroupID: "missing-group"},
		}},
		Annotations: AnnotationState{},
		Now:         time.Now(),
	}, nil)
	if len(payload.GroupReferenceWarnings) != 1 {
		t.Fatalf("group_reference_warnings = %#v, want one warning", payload.GroupReferenceWarnings)
	}
	warning := payload.GroupReferenceWarnings[0]
	if warning.Reason != groupReferenceNotFound {
		t.Fatalf("warning reason = %q, want %q", warning.Reason, groupReferenceNotFound)
	}
	if warning.ID != "missing-group" {
		t.Fatalf("warning group reference = %q, want missing-group", warning.ID)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONString(raw, "group_reference_warnings") || !containsJSONString(raw, "missing-group") {
		t.Fatalf("status JSON missing group reference warning: %s", raw)
	}
}

func containsJSONString(raw []byte, want string) bool {
	return strings.Contains(string(raw), want)
}
