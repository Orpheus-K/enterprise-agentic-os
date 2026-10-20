package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/skills"
	"bytes"
	"context"
	"encoding/json"
	"io"
)

func (s *Store) WithRoutes(registry *skills.Registry, refs []skills.Ref) (*Store, error) {
	if registry == nil || len(refs) == 0 || len(refs) > 32 {
		return nil, ErrSkillDenied
	}
	names := map[string]bool{}
	for _, ref := range refs {
		if !registry.Contains(ref) || names[ref.Name] {
			return nil, ErrSkillDenied
		}
		names[ref.Name] = true
	}
	copy := *s
	copy.skills = registry
	copy.selectedSkill = skills.Ref{}
	copy.routes = append([]skills.Ref(nil), refs...)
	return &copy, nil
}

func (s *Store) RouteSummaries(ctx context.Context, p identity.Principal, refs []skills.Ref) ([]skills.Summary, error) {
	if s.skills == nil {
		return nil, ErrSkillDenied
	}
	auth, err := identitypg.NewAssetAuthorizer(s.identities, s.app)
	if err != nil {
		return nil, err
	}
	summaries, err := s.skills.Summaries(ctx, p, auth)
	if err != nil {
		return nil, err
	}
	allowed := map[skills.Ref]bool{}
	for _, ref := range refs {
		allowed[ref] = true
	}
	result := []skills.Summary{}
	for _, summary := range summaries {
		if allowed[summary.Ref] {
			result = append(result, summary)
		}
	}
	if len(result) == 0 {
		return nil, ErrSkillDenied
	}
	return result, nil
}

func (s *Store) BindRoute(ctx context.Context, t *Task, ref skills.Ref) error {
	if t.State != ModelRunning || t.Skill.Name != "" {
		return ErrInvalidTransition
	}
	found := false
	for _, r := range t.Routes {
		if r == ref {
			found = true
		}
	}
	if !found || s.skills == nil || !s.skills.Contains(ref) {
		return ErrSkillDenied
	}
	err := s.scoped(ctx, func(q database.Queries) error {
		if err := s.lockTask(ctx, q, t); err != nil {
			return err
		}
		if err := s.skillPermission(ctx, q, t.Principal.MemberID, ref); err != nil {
			return err
		}
		result, err := q.Exec(ctx, `UPDATE channel.tasks SET skill_name=$8,skill_revision=$9,state='pending',step_version=step_version+1,lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4 AND lease_owner=$5 AND lease_generation=$6 AND step_version=$7 AND state='model_running' AND skill_name IS NULL AND lease_until>clock_timestamp()`, s.tenant, s.app, s.revision, t.ID, s.owner, t.Generation, t.Version, ref.Name, ref.Revision)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return audit(ctx, q, s.tenant, t.ID, "pending", "skill_selected")
	})
	if err == nil {
		t.Skill = ref
		t.State = Pending
		t.Version++
	}
	return err
}

func (w *Worker) route(ctx context.Context, t *Task, finish func(State, string) error) error {
	skillStore, ok := w.Store.(SkillExecutionStore)
	if !ok {
		return ErrUnsupportedExecution
	}
	if err := w.authorize(ctx, t); err != nil {
		return authorizationFailure(err, "route_identity_denied", finish)
	}
	summaries, err := skillStore.RouteSummaries(ctx, t.Principal, t.Routes)
	if err != nil {
		return finish(Failed, "route_unavailable")
	}
	router, ok := w.Model.(SkillModel)
	if !ok {
		return finish(Failed, "route_model_unsupported")
	}
	raw, err := json.Marshal(summaries)
	if err != nil {
		return err
	}
	if err = w.Store.Transition(ctx, t, ModelRunning, "", ""); err != nil {
		return err
	}
	modelCtx, end := w.Telemetry.Start(ctx, "model.route")
	response, err := router.CompleteSkill(modelCtx, "Select exactly one matching skill from these authorized summaries. Return only JSON {\"name\":\"...\",\"revision\":\"...\"}; return null if no match. Do not execute the task.\n"+string(raw), t.Message.Text)
	end(err == nil)
	if err != nil {
		return finish(Unknown, "route_model_failed")
	}
	var ref skills.Ref
	decoder := json.NewDecoder(bytes.NewBufferString(response))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ref) != nil || decoder.Decode(new(any)) != io.EOF {
		return finish(Failed, "route_invalid")
	}
	found := false
	for _, summary := range summaries {
		if summary.Ref == ref {
			found = true
		}
	}
	if !found {
		return finish(Failed, "route_no_authorized_match")
	}
	if err = w.authorize(ctx, t); err != nil {
		return authorizationFailure(err, "route_identity_denied", finish)
	}
	err = skillStore.BindRoute(ctx, t, ref)
	if err == ErrSkillDenied {
		return finish(Failed, "route_revoked")
	}
	return err
}
