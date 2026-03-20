package transfer

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunixbochs/struc"

	"github.com/xiaokangwang/fastTransfer/interfacew"
)

func packClientMessage(t *testing.T, message *Client2Server) []byte {
	t.Helper()

	buf := bytes.NewBuffer(nil)
	if err := struc.Pack(buf, message); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestServerProcessIgnoresStopForDifferentTransferID(t *testing.T) {
	t.Parallel()

	server := &Server{
		initialize:        &sync.Once{},
		sendingWorkerDone: make(chan struct{}),
		TransferID:        2,
		RecvRate:          100,
		FileName:          "/tmp/file",
	}

	server.Process(packClientMessage(t, &Client2Server{
		TransferID: 1,
		RecvRate:   0,
	}))

	if server.TransferID != 2 {
		t.Fatalf("unexpected transfer id: got %d want 2", server.TransferID)
	}
	if server.RecvRate != 100 {
		t.Fatalf("unexpected recv rate: got %d want 100", server.RecvRate)
	}
	if server.FileName != "/tmp/file" {
		t.Fatalf("unexpected file name: got %q want %q", server.FileName, "/tmp/file")
	}
}

func TestServerProcessIgnoresStaleUpdateForDifferentTransferID(t *testing.T) {
	t.Parallel()

	server := &Server{
		initialize:        &sync.Once{},
		sendingWorkerDone: make(chan struct{}),
		TransferID:        2,
		LastSeq:           7,
		RecvRate:          100,
		RecvWindow:        200,
	}

	server.Process(packClientMessage(t, &Client2Server{
		Seq:        3,
		TransferID: 1,
		RecvRate:   50,
		RecvWindow: 25,
	}))

	if server.TransferID != 2 {
		t.Fatalf("unexpected transfer id: got %d want 2", server.TransferID)
	}
	if server.LastSeq != 7 {
		t.Fatalf("unexpected last seq: got %d want 7", server.LastSeq)
	}
	if server.RecvRate != 100 {
		t.Fatalf("unexpected recv rate: got %d want 100", server.RecvRate)
	}
	if server.RecvWindow != 200 {
		t.Fatalf("unexpected recv window: got %d want 200", server.RecvWindow)
	}
}

type fakeServerConn struct {
	writes atomic.Int32
}

func (f *fakeServerConn) Read(p []byte) (int, error) {
	return 0, io.EOF
}

func (f *fakeServerConn) Write(p []byte) (int, error) {
	f.writes.Add(1)
	return len(p), nil
}

func (f *fakeServerConn) Close() error {
	return nil
}

func (f *fakeServerConn) LocalAddr() net.Addr {
	return nil
}

func (f *fakeServerConn) RemoteAddr() net.Addr {
	return nil
}

func (f *fakeServerConn) SetDeadline(t time.Time) error {
	return nil
}

func (f *fakeServerConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (f *fakeServerConn) SetWriteDeadline(t time.Time) error {
	return nil
}

type fakeEncoder struct{}

func (f *fakeEncoder) GetShard(blockid uint32) []byte {
	return []byte{1, 2, 3}
}

func (f *fakeEncoder) Close() {}

type fakeEngine struct{}

func (f *fakeEngine) GetEncoder(in io.Reader, shardLen int32) interfacew.FECEngineEncoder {
	return &fakeEncoder{}
}

func (f *fakeEngine) GetEncoder3(in io.Reader, inLen int64, shardLen int32) interfacew.FECEngineEncoder {
	return &fakeEncoder{}
}

func (f *fakeEngine) GetDecoder(inLen int64, shardLen int32) interfacew.FECEngineDecoder {
	return nil
}

func TestDefaultServerAckTimeout(t *testing.T) {
	t.Parallel()

	if got, want := defaultServerAckTimeout, 2*time.Second; got != want {
		t.Fatalf("unexpected ack timeout: got %v want %v", got, want)
	}
}

func TestLoadServerWorkIntervalFromEnv(t *testing.T) {
	t.Parallel()

	if got := loadServerWorkIntervalFromEnv(func(string) (string, bool) { return "", false }); got != defaultServerWorkInterval {
		t.Fatalf("unexpected default work interval: got %d want %d", got, defaultServerWorkInterval)
	}
	if got := loadServerWorkIntervalFromEnv(func(string) (string, bool) { return "25", true }); got != 25 {
		t.Fatalf("unexpected overridden work interval: got %d want 25", got)
	}
}

func TestLoadServerWorkIntervalFromEnvInvalid(t *testing.T) {
	t.Parallel()

	cases := []string{
		"0",
		"abc",
	}

	for _, value := range cases {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic")
				}
				if !strings.Contains(r.(string), serverWorkIntervalEnv) {
					t.Fatalf("unexpected panic message: %v", r)
				}
			}()

			_ = loadServerWorkIntervalFromEnv(func(string) (string, bool) { return value, true })
		})
	}
}

func TestServerDoSendZeroSendsNothing(t *testing.T) {
	t.Parallel()

	conn := &fakeServerConn{}
	server := &Server{
		conn: conn,
	}

	server.doSend(0)

	if got := conn.writes.Load(); got != 0 {
		t.Fatalf("unexpected writes: got %d want 0", got)
	}
}

func TestServerSendingWorkerStopsAfterAckTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &fakeServerConn{}
	server := &Server{
		conn:              conn,
		ctx:               ctx,
		finish:            cancel,
		engine:            &fakeEngine{},
		workInterval:      1,
		RecvRate:          1000,
		RecvWindow:        1000,
		TransferID:        1,
		fecEncoder:        &fakeEncoder{},
		sendingWorkerDone: make(chan struct{}),
	}
	server.noteAckReceived(time.Now().Add(-defaultServerAckTimeout - time.Millisecond))

	server.SendingWorker()

	if got := conn.writes.Load(); got != 0 {
		t.Fatalf("unexpected writes after ack timeout: got %d want 0", got)
	}
	if server.RecvRate != 0 {
		t.Fatalf("expected recv rate to be reset, got %d", server.RecvRate)
	}
	if server.TransferID != 0 {
		t.Fatalf("expected transfer id to be reset, got %d", server.TransferID)
	}
}
