package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func sourceFixture(t *testing.T) (*Server, *store.Project, string) {
	t.Helper()
	s := autoTestServer(t)
	target, err := s.DB.InsertTarget(&store.Target{Name: "local-source", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p, err := s.DB.InsertProject(&store.Project{Name: "source", TargetID: target.ID, RepoPath: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := autoGit(context.Background(), dir, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	commitSource(t, dir, "original")
	return s, p, dir
}

func commitSource(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "sample.txt"}, {"-c", "commit.gpgSign=false", "commit", "-m", "fixture"}} {
		if err := autoGit(context.Background(), dir, args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourcePinSurvivesHeadChangeAndExcludesUncommittedFiles(t *testing.T) {
	s, p, dir := sourceFixture(t)
	ctx := context.Background()
	row, err := s.autoSourceContext(ctx, strconv.FormatInt(p.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	items := []autonomy.Proposal{{ProjectID: p.ID, SourceRevision: row["source_revision"].(string)}}
	if err := s.pinAutoSources(ctx, &autoRecord{}, items); err != nil {
		t.Fatal(err)
	}
	// Simulate persistence between approval and launch.
	raw, _ := json.Marshal(items)
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	commitSource(t, dir, "later human commit")
	later, err := autoSourceRevision(ctx, dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// Repository replacement refs must not substitute different content for
	// the already-reviewed commit, either during resolution or archival.
	if err := autoGit(ctx, dir, "replace", items[0].SourceRevision, later); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("private draft"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("dirty edit"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := autoArchiveSource(ctx, dir, items[0].SourceRevision, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "sample.txt"))
	if err != nil || string(got) != "original" {
		t.Fatalf("wrong audited content: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "uncommitted.txt")); !os.IsNotExist(err) {
		t.Fatal("uncommitted file leaked")
	}
	// A newly submitted stale plan must revise, not silently select newer code.
	if err := s.pinAutoSources(ctx, &autoRecord{}, items); err == nil {
		t.Fatal("stale discovery accepted")
	}
}

func TestSourceBridgeIsReadOnlyAndDoesNotExposePaths(t *testing.T) {
	s, p, dir := sourceFixture(t)
	for _, tc := range []struct {
		method, query string
		code          int
	}{
		{"GET", strconv.FormatInt(p.ID, 10), 200}, {"POST", strconv.FormatInt(p.ID, 10), 405},
		{"GET", "-1", 400}, {"GET", "99999", 400}, {"GET", "../../etc", 400},
	} {
		w := httptest.NewRecorder()
		s.autoReadBridge(w, httptest.NewRequest(tc.method, "/source?project_id="+tc.query, nil))
		if w.Code != tc.code {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), dir) {
			t.Fatal("local path exposed")
		}
	}
}

func TestSourcePinLegacyReportsAndRejectsLineageMixing(t *testing.T) {
	s, p, dir := sourceFixture(t)
	items := []autonomy.Proposal{{ProjectID: p.ID}}
	if err := s.pinAutoSources(context.Background(), &autoRecord{}, items); err != nil {
		t.Fatal(err)
	}
	if !autoSourceHash(items[0].SourceRevision) {
		t.Fatal("missing pre-audit pin")
	}
	for _, rev := range []string{"HEAD", "--help", "abc", strings.Repeat("g", 40)} {
		if err := s.validateAutoSources(&autoRecord{}, []autonomy.Proposal{{ProjectID: p.ID, SourceRevision: rev}}); err == nil {
			t.Fatal("invalid revision accepted", rev)
		}
	}
	for _, p := range []autonomy.Proposal{
		{ProjectID: p.ID, SourceRevision: items[0].SourceRevision, ContinueTaskID: 1},
		{ProjectID: p.ID, SourceRevision: items[0].SourceRevision, RepairTaskID: 1},
	} {
		if err := s.validateAutoSources(&autoRecord{}, []autonomy.Proposal{p}); err == nil {
			t.Fatal("source replaced checkpoint lineage")
		}
	}
	if _, err := autoSourceRevision(context.Background(), dir, strings.Repeat("0", 40)); err == nil {
		t.Fatal("missing commit accepted")
	}
}

func TestSourcePinTravelsThroughBothAuditsAndAdmission(t *testing.T) {
	s, p, _ := sourceFixture(t)
	state, _ := autonomy.NewState("2026-09-25")
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state}
	a.Config.Enabled = true
	if err := state.RegisterTask("planner", 1); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(autonomy.PlanReport{Items: []autonomy.Proposal{{ProjectID: p.ID, Title: "Next milestone", Why: "Distinct migration corpus", Acceptance: []string{"New fixtures survive restore"}}}})
	if err := state.ApplyReport(a.Config, 1, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.pinAutoSources(context.Background(), a, state.Items); err != nil {
		t.Fatal(err)
	}
	pin := state.Items[0].SourceRevision
	job := &autoJob{ID: "bound-source", TaskID: 4, Role: "builder"}
	for i, role := range []string{"auditor_a", "auditor_b"} {
		if autoNewAdmission(a, job) != nil {
			t.Fatal("source availability bypassed plan audit")
		}
		if !strings.Contains(s.autoPrompt(context.Background(), a, role, p), pin) {
			t.Fatal("auditor did not see pinned source")
		}
		id := int64(i + 2)
		if err := state.RegisterTask(role, id); err != nil {
			t.Fatal(err)
		}
		if err := state.ApplyReport(a.Config, id, []byte(`{"approve":true,"reason":"Distinct milestone and reviewed source"}`)); err != nil {
			t.Fatal(err)
		}
	}
	job.Admission = autoNewAdmission(a, job)
	if job.Admission == nil || job.Admission.Proposal.SourceRevision != pin {
		t.Fatal("admission lost reviewed revision")
	}
	a.Jobs = append(a.Jobs, job)
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.autoJobReadBridge(job.ID)(w, httptest.NewRequest("GET", "/assignment", nil))
	var receipt autoAdmission
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || receipt.Proposal.SourceRevision != pin || len(receipt.PlanAudits) != 2 {
		t.Fatal("persisted admission lost source/audits", w.Body.String())
	}
}
