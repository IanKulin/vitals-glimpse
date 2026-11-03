package main

import (
	"runtime"
	"testing"
	"time"
)

var t0 = time.Unix(1000, 0)

func TestValidWindow(t *testing.T) {
	for _, w := range []int{4, 33, 61, 0, -5, 65} {
		if validWindow(w) {
			t.Errorf("validWindow(%d) = true, want false", w)
		}
	}
	for _, w := range []int{5, 30, 60} {
		if !validWindow(w) {
			t.Errorf("validWindow(%d) = false, want true", w)
		}
	}
}

func TestPickWindow(t *testing.T) {
	mk := func(n int) []sample {
		s := make([]sample, n)
		for i := range s {
			s[i].reads = int64(i)
		}
		return s
	}
	if _, _, ok := pickWindow(mk(1), 6); ok {
		t.Error("expected !ok with one sample")
	}
	a, b, ok := pickWindow(mk(3), 6) // clamped to oldest
	if !ok || a.reads != 0 || b.reads != 2 {
		t.Errorf("clamp: got %d,%d ok=%v", a.reads, b.reads, ok)
	}
	a, b, _ = pickWindow(mk(10), 6)
	if a.reads != 3 || b.reads != 9 {
		t.Errorf("full: got %d,%d", a.reads, b.reads)
	}
}

func TestCPUProcStat(t *testing.T) {
	a := sample{t: t0, cpuOK: true, cpuIdle: 100, cpuTotal: 200}
	b := sample{t: t0.Add(5 * time.Second), cpuOK: true, cpuIdle: 150, cpuTotal: 300}
	if got := cpuPercentProcStat(a, b); got != 50 {
		t.Errorf("got %d, want 50", got)
	}
	bad := b
	bad.cpuOK = false
	if cpuPercentProcStat(a, bad) != -1 {
		t.Error("want -1 for failed read")
	}
	back := b
	back.cpuTotal = 100
	if cpuPercentProcStat(a, back) != -1 {
		t.Error("want -1 for counter reset")
	}
	same := a
	if cpuPercentProcStat(a, same) != -1 {
		t.Error("want -1 for zero elapsed")
	}
}

func TestCPUCgroupHonoursElapsed(t *testing.T) {
	n := int64(runtime.NumCPU())
	a := sample{t: t0, cpuOK: true, cgroupUsec: 0}
	// half of all CPUs busy over 10s
	b := sample{t: t0.Add(10 * time.Second), cpuOK: true, cgroupUsec: 10_000_000 * n / 2}
	if got := cpuPercentCgroup(a, b); got != 50 {
		t.Errorf("got %d, want 50", got)
	}
	b.cgroupUsec = 1 << 40
	if got := cpuPercentCgroup(a, b); got != 100 {
		t.Errorf("got %d, want cap of 100", got)
	}
	b.cgroupUsec = 0
	a.cgroupUsec = 5
	if cpuPercentCgroup(a, b) != -1 {
		t.Error("want -1 for counter reset")
	}
}

func TestIOPS(t *testing.T) {
	a := sample{t: t0, diskOK: true, reads: 100, writes: 100}
	b := sample{t: t0.Add(10 * time.Second), diskOK: true, reads: 150, writes: 250}
	if got := iops(a, b); got != 20 {
		t.Errorf("got %d, want 20", got)
	}
	b.diskOK = false
	if iops(a, b) != -1 {
		t.Error("want -1 for failed read")
	}
	b = sample{t: t0.Add(10 * time.Second), diskOK: true, reads: 50, writes: 250}
	if iops(a, b) != -1 {
		t.Error("want -1 for counter reset")
	}
	b.t = t0
	if iops(a, b) != -1 {
		t.Error("want -1 for zero elapsed")
	}
}

func TestNetKBs(t *testing.T) {
	a := sample{t: t0, netOK: true, rxBytes: 0, txBytes: 0}
	b := sample{t: t0.Add(10 * time.Second), netOK: true, rxBytes: 10 * 1024 * 100, txBytes: 10 * 1024 * 5}
	in, out := netKBs(a, b)
	if in != 100 || out != 5 {
		t.Errorf("got %d,%d want 100,5", in, out)
	}
	// small rates must not truncate to zero before dividing by elapsed
	b = sample{t: t0.Add(time.Second), netOK: true, rxBytes: 1536, txBytes: 0}
	if in, _ := netKBs(a, b); in != 1 {
		t.Errorf("got %d, want 1", in)
	}
	// uint64 counter going backwards must not underflow
	a.rxBytes = 5000
	b = sample{t: t0.Add(time.Second), netOK: true, rxBytes: 100}
	if in, out := netKBs(a, b); in != -1 || out != -1 {
		t.Errorf("got %d,%d want -1,-1", in, out)
	}
	b.netOK = false
	if in, _ := netKBs(a, b); in != -1 {
		t.Error("want -1 for failed read")
	}
}
