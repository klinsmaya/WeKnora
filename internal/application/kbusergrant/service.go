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

type KBUserGrantPermission string

type KBUserGrant struct {
	KnowledgeBaseID string                `json:"knowledge_base_id" gorm:"primaryKey;type:varchar(36)"`
	RecipientUserID string                `json:"recipient_user_id" gorm:"primaryKey;type:varchar(36)"`
	Source          string                `json:"source,omitempty" gorm:"->;-:migration"`
	Permission      KBUserGrantPermission `json:"permission" gorm:"type:varchar(32);not null"`
	GrantedByUserID string                `json:"granted_by_user_id" gorm:"type:varchar(36);not null"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

func (KBUserGrant) TableName() string { return "kb_user_grants" }

// Source grants coexist with direct grants. Revoking one source never removes
// a direct grant or another integration's grant for the same recipient.
type SourceGrant struct {
	KnowledgeBaseID string                `gorm:"primaryKey;type:varchar(36)"`
	RecipientUserID string                `gorm:"primaryKey;type:varchar(36)"`
	Source          string                `gorm:"primaryKey;type:varchar(96)"`
	Permission      KBUserGrantPermission `gorm:"type:varchar(32);not null"`
	GrantedByUserID string                `gorm:"type:varchar(36);not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (SourceGrant) TableName() string { return "kb_user_grant_sources" }

func ValidSource(source string) bool {
	if len(source) == 0 || len(source) > 96 {
		return false
	}
	for _, c := range source {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':') {
			return false
		}
	}
	return true
}

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
	return s.upsert(ctx, knowledgeBaseID, recipientUserID, rawPermission, grantedByUserID, callerTenantID, "")
}

func (s *Service) UpsertSource(ctx context.Context, knowledgeBaseID, recipientUserID, rawPermission, grantedByUserID string, callerTenantID uint64, source string) (*KBUserGrant, error) {
	if !ValidSource(source) {
		return nil, ErrInvalidPermission
	}
	return s.upsert(ctx, knowledgeBaseID, recipientUserID, rawPermission, grantedByUserID, callerTenantID, source)
}

func (s *Service) upsert(ctx context.Context, knowledgeBaseID, recipientUserID, rawPermission, grantedByUserID string, callerTenantID uint64, source string) (*KBUserGrant, error) {
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
	columns := []clause.Column{{Name: "knowledge_base_id"}, {Name: "recipient_user_id"}}
	target := s.db.WithContext(ctx).Model(&KBUserGrant{})
	if source != "" {
		columns = append(columns, clause.Column{Name: "source"})
		target = s.db.WithContext(ctx).Model(&SourceGrant{})
	}
	values := map[string]any{
		"knowledge_base_id": row.KnowledgeBaseID, "recipient_user_id": row.RecipientUserID,
		"permission": permission, "granted_by_user_id": grantedByUserID, "created_at": now, "updated_at": now,
	}
	if source != "" {
		values["source"] = source
	}
	err = target.Clauses(clause.OnConflict{
		Columns: columns,
		DoUpdates: clause.Assignments(map[string]any{
			"permission": permission, "granted_by_user_id": grantedByUserID, "updated_at": now,
		}),
	}).Create(values).Error
	if err != nil {
		return nil, err
	}
	if source == "" {
		err = s.db.WithContext(ctx).Where("knowledge_base_id = ? AND recipient_user_id = ?", row.KnowledgeBaseID, row.RecipientUserID).Take(&row).Error
	} else {
		err = s.db.WithContext(ctx).Table("kb_user_grant_sources").Where("knowledge_base_id = ? AND recipient_user_id = ? AND source = ?", row.KnowledgeBaseID, row.RecipientUserID, source).Take(&row).Error
	}
	return &row, err
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
	if err := s.allGrants(ctx).Where("recipient_user_id = ?", userID).Order("permission ASC").Find(&rows).Error; err != nil {
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
	seen := make(map[string]bool)
	for _, row := range rows {
		if seen[row.KnowledgeBaseID] {
			continue
		}
		seen[row.KnowledgeBaseID] = true
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
	err := s.allGrants(ctx).
		Where("knowledge_base_id = ? AND recipient_user_id = ?", knowledgeBaseID, recipientUserID).
		Order("permission ASC").
		Take(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Service) allGrants(ctx context.Context) *gorm.DB {
	direct := s.db.WithContext(ctx).Model(&KBUserGrant{}).Select("knowledge_base_id, recipient_user_id, permission, granted_by_user_id, created_at, updated_at, '' AS source")
	sources := s.db.WithContext(ctx).Model(&SourceGrant{}).Select("knowledge_base_id, recipient_user_id, permission, granted_by_user_id, created_at, updated_at, source")
	return s.db.WithContext(ctx).Table("(? UNION ALL ?) AS grants", direct, sources)
}

func (s *Service) RevokeSource(ctx context.Context, knowledgeBaseID, recipientUserID string, callerTenantID uint64, source string) error {
	if !ValidSource(source) || strings.TrimSpace(recipientUserID) == "" {
		return ErrInvalidPermission
	}
	kb, err := s.ownedKB(ctx, knowledgeBaseID, callerTenantID)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Where("knowledge_base_id = ? AND recipient_user_id = ? AND source = ?", kb.ID, recipientUserID, source).Delete(&SourceGrant{}).Error
}
