package router

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const knowledgeDownloadOriginal = "ORIGINAL-BYTES"

type downloadRouteKnowledge struct {
	interfaces.KnowledgeService
	knowledge *types.Knowledge
	opened    int
}

func (s *downloadRouteKnowledge) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	if s.knowledge != nil && s.knowledge.ID == id {
		return s.knowledge, nil
	}
	return nil, apprepo.ErrKnowledgeNotFound
}

func (s *downloadRouteKnowledge) GetKnowledgeFile(_ context.Context, id string) (io.ReadCloser, string, error) {
	if s.knowledge == nil || s.knowledge.ID != id {
		return nil, "", apprepo.ErrKnowledgeNotFound
	}
	s.opened++
	return io.NopCloser(strings.NewReader(knowledgeDownloadOriginal)), "original.txt", nil
}

func (s *downloadRouteKnowledge) GetKnowledgeBatch(
	_ context.Context, tenantID uint64, ids []string,
) ([]*types.Knowledge, error) {
	if s.knowledge == nil || s.knowledge.TenantID != tenantID {
		return nil, nil
	}
	var out []*types.Knowledge
	for _, id := range ids {
		if id == s.knowledge.ID {
			out = append(out, s.knowledge)
		}
	}
	return out, nil
}

type downloadRouteKB struct {
	interfaces.KnowledgeBaseService
	kbs map[string]*types.KnowledgeBase
}

func (s *downloadRouteKB) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	if kb, ok := s.kbs[id]; ok {
		return kb, nil
	}
	return nil, apprepo.ErrKnowledgeBaseNotFound
}

type downloadKBShareStub struct {
	interfaces.KBShareService
	permission types.OrgMemberRole
	shared     bool
}

func (s *downloadKBShareStub) CheckTenantKBPermission(
	_ context.Context,
	_ string,
	_ uint64,
	_ types.TenantRole,
) (types.OrgMemberRole, bool, error) {
	if s == nil || !s.shared {
		return "", false, nil
	}
	return s.permission, true, nil
}

func newKnowledgeDownloadRouteTestEngine(
	t *testing.T,
	role types.TenantRole,
	knowledge *types.Knowledge,
	kb *types.KnowledgeBase,
	share interfaces.KBShareService,
) (*gin.Engine, *downloadRouteKnowledge) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	enabled := true
	lookup := &downloadRouteKnowledge{knowledge: knowledge}
	guards := &rbacGuards{
		cfg:              &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}},
		knowledgeService: lookup,
		kbService:        &stubWikiKBLookup{kbs: map[string]*types.KnowledgeBase{kb.ID: kb}},
		kbShareService:   share,
	}
	h := handler.NewKnowledgeHandler(
		nil,
		lookup,
		&downloadRouteKB{kbs: map[string]*types.KnowledgeBase{kb.ID: kb}},
		share,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(1))
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
		c.Request = c.Request.WithContext(ctx)
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		c.Next()
	})
	RegisterKnowledgeRoutes(r.Group("/api/v1"), h, guards)
	return r, lookup
}

func serveKnowledgeRoute(engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	engine.ServeHTTP(rec, req)
	return rec
}

func TestKnowledgeDownloadAllowsTenantViewerOnOwnKB(t *testing.T) {
	engine, lookup := newKnowledgeDownloadRouteTestEngine(
		t,
		types.TenantRoleViewer,
		&types.Knowledge{ID: "knowledge-own", KnowledgeBaseID: "kb-own", TenantID: 1, FilePath: "stored/own"},
		&types.KnowledgeBase{ID: "kb-own", TenantID: 1},
		nil,
	)

	rec := serveKnowledgeRoute(engine, http.MethodGet, "/api/v1/knowledge/knowledge-own/download", "")

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, knowledgeDownloadOriginal, rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
	require.Equal(t, 1, lookup.opened)
}

func TestKnowledgeDownloadAllowsOrgSharedViewer(t *testing.T) {
	engine, lookup := newKnowledgeDownloadRouteTestEngine(
		t,
		types.TenantRoleContributor,
		&types.Knowledge{ID: "knowledge-shared", KnowledgeBaseID: "kb-shared", TenantID: 2, FilePath: "stored/shared"},
		&types.KnowledgeBase{ID: "kb-shared", TenantID: 2},
		&downloadKBShareStub{permission: types.OrgRoleViewer, shared: true},
	)

	rec := serveKnowledgeRoute(engine, http.MethodGet, "/api/v1/knowledge/knowledge-shared/download", "")

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, knowledgeDownloadOriginal, rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
	require.NotContains(t, rec.Body.String(), "presigned")
	require.Equal(t, 1, lookup.opened)
}

func TestKnowledgeDownloadRejectsWithoutEffectiveRead(t *testing.T) {
	engine, lookup := newKnowledgeDownloadRouteTestEngine(
		t,
		types.TenantRoleContributor,
		&types.Knowledge{ID: "knowledge-shared", KnowledgeBaseID: "kb-shared", TenantID: 2, FilePath: "stored/secret"},
		&types.KnowledgeBase{ID: "kb-shared", TenantID: 2},
		&downloadKBShareStub{permission: types.OrgRoleViewer, shared: false},
	)

	rec := serveKnowledgeRoute(engine, http.MethodGet, "/api/v1/knowledge/knowledge-shared/download", "")

	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	require.NotContains(t, rec.Body.String(), knowledgeDownloadOriginal)
	require.Zero(t, lookup.opened)
}

func TestKnowledgeDownloadDoesNotUsePreview(t *testing.T) {
	knowledge := &types.Knowledge{
		ID: "knowledge-shared", KnowledgeBaseID: "kb-shared", TenantID: 2, FilePath: "stored/shared",
	}
	kb := &types.KnowledgeBase{ID: "kb-shared", TenantID: 2}
	share := &downloadKBShareStub{permission: types.OrgRoleViewer, shared: true}
	engine, lookup := newKnowledgeDownloadRouteTestEngine(t, types.TenantRoleViewer, knowledge, kb, share)

	download := serveKnowledgeRoute(engine, http.MethodGet, "/api/v1/knowledge/knowledge-shared/download", "")
	require.Equal(t, http.StatusOK, download.Code, download.Body.String())
	require.Contains(t, download.Header().Get("Content-Disposition"), "attachment")
	require.Equal(t, "application/octet-stream", download.Header().Get("Content-Type"))

	preview := serveKnowledgeRoute(engine, http.MethodGet, "/api/v1/knowledge/knowledge-shared/preview", "")
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Contains(t, preview.Header().Get("Content-Disposition"), "inline")
	require.NotContains(t, preview.Header().Get("Content-Disposition"), "attachment")
	require.NotEqual(t, download.Header().Get("Content-Disposition"), preview.Header().Get("Content-Disposition"))

	deniedEngine, deniedLookup := newKnowledgeDownloadRouteTestEngine(
		t,
		types.TenantRoleViewer,
		knowledge,
		kb,
		&downloadKBShareStub{shared: false},
	)
	deniedPreview := serveKnowledgeRoute(deniedEngine, http.MethodGet, "/api/v1/knowledge/knowledge-shared/preview", "")
	deniedDownload := serveKnowledgeRoute(deniedEngine, http.MethodGet, "/api/v1/knowledge/knowledge-shared/download", "")
	require.Equal(t, http.StatusForbidden, deniedPreview.Code, deniedPreview.Body.String())
	require.Equal(t, http.StatusForbidden, deniedDownload.Code, deniedDownload.Body.String())
	require.NotContains(t, deniedPreview.Body.String(), knowledgeDownloadOriginal)
	require.NotContains(t, deniedDownload.Body.String(), knowledgeDownloadOriginal)
	require.Zero(t, deniedLookup.opened)
	require.Equal(t, 2, lookup.opened)
}

func TestKnowledgeDownloadUnknownIDIsNotFound(t *testing.T) {
	engine, lookup := newKnowledgeDownloadRouteTestEngine(
		t,
		types.TenantRoleViewer,
		&types.Knowledge{ID: "knowledge-own", KnowledgeBaseID: "kb-own", TenantID: 1, FilePath: "stored/own"},
		&types.KnowledgeBase{ID: "kb-own", TenantID: 1},
		nil,
	)

	rec := serveKnowledgeRoute(engine, http.MethodGet, "/api/v1/knowledge/missing/download", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Zero(t, lookup.opened)
}

func TestBatchKnowledgeDownloadAllowsTenantViewerOnOwnKB(t *testing.T) {
	engine, lookup := newKnowledgeDownloadRouteTestEngine(
		t,
		types.TenantRoleViewer,
		&types.Knowledge{ID: "knowledge-own", KnowledgeBaseID: "kb-own", TenantID: 1, FilePath: "stored/own"},
		&types.KnowledgeBase{ID: "kb-own", TenantID: 1},
		nil,
	)

	rec := serveKnowledgeRoute(
		engine,
		http.MethodPost,
		"/api/v1/knowledge-bases/kb-own/knowledge/batch-download",
		`{"ids":["knowledge-own"]}`,
	)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
	reader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	require.NoError(t, err)
	require.Len(t, reader.File, 1)
	file, err := reader.File[0].Open()
	require.NoError(t, err)
	defer file.Close()
	content, err := io.ReadAll(file)
	require.NoError(t, err)
	require.Equal(t, knowledgeDownloadOriginal, string(content))
	require.Equal(t, 1, lookup.opened)
}

func TestBatchKnowledgeDownloadAllowsOrgSharedViewer(t *testing.T) {
	engine, lookup := newKnowledgeDownloadRouteTestEngine(
		t,
		types.TenantRoleContributor,
		&types.Knowledge{ID: "knowledge-shared", KnowledgeBaseID: "kb-shared", TenantID: 2, FilePath: "stored/shared"},
		&types.KnowledgeBase{ID: "kb-shared", TenantID: 2},
		&downloadKBShareStub{permission: types.OrgRoleViewer, shared: true},
	)

	rec := serveKnowledgeRoute(
		engine,
		http.MethodPost,
		"/api/v1/knowledge-bases/kb-shared/knowledge/batch-download",
		`{"ids":["knowledge-shared"]}`,
	)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, 1, lookup.opened)
}

func TestBatchKnowledgeDownloadRejectsWithoutEffectiveRead(t *testing.T) {
	engine, lookup := newKnowledgeDownloadRouteTestEngine(
		t,
		types.TenantRoleContributor,
		&types.Knowledge{ID: "knowledge-shared", KnowledgeBaseID: "kb-shared", TenantID: 2, FilePath: "stored/secret"},
		&types.KnowledgeBase{ID: "kb-shared", TenantID: 2},
		&downloadKBShareStub{permission: types.OrgRoleViewer, shared: false},
	)

	rec := serveKnowledgeRoute(
		engine,
		http.MethodPost,
		"/api/v1/knowledge-bases/kb-shared/knowledge/batch-download",
		`{"ids":["knowledge-shared"]}`,
	)

	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	require.NotContains(t, rec.Body.String(), knowledgeDownloadOriginal)
	require.NotContains(t, rec.Header().Get("Content-Type"), "zip")
	require.Zero(t, lookup.opened)
}
