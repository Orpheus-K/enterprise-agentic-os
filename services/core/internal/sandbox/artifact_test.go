package sandbox

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func artifactModule(data []byte, offset, size uint32) []byte {
	u := func(n uint64) []byte { return binary.AppendUvarint(nil, n) }
	section := func(id byte, body []byte) []byte { return join([]byte{id}, u(uint64(len(body))), body) }
	constant := func(n uint32) []byte {
		b := u(uint64(n))
		if b[len(b)-1]&0x40 != 0 {
			b[len(b)-1] |= 0x80
			b = append(b, 0)
		}
		return append([]byte{0x41}, b...)
	}
	body := join([]byte{0}, constant(offset), constant(size), []byte{0x0b})
	return join(header,
		section(1, []byte{1, 0x60, 0, 2, 0x7f, 0x7f}), section(3, []byte{1, 0}),
		section(5, []byte{1, 0, 1}),
		section(7, []byte{2, 3, 'r', 'u', 'n', 0, 0, 6, 'm', 'e', 'm', 'o', 'r', 'y', 2, 0}),
		section(10, join([]byte{1}, u(uint64(len(body))), body)),
		section(11, join([]byte{1, 0, 0x41, 0, 0x0b}, u(uint64(len(data))), data)))
}

func TestArtifactOutput(t *testing.T) {
	for _, scenario := range []string{"text", "json", "limit", "oversize", "offset", "empty", "utf8", "binary", "script", "key", "eicar", "escaped_script", "invalid_json", "unsupported_type", "no_scanner", "scanner_denied", "scanner_mutation", "cancelled", "no_memory"} {
		t.Run(scenario, func(t *testing.T) {
			data := []byte("合成结果")
			calls := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			policy := ArtifactPolicy{MediaType: "text/plain", MaxBytes: 128, Scan: func(context.Context, []byte) error { calls++; return nil }}
			offset := uint32(0)
			switch scenario {
			case "json":
				data = []byte(`{"synthetic":true}`)
				policy.MediaType = "application/json"
			case "limit":
				policy.MaxBytes = uint32(len(data))
			case "oversize":
				policy.MaxBytes = uint32(len(data) - 1)
			case "offset":
				offset = 65535
			case "empty":
				data = nil
			case "utf8":
				data = []byte{0xff}
			case "binary":
				data = []byte{'M', 'Z', 0}
			case "script":
				data = []byte("<ScRiPt>alert(1)</script>")
			case "key":
				data = []byte("-----BEGIN PRIVATE KEY-----")
			case "eicar":
				data = []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")
			case "invalid_json":
				policy.MediaType = "application/json"
			case "escaped_script":
				policy.MediaType = "application/json"
				data = []byte(`{"text":"\u003cscript\u003e"}`)
			case "unsupported_type":
				policy.MediaType = "text/html"
			case "no_scanner":
				policy.Scan = nil
			case "scanner_denied":
				policy.Scan = func(context.Context, []byte) error { return errors.New("sensitive synthetic content") }
			case "scanner_mutation":
				policy.Scan = func(_ context.Context, b []byte) error { calls++; b[0] = '!'; return nil }
			case "cancelled":
				policy.Scan = func(context.Context, []byte) error { cancel(); return nil }
			}
			program := pin(artifactModule(data, offset, uint32(len(data))))
			if scenario == "no_memory" {
				program = pin(signatureModule(nil, []byte{0x7f, 0x7f}, []byte{0x41, 0, 0x41, 0}))
			}
			got, err := RunArtifact(ctx, program, limits, "run", policy)
			switch scenario {
			case "text", "json", "limit", "scanner_mutation":
				if err != nil || string(got) != string(data) || calls != 1 {
					t.Fatalf("artifact: %q %v scanner=%d", got, err, calls)
				}
			default:
				if err == nil || got != nil {
					t.Fatalf("unsafe artifact: %q %v", got, err)
				}
			}
		})
	}
}

func TestArtifactRequiresBoundedPolicy(t *testing.T) {
	for _, maximum := range []uint32{0, MaxArtifactBytes + 1} {
		got, err := RunArtifact(t.Context(), pin(artifactModule([]byte("ok"), 0, 2)), limits, "run", ArtifactPolicy{MediaType: "text/plain", MaxBytes: maximum, Scan: func(context.Context, []byte) error { return nil }})
		if !errors.Is(err, ErrInvalid) || got != nil {
			t.Fatal("unbounded output accepted")
		}
	}
	if err := scanText([]byte(strings.Repeat("x", 10)), "application/json"); err == nil {
		t.Fatal("non-JSON accepted")
	}
}
