package ampfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	legacyManifestVersion = 1
	manifestVersion       = 2
	maxSymbolIndex        = 0xefffffff
	cborMajorUint         = 0
	cborMajorBytes        = 2
	cborMajorText         = 3
	cborMajorArray        = 4
)

type RootManifest struct {
	Version            uint64
	RunID              string
	FileName           string
	FileSize           uint64
	FileSHA256         []byte
	ResourceBase       string
	CacheDomain        string
	ChunkSize          uint64
	SymbolSize         uint64
	MinRecoverySymbols uint64
	FECTotalMillis     uint64
	ChunkCount         uint64
	ChunksPerPage      uint64
	Pages              []ManifestPageRef
}

type ManifestPageRef struct {
	Index    uint64
	CacheURL string
}

type PageManifest struct {
	Version    uint64
	RunID      string
	PageIndex  uint64
	FirstChunk uint64
	Chunks     []ChunkManifest
}

type ChunkManifest struct {
	Index        uint64
	Offset       uint64
	Length       uint64
	SHA256       []byte
	SymbolRanges []SymbolRange
}

type SymbolRange struct {
	Start uint64
	End   uint64
}

func (c ChunkManifest) AvailableSymbols() uint64 {
	ids, err := expandSymbolRanges(c.SymbolRanges)
	if err != nil {
		return 0
	}
	return uint64(len(ids))
}

func symbolRangesForInitialUpload(total uint64) []SymbolRange {
	if total == 0 {
		return nil
	}
	return []SymbolRange{{Start: 0, End: total - 1}}
}

func expandSymbolRanges(ranges []SymbolRange) ([]uint64, error) {
	ids := make([]uint64, 0)
	seen := make(map[uint64]struct{})
	for _, r := range ranges {
		if r.Start > r.End {
			return nil, errors.New("symbol range start is greater than end")
		}
		if r.End > maxSymbolIndex {
			return nil, fmt.Errorf("symbol range end %d exceeds max %d", r.End, uint64(maxSymbolIndex))
		}
		for id := r.Start; id <= r.End; id++ {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
			if id == r.End {
				break
			}
		}
	}
	return ids, nil
}

func encodeRootManifest(root RootManifest) []byte {
	var w cborWriter
	if root.Version == legacyManifestVersion {
		w.array(14)
	} else {
		w.array(12)
	}
	w.uint(root.Version)
	w.text(root.RunID)
	w.text(root.FileName)
	w.uint(root.FileSize)
	w.bytes(root.FileSHA256)
	w.text(root.ResourceBase)
	w.text(root.CacheDomain)
	w.uint(root.ChunkSize)
	w.uint(root.SymbolSize)
	if root.Version == legacyManifestVersion {
		w.uint(root.MinRecoverySymbols)
		w.uint(root.FECTotalMillis)
	}
	w.uint(root.ChunkCount)
	w.uint(root.ChunksPerPage)
	w.array(uint64(len(root.Pages)))
	for _, page := range root.Pages {
		w.array(2)
		w.uint(page.Index)
		w.text(page.CacheURL)
	}
	return w.buf.Bytes()
}

func decodeRootManifest(data []byte) (RootManifest, error) {
	r := newCBORReader(data)
	n, err := r.array()
	if err != nil {
		return RootManifest{}, err
	}
	var root RootManifest
	if root.Version, err = r.uint(); err != nil {
		return RootManifest{}, err
	}
	switch root.Version {
	case legacyManifestVersion:
		if n != 14 {
			return RootManifest{}, fmt.Errorf("root manifest v1 field count = %d, want 14", n)
		}
	case manifestVersion:
		if n != 12 {
			return RootManifest{}, fmt.Errorf("root manifest v2 field count = %d, want 12", n)
		}
	default:
		return RootManifest{}, fmt.Errorf("unsupported root manifest version %d", root.Version)
	}
	if root.RunID, err = r.text(); err != nil {
		return RootManifest{}, err
	}
	if root.FileName, err = r.text(); err != nil {
		return RootManifest{}, err
	}
	if root.FileSize, err = r.uint(); err != nil {
		return RootManifest{}, err
	}
	if root.FileSHA256, err = r.bytes(); err != nil {
		return RootManifest{}, err
	}
	if root.ResourceBase, err = r.text(); err != nil {
		return RootManifest{}, err
	}
	if root.CacheDomain, err = r.text(); err != nil {
		return RootManifest{}, err
	}
	if root.ChunkSize, err = r.uint(); err != nil {
		return RootManifest{}, err
	}
	if root.SymbolSize, err = r.uint(); err != nil {
		return RootManifest{}, err
	}
	if root.Version == legacyManifestVersion {
		if root.MinRecoverySymbols, err = r.uint(); err != nil {
			return RootManifest{}, err
		}
		if root.FECTotalMillis, err = r.uint(); err != nil {
			return RootManifest{}, err
		}
	}
	if root.ChunkCount, err = r.uint(); err != nil {
		return RootManifest{}, err
	}
	if root.ChunksPerPage, err = r.uint(); err != nil {
		return RootManifest{}, err
	}
	pageCount, err := r.array()
	if err != nil {
		return RootManifest{}, err
	}
	root.Pages = make([]ManifestPageRef, 0, pageCount)
	for i := uint64(0); i < pageCount; i++ {
		if n, err := r.array(); err != nil {
			return RootManifest{}, err
		} else if n != 2 {
			return RootManifest{}, fmt.Errorf("page ref field count = %d, want 2", n)
		}
		index, err := r.uint()
		if err != nil {
			return RootManifest{}, err
		}
		cacheURL, err := r.text()
		if err != nil {
			return RootManifest{}, err
		}
		root.Pages = append(root.Pages, ManifestPageRef{Index: index, CacheURL: cacheURL})
	}
	if !r.done() {
		return RootManifest{}, errors.New("trailing bytes after root manifest")
	}
	if len(root.FileSHA256) != 32 {
		return RootManifest{}, errors.New("root manifest file hash must be 32 bytes")
	}
	return root, nil
}

func encodePageManifest(page PageManifest) []byte {
	var w cborWriter
	w.array(5)
	w.uint(page.Version)
	w.text(page.RunID)
	w.uint(page.PageIndex)
	w.uint(page.FirstChunk)
	w.array(uint64(len(page.Chunks)))
	for _, chunk := range page.Chunks {
		if page.Version == legacyManifestVersion {
			w.array(4)
		} else {
			w.array(5)
		}
		w.uint(chunk.Index)
		w.uint(chunk.Offset)
		w.uint(chunk.Length)
		w.bytes(chunk.SHA256)
		if page.Version != legacyManifestVersion {
			w.array(uint64(len(chunk.SymbolRanges)))
			for _, r := range chunk.SymbolRanges {
				w.array(2)
				w.uint(r.Start)
				w.uint(r.End)
			}
		}
	}
	return w.buf.Bytes()
}

func decodePageManifest(data []byte) (PageManifest, error) {
	r := newCBORReader(data)
	if n, err := r.array(); err != nil {
		return PageManifest{}, err
	} else if n != 5 {
		return PageManifest{}, fmt.Errorf("page manifest field count = %d, want 5", n)
	}
	var page PageManifest
	var err error
	if page.Version, err = r.uint(); err != nil {
		return PageManifest{}, err
	}
	if page.Version != legacyManifestVersion && page.Version != manifestVersion {
		return PageManifest{}, fmt.Errorf("unsupported page manifest version %d", page.Version)
	}
	if page.RunID, err = r.text(); err != nil {
		return PageManifest{}, err
	}
	if page.PageIndex, err = r.uint(); err != nil {
		return PageManifest{}, err
	}
	if page.FirstChunk, err = r.uint(); err != nil {
		return PageManifest{}, err
	}
	chunkCount, err := r.array()
	if err != nil {
		return PageManifest{}, err
	}
	page.Chunks = make([]ChunkManifest, 0, chunkCount)
	for i := uint64(0); i < chunkCount; i++ {
		n, err := r.array()
		if err != nil {
			return PageManifest{}, err
		}
		if page.Version == legacyManifestVersion && n != 4 {
			return PageManifest{}, fmt.Errorf("chunk v1 field count = %d, want 4", n)
		}
		if page.Version == manifestVersion && n != 5 {
			return PageManifest{}, fmt.Errorf("chunk v2 field count = %d, want 5", n)
		}
		var chunk ChunkManifest
		if chunk.Index, err = r.uint(); err != nil {
			return PageManifest{}, err
		}
		if chunk.Offset, err = r.uint(); err != nil {
			return PageManifest{}, err
		}
		if chunk.Length, err = r.uint(); err != nil {
			return PageManifest{}, err
		}
		if chunk.SHA256, err = r.bytes(); err != nil {
			return PageManifest{}, err
		}
		if len(chunk.SHA256) != 32 {
			return PageManifest{}, errors.New("chunk hash must be 32 bytes")
		}
		if page.Version == manifestVersion {
			rangeCount, err := r.array()
			if err != nil {
				return PageManifest{}, err
			}
			chunk.SymbolRanges = make([]SymbolRange, 0, rangeCount)
			for j := uint64(0); j < rangeCount; j++ {
				if n, err := r.array(); err != nil {
					return PageManifest{}, err
				} else if n != 2 {
					return PageManifest{}, fmt.Errorf("symbol range field count = %d, want 2", n)
				}
				start, err := r.uint()
				if err != nil {
					return PageManifest{}, err
				}
				end, err := r.uint()
				if err != nil {
					return PageManifest{}, err
				}
				if start > end {
					return PageManifest{}, errors.New("symbol range start is greater than end")
				}
				if end > maxSymbolIndex {
					return PageManifest{}, fmt.Errorf("symbol range end %d exceeds max %d", end, uint64(maxSymbolIndex))
				}
				chunk.SymbolRanges = append(chunk.SymbolRanges, SymbolRange{Start: start, End: end})
			}
			if chunk.Length > 0 && len(chunk.SymbolRanges) == 0 {
				return PageManifest{}, errors.New("non-empty chunk has no symbol ranges")
			}
			if chunk.Length == 0 && len(chunk.SymbolRanges) > 0 {
				return PageManifest{}, errors.New("empty chunk has symbol ranges")
			}
		}
		page.Chunks = append(page.Chunks, chunk)
	}
	if !r.done() {
		return PageManifest{}, errors.New("trailing bytes after page manifest")
	}
	return page, nil
}

type cborWriter struct {
	buf bytes.Buffer
}

func (w *cborWriter) uint(v uint64) {
	w.header(cborMajorUint, v)
}

func (w *cborWriter) bytes(v []byte) {
	w.header(cborMajorBytes, uint64(len(v)))
	w.buf.Write(v)
}

func (w *cborWriter) text(v string) {
	w.header(cborMajorText, uint64(len(v)))
	w.buf.WriteString(v)
}

func (w *cborWriter) array(n uint64) {
	w.header(cborMajorArray, n)
}

func (w *cborWriter) header(major byte, value uint64) {
	prefix := major << 5
	switch {
	case value < 24:
		w.buf.WriteByte(prefix | byte(value))
	case value <= 0xff:
		w.buf.WriteByte(prefix | 24)
		w.buf.WriteByte(byte(value))
	case value <= 0xffff:
		w.buf.WriteByte(prefix | 25)
		var tmp [2]byte
		binary.BigEndian.PutUint16(tmp[:], uint16(value))
		w.buf.Write(tmp[:])
	case value <= 0xffffffff:
		w.buf.WriteByte(prefix | 26)
		var tmp [4]byte
		binary.BigEndian.PutUint32(tmp[:], uint32(value))
		w.buf.Write(tmp[:])
	default:
		w.buf.WriteByte(prefix | 27)
		var tmp [8]byte
		binary.BigEndian.PutUint64(tmp[:], value)
		w.buf.Write(tmp[:])
	}
}

type cborReader struct {
	data []byte
	pos  int
}

func newCBORReader(data []byte) *cborReader {
	return &cborReader{data: data}
}

func (r *cborReader) done() bool {
	return r.pos == len(r.data)
}

func (r *cborReader) uint() (uint64, error) {
	major, value, err := r.header()
	if err != nil {
		return 0, err
	}
	if major != cborMajorUint {
		return 0, fmt.Errorf("cbor major = %d, want uint", major)
	}
	return value, nil
}

func (r *cborReader) bytes() ([]byte, error) {
	major, n, err := r.header()
	if err != nil {
		return nil, err
	}
	if major != cborMajorBytes {
		return nil, fmt.Errorf("cbor major = %d, want bytes", major)
	}
	if n > uint64(len(r.data)-r.pos) {
		return nil, io.ErrUnexpectedEOF
	}
	out := append([]byte(nil), r.data[r.pos:r.pos+int(n)]...)
	r.pos += int(n)
	return out, nil
}

func (r *cborReader) text() (string, error) {
	b, err := r.bytesLike(cborMajorText)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (r *cborReader) array() (uint64, error) {
	major, n, err := r.header()
	if err != nil {
		return 0, err
	}
	if major != cborMajorArray {
		return 0, fmt.Errorf("cbor major = %d, want array", major)
	}
	return n, nil
}

func (r *cborReader) bytesLike(wantMajor byte) ([]byte, error) {
	major, n, err := r.header()
	if err != nil {
		return nil, err
	}
	if major != wantMajor {
		return nil, fmt.Errorf("cbor major = %d, want %d", major, wantMajor)
	}
	if n > uint64(len(r.data)-r.pos) {
		return nil, io.ErrUnexpectedEOF
	}
	out := append([]byte(nil), r.data[r.pos:r.pos+int(n)]...)
	r.pos += int(n)
	return out, nil
}

func (r *cborReader) header() (major byte, value uint64, err error) {
	if r.pos >= len(r.data) {
		return 0, 0, io.ErrUnexpectedEOF
	}
	initial := r.data[r.pos]
	r.pos++
	major = initial >> 5
	additional := initial & 0x1f
	switch {
	case additional < 24:
		return major, uint64(additional), nil
	case additional == 24:
		if r.pos+1 > len(r.data) {
			return 0, 0, io.ErrUnexpectedEOF
		}
		value = uint64(r.data[r.pos])
		r.pos++
		return major, value, nil
	case additional == 25:
		if r.pos+2 > len(r.data) {
			return 0, 0, io.ErrUnexpectedEOF
		}
		value = uint64(binary.BigEndian.Uint16(r.data[r.pos : r.pos+2]))
		r.pos += 2
		return major, value, nil
	case additional == 26:
		if r.pos+4 > len(r.data) {
			return 0, 0, io.ErrUnexpectedEOF
		}
		value = uint64(binary.BigEndian.Uint32(r.data[r.pos : r.pos+4]))
		r.pos += 4
		return major, value, nil
	case additional == 27:
		if r.pos+8 > len(r.data) {
			return 0, 0, io.ErrUnexpectedEOF
		}
		value = binary.BigEndian.Uint64(r.data[r.pos : r.pos+8])
		r.pos += 8
		return major, value, nil
	default:
		return 0, 0, fmt.Errorf("unsupported cbor additional info %d", additional)
	}
}
