package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"www.bamsoftware.com/git/dnstt.git/dns"
	"www.bamsoftware.com/git/dnstt.git/turbotunnel"
)

func TestMultiServerBasic(t *testing.T) {
	// Setup three DoH servers: good, delayed, and invalid.
	countGood := 0
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		_, _ = io.ReadAll(r.Body)
		countGood++
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	}))
	defer good.Close()

	countDelayed := 0
	delayed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		// Delay beyond health timeout.
		time.Sleep(3 * time.Second)
		_, _ = io.ReadAll(r.Body)
		countDelayed++
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	}))
	defer delayed.Close()

	countInvalid := 0
	// invalid server: returns invalid responses for the first few requests,
	// then starts returning valid responses to simulate recovery.
	invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		_, _ = io.ReadAll(r.Body)
		countInvalid++
		if countInvalid <= 3 {
			// Return invalid content-type and body for the first 3 requests.
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("invalid"))
			return
		}
		// After initial failures, start returning valid responses.
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	}))
	defer invalid.Close()

	domain, err := dns.ParseName("t.example.com")
	if err != nil {
		t.Fatal(err)
	}
	// Remove any pre-existing observations file to avoid stale data.
	_ = os.Remove("test_obs.json")

	// Create HTTPPacketConns and corresponding DNSPacketConns.
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil

	hg, err := NewHTTPPacketConn(rt, good.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dg := NewDNSPacketConn(hg, turbotunnel.DummyAddr{}, domain)

	hd, err := NewHTTPPacketConn(rt, delayed.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dd := NewDNSPacketConn(hd, turbotunnel.DummyAddr{}, domain)

	hi, err := NewHTTPPacketConn(rt, invalid.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	di := NewDNSPacketConn(hi, turbotunnel.DummyAddr{}, domain)

	health := DefaultHealthConfig()
	siGood := &serverInfo{name: "good", dnsConn: dg, addr: turbotunnel.DummyAddr{}}
	siDelayed := &serverInfo{name: "delayed", dnsConn: dd, addr: turbotunnel.DummyAddr{}}
	siInvalid := &serverInfo{name: "invalid", dnsConn: di, addr: turbotunnel.DummyAddr{}}

	// Wire health callbacks using the shared helper.
	wireHealthCallbacks(siGood, dg, hg, health)
	wireHealthCallbacks(siDelayed, dd, hd, health)
	wireHealthCallbacks(siInvalid, di, hi, health)

	servers := []*serverInfo{siGood, siDelayed, siInvalid}
	multi := NewMultiDNSPacketConn(servers, "test_obs.json", health)
	defer multi.Close()

	// Send several small packets and allow them to be flushed.
	for i := 0; i < 5; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}

	// Wait for requests to be delivered.
	time.Sleep(500 * time.Millisecond)

	if countGood+countDelayed+countInvalid == 0 {
		t.Fatalf("expected some requests to be sent, got 0")
	}

	// Wait up to 6 seconds for the observations file to be written by the
	// background observation writer (it writes every 5s).
	var b []byte
	var rerr error
	found := false
	for i := 0; i < 60; i++ {
		b, rerr = os.ReadFile("test_obs.json")
		if rerr == nil {
			found = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatalf("observations file missing after wait: %v", rerr)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		t.Fatalf("observations file empty")
	}

	// Parse observations JSON — the format is now a wrapper object with
	// "servers" and "routing" fields.
	type obsEntry struct {
		Name         string  `json:"name"`
		Working      bool    `json:"working"`
		NotBefore    string  `json:"not_before"`
		Requests     int64   `json:"requests"`
		RateEstimate float64 `json:"rate_estimate"`
		TokensAvail  float64 `json:"tokens_avail"`
	}
	var obsDoc struct {
		Servers []obsEntry `json:"servers"`
		Routing struct {
			Phase1Writes int64 `json:"phase1_writes"`
			Phase2Writes int64 `json:"phase2_writes"`
			Phase3Writes int64 `json:"phase3_writes"`
			Phase4Writes int64 `json:"phase4_writes"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(b, &obsDoc); err != nil {
		t.Fatalf("cannot parse observations json: %v", err)
	}
	var foundGood bool
	for _, e := range obsDoc.Servers {
		if e.Name == "good" {
			foundGood = true
			if e.Requests == 0 {
				t.Fatalf("good server reported zero requests")
			}
		}
	}
	if !foundGood {
		t.Fatalf("good server not present in observations")
	}
}
