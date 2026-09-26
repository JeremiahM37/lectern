package sessions

import (
	"strings"
	"testing"
)

// TestCapabilitiesDegradeWithAReason is the contract every picker relies on:
// a missing capability must come back Available:false with a reason naming
// both the feature and the agent, so a UI can render "Resume isn't available
// for Aider" instead of a disabled control nobody can explain.
func TestCapabilitiesDegradeWithAReason(t *testing.T) {
	bare := Spec{Name: "bare-agent", Command: "bare"}
	caps := bare.Capabilities()
	for _, cap := range []Capability{CapResume, CapForkTo, CapModel, CapModelsList, CapYolo, CapACP, CapTask} {
		state, ok := caps[cap]
		if !ok {
			t.Fatalf("Capabilities() did not report %q at all", cap)
		}
		if state.Available {
			t.Errorf("%q should not be available on a bare spec with no supporting fields", cap)
		}
		if state.Reason == "" {
			t.Errorf("%q has no reason text for a disabled capability", cap)
		}
		if !strings.Contains(state.Reason, "bare-agent") {
			t.Errorf("%q reason %q does not name the agent", cap, state.Reason)
		}
	}
	if !strings.Contains(caps[CapResume].Reason, "Resume") {
		t.Errorf("resume reason should name the feature: %q", caps[CapResume].Reason)
	}
}

// TestCapabilitiesAvailableWhenFieldsPopulated proves the inverse: every
// field that would make a Spec.LaunchCommand actually use a feature also
// flips its capability to Available with no reason text.
func TestCapabilitiesAvailableWhenFieldsPopulated(t *testing.T) {
	full := Spec{
		Name: "full-agent", Command: "full",
		ResumeArgs: []string{"--continue"}, ForkArgs: []string{"--fork", "{id}"},
		ModelFlag: "--model", ModelsCommand: "{bin} models",
		YoloArgs: []string{"--yolo"},
	}
	caps := full.Capabilities()
	for _, cap := range []Capability{CapResume, CapForkTo, CapModel, CapModelsList, CapYolo} {
		state := caps[cap]
		if !state.Available {
			t.Errorf("%q should be available, got reason %q", cap, state.Reason)
		}
		if state.Reason != "" {
			t.Errorf("%q available capability should carry no reason, got %q", cap, state.Reason)
		}
	}
	// This spec has neither Task nor ACP, so background work is genuinely
	// unavailable — not every field populated implies every capability.
	if caps[CapTask].Available {
		t.Error("CapTask should not be available with no Task or ACP configured")
	}
	if caps[CapACP].Available {
		t.Error("CapACP should not be available with no ACP configured")
	}
}

// TestCapabilitiesTaskAvailableViaEitherBackend checks CapTask treats Task
// and ACP as interchangeable for "can this agent do headless work at all" —
// a picker offering best-of-N/routines/evals only needs that one bit, not
// which backend implements it.
func TestCapabilitiesTaskAvailableViaEitherBackend(t *testing.T) {
	viaTask := Spec{Name: "t", Command: "t", Task: &TaskSpec{PromptTemplate: "{prompt}"}}
	if !viaTask.Capabilities()[CapTask].Available {
		t.Error("a Task-backed spec should report CapTask available")
	}
	viaACP := Spec{Name: "a", Command: "a", ACP: &ACPSpec{Command: "a"}}
	if !viaACP.Capabilities()[CapTask].Available {
		t.Error("an ACP-backed spec should report CapTask available")
	}
	if !viaACP.Capabilities()[CapACP].Available {
		t.Error("an ACP-backed spec should report CapACP available")
	}
	if viaTask.Capabilities()[CapACP].Available {
		t.Error("a plain Task-backed spec should not report CapACP available")
	}
}

// TestBuiltinCapabilities pins the current, known-correct capability shape
// of the three built-ins, so a future edit to Builtins() that accidentally
// drops a flag (e.g. claude's ForkArgs) is caught here instead of only in a
// picker at runtime.
func TestBuiltinCapabilities(t *testing.T) {
	claude, _ := Find(Builtins(), "claude")
	cc := claude.Capabilities()
	for _, cap := range []Capability{CapResume, CapForkTo, CapModel, CapYolo, CapTask} {
		if !cc[cap].Available {
			t.Errorf("claude: expected %q available", cap)
		}
	}
	if cc[CapACP].Available {
		t.Error("claude: ACP should not be available (it is Task-backed via internal/agents, not sessions.Spec.ACP)")
	}

	gemini, _ := Find(Builtins(), "gemini")
	gc := gemini.Capabilities()
	if gc[CapResume].Available {
		t.Error("gemini: resume is not exposed as a CLI flag, should not be available")
	}
	if !gc[CapModel].Available || !gc[CapYolo].Available {
		t.Error("gemini: model and yolo should be available (-m, --yolo)")
	}
}
