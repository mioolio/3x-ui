package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

func TestOfficialXrayUpdateBlockedBeforeServiceAccess(t *testing.T) {
	if !config.RequiresPatchedCore {
		t.Skip("guard applies only to the patched distribution")
	}
	// A zero service has no Xray process or settings client. Reaching either
	// would panic or attempt a network request before this guard returns.
	s := &ServerService{}
	versions, err := s.GetXrayVersionsCached()
	if err != nil || len(versions) != 0 {
		t.Fatalf("upstream Xray versions = %v, %v; want empty list", versions, err)
	}
	if err := s.UpdateXray("v99.99.99"); err == nil || !strings.Contains(err.Error(), "patched Xray core") {
		t.Fatalf("official Xray update error = %v; want patched-core refusal", err)
	}
}

func TestNodeOfficialUpdateBlockedBeforeFanout(t *testing.T) {
	if !config.RequiresPatchedCore {
		t.Skip("guard applies only to the patched distribution")
	}
	// No runtime manager or node DB exists here: both are downstream of the
	// guard, as are all node HTTP requests.
	results, err := (&NodeService{}).UpdatePanels([]int{1, 2}, false)
	if err == nil || !strings.Contains(err.Error(), "patched Xray core") || results != nil {
		t.Fatalf("node update = %v, %v; want refusal before fanout", results, err)
	}
}
