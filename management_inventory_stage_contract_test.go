package main

import (
	"reflect"
	"testing"
)

func TestManagementInventoryStageCountsAndAnnotationFields(t *testing.T) {
	requireManagementInventorySchema(t)
	state, _, listCalls := installManagementInventoryFixture(t)
	annotations := state.Annotations()
	annotations.Accounts["auth:highest-oauth"] = AccountAnnotation{Alias: "High OAuth", Tags: []string{"team-a", "paid"}, GroupID: "core"}
	state.SetAnnotations(annotations)
	cfg := state.Config()
	cfg.ExcludeFreeAccounts = false
	state.ReplaceConfig(cfg)

	payload := readManagementInventoryStatus(t)
	byID := managementInventoryEntriesByAuthID(t, managementInventoryEntries(t, payload))
	entry := byID["highest-oauth"]
	if got := managementInventoryEntryString(t, entry, "Alias"); got != "High OAuth" {
		t.Fatalf("inventory alias = %q, want High OAuth", got)
	}
	if got := managementInventoryEntryString(t, entry, "GroupID"); got != "core" {
		t.Fatalf("inventory group id = %q, want core", got)
	}
	if got := managementInventoryEntryStringSlice(t, entry, "Tags"); !equalStrings(got, []string{"team-a", "paid"}) {
		t.Fatalf("inventory tags = %v, want team-a,paid", got)
	}

	counts := reflect.ValueOf(payload).FieldByName("Inventory").FieldByName("Counts")
	if !counts.IsValid() || counts.Kind() != reflect.Struct {
		t.Fatalf("management inventory counts = %#v, want struct", payload.Inventory)
	}
	wantCounts := map[string]int{"Total": 6, "HighestTier": 5, "InPool": 6, "InPoolCandidates": 5, "Schedulable": 1}
	for field, want := range wantCounts {
		value := counts.FieldByName(field)
		if !value.IsValid() || value.Kind() != reflect.Int {
			t.Fatalf("inventory count %s = %#v, want int field", field, value)
		}
		if got := int(value.Int()); got != want {
			t.Fatalf("inventory count %s = %d, want %d", field, got, want)
		}
	}
	if *listCalls != 1 {
		t.Fatalf("inventory stage read caused host.auth.list calls = %d, want 1 shared publication", *listCalls)
	}
}

func managementInventoryEntryStringSlice(t *testing.T, entry reflect.Value, fieldName string) []string {
	t.Helper()
	field := entry.FieldByName(fieldName)
	if !field.IsValid() || field.Kind() != reflect.Slice {
		t.Fatalf("inventory entry field %s = %#v, want string slice", fieldName, field)
	}
	out := make([]string, field.Len())
	for index := range out {
		out[index] = field.Index(index).String()
	}
	return out
}

func TestManagementInventoryStageCountsReflectPoolMembership(t *testing.T) {
	requireManagementInventorySchema(t)
	state, _, listCalls := installManagementInventoryFixture(t)
	annotations := state.Annotations()
	annotations.Accounts["auth:highest-oauth"] = AccountAnnotation{GroupID: "core"}
	annotations.Accounts["auth:low-oauth"] = AccountAnnotation{GroupID: "core"}
	state.SetAnnotations(annotations)
	cfg := state.Config()
	cfg.ActivePool = "group:core"
	cfg.ExcludeFreeAccounts = false
	state.ReplaceConfig(cfg)

	payload := readManagementInventoryStatus(t)
	counts := reflect.ValueOf(payload).FieldByName("Inventory").FieldByName("Counts")
	if !counts.IsValid() || counts.Kind() != reflect.Struct {
		t.Fatalf("management inventory counts = %#v, want struct", payload.Inventory)
	}
	wantCounts := map[string]int{"Total": 6, "HighestTier": 5, "InPool": 2, "InPoolCandidates": 1, "Schedulable": 1}
	for field, want := range wantCounts {
		value := counts.FieldByName(field)
		if !value.IsValid() || value.Kind() != reflect.Int {
			t.Fatalf("inventory count %s = %#v, want int field", field, value)
		}
		if got := int(value.Int()); got != want {
			t.Fatalf("inventory count %s = %d, want %d", field, got, want)
		}
	}
	if *listCalls != 1 {
		t.Fatalf("inventory pool read caused host.auth.list calls = %d, want 1 shared publication", *listCalls)
	}
}
