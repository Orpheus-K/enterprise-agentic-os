package quotation

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
)

func TestToolInputRejectsUntrustedShape(t *testing.T) {
	r, c, p := inputs()
	raw, err := json.Marshal(ToolInput{r, c, p})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := FromTool(uuid.New(), raw); err != nil || d.TotalMinor != 15000 {
		t.Fatal("valid tool input", err)
	}
	for _, body := range [][]byte{append(append([]byte(nil), raw...), []byte(" {}")...), []byte(`{"request":{},"approved":true}`), []byte(`{"request":{"items":[{"quantity":1.5}]}}`)} {
		if _, err := FromTool(uuid.New(), body); err == nil {
			t.Fatal("invalid tool payload accepted")
		}
	}
}
