package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestWireGuardPeerDirectionalLimitsReachPatchedCore(t *testing.T) {
	const email, tag = "peer@wg.test", "wg-rate"
	seedWGInbound(t, tag, 51849, []model.Client{{
		Email: email, Enable: true, PublicKey: "peer-public-key", AllowedIPs: []string{"10.0.0.2/32"},
	}})
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	db := database.GetDB()
	var inbound model.Inbound
	if err := db.Where("tag = ?", tag).First(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	var client model.ClientRecord
	if err := db.Where("email = ?", email).First(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&inbound).Updates(map[string]any{
		"speed_limit_up_kbps": int64(15_000), "speed_limit_down_kbps": int64(15_000),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&client).Updates(map[string]any{
		"speed_limit_up_kbps": int64(10_000), "speed_limit_down_kbps": int64(8_000),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ClientInbound{}).
		Where("client_id = ? AND inbound_id = ?", client.Id, inbound.Id).
		Updates(map[string]any{"speed_limit_up_kbps": int64(12_000), "speed_limit_down_kbps": int64(5_000)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&XrayService{}).RefreshRatePolicy(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(xray.GetRatePolicyPath())
	if err != nil {
		t.Fatal(err)
	}
	var policy ratePolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	if got := policy.InboundDirections[tag]; got != (directionalRate{15_000, 15_000}) {
		t.Fatalf("WireGuard inbound ceiling = %+v", got)
	}
	if got := policy.ClientDirections[email]; got != (directionalRate{10_000, 8_000}) {
		t.Fatalf("WireGuard client ceiling = %+v", got)
	}
	if got := policy.OverrideDirections[tag+"\x00"+email]; got != (directionalRate{12_000, 5_000}) {
		t.Fatalf("WireGuard association ceiling = %+v", got)
	}
	peers := wgPeerList(t, wgInboundEmittedSettings(t, tag))
	if len(peers) != 1 || peers[0]["email"] != email {
		t.Fatalf("WireGuard user identity required by dispatcher is missing: %+v", peers)
	}
}
