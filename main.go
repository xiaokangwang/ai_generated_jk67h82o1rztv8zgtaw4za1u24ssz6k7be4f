package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"time"
)

const (
	pcapngBlockTypeSectionHeader = 0x0A0D0D0A
	pcapngBlockTypeInterfaceDesc = 0x00000001
	pcapngBlockTypePacket        = 0x00000002
	pcapngBlockTypeSimplePacket  = 0x00000003
	pcapngBlockTypeEnhanced      = 0x00000006
	pcapngByteOrderMagic         = 0x1A2B3C4D

	pcapngOptionEnd          = 0
	pcapngOptionIfName       = 2
	pcapngOptionIfTSRes      = 9
	pcapngOptionIfTSOff      = 14
	linkTypeEthernet         = 1
	etherTypeIPv4            = 0x0800
	etherTypeIPv6            = 0x86DD
	udpProtocol              = 17
	socksAtypIPv4       byte = 0x01
	socksAtypDomain     byte = 0x03
	socksAtypIPv6       byte = 0x04
)

var (
	errNotEthernet     = errors.New("packet is not Ethernet")
	errNotUDP          = errors.New("packet is not UDP over IPv4/IPv6")
	errNotProxyPacket  = errors.New("packet does not involve the SOCKS relay")
	errBadSocksHeader  = errors.New("payload is not a SOCKS5 UDP datagram")
	errFragmentedSocks = errors.New("SOCKS5 UDP fragmentation is not supported")
	errDomainAddress   = errors.New("SOCKS5 domain destinations cannot be restored into IP packets")
	errFamilyMismatch  = errors.New("SOCKS destination address family does not match the client packet family")
	errFragmentedIP    = errors.New("IP fragmentation is not supported")
)

type config struct {
	inputPath  string
	outputPath string
	proxy      netip.AddrPort
	proxySet   bool
}

type rewriteStats struct {
	total       int
	transformed int
	copied      int
}

type captureInfo struct {
	Timestamp      time.Time
	InterfaceIndex int
	CaptureLength  int
	Length         int
}

type pcapInterface struct {
	LinkType            uint16
	SnapLength          uint32
	TimestampResolution uint8
	TimestampOffset     int64
	secondMask          uint64
	scaleUp             uint64
	scaleDown           uint64
}

type pcapngReader struct {
	r      *bufio.Reader
	order  binary.ByteOrder
	ifaces []pcapInterface
}

type pcapngWriter struct {
	w *bufio.Writer
}

type endpointStats struct {
	addr     netip.AddrPort
	srcCount int
	dstCount int
	peers    map[netip.AddrPort]struct{}
}

type socksTarget struct {
	Addr   netip.Addr
	Domain string
	Port   uint16
}

type direction int

const (
	dirOutgoing direction = iota
	dirIncoming
)

type ethernetHeader struct {
	dstMAC    [6]byte
	srcMAC    [6]byte
	etherType uint16
}

type ipv4Header struct {
	tos              uint8
	id               uint16
	flagsAndFragment uint16
	ttl              uint8
	src              netip.Addr
	dst              netip.Addr
}

type ipv6Header struct {
	trafficClass uint8
	flowLabel    uint32
	hopLimit     uint8
	src          netip.Addr
	dst          netip.Addr
}

type udpHeader struct {
	srcPort uint16
	dstPort uint16
	payload []byte
}

type decodedPacket struct {
	eth ethernetHeader
	ip4 *ipv4Header
	ip6 *ipv6Header
	udp udpHeader
}

func main() {
	cfg, err := parseFlags()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() (config, error) {
	cfg := config{}
	proxy := flag.String("proxy", "", "SOCKS5 relay address in ip:port form, for example 127.0.0.1:11803")
	flag.StringVar(&cfg.inputPath, "in", "", "input pcapng file")
	flag.StringVar(&cfg.outputPath, "out", "", "output pcapng file")
	flag.Parse()

	if cfg.inputPath == "" {
		return cfg, errors.New("missing -in")
	}
	if cfg.outputPath == "" {
		return cfg, errors.New("missing -out")
	}
	if *proxy != "" {
		addr, err := netip.ParseAddrPort(*proxy)
		if err != nil {
			return cfg, fmt.Errorf("parse -proxy: %w", err)
		}
		cfg.proxy = addr
		cfg.proxySet = true
	}
	return cfg, nil
}

func run(cfg config) error {
	proxy := cfg.proxy
	if !cfg.proxySet {
		detected, err := detectProxyEndpoint(cfg.inputPath)
		if err != nil {
			return err
		}
		proxy = detected
	}

	inFile, err := os.Open(cfg.inputPath)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer inFile.Close()

	reader := newPcapngReader(inFile)

	outFile, err := os.Create(cfg.outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer outFile.Close()

	writer, err := newPcapngWriter(outFile)
	if err != nil {
		return fmt.Errorf("create writer: %w", err)
	}

	stats := rewriteStats{}
	for {
		data, ci, err := reader.ReadPacket()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read packet: %w", err)
		}

		stats.total++
		linkType, ok := reader.InterfaceLinkType(ci.InterfaceIndex)
		if !ok || linkType != linkTypeEthernet {
			stats.copied++
			ci.InterfaceIndex = 0
			if err := writer.WritePacket(ci, data); err != nil {
				return fmt.Errorf("write packet %d: %w", stats.total, err)
			}
			continue
		}

		outData, transformed, err := restorePacket(data, proxy)
		if err != nil {
			return fmt.Errorf("packet %d: %w", stats.total, err)
		}
		if transformed {
			stats.transformed++
		} else {
			stats.copied++
		}

		ci.InterfaceIndex = 0
		ci.CaptureLength = len(outData)
		ci.Length = len(outData)
		if err := writer.WritePacket(ci, outData); err != nil {
			return fmt.Errorf("write packet %d: %w", stats.total, err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush output: %w", err)
	}

	fmt.Fprintf(os.Stderr, "proxy=%s total=%d transformed=%d copied=%d\n", proxy, stats.total, stats.transformed, stats.copied)
	return nil
}

func detectProxyEndpoint(path string) (netip.AddrPort, error) {
	inFile, err := os.Open(path)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("open input for detection: %w", err)
	}
	defer inFile.Close()

	reader := newPcapngReader(inFile)
	statsByEndpoint := map[netip.AddrPort]*endpointStats{}
	for {
		data, ci, err := reader.ReadPacket()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("read packet during detection: %w", err)
		}

		linkType, ok := reader.InterfaceLinkType(ci.InterfaceIndex)
		if !ok || linkType != linkTypeEthernet {
			continue
		}

		view, err := decodePacket(data)
		if err != nil {
			continue
		}
		if _, _, err := parseSocksTarget(view.udp.payload); err != nil && !errors.Is(err, errDomainAddress) {
			continue
		}

		src, dst := view.endpoints()
		srcStats := getEndpointStats(statsByEndpoint, src)
		srcStats.srcCount++
		srcStats.peers[dst] = struct{}{}

		dstStats := getEndpointStats(statsByEndpoint, dst)
		dstStats.dstCount++
		dstStats.peers[src] = struct{}{}
	}

	return pickProxyEndpoint(statsByEndpoint)
}

func pickProxyEndpoint(statsByEndpoint map[netip.AddrPort]*endpointStats) (netip.AddrPort, error) {
	type candidate struct {
		addr      netip.AddrPort
		peerCount int
		packetSum int
	}

	var candidates []candidate
	for _, stat := range statsByEndpoint {
		if stat.srcCount == 0 || stat.dstCount == 0 {
			continue
		}
		candidates = append(candidates, candidate{
			addr:      stat.addr,
			peerCount: len(stat.peers),
			packetSum: stat.srcCount + stat.dstCount,
		})
	}
	if len(candidates) == 0 {
		return netip.AddrPort{}, errors.New("could not auto-detect the SOCKS relay, provide -proxy")
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].peerCount != candidates[j].peerCount {
			return candidates[i].peerCount > candidates[j].peerCount
		}
		if candidates[i].packetSum != candidates[j].packetSum {
			return candidates[i].packetSum > candidates[j].packetSum
		}
		return candidates[i].addr.String() < candidates[j].addr.String()
	})

	if len(candidates) > 1 &&
		candidates[0].peerCount == candidates[1].peerCount &&
		candidates[0].packetSum == candidates[1].packetSum {
		return netip.AddrPort{}, errors.New("proxy auto-detection is ambiguous, provide -proxy")
	}

	return candidates[0].addr, nil
}

func getEndpointStats(statsByEndpoint map[netip.AddrPort]*endpointStats, addr netip.AddrPort) *endpointStats {
	if stat, ok := statsByEndpoint[addr]; ok {
		return stat
	}
	stat := &endpointStats{
		addr:  addr,
		peers: map[netip.AddrPort]struct{}{},
	}
	statsByEndpoint[addr] = stat
	return stat
}

func restorePacket(data []byte, proxy netip.AddrPort) ([]byte, bool, error) {
	view, err := decodePacket(data)
	if err != nil {
		if errors.Is(err, errNotEthernet) || errors.Is(err, errNotUDP) || errors.Is(err, errFragmentedIP) {
			return data, false, nil
		}
		return nil, false, err
	}

	target, payload, err := parseSocksTarget(view.udp.payload)
	if err != nil {
		if errors.Is(err, errBadSocksHeader) || errors.Is(err, errFragmentedSocks) || errors.Is(err, errDomainAddress) {
			return data, false, nil
		}
		return nil, false, err
	}

	dir, err := view.direction(proxy)
	if err != nil {
		if errors.Is(err, errNotProxyPacket) {
			return data, false, nil
		}
		return nil, false, err
	}

	srcIP, dstIP, srcPort, dstPort, err := restoreEndpoints(view, target, dir)
	if err != nil {
		if errors.Is(err, errFamilyMismatch) {
			return data, false, nil
		}
		return nil, false, err
	}

	out, err := buildPacket(view, srcIP, dstIP, srcPort, dstPort, payload)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func restoreEndpoints(view *decodedPacket, target socksTarget, dir direction) (netip.Addr, netip.Addr, uint16, uint16, error) {
	src, dst := view.endpoints()
	switch dir {
	case dirOutgoing:
		if src.Addr().BitLen() != target.Addr.BitLen() {
			return netip.Addr{}, netip.Addr{}, 0, 0, errFamilyMismatch
		}
		return src.Addr(), target.Addr, src.Port(), target.Port, nil
	case dirIncoming:
		if dst.Addr().BitLen() != target.Addr.BitLen() {
			return netip.Addr{}, netip.Addr{}, 0, 0, errFamilyMismatch
		}
		return target.Addr, dst.Addr(), target.Port, dst.Port(), nil
	default:
		return netip.Addr{}, netip.Addr{}, 0, 0, fmt.Errorf("unknown direction %d", dir)
	}
}

func decodePacket(frame []byte) (*decodedPacket, error) {
	if len(frame) < 14 {
		return nil, errNotEthernet
	}

	var eth ethernetHeader
	copy(eth.dstMAC[:], frame[:6])
	copy(eth.srcMAC[:], frame[6:12])
	eth.etherType = binary.BigEndian.Uint16(frame[12:14])

	switch eth.etherType {
	case etherTypeIPv4:
		return decodeIPv4Packet(eth, frame[14:])
	case etherTypeIPv6:
		return decodeIPv6Packet(eth, frame[14:])
	default:
		return nil, errNotUDP
	}
}

func decodeIPv4Packet(eth ethernetHeader, payload []byte) (*decodedPacket, error) {
	if len(payload) < 20 {
		return nil, errNotUDP
	}
	if payload[0]>>4 != 4 {
		return nil, errNotUDP
	}
	ihl := int(payload[0]&0x0f) * 4
	if ihl < 20 || len(payload) < ihl+8 {
		return nil, errNotUDP
	}
	totalLen := int(binary.BigEndian.Uint16(payload[2:4]))
	if totalLen < ihl+8 || len(payload) < totalLen {
		return nil, errNotUDP
	}

	flagsAndFragment := binary.BigEndian.Uint16(payload[6:8])
	if flagsAndFragment&0x2000 != 0 || flagsAndFragment&0x1fff != 0 {
		return nil, errFragmentedIP
	}
	if payload[9] != udpProtocol {
		return nil, errNotUDP
	}

	udpOffset := ihl
	udpLength := int(binary.BigEndian.Uint16(payload[udpOffset+4 : udpOffset+6]))
	if udpLength < 8 || udpOffset+udpLength > totalLen {
		return nil, errNotUDP
	}

	var srcBytes [4]byte
	var dstBytes [4]byte
	copy(srcBytes[:], payload[12:16])
	copy(dstBytes[:], payload[16:20])

	return &decodedPacket{
		eth: eth,
		ip4: &ipv4Header{
			tos:              payload[1],
			id:               binary.BigEndian.Uint16(payload[4:6]),
			flagsAndFragment: flagsAndFragment,
			ttl:              payload[8],
			src:              netip.AddrFrom4(srcBytes),
			dst:              netip.AddrFrom4(dstBytes),
		},
		udp: udpHeader{
			srcPort: binary.BigEndian.Uint16(payload[udpOffset : udpOffset+2]),
			dstPort: binary.BigEndian.Uint16(payload[udpOffset+2 : udpOffset+4]),
			payload: append([]byte(nil), payload[udpOffset+8:udpOffset+udpLength]...),
		},
	}, nil
}

func decodeIPv6Packet(eth ethernetHeader, payload []byte) (*decodedPacket, error) {
	if len(payload) < 40 {
		return nil, errNotUDP
	}
	if payload[0]>>4 != 6 {
		return nil, errNotUDP
	}
	if payload[6] != udpProtocol {
		return nil, errNotUDP
	}
	payloadLength := int(binary.BigEndian.Uint16(payload[4:6]))
	totalLen := 40 + payloadLength
	if len(payload) < totalLen || payloadLength < 8 {
		return nil, errNotUDP
	}

	var srcBytes [16]byte
	var dstBytes [16]byte
	copy(srcBytes[:], payload[8:24])
	copy(dstBytes[:], payload[24:40])

	headerWord := binary.BigEndian.Uint32(payload[:4])
	udpOffset := 40
	udpLength := int(binary.BigEndian.Uint16(payload[udpOffset+4 : udpOffset+6]))
	if udpLength < 8 || udpOffset+udpLength > totalLen {
		return nil, errNotUDP
	}

	return &decodedPacket{
		eth: eth,
		ip6: &ipv6Header{
			trafficClass: uint8((headerWord >> 20) & 0xff),
			flowLabel:    headerWord & 0x000fffff,
			hopLimit:     payload[7],
			src:          netip.AddrFrom16(srcBytes),
			dst:          netip.AddrFrom16(dstBytes),
		},
		udp: udpHeader{
			srcPort: binary.BigEndian.Uint16(payload[udpOffset : udpOffset+2]),
			dstPort: binary.BigEndian.Uint16(payload[udpOffset+2 : udpOffset+4]),
			payload: append([]byte(nil), payload[udpOffset+8:udpOffset+udpLength]...),
		},
	}, nil
}

func (p *decodedPacket) endpoints() (netip.AddrPort, netip.AddrPort) {
	if p.ip4 != nil {
		return netip.AddrPortFrom(p.ip4.src, p.udp.srcPort), netip.AddrPortFrom(p.ip4.dst, p.udp.dstPort)
	}
	return netip.AddrPortFrom(p.ip6.src, p.udp.srcPort), netip.AddrPortFrom(p.ip6.dst, p.udp.dstPort)
}

func (p *decodedPacket) direction(proxy netip.AddrPort) (direction, error) {
	src, dst := p.endpoints()
	switch {
	case dst == proxy:
		return dirOutgoing, nil
	case src == proxy:
		return dirIncoming, nil
	default:
		return 0, errNotProxyPacket
	}
}

func buildPacket(template *decodedPacket, srcIP, dstIP netip.Addr, srcPort, dstPort uint16, payload []byte) ([]byte, error) {
	switch {
	case srcIP.Is4() && dstIP.Is4():
		return buildIPv4Packet(template, srcIP, dstIP, srcPort, dstPort, payload)
	case srcIP.Is6() && dstIP.Is6():
		return buildIPv6Packet(template, srcIP, dstIP, srcPort, dstPort, payload)
	default:
		return nil, errFamilyMismatch
	}
}

func buildIPv4Packet(template *decodedPacket, srcIP, dstIP netip.Addr, srcPort, dstPort uint16, payload []byte) ([]byte, error) {
	if template.ip4 == nil {
		return nil, errFamilyMismatch
	}

	frame := make([]byte, 14+20+8+len(payload))
	copy(frame[:6], template.eth.dstMAC[:])
	copy(frame[6:12], template.eth.srcMAC[:])
	binary.BigEndian.PutUint16(frame[12:14], etherTypeIPv4)

	ip := frame[14:34]
	ip[0] = 0x45
	ip[1] = template.ip4.tos
	binary.BigEndian.PutUint16(ip[2:4], uint16(len(ip)+8+len(payload)))
	binary.BigEndian.PutUint16(ip[4:6], template.ip4.id)
	binary.BigEndian.PutUint16(ip[6:8], template.ip4.flagsAndFragment)
	ttl := template.ip4.ttl
	if ttl == 0 {
		ttl = 64
	}
	ip[8] = ttl
	ip[9] = udpProtocol
	copy(ip[12:16], srcIP.AsSlice())
	copy(ip[16:20], dstIP.AsSlice())
	binary.BigEndian.PutUint16(ip[10:12], 0)
	binary.BigEndian.PutUint16(ip[10:12], checksum(ip))

	udp := frame[34:]
	binary.BigEndian.PutUint16(udp[0:2], srcPort)
	binary.BigEndian.PutUint16(udp[2:4], dstPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(payload)))
	copy(udp[8:], payload)
	binary.BigEndian.PutUint16(udp[6:8], 0)

	var srcBytes [4]byte
	var dstBytes [4]byte
	copy(srcBytes[:], srcIP.AsSlice())
	copy(dstBytes[:], dstIP.AsSlice())
	udpChecksum := udpChecksumIPv4(srcBytes, dstBytes, udp)
	binary.BigEndian.PutUint16(udp[6:8], udpChecksum)
	return frame, nil
}

func buildIPv6Packet(template *decodedPacket, srcIP, dstIP netip.Addr, srcPort, dstPort uint16, payload []byte) ([]byte, error) {
	if template.ip6 == nil {
		return nil, errFamilyMismatch
	}

	frame := make([]byte, 14+40+8+len(payload))
	copy(frame[:6], template.eth.dstMAC[:])
	copy(frame[6:12], template.eth.srcMAC[:])
	binary.BigEndian.PutUint16(frame[12:14], etherTypeIPv6)

	ip := frame[14:54]
	headerWord := uint32(6<<28) | uint32(template.ip6.trafficClass)<<20 | (template.ip6.flowLabel & 0x000fffff)
	binary.BigEndian.PutUint32(ip[:4], headerWord)
	binary.BigEndian.PutUint16(ip[4:6], uint16(8+len(payload)))
	ip[6] = udpProtocol
	hopLimit := template.ip6.hopLimit
	if hopLimit == 0 {
		hopLimit = 64
	}
	ip[7] = hopLimit
	copy(ip[8:24], srcIP.AsSlice())
	copy(ip[24:40], dstIP.AsSlice())

	udp := frame[54:]
	binary.BigEndian.PutUint16(udp[0:2], srcPort)
	binary.BigEndian.PutUint16(udp[2:4], dstPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(payload)))
	copy(udp[8:], payload)
	binary.BigEndian.PutUint16(udp[6:8], 0)

	var srcBytes [16]byte
	var dstBytes [16]byte
	copy(srcBytes[:], srcIP.AsSlice())
	copy(dstBytes[:], dstIP.AsSlice())
	udpChecksum := udpChecksumIPv6(srcBytes, dstBytes, udp)
	binary.BigEndian.PutUint16(udp[6:8], udpChecksum)
	return frame, nil
}

func parseSocksTarget(payload []byte) (socksTarget, []byte, error) {
	if len(payload) < 4 {
		return socksTarget{}, nil, errBadSocksHeader
	}
	if payload[0] != 0 || payload[1] != 0 {
		return socksTarget{}, nil, errBadSocksHeader
	}
	if payload[2] != 0 {
		return socksTarget{}, nil, errFragmentedSocks
	}

	target := socksTarget{}
	offset := 4
	switch payload[3] {
	case socksAtypIPv4:
		if len(payload) < offset+4+2 {
			return socksTarget{}, nil, errBadSocksHeader
		}
		var addrBytes [4]byte
		copy(addrBytes[:], payload[offset:offset+4])
		target.Addr = netip.AddrFrom4(addrBytes)
		offset += 4
	case socksAtypDomain:
		if len(payload) < offset+1 {
			return socksTarget{}, nil, errBadSocksHeader
		}
		size := int(payload[offset])
		offset++
		if len(payload) < offset+size+2 {
			return socksTarget{}, nil, errBadSocksHeader
		}
		target.Domain = string(payload[offset : offset+size])
		offset += size
	case socksAtypIPv6:
		if len(payload) < offset+16+2 {
			return socksTarget{}, nil, errBadSocksHeader
		}
		var addrBytes [16]byte
		copy(addrBytes[:], payload[offset:offset+16])
		target.Addr = netip.AddrFrom16(addrBytes)
		offset += 16
	default:
		return socksTarget{}, nil, errBadSocksHeader
	}

	target.Port = binary.BigEndian.Uint16(payload[offset : offset+2])
	offset += 2
	if target.Domain != "" {
		return target, payload[offset:], errDomainAddress
	}
	if !target.Addr.IsValid() {
		return socksTarget{}, nil, errBadSocksHeader
	}
	return target, payload[offset:], nil
}

func newPcapngReader(r io.Reader) *pcapngReader {
	return &pcapngReader{r: bufio.NewReader(r)}
}

func (r *pcapngReader) InterfaceLinkType(index int) (uint16, bool) {
	if index < 0 || index >= len(r.ifaces) {
		return 0, false
	}
	return r.ifaces[index].LinkType, true
}

func (r *pcapngReader) ReadPacket() ([]byte, captureInfo, error) {
	for {
		blockType, body, err := r.readBlock()
		if err != nil {
			return nil, captureInfo{}, err
		}

		switch blockType {
		case pcapngBlockTypeSectionHeader:
			r.ifaces = nil
		case pcapngBlockTypeInterfaceDesc:
			if err := r.readInterfaceDesc(body); err != nil {
				return nil, captureInfo{}, err
			}
		case pcapngBlockTypeEnhanced:
			return r.readEnhancedPacket(body)
		case pcapngBlockTypePacket:
			return r.readPacketBlock(body)
		case pcapngBlockTypeSimplePacket:
			return r.readSimplePacket(body)
		}
	}
}

func (r *pcapngReader) readBlock() (uint32, []byte, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(r.r, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}

	if bytes.Equal(header[:4], []byte{0x0A, 0x0D, 0x0D, 0x0A}) {
		magic := make([]byte, 4)
		if _, err := io.ReadFull(r.r, magic); err != nil {
			return 0, nil, err
		}

		var order binary.ByteOrder
		switch {
		case binary.BigEndian.Uint32(magic) == pcapngByteOrderMagic:
			order = binary.BigEndian
		case binary.LittleEndian.Uint32(magic) == pcapngByteOrderMagic:
			order = binary.LittleEndian
		default:
			return 0, nil, errors.New("invalid pcapng byte-order magic")
		}

		length := order.Uint32(header[4:8])
		if length < 28 || length%4 != 0 {
			return 0, nil, fmt.Errorf("invalid section header length %d", length)
		}
		rest := make([]byte, int(length)-12)
		if _, err := io.ReadFull(r.r, rest); err != nil {
			return 0, nil, err
		}
		if order.Uint32(rest[len(rest)-4:]) != length {
			return 0, nil, errors.New("pcapng section header length trailer mismatch")
		}
		r.order = order
		return pcapngBlockTypeSectionHeader, rest[:len(rest)-4], nil
	}

	if r.order == nil {
		return 0, nil, errors.New("pcapng file does not start with a section header")
	}

	blockType := r.order.Uint32(header[:4])
	length := r.order.Uint32(header[4:8])
	if length < 12 || length%4 != 0 {
		return 0, nil, fmt.Errorf("invalid pcapng block length %d", length)
	}

	rest := make([]byte, int(length)-8)
	if _, err := io.ReadFull(r.r, rest); err != nil {
		return 0, nil, err
	}
	if r.order.Uint32(rest[len(rest)-4:]) != length {
		return 0, nil, fmt.Errorf("pcapng block trailer mismatch for block type %d", blockType)
	}
	return blockType, rest[:len(rest)-4], nil
}

func (r *pcapngReader) readInterfaceDesc(body []byte) error {
	if len(body) < 8 {
		return errors.New("short interface description block")
	}

	iface := newPcapInterface(
		r.order.Uint16(body[:2]),
		r.order.Uint32(body[4:8]),
	)
	if err := parsePcapngOptions(body[8:], r.order, func(code uint16, value []byte) error {
		switch code {
		case pcapngOptionIfTSRes:
			if len(value) < 1 {
				return errors.New("short if_tsresol option")
			}
			return iface.setTimestampResolution(value[0])
		case pcapngOptionIfTSOff:
			if len(value) < 8 {
				return errors.New("short if_tsoffset option")
			}
			iface.TimestampOffset = int64(r.order.Uint64(value[:8]))
			return nil
		default:
			return nil
		}
	}); err != nil {
		return err
	}

	r.ifaces = append(r.ifaces, iface)
	return nil
}

func (r *pcapngReader) readEnhancedPacket(body []byte) ([]byte, captureInfo, error) {
	if len(body) < 20 {
		return nil, captureInfo{}, errors.New("short enhanced packet block")
	}

	ifaceID := int(r.order.Uint32(body[:4]))
	if ifaceID < 0 || ifaceID >= len(r.ifaces) {
		return nil, captureInfo{}, fmt.Errorf("packet references unknown interface %d", ifaceID)
	}

	captureLength := int(r.order.Uint32(body[12:16]))
	originalLength := int(r.order.Uint32(body[16:20]))
	paddedLength := captureLength + padLength(captureLength)
	if len(body) < 20+paddedLength {
		return nil, captureInfo{}, errors.New("short packet data in enhanced packet block")
	}

	rawTimestamp := uint64(r.order.Uint32(body[4:8]))<<32 | uint64(r.order.Uint32(body[8:12]))
	packetData := append([]byte(nil), body[20:20+captureLength]...)
	return packetData, captureInfo{
		Timestamp:      r.ifaces[ifaceID].decodeTimestamp(rawTimestamp),
		InterfaceIndex: ifaceID,
		CaptureLength:  captureLength,
		Length:         originalLength,
	}, nil
}

func (r *pcapngReader) readPacketBlock(body []byte) ([]byte, captureInfo, error) {
	if len(body) < 20 {
		return nil, captureInfo{}, errors.New("short packet block")
	}

	ifaceID := int(r.order.Uint16(body[:2]))
	if ifaceID < 0 || ifaceID >= len(r.ifaces) {
		return nil, captureInfo{}, fmt.Errorf("packet references unknown interface %d", ifaceID)
	}

	captureLength := int(r.order.Uint32(body[12:16]))
	originalLength := int(r.order.Uint32(body[16:20]))
	paddedLength := captureLength + padLength(captureLength)
	if len(body) < 20+paddedLength {
		return nil, captureInfo{}, errors.New("short packet data in packet block")
	}

	rawTimestamp := uint64(r.order.Uint32(body[4:8]))<<32 | uint64(r.order.Uint32(body[8:12]))
	packetData := append([]byte(nil), body[20:20+captureLength]...)
	return packetData, captureInfo{
		Timestamp:      r.ifaces[ifaceID].decodeTimestamp(rawTimestamp),
		InterfaceIndex: ifaceID,
		CaptureLength:  captureLength,
		Length:         originalLength,
	}, nil
}

func (r *pcapngReader) readSimplePacket(body []byte) ([]byte, captureInfo, error) {
	if len(body) < 4 {
		return nil, captureInfo{}, errors.New("short simple packet block")
	}

	originalLength := int(r.order.Uint32(body[:4]))
	captureLength := len(body) - 4
	if len(r.ifaces) > 0 && r.ifaces[0].SnapLength != 0 && captureLength > int(r.ifaces[0].SnapLength) {
		captureLength = int(r.ifaces[0].SnapLength)
	}
	if originalLength < captureLength {
		captureLength = originalLength
	}
	packetData := append([]byte(nil), body[4:4+captureLength]...)
	return packetData, captureInfo{
		InterfaceIndex: 0,
		CaptureLength:  captureLength,
		Length:         originalLength,
	}, nil
}

func newPcapInterface(linkType uint16, snapLength uint32) pcapInterface {
	iface := pcapInterface{
		LinkType:   linkType,
		SnapLength: snapLength,
	}
	_ = iface.setTimestampResolution(6)
	return iface
}

func (iface *pcapInterface) setTimestampResolution(raw uint8) error {
	iface.TimestampResolution = raw
	exponent := raw & 0x7f

	if raw&0x80 != 0 {
		if exponent > 63 {
			return fmt.Errorf("binary timestamp resolution exponent too large: %d", exponent)
		}
		iface.secondMask = uint64(1) << exponent
	} else {
		iface.secondMask = 1
		for i := uint8(0); i < exponent; i++ {
			iface.secondMask *= 10
		}
	}

	if iface.secondMask == 0 {
		return errors.New("invalid timestamp resolution")
	}
	iface.scaleUp = 1
	iface.scaleDown = 1
	if iface.secondMask < 1_000_000_000 {
		iface.scaleUp = 1_000_000_000 / iface.secondMask
	} else {
		iface.scaleDown = iface.secondMask / 1_000_000_000
		if iface.scaleDown == 0 {
			iface.scaleDown = 1
		}
	}
	return nil
}

func (iface pcapInterface) decodeTimestamp(raw uint64) time.Time {
	if iface.secondMask == 0 {
		return time.Unix(0, int64(raw)).UTC()
	}
	seconds := int64(raw/iface.secondMask) + iface.TimestampOffset
	nanos := int64(raw%iface.secondMask) * int64(iface.scaleUp) / int64(iface.scaleDown)
	return time.Unix(seconds, nanos).UTC()
}

func parsePcapngOptions(body []byte, order binary.ByteOrder, visit func(code uint16, value []byte) error) error {
	for len(body) > 0 {
		if len(body) < 4 {
			return errors.New("short pcapng option header")
		}
		code := order.Uint16(body[:2])
		length := int(order.Uint16(body[2:4]))
		body = body[4:]

		if code == pcapngOptionEnd {
			return nil
		}
		if len(body) < length {
			return errors.New("short pcapng option payload")
		}
		value := body[:length]
		if err := visit(code, value); err != nil {
			return err
		}

		padding := padLength(length)
		if len(body) < length+padding {
			return errors.New("short pcapng option padding")
		}
		body = body[length+padding:]
	}
	return nil
}

func newPcapngWriter(w io.Writer) (*pcapngWriter, error) {
	writer := &pcapngWriter{w: bufio.NewWriter(w)}
	if err := writer.writeSectionHeader(); err != nil {
		return nil, err
	}
	if err := writer.writeInterfaceDescription(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *pcapngWriter) writeSectionHeader() error {
	body := make([]byte, 16)
	binary.LittleEndian.PutUint32(body[:4], pcapngByteOrderMagic)
	binary.LittleEndian.PutUint16(body[4:6], 1)
	binary.LittleEndian.PutUint16(body[6:8], 0)
	binary.LittleEndian.PutUint64(body[8:16], ^uint64(0))
	return w.writeBlock(pcapngBlockTypeSectionHeader, body)
}

func (w *pcapngWriter) writeInterfaceDescription() error {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint16(body[:2], linkTypeEthernet)
	binary.LittleEndian.PutUint32(body[4:8], 0)

	options := make([]byte, 0, 32)
	options = append(options, buildPcapngOption(pcapngOptionIfName, []byte("restored"))...)
	options = append(options, buildPcapngOption(pcapngOptionIfTSRes, []byte{9})...)
	options = append(options, 0, 0, 0, 0)
	body = append(body, options...)
	return w.writeBlock(pcapngBlockTypeInterfaceDesc, body)
}

func (w *pcapngWriter) WritePacket(ci captureInfo, data []byte) error {
	body := make([]byte, 20)
	timestamp := uint64(ci.Timestamp.UnixNano())
	binary.LittleEndian.PutUint32(body[:4], 0)
	binary.LittleEndian.PutUint32(body[4:8], uint32(timestamp>>32))
	binary.LittleEndian.PutUint32(body[8:12], uint32(timestamp))
	binary.LittleEndian.PutUint32(body[12:16], uint32(len(data)))
	binary.LittleEndian.PutUint32(body[16:20], uint32(len(data)))

	body = append(body, data...)
	body = append(body, make([]byte, padLength(len(data)))...)
	return w.writeBlock(pcapngBlockTypeEnhanced, body)
}

func (w *pcapngWriter) Flush() error {
	return w.w.Flush()
}

func (w *pcapngWriter) writeBlock(blockType uint32, body []byte) error {
	totalLength := uint32(len(body) + 12)
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[:4], blockType)
	binary.LittleEndian.PutUint32(header[4:8], totalLength)

	trailer := make([]byte, 4)
	binary.LittleEndian.PutUint32(trailer, totalLength)

	if _, err := w.w.Write(header); err != nil {
		return err
	}
	if _, err := w.w.Write(body); err != nil {
		return err
	}
	_, err := w.w.Write(trailer)
	return err
}

func buildPcapngOption(code uint16, value []byte) []byte {
	out := make([]byte, 4+len(value)+padLength(len(value)))
	binary.LittleEndian.PutUint16(out[:2], code)
	binary.LittleEndian.PutUint16(out[2:4], uint16(len(value)))
	copy(out[4:], value)
	return out
}

func padLength(length int) int {
	return (4 - (length % 4)) % 4
}

func checksum(data []byte) uint16 {
	return finalizeChecksum(addChecksumBytes(0, data))
}

func udpChecksumIPv4(src, dst [4]byte, udp []byte) uint16 {
	sum := uint32(0)
	sum = addChecksumBytes(sum, src[:])
	sum = addChecksumBytes(sum, dst[:])
	sum += uint32(udpProtocol)
	sum += uint32(len(udp))
	result := finalizeChecksum(addChecksumBytes(sum, udp))
	if result == 0 {
		return 0xffff
	}
	return result
}

func udpChecksumIPv6(src, dst [16]byte, udp []byte) uint16 {
	sum := uint32(0)
	sum = addChecksumBytes(sum, src[:])
	sum = addChecksumBytes(sum, dst[:])
	sum += uint32(len(udp) >> 16)
	sum += uint32(len(udp) & 0xffff)
	sum += uint32(udpProtocol)
	result := finalizeChecksum(addChecksumBytes(sum, udp))
	if result == 0 {
		return 0xffff
	}
	return result
}

func addChecksumBytes(sum uint32, data []byte) uint32 {
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	return sum
}

func finalizeChecksum(sum uint32) uint16 {
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}
