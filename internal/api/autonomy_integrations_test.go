package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func integrationFixture(t *testing.T) (*Server, *autoRecord, autoIntegrationInput, []byte, string) {
	s, p, dir := sourceFixture(t)
	task, err := s.DB.InsertTask(&store.Task{ProjectID: p.ID, Title: "retained work", Status: "review"})
	if err != nil {
		t.Fatal(err)
	}
	report := []byte(`{"outcome":"ready_for_review","summary":"source evidence"}`)
	sha := sha256.Sum256(report)
	revision, err := autoSourceRevision(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	a := &autoRecord{Config: autonomy.DefaultConfig(), Jobs: []*autoJob{{ID: "original-builder", TaskID: task.ID, Role: "builder", Status: "done", Rejected: true, Approved: false, RepairAttemptTaskID: 123}}}
	in := autoIntegrationInput{TaskID: task.ID, JobID: "original-builder", ProjectID: p.ID, Revision: revision, ReportSHA256: hex.EncodeToString(sha[:]), ScopeType: "partial", IntegratedScope: "identity fix adapted with stronger regression tests", RemainingScope: "preview still awaits repair and review", Evidence: []string{"private verification report"}}
	return s, a, in, report, dir
}

func TestIntegrationHistoricalRejectionAndUnfinishedScopeRemain(t *testing.T) {
	s, a, in, report, dir := integrationFixture(t)
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	original := s.DB.Setting(autoKey)
	receipt, err := s.validateAutoIntegration(context.Background(), a, in, report)
	if err != nil {
		t.Fatal(err)
	}
	row, created, err := s.recordAutoIntegration(receipt)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	duplicate, created, err := s.recordAutoIntegration(receipt)
	if err != nil || created || duplicate.ID != row.ID {
		t.Fatal("receipt is not idempotent")
	}
	if s.DB.Setting(autoKey) != original {
		t.Fatal("integration changed historical review/admission state")
	}
	if !a.Jobs[0].Rejected || a.Jobs[0].Approved || a.Jobs[0].RepairAttemptTaskID != 123 {
		t.Fatal("review or repair reset")
	}
	if row.RemainingScope == "" || !strings.Contains(row.CurrentPresence, "unknown") {
		t.Fatal("partial scope/revert limitation hidden")
	}
	commitSource(t, dir, "later change")
	// Revert-like content change leaves historical membership true, never proves
	// current presence. The immutable receipt captures the original HEAD only.
	commitSource(t, dir, "original")
	later, err := s.validateAutoIntegration(context.Background(), a, in, report)
	if err != nil {
		t.Fatal(err)
	}
	if later.ID != row.ID || later.CanonicalHead == row.CanonicalHead || !strings.Contains(later.CurrentPresence, "unknown") {
		t.Fatal("history interpreted as current presence")
	}
	rows, _ := s.autoIntegrations()
	if len(rows) != 1 || rows[0].CanonicalHead != row.CanonicalHead {
		t.Fatal("receipt overwritten")
	}
}

func TestIntegrationRejectsWrongProvenanceAndUnsupportedClaims(t *testing.T) {
	s, a, in, report, _ := integrationFixture(t)
	cases := map[string]func(*autoIntegrationInput){
		"wrong project":     func(p *autoIntegrationInput) { p.ProjectID++ },
		"wrong task":        func(p *autoIntegrationInput) { p.TaskID++ },
		"wrong job":         func(p *autoIntegrationInput) { p.JobID = "other" },
		"wrong report":      func(p *autoIntegrationInput) { p.ReportSHA256 = strings.Repeat("0", 64) },
		"missing commit":    func(p *autoIntegrationInput) { p.Revision = strings.Repeat("0", 40) },
		"short commit":      func(p *autoIntegrationInput) { p.Revision = p.Revision[:12] },
		"unsupported exact": func(p *autoIntegrationInput) { p.ScopeType = "exact" },
		"hidden remainder":  func(p *autoIntegrationInput) { p.RemainingScope = "" },
		"no evidence":       func(p *autoIntegrationInput) { p.Evidence = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := in
			change(&candidate)
			if _, err := s.validateAutoIntegration(context.Background(), a, candidate, report); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
	a.Jobs[0].Status = "running"
	if _, err := s.validateAutoIntegration(context.Background(), a, in, report); err == nil {
		t.Fatal("live work accepted")
	}
}

func TestIntegrationBridgeRefusesWritesAndUntrustedAPI(t *testing.T) {
	s := autoTestServer(t)
	for _, handler := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) {
			s.autoReadBridge(w, httptest.NewRequest("POST", "/integrations", strings.NewReader(`{}`)))
		},
		func(w *httptest.ResponseRecorder) {
			s.postAutoIntegration(w, httptest.NewRequest("POST", "/api/autonomy/integrations", strings.NewReader(`{}`)))
		},
	} {
		w := httptest.NewRecorder()
		handler(w)
		if w.Code != 403 && w.Code != 405 {
			t.Fatal(w.Code)
		}
	}
}

func TestIntegrationRefusesUnreachableCommitAndUnavailableRepository(t *testing.T) {
	s, a, in, report, dir := integrationFixture(t)
	if err := autoGit(context.Background(), dir, "checkout", "--orphan", "different-history"); err != nil {
		t.Fatal(err)
	}
	commitSource(t, dir, "unrelated commit")
	if _, err := s.validateAutoIntegration(context.Background(), a, in, report); err == nil {
		t.Fatal("unreachable commit accepted")
	}
	if err := os.Rename(dir, dir+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(dir+"-unavailable", dir)
	if _, err := s.validateAutoIntegration(context.Background(), a, in, report); err == nil {
		t.Fatal("unavailable canonical repository accepted")
	}
}

func TestIntegrationAuthorizationSeparatesLocalHumanAndService(t *testing.T) {
	s := autoTestServer(t)
	for _, tc := range []struct {
		name string
		p    auth.Principal
		want int
	}{
		{"local integrator", auth.Principal{Kind: auth.KindLocal}, 400},
		{"authorized human", auth.Principal{Kind: auth.KindTailscale, Human: true, Login: "owner@example.test"}, 400},
		{"service principal", auth.Principal{Kind: auth.KindTailscale, Human: false, Node: "tagged-worker"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/autonomy/integrations", strings.NewReader(`{}`))
			r = r.WithContext(auth.WithPrincipal(r.Context(), tc.p))
			w := httptest.NewRecorder()
			s.postAutoIntegration(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}

func TestIntegrationConcurrentDuplicatesAppendOnce(t *testing.T) {
	s, a, in, report, _ := integrationFixture(t)
	receipt, err := s.validateAutoIntegration(context.Background(), a, in, report)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, e := s.recordAutoIntegration(receipt); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	rows, err := s.autoIntegrations()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}

func TestIntegrationBridgeExposesReceiptsWithoutChangingReview(t *testing.T) {
	s, a, in, report, _ := integrationFixture(t)
	receipt, err := s.validateAutoIntegration(context.Background(), a, in, report)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.recordAutoIntegration(receipt); err != nil {
		t.Fatal(err)
	}
	if err = s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", "/integrations", nil))
	var rows []autoIntegration
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 || rows[0].RemainingScope != in.RemainingScope {
		t.Fatal(w.Code, w.Body.String())
	}
	// An integrated rejected task does not enter the approved artifact catalog.
	w = httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", "/artifacts", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("integration laundered approval", w.Body.String())
	}
	a.Jobs[0].Rejected = false
	a.Jobs[0].Approved = true
	a.Jobs[0].ReviewTaskID = 987
	a.Jobs = append(a.Jobs, &autoJob{TaskID: 987, Role: "reviewer", Status: "done"})
	if err = s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", "/artifacts", nil))
	var artifacts []map[string]json.RawMessage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &artifacts) != nil || len(artifacts) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if json.Unmarshal(artifacts[0]["private_integrations"], &rows) != nil || len(rows) != 1 || rows[0].ID != receipt.ID {
		t.Fatal("missing integration annotation")
	}
}
