package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CodexCredentials struct {
	AccessToken           string
	RefreshToken          string
	IDToken               string
	ChatGPTAccountID      string
	ExpiresAt             time.Time
	PlanType              string
	SubscriptionExpiresAt time.Time
}

func ExtractCodexCredentials(raw json.RawMessage) (CodexCredentials, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return CodexCredentials{}, err
	}

	accessToken, ok := lookupStringDeep(doc, "access_token")
	if !ok || accessToken == "" {
		return CodexCredentials{}, errors.New("missing access_token")
	}
	refreshToken, _ := lookupStringDeep(doc, "refresh_token")
	idToken, _ := lookupStringDeep(doc, "id_token")
	expiresAt, _ := lookupTimeDeep(doc, "expired", "expire", "expires_at", "expiresAt")

	accountID, _ := lookupStringDeep(doc, "account_id")
	if accountID == "" {
		accountID, _ = lookupStringDeep(doc, "chatgpt_account_id")
	}
	planType, subscriptionExpiresAt := extractSubscriptionClaims(idToken)
	if accountID == "" {
		if extracted, err := extractChatGPTAccountID(idToken); err == nil {
			accountID = extracted
		} else if idToken != "" {
			if _, decodeErr := decodeJWTClaims(idToken); decodeErr != nil {
				return CodexCredentials{}, fmt.Errorf("missing chatgpt_account_id: %w", decodeErr)
			}
		}
	}

	return CodexCredentials{
		AccessToken:           accessToken,
		RefreshToken:          refreshToken,
		IDToken:               idToken,
		ChatGPTAccountID:      accountID,
		ExpiresAt:             expiresAt,
		PlanType:              planType,
		SubscriptionExpiresAt: subscriptionExpiresAt,
	}, nil
}

func extractSubscriptionClaims(idToken string) (string, time.Time) {
	if idToken == "" {
		return "", time.Time{}
	}
	claims, err := decodeJWTClaims(idToken)
	if err != nil {
		return "", time.Time{}
	}
	plan, _ := lookupStringDeepAny(claims, "https://api.openai.com/auth.chatgpt_plan_type", "chatgpt_plan_type", "plan_type", "planType")
	var expiresAt time.Time
	if raw, ok := lookupAnyDeep(claims, "https://api.openai.com/auth.chatgpt_subscription_active_until", "chatgpt_subscription_active_until", "subscription_active_until", "subscription_expires_at"); ok {
		expiresAt, _ = parseSubscriptionTime(raw)
	}
	return normalizePlanType(plan), expiresAt
}

func normalizePlanType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func lookupStringDeepAny(v any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := lookupStringDeep(v, key); ok {
			return value, true
		}
	}
	return "", false
}

func lookupAnyDeep(v any, keys ...string) (any, bool) {
	switch typed := v.(type) {
	case map[string]any:
		for _, key := range keys {
			if value, ok := typed[key]; ok {
				return value, true
			}
		}
		for _, child := range typed {
			if value, ok := lookupAnyDeep(child, keys...); ok {
				return value, true
			}
		}
	case []any:
		for _, child := range typed {
			if value, ok := lookupAnyDeep(child, keys...); ok {
				return value, true
			}
		}
	}
	return nil, false
}

func parseSubscriptionTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		raw := strings.TrimSpace(typed)
		if parsed, ok := parseAuthTime(raw); ok {
			return parsed, true
		}
		seconds, err := strconv.ParseInt(raw, 10, 64)
		if err == nil {
			return time.Unix(seconds, 0).UTC(), true
		}
	case float64:
		return time.Unix(int64(typed), 0).UTC(), true
	case json.Number:
		seconds, err := typed.Int64()
		if err == nil {
			return time.Unix(seconds, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func extractChatGPTAccountID(idToken string) (string, error) {
	claims, err := decodeJWTClaims(idToken)
	if err != nil {
		return "", fmt.Errorf("missing chatgpt_account_id: %w", err)
	}
	if v, ok := stringFromMap(claims, "https://api.openai.com/auth.chatgpt_account_id"); ok && v != "" {
		return v, nil
	}
	if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if v, ok := stringFromMap(authClaim, "chatgpt_account_id"); ok && v != "" {
			return v, nil
		}
	}
	if v, ok := lookupStringDeep(claims, "https://api.openai.com/auth.chatgpt_account_id"); ok && v != "" {
		return v, nil
	}
	if v, ok := lookupStringDeep(claims, "chatgpt_account_id"); ok && v != "" {
		return v, nil
	}
	return "", errors.New("missing chatgpt_account_id")
}

func decodeJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errors.New("invalid id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func lookupStringDeep(v any, key string) (string, bool) {
	switch typed := v.(type) {
	case map[string]any:
		if found, ok := stringFromMap(typed, key); ok {
			return found, true
		}
		for _, child := range typed {
			if found, ok := lookupStringDeep(child, key); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range typed {
			if found, ok := lookupStringDeep(child, key); ok {
				return found, true
			}
		}
	}
	return "", false
}

func stringFromMap(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func lookupTimeDeep(v any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if raw, ok := lookupStringDeep(v, key); ok {
			if parsed, ok := parseAuthTime(raw); ok {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

func parseAuthTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func accessTokenExpired(credentials CodexCredentials, now time.Time) bool {
	if credentials.ExpiresAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	return !now.Add(2 * time.Minute).Before(credentials.ExpiresAt)
}
