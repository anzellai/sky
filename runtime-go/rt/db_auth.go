package rt

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"unicode"

	"github.com/golang-jwt/jwt/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // Postgres driver registered as "pgx"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// ═══════════════════════════════════════════════════════════
// Std.Db — SQLite (pure Go, no CGO)
// ═══════════════════════════════════════════════════════════

// SkyDb is an opaque handle over a *sql.DB.
type SkyDb struct {
	conn   *sql.DB
	name   string
	driver string // "sqlite" or "pgx"
}

// placeholder returns "?" for SQLite, "$N" for Postgres.
func (d *SkyDb) placeholder(i int) string {
	if d.driver == "pgx" {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// placeholders produces a joined list of placeholders "$1,$2,$3" or "?,?,?"
func (d *SkyDb) placeholders(n int) string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = d.placeholder(i + 1)
	}
	return strings.Join(out, ",")
}

// quoteIdent returns a safely-quoted SQL identifier (table or column name).
// Rejects anything that isn't a plain ASCII identifier to prevent SQL injection
// via table/column name strings. Returns "" if invalid — callers should
// short-circuit with an Err in that case.
// Both SQLite and Postgres support ANSI-standard double-quoted identifiers.
func quoteIdent(s string) string {
	if !isSafeIdent(s) {
		return ""
	}
	return "\"" + s + "\""
}

// isSafeIdent: first rune must be a Unicode letter or '_'; remainder must be
// letters, digits, or '_'. Bounded to 63 bytes (Postgres identifier limit).
// Rejects whitespace, quotes, semicolons, control chars, punctuation — anything
// that could break out of the identifier context when quoted. Embedded double
// quotes are also rejected (we do not try to escape them; reject instead).
func isSafeIdent(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_':
			// always OK
		case unicode.IsLetter(c):
			// Unicode letter OK (pL)
		case i > 0 && unicode.IsDigit(c):
			// Unicode digit OK after first rune
		default:
			return false
		}
	}
	return true
}

// safeTable wraps a table identifier after validation; returns "" if invalid.
func safeTable(v any) string {
	return quoteIdent(fmt.Sprintf("%v", v))
}

var (
	dbRegistry   = map[string]*SkyDb{}
	dbRegistryMu sync.Mutex
)

// Db.connect : String -> Result String Db
// Accepts:
//   ":memory:"             — in-memory SQLite
//   "/path/file.db"        — file-backed SQLite
//   "postgres://user:pw@host:5432/dbname?sslmode=disable"
//   "postgresql://..."     — equivalent
//   "host=... user=... ..." — libpq-style keyword connection string
func Db_connect(path any) any {
	p := fmt.Sprintf("%v", path)
	dbRegistryMu.Lock()
	defer dbRegistryMu.Unlock()
	if existing, ok := dbRegistry[p]; ok {
		return Ok[any, any](existing)
	}
	driver, dsn := detectDriver(p)
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return Err[any, any](ErrIo("db connect: " + err.Error()))
	}
	if err := conn.Ping(); err != nil {
		return Err[any, any](ErrIo("db ping: " + err.Error()))
	}
	db := &SkyDb{conn: conn, name: p, driver: driver}
	dbRegistry[p] = db
	return Ok[any, any](db)
}

// detectDriver returns the (driverName, dsn) pair for a connection string.
func detectDriver(s string) (string, string) {
	ss := strings.TrimSpace(s)
	low := strings.ToLower(ss)
	switch {
	case strings.HasPrefix(low, "postgres://"),
		strings.HasPrefix(low, "postgresql://"):
		return "pgx", ss
	case strings.Contains(low, "host=") && strings.Contains(low, "user="):
		// libpq keyword form — treat as Postgres
		return "pgx", ss
	default:
		return "sqlite", ss
	}
}

// Db.open — alias of connect. Accepts either:
//   Db.open path               (1 arg)
//   Db.open driver path        (2 args; driver arg informational, path is used)
func Db_open(args ...any) any {
	switch len(args) {
	case 1:
		return Db_connect(args[0])
	case 2:
		return Db_connect(args[1])
	default:
		return Err[any, any](ErrInvalidInput("Db.open: expected 1 or 2 args"))
	}
}

// Db.execRaw : Db -> String -> Result String Int
// Raw SQL without parameter binding. For DDL like CREATE TABLE.
func Db_execRaw(db any, query any) any {
	return Db_exec(db, query, []any{})
}

// Db.close : Db -> Result String ()
func Db_close(db any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.close: not a Db"))
	}
	if err := d.conn.Close(); err != nil {
		return Err[any, any](ErrFfi(err.Error()))
	}
	return Ok[any, any](struct{}{})
}

// Db.exec : Db -> String -> List any -> Result String Int
// Runs a statement that doesn't return rows. Returns rows affected.
func Db_exec(db any, query any, args any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.exec: not a Db"))
	}
	argList := asList(args)
	goArgs := make([]any, len(argList))
	for i, a := range argList {
		goArgs[i] = a
	}
	res, err := d.conn.Exec(fmt.Sprintf("%v", query), goArgs...)
	if err != nil {
		return Err[any, any](ErrIo("db.exec: " + err.Error()))
	}
	n, _ := res.RowsAffected()
	return Ok[any, any](int(n))
}

// Db.query : Db -> String -> List any -> Result String (List (Dict String any))
// Returns each row as a Dict of column name → value.
func Db_query(db any, query any, args any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.query: not a Db"))
	}
	argList := asList(args)
	goArgs := make([]any, len(argList))
	for i, a := range argList {
		goArgs[i] = a
	}
	rows, err := d.conn.Query(fmt.Sprintf("%v", query), goArgs...)
	if err != nil {
		return Err[any, any](ErrIo("db.query: " + err.Error()))
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return Err[any, any](ErrIo("db.query columns: " + err.Error()))
	}
	var out []any
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Err[any, any](ErrIo("db.query scan: " + err.Error()))
		}
		rowDict := map[string]any{}
		for i, c := range cols {
			rowDict[c] = normaliseSqlValue(raw[i])
		}
		out = append(out, rowDict)
	}
	return Ok[any, any](out)
}

// Db.queryDecode : Db -> String -> List any -> JsonDecoder a -> Result String (List a)
// Runs a query then decodes each row as a JSON-ish object.
func Db_queryDecode(db any, query any, args any, decoder any) any {
	resp := Db_query(db, query, args)
	r, ok := resp.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return resp
	}
	rows := r.OkValue.([]any)
	d, isDec := decoder.(JsonDecoder)
	if !isDec {
		return Ok[any, any](rows)
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		result := d.run(row)
		sr, ok := result.(SkyResult[any, any])
		if !ok {
			return Err[any, any](ErrDecode("decode error"))
		}
		if sr.Tag != 0 {
			return result
		}
		out = append(out, sr.OkValue)
	}
	return Ok[any, any](out)
}

// Db.insertRow : Db -> String -> Dict String any -> Result String Int
// Returns the last-insert id.
// Table and column names are validated as plain identifiers then quoted;
// values go through parameter placeholders. No unvalidated string interpolation.
func Db_insertRow(db any, table any, row any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.insertRow: not a Db"))
	}
	m, ok := row.(map[string]any)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.insertRow: row must be a Dict"))
	}
	qTable := safeTable(table)
	if qTable == "" {
		return Err[any, any](ErrInvalidInput("db.insertRow: invalid table name"))
	}
	var cols []string
	var vals []any
	for k, v := range m {
		qc := quoteIdent(k)
		if qc == "" {
			return Err[any, any](ErrInvalidInput("db.insertRow: invalid column name: " + k))
		}
		cols = append(cols, qc)
		vals = append(vals, v)
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		qTable, strings.Join(cols, ","), d.placeholders(len(cols)))
	if d.driver == "pgx" {
		// Postgres doesn't support LastInsertId — use RETURNING id
		q += " RETURNING id"
		var id int64
		if err := d.conn.QueryRow(q, vals...).Scan(&id); err != nil {
			return Err[any, any](ErrIo("db.insertRow: " + err.Error()))
		}
		return Ok[any, any](int(id))
	}
	res, err := d.conn.Exec(q, vals...)
	if err != nil {
		return Err[any, any](ErrIo("db.insertRow: " + err.Error()))
	}
	id, _ := res.LastInsertId()
	return Ok[any, any](int(id))
}

// Db.getById : Db -> String -> Int -> Result String (Dict String any)
func Db_getById(db any, table any, id any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.getById: not a Db"))
	}
	qTable := safeTable(table)
	if qTable == "" {
		return Err[any, any](ErrInvalidInput("db.getById: invalid table name"))
	}
	q := fmt.Sprintf("SELECT * FROM %s WHERE id = %s LIMIT 1", qTable, d.placeholder(1))
	result := Db_query(db, q, []any{AsInt(id)})
	r, ok := result.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return result
	}
	rows := r.OkValue.([]any)
	if len(rows) == 0 {
		return Err[any, any](ErrNotFound())
	}
	return Ok[any, any](rows[0])
}

// Db.updateById : Db -> String -> Int -> Dict String any -> Result String Int
func Db_updateById(db any, table any, id any, row any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.updateById: not a Db"))
	}
	m, ok := row.(map[string]any)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.updateById: row must be a Dict"))
	}
	qTable := safeTable(table)
	if qTable == "" {
		return Err[any, any](ErrInvalidInput("db.updateById: invalid table name"))
	}
	var sets []string
	var vals []any
	i := 1
	for k, v := range m {
		qc := quoteIdent(k)
		if qc == "" {
			return Err[any, any](ErrInvalidInput("db.updateById: invalid column name: " + k))
		}
		sets = append(sets, qc+" = "+d.placeholder(i))
		vals = append(vals, v)
		i++
	}
	vals = append(vals, AsInt(id))
	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = %s", qTable, strings.Join(sets, ","), d.placeholder(i))
	res, err := d.conn.Exec(q, vals...)
	if err != nil {
		return Err[any, any](ErrIo("db.updateById: " + err.Error()))
	}
	n, _ := res.RowsAffected()
	return Ok[any, any](int(n))
}

// Db.deleteById : Db -> String -> Int -> Result String Int
func Db_deleteById(db any, table any, id any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.deleteById: not a Db"))
	}
	qTable := safeTable(table)
	if qTable == "" {
		return Err[any, any](ErrInvalidInput("db.deleteById: invalid table name"))
	}
	q := fmt.Sprintf("DELETE FROM %s WHERE id = %s", qTable, d.placeholder(1))
	res, err := d.conn.Exec(q, AsInt(id))
	if err != nil {
		return Err[any, any](ErrIo("db.deleteById: " + err.Error()))
	}
	n, _ := res.RowsAffected()
	return Ok[any, any](int(n))
}

// Db.findWhere — audit P1-3: renamed to Db_unsafeFindWhere at the Sky
// level. The old name remains as a thin alias for compiled binaries
// in sky-out/* dirs that haven't been regenerated yet. All new code
// must use Db.findOneByField / Db.findManyByField / Db.findByConditions
// (parameterised, table + column names validated) or explicit
// Db.unsafeFindWhere with an injection-risk comment.
func Db_findWhere(db any, table any, whereClause any, args any) any {
	return Db_unsafeFindWhere(db, table, whereClause, args)
}

// Db.unsafeFindWhere : Db -> String -> String -> List any -> Result Error (List Row)
// Raw-SQL WHERE clause. Table is validated and quoted; arguments go
// through parameter placeholders; the WHERE clause text itself is
// NOT escaped. NEVER build the clause from untrusted input. Use
// Db.findOneByField / Db.findManyByField / Db.findByConditions when
// the predicate is a field/value comparison — those are parameterised
// end-to-end and safe with any input.
func Db_unsafeFindWhere(db any, table any, whereClause any, args any) any {
	qTable := safeTable(table)
	if qTable == "" {
		return Err[any, any](ErrInvalidInput("db.unsafeFindWhere: invalid table name"))
	}
	q := fmt.Sprintf("SELECT * FROM %s WHERE %v", qTable, whereClause)
	return Db_query(db, q, args)
}

// Db.findOneByField : Db -> String -> String -> any -> Result Error (Maybe Row)
// Returns the first row where `field = value`. Table and column names
// go through safeTable / quoteIdent — unsafe characters reject; the
// value is always bound as a SQL parameter. Audit P1-3.
func Db_findOneByField(db any, table any, field any, value any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.findOneByField: not a Db"))
	}
	qTable := safeTable(table)
	if qTable == "" {
		return Err[any, any](ErrInvalidInput("db.findOneByField: invalid table name"))
	}
	qField := quoteIdent(fmt.Sprintf("%v", field))
	if qField == "" {
		return Err[any, any](ErrInvalidInput("db.findOneByField: invalid column name"))
	}
	q := fmt.Sprintf("SELECT * FROM %s WHERE %s = %s LIMIT 1", qTable, qField, d.placeholder(1))
	res := Db_query(db, q, []any{value})
	sr, ok := res.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		return res
	}
	rows, ok := sr.OkValue.([]any)
	if !ok {
		return Err[any, any](ErrIo("db.findOneByField: unexpected result shape"))
	}
	if len(rows) == 0 {
		return Ok[any, any](Nothing[any]())
	}
	return Ok[any, any](Just[any](rows[0]))
}

// Db.findManyByField : Db -> String -> String -> any -> Result Error (List Row)
// Returns all rows where `field = value`. Same safety properties as
// findOneByField — identifiers validated, value bound as a parameter.
func Db_findManyByField(db any, table any, field any, value any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.findManyByField: not a Db"))
	}
	qTable := safeTable(table)
	if qTable == "" {
		return Err[any, any](ErrInvalidInput("db.findManyByField: invalid table name"))
	}
	qField := quoteIdent(fmt.Sprintf("%v", field))
	if qField == "" {
		return Err[any, any](ErrInvalidInput("db.findManyByField: invalid column name"))
	}
	q := fmt.Sprintf("SELECT * FROM %s WHERE %s = %s", qTable, qField, d.placeholder(1))
	return Db_query(db, q, []any{value})
}

// Db.findByConditions : Db -> String -> Dict String any -> Result Error (List Row)
// Returns all rows matching every column = value in the conditions
// map (AND across entries). Column names validated; all values bound
// as parameters. The condition map's iteration order is Go-random
// but deterministic for any given map, so the emitted SQL is
// consistent across rows — no ordering surprises.
func Db_findByConditions(db any, table any, conditions any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.findByConditions: not a Db"))
	}
	qTable := safeTable(table)
	if qTable == "" {
		return Err[any, any](ErrInvalidInput("db.findByConditions: invalid table name"))
	}
	m, ok := conditions.(map[string]any)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.findByConditions: conditions must be a Dict String any"))
	}
	if len(m) == 0 {
		q := fmt.Sprintf("SELECT * FROM %s", qTable)
		return Db_query(db, q, []any{})
	}
	clauses := make([]string, 0, len(m))
	args := make([]any, 0, len(m))
	i := 1
	for col, val := range m {
		qc := quoteIdent(col)
		if qc == "" {
			return Err[any, any](ErrInvalidInput("db.findByConditions: invalid column name: " + col))
		}
		clauses = append(clauses, fmt.Sprintf("%s = %s", qc, d.placeholder(i)))
		args = append(args, val)
		i++
	}
	q := fmt.Sprintf("SELECT * FROM %s WHERE %s", qTable, strings.Join(clauses, " AND "))
	return Db_query(db, q, args)
}

// Db.withTransaction : Db -> (Db -> Result String a) -> Result String a
func Db_withTransaction(db any, body any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("db.withTransaction: not a Db"))
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return Err[any, any](ErrFfi("tx begin: " + err.Error()))
	}
	// We don't have a separate tx handle type yet — pass the db. The semantics
	// are conservative: if body returns Err, roll back. Otherwise commit.
	fn, ok := body.(func(any) any)
	if !ok {
		tx.Rollback()
		return Err[any, any](ErrInvalidInput("withTransaction: body is not a function"))
	}
	result := fn(db)
	if sr, ok := result.(SkyResult[any, any]); ok && sr.Tag == 0 {
		if err := tx.Commit(); err != nil {
			return Err[any, any](ErrFfi("tx commit: " + err.Error()))
		}
		return result
	}
	tx.Rollback()
	return result
}

// normaliseSqlValue unwraps driver values like []byte → string, etc.
func normaliseSqlValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case int64:
		return int(x)
	case nil:
		return Nothing[any]()
	default:
		return v
	}
}

// ═══════════════════════════════════════════════════════════
// Std.Auth — bcrypt password hashing + JWT tokens
// ═══════════════════════════════════════════════════════════

// Auth.hashPassword : String -> Result String String
// Uses bcrypt at cost 12 — higher than Go's DefaultCost (10). Takes ~200ms on
// a typical server; calibrated to resist offline GPU brute force while staying
// acceptable on a login path.
// Callers can use hashPasswordCost for custom cost.
func Auth_hashPassword(pw any) any {
	return Auth_hashPasswordCost(pw, 12)
}

// Auth.hashPasswordCost : String -> Int -> Result String String
func Auth_hashPasswordCost(pw any, cost any) any {
	s := fmt.Sprintf("%v", pw)
	c := AsInt(cost)
	if c < bcrypt.MinCost {
		c = bcrypt.MinCost
	}
	if c > bcrypt.MaxCost {
		c = bcrypt.MaxCost
	}
	if len(s) < 8 {
		return Err[any, any](ErrInvalidInput("hashPassword: password must be at least 8 characters"))
	}
	// bcrypt truncates at 72 bytes — reject overlong passwords explicitly
	// to avoid the silent-truncation footgun where pw[0:72] collides.
	if len(s) > 72 {
		return Err[any, any](ErrInvalidInput("hashPassword: password longer than 72 bytes (use a KDF like argon2 for long inputs)"))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(s), c)
	if err != nil {
		return Err[any, any](ErrFfi("hashPassword: " + err.Error()))
	}
	return Ok[any, any](string(hash))
}

// Auth.passwordStrength : String -> Result String ()
// Enforces a safe baseline: ≥8 chars, ≤72 bytes, at least one letter + one digit.
// Returns Ok () if strong enough, Err describing what's missing otherwise.
func Auth_passwordStrength(pw any) any {
	s := fmt.Sprintf("%v", pw)
	if len(s) < 8 {
		return Err[any, any](ErrInvalidInput("password must be at least 8 characters"))
	}
	if len(s) > 72 {
		return Err[any, any](ErrInvalidInput("password longer than 72 bytes (bcrypt limit)"))
	}
	hasLetter := false
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	if !hasLetter {
		return Err[any, any](ErrInvalidInput("password must contain a letter"))
	}
	if !hasDigit {
		return Err[any, any](ErrInvalidInput("password must contain a digit"))
	}
	return Ok[any, any](struct{}{})
}

// Auth.verifyPassword : String -> String -> Bool
// (password, hash) — returns True on match
func Auth_verifyPassword(pw any, hashed any) any {
	err := bcrypt.CompareHashAndPassword(
		[]byte(fmt.Sprintf("%v", hashed)),
		[]byte(fmt.Sprintf("%v", pw)),
	)
	return err == nil
}

// Auth.signToken : String -> Dict String any -> Int -> Result String String
// (secret, claims, expirySeconds)
func Auth_signToken(secret any, claims any, expirySeconds any) any {
	m := map[string]any{}
	if c, ok := claims.(map[string]any); ok {
		for k, v := range c {
			m[k] = v
		}
	}
	exp := AsInt(expirySeconds)
	m["exp"] = time.Now().Add(time.Duration(exp) * time.Second).Unix()
	m["iat"] = time.Now().Unix()

	mc := jwt.MapClaims{}
	for k, v := range m {
		mc[k] = v
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, mc)
	signed, err := token.SignedString([]byte(fmt.Sprintf("%v", secret)))
	if err != nil {
		return Err[any, any](ErrFfi("signToken: " + err.Error()))
	}
	return Ok[any, any](signed)
}

// Auth.verifyToken : String -> String -> Result String (Dict String any)
func Auth_verifyToken(secret any, token any) any {
	parsed, err := jwt.Parse(fmt.Sprintf("%v", token), func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(fmt.Sprintf("%v", secret)), nil
	})
	if err != nil {
		return Err[any, any](ErrFfi("verifyToken: " + err.Error()))
	}
	if !parsed.Valid {
		return Err[any, any](ErrPermissionDenied("verifyToken: invalid token"))
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Err[any, any](ErrPermissionDenied("verifyToken: bad claims"))
	}
	out := map[string]any{}
	for k, v := range claims {
		out[k] = v
	}
	return Ok[any, any](out)
}

// Auth.register : Db -> String -> String -> Result String Int
// Creates a users table if missing, hashes password, inserts user. Returns new user id.
func Auth_register(db any, email any, password any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("auth.register: not a Db"))
	}
	// Use portable schema — `SERIAL`/`AUTOINCREMENT` varies, so use lowest
	// common denominator and let each DB handle sequence.
	schema := `CREATE TABLE IF NOT EXISTS users (
		id ` + autoIdColumn(d.driver) + `,
		email TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT DEFAULT 'user',
		created_at BIGINT NOT NULL
	)`
	if _, err := d.conn.Exec(schema); err != nil {
		return Err[any, any](ErrFfi("auth.register create: " + err.Error()))
	}
	hashResult := Auth_hashPassword(password)
	hr, ok := hashResult.(SkyResult[any, any])
	if !ok || hr.Tag != 0 {
		return hashResult
	}
	q := fmt.Sprintf(
		"INSERT INTO users (email, password_hash, created_at) VALUES (%s, %s, %s)",
		d.placeholder(1), d.placeholder(2), d.placeholder(3),
	)
	if d.driver == "pgx" {
		q += " RETURNING id"
		var id int64
		if err := d.conn.QueryRow(q,
			fmt.Sprintf("%v", email),
			hr.OkValue,
			time.Now().Unix(),
		).Scan(&id); err != nil {
			return Err[any, any](ErrFfi("auth.register: " + err.Error()))
		}
		return Ok[any, any](int(id))
	}
	res, err := d.conn.Exec(q,
		fmt.Sprintf("%v", email),
		hr.OkValue,
		time.Now().Unix(),
	)
	if err != nil {
		return Err[any, any](ErrFfi("auth.register: " + err.Error()))
	}
	id, _ := res.LastInsertId()
	return Ok[any, any](int(id))
}

func autoIdColumn(driver string) string {
	if driver == "pgx" {
		return "SERIAL PRIMARY KEY"
	}
	return "INTEGER PRIMARY KEY AUTOINCREMENT"
}

// Auth.login : Db -> String -> String -> Result String (Dict String any)
// Returns user row on success.
func Auth_login(db any, email any, password any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any](ErrInvalidInput("auth.login: not a Db"))
	}
	row := d.conn.QueryRow(
		fmt.Sprintf("SELECT id, email, password_hash, role FROM users WHERE email = %s", d.placeholder(1)),
		fmt.Sprintf("%v", email),
	)
	var id int
	var em, hash, role string
	if err := row.Scan(&id, &em, &hash, &role); err != nil {
		return Err[any, any](ErrFfi("auth.login: " + err.Error()))
	}
	ok2 := Auth_verifyPassword(password, hash)
	if b, isB := ok2.(bool); !isB || !b {
		return Err[any, any](ErrPermissionDenied("auth.login: invalid credentials"))
	}
	return Ok[any, any](map[string]any{
		"id":    id,
		"email": em,
		"role":  role,
	})
}

// Auth.setRole : Db -> Int -> String -> Result String Int
func Auth_setRole(db any, userId any, role any) any {
	return Db_updateById(db, "users", userId, map[string]any{"role": fmt.Sprintf("%v", role)})
}

// Db.getField : String -> Dict String a -> String
// Sky convention: returns the field value as a string (stringified),
// empty string when the key is missing or the row is not a dict.
// This mirrors Dict.get "key" row |> Maybe.withDefault "", which is
// the shape every Sky user expects from a row-field accessor.
// NOTE: earlier versions wrapped the result in a Result — every
// caller then had to unwrap (unnecessarily), and typed-codegen
// paths with `.(string)` assertions panicked when the wrapper leaked
// through. If you need distinguishable "missing" behaviour, use
// Db.getFieldOr with a sentinel default, or the dedicated
// getString/getInt/getBool helpers (which still return Result).
func Db_getField(fname any, row any) string {
	if m, ok := row.(map[string]any); ok {
		if v, exists := m[fmt.Sprintf("%v", fname)]; exists {
			if s, isStr := v.(string); isStr {
				return s
			}
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// Db.getFieldOr : default -> row -> fieldName -> any
func Db_getFieldOr(defaultVal any, row any, fname any) any {
	if m, ok := row.(map[string]any); ok {
		if v, exists := m[fmt.Sprintf("%v", fname)]; exists {
			return v
		}
	}
	return defaultVal
}

func Db_getString(row any, fname any) any {
	if m, ok := row.(map[string]any); ok {
		if v, exists := m[fmt.Sprintf("%v", fname)]; exists {
			return Ok[any, any](fmt.Sprintf("%v", v))
		}
	}
	return Err[any, any](ErrInvalidInput("getString: no field '" + fmt.Sprintf("%v", fname) + "'"))
}

func Db_getInt(row any, fname any) any {
	if m, ok := row.(map[string]any); ok {
		if v, exists := m[fmt.Sprintf("%v", fname)]; exists {
			return Ok[any, any](AsInt(v))
		}
	}
	return Err[any, any](ErrInvalidInput("getInt: no field '" + fmt.Sprintf("%v", fname) + "'"))
}

func Db_getBool(row any, fname any) any {
	if m, ok := row.(map[string]any); ok {
		if v, exists := m[fmt.Sprintf("%v", fname)]; exists {
			return Ok[any, any](AsBool(v))
		}
	}
	return Err[any, any](ErrInvalidInput("getBool: no field '" + fmt.Sprintf("%v", fname) + "'"))
}
