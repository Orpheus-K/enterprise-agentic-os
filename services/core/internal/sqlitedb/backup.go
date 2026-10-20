package sqlitedb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type backupManifest struct {
	Format    int       `json:"format"`
	Schema    int       `json:"schema"`
	CreatedAt time.Time `json:"created_at"`
	SHA256    string    `json:"sha256"`
	Bytes     int64     `json:"bytes"`
}

type backupQueries interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func checkBackupDatabase(ctx context.Context, q backupQueries) error {
	var version int
	if err := q.QueryRowContext(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied=1").Scan(&version); err != nil {
		return err
	}
	if version != SchemaVersion {
		return fmt.Errorf("backup requires SQLite schema %d", SchemaVersion)
	}
	var integrity string
	if err := q.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("SQLite integrity check failed")
	}
	rows, err := q.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	broken := rows.Next()
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if broken {
		return errors.New("SQLite foreign key check failed")
	}
	return nil
}

func regularAbsolute(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("absolute file path required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("regular file required; symlinks are not accepted")
	}
	return nil
}

func copyBackup(ctx context.Context, source, target string) (sum string, size int64, err error) {
	if err = regularAbsolute(source); err != nil {
		return
	}
	input, err := os.Open(source)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		if err = ctx.Err(); err != nil {
			return
		}
		var n int
		n, err = input.Read(buffer)
		if n > 0 {
			if _, writeErr := output.Write(buffer[:n]); writeErr != nil {
				err = writeErr
				return
			}
			_, _ = hash.Write(buffer[:n])
			size += int64(n)
		}
		if errors.Is(err, io.EOF) {
			err = nil
			break
		}
		if err != nil {
			return
		}
	}
	err = output.Sync()
	sum = hex.EncodeToString(hash.Sum(nil))
	return
}

// Backup is a stopped-writer operation for the default DELETE journal mode.
// The exclusive lock fences SQLite writers for the entire file copy.
func Backup(ctx context.Context, source, directory string) (err error) {
	if err = regularAbsolute(source); err != nil {
		return
	}
	if !filepath.IsAbs(directory) {
		return errors.New("absolute backup directory required")
	}
	db, err := connect(ctx, source, "rw")
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if _, err = conn.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		return
	}
	if _, err = conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return errors.New("SQLite busy; stop all instances before backup")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, rollbackErr := conn.ExecContext(cleanup, "ROLLBACK")
		err = errors.Join(err, rollbackErr)
	}()
	var journal string
	if err = conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return
	}
	if journal != "delete" {
		return errors.New("offline backup requires DELETE journal mode; WAL is not supported")
	}
	if err = checkBackupDatabase(ctx, conn); err != nil {
		return
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		return
	}
	file := filepath.Join(directory, "database.sqlite")
	sum, size, err := copyBackup(ctx, source, file)
	if err != nil {
		return
	}
	if err = validateBackupCopy(ctx, file); err != nil {
		return
	}
	manifest := backupManifest{Format: 1, Schema: SchemaVersion, CreatedAt: time.Now().UTC(), SHA256: sum, Bytes: size}
	out, err := os.OpenFile(filepath.Join(directory, "manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if err = json.NewEncoder(out).Encode(manifest); err != nil {
		return
	}
	return out.Sync()
}

func validateBackupCopy(ctx context.Context, path string) (err error) {
	db, err := connect(ctx, path, "ro")
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return checkBackupDatabase(ctx, db)
}

// Restore never overwrites a database and never upgrades its schema.
func Restore(ctx context.Context, directory, target string) (err error) {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(target) {
		return errors.New("absolute restore paths required")
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	if err = regularAbsolute(manifestPath); err != nil {
		return
	}
	raw, err := os.Open(manifestPath)
	if err != nil {
		return
	}
	var manifest backupManifest
	decoder := json.NewDecoder(io.LimitReader(raw, 64*1024))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&manifest)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = errors.New("invalid backup manifest trailer")
	}
	err = errors.Join(err, raw.Close())
	digest, decodeErr := hex.DecodeString(manifest.SHA256)
	if err != nil {
		return
	}
	if manifest.Format != 1 || manifest.Schema != SchemaVersion || manifest.Bytes < 1 || manifest.CreatedAt.IsZero() || decodeErr != nil || len(digest) != sha256.Size {
		return errors.New("unsupported or invalid backup manifest")
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		return errors.New("restore target must not exist")
	}
	// Validate in a private sibling before creating the target with O_EXCL.
	staging, err := os.MkdirTemp(filepath.Dir(target), ".sqlite-restore-")
	if err != nil {
		return
	}
	stageFile := filepath.Join(staging, "database.sqlite")
	defer func() {
		if removeErr := os.Remove(stageFile); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
		err = errors.Join(err, os.Remove(staging))
	}()
	sum, size, err := copyBackup(ctx, filepath.Join(directory, "database.sqlite"), stageFile)
	if err != nil {
		return
	}
	if sum != manifest.SHA256 || size != manifest.Bytes {
		return errors.New("backup checksum or size mismatch")
	}
	if err = validateBackupCopy(ctx, stageFile); err != nil {
		return
	}
	input, err := os.Open(stageFile)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer func() {
		err = errors.Join(err, output.Close())
		if err != nil {
			err = errors.Join(err, os.Remove(target))
		}
	}()
	if _, err = io.Copy(output, input); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	return output.Sync()
}
