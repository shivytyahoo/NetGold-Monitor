package poller

import (
	"testing"
	"time"
)

func TestRateBasic(t *testing.T) {
	r := NewRateCalc(64)
	t0 := time.Now()
	if _, ok := r.Add(1000, 10000, t0); ok {
		t.Fatal("first sample must not give a rate")
	}
	// 8000 bytes in 10s = 6400 bps
	bps, ok := r.Add(9000, 11000, t0.Add(10*time.Second))
	if !ok || bps != 6400 {
		t.Fatalf("bps=%v ok=%v", bps, ok)
	}
}

func TestRateWrap32(t *testing.T) {
	r := NewRateCalc(32)
	t0 := time.Now()
	r.Add(uint64(1)<<32-500, 10000, t0) // 500 bytes before wrap
	bps, ok := r.Add(500, 10100, t0.Add(10*time.Second))
	// delta = 500 + 500 = 1000 bytes in 10s = 800 bps
	if !ok || bps != 800 {
		t.Fatalf("wrap bps=%v ok=%v", bps, ok)
	}
}

func TestRateRebootReset(t *testing.T) {
	r := NewRateCalc(64)
	t0 := time.Now()
	r.Add(1_000_000, 999999, t0)
	// uptime goes backwards = reboot; counter smaller must NOT create a spike
	if _, ok := r.Add(100, 50, t0.Add(10*time.Second)); ok {
		t.Fatal("reboot sample must only reset baseline")
	}
	bps, ok := r.Add(1100, 150, t0.Add(20*time.Second))
	if !ok || bps != 800 {
		t.Fatalf("post-reboot bps=%v ok=%v", bps, ok)
	}
}

func TestRateZeroTime(t *testing.T) {
	r := NewRateCalc(64)
	t0 := time.Now()
	r.Add(0, 100, t0)
	if _, ok := r.Add(1000, 100, t0); ok {
		t.Fatal("zero elapsed time must not give a rate")
	}
}
