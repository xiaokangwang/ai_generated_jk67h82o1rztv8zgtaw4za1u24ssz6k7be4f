package ampfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gomodstore/internal/ampcache"
)

const (
	DefaultChunkSize          = 64 * 1024 * 1024
	DefaultSymbolSize         = 512 * 1024
	DefaultMinRecoverySymbols = 16
	DefaultFECTotalMillis     = 1250
	DefaultPublishWorkers     = 4
	DefaultSymbolWorkers      = 32
	DefaultSymbolRetries      = 6
	DefaultChunksPerPage      = 1024
)

type PublishOptions struct {
	ServerURL          string
	ResourceBase       string
	CacheDomain        string
	InputPath          string
	ChunkSize          uint64
	SymbolSize         uint64
	MinRecoverySymbols uint64
	FECTotalMillis     uint64
	PublishWorkers     int
	VerifyWorkers      int
	HTTPClient         *http.Client
	UploadTimeout      time.Duration
	PollInterval       time.Duration
	VerifyTimeout      time.Duration
	VerifyPollInterval time.Duration
	DebugSymbolURLs    bool
	NoVerify           bool
	Logger             *log.Logger
}

type RepairOptions struct {
	ServerURL          string
	ResourceBase       string
	CacheDomain        string
	ManifestURL        string
	SourcePath         string
	MinRecoverySymbols uint64
	FECTotalMillis     uint64
	PublishWorkers     int
	HTTPClient         *http.Client
	UploadTimeout      time.Duration
	PollInterval       time.Duration
	Logger             *log.Logger
}

type PublishResult struct {
	ManifestURL        string       `json:"manifest_url"`
	RunID              string       `json:"run_id"`
	FileName           string       `json:"file_name"`
	FileSize           uint64       `json:"file_size"`
	FileSHA256         string       `json:"file_sha256"`
	ChunkCount         uint64       `json:"chunk_count"`
	SymbolSize         uint64       `json:"symbol_size"`
	ChunkSize          uint64       `json:"chunk_size"`
	MinRecoverySymbols uint64       `json:"min_recovery_symbols"`
	FECTotalMillis     uint64       `json:"fec_total_millis"`
	Verified           bool         `json:"verified"`
	Verify             VerifyResult `json:"verify,omitempty"`
}

type RepairResult struct {
	ManifestURL     string `json:"manifest_url"`
	FileSize        uint64 `json:"file_size"`
	FileSHA256      string `json:"file_sha256"`
	ChunkCount      uint64 `json:"chunk_count"`
	RepairedChunks  uint64 `json:"repaired_chunks"`
	UploadedSymbols uint64 `json:"uploaded_symbols"`
}

type VerifyOptions struct {
	ManifestURL        string
	HTTPClient         *http.Client
	SymbolWorkers      int
	SymbolRetries      int
	Timeout            time.Duration
	RetryInterval      time.Duration
	Logger             *log.Logger
	StopAfterRecovered bool
	DebugSymbolURLs    bool
}

type VerifyResult struct {
	RecoveredChunks uint64              `json:"recovered_chunks"`
	TotalChunks     uint64              `json:"total_chunks"`
	FileSHA256      string              `json:"file_sha256"`
	Warnings        bool                `json:"warnings"`
	Chunks          []ChunkVerifyResult `json:"chunks"`
}

type ChunkVerifyResult struct {
	Index         uint64         `json:"index"`
	Recovered     bool           `json:"recovered"`
	Attempted     uint64         `json:"attempted"`
	Succeeded     uint64         `json:"succeeded"`
	Failed        uint64         `json:"failed"`
	SourceSymbols uint64         `json:"source_symbols"`
	TotalSymbols  uint64         `json:"total_symbols"`
	Errors        map[string]int `json:"errors,omitempty"`
	SampleErrors  []string       `json:"sample_errors,omitempty"`
}

type GetOptions struct {
	ManifestURL   string
	OutputPath    string
	HTTPClient    *http.Client
	SymbolWorkers int
	SymbolRetries int
	Verbose       bool
	Timeout       time.Duration
	RetryInterval time.Duration
	Logger        *log.Logger
}

type GetResult struct {
	OutputPath string              `json:"output_path"`
	FileSize   uint64              `json:"file_size"`
	FileSHA256 string              `json:"file_sha256"`
	Chunks     uint64              `json:"chunks"`
	Download   DownloadStats       `json:"download"`
	Recovered  []ChunkVerifyResult `json:"recovered"`
}

type DownloadStats struct {
	CacheBodyBytes                        uint64  `json:"cache_body_bytes"`
	DecodedPayloadBytes                   uint64  `json:"decoded_payload_bytes"`
	ManifestCacheBodyBytes                uint64  `json:"manifest_cache_body_bytes"`
	ManifestDecodedPayloadBytes           uint64  `json:"manifest_decoded_payload_bytes"`
	SymbolCacheBodyBytes                  uint64  `json:"symbol_cache_body_bytes"`
	SymbolDecodedPayloadBytes             uint64  `json:"symbol_decoded_payload_bytes"`
	FontEncodingOverheadBytes             uint64  `json:"font_encoding_overhead_bytes"`
	ManifestFontEncodingOverheadBytes     uint64  `json:"manifest_font_encoding_overhead_bytes"`
	SymbolFontEncodingOverheadBytes       uint64  `json:"symbol_font_encoding_overhead_bytes"`
	ManifestResponses                     uint64  `json:"manifest_responses"`
	SymbolResponses                       uint64  `json:"symbol_responses"`
	SourceSymbols                         uint64  `json:"source_symbols"`
	AttemptedSymbols                      uint64  `json:"attempted_symbols"`
	SucceededSymbols                      uint64  `json:"succeeded_symbols"`
	FailedSymbols                         uint64  `json:"failed_symbols"`
	TotalAvailableSymbols                 uint64  `json:"total_available_symbols"`
	CacheBodyOverFileRatio                float64 `json:"cache_body_over_file_ratio"`
	SymbolSuccessOverSourceRatio          float64 `json:"symbol_success_over_source_ratio"`
	SymbolSuccessOverAvailableRatio       float64 `json:"symbol_success_over_available_ratio"`
	FontEncodingOverheadRatio             float64 `json:"font_encoding_overhead_ratio"`
	ManifestFontEncodingOverheadRatio     float64 `json:"manifest_font_encoding_overhead_ratio"`
	SymbolFontEncodingOverheadRatio       float64 `json:"symbol_font_encoding_overhead_ratio"`
	CacheBodyOverDecodedPayloadRatio      float64 `json:"cache_body_over_decoded_payload_ratio"`
	SymbolCacheBodyOverSymbolDecodedRatio float64 `json:"symbol_cache_body_over_symbol_decoded_ratio"`
}

type downloadCounter struct {
	mu    sync.Mutex
	stats DownloadStats
}

func (c *downloadCounter) add(kind string, cacheBodyBytes, decodedPayloadBytes uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.CacheBodyBytes += cacheBodyBytes
	c.stats.DecodedPayloadBytes += decodedPayloadBytes
	switch kind {
	case "manifest":
		c.stats.ManifestResponses++
		c.stats.ManifestCacheBodyBytes += cacheBodyBytes
		c.stats.ManifestDecodedPayloadBytes += decodedPayloadBytes
	case "symbol":
		c.stats.SymbolResponses++
		c.stats.SymbolCacheBodyBytes += cacheBodyBytes
		c.stats.SymbolDecodedPayloadBytes += decodedPayloadBytes
	}
}

func (c *downloadCounter) snapshot() DownloadStats {
	if c == nil {
		return DownloadStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

func (s *DownloadStats) fillRatios(fileSize uint64) {
	s.FontEncodingOverheadBytes = positiveDiff(s.CacheBodyBytes, s.DecodedPayloadBytes)
	s.ManifestFontEncodingOverheadBytes = positiveDiff(s.ManifestCacheBodyBytes, s.ManifestDecodedPayloadBytes)
	s.SymbolFontEncodingOverheadBytes = positiveDiff(s.SymbolCacheBodyBytes, s.SymbolDecodedPayloadBytes)
	s.CacheBodyOverFileRatio = ratio(s.CacheBodyBytes, fileSize)
	s.SymbolSuccessOverSourceRatio = ratio(s.SucceededSymbols, s.SourceSymbols)
	s.SymbolSuccessOverAvailableRatio = ratio(s.SucceededSymbols, s.TotalAvailableSymbols)
	s.FontEncodingOverheadRatio = ratio(s.FontEncodingOverheadBytes, s.DecodedPayloadBytes)
	s.ManifestFontEncodingOverheadRatio = ratio(s.ManifestFontEncodingOverheadBytes, s.ManifestDecodedPayloadBytes)
	s.SymbolFontEncodingOverheadRatio = ratio(s.SymbolFontEncodingOverheadBytes, s.SymbolDecodedPayloadBytes)
	s.CacheBodyOverDecodedPayloadRatio = ratio(s.CacheBodyBytes, s.DecodedPayloadBytes)
	s.SymbolCacheBodyOverSymbolDecodedRatio = ratio(s.SymbolCacheBodyBytes, s.SymbolDecodedPayloadBytes)
}

type loadedManifest struct {
	Root   RootManifest
	Pages  []PageManifest
	Key    []byte
	CKey   []byte
	PKey   []byte
	Chunks []ChunkManifest
}

func PublishFile(ctx context.Context, opts PublishOptions) (*PublishResult, error) {
	if err := opts.withDefaults(); err != nil {
		return nil, err
	}
	logf := func(string, ...any) {}
	if opts.Logger != nil {
		logf = opts.Logger.Printf
	}
	file, err := os.Open(opts.InputPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("input path must be a regular file")
	}
	fileSize := uint64(info.Size())
	chunkCount := ceilDiv(fileSize, opts.ChunkSize)
	runID := newRunID()
	key, err := randomBytes(32)
	if err != nil {
		return nil, err
	}
	cKey := contentKey(key)
	pKey := pathKey(key)
	fileHash := sha256.New()
	chunks := make([]ChunkManifest, 0, chunkCount)

	for chunkIndex := uint64(0); chunkIndex < chunkCount; chunkIndex++ {
		offset := chunkIndex * opts.ChunkSize
		length := min(opts.ChunkSize, fileSize-offset)
		chunk := make([]byte, length)
		if _, err := file.ReadAt(chunk, int64(offset)); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		_, _ = fileHash.Write(chunk)
		chunkSum := sha256.Sum256(chunk)
		source := sourceSymbolCount(length, opts.SymbolSize)
		total := totalSymbolCount(source, opts.MinRecoverySymbols, opts.FECTotalMillis)
		ranges := symbolRangesForInitialUpload(total)
		chunks = append(chunks, ChunkManifest{Index: chunkIndex, Offset: offset, Length: length, SHA256: chunkSum[:], SymbolRanges: ranges})
		logf("publish-file: chunk=%d length=%d source_symbols=%d total_symbols=%d", chunkIndex, length, source, total)
		if err := publishChunkSymbols(ctx, opts, runID, pKey, cKey, chunkIndex, chunk, ranges, logf); err != nil {
			return nil, err
		}
	}
	fileSum := fileHash.Sum(nil)
	pageRefs, err := publishManifestPages(ctx, opts, runID, cKey, chunks, logf)
	if err != nil {
		return nil, err
	}
	root := RootManifest{
		Version:       manifestVersion,
		RunID:         runID,
		FileName:      filepath.Base(opts.InputPath),
		FileSize:      fileSize,
		FileSHA256:    fileSum,
		ResourceBase:  opts.ResourceBase,
		CacheDomain:   opts.CacheDomain,
		ChunkSize:     opts.ChunkSize,
		SymbolSize:    opts.SymbolSize,
		ChunkCount:    chunkCount,
		ChunksPerPage: DefaultChunksPerPage,
		Pages:         pageRefs,
	}
	keyed, rootCacheURL, err := publishRootManifest(ctx, opts, key, cKey, root, logf)
	if err != nil {
		return nil, err
	}
	logf("publish-file: root_manifest uploaded cache_url=%s", rootCacheURL)
	logf("publish-file: manifest_url=%s", keyed)
	result := &PublishResult{
		ManifestURL:        keyed,
		RunID:              runID,
		FileName:           root.FileName,
		FileSize:           root.FileSize,
		FileSHA256:         hex.EncodeToString(root.FileSHA256),
		ChunkCount:         root.ChunkCount,
		SymbolSize:         root.SymbolSize,
		ChunkSize:          root.ChunkSize,
		MinRecoverySymbols: opts.MinRecoverySymbols,
		FECTotalMillis:     opts.FECTotalMillis,
	}
	if !opts.NoVerify {
		logf("publish-file: verification start timeout=%s workers=%d", opts.VerifyTimeout, opts.VerifyWorkers)
		verifyCtx := ctx
		if opts.VerifyTimeout > 0 {
			var cancel context.CancelFunc
			verifyCtx, cancel = context.WithTimeout(ctx, opts.VerifyTimeout)
			defer cancel()
		}
		verify, err := VerifyFile(verifyCtx, VerifyOptions{
			ManifestURL:     keyed,
			HTTPClient:      opts.HTTPClient,
			SymbolWorkers:   opts.VerifyWorkers,
			SymbolRetries:   DefaultSymbolRetries,
			RetryInterval:   opts.VerifyPollInterval,
			Logger:          opts.Logger,
			DebugSymbolURLs: opts.DebugSymbolURLs,
		})
		if err != nil {
			return nil, fmt.Errorf("publish verification failed: %w", err)
		}
		result.Verified = true
		result.Verify = *verify
		logf("publish-file: verification complete recovered_chunks=%d/%d warnings=%v", verify.RecoveredChunks, verify.TotalChunks, verify.Warnings)
	}
	return result, nil
}

func RepairFile(ctx context.Context, opts RepairOptions) (*RepairResult, error) {
	if err := opts.withDefaults(); err != nil {
		return nil, err
	}
	logf := func(string, ...any) {}
	if opts.Logger != nil {
		logf = opts.Logger.Printf
	}
	loaded, err := loadManifest(ctx, opts.HTTPClient, opts.ManifestURL, false, nil)
	if err != nil {
		return nil, err
	}
	if opts.ResourceBase == "" {
		opts.ResourceBase = loaded.Root.ResourceBase
	} else if opts.ResourceBase != loaded.Root.ResourceBase {
		return nil, errors.New("repair resource base must match manifest resource base")
	}
	if opts.CacheDomain == "" {
		opts.CacheDomain = loaded.Root.CacheDomain
	} else if opts.CacheDomain != loaded.Root.CacheDomain {
		return nil, errors.New("repair cache domain must match manifest cache domain")
	}
	publishOpts := publishOptionsFromRepair(opts)
	publishOpts.SymbolSize = loaded.Root.SymbolSize

	var source *os.File
	if opts.SourcePath != "" {
		source, err = openVerifiedSource(opts.SourcePath, loaded.Root.FileSize, loaded.Root.FileSHA256)
		if err != nil {
			return nil, err
		}
		defer source.Close()
	}

	updatedChunks := cloneChunks(loaded.Chunks)
	changedPages := make(map[uint64]bool)
	var repairedChunks uint64
	var uploadedSymbols uint64
	for i, chunk := range updatedChunks {
		sourceSymbols := sourceSymbolCount(chunk.Length, loaded.Root.SymbolSize)
		targetTotal := totalSymbolCount(sourceSymbols, opts.MinRecoverySymbols, opts.FECTotalMillis)
		start := time.Now()
		logf("repair-file: chunk=%d start length=%d source_symbols=%d target_symbols=%d available_symbols=%d", chunk.Index, chunk.Length, sourceSymbols, targetTotal, chunk.AvailableSymbols())
		recovered, chunkResult, recoverErr := recoverChunk(ctx, opts.HTTPClient, loaded, chunk, DefaultSymbolWorkers, false, 0, 0, false, nil)
		var chunkBytes []byte
		if recoverErr == nil {
			chunkBytes, err = io.ReadAll(recovered.reader())
			if err != nil {
				return nil, err
			}
		} else if source != nil {
			chunkBytes, err = readSourceChunk(source, chunk.Offset, chunk.Length)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("chunk %d cannot be recovered and no matching source file was supplied: %w", chunk.Index, recoverErr)
		}
		successful := chunkResult.Succeeded
		if successful >= targetTotal {
			logf("repair-file: chunk=%d no repair needed after=%s successful=%d target=%d", chunk.Index, time.Since(start).Round(time.Millisecond), successful, targetTotal)
			continue
		}
		newSymbols := targetTotal - successful
		newRange, err := chooseRepairRange(chunk.SymbolRanges, newSymbols)
		if err != nil {
			return nil, fmt.Errorf("choose repair range for chunk %d: %w", chunk.Index, err)
		}
		logf("repair-file: chunk=%d upload repair range=[%d,%d] symbols=%d", chunk.Index, newRange.Start, newRange.End, newSymbols)
		if err := publishChunkSymbols(ctx, publishOpts, loaded.Root.RunID, loaded.PKey, loaded.CKey, chunk.Index, chunkBytes, []SymbolRange{newRange}, logf); err != nil {
			return nil, err
		}
		updatedChunks[i].SymbolRanges = append(updatedChunks[i].SymbolRanges, newRange)
		pageIndex := chunk.Index / DefaultChunksPerPage
		changedPages[pageIndex] = true
		repairedChunks++
		uploadedSymbols += newSymbols
	}

	if uploadedSymbols == 0 {
		logf("repair-file: no repair symbols uploaded")
		return &RepairResult{
			ManifestURL:     opts.ManifestURL,
			FileSize:        loaded.Root.FileSize,
			FileSHA256:      hex.EncodeToString(loaded.Root.FileSHA256),
			ChunkCount:      loaded.Root.ChunkCount,
			RepairedChunks:  0,
			UploadedSymbols: 0,
		}, nil
	}

	oldRefs := make(map[uint64]string)
	for _, ref := range loaded.Root.Pages {
		oldRefs[ref.Index] = ref.CacheURL
	}
	refs, err := publishManifestPagesSelective(ctx, publishOpts, loaded.Root.RunID, loaded.CKey, updatedChunks, oldRefs, changedPages, logf)
	if err != nil {
		return nil, err
	}
	root := RootManifest{
		Version:       manifestVersion,
		RunID:         loaded.Root.RunID,
		FileName:      loaded.Root.FileName,
		FileSize:      loaded.Root.FileSize,
		FileSHA256:    loaded.Root.FileSHA256,
		ResourceBase:  loaded.Root.ResourceBase,
		CacheDomain:   loaded.Root.CacheDomain,
		ChunkSize:     loaded.Root.ChunkSize,
		SymbolSize:    loaded.Root.SymbolSize,
		ChunkCount:    loaded.Root.ChunkCount,
		ChunksPerPage: DefaultChunksPerPage,
		Pages:         refs,
	}
	keyed, rootCacheURL, err := publishRootManifest(ctx, publishOpts, loaded.Key, loaded.CKey, root, logf)
	if err != nil {
		return nil, err
	}
	logf("repair-file: root_manifest uploaded cache_url=%s", rootCacheURL)
	logf("repair-file: manifest_url=%s repaired_chunks=%d uploaded_symbols=%d", keyed, repairedChunks, uploadedSymbols)
	return &RepairResult{
		ManifestURL:     keyed,
		FileSize:        loaded.Root.FileSize,
		FileSHA256:      hex.EncodeToString(loaded.Root.FileSHA256),
		ChunkCount:      loaded.Root.ChunkCount,
		RepairedChunks:  repairedChunks,
		UploadedSymbols: uploadedSymbols,
	}, nil
}

func VerifyFile(ctx context.Context, opts VerifyOptions) (*VerifyResult, error) {
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.SymbolWorkers <= 0 {
		opts.SymbolWorkers = DefaultSymbolWorkers
	}
	if opts.SymbolRetries < 0 {
		return nil, errors.New("symbol retries must be non-negative")
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = 2 * time.Second
	}
	logf := func(string, ...any) {}
	if opts.Logger != nil {
		logf = opts.Logger.Printf
	}
	retryManifest := false
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
		retryManifest = true
	} else if _, ok := ctx.Deadline(); ok {
		retryManifest = true
	}
	start := time.Now()
	logf("verify-file: attempt=1 start workers=%d symbol_retries=%d", opts.SymbolWorkers, opts.SymbolRetries)
	loaded, err := loadManifestForVerify(ctx, opts, retryManifest, logf)
	if err != nil {
		logf("verify-file: attempt=1 failed after=%s err=%v", time.Since(start).Round(time.Millisecond), err)
		return nil, err
	}
	result, err := verifyLoadedManifest(ctx, opts, loaded, logf)
	if err == nil {
		logf("verify-file: attempt=1 complete after=%s recovered_chunks=%d/%d warnings=%v", time.Since(start).Round(time.Millisecond), result.RecoveredChunks, result.TotalChunks, result.Warnings)
		return result, nil
	}
	if result != nil {
		logf("verify-file: attempt=1 failed after=%s recovered_chunks=%d/%d err=%v", time.Since(start).Round(time.Millisecond), result.RecoveredChunks, result.TotalChunks, err)
	} else {
		logf("verify-file: attempt=1 failed after=%s err=%v", time.Since(start).Round(time.Millisecond), err)
	}
	return result, err
}

func GetFile(ctx context.Context, opts GetOptions) (*GetResult, error) {
	if opts.OutputPath == "" {
		return nil, errors.New("output path is required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.SymbolWorkers <= 0 {
		opts.SymbolWorkers = DefaultSymbolWorkers
	}
	if opts.SymbolRetries < -1 {
		return nil, errors.New("symbol retries must be non-negative, or -1 for unlimited retries")
	}
	logf := func(string, ...any) {}
	if opts.Logger != nil {
		logf = opts.Logger.Printf
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	start := time.Now()
	logf("get-file: attempt=1 start workers=%d symbol_retries=%d", opts.SymbolWorkers, opts.SymbolRetries)
	result, err := getFileOnce(ctx, opts, logf)
	if err == nil {
		logf("get-file: attempt=1 complete after=%s", time.Since(start).Round(time.Millisecond))
		return result, nil
	}
	logf("get-file: attempt=1 failed after=%s err=%v", time.Since(start).Round(time.Millisecond), err)
	return nil, err
}

func getFileOnce(ctx context.Context, opts GetOptions, logf func(string, ...any)) (*GetResult, error) {
	stats := &downloadCounter{}
	loaded, err := loadManifestForGet(ctx, opts, stats, logf)
	if err != nil {
		return nil, err
	}
	logf("get-file: manifest loaded file_size=%d chunk_count=%d symbol_size=%d", loaded.Root.FileSize, loaded.Root.ChunkCount, loaded.Root.SymbolSize)
	if err := os.MkdirAll(filepath.Dir(opts.OutputPath), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(opts.OutputPath), ".ampcache-get-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	outHash := sha256.New()
	chunkResults := make([]ChunkVerifyResult, 0, len(loaded.Chunks))
	for _, chunk := range loaded.Chunks {
		source := sourceSymbolCount(chunk.Length, loaded.Root.SymbolSize)
		total := chunk.AvailableSymbols()
		start := time.Now()
		logf("get-file: chunk=%d start length=%d source_symbols=%d total_symbols=%d", chunk.Index, chunk.Length, source, total)
		recovery, err := newChunkRecovery(opts.HTTPClient, loaded, chunk, opts.Verbose, stats)
		if err != nil {
			return nil, err
		}
		recovered, chunkResult, err := recovery.recover(ctx, opts.SymbolWorkers, true, opts.SymbolRetries, opts.RetryInterval, func(round int, pending int) {
			if opts.SymbolRetries < 0 {
				logf("get-file: chunk=%d retry_round=%d/unlimited failed_symbols=%d wait=%s", chunk.Index, round, pending, opts.RetryInterval)
				return
			}
			logf("get-file: chunk=%d retry_round=%d/%d failed_symbols=%d wait=%s", chunk.Index, round, opts.SymbolRetries, pending, opts.RetryInterval)
		})
		if err != nil {
			logf("get-file: chunk=%d failed after=%s succeeded=%d failed=%d total=%d errors=%s", chunk.Index, time.Since(start).Round(time.Millisecond), chunkResult.Succeeded, chunkResult.Failed, chunkResult.TotalSymbols, formatErrorCounts(chunkResult.Errors))
			if opts.Verbose {
				for _, sample := range chunkResult.SampleErrors {
					logf("get-file: chunk=%d sample_error: %s", chunk.Index, sample)
				}
			}
			return nil, err
		}
		if opts.Verbose && chunkResult.Failed > 0 {
			logf("get-file: chunk=%d recovered after=%s succeeded=%d failed=%d total=%d errors=%s", chunk.Index, time.Since(start).Round(time.Millisecond), chunkResult.Succeeded, chunkResult.Failed, chunkResult.TotalSymbols, formatErrorCounts(chunkResult.Errors))
			for _, sample := range chunkResult.SampleErrors {
				logf("get-file: chunk=%d sample_error: %s", chunk.Index, sample)
			}
		} else {
			logf("get-file: chunk=%d recovered after=%s succeeded=%d failed=%d total=%d", chunk.Index, time.Since(start).Round(time.Millisecond), chunkResult.Succeeded, chunkResult.Failed, chunkResult.TotalSymbols)
		}
		chunkResults = append(chunkResults, chunkResult)
		if _, err := io.Copy(io.MultiWriter(tmp, outHash), recovered.reader()); err != nil {
			return nil, err
		}
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	gotSum := outHash.Sum(nil)
	if !bytes.Equal(gotSum, loaded.Root.FileSHA256) {
		return nil, errors.New("recovered file SHA-256 mismatch")
	}
	if err := os.Rename(tmpPath, opts.OutputPath); err != nil {
		return nil, err
	}
	download := stats.snapshot()
	for _, chunk := range chunkResults {
		download.SourceSymbols += chunk.SourceSymbols
		download.AttemptedSymbols += chunk.Attempted
		download.SucceededSymbols += chunk.Succeeded
		download.FailedSymbols += chunk.Failed
		download.TotalAvailableSymbols += chunk.TotalSymbols
	}
	download.fillRatios(loaded.Root.FileSize)
	return &GetResult{
		OutputPath: opts.OutputPath,
		FileSize:   loaded.Root.FileSize,
		FileSHA256: hex.EncodeToString(loaded.Root.FileSHA256),
		Chunks:     loaded.Root.ChunkCount,
		Download:   download,
		Recovered:  chunkResults,
	}, nil
}

func loadManifestForGet(ctx context.Context, opts GetOptions, stats *downloadCounter, logf func(string, ...any)) (*loadedManifest, error) {
	attempt := 0
	for {
		attempt++
		loaded, err := loadManifest(ctx, opts.HTTPClient, opts.ManifestURL, opts.Verbose, stats)
		if err == nil {
			return loaded, nil
		}
		if opts.RetryInterval <= 0 || !isRetryableSymbolError(err) {
			return nil, err
		}
		logf("get-file: manifest load attempt=%d failed err=%v; retrying in %s", attempt, err, opts.RetryInterval)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(opts.RetryInterval):
		}
	}
}

func publishChunkSymbols(ctx context.Context, opts PublishOptions, runID string, pKey, cKey []byte, chunkIndex uint64, chunk []byte, ranges []SymbolRange, logf func(string, ...any)) error {
	type job struct {
		id uint64
	}
	ids, err := expandSymbolRanges(ranges)
	if err != nil {
		return err
	}
	jobs := make(chan job)
	errs := make(chan error, 1)
	sendErr := func(err error) {
		if err == nil {
			return
		}
		select {
		case errs <- err:
		default:
		}
	}
	var wg sync.WaitGroup
	for worker := 0; worker < opts.PublishWorkers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				logical := symbolLogicalID(runID, chunkIndex, job.id)
				token := pathToken(pKey, logical)
				originURL := resourceURLForToken(opts.ResourceBase, token)
				symbol, err := encodeSymbol(chunk, int(opts.SymbolSize), job.id)
				if err != nil {
					sendErr(err)
					continue
				}
				encrypted, err := encryptPayload(cKey, aadSymbol(runID, chunkIndex, job.id), symbol)
				if err != nil {
					sendErr(err)
					continue
				}
				label := fmt.Sprintf("chunk=%d symbol=%d", chunkIndex, job.id)
				result, err := publishEncryptedResource(ctx, opts, label, originURL, encrypted)
				if err != nil {
					sendErr(err)
					continue
				}
				if err := validateExpectedCacheURL(opts.CacheDomain, originURL, result.CacheURL); err != nil {
					sendErr(err)
				}
			}
		}()
	}
	for _, id := range ids {
		select {
		case err := <-errs:
			close(jobs)
			wg.Wait()
			return err
		case jobs <- job{id: id}:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
	}
	logf("publish-file: chunk=%d uploaded_symbols=%d", chunkIndex, len(ids))
	return nil
}

func publishManifestPages(ctx context.Context, opts PublishOptions, runID string, cKey []byte, chunks []ChunkManifest, logf func(string, ...any)) ([]ManifestPageRef, error) {
	return publishManifestPagesSelective(ctx, opts, runID, cKey, chunks, nil, nil, logf)
}

func publishManifestPagesSelective(ctx context.Context, opts PublishOptions, runID string, cKey []byte, chunks []ChunkManifest, oldRefs map[uint64]string, changedPages map[uint64]bool, logf func(string, ...any)) ([]ManifestPageRef, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	pageCount := ceilDiv(uint64(len(chunks)), DefaultChunksPerPage)
	refs := make([]ManifestPageRef, 0, pageCount)
	for pageIndex := uint64(0); pageIndex < pageCount; pageIndex++ {
		if oldRefs != nil && changedPages != nil && !changedPages[pageIndex] {
			cacheURL, ok := oldRefs[pageIndex]
			if !ok {
				return nil, fmt.Errorf("missing old manifest page ref %d", pageIndex)
			}
			refs = append(refs, ManifestPageRef{Index: pageIndex, CacheURL: cacheURL})
			continue
		}
		start := pageIndex * DefaultChunksPerPage
		end := min(start+DefaultChunksPerPage, uint64(len(chunks)))
		page := PageManifest{
			Version:    manifestVersion,
			RunID:      runID,
			PageIndex:  pageIndex,
			FirstChunk: start,
			Chunks:     chunks[start:end],
		}
		token, err := randomPathToken()
		if err != nil {
			return nil, err
		}
		originURL := resourceURLForToken(opts.ResourceBase, token)
		encrypted, err := encryptPayload(cKey, aadPage(runID, pageIndex), encodePageManifest(page))
		if err != nil {
			return nil, err
		}
		label := fmt.Sprintf("manifest_page=%d", pageIndex)
		logf("publish-file: %s upload start chunks=%d", label, len(page.Chunks))
		result, err := publishEncryptedResource(ctx, opts, label, originURL, encrypted)
		if err != nil {
			return nil, err
		}
		if err := validateExpectedCacheURL(opts.CacheDomain, originURL, result.CacheURL); err != nil {
			return nil, err
		}
		logf("publish-file: manifest_page=%d uploaded chunks=%d cache_url=%s", pageIndex, len(page.Chunks), result.CacheURL)
		refs = append(refs, ManifestPageRef{Index: pageIndex, CacheURL: result.CacheURL})
	}
	return refs, nil
}

func publishRootManifest(ctx context.Context, opts PublishOptions, key, cKey []byte, root RootManifest, logf func(string, ...any)) (string, string, error) {
	rootPath, err := randomPathToken()
	if err != nil {
		return "", "", err
	}
	rootOrigin := resourceURLForToken(opts.ResourceBase, rootPath)
	rootPayload, err := encryptPayload(cKey, aadRoot(), encodeRootManifest(root))
	if err != nil {
		return "", "", err
	}
	logf("publish-file: root_manifest upload start")
	rootResult, err := publishEncryptedResource(ctx, opts, "root_manifest", rootOrigin, rootPayload)
	if err != nil {
		return "", "", err
	}
	if err := validateExpectedCacheURL(opts.CacheDomain, rootOrigin, rootResult.CacheURL); err != nil {
		return "", "", err
	}
	rootPayloadHash := sha256.Sum256(rootPayload)
	return keyedURL(rootResult.CacheURL, key, rootPayloadHash[:]), rootResult.CacheURL, nil
}

func publishEncryptedResource(ctx context.Context, opts PublishOptions, label, resourceURL string, payload []byte) (*ampcache.PublishResult, error) {
	uploadCtx := ctx
	if opts.UploadTimeout > 0 {
		var cancel context.CancelFunc
		uploadCtx, cancel = context.WithTimeout(ctx, opts.UploadTimeout)
		defer cancel()
	}
	logf := func(string, ...any) {}
	if opts.Logger != nil {
		logf = opts.Logger.Printf
	}
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		start := time.Now()
		for {
			select {
			case <-done:
				return
			case <-timer.C:
				logf("publish-file: waiting for AMP origin fetch label=%s elapsed=%s resource_url=%s", label, time.Since(start).Round(time.Second), resourceURL)
				timer.Reset(10 * time.Second)
			}
		}
	}()
	start := time.Now()
	result, err := ampcache.Publish(uploadCtx, ampcache.PublishOptions{
		ServerURL:    opts.ServerURL,
		ResourceURL:  resourceURL,
		Encoding:     ampcache.EncodingFont,
		Body:         bytes.NewReader(payload),
		HTTPClient:   opts.HTTPClient,
		Timeout:      opts.UploadTimeout,
		PollInterval: opts.PollInterval,
		WaitMode:     "origin-fetch",
	})
	close(done)
	if err != nil {
		logf("publish-file: upload failed label=%s after=%s err=%v", label, time.Since(start).Round(time.Millisecond), err)
		return nil, err
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		logf("publish-file: upload complete label=%s after=%s", label, elapsed.Round(time.Millisecond))
	}
	return result, nil
}

func loadManifestForVerify(ctx context.Context, opts VerifyOptions, retry bool, logf func(string, ...any)) (*loadedManifest, error) {
	for {
		logf("verify-file: loading manifest")
		loaded, err := loadManifest(ctx, opts.HTTPClient, opts.ManifestURL, opts.DebugSymbolURLs, nil)
		if err == nil {
			logf("verify-file: manifest loaded file_size=%d chunk_count=%d symbol_size=%d", loaded.Root.FileSize, loaded.Root.ChunkCount, loaded.Root.SymbolSize)
			return loaded, nil
		}
		if !retry || !isRetryableSymbolError(err) {
			return nil, err
		}
		logf("verify-file: manifest load failed err=%v; retrying in %s", err, opts.RetryInterval)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(opts.RetryInterval):
		}
	}
}

func verifyLoadedManifest(ctx context.Context, opts VerifyOptions, loaded *loadedManifest, logf func(string, ...any)) (*VerifyResult, error) {
	result := &VerifyResult{TotalChunks: loaded.Root.ChunkCount, FileSHA256: hex.EncodeToString(loaded.Root.FileSHA256)}
	fileHash := sha256.New()
	for _, chunk := range loaded.Chunks {
		source := sourceSymbolCount(chunk.Length, loaded.Root.SymbolSize)
		total := chunk.AvailableSymbols()
		logf("verify-file: chunk=%d start length=%d source_symbols=%d total_symbols=%d", chunk.Index, chunk.Length, source, total)
		start := time.Now()
		recovered, chunkResult, err := recoverChunk(ctx, opts.HTTPClient, loaded, chunk, opts.SymbolWorkers, opts.StopAfterRecovered, opts.SymbolRetries, opts.RetryInterval, opts.DebugSymbolURLs, nil)
		result.Chunks = append(result.Chunks, chunkResult)
		if err != nil {
			logf("verify-file: chunk=%d failed after=%s succeeded=%d failed=%d total=%d errors=%s err=%v", chunk.Index, time.Since(start).Round(time.Millisecond), chunkResult.Succeeded, chunkResult.Failed, chunkResult.TotalSymbols, formatErrorCounts(chunkResult.Errors), err)
			for _, sample := range chunkResult.SampleErrors {
				logf("verify-file: chunk=%d sample_error: %s", chunk.Index, sample)
			}
			return result, err
		}
		if chunkResult.Failed > 0 {
			logf("verify-file: chunk=%d recovered after=%s succeeded=%d failed=%d total=%d errors=%s", chunk.Index, time.Since(start).Round(time.Millisecond), chunkResult.Succeeded, chunkResult.Failed, chunkResult.TotalSymbols, formatErrorCounts(chunkResult.Errors))
			for _, sample := range chunkResult.SampleErrors {
				logf("verify-file: chunk=%d sample_error: %s", chunk.Index, sample)
			}
		} else {
			logf("verify-file: chunk=%d recovered after=%s succeeded=%d failed=%d total=%d", chunk.Index, time.Since(start).Round(time.Millisecond), chunkResult.Succeeded, chunkResult.Failed, chunkResult.TotalSymbols)
		}
		result.RecoveredChunks++
		if chunkResult.Failed > 0 {
			result.Warnings = true
		}
		if _, err := io.Copy(fileHash, recovered.reader()); err != nil {
			return result, err
		}
	}
	if !bytes.Equal(fileHash.Sum(nil), loaded.Root.FileSHA256) {
		return result, errors.New("verified chunks recovered but final file SHA-256 mismatch")
	}
	return result, nil
}

func loadManifest(ctx context.Context, client *http.Client, keyed string, fullErrorBody bool, stats *downloadCounter) (*loadedManifest, error) {
	cacheURL, master, manifestHash, err := splitKeyedURL(keyed)
	if err != nil {
		return nil, err
	}
	cKey := contentKey(master)
	pKey := pathKey(master)
	rootPayload, err := fetchAndDecodeFont(ctx, client, cacheURL, fullErrorBody, stats, "manifest")
	if err != nil {
		return nil, fmt.Errorf("fetch root manifest: %w", err)
	}
	if len(manifestHash) > 0 {
		got := sha256.Sum256(rootPayload)
		if !bytes.Equal(got[:], manifestHash) {
			return nil, errors.New("root manifest hash mismatch")
		}
	}
	rootPlain, err := decryptPayload(cKey, aadRoot(), rootPayload)
	if err != nil {
		return nil, fmt.Errorf("decrypt root manifest: %w", err)
	}
	root, err := decodeRootManifest(rootPlain)
	if err != nil {
		return nil, err
	}
	loaded := &loadedManifest{Root: root, Key: master, CKey: cKey, PKey: pKey}
	for _, ref := range root.Pages {
		pagePayload, err := fetchAndDecodeFont(ctx, client, ref.CacheURL, fullErrorBody, stats, "manifest")
		if err != nil {
			return nil, fmt.Errorf("fetch manifest page %d: %w", ref.Index, err)
		}
		pagePlain, err := decryptPayload(cKey, aadPage(root.RunID, ref.Index), pagePayload)
		if err != nil {
			return nil, fmt.Errorf("decrypt manifest page %d: %w", ref.Index, err)
		}
		page, err := decodePageManifest(pagePlain)
		if err != nil {
			return nil, err
		}
		if page.RunID != root.RunID || page.PageIndex != ref.Index {
			return nil, errors.New("manifest page identity mismatch")
		}
		if root.Version == legacyManifestVersion {
			for i := range page.Chunks {
				source := sourceSymbolCount(page.Chunks[i].Length, root.SymbolSize)
				total := totalSymbolCount(source, root.MinRecoverySymbols, root.FECTotalMillis)
				page.Chunks[i].SymbolRanges = symbolRangesForInitialUpload(total)
			}
		}
		loaded.Pages = append(loaded.Pages, page)
		loaded.Chunks = append(loaded.Chunks, page.Chunks...)
	}
	if uint64(len(loaded.Chunks)) != root.ChunkCount {
		return nil, fmt.Errorf("manifest has %d chunks, want %d", len(loaded.Chunks), root.ChunkCount)
	}
	return loaded, nil
}

type chunkRecovery struct {
	client          *http.Client
	loaded          *loadedManifest
	chunk           ChunkManifest
	decoder         *chunkDecoder
	result          ChunkVerifyResult
	pending         []uint64
	debugSymbolURLs bool
	stats           *downloadCounter
}

func newChunkRecovery(client *http.Client, loaded *loadedManifest, chunk ChunkManifest, debugSymbolURLs bool, stats *downloadCounter) (*chunkRecovery, error) {
	source := sourceSymbolCount(chunk.Length, loaded.Root.SymbolSize)
	ids, err := expandSymbolRanges(chunk.SymbolRanges)
	if err != nil {
		return nil, err
	}
	total := uint64(len(ids))
	result := ChunkVerifyResult{Index: chunk.Index, SourceSymbols: source, TotalSymbols: total, Errors: make(map[string]int)}
	decoder, err := newChunkDecoder(chunk.Length, int(loaded.Root.SymbolSize))
	if err != nil {
		return nil, err
	}
	if total == 0 {
		decoder.done = true
		decoder.recovered = nil
	}
	return &chunkRecovery{
		client:          client,
		loaded:          loaded,
		chunk:           chunk,
		decoder:         decoder,
		result:          result,
		pending:         append([]uint64(nil), ids...),
		debugSymbolURLs: debugSymbolURLs,
		stats:           stats,
	}, nil
}

func recoverChunk(ctx context.Context, client *http.Client, loaded *loadedManifest, chunk ChunkManifest, workers int, stopAfterRecovered bool, symbolRetries int, symbolRetryInterval time.Duration, debugSymbolURLs bool, stats *downloadCounter) (*chunkDecoder, ChunkVerifyResult, error) {
	recovery, err := newChunkRecovery(client, loaded, chunk, debugSymbolURLs, stats)
	if err != nil {
		source := sourceSymbolCount(chunk.Length, loaded.Root.SymbolSize)
		return nil, ChunkVerifyResult{Index: chunk.Index, SourceSymbols: source, Errors: make(map[string]int)}, err
	}
	return recovery.recover(ctx, workers, stopAfterRecovered, symbolRetries, symbolRetryInterval, nil)
}

func (r *chunkRecovery) recover(ctx context.Context, workers int, stopAfterRecovered bool, symbolRetries int, symbolRetryInterval time.Duration, logRetry func(round int, pending int)) (*chunkDecoder, ChunkVerifyResult, error) {
	unlimited := symbolRetries < 0
	for attempt := 0; len(r.pending) > 0; attempt++ {
		if err := r.fetchPendingOnce(ctx, workers, stopAfterRecovered); err != nil {
			return r.decoder, r.result, err
		}
		if r.decoder.done && stopAfterRecovered {
			break
		}
		if len(r.pending) > 0 {
			if !unlimited && attempt >= symbolRetries {
				break
			}
			if logRetry != nil {
				logRetry(attempt+1, len(r.pending))
			}
			if symbolRetryInterval > 0 {
				select {
				case <-ctx.Done():
					return r.decoder, r.result, ctx.Err()
				case <-time.After(symbolRetryInterval):
				}
			}
		}
	}
	return r.finish()
}

func (r *chunkRecovery) fetchPendingOnce(ctx context.Context, workers int, stopAfterRecovered bool) error {
	type symbolResult struct {
		id       uint64
		cacheURL string
		data     []byte
		err      error
	}
	roundCtx, cancel := context.WithCancel(ctx)
	jobs := make(chan uint64)
	results := make(chan symbolResult)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				data, cacheURL, err := fetchSymbol(roundCtx, r.client, r.loaded, r.chunk.Index, id, r.debugSymbolURLs, r.stats)
				select {
				case results <- symbolResult{id: id, cacheURL: cacheURL, data: data, err: err}:
				case <-roundCtx.Done():
					return
				}
			}
		}()
	}
	go func(ids []uint64) {
		defer close(jobs)
		for _, id := range ids {
			select {
			case jobs <- id:
			case <-roundCtx.Done():
				return
			}
		}
	}(r.pending)
	go func() {
		wg.Wait()
		close(results)
	}()

	next := make([]uint64, 0)
	for res := range results {
		r.result.Attempted++
		if res.err != nil {
			r.result.Failed++
			r.result.Errors[classifyErr(res.err)]++
			addSampleError(&r.result, res.id, res.cacheURL, res.err, r.debugSymbolURLs)
			if isRetryableSymbolError(res.err) {
				next = append(next, res.id)
			}
			continue
		}
		r.result.Succeeded++
		done, err := r.decoder.put(res.id, res.data)
		if err != nil {
			r.result.Failed++
			r.result.Errors[classifyErr(err)]++
			addSampleError(&r.result, res.id, res.cacheURL, err, r.debugSymbolURLs)
			continue
		}
		if done && stopAfterRecovered {
			cancel()
		}
	}
	cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.pending = next
	return nil
}

func (r *chunkRecovery) finish() (*chunkDecoder, ChunkVerifyResult, error) {
	if !r.decoder.done {
		return r.decoder, r.result, fmt.Errorf("chunk %d did not recover: successful=%d failed=%d total=%d", r.chunk.Index, r.result.Succeeded, r.result.Failed, r.result.TotalSymbols)
	}
	recovered, err := io.ReadAll(r.decoder.reader())
	if err != nil {
		return r.decoder, r.result, err
	}
	sum := sha256.Sum256(recovered)
	if !bytes.Equal(sum[:], r.chunk.SHA256) {
		return r.decoder, r.result, fmt.Errorf("chunk %d SHA-256 mismatch", r.chunk.Index)
	}
	r.result.Recovered = true
	return r.decoder, r.result, nil
}

func fetchSymbol(ctx context.Context, client *http.Client, loaded *loadedManifest, chunkIndex, symbolIndex uint64, fullErrorBody bool, stats *downloadCounter) ([]byte, string, error) {
	logical := symbolLogicalID(loaded.Root.RunID, chunkIndex, symbolIndex)
	token := pathToken(loaded.PKey, logical)
	originURL := resourceURLForToken(loaded.Root.ResourceBase, token)
	cacheURL, err := ampcache.CreateCacheURL(loaded.Root.CacheDomain, originURL, ampcache.EncodingFont)
	if err != nil {
		return nil, "", err
	}
	payload, err := fetchAndDecodeFont(ctx, client, cacheURL, fullErrorBody, stats, "symbol")
	if err != nil {
		return nil, cacheURL, err
	}
	plain, err := decryptPayload(loaded.CKey, aadSymbol(loaded.Root.RunID, chunkIndex, symbolIndex), payload)
	return plain, cacheURL, err
}

func fetchAndDecodeFont(ctx context.Context, client *http.Client, cacheURL string, fullErrorBody bool, stats *downloadCounter, kind string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cacheURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, retryableSymbolError{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, retryableSymbolError{err: err}
	}
	if resp.StatusCode != http.StatusOK {
		if stats != nil {
			stats.add(kind, uint64(len(body)), 0)
		}
		detail := errorBody(body, fullErrorBody)
		if detail != "" {
			return nil, retryableSymbolError{err: fmt.Errorf("cache returned %s: %s", resp.Status, detail)}
		}
		return nil, retryableSymbolError{err: fmt.Errorf("cache returned %s", resp.Status)}
	}
	payload, _, err := ampcache.DecodeResource(body, ampcache.EncodingFont)
	if err != nil {
		if stats != nil {
			stats.add(kind, uint64(len(body)), 0)
		}
		return nil, err
	}
	if stats != nil {
		stats.add(kind, uint64(len(body)), uint64(len(payload)))
	}
	return payload, nil
}

type retryableSymbolError struct {
	err error
}

func (e retryableSymbolError) Error() string {
	return e.err.Error()
}

func (e retryableSymbolError) Unwrap() error {
	return e.err
}

func (o *PublishOptions) withDefaults() error {
	if o.ServerURL == "" {
		return errors.New("server URL is required")
	}
	if o.InputPath == "" {
		return errors.New("input path is required")
	}
	if o.ResourceBase == "" {
		o.ResourceBase = o.ServerURL
	}
	if o.CacheDomain == "" {
		o.CacheDomain = ampcache.DefaultCacheDomain
	}
	if o.ChunkSize == 0 {
		o.ChunkSize = DefaultChunkSize
	}
	if o.SymbolSize == 0 {
		o.SymbolSize = DefaultSymbolSize
	}
	if o.MinRecoverySymbols == 0 {
		o.MinRecoverySymbols = DefaultMinRecoverySymbols
	}
	if o.FECTotalMillis == 0 {
		o.FECTotalMillis = DefaultFECTotalMillis
	}
	if o.PublishWorkers <= 0 {
		o.PublishWorkers = DefaultPublishWorkers
	}
	if o.VerifyWorkers <= 0 {
		o.VerifyWorkers = DefaultSymbolWorkers
	}
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	if o.UploadTimeout == 0 {
		o.UploadTimeout = 2 * time.Minute
	}
	if o.PollInterval == 0 {
		o.PollInterval = 500 * time.Millisecond
	}
	if o.VerifyTimeout == 0 {
		o.VerifyTimeout = 10 * time.Minute
	}
	if o.VerifyPollInterval == 0 {
		o.VerifyPollInterval = 2 * time.Second
	}
	if o.SymbolSize > uint64(math.MaxInt32) {
		return errors.New("symbol size is too large")
	}
	if o.ChunkSize < o.SymbolSize {
		return errors.New("chunk size must be at least symbol size")
	}
	if o.ChunkSize > uint64(math.MaxInt32)*o.SymbolSize {
		return errors.New("chunk size is too large")
	}
	return nil
}

func (o *RepairOptions) withDefaults() error {
	if o.ServerURL == "" {
		return errors.New("server URL is required")
	}
	if o.ManifestURL == "" {
		return errors.New("manifest URL is required")
	}
	if o.MinRecoverySymbols == 0 {
		o.MinRecoverySymbols = DefaultMinRecoverySymbols
	}
	if o.FECTotalMillis == 0 {
		o.FECTotalMillis = DefaultFECTotalMillis
	}
	if o.PublishWorkers <= 0 {
		o.PublishWorkers = DefaultPublishWorkers
	}
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	if o.UploadTimeout == 0 {
		o.UploadTimeout = 2 * time.Minute
	}
	if o.PollInterval == 0 {
		o.PollInterval = 500 * time.Millisecond
	}
	return nil
}

func publishOptionsFromRepair(opts RepairOptions) PublishOptions {
	return PublishOptions{
		ServerURL:      opts.ServerURL,
		ResourceBase:   opts.ResourceBase,
		CacheDomain:    opts.CacheDomain,
		PublishWorkers: opts.PublishWorkers,
		HTTPClient:     opts.HTTPClient,
		UploadTimeout:  opts.UploadTimeout,
		PollInterval:   opts.PollInterval,
		Logger:         opts.Logger,
	}
}

func cloneChunks(chunks []ChunkManifest) []ChunkManifest {
	out := make([]ChunkManifest, len(chunks))
	for i, chunk := range chunks {
		out[i] = chunk
		out[i].SHA256 = append([]byte(nil), chunk.SHA256...)
		out[i].SymbolRanges = append([]SymbolRange(nil), chunk.SymbolRanges...)
	}
	return out
}

func openVerifiedSource(path string, wantSize uint64, wantHash []byte) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("source path must be a regular file")
	}
	if uint64(info.Size()) != wantSize {
		return nil, fmt.Errorf("source file size = %d, want %d", info.Size(), wantSize)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	if !bytes.Equal(hash.Sum(nil), wantHash) {
		return nil, errors.New("source file SHA-256 mismatch")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	closeOnError = false
	return file, nil
}

func readSourceChunk(file *os.File, offset, length uint64) ([]byte, error) {
	if length > uint64(^uint(0)) {
		return nil, errors.New("chunk too large for this platform")
	}
	chunk := make([]byte, int(length))
	if _, err := file.ReadAt(chunk, int64(offset)); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return chunk, nil
}

func chooseRepairRange(existing []SymbolRange, count uint64) (SymbolRange, error) {
	if count == 0 {
		return SymbolRange{}, errors.New("repair symbol count must be greater than zero")
	}
	if count-1 > maxSymbolIndex {
		return SymbolRange{}, fmt.Errorf("repair symbol count %d exceeds max symbol index %d", count, uint64(maxSymbolIndex))
	}
	maxStart := uint64(maxSymbolIndex) - count + 1
	for attempt := 0; attempt < 256; attempt++ {
		start, err := randomUint64(maxStart + 1)
		if err != nil {
			return SymbolRange{}, err
		}
		candidate := SymbolRange{Start: start, End: start + count - 1}
		if candidate.End <= maxSymbolIndex && !rangeOverlaps(candidate, existing) {
			return candidate, nil
		}
	}
	return firstFreeRange(existing, count)
}

func randomUint64(bound uint64) (uint64, error) {
	if bound == 0 {
		return 0, errors.New("random bound must be greater than zero")
	}
	b, err := randomBytes(8)
	if err != nil {
		return 0, err
	}
	value := uint64(0)
	for _, c := range b {
		value = value<<8 | uint64(c)
	}
	return value % bound, nil
}

func firstFreeRange(existing []SymbolRange, count uint64) (SymbolRange, error) {
	ranges := append([]SymbolRange(nil), existing...)
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})
	start := uint64(0)
	for _, r := range ranges {
		if r.End > maxSymbolIndex {
			return SymbolRange{}, fmt.Errorf("existing symbol range end %d exceeds max %d", r.End, uint64(maxSymbolIndex))
		}
		if start+count-1 < r.Start {
			return SymbolRange{Start: start, End: start + count - 1}, nil
		}
		if r.End == maxSymbolIndex {
			return SymbolRange{}, errors.New("no free repair symbol range")
		}
		if r.End+1 > start {
			start = r.End + 1
		}
		if count-1 > uint64(maxSymbolIndex)-start {
			return SymbolRange{}, errors.New("no free repair symbol range")
		}
	}
	if count-1 > uint64(maxSymbolIndex)-start {
		return SymbolRange{}, errors.New("no free repair symbol range")
	}
	return SymbolRange{Start: start, End: start + count - 1}, nil
}

func rangeOverlaps(candidate SymbolRange, existing []SymbolRange) bool {
	for _, r := range existing {
		if candidate.Start <= r.End && r.Start <= candidate.End {
			return true
		}
	}
	return false
}

func resourceURLForToken(resourceBase, token string) string {
	u, err := url.Parse(resourceBase)
	if err != nil {
		return strings.TrimRight(resourceBase, "/") + "/" + token + ".ttf"
	}
	basePath := strings.TrimRight(u.Path, "/")
	u.Path = path.Join(basePath, token+".ttf")
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func validateExpectedCacheURL(cacheDomain, resourceURL, got string) error {
	want, err := ampcache.CreateCacheURL(cacheDomain, resourceURL, ampcache.EncodingFont)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("server returned cache URL %q, want %q; check -cache-domain", got, want)
	}
	return nil
}

func newRunID() string {
	token, err := randomPathToken()
	if err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + token[:16]
}

func ceilDiv(a, b uint64) uint64 {
	if a == 0 {
		return 0
	}
	return (a + b - 1) / b
}

func min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func ratio(numerator, denominator uint64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func positiveDiff(a, b uint64) uint64 {
	if a <= b {
		return 0
	}
	return a - b
}

func classifyErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "429"):
		return "rate_limited_429"
	case strings.Contains(msg, "404"):
		return "not_found_404"
	case strings.Contains(msg, "403"):
		return "forbidden_403"
	case strings.Contains(msg, "message authentication failed"):
		return "aead_failed"
	case strings.Contains(msg, "context deadline exceeded"):
		return "timeout"
	default:
		if len(msg) > 80 {
			return msg[:80]
		}
		return msg
	}
}

func isRetryableSymbolError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var retryable retryableSymbolError
	if errors.As(err, &retryable) {
		return true
	}
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return true
	case strings.Contains(msg, "429"),
		strings.Contains(msg, "404"),
		strings.Contains(msg, "408"),
		strings.Contains(msg, "500"),
		strings.Contains(msg, "502"),
		strings.Contains(msg, "503"),
		strings.Contains(msg, "504"):
		return true
	case strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "unexpected EOF"),
		strings.Contains(msg, "EOF"),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "temporary"):
		return true
	default:
		return false
	}
}

func addSampleError(result *ChunkVerifyResult, symbolIndex uint64, cacheURL string, err error, includeURL bool) {
	if len(result.SampleErrors) >= 8 {
		return
	}
	msg := fmt.Sprintf("symbol=%d err=%v", symbolIndex, err)
	if includeURL && cacheURL != "" {
		msg = fmt.Sprintf("symbol=%d cache_url=%s err=%v", symbolIndex, cacheURL, err)
	}
	if !includeURL && len(msg) > 600 {
		msg = msg[:600]
	}
	result.SampleErrors = append(result.SampleErrors, msg)
}

func formatErrorCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

func errorBody(body []byte, full bool) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	if full {
		if len(text) > 65536 {
			return text[:65536] + "\n[truncated]"
		}
		return text
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
