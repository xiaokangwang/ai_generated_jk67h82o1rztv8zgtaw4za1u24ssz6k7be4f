package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Test that when dnstt-server restarts while a client has an active session,
// the client can detect the break and establish a new session so new local
// connections succeed.
func TestServerRestartCreatesNewSession(t *testing.T) {
	root := "/root/workdir/dnstt"
	// Build binaries
	build := exec.Command("go", "build", "-o", "dnstt-server-bin", "./dnstt-server")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server failed: %v\n%s", err, string(out))
	}
	build = exec.Command("go", "build", "-o", "dnstt-client-bin", "./dnstt-client")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build client failed: %v\n%s", err, string(out))
	}

	// Keys
	tmp := t.TempDir()
	privFile := filepath.Join(tmp, "server.key")
	pubFile := filepath.Join(tmp, "server.pub")
	gen := exec.Command("./dnstt-server-bin", "-gen-key", "-privkey-file", privFile, "-pubkey-file", pubFile)
	gen.Dir = root
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate keys failed: %v\n%s", err, string(out))
	}

	// Upstream TCP echo server
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upLn.Close()
	upAddr := upLn.Addr().String()
	go func() {
		for {
			conn, err := upLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					c.SetReadDeadline(time.Now().Add(30 * time.Second))
					b, err := r.ReadBytes('\n')
					if err != nil {
						return
					}
					c.Write(b)
				}
			}(conn)
		}
	}()

	// Reserve UDP port
	rawUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	serverUDP := rawUDP.LocalAddr().(*net.UDPAddr).Port
	rawUDP.Close()

	// Start server
	serverCmd := exec.Command("./dnstt-server-bin", "-udp", fmt.Sprintf("127.0.0.1:%d", serverUDP), "-privkey-file", privFile, "t.example", upAddr)
	serverCmd.Dir = root
	serverOut := &bytes.Buffer{}
	serverCmd.Stdout = serverOut
	serverCmd.Stderr = serverOut
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { serverCmd.Process.Kill(); serverCmd.Wait() }()

	// Start client
	rawTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp port: %v", err)
	}
	clientLocal := rawTCP.Addr().String()
	rawTCP.Close()

	clientCmd := exec.Command("./dnstt-client-bin", "-udp", fmt.Sprintf("127.0.0.1:%d", serverUDP), "-pubkey-file", pubFile, "t.example", clientLocal)
	clientCmd.Dir = root
	clientOut := &bytes.Buffer{}
	clientCmd.Stdout = clientOut
	clientCmd.Stderr = clientOut
	if err := clientCmd.Start(); err != nil {
		serverCmd.Process.Kill()
		t.Fatalf("start client: %v", err)
	}
	defer func() { clientCmd.Process.Kill(); clientCmd.Wait() }()

	// Wait for tunnel to establish and accept a connection
	time.Sleep(800 * time.Millisecond)

	// Make initial connection through client
	conn1, err := net.DialTimeout("tcp", clientLocal, 10*time.Second)
	if err != nil {
		t.Fatalf("dial client local failed: %v\nclientOut:\n%s\nserverOut:\n%s", err, clientOut.String(), serverOut.String())
	}
	msg1 := []byte("first-session-msg\n")

	_, err = conn1.Write(msg1)
	if err != nil {
		conn1.Close()
		t.Fatalf("write initial msg failed: %v", err)
	}
	// Read echo
	conn1.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, 1024)
	n, err := conn1.Read(b)
	if err != nil {
		conn1.Close()
		t.Fatalf("read initial echo failed: %v\nclientOut:\n%s\nserverOut:\n%s", err, clientOut.String(), serverOut.String())
	}
	if !bytes.Equal(b[:n], msg1) {
		conn1.Close()
		t.Fatalf("unexpected initial echo: %q", string(b[:n]))
	}
	// Keep conn1 open to simulate active session

	// Restart server
	if err := serverCmd.Process.Kill(); err != nil {
		t.Fatalf("kill server: %v", err)
	}
	serverCmd.Wait()
	// Give OS time to release port
	time.Sleep(200 * time.Millisecond)
	// Start new server process on same UDP port
	serverCmd = exec.Command("./dnstt-server-bin", "-udp", fmt.Sprintf("127.0.0.1:%d", serverUDP), "-privkey-file", privFile, "t.example", upAddr)
	serverCmd.Dir = root
	serverOut = &bytes.Buffer{}
	serverCmd.Stdout = serverOut
	serverCmd.Stderr = serverOut
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("restart server: %v", err)
	}
	defer func() { serverCmd.Process.Kill(); serverCmd.Wait() }()

	// Wait a bit for client to detect and re-establish
	time.Sleep(500 * time.Millisecond)

	// Try to open a new connection through client (should create new session).
	// Retry for up to 20s because re-establishment may take a few seconds.
	var conn2 net.Conn
	var lastErr error
	deadline := time.Now().Add(20 * time.Second)
	msg2 := []byte("second-session-msg\n")
	b2 := make([]byte, 2048)
	for time.Now().Before(deadline) {
		conn2, lastErr = net.DialTimeout("tcp", clientLocal, 3*time.Second)
		if lastErr != nil {
			t.Logf("dial attempt failed: %v", lastErr)
			time.Sleep(300 * time.Millisecond)
			continue
		}
		// got connection, try to write and read echo
		conn2.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, lastErr = conn2.Write(msg2)
		if lastErr != nil {
			conn2.Close()
			t.Logf("write attempt failed: %v", lastErr)
			time.Sleep(300 * time.Millisecond)
			continue
		}
		conn2.SetReadDeadline(time.Now().Add(10 * time.Second))
		n2, lastErr := conn2.Read(b2)
		if lastErr != nil {
			conn2.Close()
			t.Logf("read attempt failed: %v", lastErr)
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if bytes.Equal(b2[:n2], msg2) {
			// success
			conn2.Close()
			lastErr = nil
			break
		}
		conn2.Close()
		t.Logf("unexpected reply after restart: %q", string(b2[:n2]))
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("failed to establish new session after restart: %v\nclientOut:\n%s\nserverOut:\n%s", lastErr, clientOut.String(), serverOut.String())
	}

	// Also try sending further data on the original connection; it may fail or be reset
	// but test ensures the client can create a new session and accept new local connections.
	conn1.Close()
}
