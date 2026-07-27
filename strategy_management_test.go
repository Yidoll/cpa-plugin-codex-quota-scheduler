package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestStrategySettingsRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SelectionStrategy = SelectionStrategySubscriptionHigh
	cfg.SubscriptionOrder = []string{"free", "plus", "pro"}
	payload := SettingsFromConfig(cfg)
	if payload.SelectionStrategy != cfg.SelectionStrategy || !reflect.DeepEqual(payload.SubscriptionOrder, cfg.SubscriptionOrder) {
		t.Fatalf("settings payload = %#v", payload)
	}
	roundTrip, err := ConfigFromSettings(DefaultConfig(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.SelectionStrategy != cfg.SelectionStrategy || !reflect.DeepEqual(roundTrip.SubscriptionOrder, cfg.SubscriptionOrder) {
		t.Fatalf("round trip config = %#v", roundTrip)
	}
}

func TestStatusPayloadExposesStrategyDerivedValues(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	used := 30.0
	cfg := DefaultConfig()
	cfg.SelectionStrategy = SelectionStrategySubscriptionHigh
	cfg.SubscriptionOrder = []string{"free", "plus", "pro"}
	account := AccountState{
		AuthID: "auth-1", Instance: 1, Family: AccountFamilyWeekly, PlanType: "plus", SubscriptionExpiresAt: now.Add(24 * time.Hour), LastSuccessAt: now,
		Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &used, ResetAt: now.Add(48 * time.Hour)}},
	}
	state := StateSnapshot{Config: cfg, Accounts: []AccountState{account}, CPAAdmission: CPAAdmissionState{Observed: true, AuthIDs: map[string]struct{}{"auth-1": {}}}, Now: now}
	ordered := BuildOrderedAccounts(requestWithCandidates("auth-1"), state, now)
	payload := BuildStatusPayload(state, ordered)
	if payload.SelectionStrategy != SelectionStrategySubscriptionHigh || len(payload.Accounts) != 1 {
		t.Fatalf("status = %#v", payload)
	}
	status := payload.Accounts[0]
	if status.PlanType != "plus" || status.SubscriptionRank == nil || *status.SubscriptionRank != 1 || status.SubscriptionExpiresAt == nil || !status.SubscriptionExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("subscription status = %#v", status)
	}
	if status.BottleneckQuota != (QuotaScore{Known: true, Remaining: 70}) || !status.StrategyKnown || status.StrategyValue == "" {
		t.Fatalf("strategy status = %#v", status)
	}
}

func TestStrategyManagementPageContainsControlsAndBilingualCopy(t *testing.T) {
	html := string(RenderStatusHTML(BuildStatusShellPayload(time.Now())))
	for _, want := range []string{
		`id="selectionStrategy"`, `id="subscriptionOrder"`,
		`value="quota_high"`, `value="quota_low"`, `value="subscription_high"`, `value="subscription_low"`, `value="expiry_soon"`,
		"选择策略", "Selection strategy", "订阅顺序", "Subscription order", "strategy_value", "未知", "Unknown",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("management HTML missing %q", want)
		}
	}
}

func TestInvalidStrategySaveKeepsLastValidConfigAndSnapshot(t *testing.T) {
	valid := DefaultConfig()
	valid.SelectionStrategy = SelectionStrategyQuotaHigh
	store := NewPluginState(valid)
	publishSchedulerState(store, map[string]struct{}{}, time.Now())
	before := publishedSchedulerSnapshot.Load()

	payload := SettingsFromConfig(valid)
	payload.SelectionStrategy = SelectionStrategySubscriptionHigh
	payload.SubscriptionOrder = nil
	if _, err := ConfigFromSettings(store.Config(), payload); err == nil {
		t.Fatal("invalid strategy settings were accepted")
	}
	if got := store.Config().SelectionStrategy; got != SelectionStrategyQuotaHigh {
		t.Fatalf("store strategy = %q after invalid save", got)
	}
	if got := publishedSchedulerSnapshot.Load(); got != before || got.SelectionStrategy != SelectionStrategyQuotaHigh {
		t.Fatalf("published snapshot changed after invalid save: before=%p after=%p", before, got)
	}
}

func TestSuccessfulStrategySaveRepublishesSnapshotAndQueue(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	previousStatePath := defaultStatePath
	defaultStatePath = func() string { return filepath.Join(t.TempDir(), "state.json") }
	t.Cleanup(func() { defaultStatePath = previousStatePath })

	lowUsed, highUsed := 10.0, 80.0
	store := NewPluginState(DefaultConfig())
	store.UpsertQuota(AccountState{AuthID: "more", Instance: 1, Family: AccountFamilyWeekly, LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &lowUsed, ResetAt: now.Add(time.Hour)}}})
	store.UpsertQuota(AccountState{AuthID: "less", Instance: 2, Family: AccountFamilyWeekly, LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &highUsed, ResetAt: now.Add(time.Hour)}}})
	publishSchedulerState(store, map[string]struct{}{"more": {}, "less": {}}, now)

	payload := SettingsFromConfig(store.Config())
	payload.SelectionStrategy = SelectionStrategyQuotaLow
	resp := saveSettingsPayload(store, payload)
	if resp.StatusCode != 200 {
		t.Fatalf("save response = %#v", resp)
	}
	snapshot := publishedSchedulerSnapshot.Load()
	if snapshot == nil || snapshot.SelectionStrategy != SelectionStrategyQuotaLow {
		t.Fatalf("published snapshot = %#v", snapshot)
	}
	result := SelectAccount(*snapshot, []Candidate{{ID: "more", Provider: "codex"}, {ID: "less", Provider: "codex"}}, now)
	if result.AuthID != "less" {
		t.Fatalf("selected %q after strategy save, want less", result.AuthID)
	}
}

func TestStrategyDecisionLogContainsOnlyDerivedValues(t *testing.T) {
	store := NewPluginState(DefaultConfig())
	decision := PickDecision{AuthID: "auth-1", Handled: true, Reason: "selected", Strategy: SelectionStrategyQuotaHigh, StrategyKnown: true, StrategyValue: "70%"}
	logSchedulerDecision(store, pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5-codex"}, decision, time.Now())
	raw, err := json.Marshal(store.Snapshot(time.Now()).Logs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"selection_strategy":"quota_high"`, `"strategy_known":true`, `"strategy_value":"70%"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("strategy log missing %s: %s", want, text)
		}
	}
	for _, secret := range []string{"ACCESS_SECRET", "REFRESH_SECRET", "ID_TOKEN_SECRET", "Cookie:", "Authorization:"} {
		if strings.Contains(text, secret) {
			t.Fatalf("strategy log leaked %q: %s", secret, text)
		}
	}
}

func TestStrategyExportImportRoundTrip(t *testing.T) {
	previousStatePath := defaultStatePath
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousStatePath })

	cfg := DefaultConfig()
	cfg.SelectionStrategy = SelectionStrategySubscriptionLow
	cfg.SubscriptionOrder = []string{"free", "plus", "pro"}
	source := NewPluginState(cfg)
	exported := handleExportState(source, time.Now())
	if exported.StatusCode != 200 {
		t.Fatalf("export response = %#v", exported)
	}
	if !strings.Contains(string(exported.Body), `"selection_strategy":"subscription_low"`) || !strings.Contains(string(exported.Body), `"subscription_order":["free","plus","pro"]`) {
		t.Fatalf("export missing strategy fields: %s", exported.Body)
	}
	target := NewPluginState(DefaultConfig())
	imported := handleImportState(target, exported.Body, time.Now())
	if imported.StatusCode != 200 {
		t.Fatalf("import response = %#v body=%s", imported, imported.Body)
	}
	got := target.Config()
	if got.SelectionStrategy != cfg.SelectionStrategy || !reflect.DeepEqual(got.SubscriptionOrder, cfg.SubscriptionOrder) {
		t.Fatalf("imported config = %#v", got)
	}
}

func TestStrategyArtifactsDoNotLeakCredentialMaterial(t *testing.T) {
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	idToken := makeUnsignedJWT(t, map[string]any{"chatgpt_account_id": "acct", "chatgpt_plan_type": "PLUS", "chatgpt_subscription_active_until": expiresAt.Unix()})
	credentials, err := ExtractCodexCredentials(json.RawMessage(`{"access_token":"ACCESS_SECRET","refresh_token":"REFRESH_SECRET","id_token":"` + idToken + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	view := AccountView{ID: "auth-1", PlanType: credentials.PlanType, SubscriptionExpiresAt: credentials.SubscriptionExpiresAt}
	known, value := strategySortValue(view, SelectionPolicy{Strategy: SelectionStrategyQuotaHigh, Now: time.Now()})
	if known || value != "unknown" {
		t.Fatalf("unknown quota output = known:%v value:%q", known, value)
	}
	artifacts := map[string]any{
		"persistent": AccountSchedulingState{PlanType: credentials.PlanType, SubscriptionExpiresAt: credentials.SubscriptionExpiresAt},
		"status":     StatusAccount{AuthID: "auth-1", PlanType: credentials.PlanType, SubscriptionExpiresAt: &credentials.SubscriptionExpiresAt, StrategyKnown: known, StrategyValue: value},
		"log":        PickDecision{AuthID: "auth-1", Strategy: SelectionStrategyQuotaHigh, StrategyKnown: known, StrategyValue: value},
	}
	raw, err := json.Marshal(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"ACCESS_SECRET", "REFRESH_SECRET", idToken, "Cookie:", "Authorization:"} {
		if strings.Contains(text, secret) {
			t.Fatalf("strategy artifact leaked credential %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"strategy_value":"unknown"`) {
		t.Fatalf("unknown strategy value not explicit: %s", text)
	}
}
