package toolgateway

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/skills"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"time"
)

type Gateway struct {
	db          *database.DB
	identities  *identitypg.Repository
	auth        *identitypg.AssetAuthorizer
	tenant      uuid.UUID
	app         string
	registry    *skills.Registry
	tools       map[skills.ToolRef]compiledTool
	credentials *Credentials
}

func New(db *database.DB, tenant uuid.UUID, app string, registry *skills.Registry, specs []Spec, credentials *Credentials) (*Gateway, error) {
	if db == nil || tenant == uuid.Nil || app == "" || registry == nil || credentials == nil || len(specs) > 128 {
		return nil, ErrInvalid
	}
	identities := identitypg.New(db)
	auth, err := identitypg.NewAssetAuthorizer(identities, app)
	if err != nil {
		return nil, err
	}
	tools := map[skills.ToolRef]compiledTool{}
	for _, spec := range specs {
		tool, err := compile(spec)
		if err != nil {
			return nil, err
		}
		if _, ok := tools[tool.ref]; ok {
			return nil, ErrInvalid
		}
		tools[tool.ref] = tool
	}
	return &Gateway{db, identities, auth, tenant, app, registry, tools, credentials}, nil
}

// Definitions exposes only the current skill's approved tools, without destinations or credentials.
func (g *Gateway) Definitions(ctx context.Context, actor identity.ExternalIdentity, skill skills.Ref) ([]Definition, error) {
	if actor.TenantID != g.tenant {
		return nil, ErrDenied
	}
	p, err := g.identities.Resolve(ctx, actor)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) || errors.Is(err, identity.ErrInvalidIdentity) {
			return nil, ErrDenied
		}
		return nil, ErrPolicyUnavailable
	}
	loaded, err := g.registry.Load(ctx, p, skill, g.auth)
	if err != nil {
		if errors.Is(err, skills.ErrUnavailable) || errors.Is(err, identity.ErrUnauthenticated) {
			return nil, ErrDenied
		}
		return nil, ErrPolicyUnavailable
	}
	definitions := make([]Definition, 0, len(loaded.Manifest.Tools))
	for _, ref := range loaded.Manifest.Tools {
		tool, ok := g.tools[ref]
		if !ok {
			return nil, ErrDenied
		}
		if tool.spec.Risk != "read" {
			return nil, ErrWriteRestricted
		}
		definitions = append(definitions, Definition{Ref: ref, Description: tool.spec.Description, InputSchema: append(json.RawMessage(nil), tool.spec.InputSchema...)})
	}
	return definitions, nil
}
func (g *Gateway) authorize(ctx context.Context, actor identity.ExternalIdentity, skill skills.Ref, ref skills.ToolRef) (identity.Principal, compiledTool, error) {
	if actor.TenantID != g.tenant {
		return identity.Principal{}, compiledTool{}, ErrDenied
	}
	p, err := g.identities.Resolve(ctx, actor)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) || errors.Is(err, identity.ErrInvalidIdentity) {
			return p, compiledTool{}, ErrDenied
		}
		return p, compiledTool{}, ErrPolicyUnavailable
	}
	loaded, err := g.registry.Load(ctx, p, skill, g.auth)
	if err != nil {
		if errors.Is(err, skills.ErrUnavailable) || errors.Is(err, identity.ErrUnauthenticated) {
			return p, compiledTool{}, ErrDenied
		}
		return p, compiledTool{}, ErrPolicyUnavailable
	}
	for _, allowed := range loaded.Manifest.Tools {
		if allowed == ref {
			tool, ok := g.tools[ref]
			if ok {
				return p, tool, nil
			}
		}
	}
	return p, compiledTool{}, ErrDenied
}

// Call receives a verified channel identity and a stable runtime operation ID, never a model-selected URL or token.
func (g *Gateway) Call(ctx context.Context, actor identity.ExternalIdentity, operation uuid.UUID, skill skills.Ref, ref skills.ToolRef, args json.RawMessage) (json.RawMessage, error) {
	if operation == uuid.Nil || len(args) == 0 || len(args) > 16384 {
		return nil, ErrInvalid
	}
	p, tool, err := g.authorize(ctx, actor, skill, ref)
	if err != nil {
		return nil, err
	}
	if tool.spec.Risk != "read" {
		return nil, ErrWriteRestricted
	}
	var value any
	if json.Unmarshal(args, &value) != nil || tool.input.Validate(value) != nil {
		return nil, ErrInvalid
	}
	token, err := g.credentials.token(ctx, p, tool.spec.Endpoint)
	if err != nil {
		return nil, err
	}
	if err = g.start(ctx, p, operation, skill, ref, args); err != nil {
		return nil, err
	}
	raw, callErr := invoke(ctx, tool, value, token, func() error {
		current, _, err := g.authorize(ctx, actor, skill, ref)
		if err != nil {
			return err
		}
		if current != p {
			return ErrDenied
		}
		return nil
	})
	state := "succeeded"
	if callErr == nil {
		current, _, err := g.authorize(ctx, actor, skill, ref)
		if err != nil {
			callErr = err
		} else if current != p {
			callErr = ErrDenied
		}
	}
	if callErr != nil {
		state = "unknown"
		if errors.Is(callErr, ErrDenied) {
			state = "denied"
		}
		if errors.Is(callErr, ErrRejected) {
			state = "rejected"
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := g.finish(cleanup, operation, state, raw); err != nil {
		return nil, ErrAudit
	}
	if callErr != nil {
		return nil, callErr
	}
	return raw, nil
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (g *Gateway) scoped(ctx context.Context, fn func(database.Queries) error) error {
	return g.db.Transaction(ctx, func(q database.Queries) error {
		if _, err := q.Exec(ctx, "SELECT set_config('agentos.tenant_id',$1,true)", g.tenant.String()); err != nil {
			return err
		}
		return fn(q)
	})
}
func (g *Gateway) start(ctx context.Context, p identity.Principal, id uuid.UUID, skill skills.Ref, tool skills.ToolRef, args []byte) error {
	traceID := ""
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		traceID = sc.TraceID().String()
	}
	err := g.scoped(ctx, func(q database.Queries) error {
		result, err := q.Exec(ctx, `INSERT INTO channel.tool_operations(tenant_id,app_id,id,member_id,skill_name,skill_revision,tool_name,tool_revision,input_hash,trace_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'started') ON CONFLICT DO NOTHING`, g.tenant, g.app, id, p.MemberID, skill.Name, skill.Revision, tool.Name, tool.Revision, digest(args), traceID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrAlreadyStarted
		}
		return nil
	})
	if errors.Is(err, ErrAlreadyStarted) {
		return err
	}
	if err != nil {
		return ErrAudit
	}
	return nil
}
func (g *Gateway) finish(ctx context.Context, id uuid.UUID, state string, raw []byte) error {
	return g.scoped(ctx, func(q database.Queries) error {
		hash := ""
		if raw != nil {
			hash = digest(raw)
		}
		result, err := q.Exec(ctx, `UPDATE channel.tool_operations SET state=$4,output_hash=$5,result=$6,finished_at=clock_timestamp() WHERE tenant_id=$1 AND app_id=$2 AND id=$3 AND state='started'`, g.tenant, g.app, id, state, hash, storedResult(state, raw))
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrAudit
		}
		return nil
	})
}

func storedResult(state string, raw []byte) any {
	if state == "succeeded" {
		return json.RawMessage(raw)
	}
	return nil
}

// Result never calls the remote endpoint; exact intent and current permissions are required.
func (g *Gateway) Result(ctx context.Context, actor identity.ExternalIdentity, id uuid.UUID, skill skills.Ref, ref skills.ToolRef, args json.RawMessage) (json.RawMessage, error) {
	p, _, err := g.authorize(ctx, actor, skill, ref)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = g.scoped(ctx, func(q database.Queries) error {
		return q.QueryRow(ctx, `SELECT result FROM channel.tool_operations WHERE tenant_id=$1 AND app_id=$2 AND id=$3 AND member_id=$4 AND skill_name=$5 AND skill_revision=$6 AND tool_name=$7 AND tool_revision=$8 AND input_hash=$9 AND state='succeeded' AND result IS NOT NULL`, g.tenant, g.app, id, p.MemberID, skill.Name, skill.Revision, ref.Name, ref.Revision, digest(args)).Scan(&raw)
	})
	if err != nil {
		return nil, ErrUnknown
	}
	if _, _, err = g.authorize(ctx, actor, skill, ref); err != nil {
		return nil, err
	}
	return raw, nil
}
