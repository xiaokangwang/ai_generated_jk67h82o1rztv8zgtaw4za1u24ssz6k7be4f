package ampcache

import (
	"crypto/tls"
	"net/http"
	"strings"
)

const (
	DefaultDomainFrontingServerName = "www.google.com"
	DefaultUserAgent                = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.7827.116 Mobile Safari/537.36"
	DefaultCacheAccept              = "application/font-woff2;q=1.0,application/font-woff;q=0.9,*/*;q=0.8"
)

type domainFrontingTransport struct {
	normal        http.RoundTripper
	fronted       http.RoundTripper
	cacheDomain   string
	tlsServerName string
}

type cacheAcceptTransport struct {
	base        http.RoundTripper
	cacheDomain string
}

type userAgentTransport struct {
	base      http.RoundTripper
	userAgent string
}

func NewDomainFrontingTransport(base http.RoundTripper, cacheDomain, tlsServerName string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	cacheDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cacheDomain)), ".")
	tlsServerName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(tlsServerName)), ".")
	if cacheDomain == "" || tlsServerName == "" {
		return base
	}
	return &domainFrontingTransport{
		normal:        base,
		fronted:       frontedRoundTripper(base, tlsServerName),
		cacheDomain:   cacheDomain,
		tlsServerName: tlsServerName,
	}
}

func NewCacheAcceptTransport(base http.RoundTripper, cacheDomain string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	cacheDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cacheDomain)), ".")
	if cacheDomain == "" {
		return base
	}
	return &cacheAcceptTransport{
		base:        base,
		cacheDomain: cacheDomain,
	}
}

func NewUserAgentTransport(base http.RoundTripper, userAgent string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		return base
	}
	return &userAgentTransport{
		base:      base,
		userAgent: userAgent,
	}
}

func NewDomainFrontingClient(cacheDomain, tlsServerName string) *http.Client {
	return NewDomainFrontingClientWithUserAgent(cacheDomain, tlsServerName, DefaultUserAgent)
}

func NewDomainFrontingClientWithUserAgent(cacheDomain, tlsServerName, userAgent string) *http.Client {
	return &http.Client{
		Transport: NewUserAgentTransport(NewCacheAcceptTransport(NewDomainFrontingTransport(http.DefaultTransport, cacheDomain, tlsServerName), cacheDomain), userAgent),
	}
}

func (t *domainFrontingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && req.URL.Scheme == "https" && isCacheDomainHost(req.URL.Hostname(), t.cacheDomain) {
		return t.fronted.RoundTrip(req)
	}
	return t.normal.RoundTrip(req)
}

func (t *cacheAcceptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && isCacheDomainHost(req.URL.Hostname(), t.cacheDomain) {
		clone := req.Clone(req.Context())
		clone.Header.Set("Accept", DefaultCacheAccept)
		return t.base.RoundTrip(clone)
	}
	return t.base.RoundTrip(req)
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("User-Agent", t.userAgent)
	return t.base.RoundTrip(clone)
}

func isCacheDomainHost(host, cacheDomain string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	cacheDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cacheDomain)), ".")
	return host == cacheDomain || strings.HasSuffix(host, "."+cacheDomain)
}

func frontedRoundTripper(base http.RoundTripper, tlsServerName string) http.RoundTripper {
	if transport, ok := base.(*http.Transport); ok {
		clone := transport.Clone()
		clone.TLSClientConfig = cloneTLSConfig(clone.TLSClientConfig)
		clone.TLSClientConfig.ServerName = tlsServerName
		return clone
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{ServerName: tlsServerName}
	return transport
}

func cloneTLSConfig(config *tls.Config) *tls.Config {
	if config == nil {
		return &tls.Config{}
	}
	return config.Clone()
}
