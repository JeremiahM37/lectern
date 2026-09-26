package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// Usage is what a sampler saw over a measurement window.
type Usage struct {
	CPUAvgPct       float64 `json:"cpu_avg_pct"`
	CPUPeakPct      float64 `json:"cpu_peak_pct"`
	CPUWithChildren float64 `json:"cpu_avg_pct_incl_children"`
	RSSAvgMB        float64 `json:"rss_avg_mb"`
	RSSPeakMB       float64 `json:"rss_peak_mb"`
	Seconds         float64 `json:"seconds"`
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
	start, startT := readProc(s.pid), time.Now()
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
			u := Usage{Seconds: round(el), CPUPeakPct: round(peak), RSSPeakMB: round(rssPeak)}
			if el > 0 && end.ok {
				u.CPUAvgPct = round((end.self - start.self) / el * 100)
				u.CPUWithChildren = round(((end.self + end.children) - (start.self + start.children)) / el * 100)
			}
			if n > 0 {
				u.RSSAvgMB = round(rssSum / float64(n))
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
