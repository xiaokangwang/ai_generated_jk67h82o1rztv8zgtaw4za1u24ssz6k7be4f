package ampfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gomodstore/internal/ampcache"
)

func TestManifestCBORRoundTrip(t *testing.T) {
	root := RootManifest{
		Version:       manifestVersion,
		RunID:         "run-test",
		FileName:      "file.bin",
		FileSize:      123,
		FileSHA256:    bytes.Repeat([]byte{1}, 32),
		ResourceBase:  "https://example.com/base",
		CacheDomain:   ampcache.DefaultCacheDomain,
		ChunkSize:     64,
		SymbolSize:    16,
		ChunkCount:    1,
		ChunksPerPage: 1024,
		Pages:         []ManifestPageRef{{Index: 0, CacheURL: "https://cache/page.ttf"}},
	}
	decoded, err := decodeRootManifest(encodeRootManifest(root))
	if err != nil {
		t.Fatalf("decodeRootManifest returned error: %v", err)
	}
	r := newCBORReader(encodeRootManifest(root))
	if n, err := r.array(); err != nil || n != 12 {
		t.Fatalf("v2 root field count = %d, err=%v; want 12 fields", n, err)
	}
	if decoded.RunID != root.RunID || decoded.Pages[0].CacheURL != root.Pages[0].CacheURL {
		t.Fatalf("decoded root mismatch: %+v", decoded)
	}
	page := PageManifest{
		Version:    manifestVersion,
		RunID:      root.RunID,
		PageIndex:  0,
		FirstChunk: 0,
		Chunks: []ChunkManifest{{
			Index:  0,
			Offset: 0,
			Length: 123,
			SHA256: bytes.Repeat([]byte{2}, 32),
			SymbolRanges: []SymbolRange{
				{Start: 0, End: 9},
				{Start: 20, End: 24},
			},
		}},
	}
	decodedPage, err := decodePageManifest(encodePageManifest(page))
	if err != nil {
		t.Fatalf("decodePageManifest returned error: %v", err)
	}
	if decodedPage.Chunks[0].Length != 123 {
		t.Fatalf("decoded page chunk length = %d, want 123", decodedPage.Chunks[0].Length)
	}
	if len(decodedPage.Chunks[0].SymbolRanges) != 2 || decodedPage.Chunks[0].SymbolRanges[1].End != 24 {
		t.Fatalf("decoded symbol ranges mismatch: %+v", decodedPage.Chunks[0].SymbolRanges)
	}
}

func TestLegacyManifestDecoding(t *testing.T) {
	root := RootManifest{
		Version:            legacyManifestVersion,
		RunID:              "run-legacy",
		FileName:           "file.bin",
		FileSize:           2048,
		FileSHA256:         bytes.Repeat([]byte{1}, 32),
		ResourceBase:       "https://example.com/base",
		CacheDomain:        ampcache.DefaultCacheDomain,
		ChunkSize:          2048,
		SymbolSize:         1024,
		MinRecoverySymbols: 3,
		FECTotalMillis:     1000,
		ChunkCount:         1,
		ChunksPerPage:      1024,
		Pages:              []ManifestPageRef{{Index: 0, CacheURL: "https://cache/page.ttf"}},
	}
	decoded, err := decodeRootManifest(encodeRootManifest(root))
	if err != nil {
		t.Fatalf("decodeRootManifest returned error: %v", err)
	}
	if decoded.Version != legacyManifestVersion || decoded.MinRecoverySymbols != 3 {
		t.Fatalf("decoded legacy root mismatch: %+v", decoded)
	}
	page := PageManifest{
		Version:    legacyManifestVersion,
		RunID:      root.RunID,
		PageIndex:  0,
		FirstChunk: 0,
		Chunks: []ChunkManifest{{
			Index:  0,
			Offset: 0,
			Length: 2048,
			SHA256: bytes.Repeat([]byte{2}, 32),
		}},
	}
	decodedPage, err := decodePageManifest(encodePageManifest(page))
	if err != nil {
		t.Fatalf("decodePageManifest returned error: %v", err)
	}
	if len(decodedPage.Chunks[0].SymbolRanges) != 0 {
		t.Fatalf("legacy page unexpectedly decoded symbol ranges: %+v", decodedPage.Chunks[0].SymbolRanges)
	}
}

func TestCryptoAndPathPrivacy(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	cKey := contentKey(key)
	plaintext := []byte("secret payload")
	aad := aadSymbol("run", 1, 2)
	encrypted, err := encryptPayload(cKey, aad, plaintext)
	if err != nil {
		t.Fatalf("encryptPayload returned error: %v", err)
	}
	decrypted, err := decryptPayload(cKey, aad, encrypted)
	if err != nil {
		t.Fatalf("decryptPayload returned error: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted payload = %q, want %q", decrypted, plaintext)
	}
	if _, err := decryptPayload(cKey, aadSymbol("run", 1, 3), encrypted); err == nil {
		t.Fatalf("decryptPayload succeeded with wrong AAD")
	}
	token := pathToken(pathKey(key), symbolLogicalID("run", 1, 2))
	if strings.Contains(token, "run") || strings.Contains(token, "1") || strings.Contains(token, "/") {
		t.Fatalf("path token leaks logical metadata: %q", token)
	}
	randomToken, err := randomPathToken()
	if err != nil {
		t.Fatalf("randomPathToken returned error: %v", err)
	}
	if len(randomToken) != len(token) {
		t.Fatalf("random token length = %d, want %d", len(randomToken), len(token))
	}
}

func TestFECRoundTripWithMissingSymbols(t *testing.T) {
	chunk := bytes.Repeat([]byte("abcdef"), 2000)
	const symbolSize = 1024
	source := sourceSymbolCount(uint64(len(chunk)), symbolSize)
	total := totalSymbolCount(source, 8, 1200)
	decoder, err := newChunkDecoder(uint64(len(chunk)), symbolSize)
	if err != nil {
		t.Fatalf("newChunkDecoder returned error: %v", err)
	}
	for id := uint64(3); id < total; id++ {
		symbol, err := encodeSymbol(chunk, symbolSize, id)
		if err != nil {
			t.Fatalf("encodeSymbol returned error: %v", err)
		}
		done, err := decoder.put(id, symbol)
		if err != nil {
			t.Fatalf("decoder.put returned error: %v", err)
		}
		if done {
			break
		}
	}
	got, err := io.ReadAll(decoder.reader())
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if !bytes.Equal(got, chunk) {
		t.Fatalf("recovered chunk mismatch")
	}
}

func TestPublishVerifyAndGetFileWithMaskedEncryptedResources(t *testing.T) {
	store, origin := newAMPFileTestServer(t)
	defer origin.Close()
	cache := newFakeAMPCache(t, origin.URL)
	defer cache.close()
	client := &http.Client{Transport: rewriteCacheTransport(t, cache.URL(), http.DefaultTransport)}

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	payload := bytes.Repeat([]byte("payload-"), 5000)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	result, err := PublishFile(context.Background(), PublishOptions{
		ServerURL:          origin.URL,
		ResourceBase:       origin.URL + "/masked",
		InputPath:          inputPath,
		ChunkSize:          10 * 1024,
		SymbolSize:         1024,
		MinRecoverySymbols: 6,
		FECTotalMillis:     1200,
		PublishWorkers:     4,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
		VerifyTimeout:      2 * time.Second,
		VerifyPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("PublishFile returned error: %v", err)
	}
	if !result.Verified {
		t.Fatalf("publish result was not verified")
	}
	if !strings.Contains(result.ManifestURL, "#k=") {
		t.Fatalf("manifest URL did not contain key fragment: %q", result.ManifestURL)
	}
	if !strings.Contains(result.ManifestURL, "&h=") {
		t.Fatalf("manifest URL did not contain root manifest hash fragment: %q", result.ManifestURL)
	}
	if strings.Contains(result.ManifestURL, result.RunID) || strings.Contains(result.ManifestURL, "/c/") || strings.Contains(result.ManifestURL, "/s/") {
		t.Fatalf("manifest URL leaks logical metadata: %q", result.ManifestURL)
	}
	if store.ActiveCount() != 0 {
		t.Fatalf("active sessions = %d, want 0", store.ActiveCount())
	}
	outputPath := filepath.Join(t.TempDir(), "output.bin")
	getResult, err := GetFile(context.Background(), GetOptions{
		ManifestURL:   result.ManifestURL,
		OutputPath:    outputPath,
		HTTPClient:    client,
		SymbolWorkers: 4,
		Timeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if getResult.FileSize != uint64(len(payload)) {
		t.Fatalf("get file size = %d, want %d", getResult.FileSize, len(payload))
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("output payload mismatch")
	}
	if _, err := loadManifest(context.Background(), client, manifestURLWithoutHash(t, result.ManifestURL), false, nil); err != nil {
		t.Fatalf("legacy manifest URL without hash did not load: %v", err)
	}
	if _, err := loadManifest(context.Background(), client, manifestURLWithHash(t, result.ManifestURL, strings.Repeat("B", 43)), false, nil); err == nil || !strings.Contains(err.Error(), "root manifest hash mismatch") {
		t.Fatalf("loadManifest with tampered hash error = %v, want root manifest hash mismatch", err)
	}
}

func TestVerifyPassesWithMissingRecoverySymbolsWarning(t *testing.T) {
	_, origin := newAMPFileTestServer(t)
	defer origin.Close()
	cache := newFakeAMPCache(t, origin.URL)
	defer cache.close()
	client := &http.Client{Transport: rewriteCacheTransport(t, cache.URL(), http.DefaultTransport)}

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	payload := bytes.Repeat([]byte("abcdefgh"), 3000)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	result, err := PublishFile(context.Background(), PublishOptions{
		ServerURL:          origin.URL,
		ResourceBase:       origin.URL + "/masked",
		InputPath:          inputPath,
		ChunkSize:          12 * 1024,
		SymbolSize:         1024,
		MinRecoverySymbols: 8,
		FECTotalMillis:     1300,
		PublishWorkers:     4,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
		NoVerify:           true,
	})
	if err != nil {
		t.Fatalf("PublishFile returned error: %v", err)
	}
	loaded, err := loadManifest(context.Background(), client, result.ManifestURL, false, nil)
	if err != nil {
		t.Fatalf("loadManifest returned error: %v", err)
	}
	ids, err := expandSymbolRanges(loaded.Chunks[0].SymbolRanges)
	if err != nil {
		t.Fatalf("expandSymbolRanges returned error: %v", err)
	}
	cache.deleteSymbol(t, loaded, 0, ids[len(ids)-1])
	verify, err := VerifyFile(context.Background(), VerifyOptions{
		ManifestURL:   result.ManifestURL,
		HTTPClient:    client,
		SymbolWorkers: 4,
	})
	if err != nil {
		t.Fatalf("VerifyFile returned error: %v", err)
	}
	if !verify.Warnings {
		t.Fatalf("VerifyFile did not report warnings")
	}
	if verify.Chunks[0].Failed == 0 {
		t.Fatalf("chunk failed count = 0, want warning failure")
	}
}

func TestGetFileRetriesFailedSymbolsAfterFullPass(t *testing.T) {
	_, origin := newAMPFileTestServer(t)
	defer origin.Close()
	cache := newFakeAMPCache(t, origin.URL)
	defer cache.close()
	baseTransport := rewriteCacheTransport(t, cache.URL(), http.DefaultTransport)
	client := &http.Client{Transport: baseTransport}

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	payload := bytes.Repeat([]byte("0123456789abcdef"), 256)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	result, err := PublishFile(context.Background(), PublishOptions{
		ServerURL:          origin.URL,
		ResourceBase:       origin.URL + "/masked",
		InputPath:          inputPath,
		ChunkSize:          8 * 1024,
		SymbolSize:         1024,
		MinRecoverySymbols: 1,
		FECTotalMillis:     1000,
		PublishWorkers:     2,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
		NoVerify:           true,
	})
	if err != nil {
		t.Fatalf("PublishFile returned error: %v", err)
	}
	loaded, err := loadManifest(context.Background(), client, result.ManifestURL, false, nil)
	if err != nil {
		t.Fatalf("loadManifest returned error: %v", err)
	}
	ids, err := expandSymbolRanges(loaded.Chunks[0].SymbolRanges)
	if err != nil {
		t.Fatalf("expandSymbolRanges returned error: %v", err)
	}
	total := uint64(len(ids))
	labels := make(map[string]uint64)
	failOnce := make(map[string]int)
	for _, id := range ids {
		cacheURL := symbolCacheURL(t, loaded, 0, id)
		labels[cacheURL] = id
		if id < 2 {
			failOnce[cacheURL] = 1
		}
	}
	flakyTransport := &transientSymbolTransport{
		underlying: baseTransport,
		labels:     labels,
		failures:   failOnce,
	}
	outputPath := filepath.Join(t.TempDir(), "output.bin")
	getResult, err := GetFile(context.Background(), GetOptions{
		ManifestURL:   result.ManifestURL,
		OutputPath:    outputPath,
		HTTPClient:    &http.Client{Transport: flakyTransport},
		SymbolWorkers: 1,
		SymbolRetries: 1,
		Timeout:       2 * time.Second,
		RetryInterval: 0,
	})
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if getResult.Download.AttemptedSymbols <= total {
		t.Fatalf("attempted symbols = %d, want more than first pass total %d", getResult.Download.AttemptedSymbols, total)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("output payload mismatch")
	}
	sequence := flakyTransport.sequence()
	if len(sequence) <= int(total) {
		t.Fatalf("symbol request sequence = %v, want at least one retry after first pass", sequence)
	}
	for id := uint64(0); id < total; id++ {
		if sequence[id] != id {
			t.Fatalf("first symbol pass sequence = %v, want sequential first pass through %d symbols", sequence, total)
		}
	}
	if sequence[total] != 0 && sequence[total] != 1 {
		t.Fatalf("first retry symbol = %d, want a failed symbol from the first pass", sequence[total])
	}
}

func TestGetFileUnlimitedRetriesTransportErrorsUntilRecovered(t *testing.T) {
	_, origin := newAMPFileTestServer(t)
	defer origin.Close()
	cache := newFakeAMPCache(t, origin.URL)
	defer cache.close()
	baseTransport := rewriteCacheTransport(t, cache.URL(), http.DefaultTransport)
	client := &http.Client{Transport: baseTransport}

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	payload := bytes.Repeat([]byte("transport-retry"), 256)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	result, err := PublishFile(context.Background(), PublishOptions{
		ServerURL:          origin.URL,
		ResourceBase:       origin.URL + "/masked",
		InputPath:          inputPath,
		ChunkSize:          8 * 1024,
		SymbolSize:         1024,
		MinRecoverySymbols: 1,
		FECTotalMillis:     1000,
		PublishWorkers:     2,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
		NoVerify:           true,
	})
	if err != nil {
		t.Fatalf("PublishFile returned error: %v", err)
	}
	loaded, err := loadManifest(context.Background(), client, result.ManifestURL, false, nil)
	if err != nil {
		t.Fatalf("loadManifest returned error: %v", err)
	}
	ids, err := expandSymbolRanges(loaded.Chunks[0].SymbolRanges)
	if err != nil {
		t.Fatalf("expandSymbolRanges returned error: %v", err)
	}
	total := uint64(len(ids))
	labels := make(map[string]uint64)
	failures := make(map[string]int)
	for _, id := range ids {
		cacheURL := symbolCacheURL(t, loaded, 0, id)
		labels[cacheURL] = id
		failures[cacheURL] = 2
	}
	flakyTransport := &transientSymbolTransport{
		underlying: baseTransport,
		labels:     labels,
		failures:   failures,
		failureErr: errors.New("synthetic transport failure"),
	}
	outputPath := filepath.Join(t.TempDir(), "output.bin")
	getResult, err := GetFile(context.Background(), GetOptions{
		ManifestURL:   result.ManifestURL,
		OutputPath:    outputPath,
		HTTPClient:    &http.Client{Transport: flakyTransport},
		SymbolWorkers: 1,
		SymbolRetries: -1,
		RetryInterval: time.Nanosecond,
		Timeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if getResult.Download.AttemptedSymbols <= total*2 {
		t.Fatalf("attempted symbols = %d, want more than two failed passes through %d symbols", getResult.Download.AttemptedSymbols, total)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("output payload mismatch")
	}
}

func TestGetFileDoesNotRetryRecoveredChunks(t *testing.T) {
	_, origin := newAMPFileTestServer(t)
	defer origin.Close()
	cache := newFakeAMPCache(t, origin.URL)
	defer cache.close()
	baseTransport := rewriteCacheTransport(t, cache.URL(), http.DefaultTransport)
	client := &http.Client{Transport: baseTransport}

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	payload := bytes.Repeat([]byte("resume-chunk-"), 600)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	result, err := PublishFile(context.Background(), PublishOptions{
		ServerURL:          origin.URL,
		ResourceBase:       origin.URL + "/masked",
		InputPath:          inputPath,
		ChunkSize:          4 * 1024,
		SymbolSize:         1024,
		MinRecoverySymbols: 1,
		FECTotalMillis:     1000,
		PublishWorkers:     2,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
		NoVerify:           true,
	})
	if err != nil {
		t.Fatalf("PublishFile returned error: %v", err)
	}
	loaded, err := loadManifest(context.Background(), client, result.ManifestURL, false, nil)
	if err != nil {
		t.Fatalf("loadManifest returned error: %v", err)
	}
	if len(loaded.Chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(loaded.Chunks))
	}
	labels := make(map[string]uint64)
	failOnce := make(map[string]int)
	for _, chunk := range loaded.Chunks {
		ids, err := expandSymbolRanges(chunk.SymbolRanges)
		if err != nil {
			t.Fatalf("expandSymbolRanges returned error: %v", err)
		}
		for _, id := range ids {
			cacheURL := symbolCacheURL(t, loaded, chunk.Index, id)
			labels[cacheURL] = chunk.Index*1000 + id
			if chunk.Index == 1 && id < 2 {
				failOnce[cacheURL] = 1
			}
		}
	}
	flakyTransport := &transientSymbolTransport{
		underlying: baseTransport,
		labels:     labels,
		failures:   failOnce,
	}
	outputPath := filepath.Join(t.TempDir(), "output.bin")
	_, err = GetFile(context.Background(), GetOptions{
		ManifestURL:   result.ManifestURL,
		OutputPath:    outputPath,
		HTTPClient:    &http.Client{Transport: flakyTransport},
		SymbolWorkers: 1,
		SymbolRetries: 2,
		RetryInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("output payload mismatch")
	}
	sequence := flakyTransport.sequence()
	seenSecondChunk := false
	secondChunkCounts := make(map[uint64]int)
	for _, label := range sequence {
		if label >= 1000 {
			seenSecondChunk = true
			secondChunkCounts[label]++
			continue
		}
		if seenSecondChunk {
			t.Fatalf("recovered chunk 0 was retried after chunk 1 started; sequence=%v", sequence)
		}
	}
	retriedSecondChunk := false
	for _, count := range secondChunkCounts {
		if count > 1 {
			retriedSecondChunk = true
			break
		}
	}
	if !retriedSecondChunk {
		t.Fatalf("sequence=%v, want retry of failed symbols from chunk 1", sequence)
	}
}

func TestGetFileVerboseLogsFullSymbolError(t *testing.T) {
	_, origin := newAMPFileTestServer(t)
	defer origin.Close()
	cache := newFakeAMPCache(t, origin.URL)
	defer cache.close()
	baseTransport := rewriteCacheTransport(t, cache.URL(), http.DefaultTransport)
	client := &http.Client{Transport: baseTransport}

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	payload := bytes.Repeat([]byte("verbose-error"), 256)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	result, err := PublishFile(context.Background(), PublishOptions{
		ServerURL:          origin.URL,
		ResourceBase:       origin.URL + "/masked",
		InputPath:          inputPath,
		ChunkSize:          8 * 1024,
		SymbolSize:         1024,
		MinRecoverySymbols: 1,
		FECTotalMillis:     1000,
		PublishWorkers:     2,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
		NoVerify:           true,
	})
	if err != nil {
		t.Fatalf("PublishFile returned error: %v", err)
	}
	loaded, err := loadManifest(context.Background(), client, result.ManifestURL, false, nil)
	if err != nil {
		t.Fatalf("loadManifest returned error: %v", err)
	}
	labels := make(map[string]uint64)
	failures := make(map[string]int)
	ids, err := expandSymbolRanges(loaded.Chunks[0].SymbolRanges)
	if err != nil {
		t.Fatalf("expandSymbolRanges returned error: %v", err)
	}
	for _, id := range ids {
		cacheURL := symbolCacheURL(t, loaded, 0, id)
		labels[cacheURL] = id
		failures[cacheURL] = 1
	}
	longBody := strings.Repeat("full-error-line\n", 80) + "verbose-tail"
	flakyTransport := &transientSymbolTransport{
		underlying:  baseTransport,
		labels:      labels,
		failures:    failures,
		failureBody: longBody,
	}
	var logs bytes.Buffer
	_, err = GetFile(context.Background(), GetOptions{
		ManifestURL:   result.ManifestURL,
		OutputPath:    filepath.Join(t.TempDir(), "output.bin"),
		HTTPClient:    &http.Client{Transport: flakyTransport},
		SymbolWorkers: 1,
		SymbolRetries: 0,
		Verbose:       true,
		Logger:        log.New(&logs, "", 0),
	})
	if err == nil {
		t.Fatalf("GetFile succeeded, want symbol failure")
	}
	text := logs.String()
	if !strings.Contains(text, "cache_url=https://") {
		t.Fatalf("verbose log did not include cache URL: %s", text)
	}
	if !strings.Contains(text, "verbose-tail") {
		t.Fatalf("verbose log did not include full error body tail: %s", text)
	}
}

func TestVerifyRetriesFailedSymbolsWithoutRepeatingFullPass(t *testing.T) {
	_, origin := newAMPFileTestServer(t)
	defer origin.Close()
	cache := newFakeAMPCache(t, origin.URL)
	defer cache.close()
	baseTransport := rewriteCacheTransport(t, cache.URL(), http.DefaultTransport)
	client := &http.Client{Transport: baseTransport}

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	payload := bytes.Repeat([]byte("verify-loop"), 256)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	result, err := PublishFile(context.Background(), PublishOptions{
		ServerURL:          origin.URL,
		ResourceBase:       origin.URL + "/masked",
		InputPath:          inputPath,
		ChunkSize:          16 * 1024,
		SymbolSize:         1024,
		MinRecoverySymbols: 1,
		FECTotalMillis:     1000,
		PublishWorkers:     2,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
		NoVerify:           true,
	})
	if err != nil {
		t.Fatalf("PublishFile returned error: %v", err)
	}
	loaded, err := loadManifest(context.Background(), client, result.ManifestURL, false, nil)
	if err != nil {
		t.Fatalf("loadManifest returned error: %v", err)
	}
	ids, err := expandSymbolRanges(loaded.Chunks[0].SymbolRanges)
	if err != nil {
		t.Fatalf("expandSymbolRanges returned error: %v", err)
	}
	total := uint64(len(ids))
	labels := make(map[string]uint64)
	failures := make(map[string]int)
	for _, id := range ids {
		cacheURL := symbolCacheURL(t, loaded, 0, id)
		labels[cacheURL] = id
		failures[cacheURL] = 2
	}
	flakyTransport := &transientSymbolTransport{
		underlying: baseTransport,
		labels:     labels,
		failures:   failures,
	}
	_, err = VerifyFile(context.Background(), VerifyOptions{
		ManifestURL:     result.ManifestURL,
		HTTPClient:      &http.Client{Transport: flakyTransport},
		SymbolWorkers:   1,
		SymbolRetries:   1,
		Timeout:         time.Second,
		RetryInterval:   time.Nanosecond,
		DebugSymbolURLs: true,
	})
	if err == nil {
		t.Fatalf("VerifyFile succeeded, want unrecoverable symbol failure")
	}
	sequence := flakyTransport.sequence()
	want := int(total * 2)
	if len(sequence) != want {
		t.Fatalf("symbol request sequence length = %d, want %d; sequence=%v", len(sequence), want, sequence)
	}
	for i, id := range ids {
		if sequence[i] != id {
			t.Fatalf("first symbol pass sequence = %v, want sequential first pass through %d symbols", sequence, total)
		}
	}
	for i, id := range ids {
		if sequence[len(ids)+i] != id {
			t.Fatalf("retry symbol pass sequence = %v, want only failed symbols retried once", sequence)
		}
	}
}

func TestRepairFileFromSourceAppendsRangesAndPreservesOldRanges(t *testing.T) {
	_, origin := newAMPFileTestServer(t)
	defer origin.Close()
	cache := newFakeAMPCache(t, origin.URL)
	defer cache.close()
	client := &http.Client{Transport: rewriteCacheTransport(t, cache.URL(), http.DefaultTransport)}

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	payload := bytes.Repeat([]byte("repair-source-"), 400)
	if err := os.WriteFile(inputPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	result, err := PublishFile(context.Background(), PublishOptions{
		ServerURL:          origin.URL,
		ResourceBase:       origin.URL + "/masked",
		InputPath:          inputPath,
		ChunkSize:          8 * 1024,
		SymbolSize:         1024,
		MinRecoverySymbols: 1,
		FECTotalMillis:     1000,
		PublishWorkers:     4,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
		NoVerify:           true,
	})
	if err != nil {
		t.Fatalf("PublishFile returned error: %v", err)
	}
	loaded, err := loadManifest(context.Background(), client, result.ManifestURL, false, nil)
	if err != nil {
		t.Fatalf("loadManifest returned error: %v", err)
	}
	oldRanges := append([]SymbolRange(nil), loaded.Chunks[0].SymbolRanges...)
	oldIDs, err := expandSymbolRanges(oldRanges)
	if err != nil {
		t.Fatalf("expandSymbolRanges returned error: %v", err)
	}
	for _, id := range oldIDs {
		cache.deleteSymbol(t, loaded, 0, id)
	}

	repair, err := RepairFile(context.Background(), RepairOptions{
		ServerURL:          origin.URL,
		ManifestURL:        result.ManifestURL,
		SourcePath:         inputPath,
		MinRecoverySymbols: 2,
		FECTotalMillis:     1000,
		PublishWorkers:     4,
		HTTPClient:         client,
		UploadTimeout:      2 * time.Second,
		PollInterval:       time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RepairFile returned error: %v", err)
	}
	if repair.ManifestURL == result.ManifestURL {
		t.Fatalf("repair did not produce a new manifest URL")
	}
	if repair.RepairedChunks != 1 || repair.UploadedSymbols == 0 {
		t.Fatalf("repair result = %+v, want one repaired chunk with uploaded symbols", repair)
	}
	repaired, err := loadManifest(context.Background(), client, repair.ManifestURL, false, nil)
	if err != nil {
		t.Fatalf("load repaired manifest returned error: %v", err)
	}
	if len(repaired.Chunks[0].SymbolRanges) != len(oldRanges)+1 {
		t.Fatalf("repaired ranges = %+v, want old ranges plus one new range", repaired.Chunks[0].SymbolRanges)
	}
	if repaired.Chunks[0].SymbolRanges[0] != oldRanges[0] {
		t.Fatalf("old range was not preserved: got %+v want %+v", repaired.Chunks[0].SymbolRanges[0], oldRanges[0])
	}
	newRange := repaired.Chunks[0].SymbolRanges[len(repaired.Chunks[0].SymbolRanges)-1]
	if newRange.End > maxSymbolIndex {
		t.Fatalf("new repair range end = %d, want <= %d", newRange.End, uint64(maxSymbolIndex))
	}

	outputPath := filepath.Join(t.TempDir(), "output.bin")
	getResult, err := GetFile(context.Background(), GetOptions{
		ManifestURL:   repair.ManifestURL,
		OutputPath:    outputPath,
		HTTPClient:    client,
		SymbolWorkers: 4,
		Timeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatalf("GetFile repaired manifest returned error: %v", err)
	}
	if getResult.FileSize != uint64(len(payload)) {
		t.Fatalf("get file size = %d, want %d", getResult.FileSize, len(payload))
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("repaired output payload mismatch")
	}
}

func TestChooseRepairRangeHonorsSymbolCap(t *testing.T) {
	for i := 0; i < 100; i++ {
		r, err := chooseRepairRange(nil, 64)
		if err != nil {
			t.Fatalf("chooseRepairRange returned error: %v", err)
		}
		if r.End > maxSymbolIndex {
			t.Fatalf("range end = %d, want <= %d", r.End, uint64(maxSymbolIndex))
		}
	}
}

type fakeAMPCache struct {
	t         *testing.T
	originURL string
	server    *httptest.Server
	mu        sync.Mutex
	stored    map[string][]byte
}

func newAMPFileTestServer(t *testing.T) (*ampcache.Server, *httptest.Server) {
	t.Helper()
	var store *ampcache.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.ServeHTTP(w, r)
	}))
	var err error
	store, err = ampcache.NewServer(ampcache.ServerConfig{
		PublicURL:        ts.URL,
		CacheDomain:      ampcache.DefaultCacheDomain,
		Timeout:          2 * time.Second,
		MaxResourceBytes: 2 << 20,
	})
	if err != nil {
		ts.Close()
		t.Fatalf("NewServer returned error: %v", err)
	}
	return store, ts
}

func newFakeAMPCache(t *testing.T, originURL string) *fakeAMPCache {
	t.Helper()
	cache := &fakeAMPCache{t: t, originURL: originURL, stored: make(map[string][]byte)}
	cache.server = httptest.NewServer(http.HandlerFunc(cache.serve))
	return cache
}

func (c *fakeAMPCache) close() {
	c.server.Close()
}

func (c *fakeAMPCache) URL() string {
	return c.server.URL
}

func (c *fakeAMPCache) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	stored := append([]byte(nil), c.stored[r.URL.RequestURI()]...)
	c.mu.Unlock()
	if stored != nil {
		w.Header().Set("Content-Type", ampcache.ContentType(ampcache.EncodingFont))
		_, _ = w.Write(stored)
		return
	}
	originURL := originURLFromCachePath(c.t, r.URL.Path)
	resp, err := http.Get(originURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		c.mu.Lock()
		c.stored[r.URL.RequestURI()] = body
		c.mu.Unlock()
	}
	http.Error(w, "not public yet", http.StatusNotFound)
}

func (c *fakeAMPCache) deleteSymbol(t *testing.T, loaded *loadedManifest, chunkIndex, symbolIndex uint64) {
	t.Helper()
	cacheURL := symbolCacheURL(t, loaded, chunkIndex, symbolIndex)
	u, err := url.Parse(cacheURL)
	if err != nil {
		t.Fatalf("url.Parse returned error: %v", err)
	}
	c.mu.Lock()
	delete(c.stored, u.RequestURI())
	c.mu.Unlock()
}

func symbolCacheURL(t *testing.T, loaded *loadedManifest, chunkIndex, symbolIndex uint64) string {
	t.Helper()
	logical := symbolLogicalID(loaded.Root.RunID, chunkIndex, symbolIndex)
	token := pathToken(loaded.PKey, logical)
	originURL := resourceURLForToken(loaded.Root.ResourceBase, token)
	cacheURL, err := ampcache.CreateCacheURL(loaded.Root.CacheDomain, originURL, ampcache.EncodingFont)
	if err != nil {
		t.Fatalf("CreateCacheURL returned error: %v", err)
	}
	return cacheURL
}

func manifestURLWithoutHash(t *testing.T, manifestURL string) string {
	t.Helper()
	u, err := url.Parse(manifestURL)
	if err != nil {
		t.Fatalf("url.Parse returned error: %v", err)
	}
	values, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatalf("ParseQuery returned error: %v", err)
	}
	key := values.Get(keyFragmentName)
	if key == "" {
		t.Fatalf("manifest URL did not contain key fragment: %q", manifestURL)
	}
	u.Fragment = keyFragmentName + "=" + key
	return u.String()
}

func manifestURLWithHash(t *testing.T, manifestURL, hash string) string {
	t.Helper()
	u, err := url.Parse(manifestURL)
	if err != nil {
		t.Fatalf("url.Parse returned error: %v", err)
	}
	values, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatalf("ParseQuery returned error: %v", err)
	}
	key := values.Get(keyFragmentName)
	if key == "" {
		t.Fatalf("manifest URL did not contain key fragment: %q", manifestURL)
	}
	u.Fragment = keyFragmentName + "=" + key + "&" + hashFragmentName + "=" + hash
	return u.String()
}

type transientSymbolTransport struct {
	underlying  http.RoundTripper
	labels      map[string]uint64
	failures    map[string]int
	failureBody string
	failureErr  error
	mu          sync.Mutex
	requests    []uint64
}

func (rt *transientSymbolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	label, isSymbol := rt.labels[req.URL.String()]
	if isSymbol {
		rt.requests = append(rt.requests, label)
	}
	remaining := rt.failures[req.URL.String()]
	if remaining > 0 {
		rt.failures[req.URL.String()] = remaining - 1
	}
	rt.mu.Unlock()
	if remaining > 0 {
		if rt.failureErr != nil {
			return nil, rt.failureErr
		}
		body := rt.failureBody
		if body == "" {
			body = "transient rate limit"
		}
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
	return rt.underlying.RoundTrip(req)
}

func (rt *transientSymbolTransport) sequence() []uint64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]uint64(nil), rt.requests...)
}

type rewriteRoundTripper struct {
	t          *testing.T
	cacheURL   *url.URL
	underlying http.RoundTripper
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
