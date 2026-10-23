package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/recovery"
	"agentos.local/core/internal/sqlitedb"
	"github.com/jackc/pgx/v5"
)

type recoveryPlan struct {
	Format              int    `json:"format"`
	Driver              string `json:"driver"`
	ChannelConfig       string `json:"channel_config,omitempty"`
	KeyCatalog          string `json:"key_catalog"`
	WrappingKeyBinding  string `json:"wrapping_key_binding"`
	AdminURLEnv         string `json:"admin_url_env,omitempty"`
	AuthoritySQLitePath string `json:"authority_sqlite_path,omitempty"`
	AuthorityURLEnv     string `json:"authority_url_env,omitempty"`
	PGDump              string `json:"pg_dump,omitempty"`
	PGRestore           string `json:"pg_restore,omitempty"`
}

type recoveryRequest struct {
	Action          string `json:"action"`
	Directory       string `json:"directory"`
	TargetDirectory string `json:"target_directory,omitempty"`
	TargetDatabase  string `json:"target_database,omitempty"`
	Online          bool   `json:"online,omitempty"`
}

var errRecoveryAdministration = errors.New("joint recovery administration failed; inspect the new target before retrying; runtime activation is not authorized")

func readRecoveryJSON(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return recovery.ErrInvalid
	}
	return nil
}

func readRecoveryFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, recovery.ErrInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, recovery.ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, recovery.ErrInvalid
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) > limit {
		return nil, recovery.ErrInvalid
	}
	return raw, nil
}

func (c Config) recoveryBindings() []string {
	bindings := append([]string{c.DatabaseURLEnv, c.Model.APIKeyEnv}, c.Channel.secretRefs()...)
	if c.Embedding != nil {
		bindings = append(bindings, c.Embedding.APIKeyEnv)
	}
	if c.Operations != nil {
		bindings = append(bindings, c.Operations.TokenEnv)
	}
	if c.Tools != nil {
		for _, binding := range c.Tools.Credentials {
			bindings = append(bindings, binding.TokenEnv)
		}
	}
	if c.OAuth != nil {
		for _, binding := range c.OAuth.KeyEnvs {
			bindings = append(bindings, binding)
		}
	}
	for _, source := range c.Sources {
		bindings = append(bindings, source.Owner.AccessTokenEnv)
		for _, recipient := range source.Recipients {
			bindings = append(bindings, recipient.AccessTokenEnv)
		}
	}
	slices.Sort(bindings)
	bindings = slices.Compact(bindings)
	return slices.DeleteFunc(bindings, func(value string) bool { return value == "" })
}

func recoveryDriver(c Config) string {
	if c.StorageDriver == "sqlite" {
		return "sqlite"
	}
	return "postgres"
}

func executableDigest() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", errRecoveryAdministration
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errRecoveryAdministration
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return "", errRecoveryAdministration
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateRecoveryPlan(p recoveryPlan, r recoveryRequest) error {
	if p.Format != 1 || (p.Driver != "sqlite" && p.Driver != "postgres") || !filepath.IsAbs(p.KeyCatalog) || !secretReference.MatchString(p.WrappingKeyBinding) || !filepath.IsAbs(r.Directory) {
		return recovery.ErrInvalid
	}
	if p.Driver == "sqlite" && (p.AdminURLEnv != "" || p.AuthorityURLEnv != "" || p.PGDump != "" || p.PGRestore != "" || r.TargetDatabase != "") {
		return recovery.ErrInvalid
	}
	if p.Driver == "postgres" && (p.AuthoritySQLitePath != "" || r.Online || !secretReference.MatchString(p.AdminURLEnv)) {
		return recovery.ErrInvalid
	}
	switch r.Action {
	case "backup":
		if !filepath.IsAbs(p.ChannelConfig) || r.TargetDirectory != "" || r.TargetDatabase != "" {
			return recovery.ErrInvalid
		}
		if p.Driver == "postgres" && !filepath.IsAbs(p.PGDump) {
			return recovery.ErrInvalid
		}
	case "restore":
		if r.Online || !filepath.IsAbs(r.TargetDirectory) {
			return recovery.ErrInvalid
		}
		if p.Driver == "sqlite" && !filepath.IsAbs(p.AuthoritySQLitePath) {
			return recovery.ErrAuthority
		}
		if p.Driver == "postgres" && (!secretReference.MatchString(p.AuthorityURLEnv) || !filepath.IsAbs(p.PGRestore) || r.TargetDatabase == "") {
			return recovery.ErrInvalid
		}
	default:
		return recovery.ErrInvalid
	}
	return nil
}

func safeRecoveryError(err error) error {
	for _, kind := range []error{recovery.ErrInvalid, recovery.ErrVersion, recovery.ErrAuthority, recovery.ErrKeys, recovery.ErrIntegrity, recovery.ErrPostgres, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, kind) {
			return kind
		}
	}
	return errRecoveryAdministration
}

// RunRecovery is an offline local-administrator command. It opens no HTTP
// listener or Worker. The plan, key catalog and environment are trusted inputs;
// the current authority must be maintained outside the backup being restored.
func RunRecovery(ctx context.Context, planPath string, input io.Reader, output io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(input, 16*1024+1))
	if err != nil || len(raw) > 16*1024 {
		return recovery.ErrInvalid
	}
	var request recoveryRequest
	if err = readRecoveryJSON(raw, &request); err != nil {
		return err
	}
	raw, err = readRecoveryFile(planPath, 128*1024)
	if err != nil {
		return err
	}
	var plan recoveryPlan
	if err = readRecoveryJSON(raw, &plan); err != nil {
		return err
	}
	if err = validateRecoveryPlan(plan, request); err != nil {
		return err
	}
	digest, err := executableDigest()
	if err != nil {
		return err
	}
	version := recovery.Version{Driver: plan.Driver, ArtifactSHA256: digest, Schema: database.SchemaVersion}
	if plan.Driver == "sqlite" {
		version.Schema = sqlitedb.SchemaVersion
	}
	keys := recovery.EnvironmentKeys{CatalogPath: plan.KeyCatalog, Getenv: os.Getenv}
	tools := recovery.PostgresTools{Dump: plan.PGDump, Restore: plan.PGRestore}
	if request.Action == "backup" {
		err = backupSite(ctx, plan, request, version, keys, tools)
	} else {
		err = restoreSite(ctx, plan, request, version, keys, tools)
	}
	if err != nil {
		return safeRecoveryError(err)
	}
	result := struct {
		Action              string `json:"action"`
		Verified            bool   `json:"verified"`
		RuntimeStartAllowed bool   `json:"runtime_start_allowed"`
		DatabaseQuarantined bool   `json:"database_quarantined"`
	}{request.Action, true, false, request.Action == "restore" && plan.Driver == "postgres"}
	if json.NewEncoder(output).Encode(result) != nil {
		return errors.New("cannot write joint recovery result; inspect target before retrying")
	}
	return nil
}

func backupSite(ctx context.Context, plan recoveryPlan, request recoveryRequest, version recovery.Version, keys recovery.EnvironmentKeys, tools recovery.PostgresTools) error {
	raw, err := readRecoveryFile(plan.ChannelConfig, recovery.MaxConfiguration)
	if err != nil {
		return err
	}
	defer clear(raw)
	c, err := Parse(raw)
	if err != nil {
		return recovery.ErrInvalid
	}
	if recoveryDriver(c) != plan.Driver {
		return recovery.ErrInvalid
	}
	refs, err := keys.References(ctx, c.recoveryBindings())
	if err != nil {
		return err
	}
	wrapping, err := keys.References(ctx, []string{plan.WrappingKeyBinding})
	if err != nil {
		return err
	}
	manifest := recovery.Manifest{Version: version, WrappingKey: wrapping[0], Secrets: refs}
	if c.OAuth != nil {
		manifest.OAuthKeyBindings = maps.Clone(c.OAuth.KeyEnvs)
	}
	if plan.Driver == "sqlite" {
		return recovery.BackupSQLite(ctx, c.SQLitePath, request.Directory, request.Online, raw, manifest, keys)
	}
	adminURL, err := secret(plan.AdminURLEnv)
	if err != nil {
		return recovery.ErrKeys
	}
	runtimeURL, err := secret(c.DatabaseURLEnv)
	if err != nil {
		return recovery.ErrKeys
	}
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		return recovery.ErrInvalid
	}
	runtimeConfig, err := pgx.ParseConfig(runtimeURL)
	if err != nil {
		return recovery.ErrInvalid
	}
	if adminConfig.Host != runtimeConfig.Host || adminConfig.Port != runtimeConfig.Port || adminConfig.Database != runtimeConfig.Database {
		return errors.New("backup administrator and configured runtime must target the same database")
	}
	if c.OAuth != nil {
		if _, err = c.loadOAuthKeys(); err != nil {
			return recovery.ErrKeys
		}
	}
	return recovery.BackupPostgres(ctx, adminURL, request.Directory, raw, manifest, keys, tools)
}

func restoreSite(ctx context.Context, plan recoveryPlan, request recoveryRequest, version recovery.Version, keys recovery.EnvironmentKeys, tools recovery.PostgresTools) error {
	var authority recovery.Authority
	if plan.Driver == "sqlite" {
		// The backup cannot certify its own freshness. A separately maintained
		// current database or authority replica is mandatory.
		bundlePath, err := filepath.EvalSymlinks(request.Directory)
		if err != nil {
			return recovery.ErrAuthority
		}
		authorityPath, err := filepath.EvalSymlinks(plan.AuthoritySQLitePath)
		if err != nil {
			return recovery.ErrAuthority
		}
		rel, err := filepath.Rel(bundlePath, authorityPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return recovery.ErrAuthority
		}
		if err != nil && filepath.VolumeName(bundlePath) == filepath.VolumeName(authorityPath) {
			return recovery.ErrAuthority
		}
		authority = func(ctx context.Context) (string, error) {
			return recovery.SQLiteAuthorization(ctx, plan.AuthoritySQLitePath)
		}
	} else {
		currentURL, err := secret(plan.AuthorityURLEnv)
		if err != nil {
			return recovery.ErrAuthority
		}
		authority = func(ctx context.Context) (string, error) { return recovery.PostgresAuthorization(ctx, currentURL) }
	}
	raw, manifest, err := recovery.ReadConfiguration(ctx, request.Directory, version, authority, keys)
	if err != nil {
		return err
	}
	defer clear(raw)
	c, err := Parse(raw)
	if err != nil || recoveryDriver(c) != plan.Driver {
		return recovery.ErrInvalid
	}
	refs, err := keys.References(ctx, c.recoveryBindings())
	if err != nil {
		return err
	}
	if !slices.Equal(refs, manifest.Secrets) {
		return recovery.ErrKeys
	}
	var bindings map[string]string
	if c.OAuth != nil {
		bindings = c.OAuth.KeyEnvs
	}
	if !maps.Equal(bindings, manifest.OAuthKeyBindings) {
		return recovery.ErrKeys
	}
	if c.OAuth != nil {
		if _, err = c.loadOAuthKeys(); err != nil {
			return recovery.ErrKeys
		}
	}
	if plan.Driver == "sqlite" {
		return recovery.RestoreSQLite(ctx, request.Directory, request.TargetDirectory, version, authority, keys)
	}
	adminURL, err := secret(plan.AdminURLEnv)
	if err != nil {
		return recovery.ErrKeys
	}
	return recovery.RestorePostgres(ctx, adminURL, request.Directory, request.TargetDatabase, request.TargetDirectory, version, authority, keys, tools)
}
