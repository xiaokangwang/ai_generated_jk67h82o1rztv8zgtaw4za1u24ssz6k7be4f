package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xiaokangwang/fastTransfer/fileParts"

	"github.com/xiaokangwang/fastTransfer/interfacew"

	"github.com/lunixbochs/struc"
)

func NewServer(ctx context.Context, conn io.ReadWriteCloser, engine interfacew.FECEngineV2) *Server {
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{
		conn:              conn,
		ctx:               ctx,
		finish:            cancel,
		initialize:        &sync.Once{},
		workInterval:      10,
		engine:            engine,
		sendingWorkerDone: make(chan struct{}),
	}
	go s.keepReading()
	go s.cleanupOnDone()
	return s
}

const defaultServerAckTimeout = 2 * time.Second

type Server struct {
	conn       io.ReadWriteCloser
	ctx        context.Context
	finish     context.CancelFunc
	initialize *sync.Once
	engine     interfacew.FECEngineV2
	cleanup    sync.Once

	workInterval int

	// File To Send
	FileName string
	FilePart uint32
	// ShardSize
	ShardSize uint32

	// Number of Packets to Send
	RecvWindow uint32
	// Packet Per Second
	RecvRate uint32

	LastSeq    uint64
	TransferID uint64

	OurSeq uint64

	fecEncoder  interfacew.FECEngineEncoder
	FileSize    uint64
	TotalParts  uint32
	PayloadType uint8

	errMsg string

	sendingWorkerStarted atomic.Bool
	sendingWorkerDone    chan struct{}
	lastAckAt            atomic.Int64
}

func (s *Server) cleanupOnDone() {
	<-s.ctx.Done()
	s.cleanup.Do(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.sendingWorkerStarted.Load() {
			select {
			case <-s.sendingWorkerDone:
			case <-time.After(2 * time.Second):
			}
		}
		if s.fecEncoder != nil {
			s.fecEncoder.Close()
			s.fecEncoder = nil
		}
		s.engine = nil
		s.conn = nil
	})
}

func (s *Server) keepReading() {
	defer s.finish()
	data := make([]byte, 1600)
	for s.ctx.Err() == nil {
		n, err := s.conn.Read(data)
		if err != nil {
			if !isExpectedConnClose(err) && s.ctx.Err() == nil {
				fmt.Println(err)
			}
			return
		}
		s.Process(data[:n])
	}
}

func (s *Server) Process(data []byte) {
	message := &Client2Server{}
	err := struc.Unpack(bytes.NewReader(data), message)
	if err != nil {
		fmt.Println(err)
		return
	}
	if message.RecvRate == 0 {
		if s.TransferID != 0 && message.TransferID == s.TransferID {
			s.stopTransfer()
		}
		return
	}
	if s.TransferID != 0 && message.TransferID != s.TransferID {
		if message.Seq != 0 {
			return
		}
		s.stopTransfer()
	}
	startSendingWorker := false
	s.initialize.Do(func() {
		s.TransferID = message.TransferID
		s.noteAckReceived(time.Now())
		s.FileName = message.Path
		s.FilePart = message.FilePart
		s.ShardSize = message.ShardSize
		reader, closer, fileSize, totalParts, payloadType, err := s.prepareRequest(message.Path, message.FilePart, message.ShardSize)
		if err != nil {
			s.errMsg = err.Error()
			fmt.Println(err)
			return
		}
		s.FileSize = fileSize
		s.TotalParts = totalParts
		s.PayloadType = payloadType
		fecEncoder, err := getEncoderSafe(s.engine, reader, int64(s.FileSize), int32(s.ShardSize))
		if closer != nil {
			_ = closer.Close()
		}
		if err != nil {
			s.errMsg = err.Error()
			fmt.Println(err)
			return
		}
		s.fecEncoder = fecEncoder
		startSendingWorker = true
	})
	if message.TransferID != s.TransferID {
		return
	}
	s.noteAckReceived(time.Now())
	if message.Seq >= s.LastSeq {
		s.RecvWindow = message.RecvWindow
		s.RecvRate = message.RecvRate
		s.LastSeq = message.Seq
	}
	if startSendingWorker && s.errMsg == "" {
		s.sendingWorkerDone = make(chan struct{})
		go s.SendingWorker()
	}
	if s.errMsg != "" {
		s.doSend(1)
	}
}

func getEncoderSafe(engine interfacew.FECEngineV2, in io.Reader, inLen int64, shardLen int32) (enc interfacew.FECEngineEncoder, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch v := r.(type) {
			case error:
				err = fmt.Errorf("create fec encoder: %w", v)
			default:
				err = fmt.Errorf("create fec encoder: %v", v)
			}
		}
	}()

	enc = engine.GetEncoder3(in, inLen, shardLen)
	return enc, nil
}

func (s *Server) SendingWorker() {
	s.sendingWorkerStarted.Store(true)
	doneCh := s.sendingWorkerDone
	defer s.sendingWorkerStarted.Store(false)
	defer close(doneCh)
	sendReady := time.NewTimer(time.Duration(0) * time.Millisecond)
	defer sendReady.Stop()

	for s.ctx.Err() == nil {
		if s.RecvRate == 0 {
			return
		}
		if s.ackTimedOut(time.Now()) {
			s.RecvRate = 0
			s.resetTransferState()
			return
		}
		numberOfPacketsToSend := uint32(math.Ceil(float64(s.RecvRate) * (0.001 * float64(s.workInterval))))
		if s.RecvWindow < numberOfPacketsToSend {
			numberOfPacketsToSend = s.RecvWindow
		}
		s.doSend(numberOfPacketsToSend)
		if s.ctx.Err() != nil {
			fmt.Println("Connection Ended")
			return
		}
		if s.RecvRate == 0 {
			return
		}
		<-sendReady.C
		sendReady.Reset(time.Duration(s.workInterval) * time.Millisecond)
	}
}

func (s *Server) doSend(amount uint32) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovered in f", r)
		}
	}()
	for i := uint32(0); i < amount; i++ {
		message := &Server2Client{}
		if s.errMsg != "" {
			message.ResponseType = ResponseTypeError
		} else {
			message.ResponseType = ResponseTypeData
			message.PayloadType = s.PayloadType
		}
		message.Seq = s.OurSeq
		message.TransferID = s.TransferID

		if s.errMsg != "" {
			message.Data = []byte(s.errMsg)
		} else {
			message.FileSize = s.FileSize
			message.FileTotalParts = s.TotalParts

			blockid := uint32(mrand.Int31())
			data := s.fecEncoder.GetShard(blockid)
			message.ShardSeq = blockid
			message.Data = data
		}
		buf := bytes.NewBuffer(nil)
		err := struc.Pack(buf, &message)
		if err != nil {
			fmt.Println(err)
			return
		}
		_, err = s.conn.Write(buf.Bytes())
		if err != nil {
			if !isExpectedConnClose(err) && s.ctx.Err() == nil {
				fmt.Println(err)
			}
			s.finish()
			return
		}
		s.OurSeq++
		if s.RecvWindow > 0 {
			s.RecvWindow--
		}
	}
}

func isExpectedConnClose(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, net.ErrClosed):
		return true
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "conn is closed")
}

func (s *Server) stopTransfer() {
	if s.sendingWorkerStarted.Load() {
		s.RecvRate = 0
		select {
		case <-s.sendingWorkerDone:
		case <-time.After(2 * time.Second):
		}
	}
	s.resetTransferState()
}

func (s *Server) noteAckReceived(now time.Time) {
	s.lastAckAt.Store(now.UnixNano())
}

func (s *Server) ackTimedOut(now time.Time) bool {
	lastAckAt := s.lastAckAt.Load()
	if lastAckAt == 0 {
		return false
	}
	return now.Sub(time.Unix(0, lastAckAt)) >= defaultServerAckTimeout
}

func (s *Server) resetTransferState() {
	if s.fecEncoder != nil {
		s.fecEncoder.Close()
		s.fecEncoder = nil
	}
	s.initialize = &sync.Once{}
	s.FileName = ""
	s.FilePart = 0
	s.ShardSize = 0
	s.RecvWindow = 0
	s.RecvRate = 0
	s.LastSeq = 0
	s.TransferID = 0
	s.OurSeq = 0
	s.FileSize = 0
	s.TotalParts = 0
	s.PayloadType = 0
	s.errMsg = ""
	s.lastAckAt.Store(0)
}

func (s *Server) prepareRequest(path string, part, shardSize uint32) (io.Reader, io.Closer, uint64, uint32, uint8, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}
	if info.IsDir() {
		if part != 0 {
			return nil, nil, 0, 0, 0, fmt.Errorf("fromPart is not supported for directory listing")
		}
		return s.prepareListingRequest(path)
	}
	return s.prepareFileRequest(path, part, shardSize)
}

func (s *Server) prepareFileRequest(path string, part, shardSize uint32) (io.Reader, io.Closer, uint64, uint32, uint8, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}
	if fileInfo.IsDir() {
		return nil, nil, 0, 0, 0, fmt.Errorf("%s is a directory; use list or recursive transfer", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}

	partSizeLimit := MaxPartSizeForEngine(s.engine, shardSize)
	if partSizeLimit == 0 {
		_ = file.Close()
		return nil, nil, 0, 0, 0, fmt.Errorf("invalid shard size %d", shardSize)
	}

	partedFile := fileParts.NewPartedFile(file, partSizeLimit)
	totalParts := partedFile.GetTotalParts()
	if part >= totalParts {
		_ = file.Close()
		return nil, nil, 0, 0, 0, fmt.Errorf("part %d not found", part)
	}

	partSize := partedFile.GetPartSize(part)
	reader, err := partedFile.GetPartReader(part)
	if err != nil {
		_ = file.Close()
		return nil, nil, 0, 0, 0, err
	}

	return reader, file, uint64(partSize), totalParts, PayloadTypeFile, nil
}

func (s *Server) prepareListingRequest(path string) (io.Reader, io.Closer, uint64, uint32, uint8, error) {
	listing, err := BuildListing(path)
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}
	data, err := EncodeListing(listing)
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}

	return bytes.NewReader(data), nil, uint64(len(data)), 1, PayloadTypeList, nil
}
