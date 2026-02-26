package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This integration test builds the dnstt server and client, generates keys,
// starts an upstream TCP echo server, runs the server and client binaries, and
// verifies a TCP connection can be established through the DNS tunnel over UDP.
func TestClientServerIntegrationUDP(t *testing.T) {
	root := "/root/workdir/dnstt"
	// Build server binary
	build := exec.Command("go", "build", "-o", "dnstt-server-bin", "./dnstt-server")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server failed: %v\n%s", err, string(out))
	}
	// Build client binary
	build = exec.Command("go", "build", "-o", "dnstt-client-bin", "./dnstt-client")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build client failed: %v\n%s", err, string(out))
	}

	// Temp dir for keys
	tmp := t.TempDir()
	privFile := filepath.Join(tmp, "server.key")
	pubFile := filepath.Join(tmp, "server.pub")
	// Generate keypair
	gen := exec.Command("./dnstt-server-bin", "-gen-key", "-privkey-file", privFile, "-pubkey-file", pubFile)
	gen.Dir = root
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate keys failed: %v\n%s", err, string(out))
	}

	// Start an upstream TCP echo server that the dnstt-server will forward to.
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upLn.Close()
	upAddr := upLn.Addr().String()
	// Accept one connection and echo data.
	upGot := make(chan []byte, 1)
	go func() {
		conn, err := upLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil && err != io.EOF {
			return
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		// Echo back
		conn.Write(data)
		upGot <- data
	}()

	// Pick a UDP port for the server to bind by reserving one and closing it.
	rawUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	udpPort := rawUDP.LocalAddr().(*net.UDPAddr).Port
	rawUDP.Close()

	// Pick a TCP port for client's local listener.
	rawTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp port: %v", err)
	}
	clientLocal := rawTCP.Addr().String()
	rawTCP.Close()

	// Start dnstt-server
	serverCmd := exec.Command("./dnstt-server-bin", "-udp", fmt.Sprintf("127.0.0.1:%d", udpPort), "-privkey-file", privFile, "t.example", upAddr)
	serverCmd.Dir = root
	serverStdout := &bytes.Buffer{}
	serverCmd.Stdout = serverStdout
	serverCmd.Stderr = serverStdout
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() {
		serverCmd.Process.Kill()
		serverCmd.Wait()
	}()

	// Start dnstt-client
	clientCmd := exec.Command("./dnstt-client-bin", "-udp", fmt.Sprintf("127.0.0.1:%d", udpPort), "-pubkey-file", pubFile, "t.example", clientLocal)
	clientCmd.Dir = root
	clientStdout := &bytes.Buffer{}
	clientCmd.Stdout = clientStdout
	clientCmd.Stderr = clientStdout
	if err := clientCmd.Start(); err != nil {
		serverCmd.Process.Kill()
		t.Fatalf("start client: %v", err)
	}
	defer func() {
		clientCmd.Process.Kill()
		clientCmd.Wait()
	}()

	// Attempt to connect to client's local listener and exchange data.
	var conn net.Conn
	var lastErr error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, lastErr = net.DialTimeout("tcp", clientLocal, 1*time.Second)
		if lastErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr != nil {
		// Dump logs to help diagnostics
		t.Logf("server stdout: %s", serverStdout.String())
		t.Logf("client stdout: %s", clientStdout.String())
		t.Fatalf("dial client local failed: %v", lastErr)
	}
	defer conn.Close()

	payload := []byte("hello-over-dnstt")
	_, err = conn.Write(payload)
	if err != nil {
		t.Fatalf("write to client local failed: %v", err)
	}
	// Read echo from upstream (via tunnel).
	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read from client local failed: %v", err)
	}
	resp := buf[:n]
	if !bytes.Equal(resp, payload) {
		t.Fatalf("unexpected response: got %q want %q", string(resp), string(payload))
	}

	// Verify upstream observed the payload as well.
	select {
	case got := <-upGot:
		if !bytes.Equal(got, payload) {
			t.Fatalf("upstream got %q want %q", string(got), string(payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("upstream did not receive data in time")
	}
}
