package main

import (
	"strings"
	"testing"
	"time"
)

func TestIdentityContractResolvesReloginByChatGPTAccountID(t *testing.T) {
	store := newGroupContractStore(t)
	store.SetAnnotations(AnnotationState{Accounts: map[string]AccountAnnotation{
		"chatgpt:acct-1": {Alias: "relog", GroupID: "grp_x"},
	}})

	original := AccountState{AuthID: "file-old", ChatGPTAccountID: "acct-1"}
	relogged := AccountState{AuthID: "file-new", ChatGPTAccountID: "acct-1"}
	for _, account := range []AccountState{original, relogged} {
		applied := ApplyAnnotations([]AccountState{account}, store.Annotations())
		if applied[0].Annotation.Alias != "relog" {
			t.Fatalf("auth %q annotation alias = %q, want inherited %q", account.AuthID, applied[0].Annotation.Alias, "relog")
		}
		if applied[0].Annotation.GroupID != "grp_x" {
			t.Fatalf("auth %q annotation group = %q, want inherited %q", account.AuthID, applied[0].Annotation.GroupID, "grp_x")
		}
	}
}

func TestIdentityContractFallsBackToAuthIDThenNormalizedEmailThenAuthIndex(t *testing.T) {
	store := newGroupContractStore(t)
	store.SetAnnotations(AnnotationState{Accounts: map[string]AccountAnnotation{
		"auth:file-a":            {Alias: "by-auth"},
		"email:team@example.com": {Alias: "by-email"},
		"index:idx-1":            {Alias: "by-index"},
	}})

	cases := []struct {
		name    string
		account AccountState
		want    string
	}{
		{name: "auth", account: AccountState{AuthID: "file-a"}, want: "by-auth"},
		{name: "normalized-email", account: AccountState{Email: " Team@Example.COM "}, want: "by-email"},
		{name: "auth-index", account: AccountState{AuthIndex: "idx-1"}, want: "by-index"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			applied := ApplyAnnotations([]AccountState{tt.account}, store.Annotations())
			if got := applied[0].Annotation.Alias; got != tt.want {
				t.Fatalf("annotation alias = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIdentityContractMultiAnnotationConflictStopsMigrationAndReports(t *testing.T) {
	store := newGroupContractStore(t)
	store.SetAnnotations(AnnotationState{Accounts: map[string]AccountAnnotation{
		"auth:file-a":    {Alias: "A", GroupID: "grp_a"},
		"chatgpt:acct-1": {Alias: "B", GroupID: "grp_b"},
	}})

	account := AccountState{AuthID: "file-a", ChatGPTAccountID: "acct-1"}
	applied, conflicts := ApplyAnnotationsWithConflicts([]AccountState{account}, store.Annotations())
	if applied[0].Annotation.Alias != "" || applied[0].Annotation.GroupID != "" {
		t.Fatalf("conflicting account migrated annotation: %#v", applied[0].Annotation)
	}
	requireIdentityConflictForAccount(t, conflicts, account.AuthID)
	requireStatusIdentityConflictForAccounts(t, store.Annotations(), account)
}

func TestIdentityContractSharedAnnotationConflictStopsMigrationAndReports(t *testing.T) {
	store := newGroupContractStore(t)
	store.SetAnnotations(AnnotationState{Accounts: map[string]AccountAnnotation{
		"chatgpt:acct-1": {Alias: "shared", GroupID: "grp_x"},
	}})

	first := AccountState{AuthID: "file-x", ChatGPTAccountID: "acct-1"}
	second := AccountState{AuthID: "file-y", ChatGPTAccountID: "acct-1"}
	applied, conflicts := ApplyAnnotationsWithConflicts([]AccountState{first, second}, store.Annotations())
	for i := range applied {
		if applied[i].Annotation.Alias != "" || applied[i].Annotation.GroupID != "" {
			t.Fatalf("account %d in shared-annotation conflict migrated annotation: %#v", i, applied[i].Annotation)
		}
	}
	requireIdentityConflictForAccounts(t, conflicts, first.AuthID, second.AuthID)
	requireStatusIdentityConflictForAccounts(t, store.Annotations(), first, second)
}

func requireIdentityConflictForAccount(t *testing.T, conflicts []AnnotationIdentityConflict, authID string) {
	t.Helper()
	if !identityConflictCovers(conflicts, authID) {
		t.Fatalf("conflicts = %#v, want account_identity_conflict covering %q", conflicts, authID)
	}
}

func requireIdentityConflictForAccounts(t *testing.T, conflicts []AnnotationIdentityConflict, authIDs ...string) {
	t.Helper()
	for _, authID := range authIDs {
		if !identityConflictCovers(conflicts, authID) {
			t.Fatalf("conflicts = %#v, want account_identity_conflict covering %q", conflicts, authID)
		}
	}
}

func identityConflictCovers(conflicts []AnnotationIdentityConflict, authID string) bool {
	for _, conflict := range conflicts {
		if conflict.Reason != accountIdentityConflict {
			continue
		}
		for _, id := range conflict.AccountIDs {
			if id == authID {
				return true
			}
		}
	}
	return false
}

func requireStatusIdentityConflictForAccounts(t *testing.T, state AnnotationState, accounts ...AccountState) {
	t.Helper()
	payload := BuildStatusPayload(StateSnapshot{
		Config:      DefaultConfig(),
		Accounts:    accounts,
		Annotations: state,
		Now:         time.Now(),
	}, nil)
	if !statusIdentityConflictCovers(payload, accounts...) {
		t.Fatalf("status identity_warnings = %#v, want account_identity_conflict covering %d accounts", payload.IdentityWarnings, len(accounts))
	}
}

func statusIdentityConflictCovers(payload StatusPayload, accounts ...AccountState) bool {
	for _, warning := range payload.IdentityWarnings {
		if warning.Reason != accountIdentityConflict {
			continue
		}
		if strings.Contains(warning.Kind, "chatgpt:") || strings.Contains(warning.Kind, "acct-") {
			return false
		}
		covered := 0
		for _, account := range accounts {
			for _, id := range warning.AccountIDs {
				if id == account.AuthID {
					covered++
				}
			}
		}
		if covered == len(accounts) {
			return true
		}
	}
	return false
}
