package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"sort"
	"strconv"
	"time"
)

// Historical sources may lack a delivered-toolchain receipt. This is a NEW
// experiment environment, frozen before the probe, never a historical claim.
type autoGoProbeRuntime struct {
	SourceJob     string                     `json:"source_job"`
	SourceSHA     string                     `json:"source_archive_sha256"`
	DependencyKey string                     `json:"go_dependency_key"`
	Generation    int                        `json:"generation"`
	StopAttempts  int                        `json:"stop_attempts,omitempty"`
	StopRequested bool                       `json:"stop_requested,omitempty"`
	Stopped       bool                       `json:"stopped,omitempty"`
	Receipt       *autoGoProbeRuntimeReceipt `json:"receipt,omitempty"`
}
type autoGoProbeRuntimeReceipt struct {
	autoGoRuntimeReceipt
	SourceSHA  string `json:"source_archive_sha256"`
	Generation int    `json:"generation"`
	Provenance string `json:"provenance"`
}

func autoGoProbeSource(a *autoRecord, pin *autoExpertRecoveryPin) *autoJob {
	for _, j := range a.Jobs {
		if j.ID == pin.SourceJob && j.TaskID == pin.SourceTaskID {
			return j
		}
	}
	return nil
}
func autoGoProbeIdentity(pin *autoExpertRecoveryPin, key string) string {
	return autoSHA([]byte(pin.SourceJob + ":" + pin.SourceSHA + ":" + key))
}
func autoUseExpertGoRuntime(a *autoRecord, pin *autoExpertRecoveryPin, r *autoExpertProbeRuntime) error {
	source := autoGoProbeSource(a, pin)
	if source == nil || source.Recovery == nil || source.Recovery.State != "verified" {
		return nil
	}
	if autoGoRuntimeValid(source.GoRuntime) {
		return autoSelectGoTestRuntime(source, r)
	}
	x := a.GoProbeRuntimes[autoGoProbeIdentity(pin, source.Recovery.Key)]
	if x == nil || x.SourceJob != pin.SourceJob || x.SourceSHA != pin.SourceSHA || x.DependencyKey != source.Recovery.Key || x.StopRequested || x.Receipt == nil || !autoGoRuntimeValid(&x.Receipt.autoGoRuntimeReceipt) || x.Receipt.Generation != x.Generation {
		return errors.New("historical Go experiment runtime preflight is not verified")
	}
	r.GoDependency = x.Receipt.DependencyKey
	r.GoBundle = x.Receipt.BundleDigest
	r.GoToolchain = x.Receipt.ToolchainDigest
	return nil
}
func (s *Server) prepareAutoExpertGoRuntime(ctx context.Context, a *autoRecord, owner *autoJob, progress string) (bool, error) {
	var pin *autoExpertRecoveryPin
	for _, p := range a.State.Items {
		if p.ExpertProgressKey == progress && p.ExpertRecoveryTaskID > 0 {
			var e error
			pin, e = autoExpertPin(a, p)
			if e != nil {
				return false, e
			}
			if e = autoPrivateExpertInvestigationCurrent(a, pin); e != nil {
				return false, e
			}
			break
		}
	}
	if pin == nil {
		return false, errors.New("Go runtime preflight requires admitted expert source")
	}
	source := autoGoProbeSource(a, pin)
	if source == nil || source.Recovery == nil || source.Recovery.State != "verified" {
		return true, nil
	}
	if autoGoRuntimeValid(source.GoRuntime) {
		return true, nil
	}
	if !autoHash256(source.Recovery.Key) || !autoHash256(pin.SourceSHA) {
		return false, errors.New("historical Go source identity unavailable")
	}
	if e := autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{owner.Provider}, time.Now()); e != nil {
		return false, e
	}
	if a.GoProbeRuntimes == nil {
		a.GoProbeRuntimes = map[string]*autoGoProbeRuntime{}
	}
	key := autoGoProbeIdentity(pin, source.Recovery.Key)
	x := a.GoProbeRuntimes[key]
	if x == nil {
		x = &autoGoProbeRuntime{SourceJob: source.ID, SourceSHA: pin.SourceSHA, DependencyKey: source.Recovery.Key, Generation: 1}
		a.GoProbeRuntimes[key] = x
	}
	if x.StopRequested && !x.Stopped {
		return false, errors.New("Go experiment runtime cancellation awaiting confirmation")
	}
	if x.Stopped {
		x.Generation++
		x.Stopped = false
		x.StopRequested = false
		x.Receipt = nil
	}
	if x.Receipt != nil && x.Receipt.State == "verified" {
		return true, nil
	}
	// Persist ownership/generation before the bounded launch handshake. Heavy
	// hashing and archive extraction occur only in the runner's fixed service.
	if e := s.saveAuto(a); e != nil {
		return false, e
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, e := s.runAutoCommand(bounded, "go-runtime", "--job", x.SourceJob, "--dependency-key", x.DependencyKey, "--source-sha256", x.SourceSHA, "--generation", strconv.Itoa(x.Generation))
	if e != nil {
		return false, e
	}
	var receipt autoGoProbeRuntimeReceipt
	if len(raw) > 65536 || json.Unmarshal(raw, &receipt) != nil || receipt.SchemaVersion != 1 || receipt.OwnerJob != x.SourceJob || receipt.SourceSHA != x.SourceSHA || receipt.DependencyKey != x.DependencyKey || receipt.Generation != x.Generation || receipt.Provenance != "new_experiment" {
		return false, errors.New("Go experiment runtime receipt binding differs")
	}
	switch receipt.State {
	case "verified":
		if !autoGoRuntimeValid(&receipt.autoGoRuntimeReceipt) {
			return false, errors.New("Go experiment runtime digests missing")
		}
	case "checking", "recovering", "waiting", "unavailable", "failed":
	default:
		return false, errors.New("invalid Go experiment runtime state")
	}
	x.Receipt = &receipt
	return receipt.State == "verified", s.saveAuto(a)
}
func autoGoProbePending(a *autoRecord) bool {
	for _, x := range a.GoProbeRuntimes {
		if !x.Stopped && (x.Receipt == nil || x.Receipt.State == "checking" || x.Receipt.State == "recovering" || x.Receipt.State == "waiting") {
			return true
		}
	}
	return false
}
func (s *Server) stopAutoGoProbeRuntimes(ctx context.Context, a *autoRecord) error {
	var first error
	keys := make([]string, 0, len(a.GoProbeRuntimes))
	for key := range a.GoProbeRuntimes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		x, y := a.GoProbeRuntimes[keys[i]], a.GoProbeRuntimes[keys[j]]
		if x.StopAttempts != y.StopAttempts {
			return x.StopAttempts < y.StopAttempts
		}
		return keys[i] < keys[j]
	})
	attempts := 0
	for _, key := range keys {
		x := a.GoProbeRuntimes[key]
		if x.Stopped || x.Receipt != nil && (x.Receipt.State == "verified" || x.Receipt.State == "unavailable" || x.Receipt.State == "failed") {
			continue
		}
		if attempts >= 4 {
			break
		}
		attempts++
		x.StopAttempts++
		x.StopRequested = true
		if e := s.saveAuto(a); e != nil {
			return e
		}
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, e := s.runAutoCommand(bounded, "go-runtime-stop", "--job", x.SourceJob, "--dependency-key", x.DependencyKey, "--source-sha256", x.SourceSHA, "--generation", strconv.Itoa(x.Generation))
		cancel()
		var stop struct {
			State         string `json:"state"`
			Job           string `json:"owner_job"`
			Generation    int    `json:"generation"`
			DependencyKey string `json:"go_dependency_key"`
			SourceSHA     string `json:"source_archive_sha256"`
		}
		if e == nil && (json.Unmarshal(raw, &stop) != nil || stop.State != "stopped" || stop.Job != x.SourceJob || stop.Generation != x.Generation || stop.DependencyKey != x.DependencyKey || stop.SourceSHA != x.SourceSHA) {
			e = errors.New("Go experiment stop unconfirmed")
		}
		if e == nil {
			x.Stopped = true
		} else if first == nil {
			first = e
		}
	}
	if e := s.saveAuto(a); e != nil && first == nil {
		first = e
	}
	return first
}

func autoExpertGoPreflightView(a *autoRecord, progress string) any {
	for _, p := range a.State.Items {
		if p.ExpertProgressKey != progress {
			continue
		}
		pin, e := autoExpertPin(a, p)
		if e != nil {
			return nil
		}
		source := autoGoProbeSource(a, pin)
		if source == nil || source.Recovery == nil {
			return nil
		}
		if x := a.GoProbeRuntimes[autoGoProbeIdentity(pin, source.Recovery.Key)]; x != nil {
			return x.Receipt
		}
	}
	return nil
}
