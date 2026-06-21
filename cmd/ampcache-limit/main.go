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
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gomodstore/internal/ampcache"
)

type config struct {
	serverURL          string
	resourceBase       string
	cacheDomain        string
	cacheTLSServerName string
	userAgent          string
	sizes              []int
	waitSchedule       []time.Duration
	payloadFill        string
	publishTimeout     time.Duration
	probeTimeout       time.Duration
	pollInterval       time.Duration
	idleExpire         bool
	idleSamples        int
	watchExpire        bool
	expireInterval     time.Duration
	expireTimeout      time.Duration
	expireMisses       int
	runID              string
	jsonOutput         bool
	verbose            bool
}

type report struct {
	StartedAt    time.Time     `json:"started_at"`
	ServerURL    string        `json:"server_url"`
	ResourceBase string        `json:"resource_base"`
	UserAgent    string        `json:"user_agent"`
	RunID        string        `json:"run_id"`
	PayloadFill  string        `json:"payload_fill"`
	WaitSchedule []string      `json:"wait_schedule"`
	IdleExpire   bool          `json:"idle_expire"`
	IdleSamples  int           `json:"idle_samples,omitempty"`
	WatchExpire  bool          `json:"watch_expire"`
	ExpireConfig *expireConfig `json:"expire_config,omitempty"`
	Results      []sizeResult  `json:"results"`
	Summary      summary       `json:"summary"`
}

type sizeResult struct {
	PayloadBytes      int               `json:"payload_bytes"`
	EncodedFontBytes  int               `json:"encoded_font_bytes"`
	ResourceURL       string            `json:"resource_url"`
	CacheURL          string            `json:"cache_url"`
	PayloadSHA256     string            `json:"payload_sha256"`
	PublishOK         bool              `json:"publish_ok"`
	PublishMillis     int64             `json:"publish_millis"`
	PublishedUnixNano int64             `json:"published_unix_nano,omitempty"`
	IdleWait          string            `json:"idle_wait,omitempty"`
	IdleWaitMillis    int64             `json:"idle_wait_millis,omitempty"`
	IdleSample        int               `json:"idle_sample,omitempty"`
	PublishError      string            `json:"publish_error,omitempty"`
	Probes            []probeResult     `json:"probes"`
	Expiration        *expirationResult `json:"expiration,omitempty"`
}

type probeResult struct {
	Wait          string `json:"wait"`
	AgeMillis     int64  `json:"age_millis"`
	ProbeUnixNano int64  `json:"probe_unix_nano"`
	Status        string `json:"status"`
	OK            bool   `json:"ok"`
	DecodeOK      bool   `json:"decode_ok"`
	BytesRead     int    `json:"bytes_read"`
	DecodedBytes  int64  `json:"decoded_bytes"`
	ElapsedMillis int64  `json:"elapsed_millis"`
	Error         string `json:"error,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

type summary struct {
	LargestPublishedPayloadBytes int                       `json:"largest_published_payload_bytes"`
	LargestAlwaysOKPayloadBytes  int                       `json:"largest_always_ok_payload_bytes"`
	LargestOKByWait              map[string]int            `json:"largest_ok_by_wait"`
	StatusCountsByWait           map[string]map[string]int `json:"status_counts_by_wait"`
	ExpiredPayloadBytes          []int                     `json:"expired_payload_bytes,omitempty"`
	NotExpiredPayloadBytes       []int                     `json:"not_expired_payload_bytes,omitempty"`
	ExpiredBySize                map[string]expireRange    `json:"expired_by_size,omitempty"`
	IdleBySize                   map[string]idleSizeResult `json:"idle_by_size,omitempty"`
}

type idleSizeResult struct {
	ObservedOK             bool                      `json:"observed_ok"`
	ObservedMiss           bool                      `json:"observed_miss"`
	LargestOKWaitMillis    int64                     `json:"largest_ok_wait_millis,omitempty"`
	SmallestMissWaitMillis int64                     `json:"smallest_miss_wait_millis,omitempty"`
	StatusCountsByWait     map[string]map[string]int `json:"status_counts_by_wait"`
}

type expireConfig struct {
	Interval string `json:"interval"`
	Timeout  string `json:"timeout"`
	Misses   int    `json:"misses"`
}

type expirationResult struct {
	Watched                   bool           `json:"watched"`
	Expired                   bool           `json:"expired"`
	SawOK                     bool           `json:"saw_ok"`
	ProbeCount                int            `json:"probe_count"`
	OKCount                   int            `json:"ok_count"`
	FailCount                 int            `json:"fail_count"`
	ConsecutiveMisses         int            `json:"consecutive_misses"`
	LastOKAgeMillis           int64          `json:"last_ok_age_millis,omitempty"`
	LastOKUnixNano            int64          `json:"last_ok_unix_nano,omitempty"`
	ExpiredAfterAgeMillis     int64          `json:"expired_after_age_millis,omitempty"`
	ExpiredBeforeAgeMillis    int64          `json:"expired_before_age_millis,omitempty"`
	ExpiredConfirmedAgeMillis int64          `json:"expired_confirmed_age_millis,omitempty"`
	ExpiredConfirmedUnixNano  int64          `json:"expired_confirmed_unix_nano,omitempty"`
	LastProbeAgeMillis        int64          `json:"last_probe_age_millis,omitempty"`
	LastProbeUnixNano         int64          `json:"last_probe_unix_nano,omitempty"`
	LastStatus                string         `json:"last_status,omitempty"`
	LastError                 string         `json:"last_error,omitempty"`
	LastDetail                string         `json:"last_detail,omitempty"`
	StatusCounts              map[string]int `json:"status_counts,omitempty"`
}

type expireRange struct {
	AfterAgeMillis     int64 `json:"after_age_millis"`
	BeforeAgeMillis    int64 `json:"before_age_millis"`
	ConfirmedAgeMillis int64 `json:"confirmed_age_millis"`
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ampcache-limit: %v\n", err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ampcache-limit: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (config, error) {
	var cfg config
	var sizesText string
	var waitsText string
	var minSizeText string
	var maxSizeText string
	var scale float64
	fs := flag.NewFlagSet("ampcache-limit", flag.ExitOnError)
	fs.StringVar(&cfg.serverURL, "server", "", "ampcache server URL")
	fs.StringVar(&cfg.resourceBase, "resource-base", "", "absolute public origin URL prefix for generated .ttf resources; defaults to -server")
	fs.StringVar(&cfg.cacheDomain, "cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain used for TLS domain fronting")
	fs.StringVar(&cfg.cacheTLSServerName, "cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache requests; empty disables domain fronting")
	fs.StringVar(&cfg.userAgent, "user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache requests; empty uses Go default")
	fs.StringVar(&sizesText, "sizes", "", "comma-separated payload sizes to test, e.g. 64KiB,128KiB,256KiB")
	fs.StringVar(&minSizeText, "min-size", "1024", "minimum payload size when -sizes is not set")
	fs.StringVar(&maxSizeText, "max-size", "524288", "maximum payload size when -sizes is not set")
	fs.Float64Var(&scale, "scale", 2, "geometric size multiplier when -sizes is not set")
	fs.StringVar(&waitsText, "waits", "0,1m,5m", "comma-separated probe waits after publish phase, e.g. 0,30s,1m,5m; defaults to 0 when -watch-expire is set and -waits is not specified")
	fs.StringVar(&cfg.payloadFill, "payload-fill", "random", "payload fill mode: random or pattern")
	fs.DurationVar(&cfg.publishTimeout, "publish-timeout", 3*time.Minute, "timeout for one origin-fetch publish")
	fs.DurationVar(&cfg.probeTimeout, "probe-timeout", 30*time.Second, "timeout for one AMP cache probe")
	fs.DurationVar(&cfg.pollInterval, "poll", 500*time.Millisecond, "AMP cache poll interval while triggering origin fetch")
	fs.BoolVar(&cfg.idleExpire, "idle-expire", false, "publish separate URLs for each wait and probe each URL once after being idle")
	fs.IntVar(&cfg.idleSamples, "idle-samples", 1, "number of independent URLs to test for each size and wait in -idle-expire mode")
	fs.BoolVar(&cfg.watchExpire, "watch-expire", false, "keep probing published cache URLs until they expire or -expire-timeout is reached")
	fs.DurationVar(&cfg.expireInterval, "expire-interval", time.Minute, "probe interval while -watch-expire is enabled")
	fs.DurationVar(&cfg.expireTimeout, "expire-timeout", 24*time.Hour, "maximum time to watch for expiration")
	fs.IntVar(&cfg.expireMisses, "expire-misses", 3, "consecutive cache-miss probes after at least one OK required to mark a URL expired")
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
	waitsSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "waits" {
			waitsSet = true
		}
	})
	if cfg.watchExpire && !waitsSet {
		waitsText = "0"
	}
	if cfg.idleExpire && cfg.watchExpire {
		return cfg, errors.New("-idle-expire and -watch-expire are mutually exclusive")
	}
	if cfg.idleSamples <= 0 {
		return cfg, errors.New("-idle-samples must be greater than zero")
	}
	if _, err := parseAbsoluteHTTPURL("-server", cfg.serverURL); err != nil {
		return cfg, err
	}
	if _, err := parseAbsoluteHTTPURL("-resource-base", cfg.resourceBase); err != nil {
		return cfg, err
	}
	switch cfg.payloadFill {
	case "random", "pattern":
	default:
		return cfg, errors.New("-payload-fill must be random or pattern")
	}
	if sizesText != "" {
		sizes, err := parseSizeList(sizesText)
		if err != nil {
			return cfg, err
		}
		cfg.sizes = sizes
	} else {
		minSize, err := parseByteCount(minSizeText)
		if err != nil {
			return cfg, fmt.Errorf("-min-size: %w", err)
		}
		maxSize, err := parseByteCount(maxSizeText)
		if err != nil {
			return cfg, fmt.Errorf("-max-size: %w", err)
		}
		if minSize <= 0 || maxSize <= 0 || minSize > maxSize {
			return cfg, errors.New("-min-size and -max-size must be positive and ordered")
		}
		if scale <= 1 {
			return cfg, errors.New("-scale must be greater than 1")
		}
		cfg.sizes = geometricSizes(minSize, maxSize, scale)
	}
	waits, err := parseWaitList(waitsText)
	if err != nil {
		return cfg, err
	}
	cfg.waitSchedule = waits
	if cfg.publishTimeout <= 0 || cfg.probeTimeout <= 0 || cfg.pollInterval <= 0 {
		return cfg, errors.New("timeouts and poll interval must be greater than zero")
	}
	if cfg.watchExpire {
		if cfg.expireInterval <= 0 || cfg.expireTimeout <= 0 {
			return cfg, errors.New("-expire-interval and -expire-timeout must be greater than zero")
		}
		if cfg.expireMisses <= 0 {
			return cfg, errors.New("-expire-misses must be greater than zero")
		}
	}
	if cfg.runID == "" {
		cfg.runID = "limit-" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000Z"), ".", "-")
	}
	return cfg, nil
}

func run(cfg config) error {
	logf := func(string, ...any) {}
	if cfg.verbose {
		logf = log.New(os.Stderr, "ampcache-limit: ", log.LstdFlags).Printf
	}
	client := ampcache.NewDomainFrontingClientWithUserAgent(cfg.cacheDomain, cfg.cacheTLSServerName, cfg.userAgent)
	rep := report{
		StartedAt:    time.Now().UTC(),
		ServerURL:    cfg.serverURL,
		ResourceBase: cfg.resourceBase,
		UserAgent:    cfg.userAgent,
		RunID:        cfg.runID,
		PayloadFill:  cfg.payloadFill,
		WaitSchedule: formatDurations(cfg.waitSchedule),
		IdleExpire:   cfg.idleExpire,
		WatchExpire:  cfg.watchExpire,
	}
	if cfg.idleExpire {
		rep.IdleSamples = cfg.idleSamples
	}
	if cfg.watchExpire {
		rep.ExpireConfig = &expireConfig{
			Interval: cfg.expireInterval.String(),
			Timeout:  cfg.expireTimeout.String(),
			Misses:   cfg.expireMisses,
		}
	}
	if cfg.idleExpire {
		if err := runIdleExpire(cfg, client, &rep, logf); err != nil {
			return err
		}
		rep.Summary = summarize(rep)
		if cfg.jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(rep)
		}
		printReport(rep)
		return nil
	}

	for i, size := range cfg.sizes {
		result, err := publishTestResource(cfg, client, i, size, "", logf)
		if err != nil {
			return err
		}
		rep.Results = append(rep.Results, result)
	}

	publishDone := time.Now()
	for _, wait := range cfg.waitSchedule {
		target := publishDone.Add(wait)
		if sleep := time.Until(target); sleep > 0 {
			logf("waiting %s before probe", sleep.Round(time.Second))
			time.Sleep(sleep)
		}
		for i := range rep.Results {
			if !rep.Results[i].PublishOK {
				continue
			}
			logf("probe wait=%s size=%d", wait, rep.Results[i].PayloadBytes)
			ctx, cancel := context.WithTimeout(context.Background(), cfg.probeTimeout)
			probe := probeCache(ctx, client, rep.Results[i], wait, publishBaseTime(rep.Results[i], publishDone))
			cancel()
			rep.Results[i].Probes = append(rep.Results[i].Probes, probe)
			if probe.OK {
				logf("probe ok wait=%s size=%d bytes=%d", wait, rep.Results[i].PayloadBytes, probe.BytesRead)
			} else {
				logf("probe failed wait=%s size=%d status=%s err=%s detail=%s", wait, rep.Results[i].PayloadBytes, probe.Status, probe.Error, probe.Detail)
			}
		}
	}
	if cfg.watchExpire {
		watchExpirations(cfg, client, &rep, publishDone, logf)
	}
	rep.Summary = summarize(rep)
	if cfg.jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printReport(rep)
	return nil
}

func runIdleExpire(cfg config, client *http.Client, rep *report, logf func(string, ...any)) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait()

	index := 0
	for _, size := range cfg.sizes {
		for _, wait := range cfg.waitSchedule {
			for sample := 1; sample <= cfg.idleSamples; sample++ {
				label := fmt.Sprintf("idle-%s-s%d", safeLabel(wait.String()), sample)
				result, err := publishTestResource(cfg, client, index, size, label, logf)
				if err != nil {
					return err
				}
				result.IdleWait = wait.String()
				result.IdleWaitMillis = wait.Milliseconds()
				result.IdleSample = sample

				mu.Lock()
				rep.Results = append(rep.Results, result)
				resultIndex := len(rep.Results) - 1
				mu.Unlock()

				if result.PublishOK {
					wg.Add(1)
					go func(resultIndex int, result sizeResult, idleWait time.Duration) {
						defer wg.Done()
						base := publishBaseTime(result, time.Now())
						target := base.Add(idleWait)
						if sleep := time.Until(target); sleep > 0 {
							logf("idle probe scheduled size=%d sample=%d idle=%s in=%s", result.PayloadBytes, result.IdleSample, idleWait, sleep.Round(time.Second))
							time.Sleep(sleep)
						}

						ctx, cancel := context.WithTimeout(context.Background(), cfg.probeTimeout)
						probe := probeCache(ctx, client, result, idleWait, base)
						cancel()
						probe.Wait = "idle:" + idleWait.String()

						mu.Lock()
						rep.Results[resultIndex].Probes = append(rep.Results[resultIndex].Probes, probe)
						mu.Unlock()

						if probe.OK {
							logf("idle probe ok size=%d sample=%d requested_idle=%s age=%s bytes=%d", result.PayloadBytes, result.IdleSample, idleWait, durationFromMillis(probe.AgeMillis), probe.BytesRead)
						} else {
							logf("idle probe failed size=%d sample=%d requested_idle=%s age=%s status=%s err=%s detail=%s", result.PayloadBytes, result.IdleSample, idleWait, durationFromMillis(probe.AgeMillis), probe.Status, probe.Error, probe.Detail)
						}
					}(resultIndex, result, wait)
				}
				index++
			}
		}
	}
	return nil
}

func publishTestResource(cfg config, client *http.Client, index, size int, label string, logf func(string, ...any)) (sizeResult, error) {
	resourceURL, err := makeResourceURL(cfg.resourceBase, cfg.runID, index, size, label)
	if err != nil {
		return sizeResult{}, err
	}
	payload, err := makePayload(size, cfg.runID, cfg.payloadFill)
	if err != nil {
		return sizeResult{}, err
	}
	sum := sha256.Sum256(payload)
	encoded, _, err := ampcache.EncodeResource(payload, ampcache.EncodingFont, resourceURL)
	if err != nil {
		return sizeResult{}, fmt.Errorf("encode sample size %d: %w", size, err)
	}
	result := sizeResult{
		PayloadBytes:     size,
		EncodedFontBytes: len(encoded),
		ResourceURL:      resourceURL,
		PayloadSHA256:    hex.EncodeToString(sum[:]),
	}
	logf("publish size=%d encoded=%d url=%s", size, len(encoded), resourceURL)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.publishTimeout)
	pub, err := ampcache.Publish(ctx, ampcache.PublishOptions{
		ServerURL:    cfg.serverURL,
		ResourceURL:  resourceURL,
		Encoding:     ampcache.EncodingFont,
		Body:         bytes.NewReader(payload),
		HTTPClient:   client,
		Timeout:      cfg.publishTimeout,
		PollInterval: cfg.pollInterval,
		WaitMode:     "origin-fetch",
	})
	cancel()
	result.PublishMillis = time.Since(start).Milliseconds()
	if err != nil {
		result.PublishError = err.Error()
		logf("publish failed size=%d after=%s err=%v", size, time.Since(start).Round(time.Millisecond), err)
		return result, nil
	}
	result.PublishOK = true
	result.CacheURL = pub.CacheURL
	result.PublishedUnixNano = time.Now().UnixNano()
	logf("published size=%d after=%s cache_url=%s", size, time.Since(start).Round(time.Millisecond), pub.CacheURL)
	return result, nil
}

func publishBaseTime(result sizeResult, fallback time.Time) time.Time {
	if result.PublishedUnixNano != 0 {
		return time.Unix(0, result.PublishedUnixNano)
	}
	return fallback
}

func watchExpirations(cfg config, client *http.Client, rep *report, fallbackBase time.Time, logf func(string, ...any)) {
	deadline := time.Now().Add(cfg.expireTimeout)
	active := 0
	for i := range rep.Results {
		if !rep.Results[i].PublishOK {
			continue
		}
		exp := &expirationResult{
			Watched:      true,
			StatusCounts: make(map[string]int),
		}
		for _, probe := range rep.Results[i].Probes {
			updateExpiration(exp, probe, cfg.expireMisses)
		}
		rep.Results[i].Expiration = exp
		if !exp.Expired {
			active++
		}
	}
	if active == 0 {
		return
	}
	logf("expiration watch start active=%d interval=%s timeout=%s misses=%d", active, cfg.expireInterval, cfg.expireTimeout, cfg.expireMisses)
	for active > 0 {
		if !time.Now().Before(deadline) {
			logf("expiration watch timeout active=%d", active)
			return
		}
		cycleStart := time.Now()
		active = 0
		for i := range rep.Results {
			result := &rep.Results[i]
			if !result.PublishOK || result.Expiration == nil || result.Expiration.Expired {
				continue
			}
			if !time.Now().Before(deadline) {
				active++
				continue
			}
			base := publishBaseTime(*result, fallbackBase)
			ctx, cancel := context.WithTimeout(context.Background(), cfg.probeTimeout)
			probe := probeCache(ctx, client, *result, 0, base)
			cancel()
			probe.Wait = "expire-watch"
			result.Probes = append(result.Probes, probe)
			updateExpiration(result.Expiration, probe, cfg.expireMisses)
			if probe.OK {
				logf("expiration probe ok size=%d age=%s", result.PayloadBytes, durationFromMillis(probe.AgeMillis))
			} else if isExpirationMiss(probe) {
				logf("expiration probe miss size=%d age=%s misses=%d/%d status=%s err=%s detail=%s", result.PayloadBytes, durationFromMillis(probe.AgeMillis), result.Expiration.ConsecutiveMisses, cfg.expireMisses, probe.Status, probe.Error, probe.Detail)
			} else {
				logf("expiration probe inconclusive size=%d age=%s status=%s err=%s", result.PayloadBytes, durationFromMillis(probe.AgeMillis), probe.Status, probe.Error)
			}
			if result.Expiration.Expired {
				logf("expiration confirmed size=%d after=%s before=%s confirmed=%s", result.PayloadBytes, durationFromMillis(result.Expiration.ExpiredAfterAgeMillis), durationFromMillis(result.Expiration.ExpiredBeforeAgeMillis), durationFromMillis(result.Expiration.ExpiredConfirmedAgeMillis))
				continue
			}
			active++
		}
		if active == 0 {
			return
		}
		sleep := cfg.expireInterval - time.Since(cycleStart)
		if sleep <= 0 {
			continue
		}
		if untilDeadline := time.Until(deadline); sleep > untilDeadline {
			sleep = untilDeadline
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
	}
}

func updateExpiration(exp *expirationResult, probe probeResult, requiredMisses int) {
	exp.ProbeCount++
	exp.LastProbeAgeMillis = probe.AgeMillis
	exp.LastProbeUnixNano = probe.ProbeUnixNano
	exp.LastStatus = probe.Status
	exp.LastError = probe.Error
	exp.LastDetail = probe.Detail
	if exp.StatusCounts == nil {
		exp.StatusCounts = make(map[string]int)
	}
	exp.StatusCounts[statusKey(probe)]++
	if probe.OK {
		exp.SawOK = true
		exp.OKCount++
		exp.ConsecutiveMisses = 0
		exp.LastOKAgeMillis = probe.AgeMillis
		exp.LastOKUnixNano = probe.ProbeUnixNano
		if !exp.Expired {
			exp.ExpiredAfterAgeMillis = 0
			exp.ExpiredBeforeAgeMillis = 0
			exp.ExpiredConfirmedAgeMillis = 0
			exp.ExpiredConfirmedUnixNano = 0
		}
		return
	}
	exp.FailCount++
	if exp.Expired || !exp.SawOK || !isExpirationMiss(probe) {
		return
	}
	if exp.ConsecutiveMisses == 0 {
		exp.ExpiredAfterAgeMillis = exp.LastOKAgeMillis
		exp.ExpiredBeforeAgeMillis = probe.AgeMillis
	}
	exp.ConsecutiveMisses++
	if exp.ConsecutiveMisses >= requiredMisses {
		exp.Expired = true
		exp.ExpiredConfirmedAgeMillis = probe.AgeMillis
		exp.ExpiredConfirmedUnixNano = probe.ProbeUnixNano
	}
}

func isExpirationMiss(probe probeResult) bool {
	if probe.OK {
		return false
	}
	if strings.HasPrefix(probe.Status, "404 ") || strings.HasPrefix(probe.Status, "410 ") {
		return true
	}
	if strings.HasPrefix(probe.Status, "200 ") && probe.Error != "" {
		return true
	}
	return false
}

func statusKey(probe probeResult) string {
	if probe.Status != "" {
		return probe.Status
	}
	if probe.Error != "" {
		return "request_error"
	}
	return "unknown"
}

func probeCache(ctx context.Context, client *http.Client, result sizeResult, wait time.Duration, publishDone time.Time) probeResult {
	start := time.Now()
	probe := probeResult{
		Wait:          wait.String(),
		AgeMillis:     start.Sub(publishDone).Milliseconds(),
		ProbeUnixNano: start.UnixNano(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, result.CacheURL, nil)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	resp, err := client.Do(req)
	if err != nil {
		probe.ElapsedMillis = time.Since(start).Milliseconds()
		probe.Error = err.Error()
		return probe
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	probe.ElapsedMillis = time.Since(start).Milliseconds()
	probe.Status = resp.Status
	probe.BytesRead = len(body)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	if resp.StatusCode != http.StatusOK {
		probe.Error = "cache returned " + resp.Status
		probe.Detail = compactBody(body)
		return probe
	}
	payload, manifest, err := ampcache.DecodeResource(body, ampcache.EncodingFont)
	if err != nil {
		probe.Error = "decode cached font: " + err.Error()
		return probe
	}
	probe.DecodeOK = true
	probe.DecodedBytes = manifest.Length
	sum := sha256.Sum256(payload)
	gotSHA := hex.EncodeToString(sum[:])
	if manifest.Length != int64(result.PayloadBytes) {
		probe.Error = fmt.Sprintf("decoded length %d, want %d", manifest.Length, result.PayloadBytes)
		return probe
	}
	if gotSHA != result.PayloadSHA256 {
		probe.Error = "decoded sha256 mismatch"
		return probe
	}
	probe.OK = true
	return probe
}

func summarize(rep report) summary {
	out := summary{
		LargestOKByWait:    make(map[string]int),
		StatusCountsByWait: make(map[string]map[string]int),
		ExpiredBySize:      make(map[string]expireRange),
		IdleBySize:         make(map[string]idleSizeResult),
	}
	for _, result := range rep.Results {
		if result.PublishOK && result.PayloadBytes > out.LargestPublishedPayloadBytes {
			out.LargestPublishedPayloadBytes = result.PayloadBytes
		}
		allOK := result.PublishOK && len(result.Probes) > 0
		for _, probe := range result.Probes {
			if out.StatusCountsByWait[probe.Wait] == nil {
				out.StatusCountsByWait[probe.Wait] = make(map[string]int)
			}
			status := probe.Status
			if status == "" {
				status = "request_error"
			}
			out.StatusCountsByWait[probe.Wait][status]++
			if probe.OK && result.PayloadBytes > out.LargestOKByWait[probe.Wait] {
				out.LargestOKByWait[probe.Wait] = result.PayloadBytes
			}
			if !probe.OK {
				allOK = false
			}
		}
		if allOK && result.PayloadBytes > out.LargestAlwaysOKPayloadBytes {
			out.LargestAlwaysOKPayloadBytes = result.PayloadBytes
		}
		if result.Expiration != nil {
			if result.Expiration.Expired {
				out.ExpiredPayloadBytes = append(out.ExpiredPayloadBytes, result.PayloadBytes)
				out.ExpiredBySize[strconv.Itoa(result.PayloadBytes)] = expireRange{
					AfterAgeMillis:     result.Expiration.ExpiredAfterAgeMillis,
					BeforeAgeMillis:    result.Expiration.ExpiredBeforeAgeMillis,
					ConfirmedAgeMillis: result.Expiration.ExpiredConfirmedAgeMillis,
				}
			} else {
				out.NotExpiredPayloadBytes = append(out.NotExpiredPayloadBytes, result.PayloadBytes)
			}
		}
		if result.IdleWait != "" {
			key := strconv.Itoa(result.PayloadBytes)
			idle := out.IdleBySize[key]
			if idle.StatusCountsByWait == nil {
				idle.StatusCountsByWait = make(map[string]map[string]int)
			}
			if idle.StatusCountsByWait[result.IdleWait] == nil {
				idle.StatusCountsByWait[result.IdleWait] = make(map[string]int)
			}
			status := "not_probed"
			if !result.PublishOK {
				status = "publish_failed"
			}
			if len(result.Probes) > 0 {
				probe := result.Probes[len(result.Probes)-1]
				status = statusKey(probe)
				if probe.OK {
					idle.ObservedOK = true
					if result.IdleWaitMillis > idle.LargestOKWaitMillis {
						idle.LargestOKWaitMillis = result.IdleWaitMillis
					}
				}
				if isExpirationMiss(probe) && (idle.SmallestMissWaitMillis == 0 || result.IdleWaitMillis < idle.SmallestMissWaitMillis) {
					idle.ObservedMiss = true
					idle.SmallestMissWaitMillis = result.IdleWaitMillis
				}
			}
			idle.StatusCountsByWait[result.IdleWait][status]++
			out.IdleBySize[key] = idle
		}
	}
	sort.Ints(out.ExpiredPayloadBytes)
	sort.Ints(out.NotExpiredPayloadBytes)
	if len(out.ExpiredBySize) == 0 {
		out.ExpiredBySize = nil
	}
	if len(out.IdleBySize) == 0 {
		out.IdleBySize = nil
	}
	return out
}

func printReport(rep report) {
	fmt.Printf("run: %s\n", rep.RunID)
	fmt.Printf("server: %s\n", rep.ServerURL)
	fmt.Printf("resource base: %s\n", rep.ResourceBase)
	fmt.Printf("user agent: %s\n", rep.UserAgent)
	fmt.Printf("payload fill: %s\n", rep.PayloadFill)
	fmt.Println()
	fmt.Println("size results:")
	for _, result := range rep.Results {
		fmt.Printf("  %8d bytes payload, %8d bytes font: publish=%v", result.PayloadBytes, result.EncodedFontBytes, result.PublishOK)
		if result.PublishError != "" {
			fmt.Printf(" error=%s", result.PublishError)
		}
		fmt.Println()
		for _, probe := range result.Probes {
			state := "fail"
			if probe.OK {
				state = "ok"
			}
			fmt.Printf("    wait=%-8s age=%6.1fs %-4s status=%-18s bytes=%d", probe.Wait, float64(probe.AgeMillis)/1000, state, probe.Status, probe.BytesRead)
			if probe.Error != "" {
				fmt.Printf(" error=%s", probe.Error)
			}
			if probe.Detail != "" {
				fmt.Printf(" detail=%s", probe.Detail)
			}
			fmt.Println()
		}
	}
	fmt.Println()
	fmt.Printf("largest published payload: %d bytes\n", rep.Summary.LargestPublishedPayloadBytes)
	fmt.Printf("largest payload ok at every probe: %d bytes\n", rep.Summary.LargestAlwaysOKPayloadBytes)
	waits := make([]string, 0, len(rep.Summary.LargestOKByWait))
	for wait := range rep.Summary.LargestOKByWait {
		waits = append(waits, wait)
	}
	sort.Strings(waits)
	for _, wait := range waits {
		fmt.Printf("largest payload ok at wait %s: %d bytes\n", wait, rep.Summary.LargestOKByWait[wait])
	}
	if rep.IdleExpire {
		fmt.Println()
		fmt.Println("idle expiration summary:")
		sizes := sortedIntStringKeys(rep.Summary.IdleBySize)
		for _, size := range sizes {
			idle := rep.Summary.IdleBySize[strconv.Itoa(size)]
			okText := "none"
			if idle.ObservedOK {
				okText = durationFromMillis(idle.LargestOKWaitMillis)
			}
			missText := "none"
			if idle.ObservedMiss {
				missText = durationFromMillis(idle.SmallestMissWaitMillis)
			}
			fmt.Printf("  %8d bytes: largest_idle_ok=%s smallest_idle_miss=%s\n", size, okText, missText)
			waitKeys := make([]string, 0, len(idle.StatusCountsByWait))
			for wait := range idle.StatusCountsByWait {
				waitKeys = append(waitKeys, wait)
			}
			sort.Slice(waitKeys, func(i, j int) bool {
				di, _ := time.ParseDuration(waitKeys[i])
				dj, _ := time.ParseDuration(waitKeys[j])
				return di < dj
			})
			for _, wait := range waitKeys {
				fmt.Printf("      idle=%-8s %s\n", wait, formatCounts(idle.StatusCountsByWait[wait]))
			}
		}
	}
	if rep.WatchExpire {
		fmt.Println()
		fmt.Println("expiration watch:")
		for _, result := range rep.Results {
			if result.Expiration == nil {
				continue
			}
			exp := result.Expiration
			if exp.Expired {
				fmt.Printf("  %8d bytes: expired between %s and %s after publish, confirmed at %s (%d probes, ok=%d fail=%d)\n",
					result.PayloadBytes,
					durationFromMillis(exp.ExpiredAfterAgeMillis),
					durationFromMillis(exp.ExpiredBeforeAgeMillis),
					durationFromMillis(exp.ExpiredConfirmedAgeMillis),
					exp.ProbeCount,
					exp.OKCount,
					exp.FailCount,
				)
				continue
			}
			state := "not expired"
			if !exp.SawOK {
				state = "never observed ok"
			}
			fmt.Printf("  %8d bytes: %s by %s after publish (%d probes, ok=%d fail=%d",
				result.PayloadBytes,
				state,
				durationFromMillis(exp.LastProbeAgeMillis),
				exp.ProbeCount,
				exp.OKCount,
				exp.FailCount,
			)
			if exp.LastStatus != "" {
				fmt.Printf(", last_status=%s", exp.LastStatus)
			}
			if exp.LastError != "" {
				fmt.Printf(", last_error=%s", exp.LastError)
			}
			fmt.Println(")")
		}
	}
}

func sortedIntStringKeys[V any](m map[string]V) []int {
	out := make([]int, 0, len(m))
	for key := range m {
		value, err := strconv.Atoi(key)
		if err != nil {
			continue
		}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func formatCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

func parseSizeList(value string) ([]int, error) {
	parts := strings.Split(value, ",")
	out := make([]int, 0, len(parts))
	seen := make(map[int]struct{})
	for _, part := range parts {
		size, err := parseByteCount(part)
		if err != nil {
			return nil, err
		}
		if size <= 0 {
			return nil, errors.New("sizes must be greater than zero")
		}
		if _, ok := seen[size]; ok {
			continue
		}
		seen[size] = struct{}{}
		out = append(out, size)
	}
	sort.Ints(out)
	if len(out) == 0 {
		return nil, errors.New("no sizes provided")
	}
	return out, nil
}

func geometricSizes(minSize, maxSize int, scale float64) []int {
	out := []int{}
	seen := make(map[int]struct{})
	current := minSize
	for current <= maxSize {
		if _, ok := seen[current]; !ok {
			out = append(out, current)
			seen[current] = struct{}{}
		}
		next := int(math.Ceil(float64(current) * scale))
		if next <= current {
			next = current + 1
		}
		current = next
	}
	if _, ok := seen[maxSize]; !ok {
		out = append(out, maxSize)
	}
	sort.Ints(out)
	return out
}

func parseByteCount(value string) (int, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return 0, errors.New("empty byte count")
	}
	multiplier := int64(1)
	for _, suffix := range []struct {
		text string
		mul  int64
	}{
		{"kib", 1024}, {"kb", 1024}, {"k", 1024},
		{"mib", 1024 * 1024}, {"mb", 1024 * 1024}, {"m", 1024 * 1024},
		{"gib", 1024 * 1024 * 1024}, {"gb", 1024 * 1024 * 1024}, {"g", 1024 * 1024 * 1024},
	} {
		if strings.HasSuffix(trimmed, suffix.text) {
			multiplier = suffix.mul
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, suffix.text))
			break
		}
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, err
	}
	maxInt := int64(int(^uint(0) >> 1))
	if n <= 0 || n > maxInt/multiplier {
		return 0, errors.New("byte count out of range")
	}
	return int(n * multiplier), nil
}

func parseWaitList(value string) ([]time.Duration, error) {
	parts := strings.Split(value, ",")
	out := make([]time.Duration, 0, len(parts))
	seen := make(map[time.Duration]struct{})
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		var d time.Duration
		var err error
		if trimmed == "0" {
			d = 0
		} else {
			d, err = time.ParseDuration(trimmed)
			if err != nil {
				return nil, err
			}
			if d < 0 {
				return nil, errors.New("wait durations must be non-negative")
			}
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) == 0 {
		return nil, errors.New("no wait durations provided")
	}
	return out, nil
}

func formatDurations(values []time.Duration) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.String())
	}
	return out
}

func makePayload(size int, runID, fill string) ([]byte, error) {
	payload := make([]byte, size)
	switch fill {
	case "random":
		if _, err := io.ReadFull(rand.Reader, payload); err != nil {
			return nil, err
		}
	case "pattern":
		seed := []byte("ampcache-limit:" + runID + ":")
		for i := range payload {
			payload[i] = seed[i%len(seed)] ^ byte(i*31)
		}
	default:
		return nil, errors.New("unsupported payload fill")
	}
	return payload, nil
}

func makeResourceURL(resourceBase, runID string, index, size int, label string) (string, error) {
	u, err := url.Parse(resourceBase)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimRight(u.Path, "/")
	name := fmt.Sprintf("p-%04d-%d", index, size)
	if label != "" {
		name += "-" + safeLabel(label)
	}
	u.Path = path.Join(basePath, runID, name+".ttf")
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func safeLabel(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	lastDash := false
	for _, r := range strings.ToLower(value) {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	return out
}

func compactBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	if idx := strings.Index(text, "Font failed validation:"); idx >= 0 {
		text = text[idx:]
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}

func durationFromMillis(millis int64) string {
	return (time.Duration(millis) * time.Millisecond).Round(time.Millisecond).String()
}

func parseAbsoluteHTTPURL(name, value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute http or https URL", name)
	}
	return u, nil
}
