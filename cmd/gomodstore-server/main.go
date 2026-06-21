package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"gomodstore/internal/gomodstore"
)

func main() {
	var (
		addr          = flag.String("addr", ":8080", "listen address")
		publicURL     = flag.String("public-url", "", "externally reachable base URL, for example https://example.com")
		cachedOnlyURL = flag.String("cached-only-url", "https://proxy.golang.org/cached-only", "cached-only GOPROXY base URL")
		timeout       = flag.Duration("timeout", 2*time.Minute, "maximum time to keep a publish request active after ready")
		maxBytes      = flag.Int64("max-bytes", 0, "maximum publish body size in bytes; 0 means unlimited")
		chunkSize     = flag.Int("chunk-size", gomodstore.DefaultChunkSize, "payload chunk size")
	)
	flag.Parse()

	server := gomodstore.NewServer(gomodstore.ServerConfig{
		PublicURL:     *publicURL,
		CachedOnlyURL: *cachedOnlyURL,
		Timeout:       *timeout,
		MaxBodyBytes:  *maxBytes,
		ChunkSize:     *chunkSize,
	})

	log.Printf("gomodstore-server listening on %s", *addr)
	if *publicURL != "" {
		log.Printf("public URL: %s", *publicURL)
	}
	if err := http.ListenAndServe(*addr, server); err != nil {
		fmt.Fprintf(os.Stderr, "gomodstore-server: %v\n", err)
		os.Exit(1)
	}
}
