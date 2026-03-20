package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/xiaokangwang/fastTransfer/transfer"
)

func TestShouldSkipLocalFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	filePath := filepath.Join(root, "file.bin")
	if err := os.WriteFile(filePath, make([]byte, 8), 0o644); err != nil {
		t.Fatal(err)
	}

	skip, err := shouldSkipLocalFile(filePath, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !skip {
		t.Fatal("expected file with matching size to be skipped")
	}

	skip, err = shouldSkipLocalFile(filePath, 7)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("did not expect file with different size to be skipped")
	}
}

func TestInspectLocalFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	filePath := filepath.Join(root, "file.bin")
	if err := os.WriteFile(filePath, make([]byte, 8), 0o644); err != nil {
		t.Fatal(err)
	}

	decision, err := inspectLocalFile(filePath, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Exists || !decision.IsRegular || decision.LocalSize != 8 || !decision.Skip {
		t.Fatalf("unexpected matching decision: %+v", decision)
	}
	if decision.Reason != "local file size matches remote size" {
		t.Fatalf("unexpected matching reason: %q", decision.Reason)
	}

	decision, err = inspectLocalFile(filePath, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Exists || !decision.IsRegular || decision.LocalSize != 8 || decision.Skip {
		t.Fatalf("unexpected mismatched decision: %+v", decision)
	}
	if decision.Reason != "local file size differs from remote size" {
		t.Fatalf("unexpected mismatched reason: %q", decision.Reason)
	}
}

func TestShouldSkipLocalFileMissingOrDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	skip, err := shouldSkipLocalFile(filepath.Join(root, "missing.bin"), 8)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("missing file should not be skipped")
	}

	skip, err = shouldSkipLocalFile(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Fatal("directory should not be skipped as a file")
	}
}

func TestInspectLocalFileMissingOrDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	decision, err := inspectLocalFile(filepath.Join(root, "missing.bin"), 8)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Exists || decision.IsRegular || decision.LocalSize != 0 || decision.Skip {
		t.Fatalf("unexpected missing-file decision: %+v", decision)
	}
	if decision.Reason != "local file missing" {
		t.Fatalf("unexpected missing-file reason: %q", decision.Reason)
	}

	decision, err = inspectLocalFile(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Exists || decision.IsRegular || decision.Skip {
		t.Fatalf("unexpected directory decision: %+v", decision)
	}
	if decision.Reason != "local path is not a regular file" {
		t.Fatalf("unexpected directory reason: %q", decision.Reason)
	}
}

func TestNormalizedRecvRate(t *testing.T) {
	t.Parallel()

	if got := normalizedRecvRate(0); got != defaultClientRecvRate {
		t.Fatalf("normalizedRecvRate(0) = %d, want %d", got, defaultClientRecvRate)
	}
	if got := normalizedRecvRate(-1); got != defaultClientRecvRate {
		t.Fatalf("normalizedRecvRate(-1) = %d, want %d", got, defaultClientRecvRate)
	}
	if got := normalizedRecvRate(321); got != 321 {
		t.Fatalf("normalizedRecvRate(321) = %d, want 321", got)
	}
}

func TestShouldRetryFetch(t *testing.T) {
	t.Parallel()

	if !shouldRetryFetch(transfer.ErrTransferInterrupted) {
		t.Fatal("expected interrupted transfer to be retried")
	}
	if !shouldRetryFetch(syscall.ECONNREFUSED) {
		t.Fatal("expected connection refused to be retried")
	}
	if shouldRetryFetch(errors.New("remote file not found")) {
		t.Fatal("did not expect application error to be retried")
	}
}

func TestReportFileProgress(t *testing.T) {
	t.Parallel()

	originalWriter := clientProgressWriter
	defer func() {
		clientProgressWriter = originalWriter
	}()

	var buf bytes.Buffer
	clientProgressWriter = &buf
	reportFileProgress("/remote/file.bin", 2, 5)

	if got, want := buf.String(), "/remote/file.bin: 2/5 parts downloaded\n"; got != want {
		t.Fatalf("unexpected progress output: got %q want %q", got, want)
	}
}

func TestReportFileProgressClampsCompletedParts(t *testing.T) {
	t.Parallel()

	originalWriter := clientProgressWriter
	defer func() {
		clientProgressWriter = originalWriter
	}()

	var buf bytes.Buffer
	clientProgressWriter = &buf
	reportFileProgress("/remote/file.bin", 7, 5)

	if got, want := buf.String(), "/remote/file.bin: 5/5 parts downloaded\n"; got != want {
		t.Fatalf("unexpected progress output: got %q want %q", got, want)
	}
}

func TestFormatByteCount(t *testing.T) {
	t.Parallel()

	if got := formatByteCount(999); got != "999 B" {
		t.Fatalf("unexpected byte count for bytes: %q", got)
	}
	if got := formatByteCount(1536); got != "1.5 KiB" {
		t.Fatalf("unexpected byte count for kibibytes: %q", got)
	}
}

func TestFormatByteRate(t *testing.T) {
	t.Parallel()

	if got := formatByteRate(2048, time.Second); got != "2.0 KiB/s" {
		t.Fatalf("unexpected byte rate: %q", got)
	}
	if got := formatByteRate(2048, 0); got != "0 B/s" {
		t.Fatalf("unexpected byte rate for zero duration: %q", got)
	}
}

func TestReportPartTransferSpeed(t *testing.T) {
	t.Parallel()

	originalWriter := clientProgressWriter
	defer func() {
		clientProgressWriter = originalWriter
	}()

	var buf bytes.Buffer
	clientProgressWriter = &buf
	reportPartTransferSpeed("/remote/file.bin", 2, 5, 2048, time.Second)

	if got, want := buf.String(), "/remote/file.bin part 2/5: 2.0 KiB in 1s (2.0 KiB/s)\n"; got != want {
		t.Fatalf("unexpected part speed output: got %q want %q", got, want)
	}
}

func TestReportFileTransferSpeed(t *testing.T) {
	t.Parallel()

	originalWriter := clientProgressWriter
	defer func() {
		clientProgressWriter = originalWriter
	}()

	var buf bytes.Buffer
	clientProgressWriter = &buf
	reportFileTransferSpeed("/remote/file.bin", 3, 5, 6144, 2*time.Second)

	if got, want := buf.String(), "/remote/file.bin: 3/5 parts, 6.0 KiB in 2s (3.0 KiB/s)\n"; got != want {
		t.Fatalf("unexpected file speed output: got %q want %q", got, want)
	}
}

func TestRemotePathIsFileErrorMatches(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("%w: %s", errRemotePathIsFile, "/remote/file.bin")
	if !errors.Is(err, errRemotePathIsFile) {
		t.Fatalf("expected wrapped file error to match sentinel: %v", err)
	}
}

func TestResolveRecursiveRootFileOutputPathUsesDirectoryTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	got, err := resolveRecursiveRootFileOutputPath("/remote/path/file.bin", root)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(root, "file.bin")
	if got != want {
		t.Fatalf("unexpected resolved path: got %q want %q", got, want)
	}
}

func TestResolveRecursiveRootFileOutputPathUsesExplicitFileTarget(t *testing.T) {
	t.Parallel()

	got, err := resolveRecursiveRootFileOutputPath("/remote/path/file.bin", "/tmp/output.bin")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/output.bin" {
		t.Fatalf("unexpected resolved path: got %q want %q", got, "/tmp/output.bin")
	}
}

func TestMatchesRecursiveDownloadFilter(t *testing.T) {
	t.Parallel()

	filter := regexp.MustCompile(`/wanted/.*\.mkv$`)
	if !matchesRecursiveDownloadFilter("/remote/wanted/movie.mkv", filter) {
		t.Fatal("expected matching file path to pass filter")
	}
	if matchesRecursiveDownloadFilter("/remote/wanted/movie.srt", filter) {
		t.Fatal("did not expect non-matching file path to pass filter")
	}
	if !matchesRecursiveDownloadFilter("/remote/anything.bin", nil) {
		t.Fatal("expected nil filter to allow all files")
	}
}

func TestEnsureRecursiveDirectoryCreatesWithoutFilter(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "a", "b")
	if err := ensureRecursiveDirectory(target, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", target)
	}
}

func TestEnsureRecursiveDirectorySkipsWhenFilterSet(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "a", "b")
	filter := regexp.MustCompile(`\.mkv$`)
	if err := ensureRecursiveDirectory(target, filter); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected %q to not be created, got err=%v", target, err)
	}
}
