package quotation

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"io"
)

// ToolInput is returned by the administrator-pinned, authorized quotation data tool.
// The tool owns catalogue/evidence policy; the model never supplies trusted prices.
type ToolInput struct {
	Request   Request   `json:"request"`
	Catalogue Catalogue `json:"catalogue"`
	Policy    Policy    `json:"policy"`
}

func FromTool(id uuid.UUID, raw []byte) (Draft, error) {
	if len(raw) > 65536 {
		return Draft{}, ErrInvalid
	}
	var input ToolInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return Draft{}, ErrInvalid
	}
	return New(id, input.Request, input.Catalogue, input.Policy)
}
