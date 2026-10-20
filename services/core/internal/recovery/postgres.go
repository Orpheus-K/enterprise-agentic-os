package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"agentos.local/core/internal/database"
	"github.com/jackc/pgx/v5"
)

type PostgresTools struct{ Dump, Restore string }

type postgresTable struct {
	Name     string `json:"name"`
	Owner    string `json:"owner"`
	RLS      bool   `json:"rls"`
	ForceRLS bool   `json:"force_rls"`
	Rows     int64  `json:"rows"`
	SHA256   string `json:"sha256"`
}

type postgresSnapshot struct {
	Schema      int             `json:"schema"`
	Major       int             `json:"postgres_major"`
	Vector      string          `json:"pgvector"`
	Tables      []postgresTable `json:"tables"`
	OAuthKeyIDs []string        `json:"oauth_key_ids"`
}

var ErrPostgres = errors.New("PostgreSQL recovery operation failed; target is not approved for runtime use")
var databaseName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func postgresConnect(ctx context.Context, connectionURL string) (*pgx.Conn, error) {
	if _, err := postgresEnvironment(connectionURL); err != nil {
		return nil, ErrPostgres
	}
	connection, err := pgx.Connect(ctx, connectionURL)
	if err != nil {
		return nil, ErrPostgres
	}
	var privileged bool
	if err = connection.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname=current_user`).Scan(&privileged); err != nil || !privileged {
		connection.Close(ctx)
		return nil, ErrPostgres
	}
	return connection, nil
}

func postgresEnvironment(connectionURL string) ([]string, error) {
	parsed, err := url.Parse(connectionURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.User == nil || parsed.Hostname() == "" || parsed.Path == "" || parsed.Path == "/" || parsed.Fragment != "" {
		return nil, ErrPostgres
	}
	password, _ := parsed.User.Password()
	env := []string{"PGHOST=" + parsed.Hostname(), "PGPORT=" + parsed.Port(), "PGUSER=" + parsed.User.Username(), "PGPASSWORD=" + password, "PGDATABASE=" + strings.TrimPrefix(parsed.Path, "/"), "PGCONNECT_TIMEOUT=10", "PGCLIENTENCODING=UTF8"}
	options := map[string]string{"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "channel_binding": "PGCHANNELBINDING", "application_name": "PGAPPNAME"}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, ErrPostgres
	}
	for key, values := range query {
		name, ok := options[key]
		if !ok || len(values) != 1 {
			return nil, ErrPostgres
		}
		env = append(env, name+"="+values[0])
	}
	return env, nil
}

func closePostgres(connection *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection.Close(ctx)
}

func snapshotPostgres(ctx context.Context, q database.Queries) (postgresSnapshot, error) {
	var snapshot postgresSnapshot
	if _, err := q.Exec(ctx, `SET LOCAL timezone='UTC'; SET LOCAL datestyle='ISO'; SET LOCAL extra_float_digits=3`); err != nil {
		return snapshot, ErrPostgres
	}
	if err := q.QueryRow(ctx, `SELECT max(version_id) FROM public.goose_db_version WHERE is_applied`).Scan(&snapshot.Schema); err != nil {
		return snapshot, ErrPostgres
	}
	if err := q.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&snapshot.Major); err != nil {
		return snapshot, ErrPostgres
	}
	if err := q.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname='vector'`).Scan(&snapshot.Vector); err != nil {
		return snapshot, ErrPostgres
	}
	if snapshot.Schema != database.SchemaVersion || snapshot.Major != 17 || snapshot.Vector != "0.8.7" {
		return snapshot, ErrVersion
	}
	rows, err := q.Query(ctx, `SELECT n.nspname,c.relname,pg_get_userbyid(c.relowner),c.relrowsecurity,c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','iam','channel','knowledge') AND c.relkind='r' ORDER BY n.nspname COLLATE "C",c.relname COLLATE "C"`)
	if err != nil {
		return snapshot, ErrPostgres
	}
	var identifiers []pgx.Identifier
	for rows.Next() {
		var schema, name string
		var table postgresTable
		if err = rows.Scan(&schema, &name, &table.Owner, &table.RLS, &table.ForceRLS); err != nil {
			break
		}
		table.Name = schema + "." + name
		identifiers = append(identifiers, pgx.Identifier{schema, name})
		snapshot.Tables = append(snapshot.Tables, table)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil || len(snapshot.Tables) == 0 {
		return snapshot, ErrPostgres
	}
	for i, identifier := range identifiers {
		hash := sha256.New()
		rows, err := q.Query(ctx, `SELECT row_to_json(t)::text FROM `+identifier.Sanitize()+` t ORDER BY row_to_json(t)::text COLLATE "C"`)
		if err != nil {
			return snapshot, ErrPostgres
		}
		for rows.Next() {
			var row string
			if err = rows.Scan(&row); err != nil {
				break
			}
			io.WriteString(hash, row+"\n")
			snapshot.Tables[i].Rows++
		}
		err = errors.Join(err, rows.Err())
		rows.Close()
		if err != nil {
			return snapshot, ErrPostgres
		}
		snapshot.Tables[i].SHA256 = hex.EncodeToString(hash.Sum(nil))
	}
	snapshot.OAuthKeyIDs, err = postgresOAuthKeyIDs(ctx, q)
	if err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func postgresAuthorization(snapshot postgresSnapshot) string {
	var tables []postgresTable
	for _, table := range snapshot.Tables {
		if strings.HasPrefix(table.Name, "iam.") || strings.HasPrefix(table.Name, "knowledge.") && slices.Contains([]string{"knowledge.sources", "knowledge.source_health", "knowledge.documents", "knowledge.chunks"}, table.Name) || strings.HasPrefix(table.Name, "channel.oauth_") {
			tables = append(tables, table)
		}
	}
	encoded, _ := json.Marshal(tables)
	return Digest(encoded)
}

// PostgresAuthorization uses a current privileged source; runtime RLS-scoped
// connections cannot certify the complete revocation state. Source health, ACLs,
// content versions and OAuth ledger changes conservatively invalidate old facts.
func PostgresAuthorization(ctx context.Context, connectionURL string) (string, error) {
	connection, err := postgresConnect(ctx, connectionURL)
	if err != nil {
		return "", err
	}
	defer closePostgres(connection)
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", ErrPostgres
	}
	defer tx.Rollback(ctx)
	snapshot, err := snapshotPostgres(ctx, tx)
	if err != nil {
		return "", err
	}
	return postgresAuthorization(snapshot), nil
}

// Read ciphertext key dependencies from the same transaction as the dump facts.
func postgresOAuthKeyIDs(ctx context.Context, q database.Queries) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT key_id FROM channel.oauth_credentials ORDER BY key_id LIMIT 129`)
	if err != nil {
		return nil, ErrPostgres
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, ErrPostgres
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return nil, ErrPostgres
	}
	if len(ids) > 128 {
		return nil, ErrKeys
	}
	return ids, nil
}

func postgresCommand(ctx context.Context, binary, connectionURL string, args ...string) error {
	if !filepath.IsAbs(binary) {
		return ErrInvalid
	}
	version, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil || !regexp.MustCompile(`^pg_(dump|restore) \(PostgreSQL\) 17\.`).Match(version) {
		return ErrPostgres
	}
	command := exec.CommandContext(ctx, binary, args...)
	env, err := postgresEnvironment(connectionURL)
	if err != nil {
		return err
	}
	// Keep connection secrets out of argv and diagnostic output; ignore ambient
	// PG* settings that could redirect a recovery operation to another database.
	for _, name := range []string{"SystemRoot", "WINDIR", "PATH", "TEMP", "TMP", "TMPDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	command.Env = append(command.Env, env...)
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrPostgres
	}
	return nil
}

// BackupPostgres captures a single pg_export_snapshot shared by pg_dump and the
// manifest. It does not export cluster roles, passwords or tablespaces.
func BackupPostgres(ctx context.Context, connectionURL, directory string, configuration []byte, manifest Manifest, keys KeyStore, tools PostgresTools) error {
	if !filepath.IsAbs(directory) || manifest.Version.Driver != "postgres" || manifest.Version.Schema != database.SchemaVersion {
		return ErrInvalid
	}
	connection, err := postgresConnect(ctx, connectionURL)
	if err != nil {
		return err
	}
	defer closePostgres(connection)
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ErrPostgres
	}
	defer tx.Rollback(ctx)
	var snapshotID string
	if err = tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&snapshotID); err != nil {
		return ErrPostgres
	}
	snapshot, err := snapshotPostgres(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range snapshot.OAuthKeyIDs {
		if manifest.OAuthKeyBindings[id] == "" {
			return ErrKeys
		}
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		return err
	}
	databaseDir := filepath.Join(directory, "database")
	if err = os.Mkdir(databaseDir, 0700); err != nil {
		return err
	}
	if err = postgresCommand(ctx, tools.Dump, connectionURL, "--no-password", "--format=custom", "--snapshot="+snapshotID, "--file="+filepath.Join(databaseDir, "database.dump")); err != nil {
		return err
	}
	dump, err := os.OpenFile(filepath.Join(databaseDir, "database.dump"), os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = errors.Join(dump.Sync(), dump.Close()); err != nil {
		return err
	}
	state, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err = writeExclusive(filepath.Join(databaseDir, "state.json"), state); err != nil {
		return err
	}
	manifest.AuthorizationSHA256 = postgresAuthorization(snapshot)
	if err = checkAuthority(ctx, func(ctx context.Context) (string, error) { return PostgresAuthorization(ctx, connectionURL) }, manifest.AuthorizationSHA256); err != nil {
		return err
	}
	manifest.Database = nil
	for _, name := range []string{"database.dump", "state.json"} {
		artifact, err := fileArtifact(ctx, databaseDir, name)
		if err != nil {
			return err
		}
		manifest.Database = append(manifest.Database, artifact)
	}
	envelope, err := Seal(ctx, manifest, configuration, keys)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return writeExclusive(filepath.Join(directory, "joint.json"), encoded)
}

// RestorePostgres creates only a new quarantined database (connection limit 0,
// no runtime/migrator CONNECT). It remains quarantined even on success. There is
// no atomic filesystem/database publish: configuration is published last; errors
// attempt removal of only the database created by this invocation. Any cleanup
// error leaves the quarantined target for explicit operator reconciliation.
func RestorePostgres(ctx context.Context, adminURL, directory, targetDB, targetDirectory string, expected Version, authority Authority, keys KeyStore, tools PostgresTools) (err error) {
	if !databaseName.MatchString(targetDB) || !filepath.IsAbs(targetDirectory) || expected.Driver != "postgres" || expected.Schema != database.SchemaVersion {
		return ErrInvalid
	}
	if _, statErr := os.Lstat(targetDirectory); !os.IsNotExist(statErr) {
		return errors.New("joint restore target directory must not exist")
	}
	envelope, err := readBundle(ctx, directory)
	if err != nil {
		return err
	}
	names := []string{}
	for _, artifact := range envelope.Manifest.Database {
		names = append(names, artifact.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"database.dump", "state.json"}) {
		return ErrInvalid
	}
	configuration, err := Open(ctx, envelope, expected, authority, keys)
	if err != nil {
		return err
	}
	defer clear(configuration)
	state, err := os.ReadFile(filepath.Join(directory, "database", "state.json"))
	if err != nil {
		return err
	}
	var snapshot postgresSnapshot
	if err = json.Unmarshal(state, &snapshot); err != nil || snapshot.Schema != expected.Schema {
		return ErrInvalid
	}
	for _, id := range snapshot.OAuthKeyIDs {
		if envelope.Manifest.OAuthKeyBindings[id] == "" {
			return ErrKeys
		}
	}
	admin, err := postgresConnect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer closePostgres(admin)
	var compatible bool
	if err = admin.QueryRow(ctx, `SELECT current_setting('server_version_num')::int/10000=$1 AND EXISTS(SELECT 1 FROM pg_available_extensions WHERE name='vector' AND default_version=$2)`, snapshot.Major, snapshot.Vector).Scan(&compatible); err != nil || !compatible {
		return ErrVersion
	}
	var exists bool
	if err = admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, targetDB).Scan(&exists); err != nil {
		return ErrPostgres
	}
	if exists {
		return errors.New("joint restore database must not exist")
	}
	quoted := pgx.Identifier{targetDB}.Sanitize()
	if _, err = admin.Exec(ctx, `CREATE DATABASE `+quoted+` OWNER agentos_migrator TEMPLATE template0 CONNECTION LIMIT 0`); err != nil {
		return ErrPostgres
	}
	published := false
	defer func() {
		if published {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, cleanupErr := admin.Exec(cleanup, `DROP DATABASE `+quoted+` WITH (FORCE)`); cleanupErr != nil {
			err = errors.Join(err, errors.New("quarantined recovery database cleanup failed; operator reconciliation required"))
		}
	}()
	if _, err = admin.Exec(ctx, `REVOKE ALL ON DATABASE `+quoted+` FROM PUBLIC,agentos_runtime,agentos_migrator`); err != nil {
		return ErrPostgres
	}
	parsed, _ := url.Parse(adminURL)
	parsed.Path = "/" + targetDB
	if err = postgresCommand(ctx, tools.Restore, parsed.String(), "--no-password", "--exit-on-error", "--single-transaction", "--dbname=", filepath.Join(directory, "database", "database.dump")); err != nil {
		return err
	}
	restored, err := postgresConnect(ctx, parsed.String())
	if err != nil {
		return err
	}
	tx, err := restored.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		closePostgres(restored)
		return ErrPostgres
	}
	actual, snapshotErr := snapshotPostgres(ctx, tx)
	tx.Rollback(ctx)
	closePostgres(restored)
	if snapshotErr != nil || !reflect.DeepEqual(actual, snapshot) || postgresAuthorization(actual) != envelope.Manifest.AuthorizationSHA256 {
		return ErrIntegrity
	}
	staging, err := os.MkdirTemp(filepath.Dir(targetDirectory), ".joint-restore-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(staging)) }()
	if err = writeExclusive(filepath.Join(staging, "configuration.json"), configuration); err != nil {
		return err
	}
	if err = checkAuthority(ctx, authority, envelope.Manifest.AuthorizationSHA256); err != nil {
		return err
	}
	if err = keys.Check(ctx, append(slices.Clone(envelope.Manifest.Secrets), envelope.Manifest.WrappingKey)); err != nil {
		return ErrKeys
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(staging, targetDirectory); err != nil {
		return err
	}
	published = true
	return nil
}
