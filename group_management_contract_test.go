package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestGroupManagementRoutesAreRegisteredAsCompatibilityEndpoints(t *testing.T) {
	registration := RegisterManagement()
	want := map[string]string{
		managementBasePath + "/annotations/group":        http.MethodPatch,
		managementBasePath + "/annotations/groups":       http.MethodPost,
		managementBasePath + "/annotations/groups/batch": http.MethodPost,
		managementBasePath + "/active-pool":              http.MethodPut,
	}
	for path, method := range want {
		found := false
		for _, route := range registration.Routes {
			if route.Path == path && route.Method == method {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("compatibility route %s %s is not registered", method, path)
		}
	}
}

func TestGroupManagementCompatibilityEndpointsNeverMutateHistoricalState(t *testing.T) {
	store := NewPluginState(Config{ActivePool: "group:legacy"})
	store.SetAnnotations(AnnotationState{
		Accounts: map[string]AccountAnnotation{"auth:one": {GroupID: "legacy", Alias: "kept"}},
		Groups:   map[string]GroupAnnotation{"legacy": {Name: "Legacy"}},
	})
	beforeConfig := store.Config()
	beforeAnnotations := store.Annotations()
	requests := []pluginapi.ManagementRequest{
		{Method: http.MethodPatch, Path: managementBasePath + "/annotations/group", Body: []byte(`{"id":"legacy","name":"new"}`)},
		{Method: http.MethodPost, Path: managementBasePath + "/annotations/groups", Body: []byte(`{"name":"new"}`)},
		{Method: http.MethodPost, Path: managementBasePath + "/annotations/groups/batch", Body: []byte(`{"account_ids":["one"],"group_id":"new"}`)},
		{Method: http.MethodPut, Path: managementBasePath + "/active-pool", Body: []byte(`{"active_pool":"all"}`)},
	}
	for _, req := range requests {
		resp := HandleManagementRequest(store, req, time.Now())
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("%s %s status = %d body=%s, want 410", req.Method, req.Path, resp.StatusCode, resp.Body)
		}
		var body map[string]string
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != pluginGroupManagementRemoved {
			t.Fatalf("error = %q, want %q", body["error"], pluginGroupManagementRemoved)
		}
	}
	if got := store.Config(); !reflect.DeepEqual(got, beforeConfig) {
		t.Fatalf("config mutated: got %#v want %#v", got, beforeConfig)
	}
	if got := store.Annotations(); !reflect.DeepEqual(got, beforeAnnotations) {
		t.Fatalf("annotations mutated: got %#v want %#v", got, beforeAnnotations)
	}
}
