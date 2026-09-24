package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func cycleTestRemote(t *testing.T, descendants any, descendantsStatus int) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/panel/api/server/status":
			_, _ = w.Write([]byte(`{"success":true,"obj":{"panelGuid":"b-guid"}}`))
		case "/panel/api/server/descendants":
			if descendantsStatus != http.StatusOK {
				w.WriteHeader(descendantsStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": descendants})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse remote URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse remote port: %v", err)
	}
	return u.Hostname(), port
}

func postNodeCycleRequest(t *testing.T, engine http.Handler, path string, body any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body=%s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestNodeAddRejectsReciprocalLinkBeforeSaving(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	selfGuid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil {
		t.Fatalf("GetPanelGuid: %v", err)
	}
	host, port := cycleTestRemote(t, []map[string]any{{"guid": selfGuid}}, http.StatusOK)
	token := "test-token"
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/add", service.NodeMutationRequest{
		Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/", ApiToken: &token,
		Enable: true, AllowPrivateAddress: true,
	})
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, "create a cycle") {
		t.Fatalf("reciprocal link was not rejected: %s", body)
	}
	var count int64
	if err := database.GetDB().Model(&model.Node{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rejected node left %d rows, err=%v", count, err)
	}
}

func TestNodeAddOutboundRejectsReciprocalLinkBeforeSaving(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	selfGuid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil {
		t.Fatalf("GetPanelGuid: %v", err)
	}
	host, port := cycleTestRemote(t, []map[string]any{{"guid": selfGuid}}, http.StatusOK)
	token := "test-token"
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/add", service.NodeMutationRequest{
		Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/", ApiToken: &token,
		OutboundTag: "proxy", Enable: true, AllowPrivateAddress: true,
	})
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, "create a cycle") {
		t.Fatalf("reciprocal outbound link was not rejected: %s", body)
	}
	var count int64
	if err := database.GetDB().Model(&model.Node{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rejected outbound node left %d rows, err=%v", count, err)
	}
}

func TestNodeAddDisabledOutboundCanBeSavedOffline(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	token := "test-token"
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/add", service.NodeMutationRequest{
		Name: "offline B", Scheme: "http", Address: "127.0.0.1", Port: 1, BasePath: "/", ApiToken: &token,
		OutboundTag: "proxy", Enable: false, AllowPrivateAddress: true,
	})
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("disabled outbound node should save offline: %s", body)
	}
	var got model.Node
	if err := database.GetDB().First(&got).Error; err != nil || got.Enable || got.OutboundTag != "proxy" {
		t.Fatalf("disabled outbound node not stored: enable=%v outbound=%q err=%v", got.Enable, got.OutboundTag, err)
	}
}

func TestNodeAddRequiresTopologyEndpoint(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	host, port := cycleTestRemote(t, nil, http.StatusNotFound)
	token := "test-token"
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/add", service.NodeMutationRequest{
		Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/", ApiToken: &token,
		Enable: true, AllowPrivateAddress: true,
	})
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, "cannot verify node topology") {
		t.Fatalf("missing descendants route was not rejected: %s", body)
	}
}

func TestNodeAddAllowsAcyclicLink(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	host, port := cycleTestRemote(t, []any{}, http.StatusOK)
	token := "test-token"
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/add", service.NodeMutationRequest{
		Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/", ApiToken: &token,
		Enable: true, AllowPrivateAddress: true,
	})
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("acyclic link was rejected: %s", body)
	}
}

func TestNodeEditCanDisableExistingCycle(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	selfGuid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil {
		t.Fatalf("GetPanelGuid: %v", err)
	}
	host, port := cycleTestRemote(t, []map[string]any{{"guid": selfGuid}}, http.StatusOK)
	node := &model.Node{Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/", ApiToken: "test-token", Enable: true, AllowPrivateAddress: true, Guid: "b-guid"}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed existing cycle: %v", err)
	}
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/update/"+strconv.Itoa(node.Id), service.NodeMutationRequest{
		Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/",
		Enable: false, AllowPrivateAddress: true,
	})
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("disabling an existing cycle was rejected: %s", body)
	}
	var got model.Node
	if err := database.GetDB().First(&got, node.Id).Error; err != nil || got.Enable {
		t.Fatalf("node still enabled after disabling edit: enable=%v err=%v", got.Enable, err)
	}
}

func TestNodeEditRejectsNewReciprocalTarget(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	selfGuid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil {
		t.Fatalf("GetPanelGuid: %v", err)
	}
	host, port := cycleTestRemote(t, []map[string]any{{"guid": selfGuid}}, http.StatusOK)
	node := &model.Node{Name: "old", Scheme: "http", Address: "old.example.test", Port: 2053, BasePath: "/", ApiToken: "test-token", Enable: true, Guid: "old-guid"}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed old node: %v", err)
	}
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/update/"+strconv.Itoa(node.Id), service.NodeMutationRequest{
		Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/",
		Enable: true, AllowPrivateAddress: true,
	})
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, "create a cycle") {
		t.Fatalf("cyclic target update was not rejected: %s", body)
	}
	var got model.Node
	if err := database.GetDB().First(&got, node.Id).Error; err != nil || got.Address != node.Address {
		t.Fatalf("rejected update changed node address to %q, err=%v", got.Address, err)
	}
}

func TestNodeEditOutboundRejectsNewReciprocalTarget(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	selfGuid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil {
		t.Fatalf("GetPanelGuid: %v", err)
	}
	host, port := cycleTestRemote(t, []map[string]any{{"guid": selfGuid}}, http.StatusOK)
	node := &model.Node{Name: "old", Scheme: "http", Address: "old.example.test", Port: 2053, BasePath: "/", ApiToken: "test-token", OutboundTag: "proxy", Enable: true, Guid: "old-guid"}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed old outbound node: %v", err)
	}
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/update/"+strconv.Itoa(node.Id), service.NodeMutationRequest{
		Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/",
		OutboundTag: "proxy", Enable: true, AllowPrivateAddress: true,
	})
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, "create a cycle") {
		t.Fatalf("cyclic outbound target update was not rejected: %s", body)
	}
	var got model.Node
	if err := database.GetDB().First(&got, node.Id).Error; err != nil || got.Address != node.Address {
		t.Fatalf("rejected outbound update changed node address to %q, err=%v", got.Address, err)
	}
}

func TestNodeEnableRejectsExistingCycle(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	selfGuid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil {
		t.Fatalf("GetPanelGuid: %v", err)
	}
	host, port := cycleTestRemote(t, []map[string]any{{"guid": selfGuid}}, http.StatusOK)
	node := &model.Node{Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/", ApiToken: "test-token", Enable: false, AllowPrivateAddress: true, Guid: "b-guid"}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed disabled cycle: %v", err)
	}
	if err := database.GetDB().Model(node).Update("enable", false).Error; err != nil {
		t.Fatalf("disable seeded node: %v", err)
	}
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/setEnable/"+strconv.Itoa(node.Id), map[string]any{"enable": true})
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, "create a cycle") {
		t.Fatalf("re-enabling cyclic node was not rejected: %s", body)
	}
	var got model.Node
	if err := database.GetDB().First(&got, node.Id).Error; err != nil || got.Enable {
		t.Fatalf("rejected re-enable changed node state: enable=%v err=%v", got.Enable, err)
	}
}

func TestNodeEnableOutboundRejectsExistingCycle(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	selfGuid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil {
		t.Fatalf("GetPanelGuid: %v", err)
	}
	host, port := cycleTestRemote(t, []map[string]any{{"guid": selfGuid}}, http.StatusOK)
	node := &model.Node{Name: "B", Scheme: "http", Address: host, Port: port, BasePath: "/", ApiToken: "test-token", OutboundTag: "proxy", AllowPrivateAddress: true, Guid: "b-guid"}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed disabled outbound cycle: %v", err)
	}
	if err := database.GetDB().Model(node).Update("enable", false).Error; err != nil {
		t.Fatalf("disable seeded outbound node: %v", err)
	}
	body := postNodeCycleRequest(t, engine, "/panel/api/nodes/setEnable/"+strconv.Itoa(node.Id), map[string]any{"enable": true})
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, "create a cycle") {
		t.Fatalf("re-enabling cyclic outbound node was not rejected: %s", body)
	}
	var got model.Node
	if err := database.GetDB().First(&got, node.Id).Error; err != nil || got.Enable {
		t.Fatalf("rejected outbound re-enable changed node state: enable=%v err=%v", got.Enable, err)
	}
}
