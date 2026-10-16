package exec

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ringSize is how many recent lines a background job keeps.
const ringSize = 2000

// Job statuses.
const (
	StatusRunning = "running"
	StatusExited  = "exited"
	StatusKilled  = "killed"
)

// Jobs tracks background shell processes started by the shell tool. One Jobs
// value is shared by the shell, shell_output and shell_kill tools and by the
// UI, which reads it through List.
type Jobs struct {
	opts Options

	mu   sync.Mutex
	seq  int
	jobs map[string]*Job
	// order keeps start order for List.
	order []string
}

// defaultTimeout is the limit for a command that names none: the configured
// tool timeout when set, else def. Config tool_timeout 0 means no limit; the
// app passes NoTimeout for that, so the timeout machinery stays one path.
func (j *Jobs) defaultTimeout(def time.Duration) time.Duration {
	if j == nil || j.opts.ToolTimeout <= 0 {
		return def
	}
	return j.opts.ToolTimeout
}

// NoTimeout is the limit to pass as Options.ToolTimeout for "no limit".
const NoTimeout = 1000 * time.Hour

// NewJobs returns an empty job table that applies opts to shell commands.
func NewJobs(opts Options) *Jobs {
	return &Jobs{opts: opts, jobs: map[string]*Job{}}
}

// Job is one background process.
type Job struct {
	ID      string
	Command string
	Dir     string
	Start   time.Time

	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	status   string
	exitCode int
	killed   bool
	lines    []string // window of recent lines; the last ringSize at most
	total    int      // lines ever produced
	finished time.Time
}

// JobInfo is a snapshot of a Job for listings.
type JobInfo struct {
	ID       string
	Command  string
	Dir      string
	Start    time.Time
	Status   string
	ExitCode int // meaningful once Status is not running
	Lines    int // lines produced so far
}

// List returns a snapshot of every job in start order.
func (j *Jobs) List() []JobInfo {
	j.mu.Lock()
	ids := append([]string(nil), j.order...)
	jobs := make([]*Job, 0, len(ids))
	for _, id := range ids {
		jobs = append(jobs, j.jobs[id])
	}
	j.mu.Unlock()

	out := make([]JobInfo, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, job.info())
	}
	return out
}

// Get returns the job with id.
func (j *Jobs) Get(id string) (*Job, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.jobs[id]
	return job, ok
}

// Kill stops the job with id and waits up to 3 s for it to exit.
func (j *Jobs) Kill(id string) error {
	job, ok := j.Get(id)
	if !ok {
		return fmt.Errorf("no background job %q", id)
	}
	job.kill()
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
	}
	return nil
}

// KillAll stops every running job. Call it when the agent exits.
func (j *Jobs) KillAll() {
	j.mu.Lock()
	var running []*Job
	for _, job := range j.jobs {
		running = append(running, job)
	}
	j.mu.Unlock()

	for _, job := range running {
		job.kill()
	}
	deadline := time.After(3 * time.Second)
	for _, job := range running {
		select {
		case <-job.done:
		case <-deadline:
			return
		}
	}
}

// start registers a job for command and runs it in the background. The
// returned job is already running; its output is read through Read.
func (j *Jobs) start(parent context.Context, command, dir string, env []string, timeout time.Duration) *Job {
	// Background jobs outlive the call that started them, so they do not
	// inherit its cancellation; only Kill, KillAll or timeout end them.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	if timeout > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, timeout)
		base := cancel
		cancel = func() { stop(); base() }
	}

	j.mu.Lock()
	j.seq++
	id := fmt.Sprintf("bg-%d", j.seq)
	job := &Job{
		ID:      id,
		Command: command,
		Dir:     dir,
		Start:   time.Now(),
		cancel:  cancel,
		done:    make(chan struct{}),
		status:  StatusRunning,
	}
	j.jobs[id] = job
	j.order = append(j.order, id)
	j.mu.Unlock()

	go func() {
		defer close(job.done)
		res := runProcess(ctx, procSpec{
			argv:   shellArgv(command),
			dir:    dir,
			env:    env,
			onLine: job.append,
		})
		cancel()
		job.finish(res)
	}()
	return job
}

// kill cancels the job. The runner terminates the process group.
func (job *Job) kill() {
	job.mu.Lock()
	job.killed = true
	job.mu.Unlock()
	job.cancel()
}

// append records one output line, keeping only the last ringSize lines.
// Reslicing drops the oldest line without copying; append reallocates (and
// copies only the live window) once the backing array is full, so the cost
// stays amortised O(1) per line.
func (job *Job) append(line string) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.total++
	job.lines = append(job.lines, line)
	if len(job.lines) > ringSize {
		job.lines = job.lines[len(job.lines)-ringSize:]
	}
}

func (job *Job) finish(res procResult) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.exitCode = res.exitCode
	job.finished = time.Now()
	switch {
	case job.killed || res.cancelled:
		job.status = StatusKilled
	default:
		job.status = StatusExited
	}
}

func (job *Job) info() JobInfo {
	job.mu.Lock()
	defer job.mu.Unlock()
	return JobInfo{
		ID:       job.ID,
		Command:  job.Command,
		Dir:      job.Dir,
		Start:    job.Start,
		Status:   job.status,
		ExitCode: job.exitCode,
		Lines:    job.total,
	}
}

// Read returns the lines at or after since (an absolute line index), the
// cursor to pass next time, how many older lines were dropped from the ring,
// and the current status.
func (job *Job) Read(since int) (lines []string, next, dropped int, status string, exitCode int) {
	job.mu.Lock()
	defer job.mu.Unlock()
	first := job.total - len(job.lines)
	if since < first {
		dropped = first - since
		since = first
	}
	if since > job.total {
		since = job.total
	}
	lines = append([]string(nil), job.lines[since-first:]...)
	return lines, job.total, dropped, job.status, job.exitCode
}

// Done returns a channel closed when the job has finished.
func (job *Job) Done() <-chan struct{} { return job.done }
