package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fileOnlyUploadStub struct {
	interfaces.KnowledgeService
	sawSkip bool
	calls   int
}

func (s *fileOnlyUploadStub) CreateKnowledgeFromFile(
	ctx context.Context,
	kbID string,
	file *multipart.FileHeader,
	metadata map[string]string,
	enableMultimodel *bool,
	customFileName string,
	tagIDs []string,
	channel string,
	processOverrides *types.KnowledgeProcessOverrides,
) (*types.Knowledge, error) {
	s.calls++
	s.sawSkip = service.SkipDocumentProcessFromContext(ctx)
	return &types.Knowledge{
		ID:              "knowledge-1",
		KnowledgeBaseID: kbID,
		TenantID:        7,
		FileName:        file.Filename,
		ParseStatus:     types.ParseStatusStored,
		FilePath:        "stored/knowledge-1",
	}, nil
}

type fileOnlyUploadResult struct {
	rec  *httptest.ResponseRecorder
	stub *fileOnlyUploadStub
}

func postKnowledgeFileResult(t *testing.T, fields map[string]string, query string) fileOnlyUploadResult {
	t.Helper()
	gin.SetMode(gin.TestMode)
	stub := &fileOnlyUploadStub{}
	router := gin.New()
	router.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		ctx := types.WithCaller(c.Request.Context(), types.Caller{
			TenantID: 7, UserID: "user-1", Role: types.TenantRoleContributor,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	h := &KnowledgeHandler{
		kgService: stub,
		kbService: &downloadKBStub{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}},
	}
	router.POST("/knowledge-bases/:id/knowledge/file", h.CreateKnowledgeFromFile)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "notes.txt")
	require.NoError(t, err)
	_, err = part.Write([]byte("hello"))
	require.NoError(t, err)
	for key, value := range fields {
		require.NoError(t, writer.WriteField(key, value))
	}
	require.NoError(t, writer.Close())

	target := "/knowledge-bases/kb-1/knowledge/file"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return fileOnlyUploadResult{rec: rec, stub: stub}
}

func TestCreateKnowledgeFromFileFlagSkipsDocumentProcess(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]string
		query  string
		skip   bool
	}{
		{name: "default still processes", skip: false},
		{name: "file_only form", fields: map[string]string{"file_only": "true"}, skip: true},
		{name: "skip_process form", fields: map[string]string{"skip_process": "1"}, skip: true},
		{name: "file-only query", query: "file-only=yes", skip: true},
		{name: "explicit false", fields: map[string]string{"file_only": "false"}, skip: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := postKnowledgeFileResult(t, tc.fields, tc.query)
			require.Equal(t, http.StatusOK, got.rec.Code, got.rec.Body.String())
			require.Equal(t, 1, got.stub.calls)
			require.Equal(t, tc.skip, got.stub.sawSkip)
			require.NotContains(t, got.rec.Body.String(), "/preview")
		})
	}
}
