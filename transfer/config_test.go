package transfer

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiaokangwang/fastTransfer/interfacew"
)

type sizingEngine struct {
	maxInputSize uint32
}

func (s sizingEngine) GetEncoder(in io.Reader, shardLen int32) interfacew.FECEngineEncoder {
	panic("not used in test")
}

func (s sizingEngine) GetEncoder3(in io.Reader, inLen int64, shardLen int32) interfacew.FECEngineEncoder {
	panic("not used in test")
}

func (s sizingEngine) GetDecoder(inLen int64, shardLen int32) interfacew.FECEngineDecoder {
	panic("not used in test")
}

func (s sizingEngine) MaxInputSize(shardLen int32) uint32 {
	return s.maxInputSize
}

type fallbackEngine struct{ sizingEngine }

func TestMaxPartSizeForEngine(t *testing.T) {
	t.Parallel()

	partSize := MaxPartSizeForEngine(sizingEngine{maxInputSize: 4096 * DefaultShardSize}, DefaultShardSize)
	if got, want := partSize, uint32(4096*1300); got != want {
		t.Fatalf("unexpected max part size: got %d want %d", got, want)
	}
	if got, want := MaxPartSizeForEngine(fallbackEngine{}, DefaultShardSize), LegacyMaxPartSize; got != want {
		t.Fatalf("unexpected fallback max part size: got %d want %d", got, want)
	}
}

func TestPrepareFileRequestUsesEnginePartSize(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const partSize = 2048
	if err := file.Truncate(int64(partSize) + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	server := &Server{
		engine: sizingEngine{maxInputSize: partSize},
	}
	_, closer, fileSize, totalParts, payloadType, err := server.prepareFileRequest(path, 0, DefaultShardSize)
	if closer != nil {
		defer closer.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if got, want := payloadType, PayloadTypeFile; got != want {
		t.Fatalf("unexpected payload type: got %d want %d", got, want)
	}
	if got, want := fileSize, uint64(partSize); got != want {
		t.Fatalf("unexpected first part size: got %d want %d", got, want)
	}
	if got, want := totalParts, uint32(2); got != want {
		t.Fatalf("unexpected total parts: got %d want %d", got, want)
	}
}
