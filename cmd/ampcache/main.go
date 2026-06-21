package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"gomodstore/internal/ampcache"
	"gomodstore/internal/ampfile"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "publish":
		err = runPublish(os.Args[2:])
	case "publish-file":
		err = runPublishFile(os.Args[2:])
	case "repair-file":
		err = runRepairFile(os.Args[2:])
	case "get":
		err = runGet(os.Args[2:])
	case "get-file":
		err = runGetFile(os.Args[2:])
	case "verify-file":
		err = runVerifyFile(os.Args[2:])
	case "urls":
		err = runURLs(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ampcache: %v\n", err)
		os.Exit(1)
	}
}

func runPublishFile(args []string) error {
	fs := flag.NewFlagSet("publish-file", flag.ExitOnError)
	serverURL := fs.String("server", "http://localhost:8081", "ampcache server URL")
	resourceBase := fs.String("resource-base", "", "absolute public origin URL prefix for generated masked .ttf resources; defaults to -server")
	cacheDomain := fs.String("cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain")
	cacheTLSServerName := fs.String("cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache requests; empty disables domain fronting")
	userAgent := fs.String("user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache requests; empty uses Go default")
	chunkSize := newByteSize(ampfile.DefaultChunkSize)
	symbolSize := newByteSize(ampfile.DefaultSymbolSize)
	fs.Var(chunkSize, "chunk-size", "logical file chunk size")
	fs.Var(symbolSize, "symbol-size", "FEC symbol payload size before encryption/font encoding")
	minRecovery := fs.Uint64("min-recovery-symbols", ampfile.DefaultMinRecoverySymbols, "minimum recovery symbols per chunk")
	fecRatio := fs.Float64("fec-total-ratio", float64(ampfile.DefaultFECTotalMillis)/1000, "minimum total symbols as a multiple of source symbols")
	workers := fs.Int("publish-workers", ampfile.DefaultPublishWorkers, "concurrent symbol publish workers")
	verifyWorkers := fs.Int("verify-workers", ampfile.DefaultSymbolWorkers, "concurrent symbol download workers during post-upload verification")
	uploadTimeout := fs.Duration("upload-timeout", 2*time.Minute, "timeout for one resource upload")
	poll := fs.Duration("poll", 500*time.Millisecond, "AMP Cache poll interval while triggering origin fetch")
	verifyTimeout := fs.Duration("verify-timeout", 10*time.Minute, "maximum time to wait for publish verification")
	verifyPoll := fs.Duration("verify-poll", 2*time.Second, "retry interval for publish verification")
	debugSymbolURLs := fs.Bool("debug-symbol-urls", false, "include sample symbol cache URLs in verification error logs")
	noVerify := fs.Bool("no-verify", false, "skip post-upload recoverability verification")
	jsonOutput := fs.Bool("json", false, "print publish result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ampcache publish-file [flags] <input-file>")
	}
	result, err := ampfile.PublishFile(context.Background(), ampfile.PublishOptions{
		ServerURL:          *serverURL,
		ResourceBase:       defaultString(*resourceBase, *serverURL),
		CacheDomain:        *cacheDomain,
		InputPath:          fs.Arg(0),
		ChunkSize:          uint64(*chunkSize),
		SymbolSize:         uint64(*symbolSize),
		MinRecoverySymbols: *minRecovery,
		FECTotalMillis:     ratioToMillis(*fecRatio),
		PublishWorkers:     *workers,
		VerifyWorkers:      *verifyWorkers,
		HTTPClient:         ampcache.NewDomainFrontingClientWithUserAgent(*cacheDomain, *cacheTLSServerName, *userAgent),
		UploadTimeout:      *uploadTimeout,
		PollInterval:       *poll,
		VerifyTimeout:      *verifyTimeout,
		VerifyPollInterval: *verifyPoll,
		DebugSymbolURLs:    *debugSymbolURLs,
		NoVerify:           *noVerify,
		Logger:             log.New(os.Stderr, "ampcache: ", log.LstdFlags),
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Fprintln(os.Stdout, result.ManifestURL)
	if result.Verified && result.Verify.Warnings {
		fmt.Fprintf(os.Stderr, "ampcache: verify passed with symbol download warnings\n")
	}
	return nil
}

func runRepairFile(args []string) error {
	fs := flag.NewFlagSet("repair-file", flag.ExitOnError)
	serverURL := fs.String("server", "http://localhost:8081", "ampcache server URL")
	resourceBase := fs.String("resource-base", "", "absolute public origin URL prefix for generated masked .ttf resources; defaults to manifest resource base")
	cacheDomain := fs.String("cache-domain", "", "AMP Cache domain; defaults to manifest cache domain")
	cacheTLSServerName := fs.String("cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache requests; empty disables domain fronting")
	userAgent := fs.String("user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache requests; empty uses Go default")
	sourcePath := fs.String("source", "", "optional original source file used when cached symbols cannot recover a chunk")
	minRecovery := fs.Uint64("min-recovery-symbols", ampfile.DefaultMinRecoverySymbols, "minimum recovery symbols per repaired chunk")
	fecRatio := fs.Float64("fec-total-ratio", float64(ampfile.DefaultFECTotalMillis)/1000, "minimum total symbols as a multiple of source symbols")
	workers := fs.Int("publish-workers", ampfile.DefaultPublishWorkers, "concurrent symbol publish workers")
	uploadTimeout := fs.Duration("upload-timeout", 2*time.Minute, "timeout for one resource upload")
	poll := fs.Duration("poll", 500*time.Millisecond, "AMP Cache poll interval while triggering origin fetch")
	jsonOutput := fs.Bool("json", false, "print repair result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ampcache repair-file [flags] <manifest-cache-url#k=...>")
	}
	result, err := ampfile.RepairFile(context.Background(), ampfile.RepairOptions{
		ServerURL:          *serverURL,
		ResourceBase:       *resourceBase,
		CacheDomain:        *cacheDomain,
		ManifestURL:        fs.Arg(0),
		SourcePath:         *sourcePath,
		MinRecoverySymbols: *minRecovery,
		FECTotalMillis:     ratioToMillis(*fecRatio),
		PublishWorkers:     *workers,
		HTTPClient:         ampcache.NewDomainFrontingClientWithUserAgent(defaultString(*cacheDomain, ampcache.DefaultCacheDomain), *cacheTLSServerName, *userAgent),
		UploadTimeout:      *uploadTimeout,
		PollInterval:       *poll,
		Logger:             log.New(os.Stderr, "ampcache: ", log.LstdFlags),
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Fprintln(os.Stdout, result.ManifestURL)
	return nil
}

func runVerifyFile(args []string) error {
	fs := flag.NewFlagSet("verify-file", flag.ExitOnError)
	cacheDomain := fs.String("cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain used for TLS domain fronting")
	cacheTLSServerName := fs.String("cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache requests; empty disables domain fronting")
	userAgent := fs.String("user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache requests; empty uses Go default")
	workers := fs.Int("symbol-workers", ampfile.DefaultSymbolWorkers, "concurrent symbol download workers")
	symbolRetries := fs.Int("symbol-retries", ampfile.DefaultSymbolRetries, "retry rounds for failed symbol URLs after each full pass")
	timeout := fs.Duration("timeout", 10*time.Minute, "maximum verification time")
	retry := fs.Duration("retry", 2*time.Second, "retry interval for manifest and failed-symbol retries")
	debugSymbolURLs := fs.Bool("debug-symbol-urls", false, "include sample symbol cache URLs in verification error logs")
	jsonOutput := fs.Bool("json", false, "print verify result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ampcache verify-file [flags] <manifest-cache-url#k=...>")
	}
	result, err := ampfile.VerifyFile(context.Background(), ampfile.VerifyOptions{
		ManifestURL:     fs.Arg(0),
		HTTPClient:      ampcache.NewDomainFrontingClientWithUserAgent(*cacheDomain, *cacheTLSServerName, *userAgent),
		SymbolWorkers:   *workers,
		SymbolRetries:   *symbolRetries,
		Timeout:         *timeout,
		RetryInterval:   *retry,
		Logger:          log.New(os.Stderr, "ampcache: ", log.LstdFlags),
		DebugSymbolURLs: *debugSymbolURLs,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Fprintf(os.Stdout, "verified %d/%d chunks\n", result.RecoveredChunks, result.TotalChunks)
	for _, chunk := range result.Chunks {
		if chunk.Failed > 0 {
			fmt.Fprintf(os.Stderr, "ampcache: warning chunk %d recovered with %d successful and %d failed symbols\n", chunk.Index, chunk.Succeeded, chunk.Failed)
			for _, sample := range chunk.SampleErrors {
				fmt.Fprintf(os.Stderr, "ampcache: warning chunk %d sample error: %s\n", chunk.Index, sample)
			}
		}
	}
	return nil
}

func runGetFile(args []string) error {
	fs := flag.NewFlagSet("get-file", flag.ExitOnError)
	outPath := fs.String("out", "", "output file path")
	cacheDomain := fs.String("cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain used for TLS domain fronting")
	cacheTLSServerName := fs.String("cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache requests; empty disables domain fronting")
	userAgent := fs.String("user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache requests; empty uses Go default")
	workers := fs.Int("symbol-workers", ampfile.DefaultSymbolWorkers, "concurrent symbol download workers")
	symbolRetries := fs.Int("symbol-retries", ampfile.DefaultSymbolRetries, "retry rounds for failed symbol URLs; -1 retries until recovered")
	timeout := fs.Duration("timeout", 0, "overall download timeout; 0 means no explicit timeout")
	retry := fs.Duration("retry", 0, "retry interval for failed-symbol retry rounds; 0 retries immediately")
	jsonOutput := fs.Bool("json", false, "print get result as JSON")
	verbose := fs.Bool("v", false, "print full cache error bodies and sample symbol URLs")
	fs.BoolVar(verbose, "verbose", false, "print full cache error bodies and sample symbol URLs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ampcache get-file -out <path> [flags] <manifest-cache-url#k=...>")
	}
	result, err := ampfile.GetFile(context.Background(), ampfile.GetOptions{
		ManifestURL:   fs.Arg(0),
		OutputPath:    *outPath,
		HTTPClient:    ampcache.NewDomainFrontingClientWithUserAgent(*cacheDomain, *cacheTLSServerName, *userAgent),
		SymbolWorkers: *workers,
		SymbolRetries: *symbolRetries,
		Verbose:       *verbose,
		Timeout:       *timeout,
		RetryInterval: *retry,
		Logger:        log.New(os.Stderr, "ampcache: ", log.LstdFlags),
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Fprintf(os.Stdout, "%s\n", result.OutputPath)
	printGetFileOverhead(os.Stdout, result)
	return nil
}

func printGetFileOverhead(w io.Writer, result *ampfile.GetResult) {
	d := result.Download
	fmt.Fprintf(w, "file bytes: %d\n", result.FileSize)
	fmt.Fprintf(w, "cache body bytes: %d (%s over file)\n", d.CacheBodyBytes, formatRatio(d.CacheBodyOverFileRatio))
	fmt.Fprintf(w, "  manifest: %d bytes in %d responses\n", d.ManifestCacheBodyBytes, d.ManifestResponses)
	fmt.Fprintf(w, "  symbols: %d bytes in %d responses\n", d.SymbolCacheBodyBytes, d.SymbolResponses)
	fmt.Fprintf(w, "decoded payload bytes: %d\n", d.DecodedPayloadBytes)
	fmt.Fprintf(w, "font encoding overhead: %d bytes (%s overhead, %s encoded/decoded)\n", d.FontEncodingOverheadBytes, formatPercent(d.FontEncodingOverheadRatio), formatRatio(d.CacheBodyOverDecodedPayloadRatio))
	fmt.Fprintf(w, "  manifest font overhead: %d bytes (%s overhead)\n", d.ManifestFontEncodingOverheadBytes, formatPercent(d.ManifestFontEncodingOverheadRatio))
	fmt.Fprintf(w, "  symbol font overhead: %d bytes (%s overhead, %s encoded/decoded)\n", d.SymbolFontEncodingOverheadBytes, formatPercent(d.SymbolFontEncodingOverheadRatio), formatRatio(d.SymbolCacheBodyOverSymbolDecodedRatio))
	fmt.Fprintf(w, "fec symbols: source=%d fetched_ok=%d attempted=%d failed=%d available=%d\n", d.SourceSymbols, d.SucceededSymbols, d.AttemptedSymbols, d.FailedSymbols, d.TotalAvailableSymbols)
	fmt.Fprintf(w, "fec recovery overhead: %s fetched/source, %s fetched/available\n", formatRatio(d.SymbolSuccessOverSourceRatio), formatRatio(d.SymbolSuccessOverAvailableRatio))
}

func formatRatio(value float64) string {
	if value == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return "0.000x"
	}
	return fmt.Sprintf("%.3fx", value)
}

func formatPercent(value float64) string {
	if value == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return "0.00%"
	}
	return fmt.Sprintf("%.2f%%", value*100)
}

func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	serverURL := fs.String("server", "http://localhost:8081", "ampcache server URL")
	resourceURL := fs.String("resource-url", "", "absolute public origin resource URL")
	encoding := fs.String("encoding", "", "resource encoding: html, font, or image")
	cacheDomain := fs.String("cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain used for TLS domain fronting")
	cacheTLSServerName := fs.String("cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache requests; empty disables domain fronting")
	userAgent := fs.String("user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache requests; empty uses Go default")
	timeout := fs.Duration("timeout", 2*time.Minute, "overall publish timeout")
	poll := fs.Duration("poll", 2*time.Second, "AMP Cache polling interval")
	waitMode := fs.String("wait", "cached", "publish wait mode: cached or origin-fetch")
	jsonOutput := fs.Bool("json", false, "print publish result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	enc, err := ampcache.ParseEncoding(*encoding)
	if err != nil {
		return err
	}
	if *resourceURL == "" {
		return fmt.Errorf("-resource-url is required")
	}
	if *waitMode != "cached" && *waitMode != "origin-fetch" {
		return fmt.Errorf("-wait must be cached or origin-fetch")
	}
	input, closeInput, err := openInput(fs.Args())
	if err != nil {
		return err
	}
	defer closeInput()

	result, err := ampcache.Publish(context.Background(), ampcache.PublishOptions{
		ServerURL:    *serverURL,
		ResourceURL:  *resourceURL,
		Encoding:     enc,
		Body:         input,
		HTTPClient:   ampcache.NewDomainFrontingClientWithUserAgent(*cacheDomain, *cacheTLSServerName, *userAgent),
		Timeout:      *timeout,
		PollInterval: *poll,
		Logger:       log.New(os.Stderr, "ampcache: ", log.LstdFlags),
		WaitMode:     *waitMode,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Fprintln(os.Stdout, result.CacheURL)
	return nil
}

func runGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	encoding := fs.String("encoding", "", "resource encoding: html, font, or image")
	cacheDomain := fs.String("cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain used for TLS domain fronting")
	cacheTLSServerName := fs.String("cache-tls-server-name", ampcache.DefaultDomainFrontingServerName, "TLS server name for AMP Cache requests; empty disables domain fronting")
	userAgent := fs.String("user-agent", ampcache.DefaultUserAgent, "HTTP User-Agent for AMP Cache requests; empty uses Go default")
	outPath := fs.String("out", "-", "output file, or - for stdout")
	timeout := fs.Duration("timeout", 2*time.Minute, "download timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ampcache get -encoding <mode> [flags] <cache-url>")
	}
	enc, err := ampcache.ParseEncoding(*encoding)
	if err != nil {
		return err
	}
	output, closeOutput, err := openOutput(*outPath)
	if err != nil {
		return err
	}
	defer closeOutput()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	_, err = ampcache.Get(ctx, ampcache.GetOptions{
		CacheURL:   fs.Arg(0),
		Encoding:   enc,
		Output:     output,
		HTTPClient: ampcache.NewDomainFrontingClientWithUserAgent(*cacheDomain, *cacheTLSServerName, *userAgent),
	})
	return err
}

func runURLs(args []string) error {
	fs := flag.NewFlagSet("urls", flag.ExitOnError)
	encoding := fs.String("encoding", "", "resource encoding: html, font, or image")
	cacheDomain := fs.String("cache-domain", ampcache.DefaultCacheDomain, "AMP Cache domain")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ampcache urls -encoding <mode> [flags] <resource-url>")
	}
	enc, err := ampcache.ParseEncoding(*encoding)
	if err != nil {
		return err
	}
	origin, cache, err := ampcache.URLs(*cacheDomain, fs.Arg(0), enc)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, origin)
	fmt.Fprintln(os.Stdout, cache)
	return nil
}

func openInput(args []string) (io.Reader, func(), error) {
	if len(args) > 1 {
		return nil, nil, fmt.Errorf("usage: ampcache publish [flags] [file]")
	}
	if len(args) == 0 || args[0] == "-" {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, os.Stdin); err != nil {
			return nil, nil, err
		}
		return &buf, func() {}, nil
	}
	file, err := os.Open(args[0])
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func openOutput(path string) (io.Writer, func(), error) {
	if path == "-" {
		return os.Stdout, func() {}, nil
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  ampcache publish -encoding <mode> -resource-url <url> [flags] [file]
  ampcache publish-file -server <url> -resource-base <url> [flags] <input-file>
  ampcache repair-file -server <url> [flags] <manifest-cache-url#k=...>
  ampcache get -encoding <mode> [flags] <cache-url>
  ampcache get-file -out <path> [flags] <manifest-cache-url#k=...>
  ampcache verify-file [flags] <manifest-cache-url#k=...>
  ampcache urls -encoding <mode> [flags] <resource-url>`)
}

type byteSize uint64

func newByteSize(v uint64) *byteSize {
	b := byteSize(v)
	return &b
}

func (b *byteSize) String() string {
	if b == nil {
		return "0"
	}
	return fmt.Sprintf("%d", uint64(*b))
}

func (b *byteSize) Set(value string) error {
	parsed, err := parseByteSize(value)
	if err != nil {
		return err
	}
	*b = byteSize(parsed)
	return nil
}

func parseByteSize(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty size")
	}
	lower := strings.ToLower(value)
	multiplier := uint64(1)
	for _, suffix := range []struct {
		s string
		m uint64
	}{
		{"kib", 1024},
		{"kb", 1024},
		{"k", 1024},
		{"mib", 1024 * 1024},
		{"mb", 1024 * 1024},
		{"m", 1024 * 1024},
		{"gib", 1024 * 1024 * 1024},
		{"gb", 1024 * 1024 * 1024},
		{"g", 1024 * 1024 * 1024},
	} {
		if strings.HasSuffix(lower, suffix.s) {
			multiplier = suffix.m
			value = strings.TrimSpace(value[:len(value)-len(suffix.s)])
			break
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, err
	}
	if n > math.MaxUint64/multiplier {
		return 0, fmt.Errorf("size overflows uint64")
	}
	return n * multiplier, nil
}

func ratioToMillis(value float64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(math.Ceil(value * 1000))
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
