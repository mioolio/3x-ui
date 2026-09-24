package wireguard

import (
	"context"
	"net/netip"
	"sync"
	"testing"

	"github.com/xtls/xray-core/common/protocol"
)

func TestGetUserByAddrUsesLongestPrefixAndRejectsAmbiguity(t *testing.T) {
	users := &sync.Map{}
	server := &Server{users: users}
	makeUser := func(email string, prefixes ...string) *protocol.MemoryUser {
		t.Helper()
		account := &MemoryAccount{}
		for _, raw := range prefixes {
			account.AllowedIPs = append(account.AllowedIPs, netip.MustParsePrefix(raw))
		}
		return &protocol.MemoryUser{Email: email, Account: account}
	}
	broad := makeUser("broad", "10.9.0.0/16")
	narrow := makeUser("narrow", "10.9.4.0/24")
	users.Store("broad", broad)
	users.Store("narrow", narrow)
	addr := netip.MustParseAddr("10.9.4.2")
	for range 50 {
		if got := server.GetUserByAddr(context.Background(), addr); got != narrow {
			t.Fatalf("overlapping tunnel prefixes mapped to %+v, want the most specific peer", got)
		}
	}
	if got := server.GetUserByAddr(context.Background(), netip.MustParseAddr("10.9.6.2")); got != broad {
		t.Fatalf("unoverlapped address mapped to %+v, want broad peer", got)
	}
	users.Store("duplicate", makeUser("duplicate", "10.9.4.0/24"))
	if got := server.GetUserByAddr(context.Background(), addr); got != nil {
		t.Fatalf("duplicate tunnel prefix must not charge an arbitrary peer: %+v", got)
	}
}
