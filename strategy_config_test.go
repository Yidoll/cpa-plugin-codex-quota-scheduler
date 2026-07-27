package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSelectionStrategyConstants(t *testing.T) {
	got := []SelectionStrategy{
		SelectionStrategyQuotaHigh,
		SelectionStrategyQuotaLow,
		SelectionStrategySubscriptionHigh,
		SelectionStrategySubscriptionLow,
		SelectionStrategyExpirySoon,
	}
	want := []SelectionStrategy{"quota_high", "quota_low", "subscription_high", "subscription_low", "expiry_soon"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("strategies = %#v, want %#v", got, want)
	}
}

func TestOldPluginDiskStateLoadsWithoutStrategyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"config":{"MonthlyMode":"priority"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadPluginDiskState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Config.SelectionStrategy != "" || state.Config.SubscriptionOrder != nil || state.Config.MonthlyMode != MonthlyModePriority {
		t.Fatalf("old state config = %#v", state.Config)
	}
}

func TestDecodeConfigSelectionStrategy(t *testing.T) {
	tests := []struct {
		name              string
		raw               string
		wantStrategy      SelectionStrategy
		wantSubscriptions []string
		wantMonthlyMode   MonthlyMode
	}{
		{
			name:            "legacy fields absent",
			raw:             "monthly_mode: priority\n",
			wantMonthlyMode: MonthlyModePriority,
		},
		{
			name:            "quota strategy",
			raw:             "selection_strategy: quota_high\n",
			wantStrategy:    SelectionStrategyQuotaHigh,
			wantMonthlyMode: MonthlyModeExpiryOrder,
		},
		{
			name:              "subscription normalization",
			raw:               "selection_strategy: subscription_high\nsubscription_order:\n  - ' Free '\n  - PLUS\n  - Pro\n",
			wantStrategy:      SelectionStrategySubscriptionHigh,
			wantSubscriptions: []string{"free", "plus", "pro"},
			wantMonthlyMode:   MonthlyModeExpiryOrder,
		},
		{
			name:              "new and old fields coexist",
			raw:               "monthly_mode: priority\nselection_strategy: subscription_low\nsubscription_order: [free, plus]\n",
			wantStrategy:      SelectionStrategySubscriptionLow,
			wantSubscriptions: []string{"free", "plus"},
			wantMonthlyMode:   MonthlyModePriority,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := DecodeConfig([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SelectionStrategy != tt.wantStrategy {
				t.Fatalf("SelectionStrategy = %q, want %q", cfg.SelectionStrategy, tt.wantStrategy)
			}
			if !reflect.DeepEqual(cfg.SubscriptionOrder, tt.wantSubscriptions) {
				t.Fatalf("SubscriptionOrder = %#v, want %#v", cfg.SubscriptionOrder, tt.wantSubscriptions)
			}
			if cfg.MonthlyMode != tt.wantMonthlyMode {
				t.Fatalf("MonthlyMode = %q, want %q", cfg.MonthlyMode, tt.wantMonthlyMode)
			}
		})
	}
}

func TestDecodeConfigRejectsInvalidSelectionStrategy(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unknown strategy", raw: "selection_strategy: fastest\n", want: "selection_strategy"},
		{name: "subscription order required", raw: "selection_strategy: subscription_high\n", want: "subscription_order"},
		{name: "empty subscription item", raw: "selection_strategy: subscription_low\nsubscription_order: [free, ' ']\n", want: "empty"},
		{name: "duplicate normalized item", raw: "selection_strategy: subscription_high\nsubscription_order: [PLUS, ' plus ']\n", want: "duplicate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeConfig([]byte(tt.raw))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeConfig error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateConfigIsSharedByRuntimeConfiguration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SelectionStrategy = SelectionStrategySubscriptionHigh
	if _, err := ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig accepted subscription strategy without subscription_order")
	}

	cfg.SubscriptionOrder = []string{" Free ", "PLUS"}
	normalized, err := ValidateConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized.SubscriptionOrder, []string{"free", "plus"}) {
		t.Fatalf("SubscriptionOrder = %#v", normalized.SubscriptionOrder)
	}

	store := NewPluginState(DefaultConfig())
	invalid := DefaultConfig()
	invalid.SelectionStrategy = SelectionStrategySubscriptionLow
	if err := store.ReplaceConfig(invalid); err == nil {
		t.Fatal("ReplaceConfig accepted invalid runtime configuration")
	}
	if got := store.Config().SelectionStrategy; got != "" {
		t.Fatalf("invalid runtime configuration replaced last valid strategy with %q", got)
	}

	valid := DefaultConfig()
	valid.SelectionStrategy = SelectionStrategySubscriptionHigh
	valid.SubscriptionOrder = []string{"free", "plus"}
	if err := store.ReplaceConfig(valid); err != nil {
		t.Fatal(err)
	}
	returned := store.Config()
	returned.SubscriptionOrder[0] = "mutated"
	if got := store.Config().SubscriptionOrder[0]; got != "free" {
		t.Fatalf("runtime config exposed mutable subscription_order: %q", got)
	}
}
