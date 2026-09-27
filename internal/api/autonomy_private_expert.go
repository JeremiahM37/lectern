package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"time"
)

// Independent causal investigation is available before build admission, without
// treating integration corrections as newly granted ordinary repair attempts.
func (s *Server) pinAutoExpertPrivate(ctx context.Context, a *autoRecord, project, source int64) (*autoExpertRecoveryPin, error) {
	j, err := s.autoContinuation(a, project, source)
	if err != nil {
		return nil, err
	}
	v, err := autoPrivateAttempt(a, j)
	if err != nil {
		return nil, err
	}
	run := a.PrivateIntegration.Runs[v.RootID]
	if v.Status != "rejected" || v.Review == nil || v.Review.Approved || v.Candidate == nil || v.TaskID != source || v.Number != len(run.Attempts) {
		return nil, errors.New("expert private recovery requires latest independently rejected sealed candidate")
	}
	count := 0
	for _, prior := range run.Attempts {
		if prior.Authority.Pin.BaseTree == v.Authority.Pin.BaseTree {
			count++
		}
	}
	if count <= a.Config.MaxRevisionRounds {
		return nil, errors.New("private integration ordinary corrections are not exhausted")
	}
	rid, _, ok := autoRejectedCheckpoint(a, source)
	if !ok || rid != v.ReviewerTaskID || v.Review.TaskID != rid {
		return nil, errors.New("private recovery final rejection identity mismatch")
	}
	review := autoFindJob(a, rid)
	if review == nil {
		return nil, errors.New("private rejection reviewer missing")
	}
	sourceSHA, err := s.autoArchiveIdentity(ctx, j)
	if err != nil {
		return nil, err
	}
	reviewSHA, err := s.autoArchiveIdentity(ctx, review)
	if err != nil {
		return nil, err
	}
	if sourceSHA != v.Candidate.BuilderArchiveSHA || reviewSHA != v.Review.ArchiveSHA {
		return nil, errors.New("private rejected candidate/archive binding changed")
	}
	rootTask := run.Attempts[0].TaskID
	if rootTask <= 0 {
		return nil, errors.New("private root admission missing")
	}
	original, err := autoTaskAcceptance(a, rootTask)
	if err != nil {
		return nil, err
	}
	selected, err := autoTaskAcceptance(a, source)
	if err != nil {
		return nil, err
	}
	// Repin destination reality before audits. The retained rejected candidate is
	// evidence, never permission to overwrite an advanced managed destination.
	old := v.Authority.Pin
	p := autonomy.Proposal{ProjectID: project, IntegrationTaskID: old.SourceTaskID, IntegrationPaths: append([]string(nil), old.Paths...), Acceptance: append([]string(nil), old.Acceptance...)}
	items := []autonomy.Proposal{p}
	if err = s.pinAutoPrivateIntegrations(ctx, a, items); err != nil {
		return nil, err
	}
	base := a.PrivateIntegration.Pins[items[0].IntegrationPin]
	if base == nil || base.RootID != run.RootID || base.SourceJob != old.SourceJob || base.SourceSHA != old.SourceSHA {
		return nil, errors.New("private recovery original approved input changed")
	}
	pin := &autoExpertRecoveryPin{PrivateIntegration: base, PrivateSourceAttemptID: v.ID, PrivateCandidate: v.Candidate, RootTaskID: rootTask, ProjectID: project, SourceTaskID: source, ReviewTaskID: rid, SourceJob: j.ID, ReviewJob: review.ID, SourceSHA: sourceSHA, ReviewSHA: reviewSHA, Acceptance: original, SourceAcceptance: selected, AcceptanceSHA: autoSHA([]byte(store.J(original))), SourceAcceptanceSHA: autoSHA([]byte(store.J(selected)))}
	if e := autoPinExpertNodeSource(pin, j); e != nil {
		return nil, e
	}
	pin.Key = autoExpertPinKey(pin)
	raw, _ := json.Marshal(pin)
	var frozen autoExpertRecoveryPin
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	autoExpertLedger(a).Pins[frozen.Key] = &frozen
	return &frozen, nil
}
func autoValidateExpertPrivatePin(a *autoRecord, pin *autoExpertRecoveryPin) error {
	if pin.PrivateIntegration == nil {
		if pin.PrivateSourceAttemptID != "" || pin.PrivateCandidate != nil {
			return errors.New("partial private expert authority")
		}
		return nil
	}
	base := pin.PrivateIntegration
	if e := autoValidatePrivatePrior(a, base); e != nil {
		return e
	}
	if a.PrivateIntegration == nil || base.Key != autoPrivatePinKey(*base) || base.ProjectID != pin.ProjectID {
		return errors.New("private expert pin invalid")
	}
	run := a.PrivateIntegration.Runs[base.RootID]
	if run == nil || len(run.Attempts) == 0 || run.Attempts[0].TaskID != pin.RootTaskID {
		return errors.New("private expert root changed")
	}
	var source *autoPrivateIntegrationAttempt
	for _, v := range run.Attempts {
		if v.ID == pin.PrivateSourceAttemptID {
			source = v
		}
	}
	if source == nil || source.TaskID != pin.SourceTaskID || source.Status != "rejected" || source.Candidate == nil || source.Review == nil || source.Review.Approved || source.Review.TaskID != pin.ReviewTaskID || source.Candidate.BuilderArchiveSHA != pin.SourceSHA || source.Review.ArchiveSHA != pin.ReviewSHA || store.J(source.Candidate) != store.J(pin.PrivateCandidate) || source.Authority.Pin.RootID != base.RootID || source.Authority.Pin.SourceSHA != base.SourceSHA || store.J(source.Authority.Pin.Paths) != store.J(base.Paths) || store.J(source.Authority.Pin.Acceptance) != store.J(base.Acceptance) {
		return errors.New("private expert rejected candidate or authority changed")
	}
	return nil
}

// Called after expert reservation and before any worker effects; caller persists
// both ledgers together. Repeated calls resolve the same dual lease. All causal
// evidence, quota, cooldown and rate gates are still owned by expert admission.
func autoReserveExpertPrivateIntegration(a *autoRecord, p autonomy.Proposal, expert *autoExpertRecoveryAttempt) (*autoPrivateIntegrationAttempt, error) {
	pin, err := autoExpertPin(a, p)
	if err != nil {
		return nil, err
	}
	if pin.PrivateIntegration == nil {
		return nil, nil
	}
	if expert == nil || expert.ProgressKey != pin.Key || expert.RootTaskID != pin.RootTaskID || expert.Status != "reserved" || expert.Cycle != a.State.Cycle || expert.Revision != a.State.Revision || expert.Item != a.State.Item || store.J(expert.Proposal) != store.J(p) || !autoRepairAudited(a) {
		return nil, errors.New("private recovery requires exact admitted expert lease")
	}
	exists := false
	for _, v := range a.ExpertRecovery.Attempts[expert.RootTaskID] {
		if v == expert || store.J(v) == store.J(expert) {
			exists = true
		}
	}
	if !exists {
		return nil, errors.New("expert lease not durable in controller ledger")
	}
	run := a.PrivateIntegration.Runs[pin.PrivateIntegration.RootID]
	for _, v := range run.Attempts {
		if v.ExpertLeaseKey == expert.LeaseKey {
			if v.JobID != expert.JobID {
				return nil, errors.New("private expert lease already owns another UUID")
			}
			return v, nil
		}
		if !autoPrivateAttemptTerminal(v.Status) {
			return nil, errors.New("private integration has a competing active owner")
		}
	}
	n := len(run.Attempts) + 1
	v := &autoPrivateIntegrationAttempt{ID: autoSHA([]byte(fmt.Sprintf("%s:%d:%s", run.RootID, n, pin.PrivateIntegration.Key))), RootID: run.RootID, Number: n, PinKey: pin.PrivateIntegration.Key, Status: "reserved", Cycle: a.State.Cycle, Revision: a.State.Revision, Item: a.State.Item, Proposal: p, JobID: expert.JobID, CreatedAt: time.Now(), Generation: 1, ExpertLeaseKey: expert.LeaseKey, ExpertRootTaskID: expert.RootTaskID, ExpertAttempt: expert.Number, Authority: autoPrivateIntegrationAuthority{Pin: *pin.PrivateIntegration, Audits: a.State.Audits, Scope: "Progress-gated expert correction of retained private integration; original scope and source acceptance remain binding. Exact combined candidate must pass independent execution and review before managed private publication. No canonical or public authority."}}
	raw, _ := json.Marshal(v)
	var frozen autoPrivateIntegrationAttempt
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	run.Attempts = append(run.Attempts, &frozen)
	return &frozen, nil
}
func autoPrivateAttemptTerminal(status string) bool {
	return status == "rejected" || status == "unavailable" || status == "budget_exhausted" || status == "published_private" || status == "base_advanced"
}

// Same acceptance can fail anew after an actually changed managed base. Require
// immutable publication ancestry, not a timestamp, new title, or source log hash.
func autoPrivateLaterRejectedBase(a *autoRecord, pin *autoExpertRecoveryPin, approved *autoExpertRecoveryAttempt) bool {
	if approved == nil || approved.Status != "approved" || pin.PrivateIntegration == nil || pin.PrivateCandidate == nil || a.PrivateIntegration == nil {
		return false
	}
	source := autoFindJob(a, pin.SourceTaskID)
	prior := autoFindJob(a, approved.TaskID)
	if source == nil || prior == nil {
		return false
	}
	failed, e := autoPrivateAttempt(a, source)
	if e != nil || failed.Status != "rejected" || failed.Review == nil || failed.Review.TaskID <= approved.ReviewTaskID {
		return false
	}
	success, e := autoPrivateAttempt(a, prior)
	if e != nil || success.RootID != failed.RootID || success.Publication == nil {
		return false
	}
	published, e := autoPrivateIntegrationSource(a, success.Publication.ID, pin.ProjectID)
	if e != nil || failed.Authority.Pin.BaseTree == published.GitTree {
		return false
	}
	current := failed.Authority.Pin.BasePublicationID
	seen := map[string]bool{}
	for current != "" && !seen[current] {
		seen[current] = true
		entry, e := autoPrivateIntegrationSource(a, current, pin.ProjectID)
		if e != nil {
			return false
		}
		if entry.ID == published.ID {
			return true
		}
		var parent string
		for _, run := range a.PrivateIntegration.Runs {
			for _, v := range run.Attempts {
				if v.ID == current {
					parent = v.Authority.Pin.BasePublicationID
				}
			}
		}
		current = parent
	}
	return false
}

func autoPrivateExpertInvestigationCurrent(a *autoRecord, pin *autoExpertRecoveryPin) error {
	if pin.PrivateIntegration == nil {
		return nil
	}
	run := a.PrivateIntegration.Runs[pin.PrivateIntegration.RootID]
	if run == nil || len(run.Attempts) == 0 || run.Attempts[len(run.Attempts)-1].ID != pin.PrivateSourceAttemptID {
		return errors.New("private integration has a newer retained attempt; investigate its actual latest rejection")
	}
	return nil
}
