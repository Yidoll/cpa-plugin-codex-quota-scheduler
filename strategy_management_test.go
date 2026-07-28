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

func TestStatusPayloadDisplaysLegacyStrategyExplicitly(t *testing.T) {
	payload := BuildStatusPayload(StateSnapshot{Config: DefaultConfig(), Now: time.Now()}, nil)
	if payload.SelectionStrategy != "" || payload.SelectionStrategyDisplay != "legacy" {
		t.Fatalf("legacy status = strategy:%q display:%q", payload.SelectionStrategy, payload.SelectionStrategyDisplay)
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
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousStatePath })

	lowUsed, highUsed := 10.0, 80.0
	store := NewPluginState(DefaultConfig())
	store.UpsertQuota(AccountState{AuthID: "more", Instance: 1, Family: AccountFamilyWeekly, PlanType: "plus", LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &lowUsed, ResetAt: now.Add(time.Hour)}}})
	store.UpsertQuota(AccountState{AuthID: "less", Instance: 2, Family: AccountFamilyWeekly, PlanType: "plus", LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &highUsed, ResetAt: now.Add(time.Hour)}}})
	store.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, AuthIDs: map[string]struct{}{"more": {}, "less": {}}})
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
	status := BuildStatusPayload(store.Snapshot(now), nil)
	if status.SelectionStrategy != SelectionStrategyQuotaLow || status.Settings.SelectionStrategy != SelectionStrategyQuotaLow {
		t.Fatalf("status strategy = %q/%q, want quota_low", status.SelectionStrategy, status.Settings.SelectionStrategy)
	}
	exported := handleExportState(store, now)
	if exported.StatusCode != 200 || !strings.Contains(string(exported.Body), `"selection_strategy":"quota_low"`) {
		t.Fatalf("exported strategy missing after save: status=%d body=%s", exported.StatusCode, exported.Body)
	}
}

func TestFailedStrategyPersistenceKeepsConfigAndSnapshot(t *testing.T) {
	previousPath := defaultStatePath
	invalidPath := filepath.Join(t.TempDir()+string(rune(0)), "state.json")
	previousSnapshot := publishedSchedulerSnapshot.Load()
	defaultStatePath = func() string { return invalidPath }
	t.Cleanup(func() {
		defaultStatePath = previousPath
		publishedSchedulerSnapshot.Store(previousSnapshot)
	})

	valid := DefaultConfig()
	valid.SelectionStrategy = SelectionStrategyQuotaHigh
	store := NewPluginState(valid)
	publishSchedulerState(store, map[string]struct{}{}, time.Now())
	beforeSnapshot := publishedSchedulerSnapshot.Load()

	payload := SettingsFromConfig(valid)
	payload.SelectionStrategy = SelectionStrategyQuotaLow
	resp := saveSettingsPayload(store, payload)
	if resp.StatusCode != 500 {
		t.Fatalf("save status = %d, want 500; body=%s", resp.StatusCode, resp.Body)
	}
	if got := store.Config().SelectionStrategy; got != SelectionStrategyQuotaHigh {
		t.Fatalf("runtime strategy = %q after failed persistence", got)
	}
	if got := publishedSchedulerSnapshot.Load(); got != beforeSnapshot || got.SelectionStrategy != SelectionStrategyQuotaHigh {
		t.Fatalf("snapshot changed after failed persistence: before=%p after=%p", beforeSnapshot, got)
	}
}

func TestStrategyCommitHidesPartialStateFromManagementAndPick(t *testing.T) {
	previousPath := defaultStatePath
	previousSnapshot := publishedSchedulerSnapshot.Load()
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() {
		defaultStatePath = previousPath
		publishedSchedulerSnapshot.Store(previousSnapshot)
	})

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	lowUsed, highUsed := 10.0, 80.0
	store := NewPluginState(DefaultConfig())
	store.UpsertQuota(AccountState{AuthID: "more", Instance: 1, Family: AccountFamilyWeekly, PlanType: "plus", LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &lowUsed, ResetAt: now.Add(time.Hour)}}})
	store.UpsertQuota(AccountState{AuthID: "less", Instance: 2, Family: AccountFamilyWeekly, PlanType: "plus", LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &highUsed, ResetAt: now.Add(time.Hour)}}})
	store.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, AuthIDs: map[string]struct{}{"more": {}, "less": {}}})
	publishSchedulerState(store, map[string]struct{}{"more": {}, "less": {}}, now)

	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "more", Provider: "codex"}, {ID: "less", Provider: "codex"}}}
	initial := SettingsFromConfig(store.Config())
	initial.SelectionStrategy = SelectionStrategyQuotaHigh
	if resp := saveSettingsPayload(store, initial); resp.StatusCode != 200 {
		t.Fatalf("initial save status = %d; body=%s", resp.StatusCode, resp.Body)
	}
	if got := schedulerPickPublished(req, now).AuthID; got != "more" {
		t.Fatalf("initial quota_high pick = %q, want more", got)
	}

	schedulerStatePublicationMu.Lock()
	publicationLocked := true
	t.Cleanup(func() {
		if publicationLocked {
			schedulerStatePublicationMu.Unlock()
		}
	})
	saveDone := make(chan pluginapi.ManagementResponse, 1)
	go func() {
		payload := SettingsFromConfig(store.Config())
		payload.SelectionStrategy = SelectionStrategyQuotaLow
		saveDone <- saveSettingsPayload(store, payload)
	}()
	if !waitForCondition(t, time.Second, func() bool { return store.Config().SelectionStrategy == SelectionStrategyQuotaLow }) {
		t.Fatal("strategy update did not reach state before blocked publication")
	}
	if got := schedulerPickPublished(req, now).AuthID; got != "more" {
		t.Fatalf("pick during commit = %q, want old quota_high result more", got)
	}

	settingsDone := make(chan pluginapi.ManagementResponse, 1)
	go func() {
		settingsDone <- HandleManagementRequest(store, pluginapi.ManagementRequest{Method: "GET", Path: "/plugins/codex-quota-scheduler/settings"}, now)
	}()
	select {
	case response := <-settingsDone:
		t.Fatalf("management read escaped partial commit: status=%d body=%s", response.StatusCode, response.Body)
	case <-time.After(50 * time.Millisecond):
	}

	schedulerStatePublicationMu.Unlock()
	publicationLocked = false
	select {
	case response := <-saveDone:
		if response.StatusCode != 200 {
			t.Fatalf("save status = %d; body=%s", response.StatusCode, response.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("strategy save did not finish after publication resumed")
	}
	var settings pluginapi.ManagementResponse
	select {
	case settings = <-settingsDone:
	case <-time.After(time.Second):
		t.Fatal("management settings read did not finish after commit")
	}
	if !strings.Contains(string(settings.Body), `"selection_strategy":"quota_low"`) {
		t.Fatalf("settings did not publish quota_low: %s", settings.Body)
	}
	if got := schedulerPickPublished(req, now).AuthID; got != "less" {
		t.Fatalf("pick after commit = %q, want quota_low result less", got)
	}
}

func TestAnnotationPersistenceSharesConfigCommitBoundary(t *testing.T) {
	previousPath := defaultStatePath
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = previousPath })

	cfg := DefaultConfig()
	cfg.SelectionStrategy = SelectionStrategyQuotaLow
	store := NewPluginState(cfg)
	if err := SaveUserData(semanticStatePaths(defaultStatePath()).UserData, diskStateFromStore(store)); err != nil {
		t.Fatalf("save initial user data: %v", err)
	}

	configCommitMu.Lock()
	commitLocked := true
	t.Cleanup(func() {
		if commitLocked {
			configCommitMu.Unlock()
		}
	})
	patchDone := make(chan pluginapi.ManagementResponse, 1)
	go func() {
		alias := "Account A"
		patchDone <- applyAccountAnnotationPatch(store, annotationPatch{AuthID: "auth-a", Alias: &alias})
	}()
	select {
	case response := <-patchDone:
		t.Fatalf("annotation write escaped config commit boundary: status=%d body=%s", response.StatusCode, response.Body)
	case <-time.After(50 * time.Millisecond):
	}
	configCommitMu.Unlock()
	commitLocked = false

	select {
	case response := <-patchDone:
		if response.StatusCode != 200 {
			t.Fatalf("annotation save status = %d; body=%s", response.StatusCode, response.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("annotation write did not finish after commit boundary opened")
	}
	disk, loaded, err := loadUserData(semanticStatePaths(defaultStatePath()).UserData)
	if err != nil || !loaded {
		t.Fatalf("load user data: loaded=%v err=%v", loaded, err)
	}
	if disk.Config.SelectionStrategy != SelectionStrategyQuotaLow || disk.Accounts["auth:auth-a"].Alias != "Account A" {
		t.Fatalf("persisted state lost config or annotation: %#v", disk)
	}
}

func TestRepublishSchedulerConfigUsesStateAdmission(t *testing.T) {
	previousSnapshot := publishedSchedulerSnapshot.Load()
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previousSnapshot) })

	store := NewPluginState(DefaultConfig())
	store.ReplaceCPAAdmission(CPAAdmissionState{Observed: true, Priority: 9, AuthIDs: map[string]struct{}{"new": {}}})
	PublishSchedulerSnapshot(&SchedulerSnapshot{
		HandleEnabled:     true,
		ActiveHighestTier: map[string]struct{}{"old": {}},
		AdmissionVersion:  1,
	})

	republishSchedulerConfig(store, time.Now())
	snapshot := publishedSchedulerSnapshot.Load()
	if snapshot == nil {
		t.Fatal("scheduler snapshot missing")
	}
	if _, ok := snapshot.ActiveHighestTier["new"]; !ok || len(snapshot.ActiveHighestTier) != 1 {
		t.Fatalf("active highest tier = %#v, want only new", snapshot.ActiveHighestTier)
	}
	if snapshot.AdmissionVersion != store.Snapshot(time.Now()).CPAAdmissionVersion {
		t.Fatalf("admission version = %d, want state version", snapshot.AdmissionVersion)
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
