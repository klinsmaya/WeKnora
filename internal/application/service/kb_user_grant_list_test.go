package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type grantListShareRepo struct {
	interfaces.KBShareRepository
	shares []*types.KnowledgeBaseShare
}

func (r grantListShareRepo) ListSharedKBsForTenant(context.Context, uint64) ([]*types.KnowledgeBaseShare, error) {
	return r.shares, nil
}

type grantListOrgRepo struct {
	interfaces.OrganizationRepository
}

func (grantListOrgRepo) GetTenantMember(context.Context, string, uint64) (*types.OrganizationTenantMember, error) {
	return &types.OrganizationTenantMember{Role: types.OrgRoleAdmin}, nil
}

type grantListCards struct {
	user  string
	cards []*types.SharedKnowledgeBaseInfo
}

func (c grantListCards) CardsForUser(_ context.Context, userID string) ([]*types.SharedKnowledgeBaseInfo, error) {
	if userID != c.user {
		return nil, nil
	}
	return c.cards, nil
}

func TestListSharedKnowledgeBasesAppendsCallerGrant(t *testing.T) {
	personal := &types.KnowledgeBase{ID: "personal", TenantID: 9, Name: "Personal"}
	temporary := &types.KnowledgeBase{ID: "temp", TenantID: 9, IsTemporary: true}
	sameTenant := &types.KnowledgeBase{ID: "mine", TenantID: 2}
	cards := grantListCards{user: "user-a", cards: []*types.SharedKnowledgeBaseInfo{
		{KnowledgeBase: personal, Permission: types.OrgRoleViewer, SharedAt: time.Unix(10, 0).UTC()},
		{KnowledgeBase: temporary, Permission: types.OrgRoleEditor},
		{KnowledgeBase: sameTenant, Permission: types.OrgRoleEditor},
		{KnowledgeBase: nil, Permission: types.OrgRoleViewer},
	}}
	svc := NewKBShareService(grantListShareRepo{}, nil, nil, nil, nil, nil, cards)
	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 2, UserID: "user-a", Role: types.TenantRoleContributor,
	})
	list, err := svc.ListSharedKnowledgeBases(ctx, 2, types.TenantRoleContributor)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "personal", list[0].KnowledgeBase.ID)
	require.Empty(t, list[0].OrganizationID)
	require.Equal(t, uint64(9), list[0].SourceTenantID)
	require.Equal(t, types.OrgRoleViewer, list[0].Permission)

	emptyUser := types.WithCaller(context.Background(), types.Caller{TenantID: 2, Role: types.TenantRoleContributor})
	withoutUser, err := svc.ListSharedKnowledgeBases(emptyUser, 2, types.TenantRoleContributor)
	require.NoError(t, err)
	require.Empty(t, withoutUser)

	capped := NewKBShareService(grantListShareRepo{}, nil, nil, nil, nil, nil, grantListCards{
		user: "user-a",
		cards: []*types.SharedKnowledgeBaseInfo{{
			KnowledgeBase: personal, Permission: types.OrgRoleEditor,
		}},
	})
	viewer := types.WithCaller(context.Background(), types.Caller{
		TenantID: 2, UserID: "user-a", Role: types.TenantRoleViewer,
	})
	cappedList, err := capped.ListSharedKnowledgeBases(viewer, 2, types.TenantRoleViewer)
	require.NoError(t, err)
	require.Len(t, cappedList, 1)
	require.Equal(t, types.OrgRoleViewer, cappedList[0].Permission)
}

func TestListSharedKnowledgeBasesRaisesPermissionAndKeepsOrganization(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "shared", TenantID: 9, Name: "Shared"}
	repo := grantListShareRepo{shares: []*types.KnowledgeBaseShare{{
		ID: "share-1", KnowledgeBaseID: "shared", OrganizationID: "org-1", SourceTenantID: 9,
		Permission:    types.OrgRoleViewer,
		KnowledgeBase: kb, Organization: &types.Organization{ID: "org-1", Name: "Org"},
		CreatedAt: time.Unix(5, 0).UTC(),
	}}}
	cards := grantListCards{user: "user-a", cards: []*types.SharedKnowledgeBaseInfo{{
		KnowledgeBase: kb, Permission: types.OrgRoleEditor, SharedAt: time.Unix(8, 0).UTC(),
	}}}
	svc := NewKBShareService(repo, grantListOrgRepo{}, nil, nil, nil, nil, cards)
	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 2, UserID: "user-a", Role: types.TenantRoleContributor,
	})
	list, err := svc.ListSharedKnowledgeBases(ctx, 2, types.TenantRoleContributor)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, types.OrgRoleEditor, list[0].Permission)
	require.Equal(t, "org-1", list[0].OrganizationID)
	require.Equal(t, "share-1", list[0].ShareID)
	require.Equal(t, uint64(9), list[0].SourceTenantID)
}
