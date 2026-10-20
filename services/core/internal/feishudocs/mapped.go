package feishudocs

import (
	"context"

	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type IdentityResolver interface {
	Resolve(context.Context, identity.ExternalIdentity) (identity.Principal, error)
}

// MappedReader binds a trusted installation to current IAM membership. It does
// not grant source synchronization rights; the knowledge store checks those
// separately in every publishing transaction.
type MappedReader struct {
	reader    *Reader
	resolver  IdentityResolver
	tenant    uuid.UUID
	tenantKey string
}

func NewMappedReader(reader *Reader, resolver IdentityResolver, tenant uuid.UUID, tenantKey string) (*MappedReader, error) {
	if reader == nil || resolver == nil || tenant == uuid.Nil || !identifier(tenantKey) {
		return nil, ErrInvalid
	}
	return &MappedReader{reader: reader, resolver: resolver, tenant: tenant, tenantKey: tenantKey}, nil
}

// Access identifies the employee checked during this operation. It is not a
// transferable or durable authorization token. Consumers must re-resolve inside
// the publishing transaction and still verify source/health/ACL constraints.
type Access struct {
	Actor     identity.ExternalIdentity
	Principal identity.Principal
}

func (r *MappedReader) resolve(ctx context.Context, c Credential, document string) (Access, error) {
	if err := r.reader.validate(c, document); err != nil {
		return Access{}, err
	}
	if c.user.TenantKey != r.tenantKey {
		return Access{}, ErrDenied
	}
	actor := identity.ExternalIdentity{TenantID: r.tenant, Provider: "feishu",
		Issuer: "feishu:" + r.reader.appID + ":" + r.tenantKey, Subject: c.user.OpenID}
	p, err := r.resolver.Resolve(ctx, actor)
	if err != nil {
		return Access{}, err
	}
	if p.Validate() != nil || p.TenantID != r.tenant {
		return Access{}, ErrDenied
	}
	return Access{Actor: actor, Principal: p}, nil
}

func (r *MappedReader) unchanged(ctx context.Context, c Credential, document string, before Access) error {
	after, err := r.resolve(ctx, c, document)
	if err != nil {
		return err
	}
	if after != before {
		return ErrDenied
	}
	return nil
}

func (r *MappedReader) CheckAccess(ctx context.Context, c Credential, document string) (Access, error) {
	access, err := r.resolve(ctx, c, document)
	if err != nil {
		return Access{}, err
	}
	if err := r.reader.CheckAccess(ctx, c, document); err != nil {
		return Access{}, err
	}
	if err := r.unchanged(ctx, c, document, access); err != nil {
		return Access{}, err
	}
	return access, nil
}

func (r *MappedReader) Read(ctx context.Context, c Credential, document string) (Snapshot, Access, error) {
	access, err := r.resolve(ctx, c, document)
	if err != nil {
		return Snapshot{}, Access{}, err
	}
	snapshot, err := r.reader.Read(ctx, c, document)
	if err != nil {
		return Snapshot{}, Access{}, err
	}
	if err := r.unchanged(ctx, c, document, access); err != nil {
		return Snapshot{}, Access{}, err
	}
	return snapshot, access, nil
}
