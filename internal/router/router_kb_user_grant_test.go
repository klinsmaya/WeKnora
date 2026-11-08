package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/kbusergrant"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestKBUserGrantHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "grants.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS kb_user_grants (
    knowledge_base_id VARCHAR(36) NOT NULL,
    recipient_user_id VARCHAR(36) NOT NULL,
    permission VARCHAR(32) NOT NULL,
    granted_by_user_id VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (knowledge_base_id, recipient_user_id),
    CHECK (permission IN ('viewer', 'editor'))
)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS kb_shares (
    id TEXT PRIMARY KEY,
    knowledge_base_id TEXT NOT NULL,
    organization_id TEXT NOT NULL
)`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO kb_shares (id, knowledge_base_id, organization_id) VALUES ('share-1', 'kb-1', 'org-1')`,
	).Error)
	kbs := httpGrantKBs{rows: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 1},
		"kb-2": {ID: "kb-2", TenantID: 9},
	}}
	users := httpGrantUsers{ids: map[string]bool{"user-a": true, "user-b": true}}
	h := handler.NewKBUserGrantHandler(kbusergrant.NewService(db, kbs, users))
	enabled := true
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		caller := types.Caller{TenantID: 1, UserID: "owner", Role: types.TenantRoleAdmin}
		c.Request = c.Request.WithContext(types.WithCaller(c.Request.Context(), caller))
		c.Next()
	})
	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}}
	RegisterKBUserGrantRoutes(engine.Group("/api/v1"), h, g)

	put := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(rec, req)
		return rec
	}

	first := put("/api/v1/knowledge-bases/kb-1/grants/user-a", `{"permission":"viewer"}`)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var created struct {
		Success bool                    `json:"success"`
		Data    kbusergrant.KBUserGrant `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &created))
	require.True(t, created.Success)
	require.Equal(t, kbusergrant.PermissionViewer, created.Data.Permission)
	require.Equal(t, "user-a", created.Data.RecipientUserID)
	require.Equal(t, "owner", created.Data.GrantedByUserID)

	again := put("/api/v1/knowledge-bases/kb-1/grants/user-a", `{"permission":"editor"}`)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	require.NoError(t, json.Unmarshal(again.Body.Bytes(), &created))
	require.Equal(t, kbusergrant.PermissionEditor, created.Data.Permission)
	var n int64
	require.NoError(t, db.Table("kb_user_grants").Count(&n).Error)
	require.Equal(t, int64(1), n)

	require.Equal(t, http.StatusOK, put("/api/v1/knowledge-bases/kb-1/grants/user-b", `{"permission":"viewer"}`).Code)
	admin := put("/api/v1/knowledge-bases/kb-1/grants/user-b", `{"permission":"admin"}`)
	require.Equal(t, http.StatusBadRequest, admin.Code, admin.Body.String())
	emptyPerm := put("/api/v1/knowledge-bases/kb-1/grants/user-b", `{"permission":""}`)
	require.Equal(t, http.StatusBadRequest, emptyPerm.Code, emptyPerm.Body.String())
	emptyUser := put("/api/v1/knowledge-bases/kb-1/grants/%20", `{"permission":"viewer"}`)
	require.Equal(t, http.StatusBadRequest, emptyUser.Code, emptyUser.Body.String())

	missingKB := put("/api/v1/knowledge-bases/missing/grants/user-a", `{"permission":"viewer"}`)
	require.Equal(t, http.StatusNotFound, missingKB.Code, missingKB.Body.String())
	missingUser := put("/api/v1/knowledge-bases/kb-1/grants/nobody", `{"permission":"viewer"}`)
	require.Equal(t, http.StatusNotFound, missingUser.Code, missingUser.Body.String())
	foreign := put("/api/v1/knowledge-bases/kb-2/grants/user-a", `{"permission":"viewer"}`)
	require.Equal(t, http.StatusForbidden, foreign.Code, foreign.Body.String())

	listRec := httptest.NewRecorder()
	engine.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases/kb-1/grants", nil))
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listed struct {
		Success bool `json:"success"`
		Data    struct {
			Grants []kbusergrant.KBUserGrant `json:"grants"`
			Total  int                       `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listed))
	require.True(t, listed.Success)
	require.Equal(t, 2, listed.Data.Total)
	require.Len(t, listed.Data.Grants, 2)
	require.Equal(t, "user-a", listed.Data.Grants[0].RecipientUserID)
	require.Equal(t, kbusergrant.PermissionEditor, listed.Data.Grants[0].Permission)
	require.Contains(t, listRec.Body.String(), `"grants":[`)

	var shares int64
	require.NoError(t, db.Table("kb_shares").Count(&shares).Error)
	require.Equal(t, int64(1), shares)

	del := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		engine.ServeHTTP(rec, req)
		return rec
	}
	firstDelete := del("/api/v1/knowledge-bases/kb-1/grants/user-a")
	require.Equal(t, http.StatusOK, firstDelete.Code, firstDelete.Body.String())
	require.Contains(t, firstDelete.Body.String(), `"success":true`)
	secondDelete := del("/api/v1/knowledge-bases/kb-1/grants/user-a")
	require.Equal(t, http.StatusOK, secondDelete.Code, secondDelete.Body.String())
	require.NoError(t, db.Table("kb_user_grants").Count(&n).Error)
	require.Equal(t, int64(1), n)
	var remaining kbusergrant.KBUserGrant
	require.NoError(t, db.Where("recipient_user_id = ?", "user-b").Take(&remaining).Error)
	require.Equal(t, kbusergrant.PermissionViewer, remaining.Permission)
	require.NoError(t, db.Table("kb_shares").Count(&shares).Error)
	require.Equal(t, int64(1), shares)

	emptyEngine := gin.New()
	emptyEngine.Use(middleware.ErrorHandler())
	emptyEngine.Use(func(c *gin.Context) {
		caller := types.Caller{TenantID: 1, UserID: "owner", Role: types.TenantRoleAdmin}
		c.Request = c.Request.WithContext(types.WithCaller(c.Request.Context(), caller))
		c.Next()
	})
	RegisterKBUserGrantRoutes(emptyEngine.Group("/api/v1"), handler.NewKBUserGrantHandler(kbusergrant.NewService(db, httpGrantKBs{
		rows: map[string]*types.KnowledgeBase{"kb-empty": {ID: "kb-empty", TenantID: 1}},
	}, users)), g)
	emptyRec := httptest.NewRecorder()
	emptyEngine.ServeHTTP(emptyRec, httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases/kb-empty/grants", nil))
	require.Equal(t, http.StatusOK, emptyRec.Code, emptyRec.Body.String())
	require.Contains(t, emptyRec.Body.String(), `"total":0`)
	require.Contains(t, emptyRec.Body.String(), `"grants":[]`)
}

func TestKBUserGrantRoutesRequireFullAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	RegisterKBUserGrantRoutes(gin.New().Group("/api/v1"), &handler.KBUserGrantHandler{}, g)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/api/v1/knowledge-bases/:id/grants/:user_id"},
		{http.MethodGet, "/api/v1/knowledge-bases/:id/grants"},
		{http.MethodDelete, "/api/v1/knowledge-bases/:id/grants/:user_id"},
	} {
		policy := mustLookupAPIKeyPolicy(t, g, tc.method, tc.path)
		require.True(t, policy.RequireFullAccess)
		require.Empty(t, policy.Capabilities)
	}
}

type httpGrantKBs struct {
	rows map[string]*types.KnowledgeBase
}

func (m httpGrantKBs) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	kb, ok := m.rows[id]
	if !ok || kb == nil {
		return nil, repository.ErrKnowledgeBaseNotFound
	}
	return kb, nil
}

func (m httpGrantKBs) GetKnowledgeBaseByIDs(_ context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	out := make([]*types.KnowledgeBase, 0, len(ids))
	for _, id := range ids {
		if kb := m.rows[id]; kb != nil {
			out = append(out, kb)
		}
	}
	return out, nil
}

type httpGrantUsers struct {
	ids map[string]bool
}

func (m httpGrantUsers) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if !m.ids[id] {
		return nil, repository.ErrUserNotFound
	}
	return &types.User{ID: id}, nil
}
