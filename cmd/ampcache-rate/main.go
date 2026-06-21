package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gomodstore/internal/ampcache"
)

const defaultPayloadBytes = 524288

type config struct {
	serverURL          string
	resourceBase       string
	cacheDomain        string
	cacheTLSServerName string
	userAgent          string
	mode               string
	payloadFill        string
	payloadBytes       int
	originWorkers      int
	cachedWorkers      int
	cachedURLs         int
	secondDuration     time.Duration
	minuteDuration     time.Duration
	originTimeout      time.Duration
	cachedTimeout      time.Duration
	publishTimeout     time.Duration
	pollInterval       time.Duration
	runID              string
	jsonOutput         bool
	verbose            bool
}

type report struct {
	StartedAt          time.Time          `json:"started_at"`
	ServerURL          string             `json:"server_url"`
	ResourceBase       string             `json:"resource_base"`
	UserAgent          string             `json:"user_agent"`
	RunID              string             `json:"run_id"`
	PayloadFill        string             `json:"payload_fill"`
	PayloadBytes       int                `json:"payload_bytes"`
	EncodedFontBytes   int                `json:"encoded_font_bytes"`
	PayloadSHA256      string             `json:"payload_sha256"`
	OriginWorkers      int                `json:"origin_workers"`
	CachedWorkers      int                `json:"cached_workers"`
	PreparedCachedURLs []preparedResource `json:"prepared_cached_urls,omitempty"`
	Phases             []phaseResult      `json:"phases"`
}

type preparedResource struct {
	ResourceURL string `json:"resource_url"`
	CacheURL    string `json:"cache_url"`
	SHA256      string `json:"sha256"`
	Length      int64  `json:"length"`
}

type phaseResult struct {
	Name                string         `json:"name"`
	Kind                string         `json:"kind"`
	Duration            string         `json:"duration"`
	Workers             int            `json:"workers"`
	Started             int64          `json:"started"`
	Succeeded           int64          `json:"succeeded"`
	SucceededInWindow   int64          `json:"succeeded_in_window"`
	Failed              int64          `json:"failed"`
	ElapsedMillis       int64          `json:"elapsed_millis"`
	InWindowPerSecond   float64        `json:"in_window_per_second"`
	TotalPerSecond      float64        `json:"total_per_second"`
	BytesRead           int64          `json:"bytes_read,omitempty"`
	StatusCounts        map[string]int `json:"status_counts,omitempty"`
	ErrorCounts         map[string]int `json:"error_counts,omitempty"`
	RepresentativeError []string       `json:"representative_errors,omitempty"`
}

type aggregate struct {
	mu           sync.Mutex
	statusCounts map[string]int
	errorCounts  map[string]int
	errorSamples []string
}

type publishSession struct {
	resp  *http.Response
	dec   *json.Decoder
	ready ampcache.PublishEvent
}

type attemptResult struct {
	ok        bool
	status    string
	err       error
	completed time.Time
	bytesRead int64
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ampcache-rate: %v\n", err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ampcache-rate: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("ampcache-rate", flag.ExitOnError)
	fs.StringVar(&cfg.serverURL, "server", "", "ampcache server URL")
	fs.StringVar(&cfg.resourceBase, "resource-base", "", "absolute public origin URL prefix for generated .ttf resources; defaults to -server")
	fs.StringVar(&cfg.cacheDomain, "cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain used for TLS domain fronting")
	fs.StringVar(&cfg.cacheTLSServerName, "cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache requests; empty disables domain fronting")
	fs.StringVar(&cfg.userAgent, "user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache requests; empty uses Go default")
	fs.StringVar(&cfg.mode, "mode", "all", "test mode: all, origin, or cached")
	fs.StringVar(&cfg.payloadFill, "payload-fill", "pattern", "payload fill mode: pattern or random")
	fs.IntVar(&cfg.payloadBytes, "payload-bytes", defaultPayloadBytes, "payload bytes to POST before font encoding")
	fs.IntVar(&cfg.originWorkers, "origin-workers", 8, "concurrent origin-fetch publish attempts")
	fs.IntVar(&cfg.cachedWorkers, "cached-workers", 16, "concurrent cached GET workers")
	fs.IntVar(&cfg.cachedURLs, "cached-urls", 1, "number of cached URLs to prepare for cached GET tests")
	fs.DurationVar(&cfg.secondDuration, "second-duration", time.Second, "short rate window")
	fs.DurationVar(&cfg.minuteDuration, "minute-duration", time.Minute, "long rate window")
	fs.DurationVar(&cfg.originTimeout, "origin-timeout", 45*time.Second, "timeout for one origin-fetch attempt")
	fs.DurationVar(&cfg.cachedTimeout, "cached-timeout", 30*time.Second, "timeout for one cached GET")
	fs.DurationVar(&cfg.publishTimeout, "publish-timeout", 3*time.Minute, "timeout for preparing one cached resource")
	fs.DurationVar(&cfg.pollInterval, "poll", 500*time.Millisecond, "AMP Cache poll interval")
	fs.StringVar(&cfg.runID, "run-id", "", "run identifier to include in generated resource URLs")
	fs.BoolVar(&cfg.jsonOutput, "json", false, "print JSON report")
	fs.BoolVar(&cfg.verbose, "v", false, "print progress logs")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if cfg.serverURL == "" {
		return cfg, errors.New("-server is required")
	}
	if cfg.resourceBase == "" {
		cfg.resourceBase = cfg.serverURL
	}
	switch cfg.mode {
	case "all", "origin", "cached":
	default:
		return cfg, errors.New("-mode must be all, origin, or cached")
	}
	switch cfg.payloadFill {
	case "pattern", "random":
	default:
		return cfg, errors.New("-payload-fill must be pattern or random")
	}
	if cfg.payloadBytes <= 0 {
		return cfg, errors.New("-payload-bytes must be greater than zero")
	}
	if cfg.originWorkers <= 0 {
		return cfg, errors.New("-origin-workers must be greater than zero")
	}
	if cfg.cachedWorkers <= 0 {
		return cfg, errors.New("-cached-workers must be greater than zero")
	}
	if cfg.cachedURLs <= 0 {
		return cfg, errors.New("-cached-urls must be greater than zero")
	}
	for name, d := range map[string]time.Duration{
		"second-duration": cfg.secondDuration,
		"minute-duration": cfg.minuteDuration,
		"origin-timeout":  cfg.originTimeout,
		"cached-timeout":  cfg.cachedTimeout,
		"publish-timeout": cfg.publishTimeout,
		"poll":            cfg.pollInterval,
	} {
		if d <= 0 {
			return cfg, fmt.Errorf("-%s must be greater than zero", name)
		}
	}
	if cfg.runID == "" {
		cfg.runID = "rate-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		cfg.runID = strings.ReplaceAll(cfg.runID, ".", "-")
	}
	if _, err := parseAbsoluteHTTPURL("-server", cfg.serverURL); err != nil {
		return cfg, err
	}
	if _, err := parseResourceBase(cfg.resourceBase); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func run(cfg config) error {
	logf := func(string, ...any) {}
	if cfg.verbose {
		logger := log.New(os.Stderr, "ampcache-rate: ", log.LstdFlags)
		logf = logger.Printf
	}

	payload, err := makePayload(cfg.payloadBytes, cfg.runID, cfg.payloadFill)
	if err != nil {
		return err
	}
	payloadSum := sha256.Sum256(payload)
	sampleURL, err := makeResourceURL(cfg.resourceBase, cfg.runID, "sample", 0)
	if err != nil {
		return err
	}
	encoded, _, err := ampcache.EncodeResource(payload, ampcache.EncodingFont, sampleURL)
	if err != nil {
		return fmt.Errorf("encode sample font resource: %w", err)
	}

	rep := report{
		StartedAt:        time.Now().UTC(),
		ServerURL:        cfg.serverURL,
		ResourceBase:     cfg.resourceBase,
		UserAgent:        cfg.userAgent,
		RunID:            cfg.runID,
		PayloadFill:      cfg.payloadFill,
		PayloadBytes:     len(payload),
		EncodedFontBytes: len(encoded),
		PayloadSHA256:    hex.EncodeToString(payloadSum[:]),
		OriginWorkers:    cfg.originWorkers,
		CachedWorkers:    cfg.cachedWorkers,
	}
	logf("payload=%d fill=%s encoded_font=%d run_id=%s", rep.PayloadBytes, rep.PayloadFill, rep.EncodedFontBytes, rep.RunID)

	client := ampcache.NewDomainFrontingClientWithUserAgent(cfg.cacheDomain, cfg.cacheTLSServerName, cfg.userAgent)
	if cfg.mode == "all" || cfg.mode == "origin" {
		for _, window := range []struct {
			name     string
			duration time.Duration
		}{
			{name: "origin-fetch-1s", duration: cfg.secondDuration},
			{name: "origin-fetch-1m", duration: cfg.minuteDuration},
		} {
			logf("starting %s duration=%s workers=%d", window.name, window.duration, cfg.originWorkers)
			phase := runOriginPhase(cfg, client, payload, window.name, window.duration)
			rep.Phases = append(rep.Phases, phase)
			logf("finished %s started=%d success=%d in_window=%d failed=%d", phase.Name, phase.Started, phase.Succeeded, phase.SucceededInWindow, phase.Failed)
		}
	}

	if cfg.mode == "all" || cfg.mode == "cached" {
		prepared, err := prepareCachedResources(cfg, client, payload, logf)
		if err != nil {
			return err
		}
		rep.PreparedCachedURLs = prepared
		for _, window := range []struct {
			name     string
			duration time.Duration
		}{
			{name: "cached-get-1s", duration: cfg.secondDuration},
			{name: "cached-get-1m", duration: cfg.minuteDuration},
		} {
			logf("starting %s duration=%s workers=%d urls=%d", window.name, window.duration, cfg.cachedWorkers, len(prepared))
			phase := runCachedPhase(cfg, client, prepared, window.name, window.duration)
			rep.Phases = append(rep.Phases, phase)
			logf("finished %s started=%d success=%d in_window=%d failed=%d", phase.Name, phase.Started, phase.Succeeded, phase.SucceededInWindow, phase.Failed)
		}
	}

	if cfg.jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printReport(rep)
	return nil
}

func runOriginPhase(cfg config, client *http.Client, payload []byte, name string, duration time.Duration) phaseResult {
	var started atomic.Int64
	var succeeded atomic.Int64
	var succeededInWindow atomic.Int64
	var failed atomic.Int64
	var bytesRead atomic.Int64
	agg := newAggregate()
	phaseStart := time.Now()
	deadline := phaseStart.Add(duration)
	var wg sync.WaitGroup
	for worker := 0; worker < cfg.originWorkers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				seq := started.Add(1)
				resourceURL, err := makeResourceURL(cfg.resourceBase, cfg.runID, name, uint64(seq))
				if err != nil {
					failed.Add(1)
					agg.addError(err)
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), cfg.originTimeout)
				result := runOriginAttempt(ctx, client, cfg.serverURL, resourceURL, payload)
				cancel()
				bytesRead.Add(result.bytesRead)
				if result.status != "" {
					agg.addStatus(result.status)
				}
				if result.ok {
					succeeded.Add(1)
					if !result.completed.After(deadline) {
						succeededInWindow.Add(1)
					}
				} else {
					failed.Add(1)
					agg.addError(result.err)
				}
			}
		}()
	}
	wg.Wait()
	return finishPhase(name, "origin-fetch", duration, cfg.originWorkers, phaseStart, started.Load(), succeeded.Load(), succeededInWindow.Load(), failed.Load(), bytesRead.Load(), agg)
}

func runOriginAttempt(ctx context.Context, client *http.Client, serverURL, resourceURL string, payload []byte) attemptResult {
	session, err := openPublishSession(ctx, client, serverURL, resourceURL, payload)
	if err != nil {
		return attemptResult{err: err, completed: time.Now()}
	}
	defer session.close()

	triggerDone := make(chan attemptResult, 1)
	go func() {
		triggerDone <- triggerCacheFetch(ctx, client, session.ready.CacheURL)
	}()

	for {
		var event ampcache.PublishEvent
		if err := session.dec.Decode(&event); err != nil {
			trigger := <-triggerDone
			if trigger.status != "" {
				return attemptResult{status: trigger.status, err: fmt.Errorf("waiting for fetched event: %w", err), completed: time.Now(), bytesRead: trigger.bytesRead}
			}
			return attemptResult{err: fmt.Errorf("waiting for fetched event: %w", err), completed: time.Now()}
		}
		switch event.Event {
		case "fetched":
			trigger := <-triggerDone
			return attemptResult{ok: true, status: trigger.status, completed: time.Now(), bytesRead: trigger.bytesRead}
		case "cached":
			trigger := <-triggerDone
			return attemptResult{ok: true, status: trigger.status, completed: time.Now(), bytesRead: trigger.bytesRead}
		case "error":
			trigger := <-triggerDone
			return attemptResult{status: trigger.status, err: errors.New(event.Error), completed: time.Now(), bytesRead: trigger.bytesRead}
		default:
			trigger := <-triggerDone
			return attemptResult{status: trigger.status, err: fmt.Errorf("unexpected publish event %q", event.Event), completed: time.Now(), bytesRead: trigger.bytesRead}
		}
	}
}

func openPublishSession(ctx context.Context, client *http.Client, serverURL, resourceURL string, payload []byte) (*publishSession, error) {
	endpoint, err := makePublishEndpoint(serverURL, resourceURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("publish failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	dec := json.NewDecoder(resp.Body)
	var ready ampcache.PublishEvent
	if err := dec.Decode(&ready); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("read ready event: %w", err)
	}
	if ready.Event != "ready" {
		resp.Body.Close()
		return nil, fmt.Errorf("expected ready event, got %q", ready.Event)
	}
	return &publishSession{resp: resp, dec: dec, ready: ready}, nil
}

func (s *publishSession) close() {
	if s != nil && s.resp != nil {
		_ = s.resp.Body.Close()
	}
}

func triggerCacheFetch(ctx context.Context, client *http.Client, cacheURL string) attemptResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cacheURL, nil)
	if err != nil {
		return attemptResult{err: err, completed: time.Now()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return attemptResult{err: err, completed: time.Now()}
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	return attemptResult{ok: resp.StatusCode == http.StatusOK, status: resp.Status, completed: time.Now(), bytesRead: n}
}

func prepareCachedResources(cfg config, client *http.Client, payload []byte, logf func(string, ...any)) ([]preparedResource, error) {
	prepared := make([]preparedResource, 0, cfg.cachedURLs)
	for i := 0; i < cfg.cachedURLs; i++ {
		resourceURL, err := makeResourceURL(cfg.resourceBase, cfg.runID, "cached-seed", uint64(i+1))
		if err != nil {
			return nil, err
		}
		logf("preparing cached resource %d/%d url=%s", i+1, cfg.cachedURLs, resourceURL)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.publishTimeout)
		result, err := ampcache.Publish(ctx, ampcache.PublishOptions{
			ServerURL:    cfg.serverURL,
			ResourceURL:  resourceURL,
			Encoding:     ampcache.EncodingFont,
			Body:         bytes.NewReader(payload),
			HTTPClient:   client,
			Timeout:      cfg.publishTimeout,
			PollInterval: cfg.pollInterval,
			WaitMode:     "cached",
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("prepare cached resource %s: %w", resourceURL, err)
		}
		prepared = append(prepared, preparedResource{
			ResourceURL: result.ResourceURL,
			CacheURL:    result.CacheURL,
			SHA256:      result.SHA256,
			Length:      result.Length,
		})
	}
	return prepared, nil
}

func runCachedPhase(cfg config, client *http.Client, prepared []preparedResource, name string, duration time.Duration) phaseResult {
	var started atomic.Int64
	var succeeded atomic.Int64
	var succeededInWindow atomic.Int64
	var failed atomic.Int64
	var bytesRead atomic.Int64
	agg := newAggregate()
	phaseStart := time.Now()
	deadline := phaseStart.Add(duration)
	var wg sync.WaitGroup
	for worker := 0; worker < cfg.cachedWorkers; worker++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			index := workerID
			for time.Now().Before(deadline) {
				started.Add(1)
				resource := prepared[index%len(prepared)]
				index++
				ctx, cancel := context.WithTimeout(context.Background(), cfg.cachedTimeout)
				result := runCachedRequest(ctx, client, resource)
				cancel()
				bytesRead.Add(result.bytesRead)
				if result.status != "" {
					agg.addStatus(result.status)
				}
				if result.ok {
					succeeded.Add(1)
					if !result.completed.After(deadline) {
						succeededInWindow.Add(1)
					}
				} else {
					failed.Add(1)
					agg.addError(result.err)
				}
			}
		}(worker)
	}
	wg.Wait()
	return finishPhase(name, "cached-get", duration, cfg.cachedWorkers, phaseStart, started.Load(), succeeded.Load(), succeededInWindow.Load(), failed.Load(), bytesRead.Load(), agg)
}

func runCachedRequest(ctx context.Context, client *http.Client, resource preparedResource) attemptResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resource.CacheURL, nil)
	if err != nil {
		return attemptResult{err: err, completed: time.Now()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return attemptResult{err: err, completed: time.Now()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return attemptResult{status: resp.Status, err: err, completed: time.Now()}
	}
	result := attemptResult{status: resp.Status, completed: time.Now(), bytesRead: int64(len(body))}
	if resp.StatusCode != http.StatusOK {
		result.err = fmt.Errorf("cache returned %s", resp.Status)
		return result
	}
	_, manifest, err := ampcache.DecodeResource(body, ampcache.EncodingFont)
	if err != nil {
		result.err = fmt.Errorf("decode cached font: %w", err)
		return result
	}
	if manifest.Length != resource.Length {
		result.err = fmt.Errorf("decoded length %d, want %d", manifest.Length, resource.Length)
		return result
	}
	if manifest.SHA256 != resource.SHA256 {
		result.err = fmt.Errorf("decoded sha256 %s, want %s", manifest.SHA256, resource.SHA256)
		return result
	}
	result.ok = true
	return result
}

func finishPhase(name, kind string, duration time.Duration, workers int, start time.Time, started, succeeded, succeededInWindow, failed, bytesRead int64, agg *aggregate) phaseResult {
	elapsed := time.Since(start)
	statusCounts, errorCounts, samples := agg.snapshot()
	result := phaseResult{
		Name:                name,
		Kind:                kind,
		Duration:            duration.String(),
		Workers:             workers,
		Started:             started,
		Succeeded:           succeeded,
		SucceededInWindow:   succeededInWindow,
		Failed:              failed,
		ElapsedMillis:       elapsed.Milliseconds(),
		InWindowPerSecond:   float64(succeededInWindow) / duration.Seconds(),
		TotalPerSecond:      float64(succeeded) / elapsed.Seconds(),
		BytesRead:           bytesRead,
		StatusCounts:        statusCounts,
		ErrorCounts:         errorCounts,
		RepresentativeError: samples,
	}
	return result
}

func newAggregate() *aggregate {
	return &aggregate{
		statusCounts: make(map[string]int),
		errorCounts:  make(map[string]int),
	}
}

func (a *aggregate) addStatus(status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statusCounts[status]++
}

func (a *aggregate) addError(err error) {
	if err == nil {
		return
	}
	key := classifyError(err)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.errorCounts[key]++
	if len(a.errorSamples) < 8 {
		a.errorSamples = append(a.errorSamples, err.Error())
	}
}

func (a *aggregate) snapshot() (map[string]int, map[string]int, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	statuses := make(map[string]int, len(a.statusCounts))
	for key, value := range a.statusCounts {
		statuses[key] = value
	}
	errorsByKind := make(map[string]int, len(a.errorCounts))
	for key, value := range a.errorCounts {
		errorsByKind[key] = value
	}
	samples := append([]string(nil), a.errorSamples...)
	return statuses, errorsByKind, samples
}

func classifyError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "429"):
		return "rate_limited_429"
	case strings.Contains(msg, "403"):
		return "forbidden_403"
	case strings.Contains(msg, "404"):
		return "not_found_404"
	case strings.Contains(msg, "context deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "connection reset"):
		return "connection_reset"
	default:
		if len(msg) > 80 {
			return msg[:80]
		}
		return msg
	}
}

func makePayload(size int, seed, fill string) ([]byte, error) {
	payload := make([]byte, size)
	switch fill {
	case "pattern":
		sum := sha256.Sum256([]byte(seed))
		for i := range payload {
			payload[i] = byte((i*131 + int(sum[i%len(sum)])) & 0xff)
		}
	case "random":
		if _, err := rand.Read(payload); err != nil {
			return nil, fmt.Errorf("fill payload with crypto/rand: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported payload fill mode %q", fill)
	}
	return payload, nil
}

func parseResourceBase(value string) (*url.URL, error) {
	u, err := parseAbsoluteHTTPURL("-resource-base", value)
	if err != nil {
		return nil, err
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("-resource-base must not contain query or fragment")
	}
	return u, nil
}

func parseAbsoluteHTTPURL(flagName, value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", flagName, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute http or https URL", flagName)
	}
	return u, nil
}

func makeResourceURL(resourceBase, runID, phase string, seq uint64) (string, error) {
	u, err := parseResourceBase(resourceBase)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimRight(u.Path, "/")
	file := fmt.Sprintf("%s-%08d.ttf", sanitizePathPart(phase), seq)
	u.Path = path.Join(basePath, sanitizePathPart(runID), file)
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String(), nil
}

func sanitizePathPart(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "x"
	}
	return out
}

func makePublishEndpoint(serverURL, resourceURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/publish"
	q := u.Query()
	q.Set("encoding", string(ampcache.EncodingFont))
	q.Set("resource_url", resourceURL)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func printReport(rep report) {
	fmt.Printf("run: %s\n", rep.RunID)
	fmt.Printf("server: %s\n", rep.ServerURL)
	fmt.Printf("resource base: %s\n", rep.ResourceBase)
	fmt.Printf("user agent: %s\n", rep.UserAgent)
	fmt.Printf("payload fill: %s\n", rep.PayloadFill)
	fmt.Printf("payload bytes: %d\n", rep.PayloadBytes)
	fmt.Printf("encoded font bytes: %d\n", rep.EncodedFontBytes)
	fmt.Printf("payload sha256: %s\n", rep.PayloadSHA256)
	fmt.Println()
	for _, phase := range rep.Phases {
		fmt.Printf("%s\n", phase.Name)
		fmt.Printf("  kind: %s\n", phase.Kind)
		fmt.Printf("  window: %s\n", phase.Duration)
		fmt.Printf("  workers: %d\n", phase.Workers)
		fmt.Printf("  started: %d\n", phase.Started)
		fmt.Printf("  succeeded in window: %d (%.2f/s)\n", phase.SucceededInWindow, phase.InWindowPerSecond)
		fmt.Printf("  succeeded total: %d (%.2f/s over elapsed)\n", phase.Succeeded, phase.TotalPerSecond)
		fmt.Printf("  failed: %d\n", phase.Failed)
		fmt.Printf("  elapsed: %s\n", time.Duration(phase.ElapsedMillis)*time.Millisecond)
		if phase.BytesRead > 0 {
			fmt.Printf("  bytes read: %d\n", phase.BytesRead)
		}
		printCounts("  statuses", phase.StatusCounts)
		printCounts("  errors", phase.ErrorCounts)
		for _, sample := range phase.RepresentativeError {
			fmt.Printf("  sample error: %s\n", sample)
		}
		fmt.Println()
	}
	if len(rep.PreparedCachedURLs) > 0 {
		fmt.Println("prepared cached URLs:")
		for _, prepared := range rep.PreparedCachedURLs {
			fmt.Printf("  %s\n", prepared.CacheURL)
		}
	}
}

func printCounts(label string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Printf("%s:\n", label)
	for _, key := range keys {
		fmt.Printf("    %s: %d\n", key, counts[key])
	}
}
