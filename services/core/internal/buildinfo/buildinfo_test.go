package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestUnavailableSourceIsNotReportedAsClean(t *testing.T) {
	for _, settings := range [][]debug.BuildSetting{
		nil,
		{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "abc"}},
		{{Key: "vcs", Value: "git"}, {Key: "vcs.modified", Value: "false"}},
	} {
		if source(settings) != nil {
			t.Fatal("incomplete provenance reported as known")
		}
	}
	for _, dirty := range []string{"true", "false"} {
		got := source([]debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: dirty}})
		if got == nil || got.Revision != "abc" || got.Modified != (dirty == "true") {
			t.Fatal("embedded provenance changed", got)
		}
	}
}
