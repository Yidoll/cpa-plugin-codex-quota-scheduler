package main

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestLegacySelectionGolden(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	accounts := []AccountView{
		{ID: "weekly-late", Family: AccountFamilyWeekly, Cache: CacheFresh, Expiry: now.Add(48 * time.Hour), RemainingQuota: 90},
		{ID: "monthly-late", Family: AccountFamilyMonthly, Cache: CacheFresh, Expiry: now.Add(72 * time.Hour), RemainingQuota: 95},
		{ID: "weekly-early-low", Family: AccountFamilyWeekly, Cache: CacheFresh, Expiry: now.Add(24 * time.Hour), RemainingQuota: 20},
		{ID: "weekly-early-high", Family: AccountFamilyWeekly, Cache: CacheFresh, Expiry: now.Add(24 * time.Hour), RemainingQuota: 80},
		{ID: "priority", Family: AccountFamilyWeekly, Cache: CacheFresh, PluginPriority: 1, Expiry: now.Add(96 * time.Hour), RemainingQuota: 1},
	}

	tests := []struct {
		name string
		mode MonthlyMode
		want string
	}{
		{
			name: "expiry order",
			mode: MonthlyModeExpiryOrder,
			want: "priority,weekly-early-high,weekly-early-low,weekly-late,monthly-late",
		},
		{
			name: "monthly priority",
			mode: MonthlyModePriority,
			want: "priority,monthly-late,weekly-early-high,weekly-early-low,weekly-late",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ordered := append([]AccountView(nil), accounts...)
			sort.Slice(ordered, func(i, j int) bool {
				return accountViewLess(ordered[i], ordered[j], SelectionPolicy{MonthlyMode: tt.mode})
			})
			ids := make([]string, 0, len(ordered))
			for _, account := range ordered {
				ids = append(ids, account.ID)
			}
			if got := strings.Join(ids, ","); got != tt.want {
				t.Fatalf("legacy order = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyConfigGoldenWhenNewStrategyFieldsAreAbsent(t *testing.T) {
	cfg, err := DecodeConfig([]byte("monthly_mode: priority\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MonthlyMode != MonthlyModePriority {
		t.Fatalf("MonthlyMode = %q, want %q", cfg.MonthlyMode, MonthlyModePriority)
	}
}
