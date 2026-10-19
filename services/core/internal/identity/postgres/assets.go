package postgres

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/skills"
	"context"
	"errors"
	"strings"
)

type AssetAuthorizer struct {
	repo *Repository
	app  string
}

func NewAssetAuthorizer(repo *Repository, app string) (*AssetAuthorizer, error) {
	if repo == nil || app == "" || strings.TrimSpace(app) != app {
		return nil, errors.New("asset authorizer requires repository and application")
	}
	return &AssetAuthorizer{repo, app}, nil
}
func (a *AssetAuthorizer) Skill(ctx context.Context, p identity.Principal, ref skills.Ref) (bool, error) {
	return a.allowed(ctx, p, "skill", ref.Name, ref.Revision)
}
func (a *AssetAuthorizer) Tool(ctx context.Context, p identity.Principal, ref skills.ToolRef) (bool, error) {
	return a.allowed(ctx, p, "tool", ref.Name, ref.Revision)
}
func (a *AssetAuthorizer) allowed(ctx context.Context, p identity.Principal, kind, name, revision string) (bool, error) {
	var allowed bool
	err := a.repo.Within(ctx, p, func(q database.Queries) error {
		return q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.asset_grants WHERE tenant_id=$1 AND member_id=$2 AND app_id=$3 AND kind=$4 AND name=$5 AND revision=$6 AND enabled)`, p.TenantID, p.MemberID, a.app, kind, name, revision).Scan(&allowed)
	})
	return allowed, err
}
