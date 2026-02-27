package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"www.bamsoftware.com/git/dnstt.git/turbotunnel"
)

// errMultiConnClosed is returned by ReadFrom after the MultiDNSPacketConn has
// been closed.
var errMultiConnClosed = errors.New("MultiDNSPacketConn closed")

// HealthConfig holds tunable parameters for health checking and load balancing.
type HealthConfig struct {
	// HealthInterval is how often the health loop runs to assess servers.
	HealthInterval time.Duration
	// HealthTimeout is the duration after a send with no response before a
	// server is marked not working.
	HealthTimeout time.Duration
	// NotBeforeDelay is how long a server is rate-limited after being marked
	// not working by the health loop.
	NotBeforeDelay time.Duration
	// RecheckInterval is how often failed servers are actively re-tested.
	RecheckInterval time.Duration
	// ObservationInterval is how often the observation JSON file is written.
	ObservationInterval time.Duration

	// MaxRecheckInterval is the maximum backoff interval for re-testing
	// persistently failing servers. The backoff starts at RecheckInterval
	// and doubles after each probe, capped at this value.
	MaxRecheckInterval time.Duration

	// AIMD rate estimation parameters.
	AIMDInterval           time.Duration // how often the AIMD loop runs
	InitialRate            float64       // initial estimated rate (q/s)
	MinRate                float64       // minimum rate clamp
	MaxRate                float64       // maximum rate clamp
	AdditiveIncrease       float64       // rate increase per successful tick
	MultiplicativeDecrease float64       // rate multiplier on failure
	MaxTokens              float64       // token bucket capacity
	// FailureRatioThreshold is the minimum failure ratio in an AIMD tick
	// required to trigger multiplicative decrease. For example, 0.1 means
	// the rate is only reduced when more than 10% of responses failed.
	FailureRatioThreshold float64

	// FailureCountThreshold is the number of consecutive invalid responses
	// required before a server is marked not-working. This prevents
	// flapping on transient DNS errors (e.g. occasional SERVFAIL).
	FailureCountThreshold int32
}

// DefaultHealthConfig returns sensible defaults for health checking.
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		HealthInterval:      1 * time.Second,
		HealthTimeout:       2 * time.Second,
		NotBeforeDelay:      5 * time.Second,
		RecheckInterval:     15 * time.Second,
		ObservationInterval: 5 * time.Second,

		MaxRecheckInterval: 5 * time.Minute,

		AIMDInterval:           2 * time.Second,
		InitialRate:            10.0,
		MinRate:                0.5,
		MaxRate:                1000.0,
		AdditiveIncrease:       2.0,
		MultiplicativeDecrease: 0.5,
		MaxTokens:              20.0,
		FailureRatioThreshold:  0.1,
		FailureCountThreshold:  3,
	}
}

// serverInfo tracks per-server state for the multi-server client.
type serverInfo struct {
	name         string
	dnsConn      *DNSPacketConn
	addr         net.Addr
	requestsSent int64        // accessed via atomic
	working      int32        // atomic boolean: 1 = working, 0 = not working
	notBefore    atomic.Value // time.Time
	lastSend     atomic.Value // time.Time
	lastResp     atomic.Value // time.Time

	// consecutiveFailures tracks back-to-back invalid responses. Only
	// after reaching the configured threshold is the server condemned.
	consecutiveFailures int32 // atomic

	// AIMD rate estimation and token bucket state.
	rateEstimate float64    // AIMD-controlled safe rate (q/s)
	tokens       float64    // current token bucket level
	lastRefill   time.Time  // for lazy refill calculation
	rateMu       sync.Mutex // protects rateEstimate, tokens, lastRefill
	successCount int64      // atomic: successes since last AIMD tick
	failureCount int64      // atomic: failures since last AIMD tick

	// Exponential backoff state for the recheck loop.
	recheckBackoff time.Duration // current backoff; doubles after each probe
	lastRecheck    time.Time     // when this server was last re-tested
}

// tryConsumeToken performs a lazy refill of the token bucket and attempts to
// consume one token. Returns true if a token was available, false otherwise.
func (si *serverInfo) tryConsumeToken(maxTokens float64) bool {
	si.rateMu.Lock()
	defer si.rateMu.Unlock()
	now := time.Now()
	elapsed := now.Sub(si.lastRefill).Seconds()
	si.tokens = math.Min(si.tokens+elapsed*si.rateEstimate, maxTokens)
	si.lastRefill = now
	if si.tokens >= 1.0 {
		si.tokens -= 1.0
		return true
	}
	return false
}

// peekTokens returns the projected token level without modifying any state.
// This avoids the side-effect of advancing lastRefill for servers that are
// only being considered as candidates, not actually selected.
func (si *serverInfo) peekTokens(maxTokens float64) float64 {
	si.rateMu.Lock()
	defer si.rateMu.Unlock()
	elapsed := time.Since(si.lastRefill).Seconds()
	return math.Min(si.tokens+elapsed*si.rateEstimate, maxTokens)
}

// serverCandidate pairs a serverInfo with its available token count for
// weighted selection in WriteTo.
type serverCandidate struct {
	si     *serverInfo
	tokens float64
}

// taggedIncoming bundles an incoming packet with the server index it came from.
type taggedIncoming struct {
	p         []byte
	addr      net.Addr
	serverIdx int
}

// MultiDNSPacketConn presents a single net.PacketConn backed by multiple
// DNSPacketConn instances. It routes outgoing packets to working servers and
// aggregates incoming packets from all servers.
type MultiDNSPacketConn struct {
	servers   []*serverInfo
	incoming  chan taggedIncoming
	rrobin    uint32
	obsFile   string
	health    HealthConfig
	stop      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	// candidatesPool recycles candidate slices to reduce GC pressure in
	// the WriteTo hot path.
	candidatesPool sync.Pool

	// Per-phase write counters for routing observability.
	phase1Writes int64 // atomic: token-weighted selection
	phase2Writes int64 // atomic: any working server (overdraft)
	phase3Writes int64 // atomic: any non-rate-limited server
	phase4Writes int64 // atomic: last-resort fallback
}

// loadPriorRates reads and parses the observation JSON file, returning a map
// of server name → rate_estimate. Returns nil on any error (file missing,
// corrupt JSON, etc). Reuses the same JSON shape that writeObservations produces.
func loadPriorRates(path string) map[string]float64 {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var doc struct {
		Servers []struct {
			Name         string  `json:"name"`
			RateEstimate float64 `json:"rate_estimate"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		return nil
	}
	if len(doc.Servers) == 0 {
		return nil
	}
	m := make(map[string]float64, len(doc.Servers))
	for _, s := range doc.Servers {
		m[s.Name] = s.RateEstimate
	}
	return m
}

// archiveObsFile renames an existing observation file to preserve it as a
// historical record. The archived name embeds the file's modification time,
// e.g. "obs.json" → "obs.2026-02-26T19-50-11.json".
func archiveObsFile(path string) {
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return // doesn't exist or not accessible — nothing to archive
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	ts := info.ModTime().Format("2006-01-02T15-04-05")
	archived := fmt.Sprintf("%s.%s%s", base, ts, ext)
	if err := os.Rename(path, archived); err != nil {
		log.Printf("archiveObsFile: %v", err)
	}
}

// NewMultiDNSPacketConn constructs a MultiDNSPacketConn. It starts goroutines
// to forward incoming packets from each underlying DNSPacketConn and an
// observation writer that periodically writes status to obsFile.
func NewMultiDNSPacketConn(servers []*serverInfo, obsFile string, health HealthConfig) *MultiDNSPacketConn {
	priorRates := loadPriorRates(obsFile)
	archiveObsFile(obsFile)
	nServers := len(servers)
	m := &MultiDNSPacketConn{
		servers:   servers,
		incoming: make(chan taggedIncoming, 1024),
		obsFile:  obsFile,
		health:   health,
		stop:     make(chan struct{}),
		candidatesPool: sync.Pool{
			New: func() interface{} {
				return make([]serverCandidate, 0, nServers)
			},
		},
	}

	now := time.Now()
	for i, s := range servers {
		// Bug 1 fix: assume servers are working until proven otherwise.
		atomic.StoreInt32(&s.working, 1)
		s.lastResp.Store(time.Time{})
		// Initialize AIMD rate estimation state, seeding from prior
		// observation if available.
		s.rateEstimate = health.InitialRate
		if rate, ok := priorRates[s.name]; ok {
			if rate < health.MinRate {
				rate = health.MinRate
			}
			if rate > health.MaxRate {
				rate = health.MaxRate
			}
			s.rateEstimate = rate
		}
		s.tokens = health.MaxTokens
		s.lastRefill = now

		// Bug 3 fix: reader goroutines are tracked via WaitGroup and
		// stop when the underlying dnsConn is closed.
		m.wg.Add(1)
		go func(idx int, si *serverInfo) {
			defer m.wg.Done()
			buf := make([]byte, 65536)
			for {
				n, addr, err := si.dnsConn.ReadFrom(buf)
				if err != nil {
					// Check if we're shutting down before logging.
					select {
					case <-m.stop:
						return
					default:
					}
					log.Printf("server %s ReadFrom error: %v", si.name, err)
					atomic.StoreInt32(&si.working, 0)
					return
				}
				p := make([]byte, n)
				copy(p, buf[:n])
				si.lastResp.Store(time.Now())
				atomic.StoreInt32(&si.consecutiveFailures, 0)
				atomic.StoreInt32(&si.working, 1)
				select {
				case m.incoming <- taggedIncoming{p, addr, idx}:
				case <-m.stop:
					return
				}
			}
		}(i, s)
	}

	// Start health loop, recheck loop, AIMD loop, and observation writer.
	m.wg.Add(4)
	go m.healthLoop()
	go m.recheckLoop()
	go m.aimdLoop()
	go m.observationLoop()
	return m
}

// aimdLoop periodically adjusts each server's rate estimate using AIMD
// (additive increase, multiplicative decrease) based on accumulated
// success/failure counters.
func (m *MultiDNSPacketConn) aimdLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.health.AIMDInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for _, si := range m.servers {
				successes := atomic.SwapInt64(&si.successCount, 0)
				failures := atomic.SwapInt64(&si.failureCount, 0)
				si.rateMu.Lock()
				if failures > 0 {
					total := float64(successes + failures)
					failureRatio := float64(failures) / total
					if failureRatio > m.health.FailureRatioThreshold {
						si.rateEstimate *= m.health.MultiplicativeDecrease
					} else if successes > 0 {
						si.rateEstimate += m.health.AdditiveIncrease
					}
				} else if successes > 0 {
					si.rateEstimate += m.health.AdditiveIncrease
				}
				// Clamp to [MinRate, MaxRate].
				if si.rateEstimate < m.health.MinRate {
					si.rateEstimate = m.health.MinRate
				}
				if si.rateEstimate > m.health.MaxRate {
					si.rateEstimate = m.health.MaxRate
				}
				si.rateMu.Unlock()
			}
		case <-m.stop:
			return
		}
	}
}

// recheckLoop actively re-tests servers that are marked not working by
// triggering an empty DNS poll. Unlike replaying real packets (which contain
// KCP Conv IDs and cause the server to create zombie sessions), an empty poll
// encodes only the ClientID + random padding — the server's recvLoop skips it
// via nextPacket without calling QueueIncoming, but still sends back a DNS
// response that triggers OnResponseReceived for recovery detection.
func (m *MultiDNSPacketConn) recheckLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.health.RecheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			for _, si := range m.servers {
				if atomic.LoadInt32(&si.working) == 1 {
					// Server recovered; reset its backoff.
					si.recheckBackoff = 0
					continue
				}
				nb := m.getNotBefore(si)
				if !nb.IsZero() && nb.After(now) {
					continue
				}
				// Exponential backoff: skip this server if its
				// per-server backoff hasn't elapsed yet.
				if si.recheckBackoff == 0 {
					si.recheckBackoff = m.health.RecheckInterval
				}
				if !si.lastRecheck.IsZero() && now.Sub(si.lastRecheck) < si.recheckBackoff {
					continue
				}
				si.lastSend.Store(now)
				si.lastRecheck = now
				atomic.AddInt64(&si.requestsSent, 1)
				si.dnsConn.RequestPoll()
				// Double the backoff for next probe, capped at max.
				si.recheckBackoff *= 2
				if si.recheckBackoff > m.health.MaxRecheckInterval {
					si.recheckBackoff = m.health.MaxRecheckInterval
				}
			}
		case <-m.stop:
			return
		}
	}
}

// ReadFrom implements net.PacketConn by returning packets received from any
// underlying server.
// Bug 2 fix: returns errMultiConnClosed when the connection is closed instead
// of blocking forever.
func (m *MultiDNSPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case ti, ok := <-m.incoming:
		if !ok {
			return 0, nil, errMultiConnClosed
		}
		n := copy(p, ti.p)
		return n, ti.addr, nil
	case <-m.stop:
		return 0, nil, errMultiConnClosed
	}
}

// healthLoop periodically assesses servers for delayed/no/invalid replies.
// Improve 12: logs state transitions.
func (m *MultiDNSPacketConn) healthLoop() {
	defer m.wg.Done()
	t := time.NewTicker(m.health.HealthInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			now := time.Now()
			for _, si := range m.servers {
				v := si.lastSend.Load()
				if v == nil {
					continue
				}
				lastSend := v.(time.Time)
				v2 := si.lastResp.Load()
				var lastResp time.Time
				if v2 != nil {
					lastResp = v2.(time.Time)
				}
				if lastResp.Before(lastSend) && now.Sub(lastSend) > m.health.HealthTimeout {
					atomic.AddInt64(&si.failureCount, 1)
					if atomic.CompareAndSwapInt32(&si.working, 1, 0) {
						log.Printf("server %s marked not working (no response for %v)", si.name, now.Sub(lastSend))
					}
					si.notBefore.Store(now.Add(m.health.NotBeforeDelay))
					continue
				}
				if atomic.LoadInt32(&si.working) == 0 && !lastResp.IsZero() && now.Sub(lastResp) < m.health.HealthTimeout {
					if atomic.CompareAndSwapInt32(&si.working, 0, 1) {
						log.Printf("server %s recovered (response received %v ago)", si.name, now.Sub(lastResp))
					}
					si.notBefore.Store(time.Time{})
				}
			}
		case <-m.stop:
			return
		}
	}
}

// WriteTo routes outgoing packets to a working server using token-weighted
// selection. It prefers servers with available tokens, falling back to any
// working server (allowing token overdraft), then any server as last resort.
// This method never blocks.
func (m *MultiDNSPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	now := time.Now()
	nServers := len(m.servers)
	if nServers == 0 {
		return 0, nil
	}

	maxTokens := m.health.MaxTokens

	// Phase 1: Among working servers with tokens >= 1.0, weighted-random
	// by token count. Uses peekTokens (read-only) to avoid advancing
	// lastRefill on servers that aren't ultimately selected.
	candidates := m.candidatesPool.Get().([]serverCandidate)
	candidates = candidates[:0]
	for _, si := range m.servers {
		nb := m.getNotBefore(si)
		if atomic.LoadInt32(&si.working) == 1 && (nb.IsZero() || nb.Before(now)) {
			avail := si.peekTokens(maxTokens)
			if avail >= 1.0 {
				candidates = append(candidates, serverCandidate{si, avail})
			}
		}
	}
	if len(candidates) > 0 {
		chosen := weightedSelectCandidate(candidates)
		m.candidatesPool.Put(candidates)
		si := chosen.si
		si.tryConsumeToken(maxTokens)
		si.lastSend.Store(now)
		atomic.AddInt64(&si.requestsSent, 1)
		atomic.AddInt64(&m.phase1Writes, 1)
		return si.dnsConn.WriteTo(p, si.addr)
	}
	m.candidatesPool.Put(candidates)

	// Phase 2: Any working server (allow token overdraft).
	start := int(atomic.AddUint32(&m.rrobin, 1)-1) % nServers
	for i := 0; i < nServers; i++ {
		idx := (start + i) % nServers
		si := m.servers[idx]
		nb := m.getNotBefore(si)
		if atomic.LoadInt32(&si.working) == 1 && (nb.IsZero() || nb.Before(now)) {
			si.lastSend.Store(now)
			atomic.AddInt64(&si.requestsSent, 1)
			atomic.AddInt64(&m.phase2Writes, 1)
			return si.dnsConn.WriteTo(p, si.addr)
		}
	}

	// Phase 3: Any server that is not rate-limited.
	for i := 0; i < nServers; i++ {
		idx := (start + i) % nServers
		si := m.servers[idx]
		nb := m.getNotBefore(si)
		if nb.IsZero() || nb.Before(now) {
			si.lastSend.Store(now)
			atomic.AddInt64(&si.requestsSent, 1)
			atomic.AddInt64(&m.phase3Writes, 1)
			return si.dnsConn.WriteTo(p, si.addr)
		}
	}

	// Phase 4: Last resort — any server regardless of state.
	si := m.servers[start]
	si.lastSend.Store(now)
	atomic.AddInt64(&si.requestsSent, 1)
	atomic.AddInt64(&m.phase4Writes, 1)
	return si.dnsConn.WriteTo(p, si.addr)
}

// weightedSelectCandidate selects a candidate using weighted-random selection
// based on token counts, reusing sampleWeighted from weightedlist.go.
func weightedSelectCandidate(candidates []serverCandidate) serverCandidate {
	if len(candidates) == 1 {
		return candidates[0]
	}
	weights := make([]uint32, len(candidates))
	for i, c := range candidates {
		w := uint32(c.tokens)
		if w < 1 {
			w = 1
		}
		weights[i] = w
	}
	idx := sampleWeighted(weights)
	return candidates[idx]
}

func (m *MultiDNSPacketConn) getNotBefore(si *serverInfo) time.Time {
	v := si.notBefore.Load()
	if v == nil {
		return time.Time{}
	}
	return v.(time.Time)
}

// observationLoop periodically writes a JSON file with server observations.
func (m *MultiDNSPacketConn) observationLoop() {
	defer m.wg.Done()
	t := time.NewTicker(m.health.ObservationInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.writeObservations()
		case <-m.stop:
			// Write a final observation before exiting.
			m.writeObservations()
			return
		}
	}
}

func (m *MultiDNSPacketConn) writeObservations() {
	if m.obsFile == "" {
		return
	}
	type serverObs struct {
		Name           string  `json:"name"`
		Working        bool    `json:"working"`
		NotBefore      string  `json:"not_before"`
		Requests       int64   `json:"requests"`
		RateEstimate   float64 `json:"rate_estimate"`
		TokensAvail    float64 `json:"tokens_avail"`
		RecheckBackoff string  `json:"recheck_backoff"`
	}
	type routingObs struct {
		Phase1Writes int64 `json:"phase1_writes"`
		Phase2Writes int64 `json:"phase2_writes"`
		Phase3Writes int64 `json:"phase3_writes"`
		Phase4Writes int64 `json:"phase4_writes"`
	}
	type observations struct {
		Servers []serverObs `json:"servers"`
		Routing routingObs  `json:"routing"`
	}
	var servers []serverObs
	for _, si := range m.servers {
		nb := m.getNotBefore(si)
		si.rateMu.Lock()
		rate := si.rateEstimate
		tokens := si.tokens
		si.rateMu.Unlock()
		servers = append(servers, serverObs{
			Name:           si.name,
			Working:        atomic.LoadInt32(&si.working) == 1,
			NotBefore:      nb.Format(time.RFC3339Nano),
			Requests:       atomic.LoadInt64(&si.requestsSent),
			RateEstimate:   rate,
			TokensAvail:    tokens,
			RecheckBackoff: si.recheckBackoff.String(),
		})
	}
	out := observations{
		Servers: servers,
		Routing: routingObs{
			Phase1Writes: atomic.LoadInt64(&m.phase1Writes),
			Phase2Writes: atomic.LoadInt64(&m.phase2Writes),
			Phase3Writes: atomic.LoadInt64(&m.phase3Writes),
			Phase4Writes: atomic.LoadInt64(&m.phase4Writes),
		},
	}

	// Atomic write: write to a temp file then rename, so readers never
	// see a truncated or partially written file.
	tmpFile := m.obsFile + ".tmp"
	f, err := os.Create(tmpFile)
	if err != nil {
		log.Printf("writeObservations: %v", err)
		return
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		f.Close()
		os.Remove(tmpFile)
		log.Printf("writeObservations encode: %v", err)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpFile)
		log.Printf("writeObservations close: %v", err)
		return
	}
	if err := os.Rename(tmpFile, m.obsFile); err != nil {
		os.Remove(tmpFile)
		log.Printf("writeObservations rename: %v", err)
	}
}

// Close shuts down the MultiDNSPacketConn and all underlying connections.
// Bug 4 fix: safe to call multiple times.
// Improve 11: waits for all goroutines to finish.
func (m *MultiDNSPacketConn) Close() error {
	m.closeOnce.Do(func() {
		close(m.stop)
		for _, si := range m.servers {
			si.dnsConn.Close()
		}
	})
	m.wg.Wait()
	return nil
}

// LocalAddr implements net.PacketConn.LocalAddr.
// Improve 13: returns a valid DummyAddr instead of nil.
func (m *MultiDNSPacketConn) LocalAddr() net.Addr {
	return turbotunnel.DummyAddr{}
}

func (m *MultiDNSPacketConn) SetDeadline(t time.Time) error      { return nil }
func (m *MultiDNSPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *MultiDNSPacketConn) SetWriteDeadline(t time.Time) error { return nil }

// wireHealthCallbacks sets up OnInvalidResponse / OnResponseReceived /
// OnRateLimit / OnResponse callbacks on the underlying connections so that
// serverInfo health state is kept up to date. httpConn may be nil for non-DoH
// transports. health provides the configured delay values.
func wireHealthCallbacks(si *serverInfo, dnsConn *DNSPacketConn, httpConn *HTTPPacketConn, health HealthConfig) {
	dnsConn.OnInvalidResponse = func(err error) {
		n := atomic.AddInt32(&si.consecutiveFailures, 1)
		if n >= health.FailureCountThreshold {
			atomic.AddInt64(&si.failureCount, 1)
			if atomic.CompareAndSwapInt32(&si.working, 1, 0) {
				log.Printf("server %s marked not working (invalid response)", si.name)
			}
			si.notBefore.Store(time.Now().Add(health.NotBeforeDelay))
		}
	}
	dnsConn.OnResponseReceived = func() {
		atomic.StoreInt32(&si.consecutiveFailures, 0)
		atomic.AddInt64(&si.successCount, 1)
		if atomic.CompareAndSwapInt32(&si.working, 0, 1) {
			log.Printf("server %s recovered (valid response)", si.name)
		}
		si.lastResp.Store(time.Now())
		si.notBefore.Store(time.Time{})
	}
	if httpConn != nil {
		httpConn.OnRateLimit = func(t time.Time) {
			atomic.AddInt64(&si.failureCount, 1)
			si.notBefore.Store(t)
			if atomic.CompareAndSwapInt32(&si.working, 1, 0) {
				log.Printf("server %s marked not working (rate limited)", si.name)
			}
		}
		httpConn.OnResponse = func() {
			atomic.AddInt64(&si.successCount, 1)
			if atomic.CompareAndSwapInt32(&si.working, 0, 1) {
				log.Printf("server %s recovered (HTTP 200)", si.name)
			}
			si.notBefore.Store(time.Time{})
		}
	}
}
