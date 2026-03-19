package fileParts

import (
	"io"
	"os"
)

func NewPartedFile(file *os.File, partSize uint32) *PartedFile {
	fileInfo, err := file.Stat()
	if err != nil {
		panic(err)
	}
	return &PartedFile{File: file, PartSize: partSize, FileTotalSize: uint64(fileInfo.Size())}
}

type PartedFile struct {
	*os.File
	FileTotalSize uint64
	PartSize      uint32
}

func (pf *PartedFile) GetTotalParts() uint32 {
	return uint32((pf.FileTotalSize + uint64(pf.PartSize) - 1) / uint64(pf.PartSize))
}

func (pf *PartedFile) GetPartSize(part uint32) uint32 {
	if part == pf.GetTotalParts()-1 {
		lastPartSize := uint32(pf.FileTotalSize % uint64(pf.PartSize))
		if lastPartSize == 0 {
			return pf.PartSize
		}
		return lastPartSize
	}
	return pf.PartSize
}

func (pf *PartedFile) GetPartReader(part uint32) (io.Reader, error) {
	pf.Seek(int64(part)*int64(pf.PartSize), 0)
	return io.LimitReader(pf.File, int64(pf.GetPartSize(part))), nil
}
