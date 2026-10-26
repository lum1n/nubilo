package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/integrity"
	"nubilo/internal/store"
)

// v1Fixture builds a single-user (schema v1) database with one device, an
// app password, a calendar with color, a folder tree, two blobs (one only
// referenced as a Live Photo movie) and a pending push idempotency row.
func v1Fixture(t *testing.T) (dir string, key []byte, ids map[string]string) {
	t.Helper()
	dir = t.TempDir()
	master, _ := ncrypto.GenerateMasterKey()
	key, _ = ncrypto.DeriveKey(master, ncrypto.BlobKeyInfo)
	schema, err := os.ReadFile(filepath.Join("testdata", "v1_schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	ids = map[string]string{
		"dev": "01J000000000000000000DEV01", "cal": "01J000000000000000000CAL01",
		"files": "01J00000000000000000FILES1", "sub": "01J000000000000000000SUB01",
		"subsub": "01J0000000000000000SUBSUB", "photos": "01J00000000000000000PHOTOS",
		"obj": "01J000000000000000000OBJ01", "photo": "01J0000000000000000PHOTO01",
	}
	put := func(payload []byte) string {
		sum := ncrypto.SHA256Hex(payload)
		enc, err := ncrypto.EncryptBlob(key, payload)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "b", sum[:2], sum[2:4], sum)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, enc, 0o600); err != nil {
			t.Fatal(err)
		}
		return sum
	}
	doc := put([]byte("hello v1"))
	jpg := put([]byte("not really a jpeg"))
	mov := put([]byte("live movie"))
	ids["doc"], ids["jpg"], ids["mov"] = doc, jpg, mov
	stmts := []string{
		`INSERT INTO schema_meta(version) VALUES (1)`,
		`INSERT INTO devices(id, name, public_key, role, permissions, created_at) VALUES ('` + ids["dev"] + `', 'mac', randomblob(32), 'agent', '{}', 1)`,
		`INSERT INTO app_passwords(id, device_id, hash, salt, scope, created_at) VALUES ('01J0000000000000000000PW01', '` + ids["dev"] + `', x'00', x'00', 'caldav', 1)`,
		`INSERT INTO collections(id, kind, name, parent_id, metadata, metadata_hash, created_at, updated_at) VALUES
			('` + ids["cal"] + `', 'calendar', 'Personal', NULL, '{"color":"#FF0000FF","order":3}', 'h', 1, 1),
			('` + ids["files"] + `', 'files', 'Files', NULL, '{}', 'h', 1, 1),
			('` + ids["sub"] + `', 'files', 'Docs', '` + ids["files"] + `', '{}', 'h', 2, 2),
			('` + ids["subsub"] + `', 'files', 'Deep', '` + ids["sub"] + `', '{}', 'h', 3, 3),
			('` + ids["photos"] + `', 'photos', 'Photos', NULL, '{}', 'h', 1, 1),
			('01J0000000000000000DUPE01', 'files', 'Files', NULL, '{}', 'h', 9, 9)`,
		`INSERT INTO blobs(id, size, refcount, created_at) VALUES ('` + doc + `', 8, 1, 1), ('` + jpg + `', 17, 1, 1), ('` + mov + `', 10, 0, 1)`,
		`INSERT INTO objects(id, collection_id, kind, revision, content_hash, metadata_hash, blob_id, size, metadata, created_at, updated_at) VALUES
			('` + ids["obj"] + `', '` + ids["subsub"] + `', 'file', 1, '` + doc + `', 'h', '` + doc + `', 8, '{"name":"a.txt"}', 1, 1),
			('` + ids["photo"] + `', '` + ids["photos"] + `', 'photo', 1, '` + jpg + `', 'h', '` + jpg + `', 17, '{"name":"p.jpg","live_pair_hash":"` + mov + `"}', 1, 1)`,
		`INSERT INTO journal(object_id, collection_id, op, revision, content_hash, metadata_hash, device_id, ts) VALUES
			('` + ids["obj"] + `', '` + ids["subsub"] + `', 'create', 1, '` + doc + `', 'h', '` + ids["dev"] + `', 1),
			('` + ids["photo"] + `', '` + ids["photos"] + `', 'create', 1, '` + jpg + `', 'h', '` + ids["dev"] + `', 1)`,
		`INSERT INTO push_idempotency(key, device_id, result, created_at) VALUES ('k1', '` + ids["dev"] + `', '[]', 1)`,
		`INSERT INTO blob_uploads(blob_id, device_id, created_at) VALUES ('` + doc + `', '` + ids["dev"] + `', 1)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	return dir, key, ids
}

func TestMigrateV1ToV2(t *testing.T) {
	dir, key, ids := v1Fixture(t)
	st, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if v, err := st.SchemaVersion(ctx); err != nil || v != 2 {
		t.Fatalf("version %d %v", v, err)
	}
	var owner, role string
	if err := st.DB.QueryRow(`SELECT id, role FROM users WHERE username = 'owner'`).Scan(&owner, &role); err != nil {
		t.Fatal(err)
	}
	if role != "admin" {
		t.Fatalf("owner role %s", role)
	}
	one := func(q string, args ...any) string {
		t.Helper()
		var s sql.NullString
		if err := st.DB.QueryRow(q, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return s.String
	}
	if got := one(`SELECT user_id FROM devices WHERE id = ?`, ids["dev"]); got != owner {
		t.Fatalf("device user %q", got)
	}
	if got := one(`SELECT user_id FROM app_passwords`); got != owner {
		t.Fatalf("app password user %q", got)
	}
	var unowned int
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM collections WHERE owner_user_id IS NULL OR owner_user_id <> ?`, owner).Scan(&unowned)
	if unowned != 0 {
		t.Fatalf("%d collections not owned by owner", unowned)
	}
	for _, c := range []string{"sub", "subsub"} {
		if got := one(`SELECT root_id FROM collections WHERE id = ?`, ids[c]); got != ids["files"] {
			t.Fatalf("%s root %q", c, got)
		}
	}
	if got := one(`SELECT root_id FROM collections WHERE id = ?`, ids["cal"]); got != ids["cal"] {
		t.Fatalf("root of root %q", got)
	}
	if got := one(`SELECT name FROM collections WHERE id = '01J0000000000000000DUPE01'`); got == "Files" {
		t.Fatal("duplicate root name not renamed")
	}
	for _, h := range []string{ids["doc"], ids["jpg"], ids["mov"]} {
		if ok, _ := st.HasBlobRef(ctx, owner, h); !ok {
			t.Fatalf("missing blob ref %s", h)
		}
	}
	if got := one(`SELECT color FROM collection_prefs WHERE user_id = ? AND collection_id = ?`, owner, ids["cal"]); got != "#FF0000FF" {
		t.Fatalf("pref color %q", got)
	}
	if got := one(`SELECT key FROM push_idempotency`); got != ids["dev"]+":k1" {
		t.Fatalf("idempotency key %q", got)
	}
	if got := one(`SELECT refcount FROM blobs WHERE id = ?`, ids["mov"]); got != "1" {
		t.Fatalf("live movie refcount %q", got)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'blob_uploads'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("blob_uploads still present (%d %v)", n, err)
	}
	issues, err := integrity.Check(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("verify after migration: %v", issues)
	}
	// Re-opening is a no-op.
	st.Close()
	st2, err := store.Open(dir, filepath.Join(dir, "m.db"), filepath.Join(dir, "b"), filepath.Join(dir, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	_ = st2.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 1 {
		t.Fatalf("users after reopen %d", n)
	}
}

func TestFreshDatabaseHasNoUsers(t *testing.T) {
	st := open(t)
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fresh db users %d %v", n, err)
	}
	if v, _ := st.SchemaVersion(context.Background()); v != 2 {
		t.Fatalf("fresh version %d", v)
	}
}

func TestOpenPathWithHash(t *testing.T) {
	ncrypto.Argon2Memory = 8
	base := filepath.Join(t.TempDir(), "a#b?c")
	key := make([]byte, 32)
	st, err := store.Open(base, filepath.Join(base, "m.db"), filepath.Join(base, "b"), filepath.Join(base, "t"), key, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := os.Stat(filepath.Join(base, "m.db")); err != nil {
		t.Fatalf("database not created at the escaped path: %v", err)
	}
}
