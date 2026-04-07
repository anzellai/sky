package main

// Sky.Db — Built-in SQL database abstraction for Sky.
// Wraps database/sql with parameterised queries and typed decode support.

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// sky_dbOpen opens a database connection pool.
// driver: "sqlite" or "postgres"
// dsn: file path for sqlite, connection string for postgres
func Sky_sky_db_Open(driver any, dsn any) any {
	d := sky_asString(driver)
	s := sky_asString(dsn)
	db, err := sql.Open(d, s)
	if err != nil {
		return SkyErr(err.Error())
	}
	if err := db.Ping(); err != nil {
		return SkyErr(err.Error())
	}
	return SkyOk(db)
}

// sky_dbClose closes a database connection pool.
func Sky_sky_db_Close(conn any) any {
	db := conn.(*sql.DB)
	if err := db.Close(); err != nil {
		return SkyErr(err.Error())
	}
	return SkyOk(struct{}{})
}

// sky_dbExec executes a parameterised statement (INSERT, UPDATE, DELETE).
// Returns number of rows affected.
func Sky_sky_db_Exec(conn any, query any, params any) any {
	db := conn.(*sql.DB)
	q := sky_asString(query)
	args := skyListToSqlArgs(params)
	result, err := db.Exec(q, args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	n, _ := result.RowsAffected()
	return SkyOk(int(n))
}

// sky_dbQuery executes a parameterised query, returns List (Dict String String).
func Sky_sky_db_Query(conn any, query any, params any) any {
	db := conn.(*sql.DB)
	q := sky_asString(query)
	args := skyListToSqlArgs(params)
	rows, err := db.Query(q, args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	return SkyOk(rowsToStringMaps(rows))
}

// sky_dbQueryOne returns Maybe (Dict String String) — Just row or Nothing.
func Sky_sky_db_QueryOne(conn any, query any, params any) any {
	db := conn.(*sql.DB)
	q := sky_asString(query)
	args := skyListToSqlArgs(params)
	rows, err := db.Query(q, args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	results := rowsToStringMaps(rows)
	list := results.([]any)
	if len(list) == 0 {
		return SkyOk(SkyNothing())
	}
	return SkyOk(SkyJust(list[0]))
}

// sky_dbExecRaw executes a raw DDL statement (CREATE TABLE, etc.).
func Sky_sky_db_ExecRaw(conn any, query any) any {
	db := conn.(*sql.DB)
	q := sky_asString(query)
	_, err := db.Exec(q)
	if err != nil {
		return SkyErr(err.Error())
	}
	return SkyOk(struct{}{})
}

// sky_dbQueryDecode executes a query and decodes each row using a Sky decoder.
// Returns List a where a is the decoded type.
// The decoder receives a map[string]any with auto-parsed typed values.
func Sky_sky_db_QueryDecode(conn any, query any, params any, decoder any) any {
	db := conn.(*sql.DB)
	q := sky_asString(query)
	args := skyListToSqlArgs(params)
	rows, err := db.Query(q, args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	typedRows := rowsToTypedMaps(rows)
	return decodeRows(typedRows, decoder)
}

// sky_dbQueryOneDecode executes a query and decodes the first row.
// Returns Maybe a.
func Sky_sky_db_QueryOneDecode(conn any, query any, params any, decoder any) any {
	db := conn.(*sql.DB)
	q := sky_asString(query)
	args := skyListToSqlArgs(params)
	rows, err := db.Query(q, args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	typedRows := rowsToTypedMaps(rows)
	list := typedRows.([]any)
	if len(list) == 0 {
		return SkyOk(SkyNothing())
	}
	// Decode the first row
	decodeFn, ok := decoder.(func(any) any)
	if !ok {
		return SkyErr("invalid decoder")
	}
	result := decodeFn(list[0])
	if isOkResult(result) {
		return SkyOk(SkyJust(unwrapOk(result)))
	}
	return result // propagate Err
}

// sky_dbInsertRow inserts a row from a Dict of column->value pairs.
func Sky_sky_db_InsertRow(conn any, table any, row any) any {
	db := conn.(*sql.DB)
	t := sky_asString(table)
	m := sky_asMap(row)
	if len(m) == 0 {
		return SkyErr("empty row")
	}
	cols := make([]string, 0, len(m))
	vals := make([]any, 0, len(m))
	placeholders := make([]string, 0, len(m))
	for k, v := range m {
		cols = append(cols, k)
		vals = append(vals, sky_asString(v))
		placeholders = append(placeholders, "?")
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", t, strings.Join(cols, ", "), strings.Join(placeholders, ", "))
	result, err := db.Exec(q, vals...)
	if err != nil {
		return SkyErr(err.Error())
	}
	n, _ := result.RowsAffected()
	return SkyOk(int(n))
}

// sky_dbGetById gets a row by ID (assumes column named "id").
func Sky_sky_db_GetById(conn any, table any, id any) any {
	db := conn.(*sql.DB)
	t := sky_asString(table)
	i := sky_asString(id)
	q := fmt.Sprintf("SELECT * FROM %s WHERE id = ? LIMIT 1", t)
	rows, err := db.Query(q, i)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	results := rowsToStringMaps(rows)
	list := results.([]any)
	if len(list) == 0 {
		return SkyOk(SkyNothing())
	}
	return SkyOk(SkyJust(list[0]))
}

// sky_dbGetByIdDecode gets a row by ID and decodes it.
func Sky_sky_db_GetByIdDecode(conn any, table any, id any, decoder any) any {
	db := conn.(*sql.DB)
	t := sky_asString(table)
	i := sky_asString(id)
	q := fmt.Sprintf("SELECT * FROM %s WHERE id = ? LIMIT 1", t)
	rows, err := db.Query(q, i)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	typedRows := rowsToTypedMaps(rows)
	list := typedRows.([]any)
	if len(list) == 0 {
		return SkyOk(SkyNothing())
	}
	decodeFn, ok := decoder.(func(any) any)
	if !ok {
		return SkyErr("invalid decoder")
	}
	result := decodeFn(list[0])
	if isOkResult(result) {
		return SkyOk(SkyJust(unwrapOk(result)))
	}
	return result
}

// sky_dbUpdateById updates a row by ID. Only updates columns in the Dict.
func Sky_sky_db_UpdateById(conn any, table any, id any, updates any) any {
	db := conn.(*sql.DB)
	t := sky_asString(table)
	i := sky_asString(id)
	m := sky_asMap(updates)
	if len(m) == 0 {
		return SkyOk(0)
	}
	setClauses := make([]string, 0, len(m))
	vals := make([]any, 0, len(m)+1)
	for k, v := range m {
		setClauses = append(setClauses, k+" = ?")
		vals = append(vals, sky_asString(v))
	}
	vals = append(vals, i)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", t, strings.Join(setClauses, ", "))
	result, err := db.Exec(q, vals...)
	if err != nil {
		return SkyErr(err.Error())
	}
	n, _ := result.RowsAffected()
	return SkyOk(int(n))
}

// sky_dbDeleteById deletes a row by ID.
func Sky_sky_db_DeleteById(conn any, table any, id any) any {
	db := conn.(*sql.DB)
	t := sky_asString(table)
	i := sky_asString(id)
	q := fmt.Sprintf("DELETE FROM %s WHERE id = ?", t)
	result, err := db.Exec(q, i)
	if err != nil {
		return SkyErr(err.Error())
	}
	n, _ := result.RowsAffected()
	return SkyOk(int(n))
}

// sky_dbFindWhere finds rows where column = value.
func Sky_sky_db_FindWhere(conn any, table any, column any, value any) any {
	db := conn.(*sql.DB)
	t := sky_asString(table)
	c := sky_asString(column)
	v := sky_asString(value)
	q := fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", t, c)
	rows, err := db.Query(q, v)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	return SkyOk(rowsToStringMaps(rows))
}

// sky_dbFindWhereDecode finds rows where column = value, decoded via decoder.
func Sky_sky_db_FindWhereDecode(conn any, table any, column any, value any, decoder any) any {
	db := conn.(*sql.DB)
	t := sky_asString(table)
	c := sky_asString(column)
	v := sky_asString(value)
	q := fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", t, c)
	rows, err := db.Query(q, v)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	typedRows := rowsToTypedMaps(rows)
	return decodeRows(typedRows, decoder)
}

// sky_dbGetField gets a string field from a row Dict.
func Sky_sky_db_GetField(field any, row any) any {
	m := sky_asMap(row)
	if v, ok := m[sky_asString(field)]; ok {
		return sky_asString(v)
	}
	return ""
}

// sky_dbGetInt gets an int field from a row Dict.
func Sky_sky_db_GetInt(field any, row any) any {
	m := sky_asMap(row)
	if v, ok := m[sky_asString(field)]; ok {
		s := sky_asString(v)
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return 0
}

// sky_dbGetBool gets a bool field from a row Dict.
func Sky_sky_db_GetBool(field any, row any) any {
	m := sky_asMap(row)
	if v, ok := m[sky_asString(field)]; ok {
		s := sky_asString(v)
		return s == "1" || s == "true" || s == "True"
	}
	return false
}

// sky_dbRawConn returns the underlying *sql.DB.
func Sky_sky_db_RawConn(conn any) any {
	return conn
}

// --- Transaction support ---

// sky_dbWithTransaction begins a tx, calls the function, commits or rolls back.
func Sky_sky_db_WithTransaction(conn any, fn any) any {
	db := conn.(*sql.DB)
	tx, err := db.Begin()
	if err != nil {
		return SkyErr(err.Error())
	}
	txFn, ok := fn.(func(any) any)
	if !ok {
		tx.Rollback()
		return SkyErr("invalid transaction function")
	}
	result := txFn(tx)
	if isOkResult(result) {
		if err := tx.Commit(); err != nil {
			return SkyErr(err.Error())
		}
		return result
	}
	tx.Rollback()
	return result
}

// sky_dbTxExec executes within a transaction.
func Sky_sky_db_TxExec(txConn any, query any, params any) any {
	tx := txConn.(*sql.Tx)
	q := sky_asString(query)
	args := skyListToSqlArgs(params)
	result, err := tx.Exec(q, args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	n, _ := result.RowsAffected()
	return SkyOk(int(n))
}

// sky_dbTxQuery queries within a transaction.
func Sky_sky_db_TxQuery(txConn any, query any, params any) any {
	tx := txConn.(*sql.Tx)
	q := sky_asString(query)
	args := skyListToSqlArgs(params)
	rows, err := tx.Query(q, args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	return SkyOk(rowsToStringMaps(rows))
}

// sky_dbTxQueryDecode queries within a transaction with decoder.
func Sky_sky_db_TxQueryDecode(txConn any, query any, params any, decoder any) any {
	tx := txConn.(*sql.Tx)
	q := sky_asString(query)
	args := skyListToSqlArgs(params)
	rows, err := tx.Query(q, args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()
	typedRows := rowsToTypedMaps(rows)
	return decodeRows(typedRows, decoder)
}

// --- Helpers ---

func skyListToSqlArgs(list any) []any {
	items := sky_asList(list)
	args := make([]any, len(items))
	for i, item := range items {
		switch v := item.(type) {
		case int:
			args[i] = v
		case float64:
			args[i] = v
		case bool:
			args[i] = v
		default:
			args[i] = sky_asString(item)
		}
	}
	return args
}

func rowsToStringMaps(rows *sql.Rows) any {
	cols, err := rows.Columns()
	if err != nil {
		return []any{}
	}
	var result []any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = valToString(values[i])
		}
		result = append(result, row)
	}
	if result == nil {
		return []any{}
	}
	return result
}

func rowsToTypedMaps(rows *sql.Rows) any {
	cols, err := rows.Columns()
	if err != nil {
		return []any{}
	}
	var result []any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = smartParse(values[i])
		}
		result = append(result, row)
	}
	if result == nil {
		return []any{}
	}
	return result
}

func valToString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// smartParse converts DB values to typed Go values for decoder consumption.
func smartParse(v any) any {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case int64:
		return int(x)
	case float64:
		return x
	case bool:
		return x
	case []byte:
		return smartParseString(string(x))
	case string:
		return smartParseString(x)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func smartParseString(s string) any {
	// Try int
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	// Try float
	if f, err := strconv.ParseFloat(s, 64); err == nil && strings.Contains(s, ".") {
		return f
	}
	// Try bool
	switch s {
	case "true", "True":
		return true
	case "false", "False":
		return false
	}
	return s
}

func decodeRows(typedRows any, decoder any) any {
	list := typedRows.([]any)
	decodeFn, ok := decoder.(func(any) any)
	if !ok {
		return SkyErr("invalid decoder")
	}
	var decoded []any
	for _, row := range list {
		result := decodeFn(row)
		if !isOkResult(result) {
			return result // propagate first error
		}
		decoded = append(decoded, unwrapOk(result))
	}
	if decoded == nil {
		return SkyOk([]any{})
	}
	return SkyOk(decoded)
}

func isOkResult(v any) bool {
	if r, ok := v.(SkyResult); ok {
		return r.Tag == 0
	}
	if m, ok := v.(map[string]any); ok {
		return m["SkyName"] == "Ok"
	}
	return false
}

func unwrapOk(v any) any {
	if r, ok := v.(SkyResult); ok {
		return r.OkValue
	}
	if m, ok := v.(map[string]any); ok {
		return m["V0"]
	}
	return v
}

// --- Non-curried Sky-callable wrappers ---
// Direct arg passing — works with flattened call convention in multi-module mode.

func sky_dbOpen(driver any, dsn any) any { return Sky_sky_db_Open(driver, dsn) }
func sky_dbClose(conn any) any { return Sky_sky_db_Close(conn) }
func sky_dbExec(conn any, query any, params any) any { return Sky_sky_db_Exec(conn, query, params) }
func sky_dbQuery(conn any, query any, params any) any { return Sky_sky_db_Query(conn, query, params) }
func sky_dbQueryOne(conn any, query any, params any) any { return Sky_sky_db_QueryOne(conn, query, params) }
func sky_dbExecRaw(conn any, query any) any { return Sky_sky_db_ExecRaw(conn, query) }
func sky_dbQueryDecode(conn any, query any, params any, decoder any) any { return Sky_sky_db_QueryDecode(conn, query, params, decoder) }
func sky_dbQueryOneDecode(conn any, query any, params any, decoder any) any { return Sky_sky_db_QueryOneDecode(conn, query, params, decoder) }
func sky_dbInsertRow(conn any, table any, row any) any { return Sky_sky_db_InsertRow(conn, table, row) }
func sky_dbGetById(conn any, table any, id any) any { return Sky_sky_db_GetById(conn, table, id) }
func sky_dbGetByIdDecode(conn any, table any, id any, decoder any) any { return Sky_sky_db_GetByIdDecode(conn, table, id, decoder) }
func sky_dbUpdateById(conn any, table any, id any, updates any) any { return Sky_sky_db_UpdateById(conn, table, id, updates) }
func sky_dbDeleteById(conn any, table any, id any) any { return Sky_sky_db_DeleteById(conn, table, id) }
func sky_dbFindWhere(conn any, table any, column any, value any) any { return Sky_sky_db_FindWhere(conn, table, column, value) }
func sky_dbFindWhereDecode(conn any, table any, column any, value any, decoder any) any { return Sky_sky_db_FindWhereDecode(conn, table, column, value, decoder) }
func sky_dbGetField(field any, row any) any { return Sky_sky_db_GetField(field, row) }
func sky_dbGetInt(field any, row any) any { return Sky_sky_db_GetInt(field, row) }
func sky_dbGetBool(field any, row any) any { return Sky_sky_db_GetBool(field, row) }
func sky_dbRawConn(conn any) any { return Sky_sky_db_RawConn(conn) }
func sky_dbWithTransaction(conn any, fn any) any { return Sky_sky_db_WithTransaction(conn, fn) }
func sky_dbTxExec(txConn any, query any, params any) any { return Sky_sky_db_TxExec(txConn, query, params) }
func sky_dbTxQuery(txConn any, query any, params any) any { return Sky_sky_db_TxQuery(txConn, query, params) }
func sky_dbTxQueryDecode(txConn any, query any, params any, decoder any) any { return Sky_sky_db_TxQueryDecode(txConn, query, params, decoder) }
