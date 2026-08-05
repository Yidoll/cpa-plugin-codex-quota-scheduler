package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const accountIdentityConflict = "account_identity_conflict"

func NormalizeAnnotationState(state AnnotationState) AnnotationState {
	normalized := AnnotationState{
		Accounts: make(map[string]AccountAnnotation, len(state.Accounts)),
		Groups:   make(map[string]GroupAnnotation, len(state.Groups)),
	}
	for key, annotation := range state.Accounts {
		annotation.Tags = normalizeTags(annotation.Tags)
		normalized.Accounts[key] = annotation
	}
	for key, annotation := range state.Groups {
		annotation.Tags = normalizeTags(annotation.Tags)
		normalized.Groups[key] = annotation
	}
	return normalized
}

func ResolveAnnotationKey(account AccountState) string {
	keys := annotationCandidates(account)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// annotationCandidates returns annotation keys in stable identity resolution
// order: ChatGPT Account ID, Auth ID, normalized email, stable Auth Index,
// then the unresolved instance placeholder.
func annotationCandidates(account AccountState) []string {
	var keys []string
	if account.ChatGPTAccountID != "" {
		keys = append(keys, "chatgpt:"+account.ChatGPTAccountID)
	}
	if account.AuthID != "" {
		keys = append(keys, "auth:"+account.AuthID)
	}
	if email := normalizeEmail(account.Email); email != "" {
		keys = append(keys, "email:"+email)
	}
	if account.AuthIndex != "" {
		keys = append(keys, "index:"+account.AuthIndex)
	}
	if account.ChatGPTAccountID == "" && account.Instance != 0 {
		keys = append(keys, "instance:"+strconv.FormatUint(uint64(account.Instance), 10))
	}
	return keys
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

type AnnotationIdentityConflict struct {
	Reason     string   `json:"reason"`
	Kind       string   `json:"kind"`
	AccountIDs []string `json:"account_ids,omitempty"`
}

func ApplyAnnotationsWithConflicts(accounts []AccountState, state AnnotationState) ([]AccountState, []AnnotationIdentityConflict) {
	return applyAnnotations(accounts, state)
}

func ApplyAnnotations(accounts []AccountState, state AnnotationState) []AccountState {
	applied, _ := applyAnnotations(accounts, state)
	return applied
}

func applyAnnotations(accounts []AccountState, state AnnotationState) ([]AccountState, []AnnotationIdentityConflict) {
	normalized := NormalizeAnnotationState(state)
	applied := make([]AccountState, len(accounts))
	hitKeys := make([][]string, len(accounts))
	for i, account := range accounts {
		applied[i] = cloneAccountState(account)
		var keys []string
		for _, key := range annotationCandidates(account) {
			if _, ok := normalized.Accounts[key]; ok {
				keys = append(keys, key)
			}
		}
		hitKeys[i] = keys
	}

	accountsByKey := make(map[string][]string, len(normalized.Accounts))
	for i, keys := range hitKeys {
		for _, key := range keys {
			accountsByKey[key] = append(accountsByKey[key], accounts[i].AuthID)
		}
	}

	conflictingKeys := make(map[string]struct{})
	for key, ids := range accountsByKey {
		if len(uniqueNonEmpty(ids)) >= 2 {
			conflictingKeys[key] = struct{}{}
		}
	}
	for _, keys := range hitKeys {
		if len(keys) < 2 {
			continue
		}
		for _, key := range keys {
			conflictingKeys[key] = struct{}{}
		}
	}

	var conflicts []AnnotationIdentityConflict
	seen := make(map[string]struct{})
	addConflict := func(key string, accountIDs []string) {
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		conflicts = append(conflicts, AnnotationIdentityConflict{
			Reason:     accountIdentityConflict,
			Kind:       annotationKeyKind(key),
			AccountIDs: accountIDs,
		})
	}
	for key, ids := range accountsByKey {
		ids = uniqueNonEmpty(ids)
		if len(ids) >= 2 {
			addConflict(key, ids)
		}
	}
	for i, keys := range hitKeys {
		if len(keys) < 2 {
			continue
		}
		for _, key := range keys {
			addConflict(key, []string{accounts[i].AuthID})
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Kind != conflicts[j].Kind {
			return conflicts[i].Kind < conflicts[j].Kind
		}
		return strings.Join(conflicts[i].AccountIDs, ",") < strings.Join(conflicts[j].AccountIDs, ",")
	})

	for i, keys := range hitKeys {
		if len(keys) != 1 {
			continue
		}
		if _, conflicted := conflictingKeys[keys[0]]; conflicted {
			continue
		}
		applied[i].Annotation = cloneAccountAnnotation(normalized.Accounts[keys[0]])
	}
	return applied, conflicts
}

func annotationKeyKind(key string) string {
	if index := strings.IndexByte(key, ':'); index >= 0 {
		return key[:index]
	}
	return key
}

func uniqueNonEmpty(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func LoadAnnotations(path string) (AnnotationState, error) {
	if path == "" {
		return NormalizeAnnotationState(AnnotationState{}), nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NormalizeAnnotationState(AnnotationState{}), nil
	}
	if err != nil {
		return AnnotationState{}, err
	}
	var state AnnotationState
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &state); err != nil {
			return AnnotationState{}, err
		}
	}
	return NormalizeAnnotationState(state), nil
}

func SaveAnnotations(path string, state AnnotationState) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(NormalizeAnnotationState(state), "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
