package sub

import (
	"math"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestSubscriptionStatusHonorsTimedGraceWithoutExtraQuota(t *testing.T) {
	initSubDB(t)
	expiry := time.Now().Add(-time.Hour).UnixMilli()
	client := model.ClientRecord{Email: "timed-grace@example.invalid", SubID: "timed-grace", Enable: true,
		ExpiryTime: expiry, GraceHours: 2, GraceUpKbps: 64, GraceDownKbps: 128}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	traffic := xray.ClientTraffic{Email: client.Email, Enable: true, Up: 10 << 30, ExpiryTime: expiry}
	service := new(SubService)
	status := service.loadSubPolicyStatus(client.SubID, traffic, nil)
	if status == nil || !status.GraceActive || status.EffectiveState != "grace" || status.GraceRemainingBytes != math.MaxInt64 {
		t.Fatalf("timed grace status = %+v", status)
	}
	if got := service.subscriptionHeaderTraffic(client.SubID, traffic).ExpiryTime; got != expiry+int64(2*time.Hour/time.Millisecond) {
		t.Fatalf("subscription expiry = %d, want the grace deadline", got)
	}
}
