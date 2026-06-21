package gomodstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultCachedOnlyURL = "https://proxy.golang.org/cached-only"

type ServerConfig struct {
	PublicURL     string
	CachedOnlyURL string
	Timeout       time.Duration
	MaxBodyBytes  int64
	ChunkSize     int
	Now           func() time.Time
	HTTPClient    *http.Client
}

type Server struct {
	publicURL     string
	cachedOnlyURL string
	timeout       time.Duration
	maxBodyBytes  int64
	chunkSize     int
	now           func() time.Time
	httpClient    *http.Client

	mu     sync.Mutex
	active map[string]*session
}

type session struct {
	artifacts *ArtifactSet

	mu       sync.Mutex
	served   map[string]bool
	cached   chan struct{}
	cachedMu sync.Once
	onCached func(*session)
}

func NewServer(config ServerConfig) *Server {
	if config.CachedOnlyURL == "" {
		config.CachedOnlyURL = defaultCachedOnlyURL
	}
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Minute
	}
	if config.ChunkSize == 0 {
		config.ChunkSize = DefaultChunkSize
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	return &Server{
		publicURL:     strings.TrimRight(config.PublicURL, "/"),
		cachedOnlyURL: strings.TrimRight(config.CachedOnlyURL, "/"),
		timeout:       config.Timeout,
		maxBodyBytes:  config.MaxBodyBytes,
		chunkSize:     config.ChunkSize,
		now:           config.Now,
		httpClient:    config.HTTPClient,
		active:        make(map[string]*session),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/publish":
		s.handlePublish(w, r)
	case strings.HasPrefix(r.URL.Path, "/proxy/"):
		s.handleProxy(w, r)
	case r.Method == http.MethodGet && r.URL.Query().Get("go-get") == "1":
		s.handleGoImport(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

func (s *Server) HasActiveSession(module string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[module]
	return ok
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	prefix := firstQuery(r, "module_prefix", "module-prefix", "prefix")
	if err := ValidateModulePrefix(prefix); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body := io.Reader(r.Body)
	if s.maxBodyBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	}
	payload, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	artifacts, err := BuildArtifacts(prefix, payload, s.now(), s.chunkSize)
	payload = nil
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess := &session{
		artifacts: artifacts,
		served:    make(map[string]bool, len(ArtifactKinds)),
		cached:    make(chan struct{}),
	}
	sess.onCached = func(done *session) {
		s.removeSession(done.artifacts.Module, done)
	}
	if !s.addSession(sess) {
		http.Error(w, "a publish request for this module is already active", http.StatusConflict)
		return
	}
	defer s.removeSession(artifacts.Module, sess)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	originURLs, err := ArtifactURLs(s.publicURLFor(r)+"/proxy", artifacts.Module, artifacts.Version)
	if err != nil {
		_ = writePublishEvent(w, flusher, PublishEvent{Event: "error", Error: err.Error()})
		return
	}
	if err := writePublishEvent(w, flusher, PublishEvent{
		Event:   "ready",
		Module:  artifacts.Module,
		Version: artifacts.Version,
		Time:    artifacts.Time.Format(time.RFC3339),
		URLs:    originURLs,
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
		_ = writePublishEvent(w, flusher, PublishEvent{
			Event:   "cached",
			Module:  artifacts.Module,
			Version: artifacts.Version,
		})
	case <-r.Context().Done():
		return
	case <-timeout:
		_ = writePublishEvent(w, flusher, PublishEvent{
			Event:   "error",
			Module:  artifacts.Module,
			Version: artifacts.Version,
			Error:   "publish timed out",
		})
	}
}

func (s *Server) handleGoImport(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.NotFound(w, r)
		return
	}
	escapedModule := strings.TrimPrefix(r.URL.Path, "/")
	module, err := UnescapeModulePath(escapedModule)
	if err != nil || ValidateModulePath(module) != nil {
		http.NotFound(w, r)
		return
	}
	if s.getSession(module) == nil {
		http.NotFound(w, r)
		return
	}

	repoRoot := s.publicURLFor(r) + "/proxy"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "<!doctype html><html><head><meta name=\"go-import\" content=\"%s mod %s\"></head><body></body></html>\n",
		html.EscapeString(module), html.EscapeString(repoRoot))
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	module, file, escapedRelative, err := parseProxyPath(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	sess := s.getSession(module)
	if sess == nil {
		s.proxyCachedOnly(w, r, escapedRelative)
		return
	}

	kind, ok := artifactKindForFile(file)
	if !ok {
		if file == "list" {
			serveBytes(w, r, []byte(ModuleVersion+"\n"), "text/plain; charset=utf-8")
			return
		}
		http.NotFound(w, r)
		return
	}

	var data []byte
	var contentType string
	switch kind {
	case "info":
		data = sess.artifacts.Info
		contentType = "application/json"
	case "mod":
		data = sess.artifacts.Mod
		contentType = "text/plain; charset=utf-8"
	case "zip":
		data = sess.artifacts.Zip
		contentType = "application/zip"
	default:
		http.NotFound(w, r)
		return
	}

	if serveBytes(w, r, data, contentType) && r.Method == http.MethodGet {
		sess.markServed(kind)
	}
}

func (s *Server) proxyCachedOnly(w http.ResponseWriter, r *http.Request, escapedRelative string) {
	target := s.cachedOnlyURL + "/" + escapedRelative
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	copyForwardHeaders(req.Header, r.Header)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, resp.Body)
	}
}

func (s *Server) addSession(sess *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	module := sess.artifacts.Module
	if _, exists := s.active[module]; exists {
		return false
	}
	s.active[module] = sess
	return true
}

func (s *Server) getSession(module string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[module]
}

func (s *Server) removeSession(module string, sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[module] == sess {
		delete(s.active, module)
	}
}

func (s *session) markServed(kind string) {
	var complete bool
	s.mu.Lock()
	s.served[kind] = true
	for _, required := range ArtifactKinds {
		if !s.served[required] {
			s.mu.Unlock()
			return
		}
	}
	complete = true
	s.mu.Unlock()

	if complete {
		s.cachedMu.Do(func() {
			if s.onCached != nil {
				s.onCached(s)
			}
			close(s.cached)
		})
	}
}

func parseProxyPath(r *http.Request) (module string, file string, escapedRelative string, err error) {
	rest := strings.TrimPrefix(r.URL.Path, "/proxy/")
	i := strings.Index(rest, "/@v/")
	if rest == "" || i <= 0 || i+len("/@v/") >= len(rest) {
		return "", "", "", errors.New("invalid proxy path")
	}
	escapedModule := rest[:i]
	module, err = UnescapeModulePath(escapedModule)
	if err != nil {
		return "", "", "", err
	}
	if err := ValidateModulePath(module); err != nil {
		return "", "", "", err
	}
	file = rest[i+len("/@v/"):]
	escapedPath := r.URL.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/proxy/") {
		return "", "", "", errors.New("invalid escaped proxy path")
	}
	escapedRelative = strings.TrimPrefix(escapedPath, "/proxy/")
	return module, file, escapedRelative, nil
}

func artifactKindForFile(file string) (string, bool) {
	for _, kind := range ArtifactKinds {
		if file == ModuleVersion+"."+kind {
			return kind, true
		}
	}
	return "", false
}

func serveBytes(w http.ResponseWriter, r *http.Request, data []byte, contentType string) bool {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if r.Method == http.MethodHead {
		return false
	}
	n, err := w.Write(data)
	return err == nil && n == len(data)
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

func firstQuery(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := r.URL.Query().Get(name); value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) publicURLFor(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if forwarded := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = forwarded
	}
	host := r.Host
	if forwarded := firstHeaderValue(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

func firstHeaderValue(value string) string {
	if value == "" {
		return ""
	}
	if i := strings.Index(value, ","); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

func copyForwardHeaders(dst, src http.Header) {
	for _, key := range []string{"Accept", "Accept-Encoding", "User-Agent"} {
		for _, value := range src.Values(key) {
			dst.Add(key, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Connection") || strings.EqualFold(key, "Keep-Alive") ||
			strings.EqualFold(key, "Proxy-Authenticate") || strings.EqualFold(key, "Proxy-Authorization") ||
			strings.EqualFold(key, "TE") || strings.EqualFold(key, "Trailer") ||
			strings.EqualFold(key, "Transfer-Encoding") || strings.EqualFold(key, "Upgrade") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
