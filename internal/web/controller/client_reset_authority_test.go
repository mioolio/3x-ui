package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientResetRequiresMasterWhenGlobalUsageIsFresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	service.StartTrafficWriter()
	t.Cleanup(service.StopTrafficWriter)
	db := database.GetDB()
	const email = "managed@example.com"
	if err := db.Create(&model.ClientRecord{Email: email, SubID: "managed", Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: email, Enable: true, Up: 17, Down: 23}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientGlobalTraffic{MasterGuid: "parent", Email: email, Up: 719080279292, Down: 132410702250}).Error; err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{model.ApiScopeAdmin, model.ApiScopeNodeSync} {
		token := scope + "-token"
		if err := db.Create(&model.ApiToken{Name: scope, Token: crypto.HashTokenSHA256(token), Enabled: true, Scope: scope}).Error; err != nil {
			t.Fatal(err)
		}
	}

	engine := gin.New()
	api := engine.Group("/panel/api")
	a := &APIController{}
	api.Use(a.checkAPIAuth, a.enforceTokenScope)
	NewClientController(api.Group("/clients"))
	post := func(scope string) entity.Msg {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/panel/api/clients/resetTraffic/"+email, nil)
		req.Header.Set("Authorization", "Bearer "+scope+"-token")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s reset HTTP %d: %s", scope, w.Code, w.Body.String())
		}
		var msg entity.Msg
		if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
			t.Fatalf("decode %s response: %v", scope, err)
		}
		return msg
	}
	if msg := post(model.ApiScopeAdmin); msg.Success || !strings.Contains(msg.Msg, "主面板重置") {
		t.Fatalf("direct admin reset should be rejected clearly: %+v", msg)
	}
	var afterAdmin xray.ClientTraffic
	if err := db.Where("email = ?", email).First(&afterAdmin).Error; err != nil {
		t.Fatal(err)
	}
	if afterAdmin.Up != 17 || afterAdmin.Down != 23 {
		t.Fatalf("rejected reset mutated usage: %d/%d", afterAdmin.Up, afterAdmin.Down)
	}
	if msg := post(model.ApiScopeNodeSync); !msg.Success {
		t.Fatalf("upstream node-sync reset should pass: %+v", msg)
	}
	var afterMaster xray.ClientTraffic
	if err := db.Where("email = ?", email).First(&afterMaster).Error; err != nil {
		t.Fatal(err)
	}
	if afterMaster.Up != 0 || afterMaster.Down != 0 {
		t.Fatalf("upstream reset did not clear usage: %d/%d", afterMaster.Up, afterMaster.Down)
	}
}
