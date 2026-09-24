package panel

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

func TestPatchedBuildDoesNotOfferOfficialPanelUpdate(t *testing.T) {
	if !config.RequiresPatchedCore {
		t.Skip("guard applies only to the patched distribution")
	}
	s := &PanelService{}
	info, err := s.GetUpdateInfo()
	if err != nil || info == nil {
		t.Fatalf("update info = %v, %v", info, err)
	}
	if info.UpdateAvailable || info.UpdateSupported || info.LatestVersion != "" || info.CurrentVersion != config.GetPanelVersion() {
		t.Fatalf("unsafe upstream update advertised: %+v", info)
	}
	for _, start := range []func() (int64, error){s.StartUpdate, func() (int64, error) { return s.StartUpdateChannel(true) }} {
		runID, err := start()
		if runID != 0 || err == nil || !strings.Contains(err.Error(), "patched Xray core") {
			t.Fatalf("start official update = %d, %v; want refusal without launch", runID, err)
		}
	}
}
