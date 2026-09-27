package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var autoServerResourceID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// A controller-created observation request grants no maintenance authority.
// Exact bytes are retained outside the worker's writable workspace.
type autoServerObservationRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	OwnerJob      string `json:"owner_job"`
	OwnerTask     int64  `json:"owner_task"`
	TargetID      string `json:"target_id"`
	RegistrySHA   string `json:"registry_sha256"`
}

func autoServerObservationOwner(a *autoRecord, jobID string, launch bool) (*autoJob, error) {
	if a == nil || !autoExpertJobID.MatchString(jobID) {
		return nil, errors.New("invalid observation owner")
	}
	for _, j := range a.Jobs {
		if j.ID != jobID || j.TaskID <= 0 {
			continue
		}
		if !launch {
			return j, nil
		}
		if !a.Config.Enabled || a.State == nil || j.Status != "running" {
			break
		}
		for _, assignment := range a.State.Assignments {
			if assignment.TaskID == j.TaskID && assignment.Role == j.Role && !assignment.Completed {
				return j, nil
			}
		}
	}
	return nil, errors.New("observation requires a current running assignment")
}

func autoNewServerObservation(j *autoJob, target, registry string, now time.Time) (autoServerObservationRequest, error) {
	r := autoServerObservationRequest{}
	if j == nil || !autoExpertJobID.MatchString(j.ID) || j.TaskID <= 0 || !autoServerResourceID.MatchString(target) || !autoHash256(registry) {
		return r, errors.New("invalid observation binding")
	}
	// Repeated requests in a minute share one immutable observation. This bounds
	// repeated polling without interpreting a timeout as a failed collection.
	id := autoSHA([]byte(fmt.Sprintf("server-observation-v1\n%s\n%d\n%s\n%s\n%d", j.ID, j.TaskID, target, registry, now.Unix()/60)))
	return autoServerObservationRequest{1, id, j.ID, j.TaskID, target, registry}, nil
}

func autoServerObservationPath(root string, r autoServerObservationRequest) (string, error) {
	if r.SchemaVersion != 1 || !autoHash256(r.RequestID) || !autoExpertJobID.MatchString(r.OwnerJob) || r.OwnerTask <= 0 || !autoServerResourceID.MatchString(r.TargetID) || !autoHash256(r.RegistrySHA) {
		return "", errors.New("invalid observation request")
	}
	return filepath.Join(root, r.OwnerJob, "server-observations", r.RequestID, "request.json"), nil
}

func autoWriteServerObservation(root string, request autoServerObservationRequest) ([]byte, error) {
	name, err := autoServerObservationPath(root, request)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	// Check every existing component below the trusted configured root. Never
	// follow worker-created symlinks into a writable work tree or another job.
	dir := root
	for i, component := range []string{request.OwnerJob, "server-observations", request.RequestID} {
		dir = filepath.Join(dir, component)
		if i > 0 {
			if err = os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
				return nil, err
			}
		}
		info, e := os.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (i > 0 && info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("unsafe observation directory")
		}
	}
	f, err := os.CreateTemp(dir, ".request-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err = os.Link(f.Name(), name); os.IsExist(err) {
		old, e := autoReadRegular(name, 4096)
		if e != nil || string(old) != string(raw) {
			return nil, errors.New("immutable observation request changed")
		}
	} else if err != nil {
		return nil, err
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return raw, d.Sync()
}
