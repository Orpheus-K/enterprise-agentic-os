package identity

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestRejectInvalidIdentity(t *testing.T) {
	valid := ExternalIdentity{TenantID: uuid.New(), Provider: "oidc", Issuer: "https://id.example", Subject: "employee"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", " user", "user ", "user\x00"} {
		invalid := valid
		invalid.Subject = value
		if !errors.Is(invalid.Validate(), ErrInvalidIdentity) {
			t.Fatalf("accepted subject %q", value)
		}
	}
	if !errors.Is((Principal{}).Validate(), ErrInvalidIdentity) {
		t.Fatal("accepted anonymous principal")
	}
}
