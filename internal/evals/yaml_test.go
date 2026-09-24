package evals

import "testing"

const validSuiteYAML = `
name: Health endpoint suite
description: Checks the agent can add a working health endpoint
cases:
  - name: add health endpoint
    prompt: 'Add a /health endpoint returning {"ok": true}'
    base_ref: main
    check_command: pytest tests/test_health.py
    timeout_s: 600
  - name: handle missing route gracefully
    prompt: Make unknown routes return a 404 with a JSON body
    check_command: pytest tests/test_404.py
`

func TestParseSuiteAcceptsAWellFormedFile(t *testing.T) {
	s, err := ParseSuite([]byte(validSuiteYAML))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Health endpoint suite" || len(s.Cases) != 2 {
		t.Fatalf("parsed: %+v", s)
	}
	if s.Cases[0].TimeoutS != 600 {
		t.Errorf("case 0 timeout: %v", s.Cases[0].TimeoutS)
	}
	if s.Cases[1].BaseRef != "" {
		t.Errorf("case 1 base_ref should default empty, got %q", s.Cases[1].BaseRef)
	}
}

func TestParseSuiteRejectsMissingName(t *testing.T) {
	_, err := ParseSuite([]byte(`cases: [{name: x, prompt: y}]`))
	if err == nil {
		t.Fatal("expected an error for a nameless suite")
	}
}

func TestParseSuiteRejectsNoCases(t *testing.T) {
	_, err := ParseSuite([]byte(`name: empty`))
	if err == nil {
		t.Fatal("expected an error for a suite with no cases")
	}
}

func TestParseSuiteRejectsCaseMissingPrompt(t *testing.T) {
	_, err := ParseSuite([]byte("name: s\ncases:\n  - name: no prompt\n"))
	if err == nil {
		t.Fatal("expected an error for a case with no prompt")
	}
}

func TestParseSuiteRejectsInvalidYAML(t *testing.T) {
	_, err := ParseSuite([]byte("name: [unterminated"))
	if err == nil {
		t.Fatal("expected a YAML parse error")
	}
}
