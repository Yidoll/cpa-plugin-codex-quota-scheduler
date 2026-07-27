package main

import (
	"testing"
	"time"
)

func TestBottleneckQuotaScore(t *testing.T) {
	percent := func(value float64) *float64 { return &value }
	tests := []struct {
		name  string
		quota ParsedQuota
		want  QuotaScore
	}{
		{
			name:  "two windows uses bottleneck",
			quota: ParsedQuota{FiveHour: &QuotaWindow{UsedPercent: percent(20)}, LongWindow: &QuotaWindow{UsedPercent: percent(70)}},
			want:  QuotaScore{Known: true, Remaining: 30},
		},
		{
			name:  "single window",
			quota: ParsedQuota{FiveHour: &QuotaWindow{UsedPercent: percent(25)}},
			want:  QuotaScore{Known: true, Remaining: 75},
		},
		{name: "all unknown", quota: ParsedQuota{}, want: QuotaScore{}},
		{
			name:  "lower boundary",
			quota: ParsedQuota{FiveHour: &QuotaWindow{UsedPercent: percent(0)}},
			want:  QuotaScore{Known: true, Remaining: 100},
		},
		{
			name:  "upper boundary",
			quota: ParsedQuota{LongWindow: &QuotaWindow{UsedPercent: percent(100)}},
			want:  QuotaScore{Known: true, Remaining: 0},
		},
		{
			name:  "clamps out of range values",
			quota: ParsedQuota{FiveHour: &QuotaWindow{UsedPercent: percent(-10)}, LongWindow: &QuotaWindow{UsedPercent: percent(140)}},
			want:  QuotaScore{Known: true, Remaining: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bottleneckQuotaScore(tt.quota); got != tt.want {
				t.Fatalf("bottleneckQuotaScore() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBottleneckQuotaScoreIsPublishedWithoutChangingExhaustion(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	used := 40.0
	account := AccountState{
		AuthID: "auth-1", LastSuccessAt: now,
		Quota: ParsedQuota{FiveHour: &QuotaWindow{UsedPercent: &used, Exhausted: false, ResetAt: now.Add(time.Hour)}},
	}
	view := accountViewFromState(account, DefaultConfig(), now, nil)
	if view.QuotaScore != (QuotaScore{Known: true, Remaining: 60}) {
		t.Fatalf("QuotaScore = %#v", view.QuotaScore)
	}
	if view.Exhausted {
		t.Fatal("score publication changed exhaustion classification")
	}
	snapshot := schedulerSnapshotFromState(StateSnapshot{Config: DefaultConfig(), Accounts: []AccountState{account}, Now: now}, nil)
	if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].QuotaScore != view.QuotaScore {
		t.Fatalf("snapshot quota score = %#v", snapshot.Accounts)
	}
}
