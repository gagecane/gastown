package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

// TestStaticDashboardModulesServed verifies every expected dashboard JS module
// is reachable through the /static/dashboard/ endpoint. This protects against
// a file being dropped from the tree (or the embed directive breaking) during
// the refactor from a single monolithic dashboard.js to modular scripts.
func TestStaticDashboardModulesServed(t *testing.T) {
	mock := &MockConvoyFetcher{}
	webCfg := &config.WebTimeoutsConfig{}
	handler, err := NewDashboardMux(mock, webCfg)
	if err != nil {
		t.Fatalf("NewDashboardMux: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	modules := []string{
		"00-core.js", "01-panels.js", "02-palette.js", "03-mail.js",
		"04-crew.js", "05-hook.js", "06-issue-modal.js", "07-work-tabs.js",
		"08-ready.js", "09-convoy.js", "10-issue-panel.js", "11-pr.js",
		"12-sling.js", "13-escalation.js", "14-activity.js",
		"15-session.js", "16-convoy-drill.js",
	}
	for _, m := range modules {
		url := server.URL + "/static/dashboard/" + m
		resp, err := http.Get(url)
		if err != nil {
			t.Errorf("GET %s: %v", url, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d", url, resp.StatusCode)
			continue
		}
		if !strings.Contains(string(body), "'use strict'") {
			t.Errorf("module %s missing 'use strict'", m)
		}
	}
}
