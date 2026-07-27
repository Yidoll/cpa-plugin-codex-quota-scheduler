package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractCodexCredentialsSubscriptionClaims(t *testing.T) {
	wantRFC3339 := time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name       string
		claims     map[string]any
		wantPlan   string
		wantExpiry time.Time
	}{
		{
			name: "namespaced RFC3339",
			claims: map[string]any{"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "acct", "chatgpt_plan_type": " PLUS ", "chatgpt_subscription_active_until": "2026-08-01T12:30:00Z",
			}},
			wantPlan: "plus", wantExpiry: wantRFC3339,
		},
		{
			name:     "compatible top level Unix number",
			claims:   map[string]any{"chatgpt_account_id": "acct", "plan_type": "TEAM", "subscription_active_until": float64(wantRFC3339.Unix())},
			wantPlan: "team", wantExpiry: wantRFC3339,
		},
		{
			name: "fully namespaced compatible claims",
			claims: map[string]any{
				"chatgpt_account_id":                                            "acct",
				"https://api.openai.com/auth.chatgpt_plan_type":                 "Enterprise",
				"https://api.openai.com/auth.chatgpt_subscription_active_until": wantRFC3339.Unix(),
			},
			wantPlan: "enterprise", wantExpiry: wantRFC3339,
		},
		{
			name:     "Unix string",
			claims:   map[string]any{"chatgpt_account_id": "acct", "chatgpt_plan_type": "Pro", "chatgpt_subscription_active_until": "1785587400"},
			wantPlan: "pro", wantExpiry: time.Unix(1785587400, 0).UTC(),
		},
		{
			name:     "invalid expiry remains unknown",
			claims:   map[string]any{"chatgpt_account_id": "acct", "chatgpt_plan_type": "Free", "chatgpt_subscription_active_until": "not-a-time"},
			wantPlan: "free",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := makeUnsignedJWT(t, tt.claims)
			credentials, err := ExtractCodexCredentials(json.RawMessage(`{"access_token":"access","id_token":"` + token + `"}`))
			if err != nil {
				t.Fatal(err)
			}
			if credentials.PlanType != tt.wantPlan {
				t.Fatalf("PlanType = %q, want %q", credentials.PlanType, tt.wantPlan)
			}
			if !credentials.SubscriptionExpiresAt.Equal(tt.wantExpiry) {
				t.Fatalf("SubscriptionExpiresAt = %s, want %s", credentials.SubscriptionExpiresAt, tt.wantExpiry)
			}
		})
	}
}

func TestEffectivePlanTypePrefersQuotaResponse(t *testing.T) {
	tests := []struct {
		quotaPlan, identityPlan, want string
	}{
		{quotaPlan: " TEAM ", identityPlan: "plus", want: "team"},
		{identityPlan: " PLUS ", want: "plus"},
		{},
	}
	for _, tt := range tests {
		if got := effectivePlanType(tt.quotaPlan, tt.identityPlan); got != tt.want {
			t.Fatalf("effectivePlanType(%q, %q) = %q, want %q", tt.quotaPlan, tt.identityPlan, got, tt.want)
		}
	}
}

func TestSubscriptionFieldsSurviveStateCloneSnapshotAndPersistence(t *testing.T) {
	expiresAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	account := AccountState{AuthID: "auth-1", PlanType: "plus", SubscriptionExpiresAt: expiresAt, LastSuccessAt: time.Now()}
	cloned := cloneAccountState(account)
	if cloned.PlanType != "plus" || !cloned.SubscriptionExpiresAt.Equal(expiresAt) {
		t.Fatalf("cloned account = %#v", cloned)
	}

	snapshot := schedulerSnapshotFromState(StateSnapshot{Config: DefaultConfig(), Accounts: []AccountState{account}, Now: time.Now()}, nil)
	if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].PlanType != "plus" || !snapshot.Accounts[0].SubscriptionExpiresAt.Equal(expiresAt) {
		t.Fatalf("scheduler snapshot = %#v", snapshot.Accounts)
	}

	persisted := NewPersistentState()
	persisted.SchedulingAccounts["auth-1"] = AccountSchedulingState{PlanType: "plus", SubscriptionExpiresAt: expiresAt}
	clonedPersistent := clonePersistentState(persisted)
	if got := clonedPersistent.SchedulingAccounts["auth-1"]; got.PlanType != "plus" || !got.SubscriptionExpiresAt.Equal(expiresAt) {
		t.Fatalf("persisted scheduling state = %#v", got)
	}
}

func TestOldPersistentStateWithoutSubscriptionFieldsIsCompatible(t *testing.T) {
	var state PersistentState
	if err := json.Unmarshal([]byte(`{"schema_version":1}`), &state); err != nil {
		t.Fatal(err)
	}
	cloned := clonePersistentState(state)
	if cloned.SchedulingAccounts == nil || len(cloned.SchedulingAccounts) != 0 {
		t.Fatalf("SchedulingAccounts = %#v, want empty initialized map", cloned.SchedulingAccounts)
	}
}

func TestExpirySoonTreatsPastExpiryAsUnknown(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	if _, known := futureSubscriptionExpiry(now.Add(-time.Second), now); known {
		t.Fatal("past subscription expiry was treated as known")
	}
	if got, known := futureSubscriptionExpiry(now.Add(time.Hour), now); !known || !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("future expiry = %s, known=%v", got, known)
	}
}

func TestPersistedSchedulingFieldsFillExistingAccountGaps(t *testing.T) {
	expiresAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	runtimeStore := NewStateStore(filepath.Join(t.TempDir(), "runtime.json"), OSFileHooks(), nil)
	if _, err := runtimeStore.Update(func(state *PersistentState) error {
		state.SchedulingAccounts["auth-1"] = AccountSchedulingState{PlanType: "plus", SubscriptionExpiresAt: expiresAt, BottleneckQuota: QuotaScore{Known: true, Remaining: 42}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pluginState := NewPluginState(DefaultConfig())
	pluginState.UpsertQuota(AccountState{AuthID: "auth-1"})
	refresher := &QuotaRefresher{state: pluginState, runtimeStore: runtimeStore, now: time.Now}
	got := refresher.mergeExistingAccount(AccountState{AuthID: "auth-1"})
	if got.PlanType != "plus" || !got.SubscriptionExpiresAt.Equal(expiresAt) || got.BottleneckQuota != (QuotaScore{Known: true, Remaining: 42}) {
		t.Fatalf("merged account = %#v", got)
	}
}
