package main

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
)

const (
	groupNameConflict      = "group_name_conflict"
	groupReferenceNotFound = "group_reference_not_found"
	groupIsActive          = "group_is_active"
	groupNotEmpty          = "group_not_empty"
	groupNotFound          = "group_not_found"
	ungroupedNotDeletable  = "ungrouped_not_deletable"
	batchMembershipInvalid = "batch_membership_invalid"
	inventoryNotConfirmed  = "inventory_not_confirmed"
)

// generateGroupID returns a new opaque, stable group identifier.
func generateGroupID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "grp_" + hex.EncodeToString(raw[:]), nil
}

// normalizeGroupName trims surrounding whitespace from a group name.
func normalizeGroupName(name string) string {
	return strings.TrimSpace(name)
}

// groupNameKey returns the case-insensitive uniqueness key for a group name.
func groupNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// findGroupNameConflict returns the ID of an existing group whose normalized
// name collides with candidate, ignoring the group with excludeID.
func findGroupNameConflict(groups map[string]GroupAnnotation, candidate, excludeID string) string {
	key := groupNameKey(candidate)
	for id, group := range groups {
		if id == excludeID {
			continue
		}
		if groupNameKey(group.Name) == key {
			return id
		}
	}
	return ""
}

// groupNameConflictWarnings returns a deterministic warning for each group
// whose normalized name collides with another group's normalized name.
// Empty normalized names are ignored because they carry no conflicting label.
func groupNameConflictWarnings(groups map[string]GroupAnnotation) []StatusGroupWarning {
	idsByKey := make(map[string][]string, len(groups))
	for id, group := range groups {
		key := groupNameKey(group.Name)
		if key == "" {
			continue
		}
		idsByKey[key] = append(idsByKey[key], id)
	}
	var warnings []StatusGroupWarning
	for _, ids := range idsByKey {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		for i, id := range ids {
			conflictID := ids[0]
			if i == 0 {
				conflictID = ids[1]
			}
			warnings = append(warnings, StatusGroupWarning{
				Reason:     groupNameConflict,
				ID:         id,
				ConflictID: conflictID,
				Name:       normalizeGroupName(groups[id].Name),
			})
		}
	}
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].ID < warnings[j].ID })
	return warnings
}

// groupReferenceWarnings returns a deterministic warning for each non-empty
// GroupID referenced by an account that has no matching group definition.
func groupReferenceWarnings(accounts []AccountState, groups map[string]GroupAnnotation) []StatusGroupWarning {
	seen := make(map[string]struct{})
	var warnings []StatusGroupWarning
	for _, account := range accounts {
		groupID := account.Annotation.GroupID
		if groupID == "" {
			continue
		}
		if _, ok := groups[groupID]; ok {
			continue
		}
		if _, dup := seen[groupID]; dup {
			continue
		}
		seen[groupID] = struct{}{}
		warnings = append(warnings, StatusGroupWarning{
			Reason: groupReferenceNotFound,
			ID:     groupID,
		})
	}
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].ID < warnings[j].ID })
	return warnings
}
