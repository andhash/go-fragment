package journal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateIsExclusiveAndMode0600(t *testing.T) {
	j := openTest(t)
	row := sample("order-1001")
	if err := j.Create(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if err := j.Create(context.Background(), row); err != ErrExists {
		t.Fatalf("second create: %v", err)
	}
	path := filepath.Join(j.root, "journal", fileName(row.Key))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Join(j.root, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", dir.Mode().Perm())
	}
}

func TestTempLeftBehindKeepsPreviousFile(t *testing.T) {
	j := openTest(t)
	row := sample("order-1001")
	if err := j.Create(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if _, err := writeTemp(filepath.Join(j.root, "journal"), fileName(row.Key), []byte("{")); err != nil {
		t.Fatal(err)
	}
	got, err := j.Load(context.Background(), row.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "preparing" || got.Version != 1 {
		t.Fatalf("damaged row %+v", got)
	}
}

func TestReqIDConflictAndZeroTime(t *testing.T) {
	j := openTest(t)
	first := sample("a")
	first.ReqID = "req-same"
	if err := j.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	cur, err := j.Load(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Time{}
	cur.ValidUntil = &zero
	if err := j.Store(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	cur, err = j.Load(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if cur.ValidUntil != nil {
		t.Fatalf("zero time stored: %v", cur.ValidUntil)
	}
	second := sample("b")
	second.ReqID = "req-same"
	if err := j.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	sec, err := j.Load(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	sec.ReqID = "req-same"
	if err := j.Store(context.Background(), sec); err != ErrReqConflict {
		t.Fatalf("req conflict: %v", err)
	}
}

func TestUnknownSchema(t *testing.T) {
	j := openTest(t)
	row := sample("a")
	if err := j.Create(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(j.root, "journal", fileName("a"))
	if err := os.WriteFile(path, []byte(`{"schema":2,"key":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Load(context.Background(), "a"); err != ErrSchema {
		t.Fatalf("schema: %v", err)
	}
}

func openTest(t *testing.T) *File {
	t.Helper()
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func sample(key string) Row {
	now := time.Unix(1_700_000_000, 0).UTC()
	return Row{
		Key: key, Product: "stars", Username: "alice", Quantity: 50,
		State: "preparing", ShowSender: true, InitAttempts: 1,
		CreatedAt: now, UpdatedAt: now,
		Messages: []byte("[]"), Breakdown: []byte("{}"),
	}
}
