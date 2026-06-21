package gomodstore

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

const (
	manifestVersion = 1
	goDirective     = "1.20"
)

var zipTimestamp = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

type ArtifactSet struct {
	Module   string
	Version  string
	Time     time.Time
	Info     []byte
	Mod      []byte
	Zip      []byte
	Manifest Manifest
}

type Manifest struct {
	Version       int             `json:"version"`
	Module        string          `json:"module"`
	ModuleVersion string          `json:"module_version"`
	Length        int64           `json:"length"`
	SHA256        string          `json:"sha256"`
	ChunkSize     int             `json:"chunk_size"`
	Chunks        []ManifestChunk `json:"chunks"`
}

type ManifestChunk struct {
	Index  int    `json:"index"`
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
}

type moduleInfo struct {
	Version string    `json:"Version"`
	Time    time.Time `json:"Time"`
}

func BuildArtifacts(prefix string, payload []byte, published time.Time, chunkSize int) (*ArtifactSet, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	module, err := ModulePathForPayload(prefix, payload)
	if err != nil {
		return nil, err
	}
	published = published.UTC().Truncate(time.Second)
	manifest := BuildManifest(module, payload, chunkSize)

	info, err := json.Marshal(moduleInfo{Version: ModuleVersion, Time: published})
	if err != nil {
		return nil, err
	}
	info = append(info, '\n')

	mod := []byte(ModuleFile(module))
	zipBytes, err := BuildModuleZip(module, manifest, payload)
	if err != nil {
		return nil, err
	}

	return &ArtifactSet{
		Module:   module,
		Version:  ModuleVersion,
		Time:     published,
		Info:     info,
		Mod:      mod,
		Zip:      zipBytes,
		Manifest: manifest,
	}, nil
}

func BuildManifest(module string, payload []byte, chunkSize int) Manifest {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	sum := sha256.Sum256(payload)
	manifest := Manifest{
		Version:       manifestVersion,
		Module:        module,
		ModuleVersion: ModuleVersion,
		Length:        int64(len(payload)),
		SHA256:        hex.EncodeToString(sum[:]),
		ChunkSize:     chunkSize,
	}
	for offset, index := 0, 0; offset < len(payload); offset, index = offset+chunkSize, index+1 {
		end := offset + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[offset:end]
		chunkSum := sha256.Sum256(chunk)
		manifest.Chunks = append(manifest.Chunks, ManifestChunk{
			Index:  index,
			Path:   fmt.Sprintf("gomodstore/chunks/%06d.bin", index),
			Offset: int64(offset),
			Length: len(chunk),
			SHA256: hex.EncodeToString(chunkSum[:]),
		})
	}
	return manifest
}

func ModuleFile(module string) string {
	return "module " + module + "\n\ngo " + goDirective + "\n"
}

func BuildModuleZip(module string, manifest Manifest, payload []byte) ([]byte, error) {
	if err := ValidateModulePath(module); err != nil {
		return nil, err
	}
	if manifest.Module != module {
		return nil, errors.New("manifest module does not match zip module")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	root := module + "@" + ModuleVersion + "/"

	if err := writeZipFile(zw, root+"go.mod", []byte(ModuleFile(module))); err != nil {
		return nil, err
	}
	if err := writeZipFile(zw, root+"LICENSE", []byte(MITLicenseText)); err != nil {
		return nil, err
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestJSON = append(manifestJSON, '\n')
	if err := writeZipFile(zw, root+"gomodstore/manifest.json", manifestJSON); err != nil {
		return nil, err
	}
	for _, chunk := range manifest.Chunks {
		if err := validateManifestChunkPath(chunk.Path); err != nil {
			return nil, err
		}
		if chunk.Offset < 0 || chunk.Length < 0 || chunk.Offset+int64(chunk.Length) > int64(len(payload)) {
			return nil, errors.New("manifest chunk range is outside payload")
		}
		data := payload[chunk.Offset : chunk.Offset+int64(chunk.Length)]
		if err := writeZipFile(zw, root+chunk.Path, data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecodeModuleZip(zipBytes []byte) ([]byte, *Manifest, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, nil, err
	}
	files := make(map[string]*zip.File, len(zr.File))
	var manifestName string
	for _, file := range zr.File {
		files[file.Name] = file
		if strings.HasSuffix(file.Name, "/gomodstore/manifest.json") {
			if manifestName != "" {
				return nil, nil, errors.New("zip contains multiple gomodstore manifests")
			}
			manifestName = file.Name
		}
	}
	if manifestName == "" {
		return nil, nil, errors.New("zip does not contain gomodstore manifest")
	}
	root := strings.TrimSuffix(manifestName, "gomodstore/manifest.json")

	manifestBytes, err := readZipFile(files[manifestName])
	if err != nil {
		return nil, nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, nil, err
	}
	if manifest.Version != manifestVersion {
		return nil, nil, fmt.Errorf("unsupported manifest version %d", manifest.Version)
	}
	if err := ValidateModulePath(manifest.Module); err != nil {
		return nil, nil, err
	}

	var payload bytes.Buffer
	for i, chunk := range manifest.Chunks {
		if chunk.Index != i {
			return nil, nil, errors.New("manifest chunks are not in index order")
		}
		if chunk.Offset != int64(payload.Len()) {
			return nil, nil, errors.New("manifest chunk offsets are not contiguous")
		}
		if err := validateManifestChunkPath(chunk.Path); err != nil {
			return nil, nil, err
		}
		file := files[root+chunk.Path]
		if file == nil {
			return nil, nil, fmt.Errorf("zip is missing chunk %s", chunk.Path)
		}
		data, err := readZipFile(file)
		if err != nil {
			return nil, nil, err
		}
		if len(data) != chunk.Length {
			return nil, nil, fmt.Errorf("chunk %s length mismatch", chunk.Path)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != chunk.SHA256 {
			return nil, nil, fmt.Errorf("chunk %s SHA-256 mismatch", chunk.Path)
		}
		if _, err := payload.Write(data); err != nil {
			return nil, nil, err
		}
	}
	if int64(payload.Len()) != manifest.Length {
		return nil, nil, errors.New("payload length does not match manifest")
	}
	sum := sha256.Sum256(payload.Bytes())
	if hex.EncodeToString(sum[:]) != manifest.SHA256 {
		return nil, nil, errors.New("payload SHA-256 does not match manifest")
	}
	return payload.Bytes(), &manifest, nil
}

func writeZipFile(zw *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{
		Name:     name,
		Method:   zip.Store,
		Modified: zipTimestamp,
	}
	header.SetMode(0644)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readZipFile(file *zip.File) ([]byte, error) {
	if file == nil {
		return nil, errors.New("zip file entry is missing")
	}
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func validateManifestChunkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") {
		return errors.New("manifest chunk path must be relative")
	}
	if path.Clean(p) != p {
		return errors.New("manifest chunk path is not clean")
	}
	if !strings.HasPrefix(p, "gomodstore/chunks/") {
		return errors.New("manifest chunk path must be under gomodstore/chunks")
	}
	return nil
}

const MITLicenseText = `MIT License

Copyright (c) 2026 gomodstore

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`
