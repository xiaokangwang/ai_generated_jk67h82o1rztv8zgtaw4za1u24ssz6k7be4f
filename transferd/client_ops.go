package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pion/dtls/v3"

	"github.com/xiaokangwang/VLite/transport/udp/udpClient"
	"github.com/xiaokangwang/fastTransfer/transfer"
)

const defaultClientRecvRate = 1000

const (
	defaultFetchRetryDelay = 2 * time.Second
)

var errRemotePathIsFile = errors.New("remote path is a file")

var nextTransferID uint64
var clientProgressWriter io.Writer = os.Stderr

type localFileDecision struct {
	Exists    bool
	IsRegular bool
	LocalSize int64
	Skip      bool
	Reason    string
}

type remoteSession struct {
	conn    net.Conn
	conn2   io.ReadWriteCloser
	ctx     context.Context
	address string
}

func normalizedRecvRate(recvRate int) int {
	if recvRate <= 0 {
		return defaultClientRecvRate
	}
	return recvRate
}

func fetchRemote(address string, request transfer.Request, recvRate int, output io.Writer) (uint32, uint8, error) {
	var session *remoteSession
	defer closeRemoteSession(session)
	return fetchRemoteWithSession(address, &session, request, recvRate, output)
}

func fetchRemoteWithSession(address string, session **remoteSession, request transfer.Request, recvRate int, output io.Writer) (uint32, uint8, error) {
	effectiveRecvRate := normalizedRecvRate(recvRate)
	if request.TransferID == 0 {
		request.TransferID = atomic.AddUint64(&nextTransferID, 1)
	}
	for {
		if *session == nil {
			nextSession, err := newRemoteSession(address)
			if err != nil {
				if !shouldRetryFetch(err) {
					return 0, 0, err
				}
				time.Sleep(defaultFetchRetryDelay)
				continue
			}
			*session = nextSession
		}

		totalParts, payloadType, err := (*session).fetch(request, effectiveRecvRate, output)
		if err == nil {
			return totalParts, payloadType, nil
		}
		if !shouldRetryFetch(err) {
			return 0, 0, err
		}
		closeRemoteSession(*session)
		*session = nil
		time.Sleep(defaultFetchRetryDelay)
	}
}

func newRemoteSession(address string) (*remoteSession, error) {
	udpClient := udpClient.NewUdpClient(address, context.TODO())
	conn, err, ctx := udpClient.Connect(context.TODO())
	if err != nil {
		return nil, err
	}

	packetConn := newPacketConnAdapter(conn, conn.LocalAddr(), conn.RemoteAddr())
	conn2, err := dtls.Client(packetConn, conn.RemoteAddr(), newClientDTLSConfig())
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := completeDTLSHandshake(ctx, conn2); err != nil {
		_ = conn2.Close()
		_ = conn.Close()
		return nil, err
	}

	return &remoteSession{
		conn:    conn,
		conn2:   conn2,
		ctx:     ctx,
		address: address,
	}, nil
}

func (s *remoteSession) fetch(request transfer.Request, recvRate int, output io.Writer) (uint32, uint8, error) {
	client, err := transfer.NewClient(s.ctx, s.conn2, NewFECEngine(), request, uint32(recvRate), output)
	if err != nil {
		return 0, 0, err
	}
	if err := client.Done(); err != nil {
		return 0, 0, err
	}

	payloadType := client.GetPayloadType()
	if payloadType == 0 {
		return 0, 0, fmt.Errorf("server closed before responding")
	}

	return client.GetTotalParts(), payloadType, nil
}

func closeRemoteSession(session *remoteSession) {
	if session == nil {
		return
	}
	if session.conn2 != nil {
		_ = session.conn2.Close()
	}
	if session.conn != nil {
		_ = session.conn.Close()
	}
}

func shouldRetryFetch(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, transfer.ErrTransferInterrupted) {
		return true
	}
	return isRetryableNetworkError(err)
}

func isRetryableNetworkError(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, io.EOF),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.ETIMEDOUT),
		errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, syscall.ENETUNREACH):
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection refused") ||
		strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "conn is closed") ||
		strings.Contains(message, "server closed before responding")
}

func fetchListing(address, remotePath string, recvRate int) (*transfer.Listing, error) {
	var session *remoteSession
	defer closeRemoteSession(session)
	return fetchListingWithSession(address, &session, remotePath, recvRate)
}

func fetchListingWithSession(address string, session **remoteSession, remotePath string, recvRate int) (*transfer.Listing, error) {
	var buf bytes.Buffer
	_, payloadType, err := fetchRemoteWithSession(address, session, transfer.Request{
		Path: remotePath,
	}, recvRate, &buf)
	if err != nil {
		return nil, err
	}
	if payloadType == transfer.PayloadTypeFile {
		return nil, fmt.Errorf("%w: %s", errRemotePathIsFile, remotePath)
	}
	if payloadType != transfer.PayloadTypeList {
		return nil, fmt.Errorf("unexpected payload type %d for %s", payloadType, remotePath)
	}

	return transfer.DecodeListing(buf.Bytes())
}

func reportFileProgress(remotePath string, completedParts, totalParts uint32) {
	if totalParts == 0 {
		return
	}
	if completedParts > totalParts {
		completedParts = totalParts
	}
	fmt.Fprintf(clientProgressWriter, "%s: %d/%d parts downloaded\n", remotePath, completedParts, totalParts)
}

func reportRecursiveFilterDecision(remotePath string, filter *regexp.Regexp, matched bool) {
	if filter == nil {
		return
	}
	fmt.Fprintf(
		clientProgressWriter,
		"filter check: remote=%q filter=%q matched=%t\n",
		remotePath,
		filter.String(),
		matched,
	)
}

func reportRecursiveResumeDecision(remotePath, localPath string, remoteSize int64, decision localFileDecision) {
	fmt.Fprintf(
		clientProgressWriter,
		"resume check: remote=%q local=%q remote_size=%d exists=%t regular=%t local_size=%d skip=%t reason=%s\n",
		remotePath,
		localPath,
		remoteSize,
		decision.Exists,
		decision.IsRegular,
		decision.LocalSize,
		decision.Skip,
		decision.Reason,
	)
}

func reportRecursiveDownloadAction(remotePath, localPath, reason string) {
	fmt.Fprintf(
		clientProgressWriter,
		"download action: remote=%q local=%q action=%s\n",
		remotePath,
		localPath,
		reason,
	)
}

func reportRecursivePlan(remotePath, outputPath string, resume bool, filter *regexp.Regexp) {
	filterValue := "<nil>"
	if filter != nil {
		filterValue = filter.String()
	}
	fmt.Fprintf(
		clientProgressWriter,
		"recursive plan: remote_root=%q local_root=%q resume=%t filter=%q\n",
		remotePath,
		outputPath,
		resume,
		filterValue,
	)
}

func downloadRemoteFileParts(address, outputPath, remotePath string, recvRate int, fromPart uint32) error {
	var session *remoteSession
	defer closeRemoteSession(session)

	currentPart := fromPart
	for {
		partOutputPath := outputPath
		if currentPart != 0 {
			partOutputPath += fmt.Sprintf(".%08d", currentPart)
		}

		if err := os.MkdirAll(filepath.Dir(partOutputPath), 0o755); err != nil {
			return err
		}
		file, err := os.Create(partOutputPath)
		if err != nil {
			return err
		}

		totalParts, payloadType, fetchErr := fetchRemoteWithSession(address, &session, transfer.Request{
			Path:     remotePath,
			FilePart: currentPart,
		}, recvRate, file)
		closeErr := file.Close()
		if fetchErr != nil {
			_ = os.Remove(partOutputPath)
			return fetchErr
		}
		if payloadType != transfer.PayloadTypeFile {
			_ = os.Remove(partOutputPath)
			return fmt.Errorf("%s is a directory; use -list or -recursive", remotePath)
		}
		if closeErr != nil {
			return closeErr
		}
		reportFileProgress(remotePath, currentPart+1, totalParts)

		if int(totalParts) <= int(currentPart)+1 {
			return nil
		}
		currentPart++
	}
}

func downloadRemoteFileAsSingleFile(address, outputPath, remotePath string, recvRate int) error {
	var session *remoteSession
	defer closeRemoteSession(session)
	return downloadRemoteFileAsSingleFileWithSession(address, &session, outputPath, remotePath, recvRate)
}

func downloadRemoteFileAsSingleFileWithSession(address string, session **remoteSession, outputPath, remotePath string, recvRate int) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}

	file, tempPath, err := createTempOutputFile(outputPath)
	if err != nil {
		return err
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()

	var currentPart uint32
	for {
		totalParts, payloadType, err := fetchRemoteWithSession(address, session, transfer.Request{
			Path:     remotePath,
			FilePart: currentPart,
		}, recvRate, file)
		if err != nil {
			return err
		}
		if payloadType != transfer.PayloadTypeFile {
			return fmt.Errorf("%s is a directory; use -list or -recursive", remotePath)
		}
		reportFileProgress(remotePath, currentPart+1, totalParts)
		if int(totalParts) <= int(currentPart)+1 {
			break
		}
		currentPart++
	}

	if err := file.Close(); err != nil {
		return err
	}
	file = nil

	if err := os.Rename(tempPath, outputPath); err != nil {
		return err
	}
	tempPath = ""
	return nil
}

func downloadRemoteRecursive(address, remotePath, outputPath string, recvRate int, resume bool, filter *regexp.Regexp) error {
	var session *remoteSession
	defer closeRemoteSession(session)
	return downloadRemoteRecursiveWithSession(address, &session, remotePath, outputPath, recvRate, resume, filter)
}

func downloadRemoteRecursiveWithSession(address string, session **remoteSession, remotePath, outputPath string, recvRate int, resume bool, filter *regexp.Regexp) error {
	listing, err := fetchListingWithSession(address, session, remotePath, recvRate)
	if err != nil {
		if errors.Is(err, errRemotePathIsFile) {
			return downloadRecursiveRootFileWithSession(address, session, remotePath, outputPath, recvRate, resume)
		}
		return err
	}

	rootOutput := defaultOutputPath(remotePath, outputPath)
	reportRecursivePlan(remotePath, rootOutput, resume, filter)
	if !listing.RootIsDir {
		if resume {
			decision, err := inspectLocalFile(rootOutput, listing.RootSize)
			if err != nil {
				return err
			}
			reportRecursiveResumeDecision(remotePath, rootOutput, listing.RootSize, decision)
			if decision.Skip {
				return nil
			}
			reportRecursiveDownloadAction(remotePath, rootOutput, "download-resume-size-mismatch")
		} else {
			reportRecursiveDownloadAction(remotePath, rootOutput, "download-resume-disabled")
		}
		return downloadRemoteFileAsSingleFile(address, rootOutput, remotePath, recvRate)
	}

	if err := ensureRecursiveDirectory(rootOutput, filter); err != nil {
		return err
	}

	return downloadRemoteRecursiveInto(address, session, remotePath, rootOutput, listing, recvRate, resume, filter)
}

func downloadRemoteRecursiveInto(address string, session **remoteSession, remotePath, outputPath string, listing *transfer.Listing, recvRate int, resume bool, filter *regexp.Regexp) error {
	for _, entry := range listing.Entries {
		localPath := filepath.Join(outputPath, filepath.FromSlash(entry.Path))
		if entry.IsDir {
			childRemotePath := remotePathJoin(remotePath, entry.Path)
			childListing, err := fetchListingWithSession(address, session, childRemotePath, recvRate)
			if err != nil {
				return err
			}
			if err := ensureRecursiveDirectory(localPath, filter); err != nil {
				return err
			}
			if err := downloadRemoteRecursiveInto(address, session, childRemotePath, localPath, childListing, recvRate, resume, filter); err != nil {
				return err
			}
			continue
		}

		remoteFilePath := remotePathJoin(remotePath, entry.Path)
		filterMatched := matchesRecursiveDownloadFilter(remoteFilePath, filter)
		reportRecursiveFilterDecision(remoteFilePath, filter, filterMatched)
		if !filterMatched {
			reportRecursiveDownloadAction(remoteFilePath, localPath, "skip-filter-mismatch")
			continue
		}

		if resume {
			decision, err := inspectLocalFile(localPath, entry.Size)
			if err != nil {
				return err
			}
			reportRecursiveResumeDecision(remoteFilePath, localPath, entry.Size, decision)
			if decision.Skip {
				reportRecursiveDownloadAction(remoteFilePath, localPath, "skip-resume-size-match")
				continue
			}
			reportRecursiveDownloadAction(remoteFilePath, localPath, "download-resume-size-mismatch")
		} else {
			reportRecursiveDownloadAction(remoteFilePath, localPath, "download-resume-disabled")
		}
		if err := downloadRemoteFileAsSingleFileWithSession(address, session, localPath, remoteFilePath, recvRate); err != nil {
			return err
		}
	}

	return nil
}

func matchesRecursiveDownloadFilter(remotePath string, filter *regexp.Regexp) bool {
	if filter == nil {
		return true
	}
	return filter.MatchString(remotePath)
}

func ensureRecursiveDirectory(path string, filter *regexp.Regexp) error {
	if filter != nil {
		return nil
	}
	return os.MkdirAll(path, 0o755)
}

func downloadRecursiveRootFile(address, remotePath, outputPath string, recvRate int, resume bool) error {
	var session *remoteSession
	defer closeRemoteSession(session)
	return downloadRecursiveRootFileWithSession(address, &session, remotePath, outputPath, recvRate, resume)
}

func downloadRecursiveRootFileWithSession(address string, session **remoteSession, remotePath, outputPath string, recvRate int, resume bool) error {
	targetPath, err := resolveRecursiveRootFileOutputPath(remotePath, outputPath)
	if err != nil {
		return err
	}
	if resume {
		parentPath := pathpkg.Dir(remotePath)
		listing, err := fetchListingWithSession(address, session, parentPath, recvRate)
		if err == nil {
			for _, entry := range listing.Entries {
				if entry.IsDir {
					continue
				}
				if remotePathJoin(parentPath, entry.Path) != remotePath {
					continue
				}
				decision, err := inspectLocalFile(targetPath, entry.Size)
				if err != nil {
					return err
				}
				reportRecursiveResumeDecision(remotePath, targetPath, entry.Size, decision)
				if decision.Skip {
					return nil
				}
				reportRecursiveDownloadAction(remotePath, targetPath, "download-resume-size-mismatch")
				break
			}
		}
	} else {
		reportRecursiveDownloadAction(remotePath, targetPath, "download-resume-disabled")
	}
	return downloadRemoteFileAsSingleFileWithSession(address, session, targetPath, remotePath, recvRate)
}

func printListing(address string, listing *transfer.Listing, recursive bool, recvRate int) error {
	var session *remoteSession
	defer closeRemoteSession(session)
	return printListingWithPrefix(address, &session, listing, recursive, recvRate, "")
}

func printListingWithPrefix(address string, session **remoteSession, listing *transfer.Listing, recursive bool, recvRate int, prefix string) error {
	if !listing.RootIsDir {
		fmt.Printf("- %s (%d bytes)\n", listing.RequestedPath, listing.RootSize)
		return nil
	}

	for _, entry := range listing.Entries {
		displayPath := entry.Path
		if prefix != "" {
			displayPath = remotePathJoin(prefix, entry.Path)
		}
		if entry.IsDir {
			fmt.Printf("d %s/\n", displayPath)
			if recursive {
				childRemotePath := remotePathJoin(listing.RequestedPath, entry.Path)
				childListing, err := fetchListingWithSession(address, session, childRemotePath, recvRate)
				if err != nil {
					return err
				}
				if err := printListingWithPrefix(address, session, childListing, true, recvRate, displayPath); err != nil {
					return err
				}
			}
			continue
		}
		fmt.Printf("- %s (%d bytes)\n", displayPath, entry.Size)
	}
	return nil
}

func defaultOutputPath(remotePath, outputPath string) string {
	if outputPath != "" {
		return outputPath
	}

	return defaultRemoteBaseName(remotePath)
}

func defaultRemoteBaseName(remotePath string) string {
	base := filepath.Base(filepath.Clean(remotePath))
	if base == "." || base == string(os.PathSeparator) || base == "" {
		return "download"
	}
	return base
}

func resolveRecursiveRootFileOutputPath(remotePath, outputPath string) (string, error) {
	if outputPath == "" {
		return defaultRemoteBaseName(remotePath), nil
	}

	if info, err := os.Stat(outputPath); err == nil {
		if info.IsDir() {
			return filepath.Join(outputPath, defaultRemoteBaseName(remotePath)), nil
		}
		return outputPath, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if strings.HasSuffix(outputPath, string(os.PathSeparator)) {
		return filepath.Join(outputPath, defaultRemoteBaseName(remotePath)), nil
	}

	return outputPath, nil
}

func getLocalUDPAddr(address string) (net.Addr, error) {
	return net.ResolveUDPAddr("udp", address)
}

func remotePathJoin(basePath, child string) string {
	return pathpkg.Join(basePath, filepath.ToSlash(child))
}

func inspectLocalFile(path string, remoteSize int64) (localFileDecision, error) {
	decision := localFileDecision{
		Reason: "local file missing",
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return decision, nil
		}
		return localFileDecision{}, err
	}
	decision.Exists = true
	decision.IsRegular = info.Mode().IsRegular()
	decision.LocalSize = info.Size()
	if !decision.IsRegular {
		decision.Reason = "local path is not a regular file"
		return decision, nil
	}
	if info.Size() == remoteSize {
		decision.Skip = true
		decision.Reason = "local file size matches remote size"
		return decision, nil
	}
	decision.Reason = "local file size differs from remote size"
	return decision, nil
}

func shouldSkipLocalFile(path string, remoteSize int64) (bool, error) {
	decision, err := inspectLocalFile(path, remoteSize)
	if err != nil {
		return false, err
	}
	return decision.Skip, nil
}

func createTempOutputFile(outputPath string) (*os.File, string, error) {
	tempFile, err := os.CreateTemp(filepath.Dir(outputPath), "."+filepath.Base(outputPath)+".tmp-*")
	if err != nil {
		return nil, "", err
	}
	return tempFile, tempFile.Name(), nil
}
