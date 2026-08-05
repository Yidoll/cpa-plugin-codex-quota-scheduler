package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPublicStatusDataLoadsWithoutKeyAndContainsOnlySafeAggregateState(t *testing.T) {
	store := NewPluginState(DefaultConfig())
	store.UpsertQuota(AccountState{
		AuthID: "auth-secret@example.com", AuthIndex: "idx-secret", Email: "secret@example.com",
		Provider: "codex", Family: AccountFamilyWeekly, LastError: "Authorization Bearer secret-token",
		Annotation: AccountAnnotation{Alias: "safe alias"},
	})
	store.RecordLog("error", "scheduler.selected", "secret@example.com Authorization Bearer secret-token", map[string]any{
		"auth_id": "auth-secret@example.com", "token": "secret-token",
	}, time.Now())

	resp := HandleManagementRequest(store, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource" + managementBasePath + "/status-data",
	}, time.Now())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public status = %d body=%s, want 200", resp.StatusCode, resp.Body)
	}
	var payload PublicStatusPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("decode public payload: %v; body=%s", err, resp.Body)
	}
	if payload.PluginID != PluginID || payload.Settings.SelectionStrategyDisplay == "" {
		t.Fatalf("public payload missing safe configuration: %#v", payload)
	}
	if payload.Aggregate.AccountCount != 1 {
		t.Fatalf("aggregate account count = %d, want 1", payload.Aggregate.AccountCount)
	}
	forbidden := []string{
		"auth-secret@example.com", "idx-secret", "secret@example.com", "secret-token", "Authorization",
		"last_error", `"logs":`, `"accounts":`, `"quota":`, `"auth_id":`, `"email":`, "cookie", "access_token",
	}
	raw := strings.ToLower(string(resp.Body))
	for _, marker := range forbidden {
		if strings.Contains(raw, strings.ToLower(marker)) {
			t.Fatalf("public payload leaked %q: %s", marker, resp.Body)
		}
	}
}

func TestPublicStatusDataQueryCannotReachManagementActions(t *testing.T) {
	store := NewPluginState(DefaultConfig())
	resp := HandleManagementRequest(store, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource" + managementBasePath + "/status-data",
		Query:  url.Values{"action": {"refresh"}, "payload": {"{}"}},
	}, time.Now())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public query status = %d body=%s, want 200 read-only response", resp.StatusCode, resp.Body)
	}
}
