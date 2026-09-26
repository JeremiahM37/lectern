package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// clock ticks per second for /proc/<pid>/stat; 100 on every Linux build this
// runs on (CONFIG_HZ does not change USER_HZ).
const userHZ = 100

type procTimes struct {
	self, children float64 // seconds of CPU
	rssKB          int64
	ok             bool
}

func readProc(pid int) procTimes {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return procTimes{}
	}
	// comm may contain spaces; fields resume after the last ')'
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procTimes{}
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); utime=14, stime=15, cutime=16, cstime=17
	num := func(field int) float64 {
		v, _ := strconv.ParseFloat(f[field-3], 64)
		return v
	}
	t := procTimes{self: (num(14) + num(15)) / userHZ, children: (num(16) + num(17)) / userHZ, ok: true}
	status, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				t.rssKB, _ = strconv.ParseInt(fs[1], 10, 64)
			}
		}
	}
	return t
}

// commCPU sums CPU seconds of every process in this PID namespace whose comm
// matches one of names (tmux servers, ttyd, the SSH fixture).
func commCPU(names ...string) map[string]float64 {
	out := map[string]float64{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue
		}
		c := strings.TrimSpace(string(comm))
		for _, n := range names {
			if strings.HasPrefix(c, n) {
				t := readProc(pid)
				out[n] += t.self
			}
		}
	}
	return out
}

// Usage is what a sampler saw over a measurement window.
type Usage struct {
	CPUAvgPct       float64            `json:"cpu_avg_pct"`
	CPUPeakPct      float64            `json:"cpu_peak_pct"`
	CPUWithChildren float64            `json:"cpu_avg_pct_incl_children"`
	RSSAvgMB        float64            `json:"rss_avg_mb"`
	RSSPeakMB       float64            `json:"rss_peak_mb"`
	OtherCPUPct     map[string]float64 `json:"other_cpu_avg_pct"`
	Seconds         float64            `json:"seconds"`
}

// sampler records one process's CPU and RSS once a second. CPU is in percent
// of one core, so 100 means one core fully busy.
type sampler struct {
	pid  int
	stop chan struct{}
	done chan Usage
}

func startSampler(pid int) *sampler {
	s := &sampler{pid: pid, stop: make(chan struct{}), done: make(chan Usage, 1)}
	go s.run()
	return s
}

func (s *sampler) run() {
	others := []string{"tmux", "ttyd", "python3", "curl", "bash", "sleep"}
	start, startT := readProc(s.pid), time.Now()
	startOther := commCPU(others...)
	prev, prevT := start, startT
	var peak, rssSum, rssPeak float64
	n := 0
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			end, endT := readProc(s.pid), time.Now()
			el := endT.Sub(startT).Seconds()
			u := Usage{Seconds: round(el), CPUPeakPct: round(peak), RSSPeakMB: round(rssPeak), OtherCPUPct: map[string]float64{}}
			if el > 0 && end.ok {
				u.CPUAvgPct = round((end.self - start.self) / el * 100)
				u.CPUWithChildren = round(((end.self + end.children) - (start.self + start.children)) / el * 100)
			}
			if n > 0 {
				u.RSSAvgMB = round(rssSum / float64(n))
			}
			// Short-lived processes that exited during the window are not
			// counted here, so this undercounts curl/python3/bash; it is a
			// floor, reported only to show where the rest of the CPU went.
			endOther := commCPU(others...)
			for _, k := range others {
				u.OtherCPUPct[k] = round(max(0, endOther[k]-startOther[k]) / el * 100)
			}
			s.done <- u
			return
		case <-tick.C:
			cur, now := readProc(s.pid), time.Now()
			if !cur.ok {
				continue
			}
			if dt := now.Sub(prevT).Seconds(); dt > 0 {
				peak = max(peak, (cur.self-prev.self)/dt*100)
			}
			mb := float64(cur.rssKB) / 1024
			rssSum += mb
			rssPeak = max(rssPeak, mb)
			n++
			prev, prevT = cur, now
		}
	}
}

func (s *sampler) finish() Usage {
	close(s.stop)
	return <-s.done
}

// syncOnce is a tiny helper so several goroutines can report the first error.
type firstErr struct {
	mu  sync.Mutex
	err error
}

func (f *firstErr) set(err error) {
	f.mu.Lock()
	if f.err == nil {
		f.err = err
	}
	f.mu.Unlock()
}
