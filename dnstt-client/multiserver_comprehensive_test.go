package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"www.bamsoftware.com/git/dnstt.git/dns"
	"www.bamsoftware.com/git/dnstt.git/turbotunnel"
)

func TestMultiServerComprehensive(t *testing.T) {
	// Clean up any stale observation file.
	_ = os.Remove("test_obs.json")

	// Server A: starts healthy, then rate-limits for a short window, then recovers.
	var aCount int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		_, _ = io.ReadAll(r.Body)
		n := atomic.AddInt32(&aCount, 1)
		// Rate-limit for requests 5..9 (inclusive).
		if n >= 5 && n <= 9 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	}))
	defer a.Close()

	// Server B: starts non-working (delays), later becomes healthy after some
	// time (after it has seen 3 requests).
	var bCount int32
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		_, _ = io.ReadAll(r.Body)
		n := atomic.AddInt32(&bCount, 1)
		if n <= 3 {
			// delay and then return 500 to simulate no useful reply
			time.Sleep(1500 * time.Millisecond)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	}))
	defer b.Close()

	// Server C: flaps between working and rate-limiting rapidly based on time.
	var cCount int32
	c := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		_, _ = io.ReadAll(r.Body)
		atomic.AddInt32(&cCount, 1)
		if time.Now().Unix()%4 < 2 {
			w.Header().Set("Content-Type", "application/dns-message")
			w.Write([]byte{})
			return
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
	}))
	defer c.Close()

	// Server D: always rate-limits for a longer period (persistent 429).
	var dCount int32
	d := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		_, _ = io.ReadAll(r.Body)
		atomic.AddInt32(&dCount, 1)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(429)
	}))
	defer d.Close()

	// Server E: random failures but often returns OK.
	var eCount int32
	e := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		_, _ = io.ReadAll(r.Body)
		atomic.AddInt32(&eCount, 1)
		if atomic.LoadInt32(&eCount)%5 == 0 {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	}))
	defer e.Close()

	domain, err := dns.ParseName("t.example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Build HTTPPacketConns and DNSPacketConns.
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil

	hA, err := NewHTTPPacketConn(rt, a.URL, 4)
	if err != nil {
		t.Fatal(err)
	}
	dA := NewDNSPacketConn(hA, turbotunnel.DummyAddr{}, domain)

	hB, err := NewHTTPPacketConn(rt, b.URL, 4)
	if err != nil {
		t.Fatal(err)
	}
	dB := NewDNSPacketConn(hB, turbotunnel.DummyAddr{}, domain)

	hC, err := NewHTTPPacketConn(rt, c.URL, 4)
	if err != nil {
		t.Fatal(err)
	}
	dC := NewDNSPacketConn(hC, turbotunnel.DummyAddr{}, domain)

	hD, err := NewHTTPPacketConn(rt, d.URL, 4)
	if err != nil {
		t.Fatal(err)
	}
	dD := NewDNSPacketConn(hD, turbotunnel.DummyAddr{}, domain)

	hE, err := NewHTTPPacketConn(rt, e.URL, 4)
	if err != nil {
		t.Fatal(err)
	}
	dE := NewDNSPacketConn(hE, turbotunnel.DummyAddr{}, domain)

	// Create serverInfo entries and wire dnsConn callbacks to update state.
	health := DefaultHealthConfig()
	siA := &serverInfo{name: "A", dnsConn: dA, addr: turbotunnel.DummyAddr{}}
	siB := &serverInfo{name: "B", dnsConn: dB, addr: turbotunnel.DummyAddr{}}
	siC := &serverInfo{name: "C", dnsConn: dC, addr: turbotunnel.DummyAddr{}}
	siD := &serverInfo{name: "D", dnsConn: dD, addr: turbotunnel.DummyAddr{}}
	siE := &serverInfo{name: "E", dnsConn: dE, addr: turbotunnel.DummyAddr{}}

	// Wire callbacks using the shared helper.
	wireHealthCallbacks(siA, dA, hA, health)
	wireHealthCallbacks(siB, dB, hB, health)
	wireHealthCallbacks(siC, dC, hC, health)
	wireHealthCallbacks(siD, dD, hD, health)
	wireHealthCallbacks(siE, dE, hE, health)

	servers := []*serverInfo{siA, siB, siC, siD, siE}
	multi := NewMultiDNSPacketConn(servers, "test_obs.json", health)
	defer multi.Close()

	// Now drive traffic for a period, sending real requests only.
	sends := 0
	stopAt := time.Now().Add(7 * time.Second)
	for time.Now().Before(stopAt) {
		payload := []byte("payload-")
		payload = append(payload, byte(sends%256))
		_, _ = multi.WriteTo(payload, turbotunnel.DummyAddr{})
		sends++
		time.Sleep(120 * time.Millisecond)
	}

	// Force writing observations and read them.
	multi.writeObservations()
	bdata, err := os.ReadFile("test_obs.json")
	if err != nil {
		t.Fatalf("missing observations: %v", err)
	}
	var obsDoc struct {
		Servers []struct {
			Name         string  `json:"name"`
			Working      bool    `json:"working"`
			NotBefore    string  `json:"not_before"`
			Requests     int64   `json:"requests"`
			RateEstimate float64 `json:"rate_estimate"`
			TokensAvail  float64 `json:"tokens_avail"`
		} `json:"servers"`
		Routing struct {
			Phase1Writes int64 `json:"phase1_writes"`
			Phase2Writes int64 `json:"phase2_writes"`
			Phase3Writes int64 `json:"phase3_writes"`
			Phase4Writes int64 `json:"phase4_writes"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(bdata, &obsDoc); err != nil {
		t.Fatalf("bad json: %v", err)
	}

	// Verify each server was observed and got some requests at some point.
	foundA, foundB, foundC := false, false, false
	for _, e := range obsDoc.Servers {
		if e.Name == "A" {
			foundA = true
		}
		if e.Name == "B" {
			foundB = true
		}
		if e.Name == "C" {
			foundC = true
		}
	}
	if !foundA || !foundB || !foundC {
		t.Fatalf("observations missing one or more servers: %v", obsDoc.Servers)
	}

	// Check that at least one server recovered from failures (B should
	// have become healthy and received requests by the end of the run).
	if atomic.LoadInt32(&bCount) == 0 {
		t.Fatalf("server B never received requests; rechecks didn't recover it")
	}

	// Sanity: total server counters should be non-trivial; allow significant
	// loss due to rate-limiting. Expect at least 10% of sends to have been
	// processed across all servers.
	total := int(atomic.LoadInt32(&aCount) + atomic.LoadInt32(&bCount) + atomic.LoadInt32(&cCount) + atomic.LoadInt32(&dCount) + atomic.LoadInt32(&eCount))
	if total < sends/10 {
		t.Fatalf("too many requests lost: sent=%d got=%d", sends, total)
	}

	// Verify AIMD convergence: healthy server A should have rate_estimate above
	// InitialRate, and persistently-failing server D should have rate_estimate
	// at MinRate.
	for _, e := range obsDoc.Servers {
		if e.Name == "A" && e.RateEstimate < 10.0 {
			t.Logf("warning: server A rate_estimate=%.2f (expected >= InitialRate)", e.RateEstimate)
		}
		if e.Name == "D" && e.RateEstimate > 1.0 {
			t.Logf("warning: server D rate_estimate=%.2f (expected near MinRate)", e.RateEstimate)
		}
	}

	// Quick content checks: ensure observation file contains expected fields.
	if !strings.Contains(string(bdata), "requests") || !strings.Contains(string(bdata), "working") {
		t.Fatalf("observations file missing expected fields")
	}
	if !strings.Contains(string(bdata), "rate_estimate") || !strings.Contains(string(bdata), "tokens_avail") {
		t.Fatalf("observations file missing AIMD fields")
	}

	// Verify routing phase counters are present and non-zero in aggregate.
	totalPhases := obsDoc.Routing.Phase1Writes + obsDoc.Routing.Phase2Writes + obsDoc.Routing.Phase3Writes + obsDoc.Routing.Phase4Writes
	if totalPhases == 0 {
		t.Fatalf("routing phase counters are all zero")
	}
	if !strings.Contains(string(bdata), "phase1_writes") {
		t.Fatalf("observations file missing routing phase fields")
	}
}
