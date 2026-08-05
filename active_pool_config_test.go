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

func TestDecodeConfigSupportsOnlyCanonicalActivePools(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "all", raw: "active_pool: all\n", want: "all"},
		{name: "ungrouped", raw: "active_pool: ungrouped\n", want: "ungrouped"},
		{name: "stable group ID", raw: "active_pool: group:team-alpha\n", want: "group:team-alpha"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := DecodeConfig([]byte(tt.raw))
			if err != nil {
				t.Fatalf("DecodeConfig(%q): %v", tt.raw, err)
			}
			if got := activePoolConfigValue(t, cfg); got != tt.want {
				t.Fatalf("active_pool = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOldUserDataMissingActivePoolDefaultsToAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".user-data.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"config":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	state, loaded, err := loadUserData(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded {
		t.Fatal("old user data was not loaded")
	}
	if got := activePoolConfigValue(t, state.Config); got != "all" {
		t.Fatalf("active_pool = %q, want legacy default all", got)
	}
}

func TestDecodeConfigRejectsNonCanonicalActivePoolValues(t *testing.T) {
	for _, raw := range []string{
		"active_pool: every_group\n",
		"active_pool: \" all \"\n",
		"active_pool: \"group:\"\n",
		"active_pool: \"group: team-alpha\"\n",
		"active_pool: \"group:team-alpha \"\n",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := DecodeConfig([]byte(raw)); err == nil {
				t.Fatalf("DecodeConfig(%q) accepted a non-canonical active_pool", raw)
			}
		})
	}
}

func TestSettingsPayloadOmitsLegacyActivePoolAndRejectsWrite(t *testing.T) {
	cfg := DefaultConfig()
	setActivePoolConfigValue(t, &cfg, "group:team-alpha")

	payload := SettingsFromConfig(cfg)
	if got := activePoolPayloadValue(t, payload); got != "" {
		t.Fatalf("settings active_pool = %q, want omitted legacy field", got)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"active_pool"`) {
		t.Fatalf("settings JSON exposes legacy active_pool: %s", raw)
	}

	payload.ActivePool = "group:team-alpha"
	if _, err := ConfigFromSettings(DefaultConfig(), payload); err == nil || err.Error() != pluginGroupManagementRemoved {
		t.Fatalf("legacy active_pool write error = %v, want %q", err, pluginGroupManagementRemoved)
	}
}

func TestConfigureAcceptsUnknownLegacyActivePoolWithoutSchedulingSemantics(t *testing.T) {
	withPersistedUserData(t, DefaultConfig(), map[string]GroupAnnotation{
		"team-alpha": {Name: "Team Alpha"},
	})

	if err := configure(lifecyclePayload(t, "active_pool: group:team-missing\n")); err != nil {
		t.Fatalf("configure rejected legacy active_pool: %v", err)
	}
	if got := activePoolConfigValue(t, globalState.Config()); got != "group:team-missing" {
		t.Fatalf("legacy active_pool = %q, want preserved value", got)
	}
}

func TestConfigureActivePoolLifecycleOverlayIsPresenceAware(t *testing.T) {
	t.Run("explicit lifecycle value overrides persisted value without rewriting user data", func(t *testing.T) {
		store := withPersistedActivePoolConfig(t, "group:team-alpha", map[string]GroupAnnotation{
			"team-alpha": {Name: "Team Alpha"},
		})

		if err := configure(lifecyclePayload(t, "active_pool: ungrouped\n")); err != nil {
			t.Fatalf("configure: %v", err)
		}
		if got := activePoolConfigValue(t, store.Config()); got != "ungrouped" {
			t.Fatalf("runtime active_pool = %q, want explicit lifecycle value ungrouped", got)
		}
		disk, loaded, err := loadUserData(semanticStatePaths(defaultStatePath()).UserData)
		if err != nil || !loaded {
			t.Fatalf("load persisted user data: loaded=%v err=%v", loaded, err)
		}
		if got := activePoolConfigValue(t, disk.Config); got != "group:team-alpha" {
			t.Fatalf("lifecycle overlay rewrote persisted active_pool to %q, want group:team-alpha", got)
		}
	})

	t.Run("missing lifecycle field preserves persisted value", func(t *testing.T) {
		store := withPersistedActivePoolConfig(t, "group:team-alpha", map[string]GroupAnnotation{
			"team-alpha": {Name: "Team Alpha"},
		})

		if err := configure(lifecyclePayload(t, "quota_refresh_interval: 1h\n")); err != nil {
			t.Fatalf("configure: %v", err)
		}
		if got := activePoolConfigValue(t, store.Config()); got != "group:team-alpha" {
			t.Fatalf("runtime active_pool = %q, want persisted group:team-alpha", got)
		}
	})
}

func withPersistedActivePoolConfig(t *testing.T, activePool string, groups map[string]GroupAnnotation) *PluginState {
	t.Helper()
	persisted := DefaultConfig()
	setActivePoolConfigValue(t, &persisted, activePool)
	return withPersistedUserData(t, persisted, groups)
}

func withPersistedUserData(t *testing.T, persisted Config, groups map[string]GroupAnnotation) *PluginState {
	t.Helper()
	dir := t.TempDir()
	previousPath := defaultStatePath
	previousState := globalState
	previousSnapshot := publishedSchedulerSnapshot.Load()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	globalState = NewPluginState(DefaultConfig())
	t.Cleanup(func() {
		defaultStatePath = previousPath
		globalState = previousState
		publishedSchedulerSnapshot.Store(previousSnapshot)
	})

	if err := SaveUserData(semanticStatePaths(defaultStatePath()).UserData, PluginDiskState{Config: persisted, Groups: groups}); err != nil {
		t.Fatalf("save persisted config: %v", err)
	}
	return globalState
}

func activePoolConfigValue(t *testing.T, cfg Config) string {
	t.Helper()
	field := reflect.ValueOf(cfg).FieldByName("ActivePool")
	if !field.IsValid() {
		t.Fatal("production type is missing required field ActivePool")
	}
	if field.Kind() != reflect.String {
		t.Fatalf("production field ActivePool kind = %s, want string", field.Kind())
	}
	return field.String()
}

func setActivePoolConfigValue(t *testing.T, cfg *Config, value string) {
	t.Helper()
	field := reflect.ValueOf(cfg).Elem().FieldByName("ActivePool")
	if !field.IsValid() {
		t.Fatal("production type is missing required field ActivePool")
	}
	if field.Kind() != reflect.String || !field.CanSet() {
		t.Fatalf("production field ActivePool is not a settable string")
	}
	field.SetString(value)
}

func activePoolPayloadValue(t *testing.T, payload SettingsPayload) string {
	t.Helper()
	field := reflect.ValueOf(payload).FieldByName("ActivePool")
	if !field.IsValid() {
		t.Fatal("production type is missing required field ActivePool")
	}
	if field.Kind() != reflect.String {
		t.Fatalf("production field ActivePool kind = %s, want string", field.Kind())
	}
	return field.String()
}

func TestSaveSettingsRejectsLegacyActivePoolWriteWithFixedError(t *testing.T) {
	store := withPersistedActivePoolConfig(t, "all", map[string]GroupAnnotation{
		"team-alpha": {Name: "Team Alpha"},
	})
	store.SetAnnotations(AnnotationState{Groups: map[string]GroupAnnotation{
		"team-alpha": {Name: "Team Alpha"},
	}})
	payload := SettingsFromConfig(store.Config())
	setActivePoolPayloadValue(t, &payload, "group:team-missing")
	response := saveSettingsPayload(store, payload)
	if response.StatusCode != http.StatusGone {
		t.Fatalf("save settings status = %d body=%s, want 410", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), pluginGroupManagementRemoved) {
		t.Fatalf("save settings body = %s, want %s", response.Body, pluginGroupManagementRemoved)
	}
	if got := activePoolConfigValue(t, store.Config()); got != "all" {
		t.Fatalf("runtime active_pool changed to %q after rejected save", got)
	}
}

func TestImportPreservesUnknownLegacyActivePool(t *testing.T) {
	previousPath := defaultStatePath
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousPath })
	target := NewPluginState(DefaultConfig())
	cfg := DefaultConfig()
	setActivePoolConfigValue(t, &cfg, "group:team-missing")
	body, err := json.Marshal(PluginDiskState{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	response := handleImportState(target, body, time.Now())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d body=%s, want 200", response.StatusCode, response.Body)
	}
	if got := activePoolConfigValue(t, target.Config()); got != "group:team-missing" {
		t.Fatalf("import active_pool = %q, want preserved legacy value", got)
	}
}

func TestExportImportPreservesActivePool(t *testing.T) {
	previousPath := defaultStatePath
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousPath })
	cfg := DefaultConfig()
	setActivePoolConfigValue(t, &cfg, "group:team-alpha")
	source := NewPluginState(cfg)
	source.SetAnnotations(AnnotationState{Groups: map[string]GroupAnnotation{
		"team-alpha": {Name: "Team Alpha"},
	}})
	exported := handleExportState(source, time.Now())
	if exported.StatusCode != 200 || !strings.Contains(string(exported.Body), `"active_pool":"group:team-alpha"`) {
		t.Fatalf("export missing active_pool: %s", exported.Body)
	}
	target := NewPluginState(DefaultConfig())
	imported := handleImportState(target, exported.Body, time.Now())
	if imported.StatusCode != 200 {
		t.Fatalf("import response = %#v body=%s", imported, imported.Body)
	}
	if got := activePoolConfigValue(t, target.Config()); got != "group:team-alpha" {
		t.Fatalf("import lost active_pool = %q", got)
	}
}

func TestStatusOmitsLegacyActivePoolFromSettings(t *testing.T) {
	cfg := DefaultConfig()
	setActivePoolConfigValue(t, &cfg, "ungrouped")
	store := NewPluginState(cfg)
	status := BuildStatusPayload(store.Snapshot(time.Now()), nil)
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"active_pool"`) {
		t.Fatalf("status exposes legacy active_pool: %s", raw)
	}
}

func setActivePoolPayloadValue(t *testing.T, payload *SettingsPayload, value string) {
	t.Helper()
	field := reflect.ValueOf(payload).Elem().FieldByName("ActivePool")
	if !field.IsValid() {
		t.Fatal("production type is missing required field ActivePool")
	}
	if field.Kind() != reflect.String || !field.CanSet() {
		t.Fatalf("production field ActivePool is not a settable string")
	}
	field.SetString(value)
}
