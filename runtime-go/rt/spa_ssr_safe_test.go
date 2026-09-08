//go:build !js

package rt

import (
	"os"
	"path/filepath"
	"testing"
)

// Fix 2 soundness: a DESTRUCTIVE kernel run WHILE an SSR settle is in flight must
// self-suppress — it returns a classified Err and never touches the store, so a
// GET can never mutate even when the settled command batches a write alongside a
// read. Outside a settle the same kernel writes normally.
func TestSsrSettle_SuppressesDestructiveWrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(target, []byte("seed"), 0644); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// The File.writeFile kernel yields a task thunk; running it performs the write.
	task, ok := File_writeFile(target, "mutated").(func() any)
	if !ok {
		t.Fatalf("File_writeFile did not return a task thunk")
	}

	// (1) Inside a settle: the write is suppressed to an Err, file unchanged.
	enterSsrSettle()
	if !InSsrSettle() {
		t.Fatal("InSsrSettle must be true after enterSsrSettle")
	}
	res := task()
	if !isErrResult(res) {
		t.Fatalf("a suppressed write must yield Err, got %#v", res)
	}
	exitSsrSettle()
	if got, _ := os.ReadFile(target); string(got) != "seed" {
		t.Fatalf("suppressed write must NOT touch the file, got %q", string(got))
	}

	// (2) Outside a settle: the same task writes normally.
	if InSsrSettle() {
		t.Fatal("InSsrSettle must be false after exitSsrSettle")
	}
	if res := task(); isErrResult(res) {
		t.Fatalf("outside a settle the write must succeed, got Err: %#v", res)
	}
	if got, _ := os.ReadFile(target); string(got) != "mutated" {
		t.Fatalf("outside a settle the write must land, got %q", string(got))
	}
}

// Fix 2 completeness (Judge finding 3): the suppressor must also cover the
// EAGER file mutators File.copy / File.rename and the Store write path
// (storeWriteResult), which reach the effect WITHOUT going through the already
// guarded Db_exec / File_writeFile. Before this an onNavigate / init command
// that batched a `Store.insert` or a `File.rename` mutated during a GET SSR.
func TestSsrSettle_SuppressesCopyRenameAndStoreWrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("payload"), 0644); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// (1) File.copy inside a settle: suppressed, destination never created.
	enterSsrSettle()
	if res := File_copy(src, filepath.Join(dir, "copy.txt")); !isErrResult(res) {
		t.Fatalf("File.copy in a settle must yield Err, got %#v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "copy.txt")); !os.IsNotExist(err) {
		t.Fatalf("suppressed File.copy must not create the destination")
	}
	// (2) File.rename inside a settle: suppressed, source untouched.
	if res := File_rename(src, filepath.Join(dir, "moved.txt")); !isErrResult(res) {
		t.Fatalf("File.rename in a settle must yield Err, got %#v", res)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("suppressed File.rename must leave the source in place: %v", err)
	}
	exitSsrSettle()

	// (3) The Store write path (storeWriteResult, reached by Store.insert /
	// upsert-returning) inside a settle: suppressed, no row written. Outside a
	// settle the same write lands. Call storeWriteResult directly — that is the
	// exact site the guard was added, without the JSON-record machinery.
	dbPath := filepath.Join(dir, "store.db")
	connRes, ok := forceAuthTask(Db_connect(dbPath)).(SkyResult[any, any])
	if !ok || connRes.Tag != 0 {
		t.Fatalf("Db_connect failed: %#v", connRes)
	}
	conn, ok := connRes.OkValue.(*SkyDb)
	if !ok {
		t.Fatalf("Db_connect Ok is not *SkyDb: %T", connRes.OkValue)
	}
	if r := AnyTaskRun(Db_exec(conn, "CREATE TABLE items (id TEXT PRIMARY KEY)", []any{})); isErrResult(r) {
		t.Fatalf("create table: %#v", r)
	}
	insertSql := "INSERT INTO items (id) VALUES (?)"

	enterSsrSettle()
	if res := storeWriteResult(conn, "Store.insert", insertSql, []any{"a1"}, "", "text"); !isErrResult(res) {
		t.Fatalf("storeWriteResult in a settle must yield Err, got %#v", res)
	}
	exitSsrSettle()
	rows := AsList(AnyTaskRun(Db_query(conn, "SELECT id FROM items", []any{})))
	if len(rows) != 0 {
		t.Fatalf("suppressed store write must write no row, found %d", len(rows))
	}

	// Outside a settle the same write lands.
	if res := storeWriteResult(conn, "Store.insert", insertSql, []any{"a1"}, "", "text"); isErrResult(res) {
		t.Fatalf("outside a settle store write must succeed, got Err: %#v", res)
	}
	rows2 := AsList(AnyTaskRun(Db_query(conn, "SELECT id FROM items", []any{})))
	if len(rows2) != 1 {
		t.Fatalf("outside a settle store write must write one row, found %d", len(rows2))
	}
}
