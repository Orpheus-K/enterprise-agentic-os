package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Only this transport sees the token. Redirects and alternate destinations are rejected.
type targetTransport struct {
	base            http.RoundTripper
	endpoint, token string
}

func (t *targetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != t.endpoint {
		return nil, ErrDenied
	}
	request := r.Clone(r.Context())
	request.Header = r.Header.Clone()
	request.Header.Set("Authorization", "Bearer "+t.token)
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, ErrUnknown
	}
	response.Body = &boundedBody{body: response.Body, remaining: 262144}
	return response, nil
}

type boundedBody struct {
	body      io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.body.Read(extra[:])
		if n > 0 {
			return 0, ErrRejected
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	return n, err
}
func (b *boundedBody) Close() error { return b.body.Close() }
func invoke(ctx context.Context, tool compiledTool, args any, token string, beforeCall func() error) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(tool.spec.TimeoutSeconds)*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: &targetTransport{transport, tool.spec.Endpoint, token}, Timeout: time.Duration(tool.spec.TimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrDenied }}
	sdk := mcp.NewClient(&mcp.Implementation{Name: "agentos", Version: "m2"}, &mcp.ClientOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
	session, err := sdk.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: tool.spec.Endpoint, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: 262144}, &mcp.ClientSessionOptions{ProtocolVersion: ProtocolVersion})
	if err != nil {
		return nil, ErrUnknown
	}
	defer session.Close()
	if session.InitializeResult() == nil || session.InitializeResult().ProtocolVersion != ProtocolVersion {
		return nil, ErrRejected
	}
	if err := beforeCall(); err != nil {
		return nil, err
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.spec.RemoteName, Arguments: args})
	if err != nil {
		return nil, ErrUnknown
	}
	if result == nil || result.IsError || result.NeedsInput() || result.StructuredContent == nil {
		return nil, ErrRejected
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil || len(raw) > 65536 || bytes.Contains(raw, []byte(token)) {
		return nil, ErrRejected
	}
	var value any
	if json.Unmarshal(raw, &value) != nil || tool.output.Validate(value) != nil {
		return nil, ErrRejected
	}
	return raw, nil
}
