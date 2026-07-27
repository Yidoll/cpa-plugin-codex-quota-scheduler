package main

import (
	"reflect"
	"testing"
	"time"
)

func TestSelectionStrategies(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	base := func(id string) AccountView {
		return AccountView{ID: id, Instance: AuthInstanceID(len(id)), Cache: CacheFresh}
	}
	withScore := func(id string, remaining float64) AccountView {
		account := base(id)
		account.QuotaScore = QuotaScore{Known: true, Remaining: remaining}
		return account
	}
	withPlan := func(id, plan string) AccountView {
		account := base(id)
		account.PlanType = plan
		return account
	}
	withExpiry := func(id string, expiresAt time.Time) AccountView {
		account := base(id)
		account.SubscriptionExpiresAt = expiresAt
		return account
	}

	tests := []struct {
		name     string
		strategy SelectionStrategy
		order    []string
		accounts []AccountView
		want     []string
	}{
		{name: "quota high", strategy: SelectionStrategyQuotaHigh, accounts: []AccountView{base("unknown"), withScore("low", 10), withScore("high", 90)}, want: []string{"high", "low", "unknown"}},
		{name: "quota low", strategy: SelectionStrategyQuotaLow, accounts: []AccountView{base("unknown"), withScore("high", 90), withScore("low", 10)}, want: []string{"low", "high", "unknown"}},
		{name: "subscription high", strategy: SelectionStrategySubscriptionHigh, order: []string{"free", "plus", "pro"}, accounts: []AccountView{withPlan("free", "free"), withPlan("unknown", "team"), withPlan("pro", "pro")}, want: []string{"pro", "free", "unknown"}},
		{name: "subscription low", strategy: SelectionStrategySubscriptionLow, order: []string{"free", "plus", "pro"}, accounts: []AccountView{withPlan("pro", "pro"), withPlan("unknown", "team"), withPlan("free", "free")}, want: []string{"free", "pro", "unknown"}},
		{name: "expiry soon", strategy: SelectionStrategyExpirySoon, accounts: []AccountView{withExpiry("unknown", time.Time{}), withExpiry("past", now.Add(-time.Hour)), withExpiry("later", now.Add(48*time.Hour)), withExpiry("soon", now.Add(time.Hour))}, want: []string{"soon", "later", "past", "unknown"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			active := make(map[string]struct{}, len(tt.accounts))
			candidates := make([]Candidate, 0, len(tt.accounts))
			for i := len(tt.accounts) - 1; i >= 0; i-- {
				active[tt.accounts[i].ID] = struct{}{}
				candidates = append(candidates, Candidate{ID: tt.accounts[i].ID, Provider: "codex"})
			}
			snapshot := SchedulerSnapshot{SelectionStrategy: tt.strategy, SubscriptionRanks: subscriptionRanks(tt.order), Accounts: tt.accounts, ActiveHighestTier: active}
			got := SelectAccount(snapshot, candidates, now)
			ids := make([]string, 0, len(got.Ordered))
			for _, account := range got.Ordered {
				ids = append(ids, account.ID)
			}
			if !reflect.DeepEqual(ids, tt.want) {
				t.Fatalf("ordered IDs = %#v, want %#v", ids, tt.want)
			}
		})
	}
}

func TestSelectionLayersOverrideStrategyAndTieByAuthID(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	accounts := []AccountView{
		{ID: "opportunistic", Cache: CacheUnknown, PluginPriority: 99, QuotaScore: QuotaScore{Known: true, Remaining: 100}},
		{ID: "priority", Cache: CacheFresh, PluginPriority: 10, QuotaScore: QuotaScore{Known: true, Remaining: 1}},
		{ID: "b", Cache: CacheFresh, QuotaScore: QuotaScore{Known: true, Remaining: 100}},
		{ID: "a", Cache: CacheFresh, QuotaScore: QuotaScore{Known: true, Remaining: 100}},
	}
	active := map[string]struct{}{}
	candidates := make([]Candidate, 0, len(accounts))
	for _, account := range accounts {
		active[account.ID] = struct{}{}
		candidates = append(candidates, Candidate{ID: account.ID, Provider: "codex"})
	}
	snapshot := SchedulerSnapshot{SelectionStrategy: SelectionStrategyQuotaHigh, Accounts: accounts, ActiveHighestTier: active}
	got := SelectAccount(snapshot, candidates, now)
	if got.AuthID != "priority" {
		t.Fatalf("selected %q, want plugin-priority account", got.AuthID)
	}
	if len(got.Ordered) != 3 || got.Ordered[1].ID != "a" || got.Ordered[2].ID != "b" {
		t.Fatalf("preferred order = %#v, want priority,a,b", got.Ordered)
	}
}

func TestProductionAndManagementQueueShareStrategyOrder(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	usedLow, usedHigh := 10.0, 80.0
	cfg := DefaultConfig()
	cfg.SelectionStrategy = SelectionStrategyQuotaLow
	accounts := []AccountState{
		{AuthID: "more", Instance: 1, Family: AccountFamilyWeekly, LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &usedLow, ResetAt: now.Add(time.Hour)}}},
		{AuthID: "less", Instance: 2, Family: AccountFamilyWeekly, LastSuccessAt: now, Quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: &usedHigh, ResetAt: now.Add(time.Hour)}}},
	}
	state := StateSnapshot{Config: cfg, Accounts: accounts, CPAAdmission: CPAAdmissionState{Observed: true, AuthIDs: map[string]struct{}{"more": {}, "less": {}}}, Now: now}
	req := requestWithCandidates("more", "less")
	managementOrder := BuildOrderedAccounts(req, state, now)
	production := SelectAccount(*schedulerSnapshotFromState(state, nil), []Candidate{{ID: "more", Provider: "codex"}, {ID: "less", Provider: "codex"}}, now)
	if len(managementOrder) == 0 || managementOrder[0].AuthID != production.AuthID || production.AuthID != "less" {
		t.Fatalf("management=%#v production=%#v", managementOrder, production)
	}
}

func TestStrategiesPreserveCacheAndTrialSemantics(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	strategies := []SelectionStrategy{
		SelectionStrategyQuotaHigh,
		SelectionStrategyQuotaLow,
		SelectionStrategySubscriptionHigh,
		SelectionStrategySubscriptionLow,
		SelectionStrategyExpirySoon,
	}
	for _, strategy := range strategies {
		t.Run(string(strategy), func(t *testing.T) {
			accounts := []AccountView{
				{ID: "preferred", Instance: 1, Cache: CacheFresh, QuotaScore: QuotaScore{Known: true, Remaining: 50}, PlanType: "free", SubscriptionExpiresAt: now.Add(48 * time.Hour)},
				{ID: "aging", Instance: 2, Cache: CacheAging, QuotaScore: QuotaScore{Known: true, Remaining: 40}, PlanType: "free", SubscriptionExpiresAt: now.Add(72 * time.Hour)},
				{ID: "unknown", Instance: 3, Cache: CacheUnknown, QuotaScore: QuotaScore{Known: true, Remaining: 100}, PlanType: "pro", SubscriptionExpiresAt: now.Add(time.Hour)},
				{ID: "stale", Instance: 4, Cache: CacheStale, LastKnownAvailable: true, QuotaScore: QuotaScore{Known: true, Remaining: 100}, PlanType: "pro", SubscriptionExpiresAt: now.Add(time.Hour)},
				{ID: "trial", Instance: 5, Cache: CacheFresh, Trial: TrialActive, QuotaScore: QuotaScore{Known: true, Remaining: 100}, PlanType: "pro", SubscriptionExpiresAt: now.Add(time.Hour)},
			}
			active := map[string]struct{}{}
			candidates := make([]Candidate, 0, len(accounts))
			for _, account := range accounts {
				active[account.ID] = struct{}{}
				candidates = append(candidates, Candidate{ID: account.ID, Provider: "codex"})
			}
			result := SelectAccount(SchedulerSnapshot{SelectionStrategy: strategy, SubscriptionRanks: subscriptionRanks([]string{"free", "pro"}), Accounts: accounts, ActiveHighestTier: active}, candidates, now)
			if result.Class != Preferred || (result.AuthID != "preferred" && result.AuthID != "aging") {
				t.Fatalf("result = %#v, want fresh/aging preferred class before opportunistic strategy values", result)
			}
			for _, account := range result.Ordered {
				if account.ID == "trial" {
					t.Fatal("active trial entered selectable strategy order")
				}
			}
		})
	}
}

func TestClearingSelectionStrategyRestoresLegacyOrder(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	accounts := []AccountView{
		{ID: "later-high-quota", Cache: CacheFresh, Expiry: now.Add(48 * time.Hour), RemainingQuota: 90, QuotaScore: QuotaScore{Known: true, Remaining: 90}},
		{ID: "earlier-low-quota", Cache: CacheFresh, Expiry: now.Add(24 * time.Hour), RemainingQuota: 10, QuotaScore: QuotaScore{Known: true, Remaining: 10}},
	}
	active := map[string]struct{}{"later-high-quota": {}, "earlier-low-quota": {}}
	candidates := []Candidate{{ID: "later-high-quota", Provider: "codex"}, {ID: "earlier-low-quota", Provider: "codex"}}
	strategy := SelectAccount(SchedulerSnapshot{SelectionStrategy: SelectionStrategyQuotaHigh, Accounts: accounts, ActiveHighestTier: active}, candidates, now)
	legacy := SelectAccount(SchedulerSnapshot{MonthlyMode: MonthlyModeExpiryOrder, Accounts: accounts, ActiveHighestTier: active}, candidates, now)
	if strategy.AuthID != "later-high-quota" || legacy.AuthID != "earlier-low-quota" {
		t.Fatalf("strategy=%q legacy=%q", strategy.AuthID, legacy.AuthID)
	}
}
