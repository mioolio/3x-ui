package throttle

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"
)

const socksHandshakeTimeout = 10 * time.Second

// socksTarget is one SOCKS5 address: a hostname (ATYP 0x03) or an IP. Hostnames
// are passed through to the egress hop verbatim so DNS stays Xray's business.
type socksTarget struct {
	host string
	ip   netip.Addr
	port uint16
}

func (t socksTarget) valid() bool { return t.host != "" || t.ip.IsValid() }

func (t socksTarget) String() string {
	if t.ip.IsValid() {
		return netip.AddrPortFrom(t.ip, t.port).String()
	}
	return net.JoinHostPort(t.host, fmt.Sprint(t.port))
}

// readSocksTarget decodes the DST.ADDR/DST.PORT block after the request head.
func readSocksTarget(r io.Reader, atyp byte) (socksTarget, error) {
	var t socksTarget
	switch atyp {
	case 0x01:
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return t, err
		}
		t.ip = netip.AddrFrom4(b)
	case 0x04:
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return t, err
		}
		t.ip = netip.AddrFrom16(b)
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return t, err
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(r, name); err != nil {
			return t, err
		}
		t.host = string(name)
	default:
		return t, fmt.Errorf("unsupported SOCKS5 address type %d", atyp)
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return t, err
	}
	t.port = binary.BigEndian.Uint16(port[:])
	return t, nil
}

// writeTarget appends the wire form of t (ATYP + addr + port).
func appendTarget(dst []byte, t socksTarget) []byte {
	switch {
	case t.ip.IsValid() && t.ip.Is4():
		b := t.ip.As4()
		dst = append(dst, 0x01)
		dst = append(dst, b[:]...)
	case t.ip.IsValid():
		b := t.ip.As16()
		dst = append(dst, 0x04)
		dst = append(dst, b[:]...)
	default:
		dst = append(dst, 0x03, byte(len(t.host)))
		dst = append(dst, t.host...)
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], t.port)
	return append(dst, port[:]...)
}

// socksGreeting performs the method negotiation and RFC 1929 sub-negotiation,
// requiring username/password auth. The username identifies the throttled
// client; the password is accepted as-is. Returns 0xFF-equivalent (false) when
// the client cannot authenticate.
func socksGreeting(conn net.Conn) (string, bool) {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return "", false
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", false
	}
	hasUserPass := false
	for _, m := range methods {
		if m == 0x02 {
			hasUserPass = true
		}
	}
	if !hasUserPass {
		_, _ = conn.Write([]byte{0x05, 0xFF})
		return "", false
	}
	if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
		return "", false
	}
	var ver [1]byte
	if _, err := io.ReadFull(conn, ver[:]); err != nil || ver[0] != 0x01 {
		return "", false
	}
	var ulen [1]byte
	if _, err := io.ReadFull(conn, ulen[:]); err != nil {
		return "", false
	}
	uname := make([]byte, ulen[0])
	if _, err := io.ReadFull(conn, uname); err != nil {
		return "", false
	}
	var plen [1]byte
	if _, err := io.ReadFull(conn, plen[:]); err != nil {
		return "", false
	}
	pass := make([]byte, plen[0])
	if _, err := io.ReadFull(conn, pass); err != nil {
		return "", false
	}
	if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
		return "", false
	}
	return string(uname), true
}

// writeSocksReply emits a success/failure reply with an empty v4 bind address;
// Xray only reads the code byte.
func writeSocksReply(w io.Writer, code byte) {
	_, _ = w.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}

// parseDatagramHeader decodes a SOCKS5 UDP datagram head
// (RSV RSV FRAG ATYP ADDR PORT) into its target and payload offset.
// Fragmented datagrams (FRAG != 0) are reported as invalid.
func parseDatagramHeader(data []byte) (socksTarget, int, bool) {
	if len(data) < 4 || data[2] != 0 {
		return socksTarget{}, 0, false
	}
	addr, err := readSocksTargetBytes(data[3:])
	if err != nil {
		return socksTarget{}, 0, false
	}
	return addr, 3 + socksAddrBlockLen(data[3:]), true
}

// socksAddrBlockLen returns the wire length of the ATYP ADDR PORT block at the
// front of b, or 0 when b is too short to carry the declared address — callers
// use it as a payload offset, so a truncated block must never yield one.
func socksAddrBlockLen(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	switch b[0] {
	case 0x01:
		if len(b) < 7 {
			return 0
		}
		return 7
	case 0x04:
		if len(b) < 19 {
			return 0
		}
		return 19
	case 0x03:
		if len(b) < 2 {
			return 0
		}
		full := 1 + 1 + int(b[1]) + 2
		if len(b) < full {
			return 0
		}
		return full
	default:
		return 0
	}
}

// readSocksTargetBytes is readSocksTarget over a byte slice.
func readSocksTargetBytes(b []byte) (socksTarget, error) {
	if len(b) == 0 {
		return socksTarget{}, fmt.Errorf("empty address block")
	}
	atyp := b[0]
	switch atyp {
	case 0x01:
		if len(b) < 1 + 4 + 2 {
			return socksTarget{}, fmt.Errorf("short v4 address")
		}
		var a [4]byte
		copy(a[:], b[1:5])
		return socksTarget{ip: netip.AddrFrom4(a), port: binary.BigEndian.Uint16(b[5:7])}, nil
	case 0x04:
		if len(b) < 1 + 16 + 2 {
			return socksTarget{}, fmt.Errorf("short v6 address")
		}
		var a [16]byte
		copy(a[:], b[1:17])
		return socksTarget{ip: netip.AddrFrom16(a), port: binary.BigEndian.Uint16(b[17:19])}, nil
	case 0x03:
		if len(b) < 2 {
			return socksTarget{}, fmt.Errorf("short domain length")
		}
		l := int(b[1])
		if l == 0 || len(b) < 1+1+l+2 {
			return socksTarget{}, fmt.Errorf("short domain address")
		}
		return socksTarget{host: string(b[2 : 2+l]), port: binary.BigEndian.Uint16(b[2+l : 4+l])}, nil
	default:
		return socksTarget{}, fmt.Errorf("unsupported address type %d", atyp)
	}
}

// buildDatagramHeader renders the RSV RSV FRAG ATYP ADDR PORT head for t.
func buildDatagramHeader(t socksTarget) []byte {
	out := []byte{0x00, 0x00, 0x00}
	return appendTarget(out, t)
}

// socksDialTCP opens a CONNECT tunnel to target through the SOCKS5 server at
// egressAddr (no-auth). The returned conn carries raw payload bytes.
func socksDialTCP(egressAddr string, target socksTarget, dialer func(network, address string) (net.Conn, error)) (net.Conn, error) {
	conn, err := dialer("tcp", egressAddr)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil || hdr[0] != 0x05 || hdr[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("throttle: egress greeting failed")
	}
	req := appendTarget([]byte{0x05, 0x01, 0x00}, target)
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		conn.Close()
		return nil, err
	}
	if head[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("throttle: egress connect rejected (%d)", head[1])
	}
	if _, err := readSocksTarget(conn, head[3]); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// udpAssociate is the client half of one SOCKS5 UDP ASSOCIATE to the egress
// hop: a TCP control connection plus the datagram socket pointed at the
// server's relay address.
type udpAssociate struct {
	control net.Conn
	sock    *net.UDPConn
	relay   *net.UDPAddr
}

// socksUDPAssociate establishes a UDP ASSOCIATE through egressAddr. The
// association lives until closed or the control connection drops server-side.
func socksUDPAssociate(egressAddr string, dialer func(network, address string) (net.Conn, error)) (*udpAssociate, error) {
	control, err := dialer("tcp", egressAddr)
	if err != nil {
		return nil, err
	}
	if _, err := control.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		control.Close()
		return nil, err
	}
	var hdr [2]byte
	if _, err := io.ReadFull(control, hdr[:]); err != nil || hdr[0] != 0x05 || hdr[1] != 0x00 {
		control.Close()
		return nil, fmt.Errorf("throttle: egress greeting failed")
	}
	// DST.ADDR 0.0.0.0:0 — the server picks the relay endpoint.
	req := []byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	if _, err := control.Write(req); err != nil {
		control.Close()
		return nil, err
	}
	var head [4]byte
	if _, err := io.ReadFull(control, head[:]); err != nil {
		control.Close()
		return nil, err
	}
	if head[1] != 0x00 {
		control.Close()
		return nil, fmt.Errorf("throttle: egress associate rejected (%d)", head[1])
	}
	bind, err := readSocksTarget(control, head[3])
	if err != nil {
		control.Close()
		return nil, err
	}
	relayHost := ""
	if bind.ip.IsValid() && !bind.ip.IsUnspecified() {
		relayHost = bind.ip.String()
	} else {
		// A 0.0.0.0 BND.ADDR means "same host as the server" — always loopback here.
		relayHost = "127.0.0.1"
	}
	relay := &net.UDPAddr{IP: net.ParseIP(relayHost), Port: int(bind.port)}
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		control.Close()
		return nil, err
	}
	return &udpAssociate{control: control, sock: sock, relay: relay}, nil
}

// send writes one payload to dst through the associated relay socket.
func (a *udpAssociate) send(target socksTarget, payload []byte) error {
	dg := buildDatagramHeader(target)
	dg = append(dg, payload...)
	_, err := a.sock.WriteToUDP(dg, a.relay)
	return err
}

// receive reads one reply datagram, returning its original destination and the
// payload with the header stripped.
func (a *udpAssociate) receive(buf []byte) (socksTarget, []byte, error) {
	n, _, err := a.sock.ReadFromUDP(buf)
	if err != nil {
		return socksTarget{}, nil, err
	}
	target, off, ok := parseDatagramHeader(buf[:n])
	if !ok {
		return socksTarget{}, nil, fmt.Errorf("throttle: bad egress datagram")
	}
	return target, buf[off:n], nil
}

func (a *udpAssociate) close() {
	a.control.Close()
	a.sock.Close()
}
