// Package feishudocs reads Feishu docx snapshots using an explicitly bound user
// credential. Application access is never interpreted as an employee's access.
package feishudocs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	docx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	drive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
)

var (
	ErrInvalid = errors.New("invalid Feishu document input")
	ErrDenied  = errors.New("Feishu user identity or document permission denied")
	ErrRemote  = errors.New("Feishu document provider unavailable or invalid")
	ErrChanged = errors.New("Feishu document changed during read")
	ErrLimit   = errors.New("Feishu document exceeds snapshot limits")
)

const (
	maxResponse = 2 << 20
	maxSnapshot = 8 << 20
	maxBlocks   = 4096
	maxPages    = 32
)

type User struct {
	AppID     string `json:"app_id"`
	OpenID    string `json:"open_id"`
	TenantKey string `json:"tenant_key"`
}

type Credential struct {
	user       User
	token      string
	oauth      *OAuth
	generation int64
}

func (Credential) String() string   { return "[Feishu credential redacted]" }
func (Credential) GoString() string { return "[Feishu credential redacted]" }

func NewCredential(user User, accessToken string) (Credential, error) {
	if !identifier(user.AppID) || !strings.HasPrefix(user.AppID, "cli_") || !identifier(user.OpenID) || !strings.HasPrefix(user.OpenID, "ou_") || !identifier(user.TenantKey) ||
		accessToken == "" || len(accessToken) > 8192 || strings.ContainsAny(accessToken, " \r\n\t") {
		return Credential{}, ErrInvalid
	}
	return Credential{user: user, token: accessToken}, nil
}

// Blocks retain complete raw objects, including unknown future fields. A later
// normalizer must explicitly support their content types before publishing text.
type Snapshot struct {
	User       User
	DocumentID string
	Revision   int
	Title      string
	Blocks     []json.RawMessage
}

type Reader struct {
	client *lark.Client
	appID  string
}

func New(appID string) (*Reader, error) {
	if !identifier(appID) || !strings.HasPrefix(appID, "cli_") {
		return nil, ErrInvalid
	}
	return newReader(appID, "https://open.feishu.cn", http.DefaultTransport), nil
}

// Only tests override the destination. Every production request uses Feishu's
// fixed origin and a user token supplied explicitly, with no application fallback.
func newReader(appID, baseURL string, transport http.RoundTripper) *Reader {
	client := &http.Client{Timeout: 15 * time.Second, Transport: boundedTransport{transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRemote }}
	return &Reader{lark.NewClient(appID, "", lark.WithOpenBaseUrl(baseURL),
		lark.WithEnableTokenCache(false), lark.WithHttpClient(client), lark.WithLogger(silentLogger{})), appID}
}

func identifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (r *Reader) validate(c Credential, id string) error {
	token := c.token
	if c.oauth != nil {
		if c.oauth.user != c.user {
			return ErrInvalid
		}
		token = "bound-oauth-reference"
	}
	if _, err := NewCredential(c.user, token); err != nil || !identifier(id) || c.user.AppID != r.appID {
		return ErrInvalid
	}
	return nil
}

// SDK CodeError defaults absent code to zero; require an explicit successful
// envelope and HTTP status. Raw errors are never forwarded or logged.
func success(resp *larkcore.ApiResp) bool {
	if resp == nil || resp.StatusCode != http.StatusOK {
		return false
	}
	var envelope struct {
		Code *int `json:"code"`
	}
	return json.Unmarshal(resp.RawBody, &envelope) == nil && envelope.Code != nil && *envelope.Code == 0
}

func (r *Reader) verifyUser(ctx context.Context, c Credential) error {
	resp, err := r.client.Authen.V1.UserInfo.Get(ctx, larkcore.WithUserAccessToken(c.token))
	if err != nil || resp == nil || !success(resp.ApiResp) || resp.Data == nil {
		return ErrRemote
	}
	if resp.Data.OpenId == nil || resp.Data.TenantKey == nil || *resp.Data.OpenId != c.user.OpenID || *resp.Data.TenantKey != c.user.TenantKey {
		return ErrDenied
	}
	return nil
}

func (r *Reader) permission(ctx context.Context, c Credential, id string) error {
	req := drive.NewAuthPermissionMemberReqBuilder().Token(id).Type("docx").Action("view").Build()
	resp, err := r.client.Drive.V1.PermissionMember.Auth(ctx, req, larkcore.WithUserAccessToken(c.token))
	if err != nil || resp == nil || !success(resp.ApiResp) || resp.Data == nil || resp.Data.AuthResult == nil {
		return ErrRemote
	}
	if !*resp.Data.AuthResult {
		return ErrDenied
	}
	return nil
}

// CheckAccess is a point-in-time proof for this particular user, not an ACL list
// and not a guarantee for other employees or future requests.
func (r *Reader) CheckAccess(ctx context.Context, c Credential, id string) error {
	if err := r.validate(c, id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if c.oauth != nil {
		var err error
		c, err = c.oauth.access(ctx)
		if err != nil {
			return err
		}
	}
	if err := r.verifyUser(ctx, c); err != nil {
		return err
	}
	if err := r.permission(ctx, c, id); err != nil {
		return err
	}
	if c.oauth != nil {
		return c.oauth.current(ctx, c.generation)
	}
	return nil
}

func (r *Reader) metadata(ctx context.Context, c Credential, id string) (Snapshot, error) {
	req := docx.NewGetDocumentReqBuilder().DocumentId(id).Build()
	resp, err := r.client.Docx.V1.Document.Get(ctx, req, larkcore.WithUserAccessToken(c.token))
	if err != nil || resp == nil || !success(resp.ApiResp) || resp.Data == nil || resp.Data.Document == nil {
		return Snapshot{}, ErrRemote
	}
	d := resp.Data.Document
	if d.DocumentId == nil || *d.DocumentId != id || d.RevisionId == nil || *d.RevisionId < 1 || d.Title == nil || len(*d.Title) > 4096 {
		return Snapshot{}, ErrRemote
	}
	return Snapshot{User: c.user, DocumentID: id, Revision: *d.RevisionId, Title: *d.Title}, nil
}

// Read pins every page to the initial revision, then checks metadata, identity
// and view permission again. Any failed page/cursor/check discards the entire
// result. Nothing from an incomplete snapshot is returned for publication.
func (r *Reader) Read(ctx context.Context, c Credential, id string) (Snapshot, error) {
	if err := r.validate(c, id); err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if c.oauth != nil {
		var err error
		c, err = c.oauth.access(ctx)
		if err != nil {
			return Snapshot{}, err
		}
	}
	if err := r.verifyUser(ctx, c); err != nil {
		return Snapshot{}, err
	}
	if err := r.permission(ctx, c, id); err != nil {
		return Snapshot{}, err
	}
	out, err := r.metadata(ctx, c, id)
	if err != nil {
		return Snapshot{}, err
	}
	cursor := ""
	seen := map[string]bool{}
	size := 0
	for page := 0; ; page++ {
		if page >= maxPages {
			return Snapshot{}, ErrLimit
		}
		req := docx.NewListDocumentBlockReqBuilder().DocumentId(id).DocumentRevisionId(out.Revision).PageSize(500).PageToken(cursor).Build()
		resp, err := r.client.Docx.V1.DocumentBlock.List(ctx, req, larkcore.WithUserAccessToken(c.token))
		if err != nil || resp == nil || !success(resp.ApiResp) {
			return Snapshot{}, ErrRemote
		}
		var envelope struct {
			Data *struct {
				Items     []json.RawMessage `json:"items"`
				HasMore   *bool             `json:"has_more"`
				PageToken string            `json:"page_token"`
			} `json:"data"`
		}
		if json.Unmarshal(resp.RawBody, &envelope) != nil || envelope.Data == nil || envelope.Data.HasMore == nil || len(envelope.Data.Items) == 0 {
			return Snapshot{}, ErrRemote
		}
		data := envelope.Data
		for _, raw := range data.Items {
			size += len(raw)
			if size > maxSnapshot || len(out.Blocks) >= maxBlocks {
				return Snapshot{}, ErrLimit
			}
			out.Blocks = append(out.Blocks, raw)
		}
		if !*data.HasMore {
			break
		}
		if data.PageToken == "" || len(data.PageToken) > 4096 || seen[data.PageToken] {
			return Snapshot{}, ErrRemote
		}
		seen[data.PageToken] = true
		cursor = data.PageToken
	}
	if err := completeTree(out.Blocks); err != nil {
		return Snapshot{}, err
	}
	latest, err := r.metadata(ctx, c, id)
	if err != nil {
		return Snapshot{}, err
	}
	if latest.Revision != out.Revision || latest.Title != out.Title {
		return Snapshot{}, ErrChanged
	}
	if err := r.verifyUser(ctx, c); err != nil {
		return Snapshot{}, err
	}
	if err := r.permission(ctx, c, id); err != nil {
		return Snapshot{}, err
	}
	if c.oauth != nil {
		if err := c.oauth.current(ctx, c.generation); err != nil {
			return Snapshot{}, err
		}
	}
	return out, nil
}

type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, ErrRemote
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	closeErr := resp.Body.Close()
	if err != nil || closeErr != nil {
		return nil, ErrRemote
	}
	if len(raw) > maxResponse {
		return nil, ErrLimit
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return resp, nil
}

// SDK logs can contain response bodies. This source adapter emits no SDK logs.
type silentLogger struct{}

func (silentLogger) Debug(context.Context, ...interface{}) {}
func (silentLogger) Info(context.Context, ...interface{})  {}
func (silentLogger) Warn(context.Context, ...interface{})  {}
func (silentLogger) Error(context.Context, ...interface{}) {}
