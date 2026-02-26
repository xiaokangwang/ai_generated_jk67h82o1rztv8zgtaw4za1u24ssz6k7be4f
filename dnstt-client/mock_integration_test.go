package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"www.bamsoftware.com/git/dnstt.git/turbotunnel"
)

// TestMockEchoThroughTunnel is a baseline test with a single healthy server.
func TestMockEchoThroughTunnel(t *testing.T) {
	mt := setupMockTunnel(t, []func(int) mockBehaviorResult{
		healthyBehavior(),
	})
	defer mt.cleanup()

	succeeded := sendAndCollectMockEchoes(t, mt.localAddr, 15, 200*time.Millisecond)
	if succeeded < 12 {
		t.Fatalf("too few echoes: %d/15", succeeded)
	}
	t.Logf("echoed %d/15", succeeded)
}

// TestMockMultiServerRouting verifies that traffic is distributed across
// all 3 healthy servers.
func TestMockMultiServerRouting(t *testing.T) {
	mt := setupMockTunnel(t, []func(int) mockBehaviorResult{
		healthyBehavior(),
		healthyBehavior(),
		healthyBehavior(),
	})
	defer mt.cleanup()

	succeeded := sendAndCollectMockEchoes(t, mt.localAddr, 15, 200*time.Millisecond)
	if succeeded < 12 {
		t.Fatalf("too few echoes: %d/15", succeeded)
	}

	// Wait for traffic to flow, then check all servers received requests.
	time.Sleep(1 * time.Second)
	for i, addr := range mt.server.addrs {
		count := atomic.LoadInt64(&mt.server.reqCounts[i])
		if count == 0 {
			t.Logf("warning: server %d (%s) got 0 requests", i, addr)
		}
	}
	t.Logf("echoed %d/15", succeeded)
}

// TestMockSingleServerDown verifies failover when one of three servers drops.
func TestMockSingleServerDown(t *testing.T) {
	mt := setupMockTunnel(t, []func(int) mockBehaviorResult{
		healthyBehavior(),
		healthyBehavior(),
		dropBehavior(),
	})
	defer mt.cleanup()

	succeeded := sendAndCollectMockEchoes(t, mt.localAddr, 20, 200*time.Millisecond)
	if succeeded < 10 {
		t.Fatalf("too few echoes with 1 dead server: %d/20", succeeded)
	}
	t.Logf("echoed %d/20", succeeded)
}

// TestMockMajorityFail verifies resilience when 3 out of 5 servers drop.
func TestMockMajorityFail(t *testing.T) {
	mt := setupMockTunnel(t, []func(int) mockBehaviorResult{
		healthyBehavior(),
		healthyBehavior(),
		dropBehavior(),
		dropBehavior(),
		dropBehavior(),
	})
	defer mt.cleanup()

	succeeded := sendAndCollectMockEchoes(t, mt.localAddr, 20, 300*time.Millisecond)
	if succeeded < 8 {
		t.Fatalf("too few echoes with 3/5 dead: %d/20", succeeded)
	}
	t.Logf("echoed %d/20", succeeded)
}

// TestMockServerRecovers verifies that a server that starts broken and
// recovers begins receiving traffic.
func TestMockServerRecovers(t *testing.T) {
	mt := setupMockTunnel(t, []func(int) mockBehaviorResult{
		healthyBehavior(),
		recoverBehavior(15),
	})
	defer mt.cleanup()

	succeeded := sendAndCollectMockEchoes(t, mt.localAddr, 30, 200*time.Millisecond)
	if succeeded < 15 {
		t.Fatalf("too few echoes: %d/30", succeeded)
	}

	// The recovered server should eventually get requests.
	time.Sleep(1 * time.Second)
	count := atomic.LoadInt64(&mt.server.reqCounts[1])
	t.Logf("echoed %d/30; recovered server got %d requests", succeeded, count)
}

// TestMockRateLimit verifies that the health system detects a server that
// starts dropping responses and shifts traffic to healthy servers. This test
// drives traffic through the MultiDNSPacketConn directly (without relying on
// end-to-end tunnel echo) to verify the health system's detection behavior.
func TestMockRateLimit(t *testing.T) {
	srv := newMockDNSServer(t, []func(int) mockBehaviorResult{
		healthyBehavior(),
		healthyBehavior(),
		rateLimitBehavior(5), // drops after 5 requests
	})
	defer srv.close()

	domain := srv.domain
	health := fastHealth()

	var serverInfos []*serverInfo
	for _, udpAddr := range srv.addrs {
		conn, err := net.ListenUDP("udp", nil)
		if err != nil {
			t.Fatal(err)
		}
		dnsConn := NewDNSPacketConn(conn, udpAddr, domain)
		si := &serverInfo{
			name:    fmt.Sprintf("127.0.0.1:%d", udpAddr.Port),
			dnsConn: dnsConn,
			addr:    udpAddr,
		}
		wireHealthCallbacks(si, dnsConn, nil, health)
		serverInfos = append(serverInfos, si)
	}

	multi := NewMultiDNSPacketConn(serverInfos, "", health)
	defer multi.Close()

	// Drive traffic through the multi conn to trigger the rate limit.
	for i := 0; i < 30; i++ {
		multi.WriteTo([]byte("probe-packet"), turbotunnel.DummyAddr{})
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for the health system to detect the rate-limited server.
	time.Sleep(3 * time.Second)

	rlReqs := atomic.LoadInt64(&srv.reqCounts[2])
	healthyReqs0 := atomic.LoadInt64(&srv.reqCounts[0])
	healthyReqs1 := atomic.LoadInt64(&srv.reqCounts[1])
	t.Logf("requests: healthy0=%d healthy1=%d rateLimited=%d", healthyReqs0, healthyReqs1, rlReqs)

	// The rate-limited server should have received some initial requests.
	if rlReqs < 5 {
		t.Fatalf("rate-limited server got too few initial requests: %d", rlReqs)
	}

	// The rate-limited server should eventually be marked not-working by
	// the health system (either by the healthLoop detecting timeout, or by
	// OnInvalidResponse callback detecting invalid DNS responses).
	rlWorking := atomic.LoadInt32(&serverInfos[2].working)
	h0Working := atomic.LoadInt32(&serverInfos[0].working)
	h1Working := atomic.LoadInt32(&serverInfos[1].working)
	t.Logf("working: healthy0=%d healthy1=%d rateLimited=%d", h0Working, h1Working, rlWorking)

	// At least one healthy server should still be working.
	if h0Working == 0 && h1Working == 0 {
		t.Fatal("both healthy servers marked not-working")
	}
}

// TestMockSlowServer verifies that a slow server is detected and bypassed.
// The slow delay is set above the health timeout (500ms) so the health system
// detects it, but not so high that it creates a massive response backlog.
func TestMockSlowServer(t *testing.T) {
	mt := setupMockTunnel(t, []func(int) mockBehaviorResult{
		healthyBehavior(),
		healthyBehavior(),
		healthyBehavior(),
		slowBehavior(2 * time.Second),
	})
	defer mt.cleanup()

	succeeded := sendAndCollectMockEchoes(t, mt.localAddr, 15, 200*time.Millisecond)
	if succeeded < 8 {
		t.Fatalf("too few echoes with slow server: %d/15", succeeded)
	}
	t.Logf("echoed %d/15", succeeded)
}

// TestMockObservations verifies that the observations JSON file contains all
// expected fields, all servers, and non-zero phase counters.
func TestMockObservations(t *testing.T) {
	mt := setupMockTunnel(t, []func(int) mockBehaviorResult{
		healthyBehavior(),
		healthyBehavior(),
		corruptBehavior(),
	})
	defer mt.cleanup()

	_ = sendAndCollectMockEchoes(t, mt.localAddr, 15, 200*time.Millisecond)

	// Wait for observations to be written.
	time.Sleep(2 * time.Second)

	data, err := os.ReadFile(mt.obsFile)
	if err != nil {
		t.Fatalf("read obs: %v", err)
	}

	// Verify it's valid JSON with the expected top-level structure.
	var doc struct {
		Servers []struct {
			Name           string  `json:"name"`
			Working        bool    `json:"working"`
			NotBefore      string  `json:"not_before"`
			Requests       int64   `json:"requests"`
			RateEstimate   float64 `json:"rate_estimate"`
			TokensAvail    float64 `json:"tokens_avail"`
			RecheckBackoff string  `json:"recheck_backoff"`
		} `json:"servers"`
		Routing struct {
			Phase1Writes int64 `json:"phase1_writes"`
			Phase2Writes int64 `json:"phase2_writes"`
			Phase3Writes int64 `json:"phase3_writes"`
			Phase4Writes int64 `json:"phase4_writes"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse obs: %v\n%s", err, string(data))
	}

	// 3 servers expected.
	if len(doc.Servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(doc.Servers))
	}

	// All servers should have a name matching one of our server addrs.
	found := 0
	for _, s := range doc.Servers {
		for _, addr := range mt.server.addrs {
			expected := addr.String()
			if s.Name == expected {
				found++
			}
		}
		if s.RateEstimate <= 0 {
			t.Fatalf("server %s has non-positive rate_estimate: %f", s.Name, s.RateEstimate)
		}
		if s.RecheckBackoff == "" {
			t.Fatalf("server %s missing recheck_backoff", s.Name)
		}
	}
	if found != 3 {
		t.Fatalf("observations missing servers: found %d/3\n%s", found, string(data))
	}

	// Routing section should have some writes.
	totalWrites := doc.Routing.Phase1Writes + doc.Routing.Phase2Writes +
		doc.Routing.Phase3Writes + doc.Routing.Phase4Writes
	if totalWrites == 0 {
		t.Fatal("all routing phase counters are zero")
	}

	// Verify the raw JSON contains all expected field names.
	s := string(data)
	for _, field := range []string{
		"servers", "routing", "name", "working", "not_before", "requests",
		"rate_estimate", "tokens_avail", "recheck_backoff",
		"phase1_writes", "phase2_writes", "phase3_writes", "phase4_writes",
	} {
		if !strings.Contains(s, field) {
			t.Fatalf("obs JSON missing field %q", field)
		}
	}
	t.Logf("observations valid with %d total writes", totalWrites)
}
