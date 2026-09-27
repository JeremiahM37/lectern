package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"io"
	"unicode/utf8"
)

type autoPrivateTestInput struct {
	CandidateSHA string                   `json:"candidate_tree_sha256"`
	Profile      string                   `json:"profile"`
	Script       string                   `json:"script"`
	Fixtures     []autoExpertProbeFixture `json:"fixtures"`
	Argv         []string                 `json:"argv"`
}

type autoPrivateTestRequest struct {
	SchemaVersion       int                      `json:"schema_version"`
	IntegrationID       string                   `json:"integration_id"`
	PinKey              string                   `json:"pin_key"`
	RootID              string                   `json:"root_id"`
	OwnerJob            string                   `json:"owner_job"`
	OwnerTask           int64                    `json:"owner_task"`
	ProjectID           int64                    `json:"project_id"`
	CandidateReceiptSHA string                   `json:"candidate_receipt_sha256"`
	CandidateSHA        string                   `json:"candidate_tree_sha256"`
	Profile             string                   `json:"profile"`
	Script              string                   `json:"script"`
	Fixtures            []autoExpertProbeFixture `json:"fixtures"`
	Argv                []string                 `json:"argv"`
	Runtime             autoExpertProbeRuntime   `json:"runtime"`
}

func autoPrivateTestOwner(a *autoRecord, jobID string) (*autoJob, *autoPrivateIntegrationAttempt, error) {
	if !a.Config.Enabled || a.State == nil || a.State.Phase != autonomy.Review {
		return nil, nil, errors.New("test execution requires enabled review phase")
	}
	for _, j := range a.Jobs {
		if j.ID != jobID || j.Role != "reviewer" || j.Status != "running" {
			continue
		}
		v, err := autoPrivateAttempt(a, j)
		if err != nil {
			return nil, nil, err
		}
		if v.ReviewerJob != j.ID || v.Status != "reviewing" || v.Candidate == nil || v.StopRequested || v.Cycle != a.State.Cycle || v.Revision != a.State.Revision || v.Item != a.State.Item {
			return nil, nil, errors.New("test owner does not match current candidate review")
		}
		for _, assignment := range a.State.Assignments {
			if assignment.TaskID == j.TaskID && assignment.Role == "reviewer" && !assignment.Completed {
				return j, v, nil
			}
		}
	}
	return nil, nil, errors.New("current independent reviewer unavailable")
}

func autoPrivateTestRequestBytes(a *autoRecord, j *autoJob, input *autoPrivateTestInput, testKey string) ([]byte, error) {
	v, err := autoPrivateAttempt(a, j)
	if err != nil {
		return nil, err
	}
	if v.Candidate == nil || input.CandidateSHA != v.Candidate.TreeSHA || !autoHash256(v.Candidate.ReceiptSHA) {
		return nil, errors.New("test requires the sealed admitted candidate")
	}
	runtime := autoExpertProbeRuntime{}
	if j.PythonUsedBundle != "" {
		if j.PythonRecovery == nil || j.PythonRecovery.State != "verified" || j.PythonRecovery.BundleKey != j.PythonUsedBundle {
			return nil, errors.New("reviewer runtime identity unavailable")
		}
		runtime.PythonBundle = j.PythonUsedBundle
		runtime.PythonInput = j.PythonRecovery.InputKey
		runtime.Browser = j.PythonRecovery.BrowserKey
	} else {
		runtime.PythonTest = testKey
	}
	if err := autoSelectGoTestRuntime(j, &runtime); err != nil {
		return nil, err
	}
	if err := autoSelectNodeTestRuntime(j, &runtime); err != nil {
		return nil, err
	}
	fixtures, argv := input.Fixtures, input.Argv
	if fixtures == nil {
		fixtures = []autoExpertProbeFixture{}
	}
	if argv == nil {
		argv = []string{}
	}
	request := autoPrivateTestRequest{SchemaVersion: 1, IntegrationID: v.ID, PinKey: v.PinKey, RootID: v.RootID, OwnerJob: j.ID, OwnerTask: j.TaskID, ProjectID: v.Authority.Pin.ProjectID, CandidateReceiptSHA: v.Candidate.ReceiptSHA, CandidateSHA: v.Candidate.TreeSHA, Profile: input.Profile, Script: input.Script, Fixtures: fixtures, Argv: argv, Runtime: runtime}
	raw, err := json.Marshal(request)
	if err == nil && len(raw) > autoExpertInputLimit {
		err = errors.New("sealed test request exceeds 1 MiB")
	}
	return raw, err
}

func autoDecodePrivateTestInput(reader io.Reader) (*autoPrivateTestInput, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, autoExpertInputLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > autoExpertInputLimit || !utf8.Valid(raw) {
		return nil, errors.New("test request exceeds 1 MiB or is not UTF-8")
	}
	var input autoPrivateTestInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("expected one test request")
	}
	if input.Profile == "" {
		input.Profile = "integration600"
	}
	if input.Profile != "integration600" {
		return nil, errors.New("test profile is not admitted")
	}
	// Share the probe payload limits and reserved entrypoint rules. This only
	// validates data; the actual sealed request retains integration600 authority.
	validation, _ := json.Marshal(autoExpertProbeInput{ProgressKey: input.CandidateSHA, Profile: "ordinary180", Script: input.Script, Fixtures: input.Fixtures, Argv: input.Argv})
	if _, err = autoDecodeExpertProbeInput(bytes.NewReader(validation)); err != nil {
		return nil, err
	}
	return &input, nil
}
