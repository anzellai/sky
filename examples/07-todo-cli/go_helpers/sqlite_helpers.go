package sky_wrappers

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Sky_sqlite_Exec executes SQL with SkyResult error wrapping.
func Sky_sqlite_Exec(db any, query any, args any) any {
	sqlDb := db.(*sql.DB)
	goArgs := toSqlArgs(args)
	_, err := sqlDb.Exec(query.(string), goArgs...)
	if err != nil {
		return SkyErr(err.Error())
	}
	return SkyOk(0)
}

// Sky_sqlite_QueryRows runs a SELECT returning all rows as list of maps.
func Sky_sqlite_QueryRows(db any, query any, args any) any {
	sqlDb := db.(*sql.DB)
	goArgs := toSqlArgs(args)

	rows, err := sqlDb.Query(query.(string), goArgs...)
	if err != nil {
		return SkyErr(err.Error())
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return SkyErr(err.Error())
	}

	var results []any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return SkyErr(err.Error())
		}

		row := make(map[string]any)
		for i, col := range cols {
			switch v := values[i].(type) {
			case int64:
				row[col] = fmt.Sprintf("%d", v)
			case float64:
				row[col] = fmt.Sprintf("%g", v)
			case []byte:
				row[col] = string(v)
			case string:
				row[col] = v
			case nil:
				row[col] = ""
			default:
				row[col] = fmt.Sprintf("%v", v)
			}
		}
		results = append(results, row)
	}
	if results == nil {
		results = []any{}
	}
	return SkyOk(results)
}

// Sky_sqlite_GetField gets a field from a row map.
func Sky_sqlite_GetField(field any, row any) any {
	m, ok := row.(map[string]any)
	if !ok {
		return ""
	}
	val, exists := m[field.(string)]
	if !exists {
		return ""
	}
	s, ok := val.(string)
	if ok {
		return s
	}
	return fmt.Sprintf("%v", val)
}

// Sky_sqlite_ParseInt converts string to int.
func Sky_sqlite_ParseInt(s any) any {
	str, ok := s.(string)
	if !ok {
		return 0
	}
	n := 0
	for _, c := range str {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func toSqlArgs(args any) []any {
	if args == nil {
		return nil
	}
	lst, ok := args.([]any)
	if !ok {
		return nil
	}
	return lst
}
