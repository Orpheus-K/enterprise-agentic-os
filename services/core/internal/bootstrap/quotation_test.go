package bootstrap

import (
	"encoding/json"
	"strings"
	"testing"

	"agentos.local/core/internal/quotation"
)

func TestQuotationWASMConfigurationAndRevision(t *testing.T) {
	base := sourceTestConfig()
	base.Sources = nil
	original := base.executionRevision()
	for _, value := range []string{"", quotation.ApprovalModuleSHA256, strings.Repeat("a", 64), "unapproved"} {
		c := base
		c.QuotationWASMSHA256 = value
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Parse(raw)
		valid := value == "" || value == quotation.ApprovalModuleSHA256
		if (err == nil) != valid {
			t.Fatal("digest validation", value, err)
		}
		if (c.executionRevision() == original) != (value == "") {
			t.Fatal("module digest not bound to execution revision")
		}
	}
}
