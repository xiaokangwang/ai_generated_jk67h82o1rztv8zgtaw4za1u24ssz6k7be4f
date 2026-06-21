package ampcache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type PublishOptions struct {
	ServerURL    string
	ResourceURL  string
	Encoding     Encoding
	Body         io.Reader
	HTTPClient   *http.Client
	Timeout      time.Duration
	PollInterval time.Duration
	Logger       *log.Logger
	WaitMode     string
}

type PublishResult struct {
	Encoding    string `json:"encoding"`
	ResourceURL string `json:"resource_url"`
	CacheURL    string `json:"cache_url"`
	SHA256      string `json:"sha256"`
	Length      int64  `json:"length"`
}

type GetOptions struct {
	CacheURL   string
	Encoding   Encoding
	Output     io.Writer
	HTTPClient *http.Client
}

func Publish(ctx context.Context, opts PublishOptions) (*PublishResult, error) {
	if err := normalizePublishOptions(&opts); err != nil {
		return nil, err
	}
	logf := func(string, ...any) {}
	if opts.Logger != nil {
		logf = opts.Logger.Printf
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	body, err := io.ReadAll(opts.Body)
	if err != nil {
		return nil, err
	}

	endpoint, err := publishEndpoint(opts.ServerURL, opts.ResourceURL, opts.Encoding)
	if err != nil {
		return nil, err
	}
	logf("publish: posting %d bytes to %s", len(body), endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	postStart := time.Now()
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	logf("publish: server responded %s after %s", resp.Status, time.Since(postStart).Round(time.Millisecond))
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, responseError("publish failed", resp, body)
	}

	dec := json.NewDecoder(resp.Body)
	event, err := readReadyEvent(dec, opts)
	if err != nil {
		return nil, err
	}
	logf("publish: ready resource_url=%s cache_url=%s resource_bytes=%d sha256=%s", event.ResourceURL, event.CacheURL, event.ResourceBytes, event.SHA256)
	if opts.WaitMode == "origin-fetch" {
		return publishUntilOriginFetch(ctx, opts, dec, event, logf)
	}
	if err := waitForCache(ctx, opts.HTTPClient, event, opts.Encoding, opts.PollInterval, logf); err != nil {
		return nil, err
	}
	logf("publish: verified AMP Cache response; confirming server")
	if err := confirmCached(ctx, opts.HTTPClient, opts.ServerURL, event.ResourceURL, event.SHA256); err != nil {
		return nil, err
	}

	for {
		event = PublishEvent{}
		if err := dec.Decode(&event); err != nil {
			return nil, fmt.Errorf("waiting for cached event: %w", err)
		}
		switch event.Event {
		case "cached":
			logf("publish: server emitted cached for %s", event.ResourceURL)
			return &PublishResult{
				Encoding:    event.Encoding,
				ResourceURL: event.ResourceURL,
				CacheURL:    event.CacheURL,
				SHA256:      event.SHA256,
				Length:      event.Length,
			}, nil
		case "fetched":
			logf("publish: origin fetch observed user_agent=%q fetch_count=%d", event.UserAgent, event.FetchCount)
			continue
		case "error":
			return nil, errors.New(event.Error)
		default:
			return nil, fmt.Errorf("unexpected publish event %q", event.Event)
		}
	}
}

func normalizePublishOptions(opts *PublishOptions) error {
	if opts.ServerURL == "" {
		return errors.New("server URL is required")
	}
	if opts.ResourceURL == "" {
		return errors.New("resource URL is required")
	}
	if err := ValidateEncoding(opts.Encoding); err != nil {
		return err
	}
	if _, err := CreateCacheURL(DefaultCacheDomain, opts.ResourceURL, opts.Encoding); err != nil {
		return err
	}
	if opts.Body == nil {
		return errors.New("publish body is required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Timeout == 0 {
		opts.Timeout = time.Duration(DefaultPublishTimeout)
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Duration(DefaultPollInterval)
	}
	if opts.WaitMode == "" {
		opts.WaitMode = "cached"
	}
	if opts.WaitMode != "cached" && opts.WaitMode != "origin-fetch" {
		return fmt.Errorf("unsupported wait mode %q", opts.WaitMode)
	}
	return nil
}

func readReadyEvent(dec *json.Decoder, opts PublishOptions) (PublishEvent, error) {
	var event PublishEvent
	if err := dec.Decode(&event); err != nil {
		return event, fmt.Errorf("reading ready event: %w", err)
	}
	if event.Event == "error" {
		return event, errors.New(event.Error)
	}
	if event.Event != "ready" {
		return event, fmt.Errorf("expected ready event, got %q", event.Event)
	}
	if event.Encoding != string(opts.Encoding) {
		return event, fmt.Errorf("server returned encoding %q, want %q", event.Encoding, opts.Encoding)
	}
	if event.ResourceURL != opts.ResourceURL {
		return event, fmt.Errorf("server returned resource URL %q, want %q", event.ResourceURL, opts.ResourceURL)
	}
	return event, nil
}

func publishUntilOriginFetch(ctx context.Context, opts PublishOptions, dec *json.Decoder, ready PublishEvent, logf func(string, ...any)) (*PublishResult, error) {
	triggerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		inflight := make(chan struct{}, 4)
		attempt := 0
		for {
			attempt++
			select {
			case inflight <- struct{}{}:
				go func(attempt int) {
					defer func() { <-inflight }()
					start := time.Now()
					ok, detail, err := cacheHasVerifiedResource(triggerCtx, opts.HTTPClient, ready, opts.Encoding)
					elapsed := time.Since(start).Round(time.Millisecond)
					if err != nil {
						logf("publish: origin fetch trigger #%d after %s: error=%v", attempt, elapsed, err)
						return
					}
					if ok {
						logf("publish: origin fetch trigger #%d after %s: %s", attempt, elapsed, detail)
						if err := confirmCached(triggerCtx, opts.HTTPClient, opts.ServerURL, ready.ResourceURL, ready.SHA256); err != nil {
							logf("publish: origin fetch trigger #%d confirm after cache-ready failed: %v", attempt, err)
						}
						return
					}
					if attempt == 1 || attempt%20 == 0 {
						logf("publish: origin fetch trigger #%d after %s: %s", attempt, elapsed, detail)
					}
				}(attempt)
			default:
				if attempt == 1 || attempt%20 == 0 {
					logf("publish: origin fetch trigger #%d skipped: maximum in-flight triggers reached", attempt)
				}
			}
			timer := time.NewTimer(opts.PollInterval)
			select {
			case <-triggerCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	logf("publish: waiting for first origin fetch from AMP Cache; poll_interval=%s", opts.PollInterval)
	for {
		var event PublishEvent
		if err := dec.Decode(&event); err != nil {
			return nil, fmt.Errorf("waiting for fetched event: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		switch event.Event {
		case "fetched":
			logf("publish: origin fetched by cache user_agent=%q fetch_count=%d", event.UserAgent, event.FetchCount)
			return &PublishResult{
				Encoding:    ready.Encoding,
				ResourceURL: ready.ResourceURL,
				CacheURL:    ready.CacheURL,
				SHA256:      ready.SHA256,
				Length:      ready.Length,
			}, nil
		case "cached":
			return &PublishResult{
				Encoding:    event.Encoding,
				ResourceURL: event.ResourceURL,
				CacheURL:    event.CacheURL,
				SHA256:      event.SHA256,
				Length:      event.Length,
			}, nil
		case "error":
			return nil, errors.New(event.Error)
		default:
			return nil, fmt.Errorf("unexpected publish event %q", event.Event)
		}
	}
}

func Get(ctx context.Context, opts GetOptions) (*Manifest, error) {
	if opts.CacheURL == "" {
		return nil, errors.New("cache URL is required")
	}
	if opts.Output == nil {
		return nil, errors.New("output writer is required")
	}
	if err := ValidateEncoding(opts.Encoding); err != nil {
		return nil, err
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.CacheURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, responseError("download failed", resp, body)
	}
	resource, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	payload, manifest, err := DecodeResource(resource, opts.Encoding)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(opts.Output, bytes.NewReader(payload)); err != nil {
		return nil, err
	}
	return manifest, nil
}

func waitForCache(ctx context.Context, client *http.Client, event PublishEvent, enc Encoding, pollInterval time.Duration, logf func(string, ...any)) error {
	attempt := 0
	logf("publish: waiting for AMP Cache verification; poll_interval=%s", pollInterval)
	for {
		attempt++
		start := time.Now()
		ok, detail, err := cacheHasVerifiedResource(ctx, client, event, enc)
		logf("publish: AMP Cache poll #%d after %s: %s", attempt, time.Since(start).Round(time.Millisecond), detail)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func cacheHasVerifiedResource(ctx context.Context, client *http.Client, event PublishEvent, enc Encoding) (bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, event.CacheURL, nil)
	if err != nil {
		return false, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, "request error: " + err.Error(), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, "status=" + resp.Status, nil
	}
	resource, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", err
	}
	_, manifest, err := DecodeResource(resource, enc)
	if err != nil {
		return false, fmt.Sprintf("status=%s bytes=%d decode_error=%v", resp.Status, len(resource), err), nil
	}
	if manifest.Length != event.Length {
		return false, fmt.Sprintf("status=%s bytes=%d length_mismatch=%d", resp.Status, len(resource), manifest.Length), nil
	}
	if manifest.SHA256 != event.SHA256 {
		return false, fmt.Sprintf("status=%s bytes=%d sha256_mismatch=%s", resp.Status, len(resource), manifest.SHA256), nil
	}
	return true, fmt.Sprintf("verified status=%s bytes=%d", resp.Status, len(resource)), nil
}

func confirmCached(ctx context.Context, client *http.Client, serverURL, resourceURL, sha string) error {
	endpoint, err := confirmEndpoint(serverURL, resourceURL, sha)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return responseError("confirm failed", resp, body)
	}
	return nil
}

func responseError(prefix string, resp *http.Response, body []byte) error {
	message := fmt.Sprintf("%s: %s", prefix, resp.Status)
	if location := resp.Header.Get("Location"); location != "" {
		message += ": Location: " + location
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		message += ": " + trimmed
	}
	return errors.New(message)
}
