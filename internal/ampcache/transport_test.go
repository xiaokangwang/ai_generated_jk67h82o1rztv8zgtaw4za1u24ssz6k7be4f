package ampcache

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDomainFrontingTransportSetsSNIForCacheDomainOnly(t *testing.T) {
	sniCh := make(chan string, 1)
	hostCh := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostCh <- r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sniCh <- hello.ServerName
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	targetAddr := server.Listener.Addr().String()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, targetAddr)
	}

	client := &http.Client{
		Transport: NewDomainFrontingTransport(base, DefaultCacheDomain, DefaultDomainFrontingServerName),
	}
	resp, err := client.Get("https://example.cdn.ampproject.org/r/s/origin.example/file.ttf")
	if err != nil {
		t.Fatalf("GET fronted cache URL: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %s, want 204 No Content", resp.Status)
	}

	select {
	case got := <-sniCh:
		if got != DefaultDomainFrontingServerName {
			t.Fatalf("SNI = %q, want %q", got, DefaultDomainFrontingServerName)
		}
	default:
		t.Fatalf("server did not observe TLS SNI")
	}
	select {
	case got := <-hostCh:
		if got != "example.cdn.ampproject.org" {
			t.Fatalf("Host = %q, want example.cdn.ampproject.org", got)
		}
	default:
		t.Fatalf("server did not receive request")
	}
}

func TestDomainFrontingTransportHostMatcher(t *testing.T) {
	if !isCacheDomainHost("a.cdn.ampproject.org", DefaultCacheDomain) {
		t.Fatalf("expected AMP Cache subdomain to match")
	}
	if !isCacheDomainHost("cdn.ampproject.org.", DefaultCacheDomain) {
		t.Fatalf("expected trailing-dot AMP Cache domain to match")
	}
	if isCacheDomainHost("notcdnampproject.org", DefaultCacheDomain) {
		t.Fatalf("unexpected match for unrelated domain")
	}
}

func TestDomainFrontingClientSetsDefaultUserAgent(t *testing.T) {
	seen := make(chan string, 1)
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen <- req.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	client := &http.Client{
		Transport: NewUserAgentTransport(NewDomainFrontingTransport(base, "", ""), DefaultUserAgent),
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/resource.ttf", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	resp.Body.Close()
	if got := <-seen; got != DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, DefaultUserAgent)
	}
}

func TestDomainFrontingClientCanDisableUserAgentOverride(t *testing.T) {
	seen := make(chan string, 1)
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen <- req.Header.Get("User-Agent")
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	client := &http.Client{
		Transport: NewUserAgentTransport(base, ""),
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/resource.ttf", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	req.Header.Set("User-Agent", "custom")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	resp.Body.Close()
	if got := <-seen; got != "custom" {
		t.Fatalf("User-Agent = %q, want custom", got)
	}
}

func TestCacheAcceptTransportSetsAcceptForCacheDomainOnly(t *testing.T) {
	seen := make(chan string, 2)
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen <- req.Header.Get("Accept")
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	client := &http.Client{
		Transport: NewCacheAcceptTransport(base, DefaultCacheDomain),
	}
	for _, target := range []string{
		"https://example.cdn.ampproject.org/r/s/origin.example/file.ttf",
		"https://origin.example/file.ttf",
	} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatalf("NewRequest returned error: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do returned error: %v", err)
		}
		resp.Body.Close()
	}
	if got := <-seen; got != DefaultCacheAccept {
		t.Fatalf("cache Accept = %q, want %q", got, DefaultCacheAccept)
	}
	if got := <-seen; got != "" {
		t.Fatalf("non-cache Accept = %q, want empty", got)
	}
}
