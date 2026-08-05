package main

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func managementAttribute(fragment, name string) string {
	match := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `="([^"]*)"`).FindStringSubmatch(fragment)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func TestManagementPageRemovesPluginGroupAndActivePoolControls(t *testing.T) {
	page := string(RenderStatusHTML(BuildStatusShellPayload(time.Now())))
	markup := page
	if index := strings.Index(markup, "<script"); index >= 0 {
		markup = markup[:index]
	}
	for _, forbidden := range []string{
		`data-purpose="inventory-panel"`,
		`data-purpose="view-group-filter"`,
		`data-purpose="active-pool"`,
		`data-purpose="active-pool-preview"`,
		`id="bulkConfirmation"`,
		`id="activePoolConfirmation"`,
		`name="active_pool"`,
		`/annotations/groups/batch`,
		`/active-pool`,
	} {
		if strings.Contains(markup, forbidden) {
			t.Fatalf("management page exposes removed group control or write path %q", forbidden)
		}
	}
}

func TestManagementPageKeepsNonGroupAnnotationEditorsAndProtectedActions(t *testing.T) {
	page := string(RenderStatusHTML(BuildStatusShellPayload(time.Now())))
	for _, required := range []string{
		`id="editAlias"`,
		`id="editTags"`,
		`id="editNotes"`,
		`id="editSchedulerPriority"`,
		`requestManagement('/annotations/account'`,
		`requestManagement('/settings'`,
		`requestManagement('/export'`,
		`requestManagement('/import'`,
		`loadPublicStatus()`,
		`/v0/resource/plugins/codex-quota-scheduler/status-data`,
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("management page missing required contract %q", required)
		}
	}
	start := strings.Index(page, "async function saveAccountModal")
	if start >= 0 {
		end := strings.Index(page[start:], "\n")
		if end < 0 {
			end = len(page) - start
		}
		if strings.Contains(page[start:start+end], "group_id") || strings.Contains(page[start:start+end], "/annotations/group") {
			t.Fatal("account editor still submits plugin group fields")
		}
	}
}
