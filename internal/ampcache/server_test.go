package ampcache

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestServerPublishConfirmLifecycle(t *testing.T) {
	store, ts := newTestServer(t, 1<<20)
	defer ts.Close()

	resourceURL := ts.URL + "/assets/payload.woff2"
	pub := startPublish(t, ts.URL, resourceURL, EncodingFont, []byte("server lifecycle"))
	defer pub.close()

	if pub.ready.Event != "ready" {
		t.Fatalf("ready event = %q", pub.ready.Event)
	}
	if pub.ready.ResourceURL != resourceURL {
		t.Fatalf("ready resource URL = %q, want %q", pub.ready.ResourceURL, resourceURL)
	}
	if !store.HasActiveSession(resourceURL) {
		t.Fatalf("session was not active after ready")
	}

	resp, err := http.Get(resourceURL)
	if err != nil {
		t.Fatalf("GET resource: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resource status = %s, body %s", resp.Status, body)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("font CORS header = %q, want *", resp.Header.Get("Access-Control-Allow-Origin"))
	}
	if resp.Header.Get("Cache-Control") != "public, max-age=31536000" {
		t.Fatalf("Cache-Control = %q, want public, max-age=31536000", resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatalf("ETag header was empty")
	}
	payload, manifest, err := DecodeResource(body, EncodingFont)
	if err != nil {
		t.Fatalf("DecodeResource returned error: %v", err)
	}
	if string(payload) != "server lifecycle" {
		t.Fatalf("payload = %q", payload)
	}

	confirm(t, ts.URL, resourceURL, manifest.SHA256)
	var cached PublishEvent
	for {
		if err := pub.dec.Decode(&cached); err != nil {
			t.Fatalf("decode cached event: %v", err)
		}
		if cached.Event == "fetched" {
			continue
		}
		break
	}
	if cached.Event != "cached" {
		t.Fatalf("event = %q, want cached", cached.Event)
	}
	waitFor(t, func() bool { return store.ActiveCount() == 0 })

	resp, err = http.Get(resourceURL)
	if err != nil {
		t.Fatalf("GET inactive resource: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("inactive resource status = %s, want 503", resp.Status)
	}
}

func TestServerConditionalETagWorksAfterSessionEndsWithBackfillDisabled(t *testing.T) {
	store, ts := newTestServer(t, 1<<20)
	defer ts.Close()

	resourceURL := ts.URL + "/assets/conditional.ttf"
	pub := startPublish(t, ts.URL, resourceURL, EncodingFont, []byte("conditional payload"))
	defer pub.close()

	resp, err := http.Get(resourceURL)
	if err != nil {
		t.Fatalf("GET active resource: %v", err)
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("active resource status = %s, want 200", resp.Status)
	}
	if etag == "" {
		t.Fatalf("ETag header was empty")
	}

	pub.close()
	waitFor(t, func() bool { return store.ActiveCount() == 0 })

	req, err := http.NewRequest(http.MethodGet, resourceURL, nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	req.Header.Set("If-None-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("conditional GET inactive resource: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional inactive status = %s, want 304; body %q", resp.Status, body)
	}
	if resp.Header.Get("ETag") != etag {
		t.Fatalf("conditional ETag = %q, want %q", resp.Header.Get("ETag"), etag)
	}
}

func TestServerCancellationStopsServing(t *testing.T) {
	store, ts := newTestServer(t, 1<<20)
	defer ts.Close()

	resourceURL := ts.URL + "/assets/cancel.png"
	pub := startPublish(t, ts.URL, resourceURL, EncodingImage, []byte("cancel"))
	if !store.HasActiveSession(resourceURL) {
		t.Fatalf("session was not active after ready")
	}
	pub.close()
	waitFor(t, func() bool { return store.ActiveCount() == 0 })

	resp, err := http.Get(resourceURL)
	if err != nil {
		t.Fatalf("GET inactive resource: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("inactive resource status = %s, want 503", resp.Status)
	}
}

func TestServerBackfillsInactiveResourceFromAMPCache(t *testing.T) {
	var store *Server
	requestedCacheURL := make(chan string, 1)
	var backfillResource []byte
	cacheClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestedCacheURL <- req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(backfillResource)),
			Request:    req,
		}, nil
	})}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.ServeHTTP(w, r)
	}))
	defer ts.Close()

	var err error
	store, err = NewServer(ServerConfig{
		PublicURL:        ts.URL,
		CacheDomain:      DefaultCacheDomain,
		Timeout:          2 * time.Second,
		MaxResourceBytes: 1 << 20,
		Backfill:         true,
		HTTPClient:       cacheClient,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	payload := []byte("backfilled from amp cache")
	resourceURL := ts.URL + "/assets/backfill.ttf"
	var manifest *Manifest
	backfillResource, manifest, err = EncodeResource(payload, EncodingFont, resourceURL)
	if err != nil {
		t.Fatalf("EncodeResource returned error: %v", err)
	}
	expectedCacheURL, err := CreateCacheURL(DefaultCacheDomain, resourceURL, EncodingFont)
	if err != nil {
		t.Fatalf("CreateCacheURL returned error: %v", err)
	}

	pub := startPublish(t, ts.URL, resourceURL, EncodingFont, payload)
	confirm(t, ts.URL, resourceURL, manifest.SHA256)
	pub.close()
	waitFor(t, func() bool { return store.ActiveCount() == 0 })

	resp, err := http.Get(resourceURL)
	if err != nil {
		t.Fatalf("GET inactive backfilled resource: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("backfilled resource status = %s, body %s", resp.Status, body)
	}
	if resp.Header.Get("X-Ampcache-Backfill") != "hit" {
		t.Fatalf("X-Ampcache-Backfill = %q, want hit", resp.Header.Get("X-Ampcache-Backfill"))
	}
	gotPayload, _, err := DecodeResource(body, EncodingFont)
	if err != nil {
		t.Fatalf("DecodeResource returned error: %v", err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload = %q, want %q", gotPayload, payload)
	}
	select {
	case got := <-requestedCacheURL:
		if got != expectedCacheURL {
			t.Fatalf("requested cache URL = %q, want %q", got, expectedCacheURL)
		}
	default:
		t.Fatalf("server did not request AMP Cache URL")
	}
}

func TestServerConditionalETagSkipsBackfill(t *testing.T) {
	var store *Server
	cacheRequests := make(chan string, 1)
	cacheClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cacheRequests <- req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte("should not be used"))),
			Request:    req,
		}, nil
	})}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.ServeHTTP(w, r)
	}))
	defer ts.Close()

	var err error
	store, err = NewServer(ServerConfig{
		PublicURL:        ts.URL,
		CacheDomain:      DefaultCacheDomain,
		Timeout:          2 * time.Second,
		MaxResourceBytes: 1 << 20,
		Backfill:         true,
		HTTPClient:       cacheClient,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	resourceURL := ts.URL + "/assets/conditional-backfill.ttf"
	pub := startPublish(t, ts.URL, resourceURL, EncodingFont, []byte("conditional payload"))
	resp, err := http.Get(resourceURL)
	if err != nil {
		pub.close()
		t.Fatalf("GET active resource: %v", err)
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		pub.close()
		t.Fatalf("active resource status = %s, want 200", resp.Status)
	}
	if etag == "" {
		pub.close()
		t.Fatalf("ETag header was empty")
	}
	pub.close()
	waitFor(t, func() bool { return store.ActiveCount() == 0 })

	req, err := http.NewRequest(http.MethodGet, resourceURL, nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	req.Header.Set("If-None-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("conditional GET inactive resource: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional inactive status = %s, want 304", resp.Status)
	}
	select {
	case got := <-cacheRequests:
		t.Fatalf("server attempted backfill for conditional request: %s", got)
	default:
	}
}

func TestServerResourceMissUses503OnlyForCacheableResourcePaths(t *testing.T) {
	_, ts := newTestServer(t, 1<<20)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/assets/missing.ttf")
	if err != nil {
		t.Fatalf("GET missing cacheable resource: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("cacheable miss status = %s, want 503", resp.Status)
	}

	resp, err = http.Get(ts.URL + "/assets/missing.txt")
	if err != nil {
		t.Fatalf("GET missing unsupported resource: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unsupported miss status = %s, want 404", resp.Status)
	}
}

func TestServerBackfillFailureReturns503ForCacheableResourcePath(t *testing.T) {
	var store *Server
	cacheClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte("cache miss"))),
			Request:    req,
		}, nil
	})}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.ServeHTTP(w, r)
	}))
	defer ts.Close()

	var err error
	store, err = NewServer(ServerConfig{
		PublicURL:        ts.URL,
		CacheDomain:      DefaultCacheDomain,
		Timeout:          2 * time.Second,
		MaxResourceBytes: 1 << 20,
		Backfill:         true,
		HTTPClient:       cacheClient,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	resp, err := http.Get(ts.URL + "/assets/missing.ttf")
	if err != nil {
		t.Fatalf("GET missing backfilled resource: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("backfill miss status = %s, want 503", resp.Status)
	}
}

func TestServerSkipsBackfillForGoogleAMPHTMLUserAgent(t *testing.T) {
	var store *Server
	cacheRequests := make(chan string, 1)
	cacheClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cacheRequests <- req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte("should not be used"))),
			Request:    req,
		}, nil
	})}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.ServeHTTP(w, r)
	}))
	defer ts.Close()

	var err error
	store, err = NewServer(ServerConfig{
		PublicURL:        ts.URL,
		CacheDomain:      DefaultCacheDomain,
		Timeout:          2 * time.Second,
		MaxResourceBytes: 1 << 20,
		Backfill:         true,
		HTTPClient:       cacheClient,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/assets/missing.ttf", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Google-AMPHTML)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET missing Google-AMPHTML resource: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Google-AMPHTML miss status = %s, want 503", resp.Status)
	}
	select {
	case got := <-cacheRequests:
		t.Fatalf("server attempted backfill for Google-AMPHTML request: %s", got)
	default:
	}
}

func TestServerRejectsOverMaxResourceSize(t *testing.T) {
	_, ts := newTestServer(t, 128)
	defer ts.Close()

	resourceURL := ts.URL + "/assets/too-big.html"
	endpoint, err := publishEndpoint(ts.URL, resourceURL, EncodingHTML)
	if err != nil {
		t.Fatalf("publishEndpoint returned error: %v", err)
	}
	resp, err := http.Post(endpoint, "application/octet-stream", bytes.NewReader(bytes.Repeat([]byte("x"), 200)))
	if err != nil {
		t.Fatalf("POST publish: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %s, want 413; body %s", resp.Status, body)
	}
}

type openPublish struct {
	cancel context.CancelFunc
	resp   *http.Response
	dec    *json.Decoder
	ready  PublishEvent
}

func newTestServer(t *testing.T, maxResourceBytes int64) (*Server, *httptest.Server) {
	t.Helper()
	var store *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.ServeHTTP(w, r)
	}))
	var err error
	store, err = NewServer(ServerConfig{
		PublicURL:        ts.URL,
		CacheDomain:      DefaultCacheDomain,
		Timeout:          2 * time.Second,
		MaxResourceBytes: maxResourceBytes,
	})
	if err != nil {
		ts.Close()
		t.Fatalf("NewServer returned error: %v", err)
	}
	return store, ts
}

func startPublish(t *testing.T, serverURL, resourceURL string, enc Encoding, payload []byte) *openPublish {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	endpoint, err := publishEndpoint(serverURL, resourceURL, enc)
	if err != nil {
		cancel()
		t.Fatalf("publishEndpoint: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		cancel()
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("POST publish: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		t.Fatalf("publish status = %s, body %s", resp.Status, body)
	}
	dec := json.NewDecoder(resp.Body)
	var ready PublishEvent
	if err := dec.Decode(&ready); err != nil {
		resp.Body.Close()
		cancel()
		t.Fatalf("Decode ready: %v", err)
	}
	return &openPublish{cancel: cancel, resp: resp, dec: dec, ready: ready}
}

func (p *openPublish) close() {
	if p == nil {
		return
	}
	p.cancel()
	if p.resp != nil && p.resp.Body != nil {
		_ = p.resp.Body.Close()
	}
}

func confirm(t *testing.T, serverURL, resourceURL, sha string) {
	t.Helper()
	endpoint, err := confirmEndpoint(serverURL, resourceURL, sha)
	if err != nil {
		t.Fatalf("confirmEndpoint returned error: %v", err)
	}
	resp, err := http.Post(endpoint, "text/plain", nil)
	if err != nil {
		t.Fatalf("POST confirm: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("confirm status = %s, body %s", resp.Status, body)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition did not become true before timeout")
}

func TestResourceKeyIncludesQuery(t *testing.T) {
	u, err := url.Parse("https://example.com/a/payload.woff2?x=1")
	if err != nil {
		t.Fatalf("url.Parse returned error: %v", err)
	}
	if got, want := ResourceKey(u), "/a/payload.woff2?x=1"; got != want {
		t.Fatalf("ResourceKey = %q, want %q", got, want)
	}
}
