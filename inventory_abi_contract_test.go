package main

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
)

func TestABIHostAuthListerRetainsInventoryFieldsAcrossAllCodexEntries(t *testing.T) {
	lister := ABIHostAuthLister{call: func(_ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"files":[
			{"id":"highest-oauth","auth_index":"idx-high","provider":"codex","account_type":"oauth","status":"active","priority":9,"email":"h@example.com"},
			{"id":"low-oauth","auth_index":"idx-low","provider":"codex","account_type":"oauth","status":"active","priority":1},
			{"id":"disabled","auth_index":"idx-disabled","provider":"codex","account_type":"oauth","status":"active","priority":9,"disabled":true},
			{"id":"unavailable","auth_index":"idx-unavailable","provider":"codex","account_type":"oauth","status":"unavailable","priority":9,"unavailable":true,"status_message":"down"},
			{"id":"auth-failure","auth_index":"idx-auth-failure","provider":"codex","account_type":"oauth","status":"error","status_message":"authentication failed","priority":9},
			{"id":"api-key","auth_index":"idx-api-key","provider":"codex","account_type":"api_key","status":"active","priority":9},
			{"id":"other-provider","auth_index":"idx-other","provider":"claude","account_type":"oauth","status":"active","priority":99}
		]}`), nil
	}}

	entries, err := lister.ListHostAuths(context.Background())
	if err != nil {
		t.Fatalf("ListHostAuths: %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("decoded codex entries = %d, want 6 complete codex entries (non-codex excluded)", len(entries))
	}
	byID := make(map[string]RosterEntry, len(entries))
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
		ids = append(ids, entry.ID)
	}
	sort.Strings(ids)
	want := []string{"api-key", "auth-failure", "disabled", "highest-oauth", "low-oauth", "unavailable"}
	if !equalStrings(ids, want) {
		t.Fatalf("decoded auth IDs = %v, want %v", ids, want)
	}

	if !byID["disabled"].Disabled {
		t.Fatalf("disabled entry lost Disabled flag: %#v", byID["disabled"])
	}
	if !byID["unavailable"].Unavailable {
		t.Fatalf("unavailable entry lost Unavailable flag: %#v", byID["unavailable"])
	}
	if got := byID["api-key"].AccountType; got != "api_key" {
		t.Fatalf("api-key AccountType = %q, want api_key", got)
	}
	if got := byID["auth-failure"].Status; got != "error" {
		t.Fatalf("auth-failure Status = %q, want error", got)
	}
	if got := byID["auth-failure"].StatusMessage; got != "authentication failed" {
		t.Fatalf("auth-failure StatusMessage = %q, want authentication failed", got)
	}
	if got := byID["unavailable"].Status; got != "unavailable" {
		t.Fatalf("unavailable Status = %q, want unavailable", got)
	}
	if got := byID["highest-oauth"].Email; got != "h@example.com" {
		t.Fatalf("highest-oauth Email = %q, want h@example.com", got)
	}
	for _, id := range want {
		entry := byID[id]
		if entry.Priority == nil {
			t.Fatalf("entry %q lost authoritative Priority pointer", id)
		}
	}
	if got := *byID["low-oauth"].Priority; got != 1 {
		t.Fatalf("low-oauth Priority = %d, want 1", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
