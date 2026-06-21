package gomodstore

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"
)

func TestValidateModulePaths(t *testing.T) {
	payload := []byte("payload")
	module, err := ModulePathForPayload("example.com/store", payload)
	if err != nil {
		t.Fatalf("ModulePathForPayload returned error: %v", err)
	}
	if err := ValidateModulePath(module); err != nil {
		t.Fatalf("generated module path did not validate: %v", err)
	}

	invalidPrefixes := []string{
		"",
		"https://example.com/store",
		"example/store",
		"Example.com/store",
		"example_com/store",
		"example.com//store",
		"example.com/store/",
		"example.com/../store",
	}
	for _, prefix := range invalidPrefixes {
		if err := ValidateModulePrefix(prefix); err == nil {
			t.Fatalf("ValidateModulePrefix(%q) succeeded, want error", prefix)
		}
	}

	invalidModules := []string{
		"example.com/store/not-a-hash",
		"example.com/store/" + strings.Repeat("g", 64),
		"example.com/store/" + strings.Repeat("A", 64),
	}
	for _, module := range invalidModules {
		if err := ValidateModulePath(module); err == nil {
			t.Fatalf("ValidateModulePath(%q) succeeded, want error", module)
		}
	}
}

func TestBuildArtifactsDecodeAndManifest(t *testing.T) {
	payload := []byte("abcdefghijklmnopqrstuvwxyz")
	artifacts, err := BuildArtifacts("example.com/store", payload, time.Date(2026, 6, 11, 12, 0, 0, 123, time.UTC), 7)
	if err != nil {
		t.Fatalf("BuildArtifacts returned error: %v", err)
	}
	if artifacts.Version != ModuleVersion {
		t.Fatalf("version = %q, want %q", artifacts.Version, ModuleVersion)
	}
	if !strings.HasPrefix(string(artifacts.Mod), "module "+artifacts.Module+"\n") {
		t.Fatalf("go.mod does not declare generated module: %s", artifacts.Mod)
	}

	manifest := artifacts.Manifest
	if manifest.Length != int64(len(payload)) {
		t.Fatalf("manifest length = %d, want %d", manifest.Length, len(payload))
	}
	sum := sha256.Sum256(payload)
	if manifest.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("manifest payload hash mismatch")
	}
	if got, want := len(manifest.Chunks), 4; got != want {
		t.Fatalf("chunk count = %d, want %d", got, want)
	}
	for i, chunk := range manifest.Chunks {
		if chunk.Index != i {
			t.Fatalf("chunk %d index = %d", i, chunk.Index)
		}
		if !strings.HasPrefix(chunk.Path, "gomodstore/chunks/") {
			t.Fatalf("chunk path %q is outside chunk directory", chunk.Path)
		}
	}

	decoded, decodedManifest, err := DecodeModuleZip(artifacts.Zip)
	if err != nil {
		t.Fatalf("DecodeModuleZip returned error: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded payload = %q, want %q", decoded, payload)
	}
	if decodedManifest.SHA256 != manifest.SHA256 {
		t.Fatalf("decoded manifest hash = %q, want %q", decodedManifest.SHA256, manifest.SHA256)
	}
}

func TestDeterministicZipAndMITLicense(t *testing.T) {
	payload := []byte("license-visible payload")
	first, err := BuildArtifacts("example.com/store", payload, time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC), 5)
	if err != nil {
		t.Fatalf("BuildArtifacts first returned error: %v", err)
	}
	second, err := BuildArtifacts("example.com/store", payload, time.Date(2030, 1, 1, 1, 2, 3, 0, time.UTC), 5)
	if err != nil {
		t.Fatalf("BuildArtifacts second returned error: %v", err)
	}
	if !bytes.Equal(first.Zip, second.Zip) {
		t.Fatalf("zip output changed for same module payload")
	}

	license := readZipEntry(t, first.Zip, first.Module+"@"+ModuleVersion+"/LICENSE")
	if string(license) != MITLicenseText {
		t.Fatalf("LICENSE entry did not contain full MIT license text")
	}
	if !bytes.Contains(license, []byte("Permission is hereby granted")) ||
		!bytes.Contains(license, []byte("THE SOFTWARE IS PROVIDED \"AS IS\"")) {
		t.Fatalf("LICENSE entry does not look license-detectable")
	}
}

func TestDecodeRejectsTamperedZip(t *testing.T) {
	payload := []byte("tamper me")
	artifacts, err := BuildArtifacts("example.com/store", payload, time.Now(), 32)
	if err != nil {
		t.Fatalf("BuildArtifacts returned error: %v", err)
	}
	tampered := append([]byte(nil), artifacts.Zip...)
	i := bytes.Index(tampered, []byte("tamper me"))
	if i < 0 {
		t.Fatalf("could not find stored payload bytes in zip")
	}
	tampered[i] = 'T'
	if _, _, err := DecodeModuleZip(tampered); err == nil {
		t.Fatalf("DecodeModuleZip succeeded for tampered zip, want error")
	}
}

func readZipEntry(t *testing.T, zipBytes []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("zip.NewReader returned error: %v", err)
	}
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return data
	}
	t.Fatalf("zip entry %s not found", name)
	return nil
}
