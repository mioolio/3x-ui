package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

func TestRemoteOfficialPanelUpdateBlockedBeforeRequest(t *testing.T) {
	if !config.RequiresPatchedCore {
		t.Skip("guard applies only to the patched distribution")
	}
	// A zero Remote has no client or URL; accessing either would fail before
	// returning this policy error.
	err := (&Remote{}).UpdatePanel(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "patched Xray core") {
		t.Fatalf("remote update error = %v; want patched-core refusal", err)
	}
}
