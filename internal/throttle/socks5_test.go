package throttle

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestKbpsToBytesPerSec(t *testing.T) {
	tests := []struct {
		kbps int
		want int64
	}{
		{0, 0},
		{-5, 0},
		{8, 1000},
		{1000, 125000},
	}
	for _, tt := range tests {
		if got := KbpsToBytesPerSec(tt.kbps); got != tt.want {
			t.Errorf("KbpsToBytesPerSec(%d) = %d, want %d", tt.kbps, got, tt.want)
		}
	}
}

func TestDatagramHeaderRoundTrip(t *testing.T) {
	targets := []socksTarget{
		{ip: netip.AddrFrom4([4]byte{1, 2, 3, 4}), port: 443},
		{ip: netip.MustParseAddr("2001:db8::1"), port: 8080},
		{host: "example.com", port: 53},
	}
	for _, want := range targets {
		dg := buildDatagramHeader(want)
		got, off, ok := parseDatagramHeader(dg)
		if !ok {
			t.Fatalf("parseDatagramHeader(% x): not ok", dg)
		}
		if off != 3+socksAddrBlockLen(dg[3:]) {
			t.Fatalf("offset = %d, want %d", off, 3+socksAddrBlockLen(dg[3:]))
		}
		if got.String() != want.String() {
			t.Fatalf("round trip = %s, want %s", got, want)
		}
	}
}

func TestParseDatagramHeaderRejectsFragments(t *testing.T) {
	dg := buildDatagramHeader(socksTarget{ip: netip.AddrFrom4([4]byte{1, 2, 3, 4}), port: 80})
	dg[2] = 1 // FRAG
	if _, _, ok := parseDatagramHeader(dg); ok {
		t.Fatal("fragmented datagram accepted")
	}
	if _, _, ok := parseDatagramHeader(dg[:3]); ok {
		t.Fatal("short datagram accepted")
	}
}

func TestReadSocksTargetRejectsUnknownATYP(t *testing.T) {
	if _, err := readSocksTargetBytes([]byte{0x7f}); err == nil {
		t.Fatal("unknown address type accepted")
	}
}

func TestAppendTargetDomainLength(t *testing.T) {
	dg := buildDatagramHeader(socksTarget{host: "abc", port: 80})
	// 3 (RSV RSV FRAG) + 1 (ATYP) + 1 (len) + 3 (host) + 2 (port)
	if len(dg) != 10 {
		t.Fatalf("len = %d, want 10", len(dg))
	}
	if !bytes.Equal(dg[3:5], []byte{0x03, 0x03}) {
		t.Fatalf("domain header = % x, want 0303", dg[3:5])
	}
}

func TestSocksAddrBlockLen(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want int
	}{
		{"v4", []byte{0x01, 0, 0, 0, 0, 0, 0}, 7},
		{"v6", append([]byte{0x04}, make([]byte, 18)...), 19},
		{"domain", []byte{0x03, 0x04, 'a', 'b', 'c', 'd', 0, 0}, 8},
		{"empty", nil, 0},
		{"truncated domain", []byte{0x03, 0x10, 'a'}, 0},
	}
	for _, tt := range tests {
		if got := socksAddrBlockLen(tt.in); got != tt.want {
			t.Errorf("%s: socksAddrBlockLen = %d, want %d", tt.name, got, tt.want)
		}
	}
}
