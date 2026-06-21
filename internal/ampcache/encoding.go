package ampcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"io"
	"regexp"
	"sort"
)

var htmlPayloadRE = regexp.MustCompile(`(?s)<pre[^>]*id=["']ampcache-payload["'][^>]*>(.*?)</pre>`)

func EncodeResource(payload []byte, enc Encoding, resourceURL string) ([]byte, *Manifest, error) {
	if err := ValidateEncoding(enc); err != nil {
		return nil, nil, err
	}
	manifest := manifestForPayload(payload, enc, resourceURL)
	switch enc {
	case EncodingHTML:
		return encodeHTML(payload, resourceURL), manifest, nil
	case EncodingFont:
		fontBytes, err := encodeSFNT(payload)
		if err != nil {
			return nil, nil, err
		}
		return fontBytes, manifest, nil
	case EncodingImage:
		imageBytes, err := encodePNG(payload)
		if err != nil {
			return nil, nil, err
		}
		return imageBytes, manifest, nil
	default:
		return nil, nil, errors.New("unsupported encoding")
	}
}

func DecodeResource(resource []byte, enc Encoding) ([]byte, *Manifest, error) {
	if err := ValidateEncoding(enc); err != nil {
		return nil, nil, err
	}
	var container []byte
	var err error
	switch enc {
	case EncodingHTML:
		container, err = decodeHTML(resource)
	case EncodingFont:
		container, err = decodeSFNT(resource)
	case EncodingImage:
		container, err = decodePNG(resource)
	default:
		err = errors.New("unsupported encoding")
	}
	if err != nil {
		return nil, nil, err
	}
	return container, manifestForPayload(container, enc, ""), nil
}

func ContentType(enc Encoding) string {
	switch enc {
	case EncodingHTML:
		return "text/html; charset=utf-8"
	case EncodingFont:
		return "font/ttf"
	case EncodingImage:
		return "image/png"
	default:
		return "application/octet-stream"
	}
}

func manifestForPayload(payload []byte, enc Encoding, resourceURL string) *Manifest {
	sum := sha256.Sum256(payload)
	return &Manifest{
		Encoding:    string(enc),
		ResourceURL: resourceURL,
		Length:      int64(len(payload)),
		SHA256:      hex.EncodeToString(sum[:]),
	}
}

func encodeHTML(container []byte, resourceURL string) []byte {
	payload := base64.RawURLEncoding.EncodeToString(container)
	return []byte(`<!doctype html>
<html amp lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,minimum-scale=1,initial-scale=1">
<link rel="canonical" href="` + html.EscapeString(resourceURL) + `">
<title>ampcache</title>
<script async src="https://cdn.ampproject.org/v0.js"></script>
<style amp-boilerplate>body{-webkit-animation:-amp-start 8s steps(1,end) 0s 1 normal both;-moz-animation:-amp-start 8s steps(1,end) 0s 1 normal both;-ms-animation:-amp-start 8s steps(1,end) 0s 1 normal both;animation:-amp-start 8s steps(1,end) 0s 1 normal both}@-webkit-keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}@-moz-keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}@-ms-keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}@-o-keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}@keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}</style><noscript><style amp-boilerplate>body{-webkit-animation:none;-moz-animation:none;-ms-animation:none;animation:none}</style></noscript>
<style amp-custom>body{font-family:sans-serif}.ampcache{white-space:pre-wrap;word-break:break-all}</style>
</head>
<body>
<main>
<h1>ampcache</h1>
<pre class="ampcache" id="ampcache-payload">` + payload + `</pre>
</main>
</body>
</html>
`)
}

func decodeHTML(resource []byte) ([]byte, error) {
	match := htmlPayloadRE.FindSubmatch(resource)
	if len(match) != 2 {
		return nil, errors.New("html resource does not contain ampcache payload")
	}
	encoded := html.UnescapeString(string(bytes.TrimSpace(match[1])))
	return base64.RawURLEncoding.DecodeString(encoded)
}

type sfntTable struct {
	tag  string
	data []byte
}

const fontPayloadMaxPointsPerGlyph = 4096

func encodeSFNT(container []byte) ([]byte, error) {
	if len(container) > int(^uint32(0))-4 {
		return nil, errors.New("container is too large for font encoding")
	}
	glyf, offsets, numGlyphs, maxPoints, err := glyfTable(container)
	if err != nil {
		return nil, err
	}

	tables := []sfntTable{
		{tag: "OS/2", data: os2Table()},
		{tag: "cmap", data: cmapTable(numGlyphs)},
		{tag: "glyf", data: glyf},
		{tag: "head", data: headTable(0, 1)},
		{tag: "hhea", data: hheaTable(numGlyphs)},
		{tag: "hmtx", data: hmtxTable(numGlyphs)},
		{tag: "loca", data: locaTable(offsets)},
		{tag: "maxp", data: maxpTable(numGlyphs, maxPoints, 1)},
		{tag: "name", data: nameTable()},
		{tag: "post", data: postTable()},
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].tag < tables[j].tag })

	numTables := len(tables)
	headerLen := 12 + numTables*16
	offset := headerLen
	records := make([]struct {
		table    sfntTable
		checksum uint32
		offset   uint32
		length   uint32
	}, numTables)
	for i, table := range tables {
		records[i].table = table
		records[i].checksum = sfntChecksum(table.data)
		records[i].offset = uint32(offset)
		records[i].length = uint32(len(table.data))
		offset += paddedLen(len(table.data))
	}

	var out bytes.Buffer
	writeU32(&out, 0x00010000)
	writeU16(&out, uint16(numTables))
	searchRange, entrySelector, rangeShift := sfntSearchParams(numTables)
	writeU16(&out, searchRange)
	writeU16(&out, entrySelector)
	writeU16(&out, rangeShift)
	for _, record := range records {
		out.WriteString(record.table.tag)
		writeU32(&out, record.checksum)
		writeU32(&out, record.offset)
		writeU32(&out, record.length)
	}
	for _, record := range records {
		out.Write(record.table.data)
		for pad := paddedLen(len(record.table.data)) - len(record.table.data); pad > 0; pad-- {
			out.WriteByte(0)
		}
	}

	font := out.Bytes()
	adjustment := uint32(uint64(0xB1B0AFBA) - uint64(sfntChecksum(font)))
	headOffset, err := sfntTableOffset(font, "head")
	if err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(font[headOffset+8:headOffset+12], adjustment)
	return font, nil
}

func decodeSFNT(resource []byte) ([]byte, error) {
	if len(resource) < 12 {
		return nil, io.ErrUnexpectedEOF
	}
	version := binary.BigEndian.Uint32(resource[:4])
	if version != 0x00010000 && version != 0x4F54544F {
		return nil, fmt.Errorf("invalid sfntVersion: %d", version)
	}
	if container, err := decodeAMPCPayload(resource); err == nil {
		return container, nil
	}
	return decodeGlyphPayload(resource)
}

func decodeAMPCPayload(resource []byte) ([]byte, error) {
	offset, length, err := sfntTableRange(resource, "AMPC")
	if err != nil {
		return nil, err
	}
	if length < 4 {
		return nil, errors.New("AMPC table is too short")
	}
	table := resource[offset : offset+length]
	containerLen := int(binary.BigEndian.Uint32(table[:4]))
	if containerLen < 0 || 4+containerLen > len(table) {
		return nil, errors.New("AMPC table payload length is invalid")
	}
	return table[4 : 4+containerLen], nil
}

func decodeGlyphPayload(resource []byte) ([]byte, error) {
	numGlyphs, err := sfntNumGlyphs(resource)
	if err != nil {
		return nil, err
	}
	locFormat, err := sfntIndexToLocFormat(resource)
	if err != nil {
		return nil, err
	}
	loca, err := sfntLocaOffsets(resource, numGlyphs, locFormat)
	if err != nil {
		return nil, err
	}
	glyfOffset, glyfLength, err := sfntTableRange(resource, "glyf")
	if err != nil {
		return nil, err
	}
	glyf := resource[glyfOffset : glyfOffset+glyfLength]
	values := make([]uint16, 0)
	for glyphID := 1; glyphID < numGlyphs; glyphID++ {
		start, end := int(loca[glyphID]), int(loca[glyphID+1])
		if start == end {
			continue
		}
		if start < 0 || end < start || end > len(glyf) {
			return nil, errors.New("glyf offset from loca table is invalid")
		}
		glyphValues, err := glyphCoordinateValues(glyf[start:end])
		if err != nil {
			return nil, err
		}
		values = append(values, glyphValues...)
	}
	return unpack15(values)
}

func sfntNumGlyphs(font []byte) (int, error) {
	offset, length, err := sfntTableRange(font, "maxp")
	if err != nil {
		return 0, err
	}
	if length < 6 {
		return 0, errors.New("maxp table is too short")
	}
	return int(binary.BigEndian.Uint16(font[offset+4 : offset+6])), nil
}

func sfntIndexToLocFormat(font []byte) (int16, error) {
	offset, length, err := sfntTableRange(font, "head")
	if err != nil {
		return 0, err
	}
	if length < 52 {
		return 0, errors.New("head table is too short")
	}
	return int16(binary.BigEndian.Uint16(font[offset+50 : offset+52])), nil
}

func sfntLocaOffsets(font []byte, numGlyphs int, locFormat int16) ([]uint32, error) {
	offset, length, err := sfntTableRange(font, "loca")
	if err != nil {
		return nil, err
	}
	count := numGlyphs + 1
	offsets := make([]uint32, count)
	switch locFormat {
	case 0:
		if length < count*2 {
			return nil, errors.New("short loca table is too short")
		}
		for i := 0; i < count; i++ {
			offsets[i] = uint32(binary.BigEndian.Uint16(font[offset+i*2:offset+i*2+2])) * 2
		}
	case 1:
		if length < count*4 {
			return nil, errors.New("long loca table is too short")
		}
		for i := 0; i < count; i++ {
			offsets[i] = binary.BigEndian.Uint32(font[offset+i*4 : offset+i*4+4])
		}
	default:
		return nil, errors.New("unsupported loca format")
	}
	return offsets, nil
}

func glyphCoordinateValues(glyph []byte) ([]uint16, error) {
	if len(glyph) < 10 {
		return nil, io.ErrUnexpectedEOF
	}
	contours := int(int16(binary.BigEndian.Uint16(glyph[:2])))
	if contours <= 0 {
		return nil, errors.New("data glyph is not a simple glyph")
	}
	endPtsOffset := 10
	endPtsEnd := endPtsOffset + contours*2
	if endPtsEnd+2 > len(glyph) {
		return nil, io.ErrUnexpectedEOF
	}
	pointCount := int(binary.BigEndian.Uint16(glyph[endPtsEnd-2:endPtsEnd])) + 1
	instructionLength := int(binary.BigEndian.Uint16(glyph[endPtsEnd : endPtsEnd+2]))
	pos := endPtsEnd + 2 + instructionLength
	if pos > len(glyph) {
		return nil, io.ErrUnexpectedEOF
	}
	flags := make([]byte, 0, pointCount)
	for len(flags) < pointCount {
		if pos >= len(glyph) {
			return nil, io.ErrUnexpectedEOF
		}
		flag := glyph[pos]
		pos++
		repeat := 1
		if flag&0x08 != 0 {
			if pos >= len(glyph) {
				return nil, io.ErrUnexpectedEOF
			}
			repeat += int(glyph[pos])
			pos++
		}
		for i := 0; i < repeat && len(flags) < pointCount; i++ {
			flags = append(flags, flag)
		}
	}
	xs, next, err := decodeGlyphCoordinates(glyph, pos, flags, true)
	if err != nil {
		return nil, err
	}
	ys, _, err := decodeGlyphCoordinates(glyph, next, flags, false)
	if err != nil {
		return nil, err
	}
	values := make([]uint16, 0, len(xs)*2)
	for i := range xs {
		if xs[i] < 0 || xs[i] > 32767 || ys[i] < 0 || ys[i] > 32767 {
			return nil, errors.New("data glyph coordinate is outside payload range")
		}
		values = append(values, uint16(xs[i]), uint16(ys[i]))
	}
	return values, nil
}

func decodeGlyphCoordinates(glyph []byte, pos int, flags []byte, xAxis bool) ([]int, int, error) {
	coords := make([]int, len(flags))
	current := 0
	shortBit := byte(0x02)
	sameOrPositiveBit := byte(0x10)
	if !xAxis {
		shortBit = 0x04
		sameOrPositiveBit = 0x20
	}
	for i, flag := range flags {
		delta := 0
		if flag&shortBit != 0 {
			if pos >= len(glyph) {
				return nil, 0, io.ErrUnexpectedEOF
			}
			delta = int(glyph[pos])
			pos++
			if flag&sameOrPositiveBit == 0 {
				delta = -delta
			}
		} else if flag&sameOrPositiveBit == 0 {
			if pos+2 > len(glyph) {
				return nil, 0, io.ErrUnexpectedEOF
			}
			delta = int(int16(binary.BigEndian.Uint16(glyph[pos : pos+2])))
			pos += 2
		}
		current += delta
		coords[i] = current
	}
	return coords, pos, nil
}

func headTable(checkSumAdjustment uint32, indexToLocFormat int16) []byte {
	var b bytes.Buffer
	writeU32(&b, 0x00010000)
	writeU32(&b, 0x00010000)
	writeU32(&b, checkSumAdjustment)
	writeU32(&b, 0x5F0F3CF5)
	writeU16(&b, 0x000B)
	writeU16(&b, 1000)
	writeI64(&b, 0)
	writeI64(&b, 0)
	writeI16(&b, 0)
	writeI16(&b, 0)
	writeI16(&b, 32767)
	writeI16(&b, 32767)
	writeU16(&b, 0)
	writeU16(&b, 8)
	writeI16(&b, 2)
	writeI16(&b, indexToLocFormat)
	writeI16(&b, 0)
	return b.Bytes()
}

func hheaTable(numGlyphs uint16) []byte {
	var b bytes.Buffer
	writeU32(&b, 0x00010000)
	writeI16(&b, 800)
	writeI16(&b, -200)
	writeI16(&b, 0)
	writeU16(&b, 600)
	writeI16(&b, 0)
	writeI16(&b, 100)
	writeI16(&b, 500)
	writeI16(&b, 1)
	writeI16(&b, 0)
	writeI16(&b, 0)
	for i := 0; i < 4; i++ {
		writeI16(&b, 0)
	}
	writeI16(&b, 0)
	writeU16(&b, numGlyphs)
	return b.Bytes()
}

func hmtxTable(numGlyphs uint16) []byte {
	var b bytes.Buffer
	for i := 0; i < int(numGlyphs); i++ {
		writeU16(&b, 600)
		writeI16(&b, 0)
	}
	return b.Bytes()
}

func locaTable(offsets []uint32) []byte {
	var b bytes.Buffer
	for _, offset := range offsets {
		writeU32(&b, offset)
	}
	return b.Bytes()
}

func glyfTable(container []byte) ([]byte, []uint32, uint16, uint16, error) {
	stream := make([]byte, 4+len(container))
	binary.BigEndian.PutUint32(stream[:4], uint32(len(container)))
	copy(stream[4:], container)
	values := pack15(stream)
	pointCount := (len(values) + 1) / 2
	if pointCount == 0 {
		pointCount = 1
	}
	dataGlyphCount := (pointCount + fontPayloadMaxPointsPerGlyph - 1) / fontPayloadMaxPointsPerGlyph
	if dataGlyphCount > int(^uint16(0))-1 {
		return nil, nil, 0, 0, errors.New("container is too large for font glyph encoding")
	}
	var b bytes.Buffer
	offsets := []uint32{0}
	glyph0 := simpleGlyphTable()
	b.Write(glyph0)
	padBuffer4(&b)
	offsets = append(offsets, uint32(b.Len()))
	maxPoints := uint16(4)
	for glyphIndex := 0; glyphIndex < dataGlyphCount; glyphIndex++ {
		startPoint := glyphIndex * fontPayloadMaxPointsPerGlyph
		endPoint := minInt(startPoint+fontPayloadMaxPointsPerGlyph, pointCount)
		glyph, points := dataGlyphTable(values, startPoint, endPoint)
		if points > maxPoints {
			maxPoints = points
		}
		b.Write(glyph)
		padBuffer4(&b)
		offsets = append(offsets, uint32(b.Len()))
	}
	return b.Bytes(), offsets, uint16(1 + dataGlyphCount), maxPoints, nil
}

func simpleGlyphTable() []byte {
	var b bytes.Buffer
	writeI16(&b, 1)
	writeI16(&b, 0)
	writeI16(&b, 0)
	writeI16(&b, 500)
	writeI16(&b, 700)
	writeU16(&b, 3)
	writeU16(&b, 0)
	for i := 0; i < 4; i++ {
		b.WriteByte(0x01)
	}
	for _, v := range []int16{0, 500, 0, -500} {
		writeI16(&b, v)
	}
	for _, v := range []int16{0, 0, 700, 0} {
		writeI16(&b, v)
	}
	return b.Bytes()
}

func dataGlyphTable(values []uint16, startPoint, endPoint int) ([]byte, uint16) {
	pointCount := endPoint - startPoint
	if pointCount < 3 {
		pointCount = 3
	}
	xs := make([]int, pointCount)
	ys := make([]int, pointCount)
	for i := 0; i < pointCount; i++ {
		valueIndex := 2 * (startPoint + i)
		if valueIndex < len(values) {
			xs[i] = int(values[valueIndex])
		}
		if valueIndex+1 < len(values) {
			ys[i] = int(values[valueIndex+1])
		}
	}
	xMin, yMin, xMax, yMax := boundsForPoints(xs, ys)
	var b bytes.Buffer
	writeI16(&b, 1)
	writeI16(&b, int16(xMin))
	writeI16(&b, int16(yMin))
	writeI16(&b, int16(xMax))
	writeI16(&b, int16(yMax))
	writeU16(&b, uint16(pointCount-1))
	writeU16(&b, 0)
	writeRepeatedFlags(&b, pointCount)
	writeCoordinateDeltas(&b, xs)
	writeCoordinateDeltas(&b, ys)
	return b.Bytes(), uint16(pointCount)
}

func boundsForPoints(xs, ys []int) (int, int, int, int) {
	xMin, yMin := xs[0], ys[0]
	xMax, yMax := xs[0], ys[0]
	for i := range xs {
		if xs[i] < xMin {
			xMin = xs[i]
		}
		if xs[i] > xMax {
			xMax = xs[i]
		}
		if ys[i] < yMin {
			yMin = ys[i]
		}
		if ys[i] > yMax {
			yMax = ys[i]
		}
	}
	return xMin, yMin, xMax, yMax
}

func writeRepeatedFlags(w io.Writer, pointCount int) {
	for remaining := pointCount; remaining > 0; {
		run := minInt(remaining, 256)
		_, _ = w.Write([]byte{0x01 | 0x08, byte(run - 1)})
		remaining -= run
	}
}

func writeCoordinateDeltas(w io.Writer, coords []int) {
	prev := 0
	for _, coord := range coords {
		writeI16(w, int16(coord-prev))
		prev = coord
	}
}

func maxpTable(numGlyphs, maxPoints, maxContours uint16) []byte {
	var b bytes.Buffer
	writeU32(&b, 0x00010000)
	writeU16(&b, numGlyphs)
	writeU16(&b, maxPoints)
	writeU16(&b, maxContours)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, 1)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, 0)
	writeU16(&b, 0)
	return b.Bytes()
}

func cmapTable(numGlyphs uint16) []byte {
	dataGlyphs := int(numGlyphs) - 1
	if dataGlyphs < 1 {
		dataGlyphs = 1
	}
	startCode := 0xE000
	endCode := startCode + dataGlyphs - 1
	if endCode > 0xFFFD {
		endCode = 0xFFFD
	}
	segCount := 2
	segCountX2 := uint16(segCount * 2)
	searchRange, entrySelector, rangeShift := cmapSearchParams(segCount)
	var sub bytes.Buffer
	writeU16(&sub, 4)
	writeU16(&sub, 16+uint16(segCount)*8)
	writeU16(&sub, 0)
	writeU16(&sub, segCountX2)
	writeU16(&sub, searchRange)
	writeU16(&sub, entrySelector)
	writeU16(&sub, rangeShift)
	writeU16(&sub, uint16(endCode))
	writeU16(&sub, 0xFFFF)
	writeU16(&sub, 0)
	writeU16(&sub, uint16(startCode))
	writeU16(&sub, 0xFFFF)
	writeI16(&sub, int16(1-startCode))
	writeU16(&sub, 1)
	writeU16(&sub, 0)
	writeU16(&sub, 0)

	var b bytes.Buffer
	writeU16(&b, 0)
	writeU16(&b, 1)
	writeU16(&b, 3)
	writeU16(&b, 1)
	writeU32(&b, 12)
	b.Write(sub.Bytes())
	return b.Bytes()
}

func nameTable() []byte {
	names := []struct {
		id    uint16
		value string
	}{
		{id: 1, value: "ampcache"},
		{id: 2, value: "Regular"},
		{id: 3, value: "ampcache Regular 1.0"},
		{id: 4, value: "ampcache Regular"},
		{id: 5, value: "Version 1.0"},
		{id: 6, value: "ampcache-Regular"},
	}
	var storage bytes.Buffer
	var records bytes.Buffer
	for _, name := range names {
		s := utf16BE(name.value)
		writeU16(&records, 3)
		writeU16(&records, 1)
		writeU16(&records, 0x0409)
		writeU16(&records, name.id)
		writeU16(&records, uint16(len(s)))
		writeU16(&records, uint16(storage.Len()))
		storage.Write(s)
	}
	var b bytes.Buffer
	writeU16(&b, 0)
	writeU16(&b, uint16(len(names)))
	writeU16(&b, uint16(6+records.Len()))
	b.Write(records.Bytes())
	b.Write(storage.Bytes())
	return b.Bytes()
}

func os2Table() []byte {
	var b bytes.Buffer
	writeU16(&b, 0)
	writeI16(&b, 600)
	writeU16(&b, 400)
	writeU16(&b, 5)
	writeU16(&b, 0)
	writeI16(&b, 650)
	writeI16(&b, 699)
	writeI16(&b, 0)
	writeI16(&b, 140)
	writeI16(&b, 650)
	writeI16(&b, 699)
	writeI16(&b, 0)
	writeI16(&b, 479)
	writeI16(&b, 49)
	writeI16(&b, 258)
	writeI16(&b, 0)
	b.Write(make([]byte, 10))
	writeU32(&b, 1)
	writeU32(&b, 0)
	writeU32(&b, 0)
	writeU32(&b, 0)
	b.WriteString("AMPC")
	writeU16(&b, 0x0040)
	writeU16(&b, 0x0041)
	writeU16(&b, 0x0041)
	writeI16(&b, 800)
	writeI16(&b, -200)
	writeI16(&b, 0)
	writeU16(&b, 800)
	writeU16(&b, 200)
	return b.Bytes()
}

func postTable() []byte {
	var b bytes.Buffer
	writeU32(&b, 0x00030000)
	writeU32(&b, 0)
	writeI16(&b, 0)
	writeI16(&b, 0)
	for i := 0; i < 5; i++ {
		writeU32(&b, 0)
	}
	return b.Bytes()
}

func sfntSearchParams(numTables int) (searchRange, entrySelector, rangeShift uint16) {
	pow := 1
	selector := 0
	for pow*2 <= numTables {
		pow *= 2
		selector++
	}
	return uint16(pow * 16), uint16(selector), uint16(numTables*16 - pow*16)
}

func cmapSearchParams(segCount int) (searchRange, entrySelector, rangeShift uint16) {
	pow := 1
	selector := 0
	for pow*2 <= segCount {
		pow *= 2
		selector++
	}
	return uint16(pow * 2), uint16(selector), uint16(segCount*2 - pow*2)
}

func sfntChecksum(data []byte) uint32 {
	var sum uint64
	for i := 0; i < len(data); i += 4 {
		var word uint32
		for j := 0; j < 4; j++ {
			word <<= 8
			if i+j < len(data) {
				word |= uint32(data[i+j])
			}
		}
		sum += uint64(word)
	}
	return uint32(sum)
}

func sfntTableOffset(font []byte, tag string) (int, error) {
	offset, _, err := sfntTableRange(font, tag)
	return offset, err
}

func sfntTableRange(font []byte, tag string) (offset int, length int, err error) {
	if len(tag) != 4 {
		return 0, 0, errors.New("sfnt tag must be 4 bytes")
	}
	if len(font) < 12 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	numTables := int(binary.BigEndian.Uint16(font[4:6]))
	recordsEnd := 12 + numTables*16
	if recordsEnd > len(font) {
		return 0, 0, io.ErrUnexpectedEOF
	}
	for pos := 12; pos < recordsEnd; pos += 16 {
		if string(font[pos:pos+4]) != tag {
			continue
		}
		offset := int(binary.BigEndian.Uint32(font[pos+8 : pos+12]))
		length := int(binary.BigEndian.Uint32(font[pos+12 : pos+16]))
		if offset < 0 || length < 0 || offset+length > len(font) {
			return 0, 0, errors.New("sfnt table range is invalid")
		}
		return offset, length, nil
	}
	return 0, 0, fmt.Errorf("sfnt table %q not found", tag)
}

func paddedLen(n int) int {
	return (n + 3) &^ 3
}

func padBuffer4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

func utf16BE(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}

func writeU16(w io.Writer, v uint16) {
	_ = binary.Write(w, binary.BigEndian, v)
}

func writeI16(w io.Writer, v int16) {
	_ = binary.Write(w, binary.BigEndian, v)
}

func writeU32(w io.Writer, v uint32) {
	_ = binary.Write(w, binary.BigEndian, v)
}

func writeI64(w io.Writer, v int64) {
	_ = binary.Write(w, binary.BigEndian, v)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func pack15(data []byte) []uint16 {
	if len(data) == 0 {
		return nil
	}
	totalBits := len(data) * 8
	values := make([]uint16, (totalBits+14)/15)
	for i := range values {
		var value uint16
		for bit := 0; bit < 15; bit++ {
			value <<= 1
			bitIndex := i*15 + bit
			if bitIndex >= totalBits {
				continue
			}
			if data[bitIndex/8]&(1<<uint(7-bitIndex%8)) != 0 {
				value |= 1
			}
		}
		values[i] = value
	}
	return values
}

func unpack15(values []uint16) ([]byte, error) {
	reader := bit15Reader{values: values}
	var lengthBytes [4]byte
	for i := range lengthBytes {
		b, err := reader.readByte()
		if err != nil {
			return nil, err
		}
		lengthBytes[i] = b
	}
	length := int(binary.BigEndian.Uint32(lengthBytes[:]))
	if length < 0 {
		return nil, errors.New("font glyph payload length is invalid")
	}
	out := make([]byte, length)
	for i := range out {
		b, err := reader.readByte()
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

type bit15Reader struct {
	values []uint16
	bit    int
}

func (r *bit15Reader) readByte() (byte, error) {
	var out byte
	for i := 0; i < 8; i++ {
		valueIndex := r.bit / 15
		if valueIndex >= len(r.values) {
			return 0, io.ErrUnexpectedEOF
		}
		bitIndex := 14 - (r.bit % 15)
		out <<= 1
		out |= byte((r.values[valueIndex] >> uint(bitIndex)) & 1)
		r.bit++
	}
	return out, nil
}

func encodePNG(container []byte) ([]byte, error) {
	if len(container) > int(^uint32(0)) {
		return nil, errors.New("container is too large for png encoding")
	}
	stream := make([]byte, 4+len(container))
	binary.BigEndian.PutUint32(stream[:4], uint32(len(container)))
	copy(stream[4:], container)

	const width = 1024
	pixels := (len(stream) + 2) / 3
	height := (pixels + width - 1) / width
	if height == 0 {
		height = 1
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < pixels; i++ {
		pos := i * 3
		c := color.NRGBA{A: 255}
		if pos < len(stream) {
			c.R = stream[pos]
		}
		if pos+1 < len(stream) {
			c.G = stream[pos+1]
		}
		if pos+2 < len(stream) {
			c.B = stream[pos+2]
		}
		img.SetNRGBA(i%width, i/width, c)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decodePNG(resource []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(resource))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	stream := make([]byte, 0, bounds.Dx()*bounds.Dy()*3)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			stream = append(stream, byte(r>>8), byte(g>>8), byte(b>>8))
		}
	}
	if len(stream) < 4 {
		return nil, io.ErrUnexpectedEOF
	}
	length := int(binary.BigEndian.Uint32(stream[:4]))
	if length < 0 || 4+length > len(stream) {
		return nil, errors.New("png ampcache payload length is invalid")
	}
	return stream[4 : 4+length], nil
}
