package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExcludeFreeAccountsDefaultsTrueAndDecodesFalse(t *testing.T) {
	if !futureBoolField(t, DefaultConfig(), "ExcludeFreeAccounts") {
		t.Fatal("DefaultConfig ExcludeFreeAccounts = false, want true")
	}
	cfg, err := DecodeConfig([]byte("exclude_free_accounts: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if futureBoolField(t, cfg, "ExcludeFreeAccounts") {
		t.Fatal("decoded ExcludeFreeAccounts = true, want explicit false")
	}
}

func TestOldUserDataMissingExcludeFreeAccountsDefaultsTrue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".user-data.json")
	raw := []byte(`{"schema_version":1,"config":{}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	state, loaded, err := loadUserData(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded || !futureBoolField(t, state.Config, "ExcludeFreeAccounts") {
		t.Fatalf("loaded=%v config=%#v, want migrated default true", loaded, state.Config)
	}
}

func TestExcludeFreeAccountsLifecycleOverlayAndPersistence(t *testing.T) {
	t.Run("explicit lifecycle true overrides persisted false", func(t *testing.T) {
		persisted := DefaultConfig()
		setFutureBoolField(t, &persisted, "ExcludeFreeAccounts", false)
		store := withPersistedStrategyConfig(t, persisted)
		if err := configure(lifecyclePayload(t, "exclude_free_accounts: true\n")); err != nil {
			t.Fatal(err)
		}
		if !futureBoolField(t, store.Config(), "ExcludeFreeAccounts") {
			t.Fatal("explicit lifecycle true did not override persisted false")
		}
		disk, _, err := loadUserData(semanticStatePaths(defaultStatePath()).UserData)
		if err != nil {
			t.Fatal(err)
		}
		if futureBoolField(t, disk.Config, "ExcludeFreeAccounts") {
			t.Fatal("lifecycle overlay unexpectedly replaced saved Management false")
		}
	})

	t.Run("explicit lifecycle false overrides persisted true", func(t *testing.T) {
		persisted := DefaultConfig()
		store := withPersistedStrategyConfig(t, persisted)
		if err := configure(lifecyclePayload(t, "exclude_free_accounts: false\n")); err != nil {
			t.Fatal(err)
		}
		if futureBoolField(t, store.Config(), "ExcludeFreeAccounts") {
			t.Fatal("explicit lifecycle false did not override persisted true")
		}
	})

	t.Run("missing lifecycle field restores persisted false", func(t *testing.T) {
		persisted := DefaultConfig()
		setFutureBoolField(t, &persisted, "ExcludeFreeAccounts", false)
		store := withPersistedStrategyConfig(t, persisted)
		if err := configure(lifecyclePayload(t, "quota_refresh_interval: 1h\n")); err != nil {
			t.Fatal(err)
		}
		if futureBoolField(t, store.Config(), "ExcludeFreeAccounts") {
			t.Fatal("missing lifecycle field did not restore persisted false")
		}
	})
}

func TestExcludeFreeAccountsManagementAndExportRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	setFutureBoolField(t, &cfg, "ExcludeFreeAccounts", false)
	payload := SettingsFromConfig(cfg)
	if futureBoolField(t, payload, "ExcludeFreeAccounts") {
		t.Fatal("settings payload lost explicit false")
	}
	roundTrip, err := ConfigFromSettings(DefaultConfig(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if futureBoolField(t, roundTrip, "ExcludeFreeAccounts") {
		t.Fatal("settings round trip lost explicit false")
	}

	previousPath := defaultStatePath
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousPath })
	source := NewPluginState(cfg)
	exported := handleExportState(source, time.Now())
	if exported.StatusCode != 200 || !strings.Contains(string(exported.Body), `"exclude_free_accounts":false`) {
		t.Fatalf("export response = %#v body=%s", exported, exported.Body)
	}
	target := NewPluginState(DefaultConfig())
	imported := handleImportState(target, exported.Body, time.Now())
	if imported.StatusCode != 200 {
		t.Fatalf("import response = %#v body=%s", imported, imported.Body)
	}
	if futureBoolField(t, target.Config(), "ExcludeFreeAccounts") {
		t.Fatal("import lost explicit false")
	}
}

func TestExcludeFreeAccountsOmittedManagementFieldPreservesBase(t *testing.T) {
	raw, err := json.Marshal(SettingsFromConfig(DefaultConfig()))
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "exclude_free_accounts")
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var payload SettingsPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigFromSettings(DefaultConfig(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ExcludeFreeAccounts {
		t.Fatal("omitted Management field disabled exclude_free_accounts")
	}
}

func TestExcludeFreeAccountsManagementRejectsNull(t *testing.T) {
	var payload SettingsPayload
	if err := json.Unmarshal([]byte(`{"exclude_free_accounts":null}`), &payload); err == nil {
		t.Fatal("exclude_free_accounts null was accepted, want boolean validation error")
	}
}

func TestExcludeFreeAccountsPersistenceFailureKeepsConfig(t *testing.T) {
	previousPath := defaultStatePath
	invalidPath := filepath.Join(t.TempDir()+string(rune(0)), "state.json")
	defaultStatePath = func() string { return invalidPath }
	t.Cleanup(func() { defaultStatePath = previousPath })

	store := NewPluginState(DefaultConfig())
	payload := SettingsFromConfig(store.Config())
	setFutureBoolField(t, &payload, "ExcludeFreeAccounts", false)
	response := saveSettingsPayload(store, payload)
	if response.StatusCode < 500 {
		t.Fatalf("response = %#v, want persistence failure", response)
	}
	if !futureBoolField(t, store.Config(), "ExcludeFreeAccounts") {
		t.Fatal("persistence failure changed runtime config")
	}
}

func TestExcludeFreeAccountsManagementUIIsBilingual(t *testing.T) {
	html := string(RenderStatusHTML(BuildStatusShellPayload(time.Now())))
	for _, want := range []string{
		`id="excludeFreeAccounts"`,
		"默认排除 Free 账号",
		"Exclude free accounts by default",
		"未知计划不由插件主动选择",
		"Unknown plans are not actively selected",
		"全部合格请求候选均为已知 Free 时，本次放宽过滤并正常主动选择",
		"When all eligible request candidates are known Free, relax the filter and actively select as usual",
		"exclude_free_accounts:document.getElementById('excludeFreeAccounts').checked",
		"document.getElementById('excludeFreeAccounts').checked=s.exclude_free_accounts!==false",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("Management UI missing %q", want)
		}
	}
}

func futureBoolField(t *testing.T, target any, name string) bool {
	t.Helper()
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{name, "exclude_free_accounts"} {
		if value, ok := object[key]; ok {
			boolean, valid := value.(bool)
			if !valid {
				t.Fatalf("%s field %q is not a bool: %#v", name, key, value)
			}
			return boolean
		}
	}
	t.Fatalf("production type is missing required field %s", name)
	return false
}
