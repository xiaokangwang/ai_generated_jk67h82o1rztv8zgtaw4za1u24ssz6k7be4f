package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunixbochs/struc"

	"github.com/xiaokangwang/fastTransfer/interfacew"
)

type fakeDecoder struct {
	closed bool
}

type countingEngine struct {
	decoderCalls int
}

type fakeConn struct {
	writeErr     error
	readFunc     func([]byte) (int, error)
	lastDeadline time.Time
}

func (f *fakeConn) Read(p []byte) (int, error) {
	if f.readFunc != nil {
		return f.readFunc(p)
	}
	return 0, io.EOF
}

func (f *fakeConn) Write(p []byte) (int, error) {
	return 0, f.writeErr
}

func (f *fakeConn) Close() error {
	return nil
}

func (f *fakeConn) LocalAddr() net.Addr {
	return nil
}

func (f *fakeConn) RemoteAddr() net.Addr {
	return nil
}

func (f *fakeConn) SetDeadline(t time.Time) error {
	return nil
}

func (f *fakeConn) SetReadDeadline(t time.Time) error {
	f.lastDeadline = t
	return nil
}

func (f *fakeConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (f *fakeDecoder) PutShard(blockid uint32, block []byte) bool {
	return false
}

func (f *fakeDecoder) GetIn() io.Reader {
	return nil
}

func (f *fakeDecoder) Close() {
	f.closed = true
}

func (f *countingEngine) GetEncoder(in io.Reader, shardLen int32) interfacew.FECEngineEncoder {
	return nil
}

func (f *countingEngine) GetEncoder3(in io.Reader, inLen int64, shardLen int32) interfacew.FECEngineEncoder {
	return nil
}

func (f *countingEngine) GetDecoder(inLen int64, shardLen int32) interfacew.FECEngineDecoder {
	f.decoderCalls++
	return &fakeDecoder{}
}

func TestClientDoneInterruptedClosesDecoder(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	decoder := &fakeDecoder{}
	client := &Client{
		ctx:        ctx,
		fecDecoder: decoder,
	}

	err := client.Done()
	if !errors.Is(err, ErrTransferInterrupted) {
		t.Fatalf("unexpected error: %v", err)
	}
	if !decoder.closed {
		t.Fatal("expected decoder to be closed")
	}
}

func TestClientDoneSuccess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &Client{
		ctx:  ctx,
		done: true,
	}

	if err := client.Done(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultClientStallTimeout(t *testing.T) {
	t.Parallel()

	if got, want := defaultClientStallTimeout, 2*time.Second; got != want {
		t.Fatalf("unexpected stall timeout: got %v want %v", got, want)
	}
}

func TestClientSendUpdateWriteFailureInterrupts(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		conn:   &fakeConn{writeErr: net.ErrClosed},
		ctx:    ctx,
		finish: cancel,
	}

	client.SendUpdate()

	if !errors.Is(client.resultErr, ErrTransferInterrupted) {
		t.Fatalf("unexpected result error: %v", client.resultErr)
	}
}

func TestClientCheckStall(t *testing.T) {
	t.Parallel()

	now := time.Now()
	client := &Client{}
	client.notePacketReceived(now.Add(-defaultClientStallTimeout - time.Millisecond))
	if !client.checkStall(now) {
		t.Fatal("expected client to be considered stalled")
	}

	client.notePacketReceived(now)
	if client.checkStall(now.Add(time.Second)) {
		t.Fatal("did not expect recent packet timestamp to be considered stalled")
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClientKeepReadingTimeoutDoesNotInterrupt(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstRead := make(chan struct{}, 1)
	release := make(chan struct{})
	var readCalls atomic.Int32
	client := &Client{
		conn: &fakeConn{
			readFunc: func(p []byte) (int, error) {
				switch readCalls.Add(1) {
				case 1:
					firstRead <- struct{}{}
					return 0, timeoutError{}
				default:
					<-release
					return 0, net.ErrClosed
				}
			},
		},
		ctx:    ctx,
		finish: cancel,
	}

	done := make(chan struct{})
	go func() {
		client.keepReading()
		close(done)
	}()

	<-firstRead
	time.Sleep(50 * time.Millisecond)

	if client.resultErr != nil {
		t.Fatalf("unexpected result error after timeout: %v", client.resultErr)
	}
	if ctx.Err() != nil {
		t.Fatalf("unexpected context cancellation after timeout: %v", ctx.Err())
	}

	cancel()
	close(release)
	<-done
}

func TestNewClientResetsReadDeadline(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := NewClient(ctx, conn, &countingEngine{}, Request{TransferID: 1}, 10, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	client.finish()

	if !conn.lastDeadline.IsZero() {
		t.Fatalf("expected zero read deadline, got %v", conn.lastDeadline)
	}
}

func TestClientProcessIgnoresMismatchedTransferID(t *testing.T) {
	t.Parallel()

	engine := &countingEngine{}
	var output bytes.Buffer
	client := &Client{
		initialize: &sync.Once{},
		engine:     engine,
		request: Request{
			TransferID: 2,
		},
		shardSize: DefaultShardSize,
		output:    &output,
	}

	buf := bytes.NewBuffer(nil)
	if err := struc.Pack(buf, &Server2Client{
		ResponseType: ResponseTypeData,
		PayloadType:  PayloadTypeFile,
		Seq:          7,
		TransferID:   1,
		FileSize:     4,
		ShardSeq:     0,
		Data:         []byte("test"),
	}); err != nil {
		t.Fatal(err)
	}

	client.Process(buf.Bytes())

	if engine.decoderCalls != 0 {
		t.Fatalf("unexpected decoder calls: got %d want 0", engine.decoderCalls)
	}
	if client.numberOfShardReceived != 0 {
		t.Fatalf("unexpected shard count: got %d want 0", client.numberOfShardReceived)
	}
	if client.fileSize != 0 {
		t.Fatalf("unexpected file size: got %d want 0", client.fileSize)
	}
}
