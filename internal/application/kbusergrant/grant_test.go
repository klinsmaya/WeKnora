package kbusergrant_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/kbusergrant"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const grantTableSQL = `
CREATE TABLE IF NOT EXISTS kb_user_grants (
    knowledge_base_id VARCHAR(36) NOT NULL,
    recipient_user_id VARCHAR(36) NOT NULL,
    permission VARCHAR(32) NOT NULL,
    granted_by_user_id VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (knowledge_base_id, recipient_user_id),
    CHECK (permission IN ('viewer', 'editor'))
);
CREATE INDEX IF NOT EXISTS idx_kb_user_grants_recipient_user_id ON kb_user_grants(recipient_user_id);
`

const shareTableSQL = `
CREATE TABLE IF NOT EXISTS kb_shares (
    id TEXT PRIMARY KEY,
    knowledge_base_id TEXT NOT NULL,
    organization_id TEXT NOT NULL,
    shared_by_user_id TEXT NOT NULL DEFAULT '',
    source_tenant_id INTEGER NOT NULL DEFAULT 0,
    permission TEXT NOT NULL DEFAULT 'viewer',
    created_at DATETIME,
    updated_at DATETIME,
    deleted_at DATETIME
);
`

type memoryKBs struct {
	rows map[string]*types.KnowledgeBase
}

func (m memoryKBs) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	kb, ok := m.rows[id]
	if !ok || kb == nil {
		return nil, repository.ErrKnowledgeBaseNotFound
	}
	return kb, nil
}

func (m memoryKBs) GetKnowledgeBaseByIDs(_ context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	out := make([]*types.KnowledgeBase, 0, len(ids))
	for _, id := range ids {
		if kb := m.rows[id]; kb != nil {
			out = append(out, kb)
		}
	}
	return out, nil
}

type memoryUsers struct {
	ids map[string]bool
}

func (m memoryUsers) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if !m.ids[id] {
		return nil, repository.ErrUserNotFound
	}
	return &types.User{ID: id}, nil
}

type grantFixture struct {
	db  *gorm.DB
	svc *kbusergrant.Service
}

func newGrantFixture(t *testing.T) *grantFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "grants.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(grantTableSQL).Error)
	require.NoError(t, db.Exec(shareTableSQL).Error)
	kbs := memoryKBs{rows: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 1, Name: "Owned"},
		"kb-2": {ID: "kb-2", TenantID: 1, Name: "Other"},
	}}
	users := memoryUsers{ids: map[string]bool{
		"user-a": true, "user-b": true, "user-c": true, "owner": true, "org-user": true,
	}}
	return &grantFixture{db: db, svc: kbusergrant.NewService(db, kbs, users)}
}

func (f *grantFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Table(table).Count(&n).Error)
	return int(n)
}

type storedGrant struct {
	RecipientUserID string
	Permission      string
	CreatedAt       time.Time
}

func (f *grantFixture) rows(t *testing.T, kbID string) []storedGrant {
	t.Helper()
	var rows []storedGrant
	require.NoError(t, f.db.Raw(
		`SELECT recipient_user_id, permission, created_at FROM kb_user_grants WHERE knowledge_base_id = ? ORDER BY recipient_user_id`,
		kbID,
	).Scan(&rows).Error)
	return rows
}

type tenantShareLookup struct {
	byTenant map[uint64]types.OrgMemberRole
}

func (s tenantShareLookup) CheckTenantKBPermission(
	_ context.Context, _ string, tenant uint64, _ types.TenantRole,
) (types.OrgMemberRole, bool, error) {
	role, ok := s.byTenant[tenant]
	return role, ok, nil
}

func TestGrantCreateOneViewerRowPerUser(t *testing.T) {
	fx := newGrantFixture(t)
	require.NoError(t, fx.db.Exec(
		`INSERT INTO kb_shares (id, knowledge_base_id, organization_id) VALUES (?, ?, ?)`,
		"share-1", "kb-1", "org-1",
	).Error)
	ctx := context.Background()
	sharesBefore := fx.count(t, "kb_shares")

	first, err := fx.svc.Upsert(ctx, "kb-1", "user-a", string(kbusergrant.PermissionViewer), "owner", 1)
	require.NoError(t, err)
	require.Equal(t, "kb-1", first.KnowledgeBaseID)
	require.Equal(t, "user-a", first.RecipientUserID)
	require.Equal(t, kbusergrant.PermissionViewer, first.Permission)

	second, err := fx.svc.Upsert(ctx, "kb-1", "user-b", string(kbusergrant.PermissionViewer), "owner", 1)
	require.NoError(t, err)
	require.Equal(t, "user-b", second.RecipientUserID)

	_, err = fx.svc.Upsert(ctx, "kb-1", "user-c", "admin", "owner", 1)
	require.ErrorIs(t, err, kbusergrant.ErrInvalidPermission)
	_, err = fx.svc.Upsert(ctx, "kb-1", "user-c", "", "owner", 1)
	require.ErrorIs(t, err, kbusergrant.ErrInvalidPermission)
	_, err = fx.svc.Upsert(ctx, "kb-1", " ", string(kbusergrant.PermissionViewer), "owner", 1)
	require.ErrorIs(t, err, kbusergrant.ErrInvalidPermission)

	require.Equal(t, sharesBefore, fx.count(t, "kb_shares"))
	rows := fx.rows(t, "kb-1")
	require.Equal(t, []storedGrant{
		{RecipientUserID: "user-a", Permission: "viewer", CreatedAt: rows[0].CreatedAt},
		{RecipientUserID: "user-b", Permission: "viewer", CreatedAt: rows[1].CreatedAt},
	}, rows)

	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 1}
	caller := types.Caller{TenantID: 2, UserID: "user-a", Role: types.TenantRoleContributor}
	resolved, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleViewer, nil, nil, fx.svc)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleViewer, resolved.Permission)
	require.Equal(t, kb.TenantID, resolved.EffectiveTenantID)
	allowed, err := access.NewKBPermissions(
		types.WithCaller(ctx, caller), nil, fx.svc,
	).Check(kb.ID, kb.TenantID, types.OrgRoleViewer)
	require.NoError(t, err)
	require.True(t, allowed)

	otherKB := &types.KnowledgeBase{ID: "kb-2", TenantID: 1}
	_, err = access.ResolveKB(ctx, access.KBRequest{Caller: caller}, otherKB, types.OrgRoleViewer, nil, nil, fx.svc)
	require.ErrorIs(t, err, access.ErrForbidden)
	stranger := types.Caller{TenantID: 2, UserID: "user-c", Role: types.TenantRoleContributor}
	_, err = access.ResolveKB(ctx, access.KBRequest{Caller: stranger}, kb, types.OrgRoleViewer, nil, nil, fx.svc)
	require.ErrorIs(t, err, access.ErrForbidden)
	strangerAllowed, err := access.NewKBPermissions(
		types.WithCaller(ctx, stranger), nil, fx.svc,
	).Check(kb.ID, kb.TenantID, types.OrgRoleViewer)
	require.NoError(t, err)
	require.False(t, strangerAllowed)
}

func TestGrantUpsertKeepsCreatedAtAndOtherUser(t *testing.T) {
	fx := newGrantFixture(t)
	ctx := context.Background()
	_, err := fx.svc.Upsert(ctx, "kb-1", "user-b", string(kbusergrant.PermissionViewer), "owner", 1)
	require.NoError(t, err)
	first, err := fx.svc.Upsert(ctx, "kb-1", "user-a", string(kbusergrant.PermissionViewer), "owner", 1)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	editor, err := fx.svc.Upsert(ctx, "kb-1", "user-a", string(kbusergrant.PermissionEditor), "owner-2", 1)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	viewer, err := fx.svc.Upsert(ctx, "kb-1", "user-a", string(kbusergrant.PermissionViewer), "owner", 1)
	require.NoError(t, err)

	require.True(t, first.CreatedAt.Equal(viewer.CreatedAt))
	require.Equal(t, kbusergrant.PermissionViewer, viewer.Permission)
	require.Equal(t, kbusergrant.PermissionEditor, editor.Permission)
	rows := fx.rows(t, "kb-1")
	require.Len(t, rows, 2)
	require.Equal(t, "user-a", rows[0].RecipientUserID)
	require.Equal(t, "viewer", rows[0].Permission)
	require.True(t, first.CreatedAt.Equal(rows[0].CreatedAt))
	require.Equal(t, "user-b", rows[1].RecipientUserID)
	require.Equal(t, "viewer", rows[1].Permission)
}

func TestGrantRevokeTwiceKeepsShareAndOtherGrant(t *testing.T) {
	fx := newGrantFixture(t)
	ctx := context.Background()
	require.NoError(t, fx.db.Exec(
		`INSERT INTO kb_shares (id, knowledge_base_id, organization_id, permission) VALUES (?, ?, ?, ?)`,
		"share-1", "kb-1", "org-1", "viewer",
	).Error)
	_, err := fx.svc.Upsert(ctx, "kb-1", "user-a", string(kbusergrant.PermissionViewer), "owner", 1)
	require.NoError(t, err)
	_, err = fx.svc.Upsert(ctx, "kb-1", "user-b", string(kbusergrant.PermissionEditor), "owner", 1)
	require.NoError(t, err)

	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 1}
	shares := tenantShareLookup{byTenant: map[uint64]types.OrgMemberRole{9: types.OrgRoleViewer}}
	grantee := types.Caller{TenantID: 2, UserID: "user-a", Role: types.TenantRoleContributor}
	resolved, err := access.ResolveKB(ctx, access.KBRequest{Caller: grantee}, kb, types.OrgRoleViewer, shares, nil, fx.svc)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleViewer, resolved.Permission)
	allowed, err := access.NewKBPermissions(types.WithCaller(ctx, grantee), shares, fx.svc).Check(kb.ID, kb.TenantID, types.OrgRoleViewer)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, fx.svc.Revoke(ctx, "kb-1", "user-a", 1))
	require.NoError(t, fx.svc.Revoke(ctx, "kb-1", "user-a", 1))

	require.Equal(t, 1, fx.count(t, "kb_shares"))
	rows := fx.rows(t, "kb-1")
	require.Equal(t, []storedGrant{
		{RecipientUserID: "user-b", Permission: "editor", CreatedAt: rows[0].CreatedAt},
	}, rows)

	_, err = access.ResolveKB(ctx, access.KBRequest{Caller: grantee}, kb, types.OrgRoleViewer, shares, nil, fx.svc)
	require.ErrorIs(t, err, access.ErrForbidden)
	allowed, err = access.NewKBPermissions(types.WithCaller(ctx, grantee), shares, fx.svc).Check(kb.ID, kb.TenantID, types.OrgRoleViewer)
	require.NoError(t, err)
	require.False(t, allowed)

	member := types.Caller{TenantID: 9, UserID: "org-user", Role: types.TenantRoleContributor}
	resolved, err = access.ResolveKB(ctx, access.KBRequest{Caller: member}, kb, types.OrgRoleViewer, shares, nil, fx.svc)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleViewer, resolved.Permission)
	require.Equal(t, uint64(1), resolved.EffectiveTenantID)

	owner := types.Caller{TenantID: 1, UserID: "owner", Role: types.TenantRoleViewer}
	resolved, err = access.ResolveKB(ctx, access.KBRequest{Caller: owner}, kb, types.OrgRoleViewer, shares, nil, fx.svc)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleAdmin, resolved.Permission)
}

func TestGrantViewerWorkspaceCapsEditorGrant(t *testing.T) {
	fx := newGrantFixture(t)
	ctx := context.Background()
	_, err := fx.svc.Upsert(ctx, "kb-1", "user-a", string(kbusergrant.PermissionEditor), "owner", 1)
	require.NoError(t, err)
	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 1}
	caller := types.Caller{TenantID: 4, UserID: "user-a", Role: types.TenantRoleViewer}
	resolved, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleViewer, nil, nil, fx.svc)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleViewer, resolved.Permission)
	_, err = access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleEditor, nil, nil, fx.svc)
	require.ErrorIs(t, err, access.ErrForbidden)
	contributor := caller
	contributor.Role = types.TenantRoleContributor
	resolved, err = access.ResolveKB(ctx, access.KBRequest{Caller: contributor}, kb, types.OrgRoleEditor, nil, nil, fx.svc)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleEditor, resolved.Permission)
}
