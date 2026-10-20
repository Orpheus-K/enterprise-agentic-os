// Package buildinfo reports embedded artifact identity without opening services.
package buildinfo

import (
	"encoding/json"
	"io"
	"runtime"
	"runtime/debug"
	"strconv"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/quotation"
)

type Source struct {
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
}

type Metadata struct {
	Component           string  `json:"component"`
	GoVersion           string  `json:"go_version"`
	OS                  string  `json:"os"`
	Architecture        string  `json:"architecture"`
	Source              *Source `json:"source"`
	SchemaVersion       int64   `json:"schema_version"`
	QuotationWASMSHA256 string  `json:"quotation_wasm_sha256"`
}

func Current(component string) Metadata {
	out := Metadata{Component: component, GoVersion: runtime.Version(), OS: runtime.GOOS,
		Architecture: runtime.GOARCH, SchemaVersion: database.SchemaVersion,
		QuotationWASMSHA256: quotation.ApprovalModuleSHA256}
	if info, ok := debug.ReadBuildInfo(); ok {
		out.Source = source(info.Settings)
	}
	return out
}

func source(settings []debug.BuildSetting) *Source {
	values := map[string]string{}
	for _, setting := range settings {
		values[setting.Key] = setting.Value
	}
	modified, err := strconv.ParseBool(values["vcs.modified"])
	if values["vcs"] != "git" || values["vcs.revision"] == "" || err != nil {
		return nil
	}
	return &Source{Revision: values["vcs.revision"], Modified: modified}
}

func Write(w io.Writer, component string) error {
	return json.NewEncoder(w).Encode(Current(component))
}
