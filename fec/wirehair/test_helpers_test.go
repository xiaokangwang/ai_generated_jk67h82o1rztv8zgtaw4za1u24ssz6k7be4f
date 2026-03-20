//go:build wirehairnativeoracle
// +build wirehairnativeoracle

package wirehair

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"codextest2/internal/nativeoracle"
)

func encodeShardsForIDs(t *testing.T, enc *Encoder, ids []uint32, blockBytes uint32) []nativeoracle.Shard {
	t.Helper()
	shards := make([]nativeoracle.Shard, 0, len(ids))
	for _, id := range ids {
		block := make([]byte, blockBytes)
		n, err := enc.Encode(id, block)
		if err != nil {
			t.Fatalf("encode block %d: %v", id, err)
		}
		shards = append(shards, nativeoracle.Shard{
			ID:   id,
			Data: append([]byte(nil), block[:n]...),
		})
	}
	return shards
}

func decodeUntilReady(t *testing.T, dec *Decoder, shards []nativeoracle.Shard) []nativeoracle.Shard {
	t.Helper()
	var used []nativeoracle.Shard
	for _, shard := range shards {
		state, err := dec.Decode(shard.ID, shard.Data)
		used = append(used, shard)
		if err != nil {
			t.Fatalf("decode shard %d: %v", shard.ID, err)
		}
		if state == StateReady {
			return used
		}
	}
	t.Fatalf("decoder never became ready")
	return nil
}

type cliSession struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr bytes.Buffer
}

func startEncodeCLI(t *testing.T, binary string, message []byte, blockBytes uint32) *cliSession {
	t.Helper()
	cmd := exec.Command(binary)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	session := &cliSession{
		t:      t,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdoutPipe),
	}
	cmd.Stderr = &session.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	session.expectLine("+OK INIT")
	if _, err := fmt.Fprintf(stdin, "ENCODE %d %d\n", len(message), blockBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(message); err != nil {
		t.Fatal(err)
	}
	session.expectLine("+OK Ready")
	return session
}

func startDecodeCLI(t *testing.T, binary string, messageBytes uint64, blockBytes uint32) *cliSession {
	t.Helper()
	cmd := exec.Command(binary)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	session := &cliSession{
		t:      t,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdoutPipe),
	}
	cmd.Stderr = &session.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	session.expectLine("+OK INIT")
	if _, err := fmt.Fprintf(stdin, "DECODE %d %d\n", messageBytes, blockBytes); err != nil {
		t.Fatal(err)
	}
	session.expectLine("+OK Ready")
	return session
}

func (s *cliSession) expectLine(prefix string) string {
	s.t.Helper()
	line, err := s.stdout.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read line %q: %v stderr=%s", prefix, err, s.stderr.String())
	}
	line = strings.TrimSpace(line)
	if line != prefix {
		s.t.Fatalf("expected line %q got %q stderr=%s", prefix, line, s.stderr.String())
	}
	return line
}

func (s *cliSession) requestBlock(id uint32) []byte {
	s.t.Helper()
	if _, err := fmt.Fprintf(s.stdin, "%d\n", id); err != nil {
		s.t.Fatal(err)
	}
	line, err := s.stdout.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read block header: %v stderr=%s", err, s.stderr.String())
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 2 || fields[0] != "+OK" {
		s.t.Fatalf("invalid block header %q stderr=%s", line, s.stderr.String())
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		s.t.Fatal(err)
	}
	block := make([]byte, n)
	if _, err := io.ReadFull(s.stdout, block); err != nil {
		s.t.Fatalf("read block payload: %v stderr=%s", err, s.stderr.String())
	}
	return block
}

func (s *cliSession) feedBlock(id uint32, data []byte) string {
	s.t.Helper()
	if _, err := fmt.Fprintf(s.stdin, "%d %d\n", id, len(data)); err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.stdin.Write(data); err != nil {
		s.t.Fatal(err)
	}
	line, err := s.stdout.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read decode response: %v stderr=%s", err, s.stderr.String())
	}
	return strings.TrimSpace(line)
}

func (s *cliSession) recover(expectedBytes int) (string, []byte) {
	s.t.Helper()
	if _, err := io.WriteString(s.stdin, "RECOVER\n"); err != nil {
		s.t.Fatal(err)
	}
	line, err := s.stdout.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read recover header: %v stderr=%s", err, s.stderr.String())
	}
	line = strings.TrimSpace(line)
	if line == "-MORE" {
		return line, nil
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != "+FINISH" {
		s.t.Fatalf("unexpected recover header %q stderr=%s", line, s.stderr.String())
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		s.t.Fatal(err)
	}
	if expectedBytes > 0 && n != expectedBytes {
		s.t.Fatalf("unexpected recover length %d expected %d", n, expectedBytes)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(s.stdout, data); err != nil {
		s.t.Fatalf("read recover payload: %v stderr=%s", err, s.stderr.String())
	}
	return line, data
}

func (s *cliSession) close() {
	s.t.Helper()
	if s.stdin != nil {
		_, _ = io.WriteString(s.stdin, "QUIT\n")
		_ = s.stdin.Close()
	}
	if err := s.cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			s.t.Fatalf("cli exit: %v stderr=%s", exitErr, s.stderr.String())
		}
		s.t.Fatal(err)
	}
}
