package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"

	"github.com/pion/dtls/v3"

	"github.com/xiaokangwang/VLite/interfaces"
	"github.com/xiaokangwang/fastTransfer/fileParts"

	"github.com/xiaokangwang/fastTransfer/fec/execfec"
	"github.com/xiaokangwang/fastTransfer/fec/raptorqfec"
	"github.com/xiaokangwang/fastTransfer/fec/wirehairfec"
	"github.com/xiaokangwang/fastTransfer/interfacew"

	"github.com/xiaokangwang/VLite/transport/udp/udpServer"
	"github.com/xiaokangwang/fastTransfer/transfer"
)

type sr struct {
	localAddr net.Addr
}

var socks5UDPRelayAddress string

func (s *sr) Connection(conn net.Conn, ctx context.Context) context.Context {
	remoteAddr, err := remoteAddrFromConnContext(ctx)
	if err != nil {
		fmt.Println(err)
		_ = conn.Close()
		return nil
	}

	go func() {
		conn2, err := dtls.Server(newPacketConnAdapter(conn, s.localAddr, remoteAddr), remoteAddr, newServerDTLSConfig())
		if err != nil {
			fmt.Println(err)
			return
		}
		if err := completeDTLSHandshake(ctx, conn2); err != nil {
			fmt.Println(err)
			_ = conn2.Close()
			return
		}
		transfer.NewServer(ctx, conn2, NewFECEngine())
	}()
	return ctx
}

func main() {
	var address string
	var client bool
	var partFile bool
	var fileName string
	var outfileName string
	var recvRate int
	var FromPart int
	var listDirectory bool
	var recursive bool
	var resume bool
	var recursiveFilter string

	flag.StringVar(&address, "Address", "127.0.0.1:13315", "")
	flag.StringVar(&fileName, "remoteFileName", "", "")
	flag.StringVar(&outfileName, "localFileName", "", "")
	flag.StringVar(&recursiveFilter, "filter", "", "")
	flag.StringVar(&socks5UDPRelayAddress, "socks5udp", "", "")
	flag.IntVar(&recvRate, "recvRate", 1000, "")
	flag.IntVar(&FromPart, "fromPart", 0, "")
	flag.BoolVar(&client, "client", false, "")
	flag.BoolVar(&listDirectory, "list", false, "")
	flag.BoolVar(&recursive, "recursive", false, "")
	flag.BoolVar(&resume, "resume", false, "")
	flag.BoolVar(&partFile, "partFile", false, "")
	flag.Parse()

	localAddr, err := getLocalUDPAddr(address)
	if err != nil {
		panic(err)
	}
	if partFile {
		file, err := os.Open(fileName)
		if err != nil {
			panic(err)
		}
		defer file.Close()
		partedFile := fileParts.NewPartedFile(file, transfer.MaxPartSizeForEngine(NewFECEngine(), transfer.DefaultShardSize))
		for i := uint32(0); i < partedFile.GetTotalParts(); i++ {
			part, err := partedFile.GetPartReader(i)
			if err != nil {
				panic(err)
			}
			hasher := sha256.New()
			_, err = io.Copy(hasher, part)
			if err != nil {
				panic(err)
			}
			fmt.Printf("%x %08d\n", hasher.Sum(nil), i)
		}
		return
	}
	if !client {
		udpServer.NewUDPServer(address, context.TODO(), &sr{localAddr: localAddr})
		select {}
	} else {
		var recursiveDownloadFilterRE *regexp.Regexp
		if recursive && recursiveFilter != "" {
			recursiveDownloadFilterRE, err = regexp.Compile(recursiveFilter)
			if err != nil {
				panic(err)
			}
		}
		switch {
		case listDirectory:
			var session *remoteSession
			defer closeRemoteSession(session)
			listing, err := fetchListingWithSession(address, &session, fileName, recvRate)
			if err != nil {
				panic(err)
			}
			if err := printListingWithPrefix(address, &session, listing, recursive, recvRate, ""); err != nil {
				panic(err)
			}
		case recursive:
			if FromPart != 0 {
				panic("fromPart is not supported with recursive transfer")
			}
			if err := downloadRemoteRecursive(address, fileName, outfileName, recvRate, resume, recursiveDownloadFilterRE); err != nil {
				panic(err)
			}
		default:
			if err := downloadRemoteFileParts(address, defaultOutputPath(fileName, outfileName), fileName, recvRate, uint32(FromPart)); err != nil {
				panic(err)
			}
		}
	}
}

func NewFECEngine() interfacew.FECEngineV2 {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FEC_ENGINE"))) {
	case "", "raptorq":
		return raptorqfec.NewRaptorQFECV2()
	case "wirehair":
		return wirehairfec.NewWirehairFECV2()
	case "exec":
		return execfec.NewExecFecEngineV2(mustGetConfFromEnv("FEC_BINARY_PATH"))
	default:
		panic(fmt.Sprintf("unsupported FEC_ENGINE %q", os.Getenv("FEC_ENGINE")))
	}
}

func mustGetConfFromEnv(name string) string {
	data, ok := os.LookupEnv(name)
	if ok {
		return data
	}
	panic(fmt.Sprintf("no env %v", name))
}

func remoteAddrFromConnContext(ctx context.Context) (net.Addr, error) {
	connID, ok := ctx.Value(interfaces.ExtraOptionsConnID).([]byte)
	if !ok || len(connID) == 0 {
		return nil, fmt.Errorf("missing udp conn id")
	}

	addr, err := net.ResolveUDPAddr("udp", string(connID))
	if err != nil {
		return nil, fmt.Errorf("resolve remote udp addr: %w", err)
	}

	return addr, nil
}
