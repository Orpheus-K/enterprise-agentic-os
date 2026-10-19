package knowledge

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestACLInheritanceDiffersFromExplicitEmpty(t *testing.T) {
	a := ACL{SourceID: uuid.New(), DocumentID: "doc", Revision: 1, ContentRevision: 1, ValidUntil: time.Now(), Chunks: map[string]ChunkACL{"chunk": {}}}
	inherit, err := digest(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Chunks["chunk"] = ChunkACL{Allow: []uuid.UUID{}}
	empty, err := digest(a)
	if err != nil {
		t.Fatal(err)
	}
	if inherit == empty {
		t.Fatal("deny-all and inheritance share event digest")
	}
}
