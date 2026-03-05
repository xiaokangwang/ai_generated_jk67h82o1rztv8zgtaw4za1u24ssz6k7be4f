package main

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	clientTSRe    = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})\s`)
	torTSRe       = regexp.MustCompile(`^([A-Z][a-z]{2}\s+\d{2}\s+\d{2}:\d{2}:\d{2}\.\d{3})\s`)
	bootstrapRe   = regexp.MustCompile(`Bootstrapped\s+(\d+)%`)
	peerConnectRe = regexp.MustCompile(`\b(snowflake-[0-9a-f]+)\s+connecting\.\.\.`)
	rportRe       = regexp.MustCompile(`\brport\s+(\d+)`)
)

type EvidenceEntry struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type Attempt struct {
	PeerID                   string                   `json:"peer_id"`
	StartLine                int                      `json:"start_line"`
	EndLine                  int                      `json:"end_line"`
	StartTS                  *float64                 `json:"start_ts"`
	EndTS                    *float64                 `json:"end_ts"`
	NATTypeActual            *string                  `json:"nat_type_actual"`
	NATTypeSent              *string                  `json:"nat_type_sent"`
	LocalPorts               []int                    `json:"local_ports"`
	RemoteCandidateEndpoints []string                 `json:"remote_candidate_endpoints"`
	PrimaryLocalPort         *int                     `json:"primary_local_port"`
	HasSignalingContact      bool                     `json:"has_signaling_contact"`
	HasRemoteSignaling       bool                     `json:"has_remote_signaling"`
	BrokerNoAnswer           bool                     `json:"broker_no_answer"`
	DatachannelTimeout       bool                     `json:"datachannel_timeout"`
	DatachannelSuccess       bool                     `json:"datachannel_success"`
	StagesSeen               []string                 `json:"stages_seen"`
	Evidence                 map[string]EvidenceEntry `json:"evidence"`
	Errors                   []string                 `json:"errors"`

	Seq           int      `json:"seq,omitempty"`
	SourceFile    string   `json:"source_file,omitempty"`
	AttemptID     string   `json:"attempt_id,omitempty"`
	NextStartLine *int     `json:"next_start_line,omitempty"`
	NextStartTS   *float64 `json:"next_start_ts,omitempty"`
}

type ProbeDoc struct {
	Probe           string    `json:"probe"`
	ClientLog       string    `json:"client_log"`
	TorLog          string    `json:"tor_log"`
	Pcap            string    `json:"pcap"`
	TcpdumpErr      string    `json:"tcpdump_err"`
	ClientLineCount int       `json:"client_line_count"`
	Year            int       `json:"year"`
	AttemptCount    int       `json:"attempt_count"`
	Attempts        []Attempt `json:"attempts"`
}

type IndexDoc struct {
	GeneratedAt  string     `json:"generated_at"`
	InputDir     string     `json:"input_dir"`
	ProbeCount   int        `json:"probe_count"`
	AttemptCount int        `json:"attempt_count"`
	Probes       []ProbeDoc `json:"probes"`
}

type PcapStats struct {
	PacketTotal                  int    `json:"packet_total"`
	DTLSInboundPackets           int    `json:"dtls_inbound_packets"`
	DTLSOutboundPackets          int    `json:"dtls_outbound_packets"`
	STUNInboundPackets           int    `json:"stun_inbound_packets"`
	STUNOutboundPackets          int    `json:"stun_outbound_packets"`
	RemotePeerSTUNInboundPackets int    `json:"remote_peer_stun_inbound_packets"`
	PrimaryLocalIP               string `json:"primary_local_ip,omitempty"`
	PrimaryLocalPortObserved     int    `json:"primary_local_port_observed,omitempty"`
	PrimaryRemoteIP              string `json:"primary_remote_ip,omitempty"`
	PrimaryRemotePort            int    `json:"primary_remote_port,omitempty"`
	InboundPackets               int    `json:"inbound_packets"`
	OutboundPackets              int    `json:"outbound_packets"`
	FirstDTLSInboundTS           string `json:"first_dtls_inbound_ts,omitempty"`
	FirstDTLSOutboundTS          string `json:"first_dtls_outbound_ts,omitempty"`
	FirstRemotePeerSTUNInboundTS string `json:"first_remote_peer_stun_inbound_ts,omitempty"`
}

type TorInfo struct {
	TorConnected        bool `json:"tor_connected"`
	TorConnectedLine    int  `json:"tor_connected_line,omitempty"`
	TorBootstrapMaxPct  int  `json:"tor_bootstrap_max_percent"`
	TorBootstrap100     bool `json:"tor_bootstrap_100"`
	TorBootstrap100Line int  `json:"tor_bootstrap_100_line,omitempty"`
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func parseClientLineEpoch(line string) *float64 {
	match := clientTSRe.FindStringSubmatch(line)
	if len(match) < 2 {
		return nil
	}
	t, err := time.ParseInLocation("2006/01/02 15:04:05", match[1], time.UTC)
	if err != nil {
		return nil
	}
	v := float64(t.Unix())
	return &v
}

func parseTorLineEpoch(line string, defaultYear int) *float64 {
	match := torTSRe.FindStringSubmatch(line)
	if len(match) < 2 {
		return nil
	}
	raw := fmt.Sprintf("%d %s", defaultYear, match[1])
	t, err := time.ParseInLocation("2006 Jan 02 15:04:05.000", raw, time.UTC)
	if err != nil {
		return nil
	}
	v := float64(t.UnixNano()) / 1e9
	return &v
}

func parseISOEpoch(raw string) *float64 {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	v := float64(t.UnixNano()) / 1e9
	return &v
}

func toISO(ts *float64) string {
	if ts == nil {
		return ""
	}
	sec := int64(*ts)
	nsec := int64((*ts - float64(sec)) * 1e9)
	if nsec < 0 {
		nsec = 0
	}
	return time.Unix(sec, nsec).UTC().Format(time.RFC3339Nano)
}

func mergeUniquePorts(target []int, incoming []int) []int {
	seen := map[int]struct{}{}
	for _, p := range target {
		seen[p] = struct{}{}
	}
	for _, p := range incoming {
		if _, ok := seen[p]; !ok {
			target = append(target, p)
			seen[p] = struct{}{}
		}
	}
	return target
}

func mergeUniqueStrings(target []string, incoming []string) []string {
	seen := map[string]struct{}{}
	for _, s := range target {
		seen[s] = struct{}{}
	}
	for _, s := range incoming {
		if _, ok := seen[s]; !ok {
			target = append(target, s)
			seen[s] = struct{}{}
		}
	}
	return target
}

func endpointKey(addr string, port int) string {
	return addr + "|" + strconv.Itoa(port)
}

func parseEndpointKey(key string) (string, int, bool) {
	parts := strings.Split(key, "|")
	if len(parts) < 2 {
		return "", 0, false
	}
	addr := strings.Join(parts[:len(parts)-1], "|")
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return "", 0, false
	}
	return addr, port, true
}

func extractLocalPortsFromSDP(sdp string) []int {
	matches := rportRe.FindAllStringSubmatch(sdp, -1)
	if len(matches) > 0 {
		seen := map[int]struct{}{}
		ports := make([]int, 0, len(matches))
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			port, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			if _, ok := seen[port]; ok {
				continue
			}
			seen[port] = struct{}{}
			ports = append(ports, port)
		}
		return ports
	}

	lines := strings.Split(strings.ReplaceAll(sdp, "\r", ""), "\n")
	seen := map[int]struct{}{}
	ports := []int{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 6 {
			continue
		}
		port, err := strconv.Atoi(parts[5])
		if err != nil {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	return ports
}

func extractCandidateEndpointsFromSDP(sdp string) []string {
	lines := strings.Split(strings.ReplaceAll(sdp, "\r", ""), "\n")
	seen := map[string]struct{}{}
	endpoints := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 6 {
			continue
		}
		addr := strings.TrimSpace(parts[4])
		port, err := strconv.Atoi(parts[5])
		if err != nil || addr == "" {
			continue
		}
		key := endpointKey(addr, port)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		endpoints = append(endpoints, key)
	}
	return endpoints
}

func trimEvidenceText(line string) string {
	text := strings.TrimSpace(line)
	if len(text) > 400 {
		return text[:400]
	}
	return text
}

func noteEvidence(attempt *Attempt, key string, lineNo int, lineText string) {
	if attempt.Evidence == nil {
		attempt.Evidence = map[string]EvidenceEntry{}
	}
	if _, ok := attempt.Evidence[key]; ok {
		return
	}
	attempt.Evidence[key] = EvidenceEntry{Line: lineNo, Text: trimEvidenceText(lineText)}
}

func newAttempt(peerID string, lineNo int, ts *float64) Attempt {
	var startTS *float64
	var endTS *float64
	if ts != nil {
		v1 := *ts
		v2 := *ts
		startTS = &v1
		endTS = &v2
	}
	return Attempt{
		PeerID:                   peerID,
		StartLine:                lineNo,
		EndLine:                  lineNo,
		StartTS:                  startTS,
		EndTS:                    endTS,
		LocalPorts:               []int{},
		RemoteCandidateEndpoints: []string{},
		StagesSeen:               []string{},
		Evidence:                 map[string]EvidenceEntry{},
		Errors:                   []string{},
		HasSignalingContact:      false,
		HasRemoteSignaling:       false,
		BrokerNoAnswer:           false,
		DatachannelTimeout:       false,
		DatachannelSuccess:       false,
	}
}

func updateTSBounds(attempt *Attempt, ts *float64) {
	if ts == nil {
		return
	}
	if attempt.StartTS == nil || *ts < *attempt.StartTS {
		v := *ts
		attempt.StartTS = &v
	}
	if attempt.EndTS == nil || *ts > *attempt.EndTS {
		v := *ts
		attempt.EndTS = &v
	}
}

func getStringFromAny(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

func getMapFromAny(v interface{}) map[string]interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		return t
	default:
		return nil
	}
}

func parseClientAttempts(clientLog string) ([]Attempt, int, int, error) {
	file, err := os.Open(clientLog)
	if err != nil {
		return nil, 0, 1970, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)

	attemptsByPeer := map[string]*Attempt{}
	peerOrder := []string{}
	lastPeerID := ""
	lineCount := 0
	year := 1970

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()
		lineTS := parseClientLineEpoch(line)
		if lineCount == 1 && lineTS != nil {
			year = time.Unix(int64(*lineTS), 0).UTC().Year()
		}

		connectMatch := peerConnectRe.FindStringSubmatch(line)
		if len(connectMatch) > 1 {
			peerID := connectMatch[1]
			attempt := attemptsByPeer[peerID]
			if attempt == nil {
				created := newAttempt(peerID, lineCount, lineTS)
				attemptsByPeer[peerID] = &created
				attempt = &created
				peerOrder = append(peerOrder, peerID)
			}
			updateTSBounds(attempt, lineTS)
			attempt.EndLine = lineCount
			noteEvidence(attempt, "peer_connecting", lineCount, line)
			lastPeerID = peerID
		}

		marker := "WebRTC_DIAG "
		if idx := strings.Index(line, marker); idx >= 0 {
			payload := strings.TrimSpace(line[idx+len(marker):])
			diag := map[string]interface{}{}
			if err := json.Unmarshal([]byte(payload), &diag); err != nil {
				if lastPeerID != "" && attemptsByPeer[lastPeerID] != nil {
					attemptsByPeer[lastPeerID].Errors = append(
						attemptsByPeer[lastPeerID].Errors,
						fmt.Sprintf("line %d: failed to parse WebRTC_DIAG json", lineCount),
					)
				}
			} else {
				peerID := getStringFromAny(diag["peer_id"])
				if peerID == "" {
					peerID = lastPeerID
				}
				if peerID != "" {
					attempt := attemptsByPeer[peerID]
					diagTS := parseISOEpoch(getStringFromAny(diag["timestamp"]))
					eventTS := diagTS
					if eventTS == nil {
						eventTS = lineTS
					}
					if attempt == nil {
						created := newAttempt(peerID, lineCount, eventTS)
						attemptsByPeer[peerID] = &created
						attempt = &created
						peerOrder = append(peerOrder, peerID)
					}
					updateTSBounds(attempt, eventTS)
					attempt.EndLine = lineCount
					lastPeerID = peerID

					stage := getStringFromAny(diag["stage"])
					if stage != "" {
						attempt.StagesSeen = append(attempt.StagesSeen, stage)
						noteEvidence(attempt, "stage:"+stage, lineCount, line)
						if stage == "broker.negotiate.complete" {
							attempt.HasSignalingContact = true
						}
						if stage == "data_channel.wait_open.timeout" {
							attempt.DatachannelTimeout = true
						}
						if stage == "data_channel.wait_open.succeeded" {
							attempt.DatachannelSuccess = true
						}
					}

					natActual := getStringFromAny(diag["nat_type_actual"])
					if natActual != "" {
						copyVal := natActual
						attempt.NATTypeActual = &copyVal
					}
					natSent := getStringFromAny(diag["nat_type_sent"])
					if natSent != "" {
						copyVal := natSent
						attempt.NATTypeSent = &copyVal
					}

					localDesc := getMapFromAny(diag["local_description"])
					if localDesc != nil {
						sdp := getStringFromAny(localDesc["sdp"])
						if sdp != "" {
							ports := extractLocalPortsFromSDP(sdp)
							if len(ports) > 0 {
								attempt.LocalPorts = mergeUniquePorts(attempt.LocalPorts, ports)
								if len(attempt.LocalPorts) > 0 {
									p := attempt.LocalPorts[0]
									attempt.PrimaryLocalPort = &p
								}
								noteEvidence(attempt, "local_sdp", lineCount, line)
							}
						}
					}

					remoteDesc := getMapFromAny(diag["remote_description"])
					if remoteDesc != nil {
						remoteSDP := getStringFromAny(remoteDesc["sdp"])
						if remoteSDP != "" {
							attempt.HasRemoteSignaling = true
							remoteEndpoints := extractCandidateEndpointsFromSDP(remoteSDP)
							if len(remoteEndpoints) > 0 {
								attempt.RemoteCandidateEndpoints = mergeUniqueStrings(attempt.RemoteCandidateEndpoints, remoteEndpoints)
							}
							noteEvidence(attempt, "remote_sdp", lineCount, line)
						}
					}

					errText := strings.ToLower(getStringFromAny(diag["error"]))
					if strings.Contains(errText, "no answer") {
						attempt.BrokerNoAnswer = true
						attempt.HasSignalingContact = true
						noteEvidence(attempt, "broker_no_answer", lineCount, line)
					}
				}
			}
		}

		if lastPeerID != "" {
			current := attemptsByPeer[lastPeerID]
			if current != nil {
				if strings.Contains(line, "HTTP rendezvous response:") {
					current.HasSignalingContact = true
					noteEvidence(current, "http_rendezvous_response", lineCount, line)
				}
				if strings.Contains(line, "Received answer:") {
					current.HasSignalingContact = true
					current.HasRemoteSignaling = true
					noteEvidence(current, "received_answer", lineCount, line)
				}
				if strings.Contains(line, "Unexpected error, no answer.") {
					current.HasSignalingContact = true
					current.BrokerNoAnswer = true
					noteEvidence(current, "unexpected_no_answer", lineCount, line)
				}
				if strings.Contains(line, "WebRTC: DataChannel.OnOpen") {
					current.DatachannelSuccess = true
					noteEvidence(current, "datachannel_onopen", lineCount, line)
				}
				if strings.Contains(line, "timeout waiting for DataChannel.OnOpen") {
					current.DatachannelTimeout = true
					noteEvidence(current, "datachannel_timeout", lineCount, line)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, lineCount, year, err
	}

	attempts := make([]Attempt, 0, len(peerOrder))
	for _, peerID := range peerOrder {
		if attempt := attemptsByPeer[peerID]; attempt != nil {
			attempts = append(attempts, *attempt)
		}
	}
	sort.Slice(attempts, func(i, j int) bool {
		a := 0.0
		b := 0.0
		if attempts[i].StartTS != nil {
			a = *attempts[i].StartTS
		}
		if attempts[j].StartTS != nil {
			b = *attempts[j].StartTS
		}
		if a == b {
			return attempts[i].StartLine < attempts[j].StartLine
		}
		return a < b
	})
	return attempts, lineCount, year, nil
}

func buildAttemptID(probe, peerID string, primaryPort *int, seq int) string {
	port := "unknown"
	if primaryPort != nil {
		port = strconv.Itoa(*primaryPort)
	}
	return fmt.Sprintf("%s-%s-%s-%03d", probe, peerID, port, seq)
}

func writeJSON(path string, data interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func readJSON(path string, out interface{}) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func toMap(v interface{}) (map[string]interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeLineRange(src, dst string, startLine int, endLine *int) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	scanner := bufio.NewScanner(in)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo < startLine {
			continue
		}
		if endLine != nil && lineNo > *endLine {
			break
		}
		if _, err := out.WriteString(scanner.Text() + "\n"); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func writeTorWindow(torLog, dst string, startTS, endTS *float64, year int) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if !fileExists(torLog) || startTS == nil || endTS == nil {
		return os.WriteFile(dst, []byte{}, 0o644)
	}
	in, err := os.Open(torLog)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	low := *startTS - 2.0
	high := *endTS + 2.0
	scanner := bufio.NewScanner(in)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		ts := parseTorLineEpoch(line, year)
		if ts == nil {
			continue
		}
		if *ts >= low && *ts <= high {
			if _, err := out.WriteString(line + "\n"); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func lastLineFromOutput(out []byte, fallback string) string {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return fallback
	}
	parts := strings.Split(trimmed, "\n")
	return strings.TrimSpace(parts[len(parts)-1])
}

func runTsharkSlice(inputPcap, outputPcap string, startTS, endTS *float64, localPorts []int) string {
	if err := os.MkdirAll(filepath.Dir(outputPcap), 0o755); err != nil {
		return err.Error()
	}
	if !fileExists(inputPcap) {
		_ = os.WriteFile(outputPcap, []byte{}, 0o644)
		return "source pcap missing"
	}
	if startTS == nil || endTS == nil {
		_ = os.WriteFile(outputPcap, []byte{}, 0o644)
		return "attempt timestamps missing"
	}

	timeFilter := fmt.Sprintf("frame.time_epoch >= %.6f && frame.time_epoch <= %.6f", *startTS-1.0, *endTS+1.0)
	displayFilter := timeFilter
	if len(localPorts) > 0 {
		exprs := make([]string, 0, len(localPorts))
		for _, p := range localPorts {
			exprs = append(exprs, fmt.Sprintf("udp.port == %d", p))
		}
		displayFilter = fmt.Sprintf("%s && (%s)", timeFilter, strings.Join(exprs, " || "))
	}

	cmd := exec.Command("tshark", "-r", inputPcap, "-Y", displayFilter, "-w", outputPcap)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.WriteFile(outputPcap, []byte{}, 0o644)
		return lastLineFromOutput(out, "tshark failed")
	}
	return ""
}

func parseIntField(raw string) *int {
	token := strings.TrimSpace(raw)
	if token == "" {
		return nil
	}
	if strings.Contains(token, ",") {
		token = strings.TrimSpace(strings.Split(token, ",")[0])
	}
	if token == "" {
		return nil
	}
	v, err := strconv.Atoi(token)
	if err != nil {
		return nil
	}
	return &v
}

func parseTextFieldFirst(raw string) string {
	token := strings.TrimSpace(raw)
	if token == "" {
		return ""
	}
	if strings.Contains(token, ",") {
		token = strings.TrimSpace(strings.Split(token, ",")[0])
	}
	return token
}

func listToIntSlice(v interface{}) []int {
	out := []int{}
	switch arr := v.(type) {
	case []interface{}:
		for _, item := range arr {
			switch t := item.(type) {
			case float64:
				out = append(out, int(t))
			case int:
				out = append(out, t)
			case string:
				if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
					out = append(out, n)
				}
			}
		}
	case []int:
		out = append(out, arr...)
	}
	return out
}

func boolFromMap(m map[string]interface{}, key string) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		l := strings.ToLower(strings.TrimSpace(t))
		return l == "true" || l == "1" || l == "yes"
	default:
		return false
	}
}

func stringFromMap(m map[string]interface{}, key string) string {
	return getStringFromAny(m[key])
}

func intFromMap(m map[string]interface{}, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	return 0
}

func floatFromMap(m map[string]interface{}, key string) (float64, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

func analyzeAttemptPcap(pcapPath string, localPorts []int, remoteCandidateEndpoints []string) (PcapStats, string) {
	stats := PcapStats{}
	if !fileExists(pcapPath) {
		return stats, "attempt pcap missing"
	}
	portSet := map[int]struct{}{}
	for _, p := range localPorts {
		portSet[p] = struct{}{}
	}
	remoteEndpointSet := map[string]struct{}{}
	for _, endpoint := range remoteCandidateEndpoints {
		remoteEndpointSet[endpoint] = struct{}{}
	}

	pairCounts := map[string]int{}
	pairOrder := []string{}
	localFallbackIP := ""
	localFallbackPort := 0

	cmd := exec.Command(
		"tshark", "-r", pcapPath,
		"-Y", "udp",
		"-T", "fields",
		"-e", "frame.time_epoch",
		"-e", "ip.src",
		"-e", "ipv6.src",
		"-e", "udp.srcport",
		"-e", "ip.dst",
		"-e", "ipv6.dst",
		"-e", "udp.dstport",
		"-e", "_ws.col.Protocol",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return stats, lastLineFromOutput(out, "tshark failed")
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 8 {
			continue
		}
		tsText := strings.TrimSpace(parts[0])
		srcIP := parseTextFieldFirst(parts[1])
		if srcIP == "" {
			srcIP = parseTextFieldFirst(parts[2])
		}
		srcPort := parseIntField(parts[3])
		dstIP := parseTextFieldFirst(parts[4])
		if dstIP == "" {
			dstIP = parseTextFieldFirst(parts[5])
		}
		dstPort := parseIntField(parts[6])
		proto := strings.ToUpper(strings.TrimSpace(parts[7]))

		stats.PacketTotal++
		inbound := false
		outbound := false
		if dstPort != nil {
			_, inbound = portSet[*dstPort]
		}
		if srcPort != nil {
			_, outbound = portSet[*srcPort]
		}
		if inbound && !outbound {
			stats.InboundPackets++
		} else if outbound && !inbound {
			stats.OutboundPackets++
		}
		if localFallbackIP == "" {
			if outbound && srcIP != "" && srcPort != nil {
				localFallbackIP = srcIP
				localFallbackPort = *srcPort
			} else if inbound && dstIP != "" && dstPort != nil {
				localFallbackIP = dstIP
				localFallbackPort = *dstPort
			}
		}

		if outbound && !inbound && srcPort != nil && dstPort != nil && srcIP != "" && dstIP != "" {
			remoteKey := endpointKey(dstIP, *dstPort)
			if _, ok := remoteEndpointSet[remoteKey]; ok {
				pairKey := fmt.Sprintf("%s|%d|%s|%d", srcIP, *srcPort, dstIP, *dstPort)
				if _, exists := pairCounts[pairKey]; !exists {
					pairOrder = append(pairOrder, pairKey)
				}
				pairCounts[pairKey]++
			}
		}
		if inbound && !outbound && srcPort != nil && dstPort != nil && srcIP != "" && dstIP != "" {
			remoteKey := endpointKey(srcIP, *srcPort)
			if _, ok := remoteEndpointSet[remoteKey]; ok {
				pairKey := fmt.Sprintf("%s|%d|%s|%d", dstIP, *dstPort, srcIP, *srcPort)
				if _, exists := pairCounts[pairKey]; !exists {
					pairOrder = append(pairOrder, pairKey)
				}
				pairCounts[pairKey]++
			}
		}

		if strings.Contains(proto, "DTLS") {
			if inbound && !outbound {
				stats.DTLSInboundPackets++
				if stats.FirstDTLSInboundTS == "" {
					stats.FirstDTLSInboundTS = tsText
				}
			} else if outbound && !inbound {
				stats.DTLSOutboundPackets++
				if stats.FirstDTLSOutboundTS == "" {
					stats.FirstDTLSOutboundTS = tsText
				}
			}
		}
		if strings.Contains(proto, "STUN") {
			if inbound && !outbound {
				stats.STUNInboundPackets++
				if srcPort != nil && srcIP != "" {
					if _, ok := remoteEndpointSet[endpointKey(srcIP, *srcPort)]; ok {
						stats.RemotePeerSTUNInboundPackets++
						if stats.FirstRemotePeerSTUNInboundTS == "" {
							stats.FirstRemotePeerSTUNInboundTS = tsText
						}
					}
				}
			} else if outbound && !inbound {
				stats.STUNOutboundPackets++
			}
		}
	}

	bestPair := ""
	bestCount := -1
	for _, key := range pairOrder {
		count := pairCounts[key]
		if count > bestCount {
			bestCount = count
			bestPair = key
		}
	}
	if bestPair != "" {
		parts := strings.Split(bestPair, "|")
		if len(parts) == 4 {
			localPort, lerr := strconv.Atoi(parts[1])
			remotePort, rerr := strconv.Atoi(parts[3])
			if lerr == nil && rerr == nil {
				stats.PrimaryLocalIP = parts[0]
				stats.PrimaryLocalPortObserved = localPort
				stats.PrimaryRemoteIP = parts[2]
				stats.PrimaryRemotePort = remotePort
			}
		}
	}
	if stats.PrimaryLocalIP == "" {
		stats.PrimaryLocalIP = localFallbackIP
	}
	if stats.PrimaryLocalPortObserved == 0 {
		stats.PrimaryLocalPortObserved = localFallbackPort
	}
	if stats.PrimaryLocalPortObserved == 0 && len(localPorts) > 0 {
		stats.PrimaryLocalPortObserved = localPorts[0]
	}
	if stats.PrimaryRemoteIP == "" && len(remoteCandidateEndpoints) > 0 {
		if addr, port, ok := parseEndpointKey(remoteCandidateEndpoints[0]); ok {
			stats.PrimaryRemoteIP = addr
			stats.PrimaryRemotePort = port
		}
	}
	return stats, ""
}

func analyzeTorSlice(torSlice string) TorInfo {
	info := TorInfo{}
	if !fileExists(torSlice) {
		return info
	}
	file, err := os.Open(torSlice)
	if err != nil {
		return info
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if !info.TorConnected && strings.Contains(line, `Managed proxy "/usr/bin/snowflake": connected`) {
			info.TorConnected = true
			info.TorConnectedLine = lineNo
		}
		match := bootstrapRe.FindStringSubmatch(line)
		if len(match) > 1 {
			pct, err := strconv.Atoi(match[1])
			if err == nil {
				if pct > info.TorBootstrapMaxPct {
					info.TorBootstrapMaxPct = pct
				}
				if pct >= 100 && !info.TorBootstrap100 {
					info.TorBootstrap100 = true
					info.TorBootstrap100Line = lineNo
				}
			}
		}
	}
	return info
}

func classifyAttempt(meta map[string]interface{}, pcapStats PcapStats, torInfo TorInfo) (string, string, string) {
	hasSignaling := boolFromMap(meta, "has_signaling_contact")
	hasRemoteSignaling := boolFromMap(meta, "has_remote_signaling")
	datachannelSuccess := boolFromMap(meta, "datachannel_success")

	if !hasSignaling {
		return "A", "medium", "No broker signaling response observed."
	}
	if !hasRemoteSignaling {
		return "B", "high", "Broker contacted but no remote signaling answer received."
	}
	if pcapStats.RemotePeerSTUNInboundPackets == 0 {
		return "B-2", "high", "Remote signaling received but no STUN from remote peer observed."
	}
	if pcapStats.DTLSInboundPackets == 0 {
		return "C", "medium", "Remote signaling received but no inbound DTLS packets observed."
	}
	if !datachannelSuccess {
		return "D", "high", "DTLS packets observed but DataChannel did not open."
	}
	if !torInfo.TorBootstrap100 {
		return "E", "high", "DataChannel opened but Tor bootstrap did not reach 100%."
	}
	return "F", "high", "DataChannel opened and Tor bootstrap reached 100%."
}

func validFinalStage(stage string) bool {
	switch stage {
	case "A", "B", "B-2", "C", "D", "E", "F":
		return true
	default:
		return false
	}
}

func stepIndex(inputDir, outputDir string, force bool) error {
	indexPath := filepath.Join(outputDir, "step1", "index.json")
	if fileExists(indexPath) && !force {
		fmt.Printf("step1 index exists: %s\n", indexPath)
		return nil
	}
	clients, err := filepath.Glob(filepath.Join(inputDir, "*-client.log"))
	if err != nil {
		return err
	}
	sort.Strings(clients)

	probes := []ProbeDoc{}
	totalAttempts := 0
	for _, clientLog := range clients {
		base := filepath.Base(clientLog)
		probe := strings.TrimSuffix(base, "-client.log")
		torLog := filepath.Join(inputDir, probe+"-tor.log")
		pcap := filepath.Join(inputDir, probe+"-eth0.pcap")
		tcpdumpErr := filepath.Join(inputDir, probe+".tcpdump.err")

		attempts, lineCount, year, err := parseClientAttempts(clientLog)
		if err != nil {
			return fmt.Errorf("parse %s: %w", clientLog, err)
		}
		for i := range attempts {
			attempts[i].Seq = i + 1
			attempts[i].SourceFile = probe
			attempts[i].AttemptID = buildAttemptID(probe, attempts[i].PeerID, attempts[i].PrimaryLocalPort, i+1)
			if i+1 < len(attempts) {
				nextStartLine := attempts[i+1].StartLine
				attempts[i].NextStartLine = &nextStartLine
				if attempts[i+1].StartTS != nil {
					nextTS := *attempts[i+1].StartTS
					attempts[i].NextStartTS = &nextTS
				}
			}
			totalAttempts++
		}

		probes = append(probes, ProbeDoc{
			Probe:           probe,
			ClientLog:       clientLog,
			TorLog:          torLog,
			Pcap:            pcap,
			TcpdumpErr:      tcpdumpErr,
			ClientLineCount: lineCount,
			Year:            year,
			AttemptCount:    len(attempts),
			Attempts:        attempts,
		})
	}

	doc := IndexDoc{
		GeneratedAt:  nowISO(),
		InputDir:     inputDir,
		ProbeCount:   len(probes),
		AttemptCount: totalAttempts,
		Probes:       probes,
	}
	if err := writeJSON(indexPath, doc); err != nil {
		return err
	}
	fmt.Printf("step1 indexed probes=%d attempts=%d\n", len(probes), totalAttempts)
	return nil
}

func stepSegment(outputDir, attemptFilter, probeFilter string, force bool) error {
	indexPath := filepath.Join(outputDir, "step1", "index.json")
	doc := IndexDoc{}
	if err := readJSON(indexPath, &doc); err != nil {
		return err
	}

	segmented := 0
	skipped := 0
	for _, probeDoc := range doc.Probes {
		if probeFilter != "" && probeDoc.Probe != probeFilter {
			continue
		}
		for _, attempt := range probeDoc.Attempts {
			if attemptFilter != "" && attempt.AttemptID != attemptFilter {
				continue
			}
			attemptDir := filepath.Join(outputDir, "attempts", attempt.AttemptID)
			marker := filepath.Join(attemptDir, "segment.done")
			if fileExists(marker) && !force {
				skipped++
				continue
			}

			startLine := attempt.StartLine
			var endLine *int
			if attempt.NextStartLine != nil {
				v := *attempt.NextStartLine - 1
				endLine = &v
			}

			startTS := attempt.StartTS
			endTS := attempt.NextStartTS
			if endTS == nil {
				if attempt.EndTS != nil {
					v := *attempt.EndTS + 15.0
					endTS = &v
				} else {
					endTS = startTS
				}
			}

			clientSlice := filepath.Join(attemptDir, "attempt_client.log")
			torSlice := filepath.Join(attemptDir, "attempt_tor.log")
			pcapSlice := filepath.Join(attemptDir, "attempt.pcap")
			metaPath := filepath.Join(attemptDir, "metadata.json")

			if err := writeLineRange(probeDoc.ClientLog, clientSlice, startLine, endLine); err != nil {
				return err
			}
			if err := writeTorWindow(probeDoc.TorLog, torSlice, startTS, endTS, probeDoc.Year); err != nil {
				return err
			}
			pcapErr := runTsharkSlice(probeDoc.Pcap, pcapSlice, startTS, endTS, attempt.LocalPorts)

			meta, err := toMap(attempt)
			if err != nil {
				return err
			}
			meta["probe"] = probeDoc.Probe
			meta["source_file"] = probeDoc.Probe
			meta["client_log"] = probeDoc.ClientLog
			meta["tor_log"] = probeDoc.TorLog
			meta["pcap"] = probeDoc.Pcap
			meta["slice_start_line"] = startLine
			if endLine != nil {
				meta["slice_end_line"] = *endLine
			} else {
				meta["slice_end_line"] = nil
			}
			meta["slice_start_time"] = toISO(startTS)
			meta["slice_end_time"] = toISO(endTS)
			meta["attempt_client_log"] = clientSlice
			meta["attempt_tor_log"] = torSlice
			meta["attempt_pcap"] = pcapSlice
			meta["pcap_slice_error"] = pcapErr
			if startTS != nil && endTS != nil {
				meta["duration_seconds"] = *endTS - *startTS
			} else {
				meta["duration_seconds"] = nil
			}
			if err := writeJSON(metaPath, meta); err != nil {
				return err
			}
			if err := os.MkdirAll(attemptDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(marker, []byte(nowISO()+"\n"), 0o644); err != nil {
				return err
			}
			segmented++
		}
	}
	summary := map[string]interface{}{
		"segmented_attempts": segmented,
		"skipped_attempts":   skipped,
		"generated_at":       nowISO(),
	}
	if err := writeJSON(filepath.Join(outputDir, "step2", "segments_summary.json"), summary); err != nil {
		return err
	}
	fmt.Printf("step2 segmented=%d skipped=%d\n", segmented, skipped)
	return nil
}

func filterEvidenceLog(evidence interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	evMap, ok := evidence.(map[string]interface{})
	if !ok {
		return out
	}
	for key, val := range evMap {
		if strings.HasPrefix(key, "stage:") ||
			key == "received_answer" ||
			key == "unexpected_no_answer" ||
			key == "datachannel_onopen" ||
			key == "datachannel_timeout" ||
			key == "http_rendezvous_response" ||
			key == "remote_sdp" {
			out[key] = val
		}
	}
	return out
}

func writeAttemptsCSV(path string, records []map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	fields := []string{
		"attempt_id",
		"source_file",
		"peer_id",
		"primary_local_ip",
		"primary_local_port",
		"primary_local_port_observed",
		"primary_remote_ip",
		"primary_remote_port",
		"start_ts",
		"end_ts",
		"duration_seconds",
		"final_stage",
		"classification_confidence",
		"classification_reason",
		"nat_type_actual",
		"nat_type_sent",
		"has_signaling_contact",
		"has_remote_signaling",
		"remote_peer_stun_inbound_packets",
		"dtls_inbound_packets",
		"dtls_outbound_packets",
		"tor_connected",
		"tor_bootstrap_max_percent",
		"tor_bootstrap_100",
		"error_note",
	}

	writer := csv.NewWriter(file)
	if err := writer.Write(fields); err != nil {
		return err
	}
	for _, record := range records {
		row := make([]string, 0, len(fields))
		for _, field := range fields {
			value := record[field]
			if value == nil {
				row = append(row, "")
				continue
			}
			switch t := value.(type) {
			case string:
				row = append(row, t)
			case bool:
				row = append(row, strconv.FormatBool(t))
			case float64:
				if field == "duration_seconds" {
					row = append(row, strconv.FormatFloat(t, 'f', 1, 64))
				} else if t == float64(int64(t)) {
					row = append(row, strconv.FormatInt(int64(t), 10))
				} else {
					row = append(row, strconv.FormatFloat(t, 'f', -1, 64))
				}
			case int:
				row = append(row, strconv.Itoa(t))
			default:
				row = append(row, fmt.Sprintf("%v", t))
			}
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func stepClassify(outputDir, attemptFilter string, force bool) error {
	metaFiles, err := filepath.Glob(filepath.Join(outputDir, "attempts", "*", "metadata.json"))
	if err != nil {
		return err
	}
	sort.Strings(metaFiles)

	records := []map[string]interface{}{}
	classified := 0
	reused := 0

	for _, metaPath := range metaFiles {
		meta := map[string]interface{}{}
		if err := readJSON(metaPath, &meta); err != nil {
			return err
		}
		attemptID := stringFromMap(meta, "attempt_id")
		if attemptID == "" {
			continue
		}
		if attemptFilter != "" && attemptID != attemptFilter {
			continue
		}

		existingStage := strings.ToUpper(stringFromMap(meta, "final_stage"))
		if !force && validFinalStage(existingStage) {
			records = append(records, meta)
			reused++
			continue
		}

		localPorts := listToIntSlice(meta["local_ports"])
		remoteEndpoints := []string{}
		if raw, ok := meta["remote_candidate_endpoints"]; ok {
			switch arr := raw.(type) {
			case []interface{}:
				for _, item := range arr {
					if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						remoteEndpoints = append(remoteEndpoints, s)
					}
				}
			case []string:
				remoteEndpoints = append(remoteEndpoints, arr...)
			}
		}
		pcapPath := stringFromMap(meta, "attempt_pcap")
		pcapStats, pcapErr := analyzeAttemptPcap(pcapPath, localPorts, remoteEndpoints)
		torInfo := analyzeTorSlice(stringFromMap(meta, "attempt_tor_log"))
		stage, confidence, reason := classifyAttempt(meta, pcapStats, torInfo)

		meta["packet_total"] = pcapStats.PacketTotal
		meta["dtls_inbound_packets"] = pcapStats.DTLSInboundPackets
		meta["dtls_outbound_packets"] = pcapStats.DTLSOutboundPackets
		meta["stun_inbound_packets"] = pcapStats.STUNInboundPackets
		meta["stun_outbound_packets"] = pcapStats.STUNOutboundPackets
		meta["remote_peer_stun_inbound_packets"] = pcapStats.RemotePeerSTUNInboundPackets
		meta["primary_local_ip"] = pcapStats.PrimaryLocalIP
		meta["primary_local_port_observed"] = pcapStats.PrimaryLocalPortObserved
		meta["primary_remote_ip"] = pcapStats.PrimaryRemoteIP
		meta["primary_remote_port"] = pcapStats.PrimaryRemotePort
		meta["inbound_packets"] = pcapStats.InboundPackets
		meta["outbound_packets"] = pcapStats.OutboundPackets
		meta["first_dtls_inbound_ts"] = pcapStats.FirstDTLSInboundTS
		meta["first_dtls_outbound_ts"] = pcapStats.FirstDTLSOutboundTS
		meta["first_remote_peer_stun_inbound_ts"] = pcapStats.FirstRemotePeerSTUNInboundTS
		meta["tor_connected"] = torInfo.TorConnected
		meta["tor_connected_line"] = torInfo.TorConnectedLine
		meta["tor_bootstrap_max_percent"] = torInfo.TorBootstrapMaxPct
		meta["tor_bootstrap_100"] = torInfo.TorBootstrap100
		meta["tor_bootstrap_100_line"] = torInfo.TorBootstrap100Line
		meta["final_stage"] = stage
		meta["classification_confidence"] = confidence
		meta["classification_reason"] = reason
		meta["evidence_log"] = filterEvidenceLog(meta["evidence"])
		meta["evidence_packets"] = map[string]interface{}{
			"dtls_inbound_packets":              pcapStats.DTLSInboundPackets,
			"dtls_outbound_packets":             pcapStats.DTLSOutboundPackets,
			"stun_inbound_packets":              pcapStats.STUNInboundPackets,
			"stun_outbound_packets":             pcapStats.STUNOutboundPackets,
			"remote_peer_stun_inbound_packets":  pcapStats.RemotePeerSTUNInboundPackets,
			"primary_local_ip":                  pcapStats.PrimaryLocalIP,
			"primary_local_port_observed":       pcapStats.PrimaryLocalPortObserved,
			"primary_remote_ip":                 pcapStats.PrimaryRemoteIP,
			"primary_remote_port":               pcapStats.PrimaryRemotePort,
			"first_dtls_inbound_ts":             pcapStats.FirstDTLSInboundTS,
			"first_dtls_outbound_ts":            pcapStats.FirstDTLSOutboundTS,
			"first_remote_peer_stun_inbound_ts": pcapStats.FirstRemotePeerSTUNInboundTS,
		}
		if strings.TrimSpace(pcapErr) != "" {
			meta["error_note"] = pcapErr
		}
		meta["classified_at"] = nowISO()

		if err := writeJSON(metaPath, meta); err != nil {
			return err
		}
		records = append(records, meta)
		classified++
	}

	sort.Slice(records, func(i, j int) bool {
		si := stringFromMap(records[i], "source_file")
		sj := stringFromMap(records[j], "source_file")
		if si == sj {
			return intFromMap(records[i], "seq") < intFromMap(records[j], "seq")
		}
		return si < sj
	})

	step3Dir := filepath.Join(outputDir, "step3")
	if err := writeJSON(filepath.Join(step3Dir, "attempts.json"), records); err != nil {
		return err
	}
	if err := writeAttemptsCSV(filepath.Join(step3Dir, "attempts.csv"), records); err != nil {
		return err
	}
	classSummary := map[string]interface{}{
		"classified_attempts": classified,
		"reused_attempts":     reused,
		"total_records":       len(records),
		"generated_at":        nowISO(),
	}
	if err := writeJSON(filepath.Join(step3Dir, "classification_summary.json"), classSummary); err != nil {
		return err
	}
	fmt.Printf("step3 classified=%d reused=%d total=%d\n", classified, reused, len(records))
	return nil
}

func stepAggregate(outputDir string) error {
	records := []map[string]interface{}{}
	if err := readJSON(filepath.Join(outputDir, "step3", "attempts.json"), &records); err != nil {
		return err
	}

	stageCounts := map[string]int{}
	for _, record := range records {
		stage := stringFromMap(record, "final_stage")
		if stage == "" {
			stage = "unknown"
		}
		stageCounts[stage]++
	}
	total := len(records)
	stageOrder := []string{"A", "B", "B-2", "C", "D", "E", "F", "unknown"}
	rows := []map[string]interface{}{}
	for _, stage := range stageOrder {
		count := stageCounts[stage]
		pct := 0.0
		if total > 0 {
			pct = float64(count) * 100.0 / float64(total)
		}
		rows = append(rows, map[string]interface{}{
			"final_stage": stage,
			"count":       count,
			"percentage":  fmt.Sprintf("%.2f", pct),
		})
	}

	step4Dir := filepath.Join(outputDir, "step4")
	if err := os.MkdirAll(step4Dir, 0o755); err != nil {
		return err
	}
	stageCSV, err := os.Create(filepath.Join(step4Dir, "stage_summary.csv"))
	if err != nil {
		return err
	}
	stageWriter := csv.NewWriter(stageCSV)
	_ = stageWriter.Write([]string{"final_stage", "count", "percentage"})
	for _, row := range rows {
		_ = stageWriter.Write([]string{
			stringFromMap(row, "final_stage"),
			strconv.Itoa(intFromMap(row, "count")),
			stringFromMap(row, "percentage"),
		})
	}
	stageWriter.Flush()
	_ = stageCSV.Close()

	bySource := map[string]map[string]int{}
	for _, record := range records {
		source := stringFromMap(record, "source_file")
		if source == "" {
			source = "unknown"
		}
		stage := stringFromMap(record, "final_stage")
		if stage == "" {
			stage = "unknown"
		}
		if bySource[source] == nil {
			bySource[source] = map[string]int{}
		}
		bySource[source][stage]++
	}

	sourceCSV, err := os.Create(filepath.Join(step4Dir, "source_breakdown.csv"))
	if err != nil {
		return err
	}
	sourceWriter := csv.NewWriter(sourceCSV)
	header := []string{"source_file", "A", "B", "B-2", "C", "D", "E", "F", "unknown", "total"}
	_ = sourceWriter.Write(header)
	sources := make([]string, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		row := []string{source}
		subtotal := 0
		for _, stage := range []string{"A", "B", "B-2", "C", "D", "E", "F", "unknown"} {
			count := bySource[source][stage]
			subtotal += count
			row = append(row, strconv.Itoa(count))
		}
		row = append(row, strconv.Itoa(subtotal))
		_ = sourceWriter.Write(row)
	}
	sourceWriter.Flush()
	_ = sourceCSV.Close()

	failureStages := []string{"A", "B", "B-2", "C", "D", "E"}
	dominant := "unknown"
	dominantCount := -1
	for _, stage := range failureStages {
		if stageCounts[stage] > dominantCount {
			dominant = stage
			dominantCount = stageCounts[stage]
		}
	}

	summary := map[string]interface{}{
		"generated_at":           nowISO(),
		"total_attempts":         total,
		"stage_counts":           stageCounts,
		"dominant_failure_stage": dominant,
		"dominant_failure_count": dominantCount,
		"unknown_count":          stageCounts["unknown"],
	}
	if err := writeJSON(filepath.Join(step4Dir, "summary.json"), summary); err != nil {
		return err
	}

	stageLabels := map[string]string{
		"A":       "Signaling unreachable",
		"B":       "No remote signaling answer",
		"B-2":     "No peer STUN after signaling",
		"C":       "No peer DTLS after peer STUN",
		"D":       "DTLS started, handshake incomplete",
		"E":       "DTLS ok, Tor bootstrap incomplete",
		"F":       "Successful connection",
		"unknown": "Unknown/unclassified",
	}

	lines := []string{
		"Connection Attempt Findings",
		"",
		fmt.Sprintf("Total attempts analyzed: %d", total),
		fmt.Sprintf("Dominant failure stage: %s (%s) (%d attempts)", dominant, stageLabels[dominant], dominantCount),
		fmt.Sprintf("Unknown/unclassified attempts: %d", stageCounts["unknown"]),
		"",
		"Stage distribution:",
	}
	for _, stage := range stageOrder {
		count := stageCounts[stage]
		pct := 0.0
		if total > 0 {
			pct = float64(count) * 100.0 / float64(total)
		}
		lines = append(lines, fmt.Sprintf("- Stage %s (%s): %d (%.2f%%)", stage, stageLabels[stage], count, pct))
	}
	lines = append(lines, "", "Likely dominant failure reason is indicated by the highest-count failure stage (A/B/B-2/C/D/E) above.")
	if err := os.WriteFile(filepath.Join(outputDir, "findings.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}

	fmt.Printf("step4 aggregated total=%d dominant_failure=%s\n", total, dominant)
	return nil
}

func getStageCounts(summary map[string]interface{}) map[string]int {
	out := map[string]int{}
	stageMap, ok := summary["stage_counts"].(map[string]interface{})
	if !ok {
		return out
	}
	for k, v := range stageMap {
		switch t := v.(type) {
		case float64:
			out[k] = int(t)
		case int:
			out[k] = t
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
				out[k] = n
			}
		}
	}
	return out
}

func stepVerify(outputDir string) (int, error) {
	records := []map[string]interface{}{}
	if err := readJSON(filepath.Join(outputDir, "step3", "attempts.json"), &records); err != nil {
		return 1, err
	}
	summary := map[string]interface{}{}
	if err := readJSON(filepath.Join(outputDir, "step4", "summary.json"), &summary); err != nil {
		return 1, err
	}

	failures := []string{}
	warnings := []string{}
	seen := map[string]struct{}{}
	stageCounts := map[string]int{}
	for _, record := range records {
		attemptID := stringFromMap(record, "attempt_id")
		if attemptID == "" {
			failures = append(failures, "record missing attempt_id")
			continue
		}
		if _, ok := seen[attemptID]; ok {
			failures = append(failures, "duplicate attempt_id: "+attemptID)
		}
		seen[attemptID] = struct{}{}

		stage := stringFromMap(record, "final_stage")
		if !validFinalStage(stage) {
			failures = append(failures, fmt.Sprintf("%s: invalid final_stage=%s", attemptID, stage))
		}
		stageCounts[stage]++

		if dur, ok := floatFromMap(record, "duration_seconds"); ok && dur < 0 {
			failures = append(failures, fmt.Sprintf("%s: negative duration %.3f", attemptID, dur))
		}
		if _, ok := record["evidence_log"]; !ok {
			warnings = append(warnings, attemptID+": missing evidence_log")
		}
	}

	totalSummary := intFromMap(summary, "total_attempts")
	if totalSummary != len(records) {
		failures = append(failures, fmt.Sprintf("summary total_attempts mismatch: summary=%d records=%d", totalSummary, len(records)))
	}
	summaryCounts := getStageCounts(summary)
	for _, stage := range []string{"A", "B", "B-2", "C", "D", "E", "F"} {
		if summaryCounts[stage] != stageCounts[stage] {
			failures = append(failures, fmt.Sprintf("stage count mismatch %s: summary=%d records=%d", stage, summaryCounts[stage], stageCounts[stage]))
		}
	}

	sampled := map[string]string{}
	for _, stage := range []string{"A", "B", "B-2", "C", "D", "E", "F"} {
		for _, record := range records {
			if stringFromMap(record, "final_stage") == stage {
				id := stringFromMap(record, "attempt_id")
				sampled[stage] = id
				if stage == "B-2" && intFromMap(record, "remote_peer_stun_inbound_packets") != 0 {
					failures = append(failures, id+": Stage B-2 should have zero remote-peer inbound STUN packets")
				}
				if stage == "C" && intFromMap(record, "dtls_inbound_packets") != 0 {
					failures = append(failures, id+": Stage C should have zero inbound DTLS packets")
				}
				if stage == "C" && intFromMap(record, "remote_peer_stun_inbound_packets") == 0 {
					warnings = append(warnings, id+": Stage C sample has zero remote-peer STUN packets")
				}
				if stage == "D" && intFromMap(record, "dtls_inbound_packets") == 0 {
					warnings = append(warnings, id+": Stage D sample has zero inbound DTLS packets")
				}
				break
			}
		}
	}

	lines := []string{
		"Verification Report",
		"",
		"Generated at: " + nowISO(),
		fmt.Sprintf("Total records checked: %d", len(records)),
		fmt.Sprintf("Failures: %d", len(failures)),
		fmt.Sprintf("Warnings: %d", len(warnings)),
		"",
		"Sampled attempts by stage:",
	}
	for _, stage := range []string{"A", "B", "B-2", "C", "D", "E", "F"} {
		id := sampled[stage]
		if id == "" {
			id = "n/a"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", stage, id))
	}
	if len(failures) > 0 {
		lines = append(lines, "", "Failures:")
		for _, f := range failures {
			lines = append(lines, "- "+f)
		}
	}
	if len(warnings) > 0 {
		lines = append(lines, "", "Warnings:")
		for _, w := range warnings {
			lines = append(lines, "- "+w)
		}
	}
	result := "PASS"
	if len(failures) > 0 {
		result = "FAIL"
	}
	lines = append(lines, "", "Result: "+result)

	reportPath := filepath.Join(outputDir, "verification_report.txt")
	if err := os.WriteFile(reportPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return 1, err
	}
	fmt.Printf("step5 verification %s\n", result)
	if len(failures) > 0 {
		return 1, nil
	}
	return 0, nil
}

func runAll(inputDir, outputDir, probe, attempt string, force bool) (int, error) {
	if err := stepIndex(inputDir, outputDir, force); err != nil {
		return 1, err
	}
	if err := stepSegment(outputDir, attempt, probe, force); err != nil {
		return 1, err
	}
	if err := stepClassify(outputDir, attempt, force); err != nil {
		return 1, err
	}
	if err := stepAggregate(outputDir); err != nil {
		return 1, err
	}
	return stepVerify(outputDir)
}

func main() {
	inputDir := flag.String("input-dir", "/root/workdir/bridgetest/log/snowflake/ie_test/20260303-1423", "input data directory")
	outputDir := flag.String("output-dir", "/root/workdir/bridgetest/connection_attempt_analysis_go", "output directory")
	probe := flag.String("probe", "", "process only one probe")
	attempt := flag.String("attempt", "", "process only one attempt id")
	force := flag.Bool("force", false, "force recompute")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: connection_attempt_pipeline.go [--input-dir DIR] [--output-dir DIR] [--probe P] [--attempt ID] [--force] {index|segment|classify|aggregate|verify|run-all}")
		os.Exit(2)
	}

	command := flag.Arg(0)
	var (
		exitCode int
		err      error
	)
	switch command {
	case "index":
		err = stepIndex(*inputDir, *outputDir, *force)
	case "segment":
		err = stepSegment(*outputDir, *attempt, *probe, *force)
	case "classify":
		err = stepClassify(*outputDir, *attempt, *force)
	case "aggregate":
		err = stepAggregate(*outputDir)
	case "verify":
		exitCode, err = stepVerify(*outputDir)
	case "run-all":
		exitCode, err = runAll(*inputDir, *outputDir, *probe, *attempt, *force)
	default:
		err = fmt.Errorf("unknown command: %s", command)
		exitCode = 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
