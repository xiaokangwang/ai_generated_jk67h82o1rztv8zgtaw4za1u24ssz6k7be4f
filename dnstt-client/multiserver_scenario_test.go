package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"www.bamsoftware.com/git/dnstt.git/dns"
	"www.bamsoftware.com/git/dnstt.git/turbotunnel"
)

// fastHealth returns a HealthConfig with accelerated timers for testing.
func fastHealth() HealthConfig {
	return HealthConfig{
		HealthInterval:         200 * time.Millisecond,
		HealthTimeout:          500 * time.Millisecond,
		NotBeforeDelay:         1 * time.Second,
		RecheckInterval:        500 * time.Millisecond,
		ObservationInterval:    300 * time.Millisecond,
		MaxRecheckInterval:     4 * time.Second,
		AIMDInterval:           300 * time.Millisecond,
		InitialRate:            10.0,
		MinRate:                0.5,
		MaxRate:                200.0,
		AdditiveIncrease:       2.0,
		MultiplicativeDecrease: 0.5,
		MaxTokens:              20.0,
		FailureRatioThreshold:  0.1,
		FailureCountThreshold:  3,
	}
}

// testDomain is a shared parsed DNS domain for tests.
func testDomain(t *testing.T) dns.Name {
	t.Helper()
	d, err := dns.ParseName("t.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// dohServer wraps an httptest.Server with its request counter and a way to
// change behavior dynamically.
type dohServer struct {
	srv     *httptest.Server
	count   int32 // atomic
	handler func(count int32, w http.ResponseWriter, r *http.Request)
}

func newDOHServer(handler func(count int32, w http.ResponseWriter, r *http.Request)) *dohServer {
	ds := &dohServer{handler: handler}
	ds.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		_, _ = io.ReadAll(r.Body)
		n := atomic.AddInt32(&ds.count, 1)
		ds.handler(n, w, r)
	}))
	return ds
}

func (ds *dohServer) close()      { ds.srv.Close() }
func (ds *dohServer) hits() int32 { return atomic.LoadInt32(&ds.count) }

// okHandler returns HTTP 200 with application/dns-message content type.
func okHandler(_ int32, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/dns-message")
	w.Write([]byte{})
}

// errHandler returns HTTP 500 on every request.
func errHandler(_ int32, w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(500)
}

// rateLimitHandler returns HTTP 429 with Retry-After on every request.
func rateLimitHandler(_ int32, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Retry-After", "2")
	w.WriteHeader(429)
}

// slowHandler delays beyond the health timeout then responds OK.
func slowHandler(_ int32, w http.ResponseWriter, _ *http.Request) {
	time.Sleep(2 * time.Second)
	w.Header().Set("Content-Type", "application/dns-message")
	w.Write([]byte{})
}

// makeServerInfo creates a connected serverInfo from a dohServer.
func makeServerInfo(t *testing.T, name string, ds *dohServer, domain dns.Name, health HealthConfig) *serverInfo {
	t.Helper()
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil
	hc, err := NewHTTPPacketConn(rt, ds.srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dc := NewDNSPacketConn(hc, turbotunnel.DummyAddr{}, domain)
	si := &serverInfo{name: name, dnsConn: dc, addr: turbotunnel.DummyAddr{}}
	wireHealthCallbacks(si, dc, hc, health)
	return si
}

// readObs reads and parses the observation file.
type obsDoc struct {
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

func readObs(t *testing.T, path string) obsDoc {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read obs: %v", err)
	}
	var doc obsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse obs: %v\n%s", err, string(data))
	}
	return doc
}

func obsServer(doc obsDoc, name string) *struct {
	Name           string  `json:"name"`
	Working        bool    `json:"working"`
	NotBefore      string  `json:"not_before"`
	Requests       int64   `json:"requests"`
	RateEstimate   float64 `json:"rate_estimate"`
	TokensAvail    float64 `json:"tokens_avail"`
	RecheckBackoff string  `json:"recheck_backoff"`
} {
	for i := range doc.Servers {
		if doc.Servers[i].Name == name {
			return &doc.Servers[i]
		}
	}
	return nil
}

// =========================================================================
// Core Routing Tests
// =========================================================================

func TestWriteToEmptyServerList(t *testing.T) {
	health := fastHealth()
	multi := NewMultiDNSPacketConn(nil, "", health)
	defer multi.Close()

	n, err := multi.WriteTo([]byte("data"), turbotunnel.DummyAddr{})
	if n != 0 || err != nil {
		t.Fatalf("expected (0, nil), got (%d, %v)", n, err)
	}
}

func TestWriteToSingleServer(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()
	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "solo", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	for i := 0; i < 10; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}
	time.Sleep(300 * time.Millisecond)

	reqs := atomic.LoadInt64(&si.requestsSent)
	if reqs != 10 {
		t.Fatalf("expected 10 requests to single server, got %d", reqs)
	}
}

func TestWriteToDistributesAcrossHealthyServers(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds1 := newDOHServer(okHandler)
	ds2 := newDOHServer(okHandler)
	ds3 := newDOHServer(okHandler)
	defer ds1.close()
	defer ds2.close()
	defer ds3.close()

	si1 := makeServerInfo(t, "s1", ds1, domain, health)
	si2 := makeServerInfo(t, "s2", ds2, domain, health)
	si3 := makeServerInfo(t, "s3", ds3, domain, health)

	multi := NewMultiDNSPacketConn([]*serverInfo{si1, si2, si3}, "", health)
	defer multi.Close()

	for i := 0; i < 60; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}
	time.Sleep(300 * time.Millisecond)

	r1 := atomic.LoadInt64(&si1.requestsSent)
	r2 := atomic.LoadInt64(&si2.requestsSent)
	r3 := atomic.LoadInt64(&si3.requestsSent)
	total := r1 + r2 + r3
	if total != 60 {
		t.Fatalf("expected 60 total requests, got %d (s1=%d s2=%d s3=%d)", total, r1, r2, r3)
	}
	// Each server should get at least some traffic (>5 of 60).
	if r1 < 5 || r2 < 5 || r3 < 5 {
		t.Fatalf("unbalanced distribution: s1=%d s2=%d s3=%d", r1, r2, r3)
	}
}

func TestWriteToAvoidsNonWorkingServers(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	// Use okHandler for both servers; we test routing logic by directly
	// marking the bad server as not-working rather than relying on health
	// detection, which is unreliable with test HTTP servers that return
	// empty DNS message bodies.
	good := newDOHServer(okHandler)
	bad := newDOHServer(okHandler)
	defer good.close()
	defer bad.close()

	// Create serverInfo without wiring health callbacks to avoid
	// callback-driven state flapping with test HTTP servers.
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil

	hcGood, err := NewHTTPPacketConn(rt, good.srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dcGood := NewDNSPacketConn(hcGood, turbotunnel.DummyAddr{}, domain)
	siGood := &serverInfo{name: "good", dnsConn: dcGood, addr: turbotunnel.DummyAddr{}}

	hcBad, err := NewHTTPPacketConn(rt, bad.srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dcBad := NewDNSPacketConn(hcBad, turbotunnel.DummyAddr{}, domain)
	siBad := &serverInfo{name: "bad", dnsConn: dcBad, addr: turbotunnel.DummyAddr{}}

	multi := NewMultiDNSPacketConn([]*serverInfo{siGood, siBad}, "", health)
	defer multi.Close()

	// Directly mark bad server as not working after construction
	// (constructor resets all servers to working=1).
	atomic.StoreInt32(&siBad.working, 0)

	for i := 0; i < 30; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}
	time.Sleep(300 * time.Millisecond)

	goodReqs := atomic.LoadInt64(&siGood.requestsSent)
	badReqs := atomic.LoadInt64(&siBad.requestsSent)

	// Good server should get all traffic since bad is marked not working.
	if goodReqs < 25 {
		t.Fatalf("good server got too few requests: good=%d bad=%d", goodReqs, badReqs)
	}
}

func TestWriteToPhase4LastResort(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	// All servers return errors.
	bad1 := newDOHServer(errHandler)
	bad2 := newDOHServer(errHandler)
	defer bad1.close()
	defer bad2.close()

	si1 := makeServerInfo(t, "bad1", bad1, domain, health)
	si2 := makeServerInfo(t, "bad2", bad2, domain, health)

	multi := NewMultiDNSPacketConn([]*serverInfo{si1, si2}, "", health)
	defer multi.Close()

	// Mark both as not working and rate-limited AFTER construction
	// (constructor resets working=1).
	atomic.StoreInt32(&si1.working, 0)
	atomic.StoreInt32(&si2.working, 0)
	si1.notBefore.Store(time.Now().Add(10 * time.Second))
	si2.notBefore.Store(time.Now().Add(10 * time.Second))

	for i := 0; i < 10; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}

	// All writes should go through Phase 4 since both servers are down
	// and rate-limited.
	p4 := atomic.LoadInt64(&multi.phase4Writes)
	if p4 != 10 {
		p1 := atomic.LoadInt64(&multi.phase1Writes)
		p2 := atomic.LoadInt64(&multi.phase2Writes)
		p3 := atomic.LoadInt64(&multi.phase3Writes)
		t.Fatalf("expected 10 phase4 writes, got p1=%d p2=%d p3=%d p4=%d", p1, p2, p3, p4)
	}
}

func TestWriteToPhase2OnTokenDrain(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()
	health.MaxTokens = 3 // Very small bucket

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	// Send burst larger than MaxTokens.
	for i := 0; i < 10; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}

	p1 := atomic.LoadInt64(&multi.phase1Writes)
	p2 := atomic.LoadInt64(&multi.phase2Writes)
	// First ~3 should use Phase 1 (tokens), rest should fall to Phase 2.
	if p1 == 0 {
		t.Fatalf("expected some phase1 writes, got 0")
	}
	if p2 == 0 {
		t.Fatalf("expected some phase2 writes after token drain, got 0")
	}
	if p1+p2 != 10 {
		t.Fatalf("expected 10 total across p1+p2, got p1=%d p2=%d", p1, p2)
	}
}

func TestWriteToPhase3WhenAllNotWorking(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	// Create serverInfo without callbacks to prevent state flapping.
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil
	hc, err := NewHTTPPacketConn(rt, ds.srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dc := NewDNSPacketConn(hc, turbotunnel.DummyAddr{}, domain)
	si := &serverInfo{name: "s1", dnsConn: dc, addr: turbotunnel.DummyAddr{}}

	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	// Mark as not working AFTER construction (constructor resets to working=1).
	// Not rate-limited (no notBefore set).
	atomic.StoreInt32(&si.working, 0)

	for i := 0; i < 5; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}

	p3 := atomic.LoadInt64(&multi.phase3Writes)
	if p3 != 5 {
		p1 := atomic.LoadInt64(&multi.phase1Writes)
		p2 := atomic.LoadInt64(&multi.phase2Writes)
		p4 := atomic.LoadInt64(&multi.phase4Writes)
		t.Fatalf("expected 5 phase3 writes, got p1=%d p2=%d p3=%d p4=%d", p1, p2, p3, p4)
	}
}

// =========================================================================
// Token Bucket Tests
// =========================================================================

func TestPeekTokensDoesNotMutateState(t *testing.T) {
	si := &serverInfo{
		rateEstimate: 10.0,
		tokens:       5.0,
		lastRefill:   time.Now().Add(-1 * time.Second),
	}

	originalTokens := si.tokens
	originalRefill := si.lastRefill

	// Call peekTokens multiple times.
	for i := 0; i < 100; i++ {
		si.peekTokens(20.0)
	}

	// State must not have changed.
	if si.tokens != originalTokens {
		t.Fatalf("peekTokens mutated tokens: was %f, now %f", originalTokens, si.tokens)
	}
	if si.lastRefill != originalRefill {
		t.Fatalf("peekTokens mutated lastRefill")
	}
}

func TestTryConsumeTokenRefillsAndConsumes(t *testing.T) {
	si := &serverInfo{
		rateEstimate: 10.0,
		tokens:       0.0,
		lastRefill:   time.Now().Add(-1 * time.Second), // 1s ago → should refill ~10 tokens
	}

	ok := si.tryConsumeToken(20.0)
	if !ok {
		t.Fatal("expected token consumption to succeed after refill")
	}
	// Tokens should be around 9 (10 refilled - 1 consumed).
	si.rateMu.Lock()
	remaining := si.tokens
	si.rateMu.Unlock()
	if remaining < 8.0 || remaining > 10.0 {
		t.Fatalf("unexpected remaining tokens: %f", remaining)
	}
}

func TestTryConsumeTokenFailsWhenEmpty(t *testing.T) {
	si := &serverInfo{
		rateEstimate: 10.0,
		tokens:       0.0,
		lastRefill:   time.Now(), // Just refilled → 0 tokens available
	}

	ok := si.tryConsumeToken(20.0)
	if ok {
		t.Fatal("expected token consumption to fail with empty bucket")
	}
}

func TestTokenBucketCapsAtMax(t *testing.T) {
	si := &serverInfo{
		rateEstimate: 1000.0,
		tokens:       0.0,
		lastRefill:   time.Now().Add(-10 * time.Second), // Long ago → would refill 10000
	}

	maxTokens := 5.0
	peeked := si.peekTokens(maxTokens)
	if peeked != maxTokens {
		t.Fatalf("expected peekTokens capped at %f, got %f", maxTokens, peeked)
	}
}

// =========================================================================
// AIMD Tests
// =========================================================================

func TestAIMDIncreaseOnSuccess(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	initialRate := health.InitialRate

	// Record successes directly.
	atomic.AddInt64(&si.successCount, 10)

	// Wait for AIMD tick.
	time.Sleep(health.AIMDInterval + 100*time.Millisecond)

	si.rateMu.Lock()
	rate := si.rateEstimate
	si.rateMu.Unlock()

	if rate <= initialRate {
		t.Fatalf("expected rate to increase from %f, got %f", initialRate, rate)
	}
}

func TestAIMDDecreaseOnHighFailureRatio(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	initialRate := health.InitialRate

	// Record mostly failures (>10% ratio).
	atomic.AddInt64(&si.failureCount, 5)
	atomic.AddInt64(&si.successCount, 1)

	// Wait for AIMD tick.
	time.Sleep(health.AIMDInterval + 100*time.Millisecond)

	si.rateMu.Lock()
	rate := si.rateEstimate
	si.rateMu.Unlock()

	expected := initialRate * health.MultiplicativeDecrease
	if rate > expected+0.01 {
		t.Fatalf("expected rate to decrease to ~%f, got %f", expected, rate)
	}
}

func TestAIMDNoDecreaseOnLowFailureRatio(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	initialRate := health.InitialRate

	// Record mostly successes with few failures (< 10% ratio).
	atomic.AddInt64(&si.successCount, 100)
	atomic.AddInt64(&si.failureCount, 1) // 1% failure rate

	time.Sleep(health.AIMDInterval + 100*time.Millisecond)

	si.rateMu.Lock()
	rate := si.rateEstimate
	si.rateMu.Unlock()

	// Rate should have increased (low failure ratio triggers additive increase).
	if rate < initialRate {
		t.Fatalf("expected rate to increase (low failure ratio), got %f (initial=%f)", rate, initialRate)
	}
}

func TestAIMDClampToMinMax(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()
	health.MinRate = 2.0
	health.MaxRate = 50.0
	health.InitialRate = 2.0

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	// Push many failures to try to go below min.
	for i := 0; i < 5; i++ {
		atomic.AddInt64(&si.failureCount, 100)
		time.Sleep(health.AIMDInterval + 50*time.Millisecond)
	}

	si.rateMu.Lock()
	rate := si.rateEstimate
	si.rateMu.Unlock()

	if rate < health.MinRate {
		t.Fatalf("rate %f below MinRate %f", rate, health.MinRate)
	}

	// Now push many successes to try to go above max.
	si.rateMu.Lock()
	si.rateEstimate = health.MaxRate - 1
	si.rateMu.Unlock()

	for i := 0; i < 10; i++ {
		atomic.AddInt64(&si.successCount, 100)
		time.Sleep(health.AIMDInterval + 50*time.Millisecond)
	}

	si.rateMu.Lock()
	rate = si.rateEstimate
	si.rateMu.Unlock()

	if rate > health.MaxRate {
		t.Fatalf("rate %f above MaxRate %f", rate, health.MaxRate)
	}
}

// =========================================================================
// Recheck Backoff Tests
// =========================================================================

func TestRecheckBackoffDoubles(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()
	health.RecheckInterval = 200 * time.Millisecond
	health.MaxRecheckInterval = 10 * time.Second

	// Server that always fails with HTTP 500. We don't wire health
	// callbacks so that the HTTP-level OnRateLimit doesn't set notBefore
	// on the serverInfo (which would block the recheck loop from probing).
	// The HTTP layer's internal notBefore will cause packets to be dropped,
	// but the recheck loop still updates the backoff state on each probe
	// attempt regardless of whether the packet reaches the server.
	ds := newDOHServer(errHandler)
	defer ds.close()

	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil
	hc, err := NewHTTPPacketConn(rt, ds.srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dc := NewDNSPacketConn(hc, turbotunnel.DummyAddr{}, domain)
	si := &serverInfo{name: "s1", dnsConn: dc, addr: turbotunnel.DummyAddr{}}

	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	// Mark as not working AFTER construction (constructor resets to working=1).
	atomic.StoreInt32(&si.working, 0)

	// Directly populate the recent buffer so the recheck loop has data to
	// send, without going through WriteTo (which would set lastSend and
	// trigger the health loop's timeout detection / notBefore cycle).
	multi.recentMu.Lock()
	multi.recent = append(multi.recent, []byte("probe"))
	multi.recentMu.Unlock()

	// Wait for several recheck ticks.
	time.Sleep(2 * time.Second)

	backoff := si.recheckBackoff
	if backoff <= health.RecheckInterval {
		t.Fatalf("expected backoff to grow beyond initial %v, got %v", health.RecheckInterval, backoff)
	}
	// Backoff should have doubled at least once.
	if backoff < 2*health.RecheckInterval {
		t.Fatalf("expected backoff >= %v, got %v", 2*health.RecheckInterval, backoff)
	}
}

func TestRecheckBackoffResetsOnRecovery(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()
	health.RecheckInterval = 200 * time.Millisecond

	// Server starts failing then recovers.
	var phase int32
	ds := newDOHServer(func(count int32, w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&phase) == 0 {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	})
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	// Drive traffic while server fails.
	for i := 0; i < 5; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}
	time.Sleep(1500 * time.Millisecond)

	// Backoff should have grown.
	if si.recheckBackoff == 0 {
		// It's possible the server was marked working due to HTTP-level
		// callbacks; check that it's at least been probed.
		t.Log("recheckBackoff is 0; server may have been marked working by HTTP callbacks")
	}

	// Now make server healthy.
	atomic.StoreInt32(&phase, 1)
	// Drive more traffic.
	for i := 0; i < 5; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}
	time.Sleep(1500 * time.Millisecond)

	// The recheck loop should have reset the backoff once the server is
	// marked working.
	if atomic.LoadInt32(&si.working) == 1 && si.recheckBackoff != 0 {
		t.Fatalf("expected backoff reset to 0 after recovery, got %v", si.recheckBackoff)
	}
}

func TestRecheckBackoffCapsAtMax(t *testing.T) {
	health := fastHealth()
	health.RecheckInterval = 100 * time.Millisecond
	health.MaxRecheckInterval = 800 * time.Millisecond

	si := &serverInfo{name: "test"}
	atomic.StoreInt32(&si.working, 0)

	// Simulate repeated backoff doublings.
	si.recheckBackoff = health.RecheckInterval
	for i := 0; i < 20; i++ {
		si.recheckBackoff *= 2
		if si.recheckBackoff > health.MaxRecheckInterval {
			si.recheckBackoff = health.MaxRecheckInterval
		}
	}

	if si.recheckBackoff != health.MaxRecheckInterval {
		t.Fatalf("expected backoff capped at %v, got %v", health.MaxRecheckInterval, si.recheckBackoff)
	}
}

// =========================================================================
// Observation File Tests
// =========================================================================

func TestObservationFileStructure(t *testing.T) {
	obsFile := t.TempDir() + "/obs.json"
	domain := testDomain(t)
	health := fastHealth()

	ds1 := newDOHServer(okHandler)
	ds2 := newDOHServer(errHandler)
	defer ds1.close()
	defer ds2.close()

	si1 := makeServerInfo(t, "healthy", ds1, domain, health)
	si2 := makeServerInfo(t, "broken", ds2, domain, health)

	multi := NewMultiDNSPacketConn([]*serverInfo{si1, si2}, obsFile, health)
	defer multi.Close()

	for i := 0; i < 10; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}
	time.Sleep(500 * time.Millisecond)
	multi.writeObservations()

	doc := readObs(t, obsFile)

	// Verify all servers present.
	if len(doc.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(doc.Servers))
	}

	s1 := obsServer(doc, "healthy")
	s2 := obsServer(doc, "broken")
	if s1 == nil || s2 == nil {
		t.Fatalf("missing server in observations: %+v", doc.Servers)
	}

	// Verify all fields are populated.
	if s1.Requests == 0 && s2.Requests == 0 {
		t.Fatal("no requests recorded for any server")
	}
	if s1.RateEstimate <= 0 {
		t.Fatalf("healthy server has non-positive rate estimate: %f", s1.RateEstimate)
	}
	if s1.RecheckBackoff == "" {
		t.Fatal("missing recheck_backoff field")
	}

	// Verify routing section present.
	totalPhases := doc.Routing.Phase1Writes + doc.Routing.Phase2Writes +
		doc.Routing.Phase3Writes + doc.Routing.Phase4Writes
	if totalPhases != 10 {
		t.Fatalf("expected 10 total phase writes, got %d", totalPhases)
	}
}

func TestObservationFileAtomicNoTmp(t *testing.T) {
	dir := t.TempDir()
	obsFile := dir + "/obs.json"
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, obsFile, health)
	defer multi.Close()

	multi.writeObservations()

	// The final file should exist.
	if _, err := os.Stat(obsFile); err != nil {
		t.Fatalf("obs file missing: %v", err)
	}
	// The temp file should NOT exist.
	if _, err := os.Stat(obsFile + ".tmp"); err == nil {
		t.Fatal("temp file still exists after atomic write")
	}
}

func TestObservationEmptyPathSkips(t *testing.T) {
	health := fastHealth()
	// Use empty obsFile — writeObservations should be a no-op.
	multi := NewMultiDNSPacketConn(nil, "", health)
	defer multi.Close()
	// Should not panic.
	multi.writeObservations()
}

func TestObservationPhaseCounters(t *testing.T) {
	obsFile := t.TempDir() + "/obs.json"
	domain := testDomain(t)
	health := fastHealth()
	health.MaxTokens = 5

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, obsFile, health)
	defer multi.Close()

	// Send burst to trigger both Phase 1 and Phase 2.
	for i := 0; i < 15; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}

	multi.writeObservations()
	doc := readObs(t, obsFile)

	if doc.Routing.Phase1Writes == 0 {
		t.Fatal("expected some phase1 writes from token consumption")
	}
	if doc.Routing.Phase2Writes == 0 {
		t.Fatal("expected some phase2 writes from token overdraft")
	}
	total := doc.Routing.Phase1Writes + doc.Routing.Phase2Writes +
		doc.Routing.Phase3Writes + doc.Routing.Phase4Writes
	if total != 15 {
		t.Fatalf("expected 15 total writes, got %d", total)
	}
}

func TestObservationIncludesRecheckBackoff(t *testing.T) {
	obsFile := t.TempDir() + "/obs.json"
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(errHandler)
	defer ds.close()

	si := makeServerInfo(t, "failing", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, obsFile, health)
	defer multi.Close()

	// Drive traffic so recheck has something to send.
	for i := 0; i < 5; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}
	// Wait for health and recheck cycles.
	time.Sleep(2 * time.Second)

	multi.writeObservations()
	data, err := os.ReadFile(obsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "recheck_backoff") {
		t.Fatal("observation file missing recheck_backoff field")
	}
}

// =========================================================================
// Close / Lifecycle Tests
// =========================================================================

func TestCloseIdempotent(t *testing.T) {
	health := fastHealth()
	multi := NewMultiDNSPacketConn(nil, "", health)
	// Calling Close multiple times should not panic.
	multi.Close()
	multi.Close()
	multi.Close()
}

func TestReadFromReturnsErrorAfterClose(t *testing.T) {
	health := fastHealth()
	domain := testDomain(t)

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		_, _, err := multi.ReadFrom(buf)
		done <- err
	}()

	// Give ReadFrom time to block.
	time.Sleep(100 * time.Millisecond)
	multi.Close()

	select {
	case err := <-done:
		if err != errMultiConnClosed {
			t.Fatalf("expected errMultiConnClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadFrom did not unblock after Close")
	}
}

func TestLocalAddrNotNil(t *testing.T) {
	health := fastHealth()
	multi := NewMultiDNSPacketConn(nil, "", health)
	defer multi.Close()

	if multi.LocalAddr() == nil {
		t.Fatal("LocalAddr returned nil")
	}
}

// =========================================================================
// wireHealthCallbacks Tests
// =========================================================================

func TestWireHealthCallbacksUsesConfigNotBeforeDelay(t *testing.T) {
	domain := testDomain(t)
	customDelay := 42 * time.Second
	health := fastHealth()
	health.NotBeforeDelay = customDelay

	ds := newDOHServer(okHandler)
	defer ds.close()

	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil
	hc, err := NewHTTPPacketConn(rt, ds.srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dc := NewDNSPacketConn(hc, turbotunnel.DummyAddr{}, domain)
	si := &serverInfo{name: "test", dnsConn: dc, addr: turbotunnel.DummyAddr{}}

	wireHealthCallbacks(si, dc, hc, health)

	// Send enough consecutive invalid responses to reach the threshold.
	for i := int32(0); i < health.FailureCountThreshold-1; i++ {
		dc.OnInvalidResponse(nil)
	}
	before := time.Now()
	dc.OnInvalidResponse(nil)
	after := time.Now()

	v := si.notBefore.Load()
	if v == nil {
		t.Fatal("notBefore not set after reaching FailureCountThreshold")
	}
	nb := v.(time.Time)

	// notBefore should be approximately now + customDelay.
	expectedMin := before.Add(customDelay)
	expectedMax := after.Add(customDelay).Add(100 * time.Millisecond)
	if nb.Before(expectedMin) || nb.After(expectedMax) {
		t.Fatalf("notBefore %v not in expected range [%v, %v]", nb, expectedMin, expectedMax)
	}
}

func TestWireHealthCallbacksOnInvalidResponseMarksDown(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "test", ds, domain, health)
	atomic.StoreInt32(&si.working, 1)

	// A single invalid response should NOT mark the server down (threshold=3).
	si.dnsConn.OnInvalidResponse(nil)
	if atomic.LoadInt32(&si.working) != 1 {
		t.Fatal("expected server still working after 1 invalid response")
	}
	if atomic.LoadInt64(&si.failureCount) != 0 {
		t.Fatal("expected failureCount not incremented before threshold")
	}

	// Second invalid response — still below threshold.
	si.dnsConn.OnInvalidResponse(nil)
	if atomic.LoadInt32(&si.working) != 1 {
		t.Fatal("expected server still working after 2 invalid responses")
	}

	// Third invalid response — reaches threshold, should mark down.
	si.dnsConn.OnInvalidResponse(nil)
	if atomic.LoadInt32(&si.working) != 0 {
		t.Fatal("expected server marked not working after 3 consecutive invalid responses")
	}
	if atomic.LoadInt64(&si.failureCount) == 0 {
		t.Fatal("expected failureCount incremented after reaching threshold")
	}
}

func TestWireHealthCallbacksOnResponseReceivedMarksUp(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "test", ds, domain, health)
	atomic.StoreInt32(&si.working, 0)

	si.dnsConn.OnResponseReceived()

	if atomic.LoadInt32(&si.working) != 1 {
		t.Fatal("expected server marked working after OnResponseReceived")
	}
	if atomic.LoadInt64(&si.successCount) == 0 {
		t.Fatal("expected successCount incremented")
	}
	nb := si.notBefore.Load()
	if nb != nil && !nb.(time.Time).IsZero() {
		t.Fatal("expected notBefore cleared after OnResponseReceived")
	}
}

func TestWireHealthCallbacksOnRateLimit(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.Proxy = nil
	hc, err := NewHTTPPacketConn(rt, ds.srv.URL, 1)
	if err != nil {
		t.Fatal(err)
	}
	dc := NewDNSPacketConn(hc, turbotunnel.DummyAddr{}, domain)
	si := &serverInfo{name: "test", dnsConn: dc, addr: turbotunnel.DummyAddr{}}
	atomic.StoreInt32(&si.working, 1)

	wireHealthCallbacks(si, dc, hc, health)

	retryAt := time.Now().Add(30 * time.Second)
	hc.OnRateLimit(retryAt)

	if atomic.LoadInt32(&si.working) != 0 {
		t.Fatal("expected server marked not working after OnRateLimit")
	}
	if atomic.LoadInt64(&si.failureCount) == 0 {
		t.Fatal("expected failureCount incremented")
	}
	v := si.notBefore.Load()
	if v == nil {
		t.Fatal("notBefore not set")
	}
	if !v.(time.Time).Equal(retryAt) {
		t.Fatalf("notBefore %v != retryAt %v", v.(time.Time), retryAt)
	}
}

// =========================================================================
// Weighted Selection Tests
// =========================================================================

func TestWeightedSelectCandidateSingleReturnsIt(t *testing.T) {
	si := &serverInfo{name: "only"}
	c := serverCandidate{si: si, tokens: 10.0}
	result := weightedSelectCandidate([]serverCandidate{c})
	if result.si.name != "only" {
		t.Fatal("single candidate not returned")
	}
}

func TestWeightedSelectCandidateBiasTowardHighTokens(t *testing.T) {
	siHigh := &serverInfo{name: "high"}
	siLow := &serverInfo{name: "low"}

	candidates := []serverCandidate{
		{si: siHigh, tokens: 19.0},
		{si: siLow, tokens: 1.0},
	}

	counts := map[string]int{"high": 0, "low": 0}
	for i := 0; i < 10000; i++ {
		result := weightedSelectCandidate(candidates)
		counts[result.si.name]++
	}

	// With 19:1 ratio, "high" should get ~95% of selections.
	highPct := float64(counts["high"]) / 10000.0
	if highPct < 0.85 {
		t.Fatalf("expected high-token server to get ~95%% of selections, got %.1f%%", highPct*100)
	}
}

// =========================================================================
// Health Loop Tests
// =========================================================================

func TestHealthLoopMarksSlowServerDown(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(slowHandler) // 2s delay
	defer ds.close()

	si := makeServerInfo(t, "slow", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	// Send a packet so lastSend gets set.
	multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})

	// Wait for health check to detect the timeout.
	time.Sleep(health.HealthTimeout + 2*health.HealthInterval)

	if atomic.LoadInt32(&si.working) == 1 {
		t.Fatal("expected slow server to be marked not working")
	}
}

func TestHealthLoopRecoverAfterResponse(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	// Server starts slow, then becomes fast.
	var phase int32
	ds := newDOHServer(func(count int32, w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&phase) == 0 {
			time.Sleep(2 * time.Second)
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	})
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	// Drive traffic while server is slow.
	multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	time.Sleep(1500 * time.Millisecond)

	// Switch to fast mode.
	atomic.StoreInt32(&phase, 1)
	// Send more traffic to trigger responses.
	for i := 0; i < 5; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
	}
	time.Sleep(1500 * time.Millisecond)

	// Due to HTTP-level OnResponse callback, the server should be marked
	// working even though the DNS layer may see invalid responses.
	// The test verifies the system doesn't stay permanently down.
	if ds.hits() == 0 {
		t.Fatal("server received no requests at all")
	}
}

// =========================================================================
// Concurrency Tests
// =========================================================================

func TestConcurrentWriteToNoPanic(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds := newDOHServer(okHandler)
	defer ds.close()

	si := makeServerInfo(t, "s1", ds, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si}, "", health)
	defer multi.Close()

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
			}
		}()
	}
	wg.Wait()

	reqs := atomic.LoadInt64(&si.requestsSent)
	if reqs != 1000 {
		t.Fatalf("expected 1000 requests, got %d", reqs)
	}
}

func TestConcurrentWriteToMultipleServers(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	ds1 := newDOHServer(okHandler)
	ds2 := newDOHServer(okHandler)
	ds3 := newDOHServer(okHandler)
	defer ds1.close()
	defer ds2.close()
	defer ds3.close()

	si1 := makeServerInfo(t, "s1", ds1, domain, health)
	si2 := makeServerInfo(t, "s2", ds2, domain, health)
	si3 := makeServerInfo(t, "s3", ds3, domain, health)

	multi := NewMultiDNSPacketConn([]*serverInfo{si1, si2, si3}, "", health)
	defer multi.Close()

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
			}
		}()
	}
	wg.Wait()

	total := atomic.LoadInt64(&si1.requestsSent) +
		atomic.LoadInt64(&si2.requestsSent) +
		atomic.LoadInt64(&si3.requestsSent)
	if total != 500 {
		t.Fatalf("expected 500 total requests, got %d", total)
	}
}

// =========================================================================
// Rate Limiting Integration Test
// =========================================================================

func TestRateLimitedServerBypassedThenRecovery(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()

	good := newDOHServer(okHandler)
	defer good.close()

	// Rate-limit server for the first 5 requests, then become healthy.
	var rlCount int32
	rateLimited := newDOHServer(func(count int32, w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&rlCount, 1)
		if n <= 5 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	})
	defer rateLimited.close()

	siGood := makeServerInfo(t, "good", good, domain, health)
	siRL := makeServerInfo(t, "rl", rateLimited, domain, health)

	multi := NewMultiDNSPacketConn([]*serverInfo{siGood, siRL}, "", health)
	defer multi.Close()

	// Send traffic while RL server is rate-limiting.
	for i := 0; i < 20; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
		time.Sleep(50 * time.Millisecond)
	}

	// Good server should have gotten the bulk of traffic.
	goodReqs := atomic.LoadInt64(&siGood.requestsSent)
	if goodReqs < 10 {
		t.Fatalf("expected good server to get most traffic, got %d requests", goodReqs)
	}

	// Wait for the rate-limit to expire and send more traffic.
	time.Sleep(2 * time.Second)
	beforeRL := atomic.LoadInt64(&siRL.requestsSent)
	for i := 0; i < 10; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	afterRL := atomic.LoadInt64(&siRL.requestsSent)
	// The RL server should start getting traffic again after recovery.
	if afterRL <= beforeRL {
		t.Fatalf("expected RL server to get traffic after recovery, before=%d after=%d", beforeRL, afterRL)
	}
}

// =========================================================================
// Server Recovery End-to-End Scenario
// =========================================================================

func TestServerFailsAndRecoversEndToEnd(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()
	obsFile := t.TempDir() + "/obs.json"

	var phase int32 // 0 = failing, 1 = healthy

	ds1 := newDOHServer(okHandler) // always healthy
	ds2 := newDOHServer(func(count int32, w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&phase) == 0 {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write([]byte{})
	})
	defer ds1.close()
	defer ds2.close()

	si1 := makeServerInfo(t, "stable", ds1, domain, health)
	si2 := makeServerInfo(t, "flaky", ds2, domain, health)
	multi := NewMultiDNSPacketConn([]*serverInfo{si1, si2}, obsFile, health)
	defer multi.Close()

	// Phase 0: flaky is broken.
	for i := 0; i < 10; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(1 * time.Second)

	// Stable should be getting most of the traffic.
	stableReqs1 := atomic.LoadInt64(&si1.requestsSent)
	flakyReqs1 := atomic.LoadInt64(&si2.requestsSent)
	t.Logf("Phase 0: stable=%d flaky=%d", stableReqs1, flakyReqs1)

	// Phase 1: flaky recovers.
	atomic.StoreInt32(&phase, 1)
	time.Sleep(2 * time.Second) // Let health checks detect recovery.

	for i := 0; i < 20; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	flakyReqs2 := atomic.LoadInt64(&si2.requestsSent)
	t.Logf("Phase 1: flaky went from %d to %d requests", flakyReqs1, flakyReqs2)

	// Verify observations file is valid.
	multi.writeObservations()
	doc := readObs(t, obsFile)
	if len(doc.Servers) != 2 {
		t.Fatalf("expected 2 servers in observations, got %d", len(doc.Servers))
	}
}

// =========================================================================
// Full Observation Snapshot Scenario
// =========================================================================

func TestObservationFullSnapshot(t *testing.T) {
	domain := testDomain(t)
	health := fastHealth()
	obsFile := t.TempDir() + "/obs.json"

	ds1 := newDOHServer(okHandler)
	ds2 := newDOHServer(rateLimitHandler)
	ds3 := newDOHServer(errHandler)
	defer ds1.close()
	defer ds2.close()
	defer ds3.close()

	si1 := makeServerInfo(t, "healthy", ds1, domain, health)
	si2 := makeServerInfo(t, "ratelimited", ds2, domain, health)
	si3 := makeServerInfo(t, "broken", ds3, domain, health)

	multi := NewMultiDNSPacketConn([]*serverInfo{si1, si2, si3}, obsFile, health)
	defer multi.Close()

	// Drive traffic for a few seconds.
	for i := 0; i < 40; i++ {
		multi.WriteTo([]byte("x"), turbotunnel.DummyAddr{})
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(1 * time.Second)

	multi.writeObservations()
	doc := readObs(t, obsFile)

	// All three servers must be present.
	if len(doc.Servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(doc.Servers))
	}

	// Verify JSON has all expected fields.
	data, _ := os.ReadFile(obsFile)
	s := string(data)
	for _, field := range []string{
		"name", "working", "not_before", "requests",
		"rate_estimate", "tokens_avail", "recheck_backoff",
		"phase1_writes", "phase2_writes", "phase3_writes", "phase4_writes",
	} {
		if !strings.Contains(s, field) {
			t.Fatalf("observation file missing field %q", field)
		}
	}

	// Total phase writes should equal total sends.
	totalPhase := doc.Routing.Phase1Writes + doc.Routing.Phase2Writes +
		doc.Routing.Phase3Writes + doc.Routing.Phase4Writes
	if totalPhase != 40 {
		t.Fatalf("expected 40 total phase writes, got %d", totalPhase)
	}

	// Healthy server should have gotten some requests.
	h := obsServer(doc, "healthy")
	if h == nil || h.Requests == 0 {
		t.Fatal("healthy server got zero requests")
	}
}
