package ampcache

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type ServerConfig struct {
	PublicURL        string
	CacheDomain      string
	Timeout          time.Duration
	MaxResourceBytes int64
	Backfill         bool
	HTTPClient       *http.Client
	Logger           *log.Logger
}

type Server struct {
	publicURL        string
	cacheDomain      string
	timeout          time.Duration
	maxResourceBytes int64
	backfill         bool
	httpClient       *http.Client
	logger           *log.Logger
	etagKey          []byte

	mu       sync.Mutex
	active   map[string]*session
	activeBy map[string]*session
}

type session struct {
	resourceURL   string
	resourceKey   string
	cacheURL      string
	encoding      Encoding
	sha256        string
	length        int64
	resourceBytes []byte

	fetches    chan PublishEvent
	fetchMu    sync.Mutex
	fetchCount int
	cached     chan struct{}
	cachedMu   sync.Once
	onCached   func(*session)
}

func NewServer(config ServerConfig) (*Server, error) {
	if _, err := parsePublicURL(config.PublicURL); err != nil {
		return nil, err
	}
	if config.MaxResourceBytes <= 0 {
		return nil, errors.New("max resource bytes must be greater than zero")
	}
	if config.CacheDomain == "" {
		config.CacheDomain = DefaultCacheDomain
	}
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Minute
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	etagKey := make([]byte, 32)
	if _, err := rand.Read(etagKey); err != nil {
		return nil, fmt.Errorf("generate etag key: %w", err)
	}
	return &Server{
		publicURL:        strings.TrimRight(config.PublicURL, "/"),
		cacheDomain:      strings.TrimSuffix(config.CacheDomain, "."),
		timeout:          config.Timeout,
		maxResourceBytes: config.MaxResourceBytes,
		backfill:         config.Backfill,
		httpClient:       config.HTTPClient,
		logger:           config.Logger,
		etagKey:          etagKey,
		active:           make(map[string]*session),
		activeBy:         make(map[string]*session),
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/publish":
		s.handlePublish(w, r)
	case "/v1/confirm":
		s.handleConfirm(w, r)
	default:
		s.handleResource(w, r)
	}
}

func (s *Server) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

func (s *Server) HasActiveSession(resourceURL string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[resourceURL]
	return ok
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	requestStart := time.Now()
	enc, err := ParseEncoding(r.URL.Query().Get("encoding"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resourceURL := r.URL.Query().Get("resource_url")
	resource, err := ValidateResourceURL(s.publicURL, resourceURL, enc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logf("publish: start encoding=%s resource_url=%s remote=%s", enc, resourceURL, r.RemoteAddr)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resourceBytes, manifest, err := EncodeResource(payload, enc, resourceURL)
	payload = nil
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if int64(len(resourceBytes)) > s.maxResourceBytes {
		s.logf("publish: reject resource_url=%s payload_bytes=%d resource_bytes=%d max_resource_bytes=%d", resourceURL, manifest.Length, len(resourceBytes), s.maxResourceBytes)
		http.Error(w, fmt.Sprintf("encoded resource is %d bytes; max is %d", len(resourceBytes), s.maxResourceBytes), http.StatusRequestEntityTooLarge)
		return
	}
	cacheURL, err := CreateCacheURL(s.cacheDomain, resourceURL, enc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess := &session{
		resourceURL:   resourceURL,
		resourceKey:   ResourceKey(resource),
		cacheURL:      cacheURL,
		encoding:      enc,
		sha256:        manifest.SHA256,
		length:        manifest.Length,
		resourceBytes: resourceBytes,
		fetches:       make(chan PublishEvent, 8),
		cached:        make(chan struct{}),
	}
	sess.onCached = func(done *session) {
		s.removeSession(done)
	}
	if !s.addSession(sess) {
		http.Error(w, "a publish request for this resource URL is already active", http.StatusConflict)
		return
	}
	defer s.removeSession(sess)
	s.logf("publish: ready resource_url=%s cache_url=%s payload_bytes=%d resource_bytes=%d sha256=%s after=%s", resourceURL, cacheURL, manifest.Length, len(resourceBytes), manifest.SHA256, time.Since(requestStart).Round(time.Millisecond))

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	if err := writePublishEvent(w, flusher, PublishEvent{
		Event:         "ready",
		Encoding:      string(enc),
		ResourceURL:   resourceURL,
		CacheURL:      cacheURL,
		SHA256:        manifest.SHA256,
		Length:        manifest.Length,
		ResourceBytes: int64(len(resourceBytes)),
	}); err != nil {
		return
	}

	var timeout <-chan time.Time
	var timer *time.Timer
	if s.timeout > 0 {
		timer = time.NewTimer(s.timeout)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case <-sess.cached:
		s.logf("publish: cached resource_url=%s total=%s", resourceURL, time.Since(requestStart).Round(time.Millisecond))
		_ = writePublishEvent(w, flusher, PublishEvent{
			Event:       "cached",
			Encoding:    string(enc),
			ResourceURL: resourceURL,
			CacheURL:    cacheURL,
			SHA256:      manifest.SHA256,
			Length:      manifest.Length,
		})
	case <-r.Context().Done():
		s.logf("publish: cancelled resource_url=%s after=%s err=%v", resourceURL, time.Since(requestStart).Round(time.Millisecond), r.Context().Err())
		return
	case fetch := <-sess.fetches:
		s.logf("publish: fetched resource_url=%s fetch_count=%d after=%s user_agent=%q", resourceURL, fetch.FetchCount, time.Since(requestStart).Round(time.Millisecond), fetch.UserAgent)
		_ = writePublishEvent(w, flusher, fetch)
		goto waitLoop
	case <-timeout:
		s.logf("publish: timeout resource_url=%s after=%s", resourceURL, time.Since(requestStart).Round(time.Millisecond))
		_ = writePublishEvent(w, flusher, PublishEvent{
			Event:       "error",
			Encoding:    string(enc),
			ResourceURL: resourceURL,
			SHA256:      manifest.SHA256,
			Error:       "publish timed out",
		})
	}
	return

waitLoop:
	for {
		select {
		case <-sess.cached:
			s.logf("publish: cached resource_url=%s total=%s", resourceURL, time.Since(requestStart).Round(time.Millisecond))
			_ = writePublishEvent(w, flusher, PublishEvent{
				Event:       "cached",
				Encoding:    string(enc),
				ResourceURL: resourceURL,
				CacheURL:    cacheURL,
				SHA256:      manifest.SHA256,
				Length:      manifest.Length,
			})
			return
		case <-r.Context().Done():
			s.logf("publish: cancelled resource_url=%s after=%s err=%v", resourceURL, time.Since(requestStart).Round(time.Millisecond), r.Context().Err())
			return
		case fetch := <-sess.fetches:
			s.logf("publish: fetched resource_url=%s fetch_count=%d after=%s user_agent=%q", resourceURL, fetch.FetchCount, time.Since(requestStart).Round(time.Millisecond), fetch.UserAgent)
			if err := writePublishEvent(w, flusher, fetch); err != nil {
				return
			}
		case <-timeout:
			s.logf("publish: timeout resource_url=%s after=%s", resourceURL, time.Since(requestStart).Round(time.Millisecond))
			_ = writePublishEvent(w, flusher, PublishEvent{
				Event:       "error",
				Encoding:    string(enc),
				ResourceURL: resourceURL,
				SHA256:      manifest.SHA256,
				Error:       "publish timed out",
			})
			return
		}
	}
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resourceURL := r.URL.Query().Get("resource_url")
	sha := r.URL.Query().Get("sha256")
	s.logf("confirm: resource_url=%s remote=%s", resourceURL, r.RemoteAddr)
	sess := s.getSession(resourceURL)
	if sess == nil {
		s.logf("confirm: miss resource_url=%s", resourceURL)
		http.NotFound(w, r)
		return
	}
	if sha == "" || sha != sess.sha256 {
		s.logf("confirm: sha mismatch resource_url=%s got=%s want=%s", resourceURL, sha, sess.sha256)
		http.Error(w, "sha256 does not match active session", http.StatusConflict)
		return
	}
	sess.markCached()
	s.logf("confirm: accepted resource_url=%s", resourceURL)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := ResourceKey(r.URL)
	sess := s.getSessionByKey(key)
	if sess == nil {
		enc, cacheableResource := s.cacheableResourceRequestEncoding(r)
		if cacheableResource {
			etag := s.resourceETag(key)
			if ifNoneMatchContains(r.Header.Get("If-None-Match"), etag) {
				s.logf("resource: not_modified method=%s path=%s etag=%s remote=%s user_agent=%q", r.Method, r.URL.RequestURI(), etag, r.RemoteAddr, r.UserAgent())
				writeResourceNotModified(w, enc, etag)
				return
			}
		}
		if s.backfill && cacheableResource && !disableBackfillForUserAgent(r.UserAgent()) {
			served, status, err := s.backfillResource(w, r)
			if served {
				return
			}
			if err != nil {
				s.logf("resource: backfill failed method=%s path=%s status=%d remote=%s user_agent=%q err=%v", r.Method, r.URL.RequestURI(), status, r.RemoteAddr, r.UserAgent(), err)
			} else {
				s.logf("resource: backfill miss method=%s path=%s remote=%s user_agent=%q", r.Method, r.URL.RequestURI(), r.RemoteAddr, r.UserAgent())
			}
		} else if s.backfill && cacheableResource {
			s.logf("resource: backfill skipped method=%s path=%s remote=%s user_agent=%q", r.Method, r.URL.RequestURI(), r.RemoteAddr, r.UserAgent())
		}
		s.logf("resource: miss method=%s path=%s remote=%s user_agent=%q", r.Method, r.URL.RequestURI(), r.RemoteAddr, r.UserAgent())
		if cacheableResource {
			writeResourceUnavailable(w)
			return
		}
		http.NotFound(w, r)
		return
	}
	etag := s.resourceETag(key)
	if ifNoneMatchContains(r.Header.Get("If-None-Match"), etag) {
		s.logf("resource: not_modified method=%s path=%s etag=%s remote=%s user_agent=%q", r.Method, r.URL.RequestURI(), etag, r.RemoteAddr, r.UserAgent())
		writeResourceNotModified(w, sess.encoding, etag)
		return
	}
	s.logf("resource: serve method=%s path=%s bytes=%d encoding=%s remote=%s user_agent=%q", r.Method, r.URL.RequestURI(), len(sess.resourceBytes), sess.encoding, r.RemoteAddr, r.UserAgent())
	writeResourceHeaders(w, sess.encoding, len(sess.resourceBytes), etag)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(sess.resourceBytes)
	sess.markFetched(r.UserAgent())
}

func (s *Server) backfillResource(w http.ResponseWriter, r *http.Request) (bool, int, error) {
	enc, err := inferEncodingFromResourcePath(r.URL.Path)
	if err != nil {
		return false, 0, err
	}
	resourceURL, err := s.resourceURLForRequest(r)
	if err != nil {
		return false, 0, err
	}
	if _, err := ValidateResourceURL(s.publicURL, resourceURL, enc); err != nil {
		return false, 0, err
	}
	cacheURL, err := CreateCacheURL(s.cacheDomain, resourceURL, enc)
	if err != nil {
		return false, 0, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cacheURL, nil)
	if err != nil {
		return false, http.StatusBadGateway, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return false, http.StatusNotFound, fmt.Errorf("cache returned %s", resp.Status)
		}
		return false, http.StatusBadGateway, fmt.Errorf("cache returned %s", resp.Status)
	}
	resourceBytes, err := readMaxBytes(resp.Body, s.maxResourceBytes)
	if err != nil {
		return false, http.StatusBadGateway, err
	}
	if _, _, err := DecodeResource(resourceBytes, enc); err != nil {
		return false, http.StatusBadGateway, fmt.Errorf("decode backfill resource: %w", err)
	}

	s.logf("resource: backfill serve method=%s path=%s cache_url=%s bytes=%d encoding=%s remote=%s user_agent=%q", r.Method, r.URL.RequestURI(), cacheURL, len(resourceBytes), enc, r.RemoteAddr, r.UserAgent())
	writeResourceHeaders(w, enc, len(resourceBytes), s.resourceETag(ResourceKey(r.URL)))
	w.Header().Set("X-Ampcache-Backfill", "hit")
	if r.Method == http.MethodHead {
		return true, http.StatusOK, nil
	}
	_, _ = w.Write(resourceBytes)
	return true, http.StatusOK, nil
}

func (s *Server) cacheableResourceRequestEncoding(r *http.Request) (Encoding, bool) {
	enc, err := inferEncodingFromResourcePath(r.URL.Path)
	if err != nil {
		return "", false
	}
	resourceURL, err := s.resourceURLForRequest(r)
	if err != nil {
		return "", false
	}
	_, err = ValidateResourceURL(s.publicURL, resourceURL, enc)
	return enc, err == nil
}

func disableBackfillForUserAgent(userAgent string) bool {
	return strings.Contains(userAgent, "Google-AMPHTML")
}

func (s *Server) resourceETag(resourceKey string) string {
	mac := hmac.New(sha256.New, s.etagKey)
	_, _ = mac.Write([]byte(resourceKey))
	tag := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return `"` + "ampcache-" + tag + `"`
}

func ifNoneMatchContains(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		candidate := strings.TrimSpace(part)
		if strings.HasPrefix(candidate, "W/") {
			candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "W/"))
		}
		if candidate == etag {
			return true
		}
	}
	return false
}

func (s *Server) resourceURLForRequest(r *http.Request) (string, error) {
	base, err := parsePublicURL(s.publicURL)
	if err != nil {
		return "", err
	}
	resource := *base
	resource.Path = r.URL.Path
	resource.RawPath = r.URL.RawPath
	resource.RawQuery = r.URL.RawQuery
	resource.Fragment = ""
	return resource.String(), nil
}

func writeResourceUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "60")
	http.Error(w, "resource temporarily unavailable", http.StatusServiceUnavailable)
}

func writeResourceCacheHeaders(w http.ResponseWriter, enc Encoding, etag string) {
	w.Header().Set("Content-Type", ContentType(enc))
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	w.Header().Set("ETag", etag)
	if enc == EncodingFont {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
}

func writeResourceHeaders(w http.ResponseWriter, enc Encoding, length int, etag string) {
	writeResourceCacheHeaders(w, enc, etag)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", length))
}

func writeResourceNotModified(w http.ResponseWriter, enc Encoding, etag string) {
	writeResourceCacheHeaders(w, enc, etag)
	w.WriteHeader(http.StatusNotModified)
}

func readMaxBytes(r io.Reader, max int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.CopyN(&buf, r, max+1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n > max {
		return nil, fmt.Errorf("backfill resource is larger than max resource bytes %d", max)
	}
	return buf.Bytes(), nil
}

func (s *Server) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

func (s *Server) addSession(sess *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.active[sess.resourceURL]; exists {
		return false
	}
	if _, exists := s.activeBy[sess.resourceKey]; exists {
		return false
	}
	s.active[sess.resourceURL] = sess
	s.activeBy[sess.resourceKey] = sess
	return true
}

func (s *Server) getSession(resourceURL string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[resourceURL]
}

func (s *Server) getSessionByKey(key string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeBy[key]
}

func (s *Server) removeSession(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[sess.resourceURL] == sess {
		delete(s.active, sess.resourceURL)
	}
	if s.activeBy[sess.resourceKey] == sess {
		delete(s.activeBy, sess.resourceKey)
	}
}

func (s *session) markCached() {
	s.cachedMu.Do(func() {
		if s.onCached != nil {
			s.onCached(s)
		}
		close(s.cached)
	})
}

func (s *session) markFetched(userAgent string) {
	s.fetchMu.Lock()
	s.fetchCount++
	count := s.fetchCount
	s.fetchMu.Unlock()
	event := PublishEvent{
		Event:       "fetched",
		Encoding:    string(s.encoding),
		ResourceURL: s.resourceURL,
		CacheURL:    s.cacheURL,
		SHA256:      s.sha256,
		Length:      s.length,
		FetchCount:  count,
		UserAgent:   userAgent,
	}
	select {
	case s.fetches <- event:
	default:
	}
}

func writePublishEvent(w http.ResponseWriter, flusher http.Flusher, event PublishEvent) error {
	if err := json.NewEncoder(w).Encode(event); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func publishEndpoint(serverURL, resourceURL string, enc Encoding) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/publish"
	q := u.Query()
	q.Set("encoding", string(enc))
	q.Set("resource_url", resourceURL)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func confirmEndpoint(serverURL, resourceURL, sha string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/confirm"
	q := u.Query()
	q.Set("resource_url", resourceURL)
	q.Set("sha256", sha)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
