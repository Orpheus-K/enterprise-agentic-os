package conversation

import (
	"agentos.local/core/internal/database"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/skills"
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrSkillDenied = errors.New("task skill is unavailable or unauthorized")

func (s *Store) WithSkills(registry *skills.Registry, selected skills.Ref) (*Store, error) {
	if registry == nil || !registry.Contains(selected) {
		return nil, ErrSkillDenied
	}
	copy := *s
	copy.skills = registry
	copy.selectedSkill = selected
	copy.routes = nil
	return &copy, nil
}
func (s *Store) skillPermission(ctx context.Context, q database.Queries, member uuid.UUID, ref skills.Ref) error {
	if _, err := q.Exec(ctx, "SELECT set_config('agentos.member_id',$1,true)", member.String()); err != nil {
		return err
	}
	var allowed bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.asset_grants WHERE tenant_id=$1 AND member_id=$2 AND app_id=$3 AND kind='skill' AND name=$4 AND revision=$5 AND enabled)`, s.tenant, member, s.app, ref.Name, ref.Revision).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrSkillDenied
	}
	return nil
}
func (s *Store) checkTaskSkill(ctx context.Context, q database.Queries, id uuid.UUID) error {
	var member uuid.UUID
	var ref skills.Ref
	err := q.QueryRow(ctx, `SELECT member_id,coalesce(skill_name,''),coalesce(skill_revision,'') FROM channel.tasks WHERE tenant_id=$1 AND id=$2`, s.tenant, id).Scan(&member, &ref.Name, &ref.Revision)
	if err != nil {
		return err
	}
	if ref.Name == "" {
		return nil
	}
	return s.skillPermission(ctx, q, member, ref)
}
func (s *Store) LoadSkill(ctx context.Context, t *Task) (skills.Loaded, error) {
	if s.skills == nil {
		return skills.Loaded{}, ErrSkillDenied
	}
	if err := s.Authorize(ctx, t); err != nil {
		return skills.Loaded{}, err
	}
	auth, err := identitypg.NewAssetAuthorizer(s.identities, s.app)
	if err != nil {
		return skills.Loaded{}, err
	}
	loaded, err := s.skills.Load(ctx, t.Principal, t.Skill, auth)
	if errors.Is(err, skills.ErrUnavailable) {
		return skills.Loaded{}, ErrSkillDenied
	}
	return loaded, err
}
