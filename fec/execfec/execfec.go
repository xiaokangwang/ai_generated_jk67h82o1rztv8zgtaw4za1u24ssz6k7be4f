package execfec

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	execos "os/exec"
	"strconv"
	"strings"

	"github.com/xiaokangwang/fastTransfer/interfacew"
)

func NewExecFecEngineV2(path string) interfacew.FECEngineV2 {
	return &execFecEngineV2{path: path}
}

type execFecEngineV2 struct {
	path string
}

func (e execFecEngineV2) GetEncoder(in io.Reader, shardLen int32) interfacew.FECEngineEncoder {
	data, _ := io.ReadAll(in)
	return e.GetEncoder3(bytes.NewReader(data), int64(len(data)), shardLen)
}

func (e execFecEngineV2) GetEncoder3(in io.Reader, inLen int64, shardLen int32) interfacew.FECEngineEncoder {
	if inLen < int64(shardLen) {
		data, _ := io.ReadAll(in)
		return smallEncoder{Buffer: data}
	}
	path, err := execos.LookPath(e.path)
	if err != nil {
		panic(err)
	}
	cmd := execos.Command(path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		panic(err)
	}
	cmd.Stderr = os.Stderr
	err = cmd.Start()
	encoder := execFecEngineEncoderV2{
		fileLen:  inLen,
		shardlen: shardLen,
		stdout:   stdout,
		stdoutb:  bufio.NewReader(stdout),
		stdin:    stdin,
		cmd:      cmd,
	}
	encoder.init(in)
	return encoder
}

func (e execFecEngineV2) GetDecoder(inLen int64, shardLen int32) interfacew.FECEngineDecoder {
	if inLen <= int64(shardLen) {
		return &smallDecoder{}
	}

	path, err := execos.LookPath(e.path)
	if err != nil {
		panic(err)
	}
	cmd := execos.Command(path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		panic(err)
	}
	cmd.Stderr = os.Stderr
	err = cmd.Start()
	decoder := execFecEngineDecoderV2{
		fileLen:  inLen,
		shardlen: shardLen,
		stdout:   stdout,
		stdoutb:  bufio.NewReader(stdout),
		stdin:    stdin,
		cmd:      cmd,
	}
	decoder.init()
	return decoder
}

type execFecEngineEncoderV2 struct {
	fileLen  int64
	shardlen int32

	stdout  io.ReadCloser
	stdoutb *bufio.Reader
	stdin   io.WriteCloser
	cmd     *execos.Cmd
}

func (e execFecEngineEncoderV2) GetShard(blockid uint32) []byte {
	e.stdin.Write([]byte(fmt.Sprintf("%v\n", blockid)))
	str, err := e.stdoutb.ReadString('\n')
	if err != nil {
		panic(err)
	}
	if !strings.HasPrefix(str, "+") {
		panic("unexpected output" + str)
	}

	splited := strings.Split(str, " ")
	outsize, _ := strconv.ParseInt(strings.TrimRight(splited[1], "\n"), 10, 32)
	shardData := make([]byte, outsize)
	_, err = io.ReadFull(e.stdoutb, shardData)
	if err != nil {
		panic(err)
	}
	return shardData
}

func (e execFecEngineEncoderV2) Close() {
	e.stdin.Write([]byte(fmt.Sprintf("QUIT\n")))
	e.stdin.Close()
	e.cmd.Wait()
}

func (e execFecEngineEncoderV2) init(in io.Reader) {
	str, err := e.stdoutb.ReadString('\n')
	if err != nil {
		panic(err)
	}
	if !strings.HasPrefix(str, "+") {
		panic("unexpected output" + str)
	}
	e.stdin.Write([]byte(fmt.Sprintf("ENCODE %v %v\n", e.fileLen, e.shardlen)))
	n, _ := io.Copy(e.stdin, in)
	if n != int64(e.fileLen) {
		panic("incorrect input stream")
	}
	str, err = e.stdoutb.ReadString('\n')
	if err != nil {
		panic(err)
	}
	if !strings.HasPrefix(str, "+") {
		panic("unexpected output " + str)
	}
}

type execFecEngineDecoderV2 struct {
	fileLen  int64
	shardlen int32

	stdout  io.ReadCloser
	stdoutb *bufio.Reader
	stdin   io.WriteCloser
	cmd     *execos.Cmd
}

func (e execFecEngineDecoderV2) init() {
	str, err := e.stdoutb.ReadString('\n')
	if err != nil {
		panic(err)
	}
	if !strings.HasPrefix(str, "+") {
		panic("unexpected output" + str)
	}
	e.stdin.Write([]byte(fmt.Sprintf("DECODE %v %v\n", e.fileLen, e.shardlen)))
	str, err = e.stdoutb.ReadString('\n')
	if err != nil {
		panic(err)
	}
	if !strings.HasPrefix(str, "+") {
		panic("unexpected output " + str)
	}
}

func (e execFecEngineDecoderV2) PutShard(blockid uint32, block []byte) bool {
	if int32(len(block)) != e.shardlen {
		return false
	}
	e.stdin.Write([]byte(fmt.Sprintf("%v %v\n", blockid, len(block))))
	n, err := io.Copy(e.stdin, bytes.NewReader(block))
	if n != int64(len(block)) {
		panic("incorrect length copied" + err.Error())
	}
	str, err := e.stdoutb.ReadString('\n')
	if err != nil {
		panic(err)
	}
	if strings.HasPrefix(str, "+") {
		return true
	}
	return false
}

func (e execFecEngineDecoderV2) GetIn() io.Reader {
	e.stdin.Write([]byte(fmt.Sprintf("RECOVER\n")))
	str, err := e.stdoutb.ReadString('\n')
	if err != nil {
		panic(err)
	}
	if !strings.HasPrefix(str, "+") {
		panic("unexpected output" + err.Error())
	}
	fmt.Println(str, "recordsize", e.fileLen)
	return io.LimitReader(e.stdoutb, e.fileLen)
}

func (e execFecEngineDecoderV2) Close() {
	e.stdin.Write([]byte(fmt.Sprintf("QUIT\n")))
	e.stdin.Close()
	e.cmd.Wait()
}
