package sub

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// A single getSubs entry can hold several links (one per host of an inbound)
// joined by newlines. BuildPageData must split them into one entry per link, with
// the email replicated, so the subpage renders one row per host instead of
// collapsing them onto a single mangled line.
func TestBuildPageData_SplitsMultiHostLinks(t *testing.T) {
	s := &SubService{}
	subs := []string{
		"vless://a@h1:443?type=tcp#DE-john@x\nvless://a@h2:443?type=tcp#DE-john@x\nvless://a@h3:443?type=tcp#DE-john@x",
		"vless://b@h:443?type=tcp#FR-alice@x",
	}
	emails := []string{"john@x", "alice@x"}

	page := s.BuildPageData("s1", "", xray.ClientTraffic{}, 0, subs, emails, "", "", "", "/", "", "")

	if len(page.Result) != 4 {
		t.Fatalf("Result len = %d, want 4 (3 host links + 1 single link)", len(page.Result))
	}
	for i, link := range page.Result {
		if strings.Contains(link, "\n") {
			t.Fatalf("Result[%d] still multi-line: %q", i, link)
		}
	}
	wantEmails := []string{"john@x", "john@x", "john@x", "alice@x"}
	if !reflect.DeepEqual(page.Emails, wantEmails) {
		t.Fatalf("Emails = %v, want %v", page.Emails, wantEmails)
	}
}

func TestSubIsOnline(t *testing.T) {
	tests := []struct {
		name   string
		sub    []string
		online []string
		want   bool
	}{
		{name: "nobody online", sub: []string{"a@x"}, online: nil, want: false},
		{name: "no sub emails", sub: nil, online: []string{"a@x"}, want: false},
		{name: "sub client online", sub: []string{"a@x"}, online: []string{"z@x", "a@x"}, want: true},
		{name: "only other clients online", sub: []string{"a@x"}, online: []string{"z@x"}, want: false},
		{name: "any of several sub entries online", sub: []string{"a@x", "b@x"}, online: []string{"b@x"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subIsOnline(tt.sub, tt.online); got != tt.want {
				t.Fatalf("subIsOnline(%v, %v) = %v, want %v", tt.sub, tt.online, got, tt.want)
			}
		})
	}
}

func TestBuildPageData_IsOnlineFalseWithoutLiveConnections(t *testing.T) {
	s := &SubService{}

	page := s.BuildPageData("s1", "", xray.ClientTraffic{}, 0, []string{"vless://a@h1:443?type=tcp#DE-john@x"}, []string{"john@x"}, "", "", "", "/", "", "")

	if page.IsOnline {
		t.Fatal("IsOnline must be false when the subscription's client has no live connection")
	}
}

func TestBuildPageData_UsesChargedAllowanceAfterNodeDiscount(t *testing.T) {
	initSubDB(t)
	page := (&SubService{}).BuildPageData("discount", "", xray.ClientTraffic{
		Up: 1 << 30, Down: 1 << 30, ChargeExtraBytes: 1 << 30,
		ChargeDiscountBytes: 2 << 30, Total: 4 << 30,
	}, 0, nil, nil, "", "", "", "/", "", "")
	if page.UsedByte != 1<<30 {
		t.Fatalf("charged usage=%d, want 1 GiB after discount", page.UsedByte)
	}
	if page.Used != common.FormatTraffic(1<<30) || page.Remained != common.FormatTraffic(3<<30) {
		t.Fatalf("display used/remaining=%q/%q, want 1 GiB/3 GiB", page.Used, page.Remained)
	}
}
