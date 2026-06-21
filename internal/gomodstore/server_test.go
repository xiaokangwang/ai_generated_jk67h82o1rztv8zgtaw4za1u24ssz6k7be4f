package gomodstore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestServerPublishFlowStateLifecycle(t *testing.T) {
	store := NewServer(ServerConfig{
		Timeout:   time.Second,
		ChunkSize: 4,
		Now:       func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) },
	})
	ts := httptest.NewServer(store)
	defer ts.Close()

	pub := startPublish(t, ts.URL, "example.com/store", []byte("hello lifecycle"))
	defer pub.close()

	if pub.ready.Event != "ready" {
		t.Fatalf("ready event = %q", pub.ready.Event)
	}
	if !store.HasActiveSession(pub.ready.Module) {
		t.Fatalf("session was not active after ready")
	}

	metaResp, err := http.Get(ts.URL + "/" + pub.ready.Module + "?go-get=1")
	if err != nil {
		t.Fatalf("GET go-import metadata: %v", err)
	}
	metaBody, _ := io.ReadAll(metaResp.Body)
	metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		t.Fatalf("metadata status = %s, body %s", metaResp.Status, metaBody)
	}
	if !bytes.Contains(metaBody, []byte("go-import")) || !bytes.Contains(metaBody, []byte(pub.ready.Module+" mod ")) {
		t.Fatalf("metadata did not contain go-import mod tag: %s", metaBody)
	}

	for _, kind := range ArtifactKinds {
		resp := getArtifact(t, ts.URL, pub.ready.Module, kind)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %s, body %s", kind, resp.Status, body)
		}
		if len(body) == 0 {
			t.Fatalf("%s artifact was empty", kind)
		}
	}

	var cached PublishEvent
	if err := pub.dec.Decode(&cached); err != nil {
		t.Fatalf("reading cached event: %v", err)
	}
	if cached.Event != "cached" {
		t.Fatalf("event = %q, want cached", cached.Event)
	}
	waitFor(t, func() bool { return store.ActiveCount() == 0 })
}

func TestServerCancellationStopsPayloadServing(t *testing.T) {
	cachedOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "inactive", http.StatusTeapot)
	}))
	defer cachedOnly.Close()

	store := NewServer(ServerConfig{
		CachedOnlyURL: cachedOnly.URL,
		Timeout:       time.Second,
	})
	ts := httptest.NewServer(store)
	defer ts.Close()

	pub := startPublish(t, ts.URL, "example.com/store", []byte("cancel me"))
	module := pub.ready.Module
	if !store.HasActiveSession(module) {
		t.Fatalf("session was not active after ready")
	}

	pub.close()
	waitFor(t, func() bool { return store.ActiveCount() == 0 })

	resp := getArtifact(t, ts.URL, module, "mod")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("inactive artifact status = %s, want 418 from cached-only upstream; body %s", resp.Status, body)
	}
}

func TestServerInactiveArtifactsProxyToCachedOnly(t *testing.T) {
	var upstreamPath string
	cachedOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.EscapedPath()
		w.Header().Set("X-Upstream", "cached-only")
		_, _ = w.Write([]byte("from cached-only"))
	}))
	defer cachedOnly.Close()

	store := NewServer(ServerConfig{CachedOnlyURL: cachedOnly.URL})
	ts := httptest.NewServer(store)
	defer ts.Close()

	module, err := ModulePathForPayload("example.com/store", []byte("already cached"))
	if err != nil {
		t.Fatalf("ModulePathForPayload returned error: %v", err)
	}
	resp := getArtifact(t, ts.URL, module, "zip")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %s, body %s", resp.Status, body)
	}
	if string(body) != "from cached-only" {
		t.Fatalf("proxy body = %q", body)
	}
	if resp.Header.Get("X-Upstream") != "cached-only" {
		t.Fatalf("cached-only response header was not forwarded")
	}
	wantPath := "/" + module + "/@v/" + ModuleVersion + ".zip"
	if upstreamPath != wantPath {
		t.Fatalf("upstream path = %q, want %q", upstreamPath, wantPath)
	}
}

type openPublish struct {
	cancel context.CancelFunc
	resp   *http.Response
	dec    *json.Decoder
	ready  PublishEvent
}

func startPublish(t *testing.T, serverURL, prefix string, payload []byte) *openPublish {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	endpoint, err := url.Parse(serverURL + "/v1/publish")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	q := endpoint.Query()
	q.Set("module_prefix", prefix)
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
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

func getArtifact(t *testing.T, serverURL, module, kind string) *http.Response {
	t.Helper()
	u, err := ArtifactURL(serverURL+"/proxy", module, ModuleVersion, kind)
	if err != nil {
		t.Fatalf("ArtifactURL: %v", err)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET %s artifact: %v", kind, err)
	}
	return resp
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

func TestParseProxyPathRejectsInvalid(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/proxy/not-a-module/@v/"+ModuleVersion+".mod", nil)
	_, _, _, err := parseProxyPath(req)
	if err == nil || !strings.Contains(err.Error(), "module") {
		t.Fatalf("parseProxyPath error = %v, want module validation error", err)
	}
}
