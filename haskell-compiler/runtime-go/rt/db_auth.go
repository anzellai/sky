package rt

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// ═══════════════════════════════════════════════════════════
// Std.Db — SQLite (pure Go, no CGO)
// ═══════════════════════════════════════════════════════════

// SkyDb is an opaque handle over a *sql.DB.
type SkyDb struct {
	conn *sql.DB
	name string
}

var (
	dbRegistry   = map[string]*SkyDb{}
	dbRegistryMu sync.Mutex
)

// Db.connect : String -> Result String Db
// path may be ":memory:" or a file path.
func Db_connect(path any) any {
	p := fmt.Sprintf("%v", path)
	dbRegistryMu.Lock()
	defer dbRegistryMu.Unlock()
	if existing, ok := dbRegistry[p]; ok {
		return Ok[any, any](existing)
	}
	conn, err := sql.Open("sqlite", p)
	if err != nil {
		return Err[any, any]("db connect: " + err.Error())
	}
	if err := conn.Ping(); err != nil {
		return Err[any, any]("db ping: " + err.Error())
	}
	db := &SkyDb{conn: conn, name: p}
	dbRegistry[p] = db
	return Ok[any, any](db)
}

// Db.open — alias of connect
func Db_open(path any) any { return Db_connect(path) }

// Db.close : Db -> Result String ()
func Db_close(db any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any]("db.close: not a Db")
	}
	if err := d.conn.Close(); err != nil {
		return Err[any, any](err.Error())
	}
	return Ok[any, any](struct{}{})
}

// Db.exec : Db -> String -> List any -> Result String Int
// Runs a statement that doesn't return rows. Returns rows affected.
func Db_exec(db any, query any, args any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any]("db.exec: not a Db")
	}
	argList := asList(args)
	goArgs := make([]any, len(argList))
	for i, a := range argList {
		goArgs[i] = a
	}
	res, err := d.conn.Exec(fmt.Sprintf("%v", query), goArgs...)
	if err != nil {
		return Err[any, any]("db.exec: " + err.Error())
	}
	n, _ := res.RowsAffected()
	return Ok[any, any](int(n))
}

// Db.query : Db -> String -> List any -> Result String (List (Dict String any))
// Returns each row as a Dict of column name → value.
func Db_query(db any, query any, args any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any]("db.query: not a Db")
	}
	argList := asList(args)
	goArgs := make([]any, len(argList))
	for i, a := range argList {
		goArgs[i] = a
	}
	rows, err := d.conn.Query(fmt.Sprintf("%v", query), goArgs...)
	if err != nil {
		return Err[any, any]("db.query: " + err.Error())
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return Err[any, any]("db.query columns: " + err.Error())
	}
	var out []any
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Err[any, any]("db.query scan: " + err.Error())
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
			return Err[any, any]("decode error")
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
func Db_insertRow(db any, table any, row any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any]("db.insertRow: not a Db")
	}
	m, ok := row.(map[string]any)
	if !ok {
		return Err[any, any]("db.insertRow: row must be a Dict")
	}
	var cols []string
	var placeholders []string
	var vals []any
	for k, v := range m {
		cols = append(cols, k)
		placeholders = append(placeholders, "?")
		vals = append(vals, v)
	}
	q := fmt.Sprintf("INSERT INTO %v (%s) VALUES (%s)",
		table, strings.Join(cols, ","), strings.Join(placeholders, ","))
	res, err := d.conn.Exec(q, vals...)
	if err != nil {
		return Err[any, any]("db.insertRow: " + err.Error())
	}
	id, _ := res.LastInsertId()
	return Ok[any, any](int(id))
}

// Db.getById : Db -> String -> Int -> Result String (Dict String any)
func Db_getById(db any, table any, id any) any {
	q := fmt.Sprintf("SELECT * FROM %v WHERE id = ? LIMIT 1", table)
	result := Db_query(db, q, []any{AsInt(id)})
	r, ok := result.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return result
	}
	rows := r.OkValue.([]any)
	if len(rows) == 0 {
		return Err[any, any]("not found")
	}
	return Ok[any, any](rows[0])
}

// Db.updateById : Db -> String -> Int -> Dict String any -> Result String Int
func Db_updateById(db any, table any, id any, row any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any]("db.updateById: not a Db")
	}
	m, ok := row.(map[string]any)
	if !ok {
		return Err[any, any]("db.updateById: row must be a Dict")
	}
	var sets []string
	var vals []any
	for k, v := range m {
		sets = append(sets, k+" = ?")
		vals = append(vals, v)
	}
	vals = append(vals, AsInt(id))
	q := fmt.Sprintf("UPDATE %v SET %s WHERE id = ?", table, strings.Join(sets, ","))
	res, err := d.conn.Exec(q, vals...)
	if err != nil {
		return Err[any, any]("db.updateById: " + err.Error())
	}
	n, _ := res.RowsAffected()
	return Ok[any, any](int(n))
}

// Db.deleteById : Db -> String -> Int -> Result String Int
func Db_deleteById(db any, table any, id any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any]("db.deleteById: not a Db")
	}
	q := fmt.Sprintf("DELETE FROM %v WHERE id = ?", table)
	res, err := d.conn.Exec(q, AsInt(id))
	if err != nil {
		return Err[any, any]("db.deleteById: " + err.Error())
	}
	n, _ := res.RowsAffected()
	return Ok[any, any](int(n))
}

// Db.findWhere : Db -> String -> String -> List any -> Result String (List (Dict String any))
func Db_findWhere(db any, table any, whereClause any, args any) any {
	q := fmt.Sprintf("SELECT * FROM %v WHERE %v", table, whereClause)
	return Db_query(db, q, args)
}

// Db.withTransaction : Db -> (Db -> Result String a) -> Result String a
func Db_withTransaction(db any, body any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any]("db.withTransaction: not a Db")
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return Err[any, any]("tx begin: " + err.Error())
	}
	// We don't have a separate tx handle type yet — pass the db. The semantics
	// are conservative: if body returns Err, roll back. Otherwise commit.
	fn, ok := body.(func(any) any)
	if !ok {
		tx.Rollback()
		return Err[any, any]("withTransaction: body is not a function")
	}
	result := fn(db)
	if sr, ok := result.(SkyResult[any, any]); ok && sr.Tag == 0 {
		if err := tx.Commit(); err != nil {
			return Err[any, any]("tx commit: " + err.Error())
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
func Auth_hashPassword(pw any) any {
	s := fmt.Sprintf("%v", pw)
	hash, err := bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
	if err != nil {
		return Err[any, any]("hashPassword: " + err.Error())
	}
	return Ok[any, any](string(hash))
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
		return Err[any, any]("signToken: " + err.Error())
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
		return Err[any, any]("verifyToken: " + err.Error())
	}
	if !parsed.Valid {
		return Err[any, any]("verifyToken: invalid token")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Err[any, any]("verifyToken: bad claims")
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
		return Err[any, any]("auth.register: not a Db")
	}
	if _, err := d.conn.Exec(`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT DEFAULT 'user',
		created_at INTEGER NOT NULL
	)`); err != nil {
		return Err[any, any]("auth.register create: " + err.Error())
	}
	hashResult := Auth_hashPassword(password)
	hr, ok := hashResult.(SkyResult[any, any])
	if !ok || hr.Tag != 0 {
		return hashResult
	}
	res, err := d.conn.Exec(
		"INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)",
		fmt.Sprintf("%v", email),
		hr.OkValue,
		time.Now().Unix(),
	)
	if err != nil {
		return Err[any, any]("auth.register: " + err.Error())
	}
	id, _ := res.LastInsertId()
	return Ok[any, any](int(id))
}

// Auth.login : Db -> String -> String -> Result String (Dict String any)
// Returns user row on success.
func Auth_login(db any, email any, password any) any {
	d, ok := db.(*SkyDb)
	if !ok {
		return Err[any, any]("auth.login: not a Db")
	}
	row := d.conn.QueryRow(
		"SELECT id, email, password_hash, role FROM users WHERE email = ?",
		fmt.Sprintf("%v", email),
	)
	var id int
	var em, hash, role string
	if err := row.Scan(&id, &em, &hash, &role); err != nil {
		return Err[any, any]("auth.login: " + err.Error())
	}
	ok2 := Auth_verifyPassword(password, hash)
	if b, isB := ok2.(bool); !isB || !b {
		return Err[any, any]("auth.login: invalid credentials")
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
