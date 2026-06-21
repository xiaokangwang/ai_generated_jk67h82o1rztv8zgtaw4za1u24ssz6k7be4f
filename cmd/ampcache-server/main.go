package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"gomodstore/internal/ampcache"
)

func main() {
	var (
		addr               = flag.String("addr", ":8081", "listen address")
		publicURL          = flag.String("public-url", "", "externally reachable base URL")
		cacheDomain        = flag.String("cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain")
		cacheTLSServerName = flag.String("cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache backfill requests; empty disables domain fronting")
		userAgent          = flag.String("user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache backfill requests; empty uses Go default")
		timeout            = flag.Duration("timeout", 2*time.Minute, "maximum time to keep a publish request active after ready")
		maxResourceBytes   = flag.Int64("max-resource-bytes", 0, "maximum encoded resource size in bytes; required")
		backfill           = flag.Bool("backfill", true, "fetch and serve missing resources from AMP Cache")
	)
	flag.Parse()
	if *publicURL == "" {
		fmt.Fprintln(os.Stderr, "ampcache-server: -public-url is required")
		os.Exit(2)
	}
	if *maxResourceBytes <= 0 {
		fmt.Fprintln(os.Stderr, "ampcache-server: -max-resource-bytes must be greater than zero")
		os.Exit(2)
	}

	server, err := ampcache.NewServer(ampcache.ServerConfig{
		PublicURL:        *publicURL,
		CacheDomain:      *cacheDomain,
		Timeout:          *timeout,
		MaxResourceBytes: *maxResourceBytes,
		Backfill:         *backfill,
		HTTPClient:       ampcache.NewDomainFrontingClientWithUserAgent(*cacheDomain, *cacheTLSServerName, *userAgent),
		Logger:           log.Default(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ampcache-server: %v\n", err)
		os.Exit(2)
	}

	log.Printf("ampcache-server listening on %s", *addr)
	log.Printf("public URL: %s", *publicURL)
	log.Printf("max resource bytes: %d", *maxResourceBytes)
	log.Printf("backfill: %t", *backfill)
	log.Printf("cache TLS server name: %s", *cacheTLSServerName)
	log.Printf("cache user agent: %s", *userAgent)
	if err := http.ListenAndServe(*addr, server); err != nil {
		fmt.Fprintf(os.Stderr, "ampcache-server: %v\n", err)
		os.Exit(1)
	}
}
