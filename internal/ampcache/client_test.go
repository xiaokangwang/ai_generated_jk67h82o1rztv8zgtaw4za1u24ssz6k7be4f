package ampcache

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestClientPublishRejectsFontRootURLBeforeNetwork(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})}
	_, err := Publish(context.Background(), PublishOptions{
		ServerURL:   "https://example.com",
		ResourceURL: "https://example.com/",
		Encoding:    EncodingFont,
		Body:        bytes.NewReader([]byte("payload")),
		HTTPClient:  client,
		Timeout:     time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "font-like resource URL extension") {
		t.Fatalf("Publish error = %v, want font extension error", err)
	}
	if called {
		t.Fatalf("HTTP client was called despite invalid resource URL")
	}
}

func TestClientPublishUsesFakeAMPCache(t *testing.T) {
	store, origin := newTestServer(t, 1<<20)
	defer origin.Close()

	fakeCache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originURL := originURLFromCachePath(t, r.URL.Path)
		resp, err := http.Get(originURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer fakeCache.Close()

	client := &http.Client{Transport: rewriteCacheTransport(t, fakeCache.URL, http.DefaultTransport)}
	resourceURL := origin.URL + "/payload/client.woff2"
	payload := []byte("client publish payload")
	result, err := Publish(context.Background(), PublishOptions{
		ServerURL:    origin.URL,
		ResourceURL:  resourceURL,
		Encoding:     EncodingFont,
		Body:         bytes.NewReader(payload),
		HTTPClient:   client,
		Timeout:      2 * time.Second,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if result.ResourceURL != resourceURL {
		t.Fatalf("result resource URL = %q, want %q", result.ResourceURL, resourceURL)
	}
	if result.CacheURL == "" || !strings.Contains(result.CacheURL, ".cdn.ampproject.org/r/") {
		t.Fatalf("unexpected cache URL %q", result.CacheURL)
	}
	waitFor(t, func() bool { return store.ActiveCount() == 0 })
}

func TestClientPublishFollowsReplayableTemporaryRedirect(t *testing.T) {
	store, origin := newTestServer(t, 1<<20)
	defer origin.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, origin.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	fakeCache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originURL := originURLFromCachePath(t, r.URL.Path)
		resp, err := http.Get(originURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer fakeCache.Close()

	client := &http.Client{Transport: rewriteCacheTransport(t, fakeCache.URL, http.DefaultTransport)}
	resourceURL := origin.URL + "/payload/redirect.woff2"
	result, err := Publish(context.Background(), PublishOptions{
		ServerURL:    redirector.URL,
		ResourceURL:  resourceURL,
		Encoding:     EncodingFont,
		Body:         bytes.NewReader([]byte("redirect payload")),
		HTTPClient:   client,
		Timeout:      2 * time.Second,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if result.ResourceURL != resourceURL {
		t.Fatalf("result resource URL = %q, want %q", result.ResourceURL, resourceURL)
	}
	waitFor(t, func() bool { return store.ActiveCount() == 0 })
}

func TestClientPublishOriginFetchModeReturnsBeforeCachedAndCleansUp(t *testing.T) {
	store, origin := newTestServer(t, 1<<20)
	defer origin.Close()

	fakeCache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originURL := originURLFromCachePath(t, r.URL.Path)
		resp, err := http.Get(originURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		http.Error(w, "not public yet", http.StatusNotFound)
	}))
	defer fakeCache.Close()

	client := &http.Client{Transport: rewriteCacheTransport(t, fakeCache.URL, http.DefaultTransport)}
	resourceURL := origin.URL + "/payload/origin-fetch.woff2"
	result, err := Publish(context.Background(), PublishOptions{
		ServerURL:    origin.URL,
		ResourceURL:  resourceURL,
		Encoding:     EncodingFont,
		Body:         bytes.NewReader([]byte("origin fetch payload")),
		HTTPClient:   client,
		Timeout:      2 * time.Second,
		PollInterval: time.Millisecond,
		WaitMode:     "origin-fetch",
	})
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if result.ResourceURL != resourceURL {
		t.Fatalf("result resource URL = %q, want %q", result.ResourceURL, resourceURL)
	}
	waitFor(t, func() bool { return store.ActiveCount() == 0 })
}

func TestClientGetDownloadsAndValidates(t *testing.T) {
	payload := []byte("download payload")
	resource, manifest, err := EncodeResource(payload, EncodingImage, "https://example.com/payload.png")
	if err != nil {
		t.Fatalf("EncodeResource returned error: %v", err)
	}
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ContentType(EncodingImage))
		_, _ = w.Write(resource)
	}))
	defer cache.Close()

	var out bytes.Buffer
	gotManifest, err := Get(context.Background(), GetOptions{
		CacheURL: cache.URL + "/i/s/example.com/payload.png",
		Encoding: EncodingImage,
		Output:   &out,
	})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("output = %q, want %q", out.Bytes(), payload)
	}
	if gotManifest.SHA256 != manifest.SHA256 {
		t.Fatalf("manifest sha = %q, want %q", gotManifest.SHA256, manifest.SHA256)
	}
}

type rewriteRoundTripper struct {
	t          *testing.T
	cacheURL   *url.URL
	underlying http.RoundTripper
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func rewriteCacheTransport(t *testing.T, cacheServerURL string, underlying http.RoundTripper) http.RoundTripper {
	t.Helper()
	u, err := url.Parse(cacheServerURL)
	if err != nil {
		t.Fatalf("url.Parse returned error: %v", err)
	}
	return rewriteRoundTripper{t: t, cacheURL: u, underlying: underlying}
}

func (rt rewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if strings.HasSuffix(clone.URL.Hostname(), "cdn.ampproject.org") {
		clone.URL.Scheme = rt.cacheURL.Scheme
		clone.URL.Host = rt.cacheURL.Host
	}
	return rt.underlying.RoundTrip(clone)
}

func originURLFromCachePath(t *testing.T, p string) string {
	t.Helper()
	trimmed := strings.TrimPrefix(p, "/r/")
	if trimmed == p {
		t.Fatalf("cache path %q did not use /r/", p)
	}
	scheme := "http"
	if strings.HasPrefix(trimmed, "s/") {
		scheme = "https"
		trimmed = strings.TrimPrefix(trimmed, "s/")
	}
	host, rest, ok := strings.Cut(trimmed, "/")
	if !ok {
		t.Fatalf("cache path %q did not include origin path", p)
	}
	return scheme + "://" + host + "/" + rest
}
