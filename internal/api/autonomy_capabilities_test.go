package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestExpertProbeCapabilityReportsAvailabilityAndEvidenceLimits(t *testing.T) {
	installed := map[string]any{"status": "installed"}
	missing := map[string]any{"status": "unavailable"}
	for _, pair := range [][2]map[string]any{{installed, missing}, {missing, installed}} {
		if autoExpertProbeCapability(pair[0], pair[1])["status"] != "unavailable" {
			t.Fatal("advertised missing capability")
		}
	}
	got := autoExpertProbeCapability(installed, installed)
	if got["status"] != "on_demand" {
		t.Fatal("installed mechanism undiscoverable")
	}
	raw, _ := json.Marshal(got)
	for _, text := range []string{"POST /expert-probes", "GET /expert-recovery", "same-assignment report corrections", "execution alone is not proof of progress", "Original acceptance", "No publication"} {
		if !strings.Contains(string(raw), text) {
			t.Fatal("missing practical contract", text)
		}
	}
}

func TestCapabilityDiscoveryDoesNotInventInstalledEnvironments(t *testing.T) {
	root := t.TempDir()
	got := autoCapabilityCatalog(root, filepath.Join(root, "missing-runner"), filepath.Join(root, "missing-helper"))
	rows := got["capabilities"].([]map[string]any)
	if rows[1]["capability"] != "python_wheels" || rows[1]["status"] != "unavailable" {
		t.Fatal(got)
	}
	// Even with installed executables, absent verified test tooling prevents
	// advertising a working Python resolver. No network call or install occurs.
	installed := map[string]any{"status": "installed", "sha256": strings.Repeat("b", 64)}
	got = autoCapabilityCatalogFromInstallation(root, installed, installed)
	rows = got["capabilities"].([]map[string]any)
	if rows[1]["status"] != "unavailable" || rows[2]["status"] != "on_demand" {
		t.Fatal(got)
	}
	key := strings.Repeat("a", 64)
	dir := filepath.Join(root, "python", key)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "python", "active.json"), []byte(`{"key":"`+key+`"}`), 0444)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"key":"`+key+`","kind":"python-test-runtime","runtime_family":"python3.13","checksum_verified":true,"packages":{"pytest":"9.1.1"}}`), 0444)
	got = autoCapabilityCatalogFromInstallation(root, installed, installed)
	rows = got["capabilities"].([]map[string]any)
	if rows[1]["status"] != "on_demand" {
		t.Fatal(got)
	}
	encoded, _ := json.Marshal(got)
	for _, term := range []string{"not that a requested package is available", "existing processes do not gain mounts retroactively", "does not change old verdicts", "No publication"} {
		if !strings.Contains(string(encoded), term) {
			t.Fatalf("missing distinction %q", term)
		}
	}
	if _, ok := rows[1]["bundle_key"]; ok {
		t.Fatal("catalog fabricated a delivered environment")
	}
}

func TestCapabilityHelperIdentityRejectsLinksAndUnsafeFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "helper")
	if err := os.Symlink("/usr/bin/true", p); err != nil {
		t.Fatal(err)
	}
	if autoCapabilityInstallation(p)["status"] != "unavailable" {
		t.Fatal("followed helper symlink")
	}
	os.Remove(p)
	os.WriteFile(p, []byte("not installed"), 0666)
	os.Chmod(p, 0666)
	if autoCapabilityInstallation(p)["status"] != "unavailable" {
		t.Fatal("trusted writable helper")
	}
}

func TestCapabilityBridgeRemainsReadOnly(t *testing.T) {
	s := autoTestServer(t)
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		w := httptest.NewRecorder()
		s.autoReadBridge(w, httptest.NewRequest(method, "/capabilities", strings.NewReader(`{"capability":"host_shell"}`)))
		if w.Code != 405 {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", "/capabilities", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"python_wheels"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestReviewedEnvironmentDiscoveryPagesWithoutPublishingUnverifiedRecipes(t *testing.T) {
	all := &autoRecord{RequirementDiagnoses: map[string]*autoRequirementDiagnosis{}}
	for i := int64(0); i < 51; i++ {
		a, b, r, _ := diagnosisEnvironmentFixture(t)
		d := a.RequirementDiagnoses["origin"]
		b.TaskID, r.TaskID = 1000+2*i, 1001+2*i
		b.ID, r.ID = fmt.Sprint("builder-", i), fmt.Sprint("reviewer-", i)
		b.ReviewTaskID = r.TaskID
		d.TaskID, d.ReviewTaskID = b.TaskID, r.TaskID
		d.VerifiedEnvironment.DiagnosisTaskID = b.TaskID
		d.VerifiedEnvironment.OriginTaskID = i + 1
		d.VerifiedEnvironment.BuilderJob, d.VerifiedEnvironment.ReviewerJob = b.ID, r.ID
		all.Jobs = append(all.Jobs, b, r)
		all.RequirementDiagnoses[fmt.Sprint(i)] = d
	}
	body, status := autoDiagnosisEnvironmentDiscovery(all, nil)
	page := body.(map[string]any)
	if status != 200 || len(page["items"].([]map[string]any)) != 50 || page["next_after"] != int64(1098) || page["total"] != 51 {
		t.Fatal(status, page)
	}
	body, status = autoDiagnosisEnvironmentDiscovery(all, url.Values{"after": {"1098"}})
	page = body.(map[string]any)
	rows := page["items"].([]map[string]any)
	if status != 200 || len(rows) != 1 || page["next_after"] != int64(0) || rows[0]["diagnosis_task_id"] != int64(1100) {
		t.Fatal(status, page)
	}
	body, status = autoDiagnosisEnvironmentDiscovery(all, url.Values{"diagnosis_task_id": {"1100"}})
	if status != 200 || body.(map[string]any)["environment"].(*autoVerifiedDiagnosisEnvironment).Receipt.BundleKey == "" {
		t.Fatal(status, body)
	}
	// The same catalog location must stop offering the environment when its
	// independent-use proof is no longer valid; historical metadata alone fails.
	all.Jobs[len(all.Jobs)-1].PythonUsedBundle = ""
	if _, status = autoDiagnosisEnvironmentDiscovery(all, url.Values{"diagnosis_task_id": {"1100"}}); status != 404 {
		t.Fatal("published unverified recipe")
	}
	for _, query := range []url.Values{{"after": {"0"}}, {"after": {"-1"}}, {"after": {"1", "2"}}, {"diagnosis_task_id": {"1000"}, "after": {"1"}}, {"command": {"install"}}} {
		if _, status = autoDiagnosisEnvironmentDiscovery(all, query); status != 400 {
			t.Fatal("accepted invalid query", query)
		}
	}
}

func TestCapabilityDiscoveryIsIncludedInEveryWorkshopRole(t *testing.T) {
	a := planEvidenceFixture()
	a.State.Items = []autonomy.Proposal{{ProjectID: 1, Title: "work", Acceptance: []string{"verify"}}}
	s := autoTestServer(t)
	for _, role := range []string{"planner", "auditor_a", "auditor_b", "builder", "reviewer"} {
		prompt := s.autoPrompt(context.Background(), a, role, &store.Project{})
		if !strings.Contains(prompt, "read /capabilities") || !strings.Contains(prompt, "does not mean project dependencies have no recovery path") {
			t.Fatal("missing capability discovery", role)
		}
	}
}
