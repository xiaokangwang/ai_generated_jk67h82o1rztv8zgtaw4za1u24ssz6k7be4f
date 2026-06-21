package ampcache

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTripAllModes(t *testing.T) {
	payload := []byte("payload bytes \x00 \xff")
	cases := []struct {
		enc Encoding
		url string
	}{
		{EncodingHTML, "https://example.com/store/payload.amp.html"},
		{EncodingFont, "https://example.com/store/payload.woff2"},
		{EncodingImage, "https://example.com/store/payload.png"},
	}
	for _, tc := range cases {
		t.Run(string(tc.enc), func(t *testing.T) {
			resource, manifest, err := EncodeResource(payload, tc.enc, tc.url)
			if err != nil {
				t.Fatalf("EncodeResource returned error: %v", err)
			}
			if manifest.ResourceURL != tc.url {
				t.Fatalf("manifest resource URL = %q, want %q", manifest.ResourceURL, tc.url)
			}
			decoded, decodedManifest, err := DecodeResource(resource, tc.enc)
			if err != nil {
				t.Fatalf("DecodeResource returned error: %v", err)
			}
			if !bytes.Equal(decoded, payload) {
				t.Fatalf("decoded payload = %q, want %q", decoded, payload)
			}
			if decodedManifest.SHA256 != manifest.SHA256 {
				t.Fatalf("decoded sha = %q, want %q", decodedManifest.SHA256, manifest.SHA256)
			}
		})
	}
}

func TestHTMLResourceContainsAMPMarkers(t *testing.T) {
	resource, _, err := EncodeResource([]byte("html payload"), EncodingHTML, "https://example.com/data/item.html")
	if err != nil {
		t.Fatalf("EncodeResource returned error: %v", err)
	}
	html := string(resource)
	for _, needle := range []string{
		"<html amp",
		`<script async src="https://cdn.ampproject.org/v0.js"></script>`,
		"amp-boilerplate",
		`<link rel="canonical" href="https://example.com/data/item.html">`,
		`id="ampcache-payload"`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("html resource did not contain %q", needle)
		}
	}
}

func TestFontResourceIsValidSFNTWrapper(t *testing.T) {
	payload := []byte("font payload")
	resource, _, err := EncodeResource(payload, EncodingFont, "https://example.com/data/item.ttf")
	if err != nil {
		t.Fatalf("EncodeResource returned error: %v", err)
	}
	if got, want := binary.BigEndian.Uint32(resource[:4]), uint32(0x00010000); got != want {
		t.Fatalf("sfntVersion = %#x, want %#x", got, want)
	}
	if _, _, err := sfntTableRange(resource, "head"); err != nil {
		t.Fatalf("head table missing: %v", err)
	}
	if _, _, err := sfntTableRange(resource, "AMPC"); err == nil {
		t.Fatalf("font resource still contains private AMPC table")
	}
	decoded, _, err := DecodeResource(resource, EncodingFont)
	if err != nil {
		t.Fatalf("DecodeResource returned error: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded font payload = %q, want %q", decoded, payload)
	}
	for _, forbidden := range [][]byte{
		[]byte(`"payload"`),
		[]byte(`"sha256"`),
		[]byte(`"resource_url"`),
		[]byte("ampcache-resource"),
	} {
		if bytes.Contains(resource, forbidden) {
			t.Fatalf("font resource still contains JSON metadata marker %q", forbidden)
		}
	}
	glyfOffset, glyfLen, err := sfntTableRange(resource, "glyf")
	if err != nil {
		t.Fatalf("glyf table missing: %v", err)
	}
	if glyfLen == 0 {
		t.Fatalf("glyf table is empty")
	}
	if got, want := binary.BigEndian.Uint16(resource[glyfOffset:glyfOffset+2]), uint16(1); got != want {
		t.Fatalf("first glyph contour count = %d, want %d", got, want)
	}
	locaOffset, locaLen, err := sfntTableRange(resource, "loca")
	if err != nil {
		t.Fatalf("loca table missing: %v", err)
	}
	if got, want := locaLen, 12; got != want {
		t.Fatalf("loca table length = %d, want %d", got, want)
	}
	loca := resource[locaOffset : locaOffset+locaLen]
	secondGlyphOffset := int(binary.BigEndian.Uint32(loca[4:8]))
	endOffset := int(binary.BigEndian.Uint32(loca[8:12]))
	if secondGlyphOffset <= 0 {
		t.Fatalf("loca second glyph offset = %d, want positive", secondGlyphOffset)
	}
	if endOffset != glyfLen {
		t.Fatalf("loca final offset = %d, want glyf length %d", endOffset, glyfLen)
	}
	maxpOffset, _, err := sfntTableRange(resource, "maxp")
	if err != nil {
		t.Fatalf("maxp table missing: %v", err)
	}
	if got, want := binary.BigEndian.Uint16(resource[maxpOffset+4:maxpOffset+6]), uint16(2); got != want {
		t.Fatalf("maxp numGlyphs = %d, want %d", got, want)
	}
	cmapOffset, cmapLen, err := sfntTableRange(resource, "cmap")
	if err != nil {
		t.Fatalf("cmap table missing: %v", err)
	}
	cmap := resource[cmapOffset : cmapOffset+cmapLen]
	if len(cmap) < 20 {
		t.Fatalf("cmap table length = %d, want at least 20", len(cmap))
	}
	subtableOffset := int(binary.BigEndian.Uint32(cmap[8:12]))
	if subtableOffset < 0 || subtableOffset+4 > len(cmap) {
		t.Fatalf("cmap subtable offset = %d outside table length %d", subtableOffset, len(cmap))
	}
	if got, want := binary.BigEndian.Uint16(cmap[subtableOffset:subtableOffset+2]), uint16(4); got != want {
		t.Fatalf("cmap format = %d, want %d", got, want)
	}
	subtableLen := int(binary.BigEndian.Uint16(cmap[subtableOffset+2 : subtableOffset+4]))
	if got, want := subtableOffset+subtableLen, len(cmap); got != want {
		t.Fatalf("cmap subtable end = %d, want table length %d", got, want)
	}
	if got, want := ContentType(EncodingFont), "font/ttf"; got != want {
		t.Fatalf("font content type = %q, want %q", got, want)
	}
}

func TestFontResourceLargePayloadUsesGlyphCoordinates(t *testing.T) {
	payload := make([]byte, 512*1024+37)
	for i := range payload {
		payload[i] = byte(i*31 + 7)
	}
	resource, _, err := EncodeResource(payload, EncodingFont, "https://example.com/data/large.ttf")
	if err != nil {
		t.Fatalf("EncodeResource returned error: %v", err)
	}
	if _, _, err := sfntTableRange(resource, "AMPC"); err == nil {
		t.Fatalf("large font resource still contains private AMPC table")
	}
	decoded, _, err := DecodeResource(resource, EncodingFont)
	if err != nil {
		t.Fatalf("DecodeResource returned error: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded large payload mismatch")
	}
	if len(resource) < len(payload) {
		t.Fatalf("encoded resource length = %d, want at least payload length %d", len(resource), len(payload))
	}
	if len(resource) > len(payload)+64*1024 {
		t.Fatalf("encoded resource length = %d, unexpectedly large for payload length %d", len(resource), len(payload))
	}
}

func TestDecodeRejectsWrongEncoding(t *testing.T) {
	resource, _, err := EncodeResource([]byte("wrong mode"), EncodingFont, "https://example.com/data/item.woff2")
	if err != nil {
		t.Fatalf("EncodeResource returned error: %v", err)
	}
	if _, _, err := DecodeResource(resource, EncodingHTML); err == nil {
		t.Fatalf("DecodeResource succeeded with wrong encoding, want error")
	}
}
