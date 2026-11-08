package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/kbusergrant"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type KBUserGrantHandler struct {
	grants *kbusergrant.Service
}

func NewKBUserGrantHandler(grants *kbusergrant.Service) *KBUserGrantHandler {
	return &KBUserGrantHandler{grants: grants}
}

type kbUserGrantBody struct {
	Permission string `json:"permission"`
}

func (h *KBUserGrantHandler) Upsert(c *gin.Context) {
	var body kbUserGrantBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("invalid grant permission"))
		return
	}
	caller := types.CallerFromContext(c.Request.Context())
	var grant *kbusergrant.KBUserGrant
	var err error
	if source := c.Param("source"); source != "" {
		grant, err = h.grants.UpsertSource(c.Request.Context(), c.Param("id"), c.Param("user_id"), body.Permission, caller.UserID, caller.TenantID, source)
	} else {
		grant, err = h.grants.Upsert(c.Request.Context(), c.Param("id"), c.Param("user_id"), body.Permission, caller.UserID, caller.TenantID)
	}
	if err != nil {
		writeGrantError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": grant})
}

func (h *KBUserGrantHandler) List(c *gin.Context) {
	caller := types.CallerFromContext(c.Request.Context())
	grants, err := h.grants.ListByKnowledgeBase(c.Request.Context(), c.Param("id"), caller.TenantID)
	if err != nil {
		writeGrantError(c, err)
		return
	}
	if grants == nil {
		grants = []kbusergrant.KBUserGrant{}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"grants": grants,
			"total":  len(grants),
		},
	})
}

func (h *KBUserGrantHandler) Revoke(c *gin.Context) {
	caller := types.CallerFromContext(c.Request.Context())
	var err error
	if source := c.Param("source"); source != "" {
		err = h.grants.RevokeSource(c.Request.Context(), c.Param("id"), c.Param("user_id"), caller.TenantID, source)
	} else {
		err = h.grants.Revoke(c.Request.Context(), c.Param("id"), c.Param("user_id"), caller.TenantID)
	}
	if err != nil {
		writeGrantError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func writeGrantError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, kbusergrant.ErrInvalidPermission):
		_ = c.Error(apperrors.NewBadRequestError("invalid grant permission"))
	case errors.Is(err, kbusergrant.ErrKnowledgeBaseNotFound):
		_ = c.Error(apperrors.NewNotFoundError("knowledge base not found"))
	case errors.Is(err, kbusergrant.ErrRecipientNotFound):
		_ = c.Error(apperrors.NewNotFoundError("recipient not found"))
	case errors.Is(err, kbusergrant.ErrForbidden):
		_ = c.Error(apperrors.NewForbiddenError("caller workspace does not own the knowledge base"))
	default:
		_ = c.Error(apperrors.NewInternalServerError(strings.TrimSpace(err.Error())))
	}
}
