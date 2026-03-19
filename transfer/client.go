package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunixbochs/struc"

	"github.com/xiaokangwang/fastTransfer/interfacew"
)

var ErrTransferInterrupted = errors.New("transfer interrupted before completion")
var ErrTransferStalled = errors.New("transfer stalled")

const defaultClientStallTimeout = 2 * time.Second

func NewClient(ctx context.Context, conn io.ReadWriteCloser, engine interfacew.FECEngineV2, request Request, recvRate uint32, output io.Writer) (*Client, error) {
	ctx, cancel := context.WithCancel(ctx)
	resetReadDeadline(conn)
	c := &Client{
		conn:              conn,
		ctx:               ctx,
		finish:            cancel,
		initialize:        &sync.Once{},
		engine:            engine,
		shardSize:         DefaultShardSize,
		request:           request,
		RecvRate:          recvRate,
		output:            output,
		assumeMultiplier:  1.3,
		constantAddWindow: 60,
	}
	c.notePacketReceived(time.Now())
	go c.keepReading()
	go c.watchdog()
	go c.SendingWorker()
	return c, nil
}

type Client struct {
	conn   io.ReadWriteCloser
	ctx    context.Context
	finish context.CancelFunc

	initialize *sync.Once
	engine     interfacew.FECEngineV2

	fecDecoder interfacew.FECEngineDecoder

	fileSize uint64

	ourSeq uint64

	request   Request
	shardSize uint32
	RecvRate  uint32

	output         io.Writer
	fileTotalParts uint32
	payloadType    uint8

	assumeMultiplier  float64
	constantAddWindow float64

	done      bool
	resultErr error

	numberOfShardReceived uint32
	lastPacketAt          atomic.Int64
	maxSeqSeen            uint64
}

func (c *Client) keepReading() {
	data := make([]byte, 1600)
	for c.ctx.Err() == nil {
		n, err := c.conn.Read(data)
		if err != nil {
			if isClientReadTimeout(err) {
				continue
			}
			if c.ctx.Err() == nil && !c.done && c.resultErr == nil {
				c.resultErr = fmt.Errorf("%w: %v", ErrTransferInterrupted, err)
			}
			if !isExpectedClientReadError(err) && c.ctx.Err() == nil {
				fmt.Println(err)
			}
			c.finish()
			return
		}
		c.Process(data[:n])
	}
}

func (c *Client) watchdog() {
	checkTicker := time.NewTicker(200 * time.Millisecond)
	defer checkTicker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-checkTicker.C:
			if c.checkStall(now) {
				c.fail(ErrTransferStalled)
				return
			}
		}
	}
}

func (c *Client) Process(data []byte) {
	debugStats.packetRecv++
	message := &Server2Client{}
	err := struc.Unpack(bytes.NewReader(data), message)
	if err != nil {
		fmt.Println(err)
		return
	}
	if message.TransferID != c.request.TransferID {
		return
	}
	c.notePacketReceived(time.Now())
	if message.ResponseType == ResponseTypeError {
		c.resultErr = errors.New(string(message.Data))
		c.finish()
		return
	}
	if message.Seq > c.maxSeqSeen {
		c.maxSeqSeen = message.Seq
	}
	c.initialize.Do(func() {
		c.payloadType = message.PayloadType
		c.fileSize = message.FileSize
		c.fileTotalParts = message.FileTotalParts
		decoder := c.engine.GetDecoder(int64(c.fileSize), int32(c.shardSize))
		c.fecDecoder = decoder
	})
	if c.resultErr != nil || c.fecDecoder == nil {
		return
	}
	ok := c.fecDecoder.PutShard(message.ShardSeq, message.Data)
	c.numberOfShardReceived++
	if ok {
		c.done = true
		input := c.fecDecoder.GetIn()
		_, err := io.Copy(c.output, input)
		if err != nil {
			c.resultErr = err
			fmt.Println(err)
		}
		c.fecDecoder.Close()
		c.fecDecoder = nil
		for i := 0; i < 8; i++ {
			c.SendUpdate()
		}
		c.finish()
		sent := c.maxSeqSeen + 1
		loss := 0.0
		if sent > 0 {
			loss = 1.0 - float64(c.numberOfShardReceived)/float64(sent)
			if loss < 0 {
				loss = 0
			}
		}
		fmt.Printf("Total Receive: %v, Seq: %v, Loss: %v\n", c.numberOfShardReceived, c.maxSeqSeen, loss)
	}
}

var debugStats struct {
	packetRecv int
}

var _ = func() bool {
	go func() {
		for {
			time.Sleep(time.Second)
			if os.Getenv("TEST") == "1" {
				fmt.Println("packet recv: ", debugStats.packetRecv)
			}
		}
	}()
	return true
}()

func (c *Client) SendingWorker() {
	sendReady := time.NewTicker(100 * time.Millisecond)
	defer sendReady.Stop()
	for {
		if c.ctx.Err() != nil {
			return
		}
		c.SendUpdate()
		select {
		case <-c.ctx.Done():
			return
		case <-sendReady.C:
		}
	}
}

func (c *Client) SendUpdate() {
	message := &Client2Server{}
	message.Seq = c.ourSeq
	message.TransferID = c.request.TransferID
	if !c.done {
		message.Path = c.request.Path
		message.FilePart = c.request.FilePart
		message.ShardSize = c.shardSize
		message.RecvRate = c.RecvRate
		RecvWindow := c.RecvRate
		if c.fileSize != 0 {
			requiredShard := float64(c.fileSize / uint64(c.shardSize))
			requiredShard *= c.assumeMultiplier
			requiredShard += c.constantAddWindow
			RecvWindow = uint32(requiredShard)
		}
		message.RecvWindow = RecvWindow
	}

	buf := bytes.NewBuffer(nil)
	err := struc.Pack(buf, &message)
	if err != nil {
		c.fail(err)
		fmt.Println(err)
		return
	}
	_, err = c.conn.Write(buf.Bytes())
	if err != nil {
		c.fail(err)
		if !isExpectedClientConnError(err) && c.ctx.Err() == nil {
			fmt.Println(err)
		}
		return
	}
	c.ourSeq++
}

func (c *Client) Done() error {
	<-c.ctx.Done()
	if c.resultErr != nil {
		c.cleanup()
		return c.resultErr
	}
	if c.done {
		c.cleanup()
		return nil
	}
	c.cleanup()
	return ErrTransferInterrupted
}

func (c *Client) GetTotalParts() uint32 {
	return c.fileTotalParts
}

func (c *Client) GetPayloadType() uint8 {
	return c.payloadType
}

func (c *Client) cleanup() {
	wakeReader(c.conn)
	if c.fecDecoder != nil {
		c.fecDecoder.Close()
		c.fecDecoder = nil
	}
	c.engine = nil
}

func (c *Client) fail(err error) {
	if err == nil || c.ctx.Err() != nil {
		return
	}
	if !c.done && c.resultErr == nil {
		c.resultErr = fmt.Errorf("%w: %v", ErrTransferInterrupted, err)
	}
	c.finish()
}

func (c *Client) notePacketReceived(now time.Time) {
	c.lastPacketAt.Store(now.UnixNano())
}

func (c *Client) checkStall(now time.Time) bool {
	if c.done {
		return false
	}
	if c.ctx != nil && c.ctx.Err() != nil {
		return false
	}
	lastPacketAt := c.lastPacketAt.Load()
	if lastPacketAt == 0 {
		return false
	}
	return now.Sub(time.Unix(0, lastPacketAt)) >= defaultClientStallTimeout
}

func isExpectedClientReadError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func isClientReadTimeout(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func wakeReader(conn io.ReadWriteCloser) {
	if deadlineConn, ok := conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = deadlineConn.SetReadDeadline(time.Now())
	}
}

func resetReadDeadline(conn io.ReadWriteCloser) {
	if deadlineConn, ok := conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = deadlineConn.SetReadDeadline(time.Time{})
	}
}

func isExpectedClientConnError(err error) bool {
	if isExpectedClientReadError(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "conn is closed")
}
