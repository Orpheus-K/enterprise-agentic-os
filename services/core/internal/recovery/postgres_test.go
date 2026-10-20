package recovery

import (
	"strings"
	"testing"
)

func TestPostgresEnvironmentIsExplicit(t *testing.T) {
	env, err := postgresEnvironment("postgres://admin:synthetic-secret@127.0.0.1:55440/source?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if values["PGDATABASE"] != "source" || values["PGHOST"] != "127.0.0.1" || values["PGPORT"] != "55440" || values["PGPASSWORD"] != "synthetic-secret" || values["PGSSLMODE"] != "disable" {
		t.Fatal("connection options changed")
	}
	for _, input := range []string{
		"postgres://admin@localhost/source?host=other",
		"postgres://admin@localhost/source?options=-crole%3Dother",
		"postgres://admin@localhost/source?sslmode=disable&sslmode=require",
		"postgres://admin@localhost/source?invalid=%xx",
		"postgres://admin@localhost/source#fragment",
		"postgres://localhost/source",
		"not-a-url",
	} {
		if _, err := postgresEnvironment(input); err == nil {
			t.Fatal("unsupported connection options accepted")
		}
	}
}
