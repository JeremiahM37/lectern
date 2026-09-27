package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
	"unicode/utf16"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

type autoMaintenanceValidationGeneration struct {
	Generation    int             `json:"generation"`
	RequestRaw    json.RawMessage `json:"request"`
	RequestSHA    string          `json:"request_sha256"`
	ReceiptRaw    json.RawMessage `json:"receipt"`
	State         string          `json:"state"`
	StopConfirmed bool            `json:"stop_confirmed"`
}
type autoMaintenanceValidationLease struct {
	History       []autoMaintenanceValidationGeneration `json:"history,omitempty"`
	ID            string                                `json:"id"`
	OperationID   string                                `json:"operation_id"`
	OwnerJob      string                                `json:"owner_job"`
	OwnerTask     int64                                 `json:"owner_task"`
	PinSHA        string                                `json:"pin_sha256"`
	Generation    int                                   `json:"generation"`
	RequestSHA    string                                `json:"request_sha256"`
	RequestRaw    json.RawMessage                       `json:"request"`
	State         string                                `json:"state"`
	ReceiptRaw    json.RawMessage                       `json:"receipt,omitempty"`
	Validation    *autoMaintenanceValidationReceipt     `json:"validation,omitempty"`
	StopRequested bool                                  `json:"stop_requested,omitempty"`
	StopConfirmed bool                                  `json:"stop_confirmed,omitempty"`
}
type autoMaintenanceValidationRequest struct {
	SchemaVersion int                   `json:"schema_version"`
	OperationID   string                `json:"operation_id"`
	PinSHA        string                `json:"pin_sha256"`
	OwnerJob      string                `json:"owner_job"`
	OwnerTask     int64                 `json:"owner_task"`
	TargetID      string                `json:"target_id"`
	ServiceID     string                `json:"service_id"`
	Action        string                `json:"action"`
	RegistrySHA   string                `json:"registry_sha256"`
	BeforeSHA     string                `json:"expected_state_sha256"`
	Limits        autoMaintenanceLimits `json:"limits"`
	Generation    int                   `json:"generation"`
}

func autoMaintenanceValidationOwner(a *autoRecord, job string, launch bool) (*autoJob, error) {
	if a == nil || a.State == nil {
		return nil, errors.New("maintenance state missing")
	}
	for _, j := range a.Jobs {
		if j.ID != job || j.Role != "reviewer" || j.TaskID <= 0 || j.MaintenanceAdmission == nil {
			continue
		}
		ad := j.MaintenanceAdmission
		if ad.Pin.Key != autoMaintenancePinHash(ad.Pin) || j.MaintenancePin != ad.Pin.Key || j.TaskID == ad.TaskID || j.ID == ad.JobID {
			return nil, errors.New("maintenance independent reviewer binding invalid")
		}
		if !launch {
			return j, nil
		}
		if !a.Config.Enabled || j.Status != "running" {
			break
		}
		for _, as := range a.State.Assignments {
			if as.TaskID == j.TaskID && as.Role == j.Role && !as.Completed && as.Item == a.State.Item && as.Round == a.State.Revision && as.Step == a.State.Step {
				if err := autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{j.Provider}, time.Now()); err != nil {
					return nil, err
				}
				return j, nil
			}
		}
	}
	return nil, errors.New("active maintenance reviewer required")
}
func autoMaintenanceOperationID(pin autoMaintenancePlanPin) string {
	return autoMaintenanceAuthorityID(autoMaintenancePinAuthority(pin))
}
func autoReserveMaintenanceValidation(a *autoRecord, j *autoJob) (*autoMaintenanceValidationLease, error) {
	pin := j.MaintenanceAdmission.Pin
	id := autoSHA([]byte(pin.Key + ":" + strconv.FormatInt(j.TaskID, 10)))
	if old := a.MaintenanceValidations[id]; old != nil {
		if old.PinSHA != pin.Key || old.OwnerTask != j.TaskID {
			return nil, errors.New("maintenance validation binding changed or cancelled")
		}
		if old.StopRequested {
			if !old.StopConfirmed || (old.State != "cancelled" && old.State != "interrupted") || len(old.ReceiptRaw) == 0 {
				return nil, errors.New("observe confirmed terminal cancellation before reauthorizing validation")
			}
			state, _, err := autoDecodeMaintenanceValidation(old.ReceiptRaw, old)
			if err != nil || state != old.State {
				return nil, errors.New("cancelled generation evidence invalid")
			}
			old.History = append(old.History, autoMaintenanceValidationGeneration{old.Generation, append(json.RawMessage(nil), old.RequestRaw...), old.RequestSHA, append(json.RawMessage(nil), old.ReceiptRaw...), old.State, true})
			var request autoMaintenanceValidationRequest
			if json.Unmarshal(old.RequestRaw, &request) != nil {
				return nil, errors.New("retained request invalid")
			}
			old.Generation++
			request.Generation = old.Generation
			raw, _ := json.Marshal(request)
			old.RequestRaw = raw
			old.RequestSHA = autoSHA(raw)
			old.ReceiptRaw = nil
			old.Validation = nil
			old.State = "reserved"
			old.StopRequested = false
			old.StopConfirmed = false
		}
		return old, nil
	}
	req := autoMaintenanceValidationRequest{1, autoMaintenanceOperationID(pin), pin.Key, j.ID, j.TaskID, pin.TargetID, pin.ServiceID, "service_resource_limits", pin.RegistrySHA, pin.BeforeSHA, pin.Limits, 1}
	raw, _ := json.Marshal(req)
	l := &autoMaintenanceValidationLease{ID: id, OperationID: req.OperationID, OwnerJob: j.ID, OwnerTask: j.TaskID, PinSHA: pin.Key, Generation: 1, RequestSHA: autoSHA(raw), RequestRaw: raw, State: "reserved"}
	if a.MaintenanceValidations == nil {
		a.MaintenanceValidations = map[string]*autoMaintenanceValidationLease{}
	}
	a.MaintenanceValidations[id] = l
	return l, nil
}
func autoWriteMaintenanceValidation(root string, l *autoMaintenanceValidationLease) error {
	if !autoExpertJobID.MatchString(l.OwnerJob) || !autoHash256(l.OperationID) || len(l.RequestRaw) > 16384 || autoSHA(l.RequestRaw) != l.RequestSHA {
		return errors.New("invalid maintenance request")
	}
	base := filepath.Join(root, l.OwnerJob)
	st, e := os.Lstat(base)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe maintenance owner directory")
	}
	dir := filepath.Join(base, "server-maintenance")
	if e = os.Mkdir(dir, 0700); e != nil && !os.IsExist(e) {
		return e
	}
	st, e = os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe maintenance request directory")
	}
	f, e := os.CreateTemp(dir, ".validation-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	_, e = f.Write(l.RequestRaw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	path := filepath.Join(dir, l.OperationID+".validate.json")
	if e = os.Link(f.Name(), path); os.IsExist(e) {
		old, err := autoReadRegular(path, 16384)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, l.RequestRaw) {
			if len(l.History) == 0 {
				return errors.New("immutable maintenance request changed")
			}
			previous := l.History[len(l.History)-1]
			if !previous.StopConfirmed || previous.Generation+1 != l.Generation || (previous.State != "cancelled" && previous.State != "interrupted") || !bytes.Equal(old, previous.RequestRaw) || autoSHA(old) != previous.RequestSHA {
				return errors.New("maintenance generation replacement lacks confirmed ownership")
			}
			if e = os.Rename(f.Name(), path); e != nil {
				return e
			}
		}
	} else if e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func autoDecodeMaintenanceValidation(raw []byte, l *autoMaintenanceValidationLease) (string, *autoMaintenanceValidationReceipt, error) {
	var envelope struct {
		Phase      string          `json:"phase"`
		Schema     int             `json:"schema_version"`
		Operation  string          `json:"operation_id"`
		Owner      string          `json:"owner_job"`
		Task       int64           `json:"owner_task"`
		Pin        string          `json:"pin_sha256"`
		Generation int             `json:"generation"`
		RequestSHA string          `json:"request_sha256"`
		State      string          `json:"state"`
		ReceiptSHA string          `json:"receipt_sha256"`
		Result     json.RawMessage `json:"result"`
	}
	if len(raw) > 256<<10 || autoVerifyMaintenanceSeal(raw) != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Schema != 1 || envelope.Operation != l.OperationID || envelope.Owner != l.OwnerJob || envelope.Task != l.OwnerTask || envelope.Pin != l.PinSHA || envelope.Generation != l.Generation || envelope.RequestSHA != l.RequestSHA || envelope.Phase != "validate" {
		return "", nil, errors.New("maintenance outer receipt binding invalid")
	}
	switch envelope.State {
	case "running", "waiting", "pending":
		return envelope.State, nil, nil
	case "validated", "validation_failed", "unavailable", "cancelled", "interrupted", "timeout", "conflict":
	default:
		return "", nil, errors.New("unknown maintenance validation state")
	}
	if envelope.Schema != 1 || !autoHash256(envelope.ReceiptSHA) {
		return "", nil, errors.New("maintenance terminal receipt identity absent")
	}
	if envelope.State != "validated" {
		return envelope.State, nil, nil
	}
	var inner struct {
		Operation  string `json:"operation_id"`
		Registry   string `json:"registry_sha256"`
		Before     string `json:"before_sha256"`
		Candidate  string `json:"candidate_sha256"`
		Pin        string `json:"pin_sha256"`
		Owner      string `json:"owner_job"`
		Task       int64  `json:"owner_task"`
		Profile    string `json:"profile"`
		ProfileSHA string `json:"profile_sha256"`
		OutputSHA  string `json:"output_sha256"`
		ReceiptSHA string `json:"receipt_sha256"`
		Executed   *bool  `json:"executed"`
		Exit       *int   `json:"exit_code"`
		Mutation   *bool  `json:"mutation_performed"`
		State      string `json:"state"`
	}
	var req autoMaintenanceValidationRequest
	if json.Unmarshal(l.RequestRaw, &req) != nil || autoVerifyMaintenanceSeal(envelope.Result) != nil || json.Unmarshal(envelope.Result, &inner) != nil || inner.Operation != l.OperationID || inner.Pin != l.PinSHA || inner.Owner != l.OwnerJob || inner.Task != l.OwnerTask || inner.Registry != req.RegistrySHA || inner.Before != req.BeforeSHA || inner.Candidate != autoMaintenanceCandidate(req.Limits) || inner.Profile != "service_resource_limits_v1" || !autoHash256(inner.ProfileSHA) || !autoHash256(inner.OutputSHA) || !autoHash256(inner.ReceiptSHA) || inner.State != "validated" || inner.Executed == nil || !*inner.Executed || inner.Exit == nil || *inner.Exit != 0 || inner.Mutation == nil || *inner.Mutation {
		return "", nil, errors.New("maintenance executed candidate validation binding invalid")
	}
	return envelope.State, &autoMaintenanceValidationReceipt{PinSHA: inner.Pin, CandidateSHA: inner.Candidate, ReceiptSHA: inner.ReceiptSHA, ProfileSHA: inner.ProfileSHA, OutputSHA: inner.OutputSHA, OwnerTask: inner.Task, OwnerJob: inner.Owner, Executed: true, ExitCode: inner.Exit, Profile: inner.Profile}, nil
}
func (s *Server) autoMaintenanceValidationBridge(job string, w http.ResponseWriter, r *http.Request) {
	s.autoMaintenanceValidationBridgeAt(autoRoot, job, w, r)
}
func (s *Server) autoMaintenanceValidationBridgeAt(root, job string, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	launch := r.Method == http.MethodPost
	id, pin := "", ""
	if launch {
		if r.URL.RawQuery != "" {
			http.Error(w, "unexpected selector", 400)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
		if e != nil || len(raw) > 4096 {
			http.Error(w, "bounded pin required", 400)
			return
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		open, e := d.Token()
		key, kerr := d.Token()
		if e != nil || open != json.Delim('{') || kerr != nil || key != "pin_sha256" || d.Decode(&pin) != nil || !autoHash256(pin) {
			http.Error(w, "one pin_sha256 required", 400)
			return
		}
		close, e := d.Token()
		if e != nil || close != json.Delim('}') || d.Decode(new(any)) != io.EOF {
			http.Error(w, "unexpected validation input", 400)
			return
		}
	} else if r.Method == http.MethodGet {
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || len(q) != 1 || len(q["id"]) != 1 || !autoHash256(q.Get("id")) {
			http.Error(w, "one validation id required", 400)
			return
		}
		id = q.Get("id")
	} else {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	var j *autoJob
	if e == nil {
		j, e = autoMaintenanceValidationOwner(a, job, launch)
	}
	if e != nil {
		http.Error(w, "maintenance reviewer unavailable", 409)
		return
	}
	var l *autoMaintenanceValidationLease
	if launch {
		if j.MaintenancePin != pin {
			http.Error(w, "foreign maintenance pin", 403)
			return
		}
		l, e = autoReserveMaintenanceValidation(a, j)
		if e == nil {
			e = s.saveAuto(a)
		}
		if e == nil {
			e = autoWriteMaintenanceValidation(root, l)
		}
	} else {
		l = a.MaintenanceValidations[id]
		if l == nil || l.OwnerTask != j.TaskID || l.PinSHA != j.MaintenancePin {
			http.Error(w, "validation not found for this reviewer", 404)
			return
		}
	}
	if e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	if len(l.ReceiptRaw) > 0 {
		var body map[string]any
		_ = json.Unmarshal(l.ReceiptRaw, &body)
		body["id"] = l.ID
		writeJSON(w, 200, body)
		return
	}
	command := "server-maintenance-status"
	if launch {
		command = "server-maintenance-validate"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	args := []string{command, "--job", l.OwnerJob, "--operation-id", l.OperationID, "--generation", strconv.Itoa(l.Generation)}
	if !launch {
		args = append(args, "--phase", "validate")
	}
	result, e := s.runAutoCommand(ctx, args...)
	if e != nil {
		writeJSON(w, 202, map[string]any{"id": l.ID, "state": "pending", "reason": "Preserve this id and poll; unavailable observation is not a new execution"})
		return
	}
	state, validation, e := autoDecodeMaintenanceValidation(result, l)
	if e != nil {
		http.Error(w, e.Error()+"; retain id "+l.ID, 502)
		return
	}
	l.State = state
	status := 202
	if state != "running" && state != "waiting" && state != "pending" {
		l.ReceiptRaw = append(json.RawMessage(nil), result...)
		l.Validation = validation
		l.StopConfirmed = true
		status = 200
	}
	if e = s.saveAuto(a); e != nil {
		http.Error(w, "validation evidence persistence failed", 503)
		return
	}
	var body map[string]any
	_ = json.Unmarshal(result, &body)
	body["id"] = l.ID
	writeJSON(w, status, body)
}

// OFF persists cancellation before contacting the privileged runner. The runner
// tombstones the operation before stopping validation; status GET never starts it.
func (s *Server) stopAutoMaintenanceValidations(ctx context.Context, a *autoRecord) error {
	pending := []*autoMaintenanceValidationLease{}
	for _, l := range a.MaintenanceValidations {
		if l.StopConfirmed || len(l.ReceiptRaw) > 0 {
			continue
		}
		l.StopRequested = true
		pending = append(pending, l)
	}
	if len(pending) == 0 {
		return nil
	}
	if e := s.saveAuto(a); e != nil {
		return e
	}
	var failures []error
	for _, l := range pending {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, e := s.runAutoCommand(c, "server-maintenance-stop", "--job", l.OwnerJob, "--operation-id", l.OperationID, "--generation", strconv.Itoa(l.Generation))
		cancel()
		if e != nil {
			failures = append(failures, e)
			continue
		}
		var body struct {
			Operation  string `json:"operation_id"`
			Generation int    `json:"generation"`
			State      string `json:"state"`
		}
		if json.Unmarshal(raw, &body) != nil || body.Operation != l.OperationID || body.Generation != l.Generation {
			failures = append(failures, errors.New("maintenance cancellation binding invalid"))
			continue
		}
		if body.State == "stopped" {
			l.StopConfirmed = true
		} else {
			failures = append(failures, errors.New("maintenance cancellation still pending"))
		}
	}
	if e := s.saveAuto(a); e != nil {
		failures = append(failures, e)
	}
	return errors.Join(failures...)
}
func autoMaintenanceValidationStopPending(a *autoRecord) bool {
	for _, l := range a.MaintenanceValidations {
		if !l.StopConfirmed && len(l.ReceiptRaw) == 0 {
			return true
		}
	}
	return false
}

// Python's root helper seals sorted compact JSON with ensure_ascii=True. Preserve
// number lexemes (including 1.0) and escape non-ASCII identically; Go's default
// encoder would otherwise change both Unicode and floating-point representation.
func autoMaintenanceCanonical(v any) ([]byte, error) {
	var b bytes.Buffer
	var write func(any) error
	quote := func(s string) {
		b.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if r < 32 || r > 126 {
					if r > 0xffff {
						hi, lo := utf16.EncodeRune(r)
						fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
					} else {
						fmt.Fprintf(&b, `\u%04x`, r)
					}
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	}
	write = func(x any) error {
		switch value := x.(type) {
		case nil:
			b.WriteString("null")
		case bool:
			if value {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		case string:
			quote(value)
		case json.Number:
			b.WriteString(string(value))
		case []any:
			b.WriteByte('[')
			for i, el := range value {
				if i > 0 {
					b.WriteByte(',')
				}
				if e := write(el); e != nil {
					return e
				}
			}
			b.WriteByte(']')
		case map[string]any:
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			b.WriteByte('{')
			for i, key := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				quote(key)
				b.WriteByte(':')
				if e := write(value[key]); e != nil {
					return e
				}
			}
			b.WriteByte('}')
		default:
			return errors.New("unsupported sealed JSON value")
		}
		return nil
	}
	if e := write(v); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
func autoVerifyMaintenanceSeal(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var parse func(int) (any, error)
	parse = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errors.New("receipt nesting exceeds bound")
		}
		tok, e := d.Token()
		if e != nil {
			return nil, e
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return tok, nil
		}
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := k.(string)
				if !ok {
					return nil, errors.New("invalid receipt key")
				}
				if _, ok = m[key]; ok {
					return nil, errors.New("duplicate receipt key")
				}
				v, e := parse(depth + 1)
				if e != nil {
					return nil, e
				}
				m[key] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, errors.New("invalid receipt object")
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, e := parse(depth + 1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, errors.New("invalid receipt array")
			}
			return a, nil
		}
		return nil, errors.New("invalid receipt delimiter")
	}
	v, e := parse(0)
	if e != nil {
		return e
	}
	if _, e = d.Token(); e != io.EOF {
		return errors.New("trailing receipt data")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("receipt must be object")
	}
	expected, ok := m["receipt_sha256"].(string)
	if !ok || !autoHash256(expected) {
		return errors.New("receipt checksum absent")
	}
	delete(m, "receipt_sha256")
	canonical, e := autoMaintenanceCanonical(m)
	if e != nil {
		return e
	}
	if autoSHA(canonical) != expected {
		return errors.New("receipt checksum mismatch")
	}
	return nil
}
