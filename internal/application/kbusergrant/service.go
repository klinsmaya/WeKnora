package kbusergrant

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	PermissionViewer KBUserGrantPermission = "viewer"
	PermissionEditor KBUserGrantPermission = "editor"
)

var (
	ErrInvalidPermission     = errors.New("invalid knowledge base grant permission")
	ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")
	ErrRecipientNotFound     = errors.New("recipient not found")
	ErrForbidden             = errors.New("knowledge base grant forbidden")
)

// KBUserGrantPermission is the stored permission. Only viewer and editor are valid.
type KBUserGrantPermission string

// KBUserGrant is one person's access to one knowledge base.
// The primary key is (KnowledgeBaseID, RecipientUserID).
type KBUserGrant struct {
	KnowledgeBaseID string                `json:"knowledge_base_id" gorm:"primaryKey;type:varchar(36)"`
	RecipientUserID string                `json:"recipient_user_id" gorm:"primaryKey;type:varchar(36)"`
	Permission      KBUserGrantPermission `json:"permission" gorm:"type:varchar(32);not null"`
	GrantedByUserID string                `json:"granted_by_user_id" gorm:"type:varchar(36);not null"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

func (KBUserGrant) TableName() string { return "kb_user_grants" }

// ParsePermission accepts viewer or editor and rejects every other value, including admin.
func ParsePermission(raw string) (KBUserGrantPermission, error) {
	switch KBUserGrantPermission(strings.TrimSpace(raw)) {
	case PermissionViewer, PermissionEditor:
		return KBUserGrantPermission(strings.TrimSpace(raw)), nil
	default:
		return "", ErrInvalidPermission
	}
}

type kbLookup interface {
	GetKnowledgeBaseByID(ctx context.Context, id string) (*types.KnowledgeBase, error)
	GetKnowledgeBaseByIDs(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error)
}

type userLookup interface {
	GetUserByID(ctx context.Context, id string) (*types.User, error)
}

// Service is the personal grant boundary. Management writes rows. Effective and
// CardsForUser are the read path shared with access checks.
type Service struct {
	db    *gorm.DB
	kbs   kbLookup
	users userLookup
}

func NewService(db *gorm.DB, kbs kbLookup, users userLookup) *Service {
	return &Service{db: db, kbs: kbs, users: users}
}

func (s *Service) Upsert(
	ctx context.Context,
	knowledgeBaseID, recipientUserID, rawPermission, grantedByUserID string,
	callerTenantID uint64,
) (*KBUserGrant, error) {
	permission, err := ParsePermission(rawPermission)
	if err != nil {
		return nil, err
	}
	recipientUserID = strings.TrimSpace(recipientUserID)
	if recipientUserID == "" {
		return nil, ErrInvalidPermission
	}
	if _, err := s.ownedKB(ctx, knowledgeBaseID, callerTenantID); err != nil {
		return nil, err
	}
	user, err := s.users.GetUserByID(ctx, recipientUserID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, ErrRecipientNotFound
		}
		return nil, err
	}
	if user == nil {
		return nil, ErrRecipientNotFound
	}
	now := time.Now().UTC()
	row := KBUserGrant{
		KnowledgeBaseID: strings.TrimSpace(knowledgeBaseID),
		RecipientUserID: recipientUserID,
		Permission:      permission,
		GrantedByUserID: grantedByUserID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "knowledge_base_id"}, {Name: "recipient_user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"permission":         permission,
			"granted_by_user_id": grantedByUserID,
			"updated_at":         now,
		}),
	}).Create(&row).Error
	if err != nil {
		return nil, err
	}
	return s.get(ctx, row.KnowledgeBaseID, row.RecipientUserID)
}

func (s *Service) ListByKnowledgeBase(ctx context.Context, knowledgeBaseID string, callerTenantID uint64) ([]KBUserGrant, error) {
	kb, err := s.ownedKB(ctx, knowledgeBaseID, callerTenantID)
	if err != nil {
		return nil, err
	}
	var rows []KBUserGrant
	err = s.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kb.ID).
		Order("recipient_user_id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []KBUserGrant{}
	}
	return rows, nil
}

func (s *Service) Revoke(ctx context.Context, knowledgeBaseID, recipientUserID string, callerTenantID uint64) error {
	recipientUserID = strings.TrimSpace(recipientUserID)
	if recipientUserID == "" {
		return ErrInvalidPermission
	}
	kb, err := s.ownedKB(ctx, knowledgeBaseID, callerTenantID)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND recipient_user_id = ?", kb.ID, recipientUserID).
		Delete(&KBUserGrant{}).Error
}

func (s *Service) Effective(ctx context.Context, knowledgeBaseID string, caller types.Caller) (types.OrgMemberRole, bool, error) {
	if strings.TrimSpace(caller.UserID) == "" || strings.TrimSpace(knowledgeBaseID) == "" {
		return "", false, nil
	}
	row, err := s.get(ctx, knowledgeBaseID, caller.UserID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return types.CapOrgRoleByTenantRole(types.OrgMemberRole(row.Permission), caller.Role), true, nil
}

func (s *Service) CardsForUser(ctx context.Context, userID string) ([]*types.SharedKnowledgeBaseInfo, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	var rows []KBUserGrant
	if err := s.db.WithContext(ctx).Where("recipient_user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []*types.SharedKnowledgeBaseInfo{}, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.KnowledgeBaseID)
	}
	kbs, err := s.kbs.GetKnowledgeBaseByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*types.KnowledgeBase, len(kbs))
	for _, kb := range kbs {
		if kb != nil {
			byID[kb.ID] = kb
		}
	}
	cards := make([]*types.SharedKnowledgeBaseInfo, 0, len(rows))
	for _, row := range rows {
		kb := byID[row.KnowledgeBaseID]
		if kb == nil {
			continue
		}
		cards = append(cards, &types.SharedKnowledgeBaseInfo{
			KnowledgeBase:  kb,
			Permission:     types.OrgMemberRole(row.Permission),
			SourceTenantID: kb.TenantID,
			SharedAt:       row.CreatedAt,
		})
	}
	return cards, nil
}

func (s *Service) ownedKB(ctx context.Context, knowledgeBaseID string, callerTenantID uint64) (*types.KnowledgeBase, error) {
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	if knowledgeBaseID == "" {
		return nil, ErrKnowledgeBaseNotFound
	}
	kb, err := s.kbs.GetKnowledgeBaseByID(ctx, knowledgeBaseID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeBaseNotFound) {
			return nil, ErrKnowledgeBaseNotFound
		}
		return nil, err
	}
	if kb == nil || kb.ID == "" {
		return nil, ErrKnowledgeBaseNotFound
	}
	if kb.TenantID != callerTenantID {
		return nil, ErrForbidden
	}
	return kb, nil
}

func (s *Service) get(ctx context.Context, knowledgeBaseID, recipientUserID string) (*KBUserGrant, error) {
	var row KBUserGrant
	err := s.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND recipient_user_id = ?", knowledgeBaseID, recipientUserID).
		Take(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}
