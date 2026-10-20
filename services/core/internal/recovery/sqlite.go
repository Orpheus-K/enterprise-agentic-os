package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"agentos.local/core/internal/sqlitedb"
)

// SQLiteAuthorization hashes the complete local authorization state in one
// transaction. Operators must also reconcile external revocations before backup
// or restore; the SQLite core does not have a remote permission synchronizer.
func SQLiteAuthorization(ctx context.Context, filename string) (digest string, err error) {
	db, err := sqlitedb.Open(ctx, filename)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	hash := sha256.New()
	queries := []string{
		`SELECT json_array(id,name,enabled) FROM iam_tenants ORDER BY id`,
		`SELECT json_array(tenant_id,id,employee_id,enabled) FROM iam_members ORDER BY tenant_id,id`,
		`SELECT json_array(tenant_id,provider,issuer,subject,member_id,enabled) FROM iam_identity_links ORDER BY tenant_id,provider,issuer,subject`,
		`SELECT json_array(tenant_id,member_id,app_id,capability,enabled) FROM iam_operation_grants ORDER BY tenant_id,member_id,app_id,capability`,
	}
	for _, query := range queries {
		// Bind table/field interpretation as well as rows; new schema requires
		// a new supported Version and an explicit review of this projection.
		io.WriteString(hash, query+"\n")
		rows, queryErr := tx.QueryContext(ctx, query)
		if queryErr != nil {
			return "", queryErr
		}
		for rows.Next() {
			var row string
			if queryErr = rows.Scan(&row); queryErr != nil {
				break
			}
			io.WriteString(hash, row+"\n")
		}
		queryErr = errors.Join(queryErr, rows.Err(), rows.Close())
		if queryErr != nil {
			return "", queryErr
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fileArtifact(ctx context.Context, directory, name string) (artifact Artifact, err error) {
	filename := filepath.Join(directory, name)
	info, err := os.Lstat(filename)
	if err != nil {
		return artifact, err
	}
	if !info.Mode().IsRegular() {
		return artifact, ErrInvalid
	}
	file, err := os.Open(filename)
	if err != nil {
		return artifact, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var size int64
	for {
		if err = ctx.Err(); err != nil {
			return artifact, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
			size += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return artifact, readErr
		}
	}
	return Artifact{Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: size}, nil
}

func writeExclusive(filename string, data []byte) (err error) {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if _, err = file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

// BackupSQLite creates a new bundle. Configuration is encrypted; secret values
// stay in the external KeyStore. Authorization must remain stable across capture.
// An error can leave an incomplete directory, which has no valid joint envelope.
func BackupSQLite(ctx context.Context, source, directory string, online bool, configuration []byte, manifest Manifest, keys KeyStore) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(directory) || manifest.Version.Driver != "sqlite" || manifest.Version.Schema != sqlitedb.SchemaVersion {
		return ErrInvalid
	}
	facts, err := SQLiteAuthorization(ctx, source)
	if err != nil {
		return err
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		return err
	}
	databaseDir := filepath.Join(directory, "database")
	if online {
		err = sqlitedb.BackupOnline(ctx, source, databaseDir)
	} else {
		err = sqlitedb.Backup(ctx, source, databaseDir)
	}
	if err != nil {
		return err
	}
	// Verify the snapshot's authorization, not just two observations of source.
	snapshotFacts, err := SQLiteAuthorization(ctx, filepath.Join(databaseDir, "database.sqlite"))
	if err != nil || snapshotFacts != facts {
		return ErrAuthority
	}
	if err = checkAuthority(ctx, func(ctx context.Context) (string, error) { return SQLiteAuthorization(ctx, source) }, facts); err != nil {
		return err
	}
	manifest.AuthorizationSHA256 = facts
	manifest.Database = nil
	for _, name := range []string{"database.sqlite", "manifest.json"} {
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

func readBundle(ctx context.Context, directory string) (Envelope, error) {
	if !filepath.IsAbs(directory) {
		return Envelope{}, ErrInvalid
	}
	root, err := os.Lstat(directory)
	if err != nil {
		return Envelope{}, err
	}
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return Envelope{}, ErrInvalid
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		return Envelope{}, ErrInvalid
	}
	if entries[0].Name() != "database" || !entries[0].IsDir() || entries[1].Name() != "joint.json" || !entries[1].Type().IsRegular() {
		return Envelope{}, ErrInvalid
	}
	file, err := os.Open(filepath.Join(directory, "joint.json"))
	if err != nil {
		return Envelope{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 2*MaxConfiguration+1))
	if err = errors.Join(readErr, file.Close()); err != nil {
		return Envelope{}, err
	}
	envelope, err := Decode(data)
	if err != nil {
		return Envelope{}, err
	}
	if err = envelope.Manifest.validate(); err != nil {
		return Envelope{}, err
	}
	databaseDir := filepath.Join(directory, "database")
	files, err := os.ReadDir(databaseDir)
	if err != nil || len(files) != len(envelope.Manifest.Database) {
		return Envelope{}, ErrInvalid
	}
	for _, expected := range envelope.Manifest.Database {
		actual, err := fileArtifact(ctx, databaseDir, expected.Name)
		if err != nil {
			return Envelope{}, err
		}
		if actual != expected {
			return Envelope{}, ErrIntegrity
		}
	}
	return envelope, nil
}

// RestoreSQLite publishes a new directory containing database.sqlite and the
// exact recovered configuration.json only after all checks. It never starts a
// service or rewrites site paths. Source and target parent directories must be
// exclusively controlled by the operator throughout the recovery window.
func RestoreSQLite(ctx context.Context, directory, target string, expected Version, authority Authority, keys KeyStore) (err error) {
	if !filepath.IsAbs(target) || expected.Driver != "sqlite" || expected.Schema != sqlitedb.SchemaVersion {
		return ErrInvalid
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		return errors.New("joint restore target must not exist")
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
	if !slices.Equal(names, []string{"database.sqlite", "manifest.json"}) {
		return ErrInvalid
	}
	configuration, err := Open(ctx, envelope, expected, authority, keys)
	if err != nil {
		return err
	}
	defer clear(configuration)
	staging, err := os.MkdirTemp(filepath.Dir(target), ".joint-restore-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(staging)) }()
	if err = sqlitedb.Restore(ctx, filepath.Join(directory, "database"), filepath.Join(staging, "database.sqlite")); err != nil {
		return err
	}
	facts, err := SQLiteAuthorization(ctx, filepath.Join(staging, "database.sqlite"))
	if err != nil || facts != envelope.Manifest.AuthorizationSHA256 {
		return ErrAuthority
	}
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
	return os.Rename(staging, target)
}
