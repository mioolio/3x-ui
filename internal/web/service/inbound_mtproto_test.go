package service

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/mtproto"
)

func TestMtprotoRoutesThroughXray(t *testing.T) {
	cases := map[string]struct {
		ib   *model.Inbound
		want bool
	}{
		"routed":      {&model.Inbound{Protocol: model.MTProto, Settings: `{"routeThroughXray":true}`}, true},
		"off":         {&model.Inbound{Protocol: model.MTProto, Settings: `{"routeThroughXray":false}`}, false},
		"capped":      {&model.Inbound{Protocol: model.MTProto, SpeedLimitKbps: 900, Settings: `{"routeThroughXray":false}`}, true},
		"absent":      {&model.Inbound{Protocol: model.MTProto, Settings: `{}`}, false},
		"non-mtproto": {&model.Inbound{Protocol: model.VLESS, Settings: `{"routeThroughXray":true}`}, false},
		"bad json":    {&model.Inbound{Protocol: model.MTProto, Settings: `{nope`}, false},
		"nil":         {nil, false},
	}
	for name, c := range cases {
		if got := mtprotoRoutesThroughXray(c.ib); got != c.want {
			t.Fatalf("%s: got %v want %v", name, got, c.want)
		}
	}
}

func routeXrayPortOf(t *testing.T, settings string) int {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		t.Fatalf("settings not valid JSON: %v\n%s", err, settings)
	}
	return settingsRouteXrayPort(parsed)
}

func TestNormalizeMtprotoXrayPort(t *testing.T) {
	s := &InboundService{}

	// Non-mtproto inbounds are left alone.
	ib := &model.Inbound{Protocol: model.VLESS, Settings: `{"x":1}`}
	if err := s.normalizeMtprotoXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if ib.Settings != `{"x":1}` {
		t.Fatalf("non-mtproto settings must be untouched, got %s", ib.Settings)
	}

	// Routing on with no existing port allocates a fresh one.
	ib = &model.Inbound{Protocol: model.MTProto, Settings: `{"routeThroughXray":true}`}
	if err := s.normalizeMtprotoXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p <= 0 {
		t.Fatalf("a routed inbound must get a port, got %d", p)
	}

	// A submitted port outside the TCP range must never reach Xray's config.
	for _, invalid := range []string{"99999", "40000.5"} {
		ib = &model.Inbound{Protocol: model.MTProto,
			Settings: `{"routeThroughXray":true,"routeXrayPort":` + invalid + `}`}
		if err := s.normalizeMtprotoXrayPort(ib, ""); err != nil {
			t.Fatal(err)
		}
		if p := routeXrayPortOf(t, ib.Settings); p < 1 || p > 65535 {
			t.Fatalf("invalid submitted port %s was not replaced: %s", invalid, ib.Settings)
		}
	}

	// On update, the stored port wins over both a missing and a client-echoed
	// value — the backend owns it, so no churn and no client override.
	ib = &model.Inbound{Protocol: model.MTProto, Settings: `{"routeThroughXray":true,"routeXrayPort":99999}`}
	if err := s.normalizeMtprotoXrayPort(ib, `{"routeThroughXray":true,"routeXrayPort":51000}`); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p != 51000 {
		t.Fatalf("stored port must win, got %d", p)
	}

	// An already-present port (no old settings) is stable and not re-marshaled.
	const stable = `{"routeThroughXray":true,"routeXrayPort":52000}`
	ib = &model.Inbound{Protocol: model.MTProto, Settings: stable}
	if err := s.normalizeMtprotoXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if ib.Settings != stable {
		t.Fatalf("stable settings must pass through untouched, got %s", ib.Settings)
	}

	// Turning routing off strips both the bridge port and the inert outbound.
	ib = &model.Inbound{Protocol: model.MTProto, Settings: `{"routeThroughXray":false,"routeXrayPort":53000,"outboundTag":"warp"}`}
	if err := s.normalizeMtprotoXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p != 0 {
		t.Fatalf("disabling routing must drop the port, got %d", p)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["outboundTag"]; ok {
		t.Fatalf("disabling routing must drop the inert outbound tag, got %s", ib.Settings)
	}

	// A ceiling routes through the Xray shaper while retaining the user's
	// explicit egress-routing choice. Removing the ceiling restores direct mtg.
	ib = &model.Inbound{Protocol: model.MTProto, SpeedLimitKbps: 500,
		Settings: `{"routeThroughXray":false,"routeXrayPort":54000,"outboundTag":"warp"}`}
	if err := s.normalizeMtprotoXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p != 54000 {
		t.Fatalf("rate bridge lost its stable port: %d", p)
	}
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["outboundTag"]; ok {
		t.Fatalf("rate-only bridge must not retain an old custom egress: %s", ib.Settings)
	}
	password, ok := parsed["rateBridgePassword"].(string)
	if !ok {
		t.Fatalf("capped inbound needs a bridge credential: %s", ib.Settings)
	}
	if _, err := uuid.Parse(password); err != nil {
		t.Fatalf("bridge credential must be a generated UUID: %q: %v", password, err)
	}
	// The backend-owned credential is stable across edits and is not replaced by
	// a value echoed or forged in the submitted settings.
	previous := ib.Settings
	ib.Settings = `{"routeThroughXray":false,"routeXrayPort":54001,"rateBridgePassword":"ed30bf3c-856f-427b-9102-a2171cf8978b"}`
	if err := s.normalizeMtprotoXrayPort(ib, previous); err != nil {
		t.Fatal(err)
	}
	parsed = nil
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["rateBridgePassword"] != password || routeXrayPortOf(t, ib.Settings) != 54000 {
		t.Fatalf("saved bridge identity must win over submitted values: %s", ib.Settings)
	}
	ib.SpeedLimitKbps = 0
	if err := s.normalizeMtprotoXrayPort(ib, ib.Settings); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p != 0 {
		t.Fatalf("removing the last ceiling must release its bridge port: %d", p)
	}
	parsed = nil
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["rateBridgePassword"]; ok {
		t.Fatalf("removing the ceiling must also release its credential: %s", ib.Settings)
	}
}

func TestGetXrayConfigUpgradesExistingCappedMtprotoInbound(t *testing.T) {
	setupSettingTestDB(t)
	db := database.GetDB()
	ib := &model.Inbound{Tag: "legacy-mtproto-rate", Enable: true, Port: 36432,
		Protocol: model.MTProto, SpeedLimitKbps: 300,
		Settings: `{"routeThroughXray":false,"clients":[{"email":"legacy","secret":"eedeadbeef","enable":true}]}`}
	if err := db.Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := mtproto.InstanceFromInbound(ib); ok {
		t.Fatal("an unmigrated capped inbound must not run without its bridge")
	}

	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	var saved model.Inbound
	if err := db.First(&saved, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(saved.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	password, ok := settings["rateBridgePassword"].(string)
	if !ok || password == "" {
		t.Fatalf("upgrade did not persist bridge credential: %s", saved.Settings)
	}
	port := routeXrayPortOf(t, saved.Settings)
	if port <= 0 || port > 65535 {
		t.Fatalf("upgrade did not persist valid bridge port: %d", port)
	}
	inst, ok := mtproto.InstanceFromInbound(&saved)
	if !ok || !inst.RateViaXray || inst.XrayRoutePort != port || inst.RateBridgePassword != password {
		t.Fatalf("sidecar does not match persisted bridge: %+v, usable=%v", inst, ok)
	}
	var bridgeFound bool
	for _, ingress := range cfg.InboundConfigs {
		if ingress.Tag != saved.Tag {
			continue
		}
		bridgeFound = ingress.Port == port && ingress.Protocol == "socks"
	}
	if !bridgeFound {
		t.Fatalf("Xray config has no matching bridge for %q on %d", saved.Tag, port)
	}
	// The next config build must reuse the same identity, so a harmless
	// restart does not break the running sidecar or rotate its credentials.
	if _, err := (&XrayService{}).GetXrayConfig(); err != nil {
		t.Fatal(err)
	}
	var again model.Inbound
	if err := db.First(&again, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if again.Settings != saved.Settings {
		t.Fatalf("repeat config build changed bridge settings: %s -> %s", saved.Settings, again.Settings)
	}
}

func TestFillProtocolDefaultsMtproto(t *testing.T) {
	cs := &ClientService{}
	ib := &model.Inbound{Protocol: model.MTProto, Settings: `{"fakeTlsDomain":"example.com"}`}

	c := &model.Client{Email: "u"}
	if err := cs.fillProtocolDefaults(c, ib); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Secret, "ee") || !strings.HasSuffix(c.Secret, hex.EncodeToString([]byte("example.com"))) {
		t.Fatalf("mtproto client should get a FakeTLS secret fronting the inbound domain, got %q", c.Secret)
	}

	// An existing secret is not overwritten.
	pre := &model.Client{Email: "v", Secret: "eepreset"}
	if err := cs.fillProtocolDefaults(pre, ib); err != nil {
		t.Fatal(err)
	}
	if pre.Secret != "eepreset" {
		t.Fatalf("an existing secret must be preserved, got %q", pre.Secret)
	}

	// With no inbound domain the default fronting host is used.
	c2 := &model.Client{Email: "w"}
	if err := cs.fillProtocolDefaults(c2, &model.Inbound{Protocol: model.MTProto, Settings: `{}`}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c2.Secret, hex.EncodeToString([]byte(defaultMtprotoDomain))) {
		t.Fatalf("a domainless inbound should front the default host, got %q", c2.Secret)
	}
}

func TestNormalizeMtprotoSecretHealsClients(t *testing.T) {
	s := &InboundService{}
	ib := &model.Inbound{Protocol: model.MTProto, Settings: `{"fakeTlsDomain":"a.com","secret":"eedeadbeef","clients":[{"email":"x","secret":""}]}`}
	s.normalizeMtprotoSecret(ib)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		t.Fatalf("healed settings not valid json: %v", err)
	}
	if _, ok := parsed["secret"]; ok {
		t.Fatalf("the vestigial inbound-level secret must be stripped, got %q", ib.Settings)
	}
	clients := parsed["clients"].([]any)
	got := clients[0].(map[string]any)["secret"].(string)
	if !strings.HasPrefix(got, "ee") || !strings.HasSuffix(got, hex.EncodeToString([]byte("a.com"))) {
		t.Fatalf("client secret should be healed to front the inbound domain, got %q", got)
	}
}
