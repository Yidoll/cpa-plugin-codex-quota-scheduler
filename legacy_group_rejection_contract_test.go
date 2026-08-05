package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestLegacyPluginGroupWritesAreRejectedWithoutMutation(t *testing.T) {
	store := NewPluginState(DefaultConfig())
	store.SetAnnotations(AnnotationState{
		Accounts: map[string]AccountAnnotation{"auth:one": {GroupID: "legacy"}},
		Groups:   map[string]GroupAnnotation{"legacy": {Name: "Legacy"}},
	})
	beforeConfig := store.Config()
	beforeAnnotations := store.Annotations()

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create", method: http.MethodPost, path: managementBasePath + "/annotations/groups", body: `{"name":"new"}`},
		{name: "delete", method: http.MethodDelete, path: managementBasePath + "/annotations/groups", body: `{"id":"legacy"}`},
		{name: "batch", method: http.MethodPost, path: managementBasePath + "/annotations/groups/batch", body: `{"account_ids":["one"],"group_id":"legacy"}`},
		{name: "active pool", method: http.MethodPut, path: managementBasePath + "/active-pool", body: `{"active_pool":"group:legacy"}`},
		{name: "group patch", method: http.MethodPatch, path: managementBasePath + "/annotations/group", body: `{"id":"legacy","name":"changed"}`},
		{name: "account group patch", method: http.MethodPatch, path: managementBasePath + "/annotations/account", body: `{"auth_id":"one","group_id":"changed"}`},
		{name: "settings active pool", method: http.MethodPut, path: managementBasePath + "/settings", body: `{"active_pool":"group:legacy"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := HandleManagementRequest(store, pluginapi.ManagementRequest{
				Method: tt.method,
				Path:   tt.path,
				Body:   []byte(tt.body),
			}, time.Now())
			if resp.StatusCode != http.StatusGone {
				t.Fatalf("status = %d body=%s, want %d", resp.StatusCode, resp.Body, http.StatusGone)
			}
			var body map[string]string
			if err := json.Unmarshal(resp.Body, &body); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, resp.Body)
			}
			if body["error"] != pluginGroupManagementRemoved {
				t.Fatalf("error = %q, want %q", body["error"], pluginGroupManagementRemoved)
			}
			if got := store.Config(); !reflect.DeepEqual(got, beforeConfig) {
				t.Fatalf("config mutated: got %#v want %#v", got, beforeConfig)
			}
			if got := store.Annotations(); !reflect.DeepEqual(got, beforeAnnotations) {
				t.Fatalf("annotations mutated: got %#v want %#v", got, beforeAnnotations)
			}
		})
	}
}
