package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func TestExpertProbeStopsBoundedAndFair(t *testing.T) {
	s, a, _, _ := expertFixture(t)
	response := documentationRunner(t)
	docResponse(t, response, map[string]string{"state": "stopping"})
	ledger := autoExpertLedger(a)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("%064x", i+1)
		ledger.Probes[id] = &autoExpertProbeLease{ID: id, OwnerJob: autoUUID(), Receipt: &autoExpertProbeReceipt{State: "exited"}}
	}
	active := strings.Repeat("f", 64)
	ledger.Probes[active] = &autoExpertProbeLease{ID: active, OwnerJob: autoUUID()}
	_ = s.stopAutoExpertProbes(context.Background(), a)
	count := 0
	for _, lease := range ledger.Probes {
		if lease.StopRequestedAt.IsZero() {
			t.Fatal("unattempted cancellation intent lost")
		}
		count += lease.StopAttempts
	}
	if count != 4 || ledger.Probes[active].StopAttempts != 1 {
		t.Fatal("unbounded stop batch or active cancellation delayed", count)
	}
	// Reload must retain fairness even when every stop remains unconfirmed.
	loaded, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_ = s.stopAutoExpertProbes(context.Background(), loaded)
	}
	for _, lease := range loaded.ExpertRecovery.Probes {
		if lease.StopAttempts == 0 {
			t.Fatal("early failed stops starved later helpers")
		}
		if lease.StopConfirmed {
			t.Fatal("stopping treated as terminal")
		}
	}
}

func TestExpertProbeInputBoundaries(t *testing.T) {
	good := autoExpertProbeInput{ProgressKey: strings.Repeat("a", 64), Script: "print('probe')", Fixtures: []autoExpertProbeFixture{{Path: "nested/input.bin", Content: base64.StdEncoding.EncodeToString([]byte{0, 255, 1})}}, Argv: []string{"--case", "EOF"}}
	for _, tc := range []struct {
		name   string
		change func(*autoExpertProbeInput)
	}{
		{"parent", func(p *autoExpertProbeInput) { p.Fixtures[0].Path = ".." }},
		{"escape", func(p *autoExpertProbeInput) { p.Fixtures[0].Path = "../host" }},
		{"absolute", func(p *autoExpertProbeInput) { p.Fixtures[0].Path = "/etc/passwd" }},
		{"normalization", func(p *autoExpertProbeInput) { p.Fixtures[0].Path = "a/../b" }},
		{"backslash", func(p *autoExpertProbeInput) { p.Fixtures[0].Path = "a\\b" }},
		{"duplicate", func(p *autoExpertProbeInput) { p.Fixtures = append(p.Fixtures, p.Fixtures[0]) }},
		{"reserved entrypoint", func(p *autoExpertProbeInput) { p.Fixtures[0].Path = "main.py" }},
		{"entrypoint directory", func(p *autoExpertProbeInput) { p.Fixtures[0].Path = "main.py/child" }},
		{"empty script", func(p *autoExpertProbeInput) { p.Script = " \n\t" }},
		{"aggregate arguments", func(p *autoExpertProbeInput) {
			p.Argv = make([]string, 9)
			for i := range p.Argv {
				p.Argv[i] = strings.Repeat("x", 2048)
			}
		}},
		{"parent collision", func(p *autoExpertProbeInput) {
			p.Fixtures = append(p.Fixtures, autoExpertProbeFixture{Path: "nested", Content: ""})
		}},
		{"base64 newline", func(p *autoExpertProbeInput) { p.Fixtures[0].Content += "\n" }},
		{"oversized fixtures", func(p *autoExpertProbeInput) {
			p.Fixtures[0].Content = base64.StdEncoding.EncodeToString(make([]byte, (512<<10)+1))
		}},
		{"oversized script", func(p *autoExpertProbeInput) { p.Script = strings.Repeat("x", (128<<10)+1) }},
		{"NUL argument", func(p *autoExpertProbeInput) { p.Argv = []string{"\x00"} }},
		{"unsupported profile", func(p *autoExpertProbeInput) { p.Profile = "unlimited" }},
		{"unbound key", func(p *autoExpertProbeInput) { p.ProgressKey = "invented" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := good
			p.Fixtures = append([]autoExpertProbeFixture(nil), good.Fixtures...)
			tc.change(&p)
			raw, _ := json.Marshal(p)
			if _, err := autoDecodeExpertProbeInput(strings.NewReader(string(raw))); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	raw, _ := json.Marshal(good)
	got, err := autoDecodeExpertProbeInput(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "ordinary180" || got.Fixtures[0].Content != good.Fixtures[0].Content {
		t.Fatal("valid input changed")
	}
	for _, suffix := range []string{`,"owner_job":"forged"}`, `,"source_job":"forged"}`, `,"runtime":{}}`} {
		forged := strings.TrimSuffix(string(raw), "}") + suffix
		if _, err := autoDecodeExpertProbeInput(strings.NewReader(forged)); err == nil {
			t.Fatal("worker authority accepted", suffix)
		}
	}
	for _, body := range []string{string(raw) + string(raw), strings.Repeat(" ", autoExpertInputLimit+1), "\xff"} {
		if _, err := autoDecodeExpertProbeInput(strings.NewReader(body)); err == nil {
			t.Fatal("invalid framing accepted")
		}
	}
}

func TestExpertPlannerPinOwnership(t *testing.T) {
	a := &autoRecord{Config: autonomy.Config{Enabled: true}, State: &autonomy.State{Phase: autonomy.Plan, Revision: 2, Assignments: []autonomy.Assignment{{TaskID: 7, Role: "planner", Round: 2}}}, Jobs: []*autoJob{{ID: "owner", TaskID: 7, Role: "planner", Status: "running"}}}
	if _, e := autoExpertPlannerOwner(a, "owner"); e != nil {
		t.Fatal(e)
	}
	if _, e := autoExpertPlannerOwner(a, "another"); e == nil {
		t.Fatal("cross-worker pin accepted")
	}
	a.State.Assignments[0].Round = 1
	if _, e := autoExpertPlannerOwner(a, "owner"); e == nil {
		t.Fatal("stale revision accepted")
	}
	a.State.Assignments[0].Round = 2
	a.State.Assignments[0].Completed = true
	if _, e := autoExpertPlannerOwner(a, "owner"); e == nil {
		t.Fatal("completed owner accepted")
	}
	a.State.Assignments[0].Completed = false
	a.Config.Enabled = false
	if _, e := autoExpertPlannerOwner(a, "owner"); e == nil {
		t.Fatal("OFF accepted")
	}
	a.Config.Enabled = true
	a.State.Phase = autonomy.Build
	if _, e := autoExpertPlannerOwner(a, "owner"); e == nil {
		t.Fatal("builder phase accepted")
	}
}

func TestExpertCatalogBoundsHistoryAndSelectors(t *testing.T) {
	a := &autoRecord{}
	ledger := autoExpertLedger(a)
	for i := 1; i <= 27; i++ {
		key := fmt.Sprintf("%064x", i)
		ledger.Pins[key] = &autoExpertRecoveryPin{Key: key, RootTaskID: int64(i), SourceTaskID: int64(i + 100), ProjectID: 1, AcceptanceSHA: strings.Repeat("b", 64)}
	}
	key := fmt.Sprintf("%064x", 1)
	for i := 1; i <= 7; i++ {
		ledger.Attempts[1] = append(ledger.Attempts[1], &autoExpertRecoveryAttempt{Number: i, ReviewReason: "preserved historical finding"})
	}
	value, status := autoExpertDiscovery(a, url.Values{})
	if status != 200 {
		t.Fatal(value)
	}
	index := value.(map[string]any)
	rows := index["items"].([]map[string]any)
	if len(rows) != 25 || index["next_after"] == "" {
		t.Fatal("unbounded or missing index pagination")
	}
	if _, exists := rows[0]["attempts"]; exists {
		t.Fatal("index leaked full history")
	}
	value, status = autoExpertDiscovery(a, url.Values{"after": {index["next_after"].(string)}})
	if status != 200 || len(value.(map[string]any)["items"].([]map[string]any)) != 2 {
		t.Fatal("lost index entries")
	}
	value, status = autoExpertDiscovery(a, url.Values{"key": {key}})
	if status != 200 {
		t.Fatal(value)
	}
	detail := value.(map[string]any)
	if len(detail["attempts"].([]*autoExpertRecoveryAttempt)) != 5 || detail["next_after_attempt"] != 5 || detail["prior_attempt"] != 7 {
		t.Fatal("history pagination or prior identity lost")
	}
	value, status = autoExpertDiscovery(a, url.Values{"key": {key}, "after_attempt": {"5"}})
	if status != 200 || len(value.(map[string]any)["attempts"].([]*autoExpertRecoveryAttempt)) != 2 {
		t.Fatal("history omitted")
	}
	for _, q := range []url.Values{{"key": {key, key}}, {"key": {key}, "after": {key}}, {"after_attempt": {"1"}}, {"key": {key}, "after_attempt": {"-1"}}, {"key": {"../../escape"}}} {
		if _, status = autoExpertDiscovery(a, q); status != 400 {
			t.Fatal("ambiguous selector accepted", q)
		}
	}
}
