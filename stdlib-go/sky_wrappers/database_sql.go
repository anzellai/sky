package sky_wrappers

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"context"
	"time"
	"fmt"
)

func Sky_database_sql_Drivers() []string {
	return sql.Drivers()
}

func Sky_database_sql_Named(arg0 any, arg1 any) sql.NamedArg {
	_arg0 := arg0.(string)
	_arg1 := arg1.(any)
	return sql.Named(_arg0, _arg1)
}

func Sky_database_sql_Open(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	res, err := sql.Open(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_OpenDB(arg0 any) *sql.DB {
	_arg0 := arg0.(driver.Connector)
	return sql.OpenDB(_arg0)
}

func Sky_database_sql_Register(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	_arg1 := arg1.(driver.Driver)
	sql.Register(_arg0, _arg1)
	return struct{}{}
}

func Sky_database_sql_ErrConnDone() any {
	return sql.ErrConnDone
}

func Sky_database_sql_ErrNoRows() any {
	return sql.ErrNoRows
}

func Sky_database_sql_ErrTxDone() any {
	return sql.ErrTxDone
}

func Sky_database_sql_LevelDefault() any {
	return sql.LevelDefault
}

func Sky_database_sql_LevelLinearizable() any {
	return sql.LevelLinearizable
}

func Sky_database_sql_LevelReadCommitted() any {
	return sql.LevelReadCommitted
}

func Sky_database_sql_LevelReadUncommitted() any {
	return sql.LevelReadUncommitted
}

func Sky_database_sql_LevelRepeatableRead() any {
	return sql.LevelRepeatableRead
}

func Sky_database_sql_LevelSerializable() any {
	return sql.LevelSerializable
}

func Sky_database_sql_LevelSnapshot() any {
	return sql.LevelSnapshot
}

func Sky_database_sql_LevelWriteCommitted() any {
	return sql.LevelWriteCommitted
}

func Sky_database_sql_ColumnTypeDatabaseTypeName(this any) string {
	_this := this.(*sql.ColumnType)

	return _this.DatabaseTypeName()
}

func Sky_database_sql_ColumnTypeDecimalSize(this any) any {
	_this := this.(*sql.ColumnType)

	_r0, _r1, _r2 := _this.DecimalSize()
	return SkyTuple3{V0: _r0, V1: _r1, V2: _r2}
}

func Sky_database_sql_ColumnTypeLength(this any) any {
	_this := this.(*sql.ColumnType)

	_val, _ok := _this.Length()
	if !_ok {
		return SkyNothing()
	}
	return SkyJust(_val)
}

func Sky_database_sql_ColumnTypeName(this any) string {
	_this := this.(*sql.ColumnType)

	return _this.Name()
}

func Sky_database_sql_ColumnTypeNullable(this any) any {
	_this := this.(*sql.ColumnType)

	_val, _ok := _this.Nullable()
	if !_ok {
		return SkyNothing()
	}
	return SkyJust(_val)
}

func Sky_database_sql_ColumnTypeScanType(this any) reflect.Type {
	_this := this.(*sql.ColumnType)

	return _this.ScanType()
}

func Sky_database_sql_ConnBeginTx(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.Conn)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(*sql.TxOptions)
	res, err := _this.BeginTx(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_ConnClose(this any) SkyResult {
	_this := this.(*sql.Conn)

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_ConnExecContext(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*sql.Conn)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := _this.ExecContext(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_ConnPingContext(this any, arg0 any) SkyResult {
	_this := this.(*sql.Conn)
	_arg0 := arg0.(context.Context)
	err := _this.PingContext(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_ConnPrepareContext(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.Conn)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	res, err := _this.PrepareContext(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_ConnQueryContext(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*sql.Conn)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := _this.QueryContext(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_ConnQueryRowContext(this any, arg0 any, arg1 any, arg2 any) *sql.Row {
	_this := this.(*sql.Conn)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	return _this.QueryRowContext(_arg0, _arg1, _arg2...)
}

func Sky_database_sql_ConnRaw(this any, arg0 any) SkyResult {
	_this := this.(*sql.Conn)
	_skyFn0 := arg0.(func(any) any)
	_arg0 := func(p0 any) error {
		return _skyFn0(p0).(error)
	}
	err := _this.Raw(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_DBBegin(this any) SkyResult {
	_this := this.(*sql.DB)

	res, err := _this.Begin()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBBeginTx(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(*sql.TxOptions)
	res, err := _this.BeginTx(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBClose(this any) SkyResult {
	_this := this.(*sql.DB)

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_DBConn(this any, arg0 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(context.Context)
	res, err := _this.Conn(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBDriver(this any) driver.Driver {
	_this := this.(*sql.DB)

	return _this.Driver()
}

func Sky_database_sql_DBExec(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := _this.Exec(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBExecContext(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := _this.ExecContext(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBPing(this any) SkyResult {
	_this := this.(*sql.DB)

	err := _this.Ping()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_DBPingContext(this any, arg0 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(context.Context)
	err := _this.PingContext(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_DBPrepare(this any, arg0 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(string)
	res, err := _this.Prepare(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBPrepareContext(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	res, err := _this.PrepareContext(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBQuery(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := _this.Query(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBQueryContext(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*sql.DB)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := _this.QueryContext(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_DBQueryRow(this any, arg0 any, arg1 any) *sql.Row {
	_this := this.(*sql.DB)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	return _this.QueryRow(_arg0, _arg1...)
}

func Sky_database_sql_DBQueryRowContext(this any, arg0 any, arg1 any, arg2 any) *sql.Row {
	_this := this.(*sql.DB)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	return _this.QueryRowContext(_arg0, _arg1, _arg2...)
}

func Sky_database_sql_DBSetConnMaxIdleTime(this any, arg0 any) any {
	_this := this.(*sql.DB)
	_arg0 := arg0.(time.Duration)
	_this.SetConnMaxIdleTime(_arg0)
	return struct{}{}
}

func Sky_database_sql_DBSetConnMaxLifetime(this any, arg0 any) any {
	_this := this.(*sql.DB)
	_arg0 := arg0.(time.Duration)
	_this.SetConnMaxLifetime(_arg0)
	return struct{}{}
}

func Sky_database_sql_DBSetMaxIdleConns(this any, arg0 any) any {
	_this := this.(*sql.DB)
	_arg0 := arg0.(int)
	_this.SetMaxIdleConns(_arg0)
	return struct{}{}
}

func Sky_database_sql_DBSetMaxOpenConns(this any, arg0 any) any {
	_this := this.(*sql.DB)
	_arg0 := arg0.(int)
	_this.SetMaxOpenConns(_arg0)
	return struct{}{}
}

func Sky_database_sql_DBStats(this any) sql.DBStats {
	_this := this.(*sql.DB)

	return _this.Stats()
}

func Sky_database_sql_DBStatsMaxOpenConnections(this any) int {
	_this := this.(*sql.DBStats)

	return _this.MaxOpenConnections
}

func Sky_database_sql_DBStatsOpenConnections(this any) int {
	_this := this.(*sql.DBStats)

	return _this.OpenConnections
}

func Sky_database_sql_DBStatsInUse(this any) int {
	_this := this.(*sql.DBStats)

	return _this.InUse
}

func Sky_database_sql_DBStatsIdle(this any) int {
	_this := this.(*sql.DBStats)

	return _this.Idle
}

func Sky_database_sql_DBStatsWaitCount(this any) int64 {
	_this := this.(*sql.DBStats)

	return _this.WaitCount
}

func Sky_database_sql_DBStatsWaitDuration(this any) time.Duration {
	_this := this.(*sql.DBStats)

	return _this.WaitDuration
}

func Sky_database_sql_DBStatsMaxIdleClosed(this any) int64 {
	_this := this.(*sql.DBStats)

	return _this.MaxIdleClosed
}

func Sky_database_sql_DBStatsMaxIdleTimeClosed(this any) int64 {
	_this := this.(*sql.DBStats)

	return _this.MaxIdleTimeClosed
}

func Sky_database_sql_DBStatsMaxLifetimeClosed(this any) int64 {
	_this := this.(*sql.DBStats)

	return _this.MaxLifetimeClosed
}

func Sky_database_sql_IsolationLevelString(this any) string {
	_this := this.(sql.IsolationLevel)

	return _this.String()
}

func Sky_database_sql_NamedArgName(this any) string {
	_this := this.(*sql.NamedArg)

	return _this.Name
}

func Sky_database_sql_NamedArgValue(this any) any {
	_this := this.(*sql.NamedArg)

	return _this.Value
}

func Sky_database_sql_NullBoolScan(this any, arg0 any) SkyResult {
	_this := this.(*sql.NullBool)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_NullBoolValue(this any) SkyResult {
	_this := this.(*sql.NullBool)

	res, err := _this.Value()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_NullBoolBool(this any) bool {
	_this := this.(*sql.NullBool)

	return _this.Bool
}

func Sky_database_sql_NullBoolValid(this any) bool {
	_this := this.(*sql.NullBool)

	return _this.Valid
}

func Sky_database_sql_NullByteScan(this any, arg0 any) SkyResult {
	_this := this.(*sql.NullByte)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_NullByteValue(this any) SkyResult {
	_this := this.(*sql.NullByte)

	res, err := _this.Value()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_NullByteByte(this any) byte {
	_this := this.(*sql.NullByte)

	return _this.Byte
}

func Sky_database_sql_NullByteValid(this any) bool {
	_this := this.(*sql.NullByte)

	return _this.Valid
}

func Sky_database_sql_NullFloat64Scan(this any, arg0 any) SkyResult {
	_this := this.(*sql.NullFloat64)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_NullFloat64Value(this any) SkyResult {
	_this := this.(*sql.NullFloat64)

	res, err := _this.Value()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_NullFloat64Float64(this any) float64 {
	_this := this.(*sql.NullFloat64)

	return _this.Float64
}

func Sky_database_sql_NullFloat64Valid(this any) bool {
	_this := this.(*sql.NullFloat64)

	return _this.Valid
}

func Sky_database_sql_NullInt16Scan(this any, arg0 any) SkyResult {
	_this := this.(*sql.NullInt16)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_NullInt16Value(this any) SkyResult {
	_this := this.(*sql.NullInt16)

	res, err := _this.Value()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_NullInt16Int16(this any) int16 {
	_this := this.(*sql.NullInt16)

	return _this.Int16
}

func Sky_database_sql_NullInt16Valid(this any) bool {
	_this := this.(*sql.NullInt16)

	return _this.Valid
}

func Sky_database_sql_NullInt32Scan(this any, arg0 any) SkyResult {
	_this := this.(*sql.NullInt32)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_NullInt32Value(this any) SkyResult {
	_this := this.(*sql.NullInt32)

	res, err := _this.Value()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_NullInt32Int32(this any) int32 {
	_this := this.(*sql.NullInt32)

	return _this.Int32
}

func Sky_database_sql_NullInt32Valid(this any) bool {
	_this := this.(*sql.NullInt32)

	return _this.Valid
}

func Sky_database_sql_NullInt64Scan(this any, arg0 any) SkyResult {
	_this := this.(*sql.NullInt64)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_NullInt64Value(this any) SkyResult {
	_this := this.(*sql.NullInt64)

	res, err := _this.Value()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_NullInt64Int64(this any) int64 {
	_this := this.(*sql.NullInt64)

	return _this.Int64
}

func Sky_database_sql_NullInt64Valid(this any) bool {
	_this := this.(*sql.NullInt64)

	return _this.Valid
}

func Sky_database_sql_NullStringScan(this any, arg0 any) SkyResult {
	_this := this.(*sql.NullString)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_NullStringValue(this any) SkyResult {
	_this := this.(*sql.NullString)

	res, err := _this.Value()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_NullStringString(this any) string {
	_this := this.(*sql.NullString)

	return _this.String
}

func Sky_database_sql_NullStringValid(this any) bool {
	_this := this.(*sql.NullString)

	return _this.Valid
}

func Sky_database_sql_NullTimeScan(this any, arg0 any) SkyResult {
	_this := this.(*sql.NullTime)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_NullTimeValue(this any) SkyResult {
	_this := this.(*sql.NullTime)

	res, err := _this.Value()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_NullTimeTime(this any) time.Time {
	_this := this.(*sql.NullTime)

	return _this.Time
}

func Sky_database_sql_NullTimeValid(this any) bool {
	_this := this.(*sql.NullTime)

	return _this.Valid
}

func Sky_database_sql_OutDest(this any) any {
	_this := this.(*sql.Out)

	return _this.Dest
}

func Sky_database_sql_OutIn(this any) bool {
	_this := this.(*sql.Out)

	return _this.In
}

func Sky_database_sql_ResultLastInsertId(this any) SkyResult {
	_this := this.(sql.Result)

	res, err := _this.LastInsertId()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_ResultRowsAffected(this any) SkyResult {
	_this := this.(sql.Result)

	res, err := _this.RowsAffected()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_RowErr(this any) SkyResult {
	_this := this.(*sql.Row)

	err := _this.Err()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_RowScan(this any, arg0 any) SkyResult {
	_this := this.(*sql.Row)
	var _arg0 []any
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(any))
	}
	err := _this.Scan(_arg0...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_RowsClose(this any) SkyResult {
	_this := this.(*sql.Rows)

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_RowsColumnTypes(this any) SkyResult {
	_this := this.(*sql.Rows)

	res, err := _this.ColumnTypes()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_RowsColumns(this any) SkyResult {
	_this := this.(*sql.Rows)

	res, err := _this.Columns()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_RowsErr(this any) SkyResult {
	_this := this.(*sql.Rows)

	err := _this.Err()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_RowsNext(this any) bool {
	_this := this.(*sql.Rows)

	return _this.Next()
}

func Sky_database_sql_RowsNextResultSet(this any) bool {
	_this := this.(*sql.Rows)

	return _this.NextResultSet()
}

func Sky_database_sql_RowsScan(this any, arg0 any) SkyResult {
	_this := this.(*sql.Rows)
	var _arg0 []any
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(any))
	}
	err := _this.Scan(_arg0...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_ScannerScan(this any, arg0 any) SkyResult {
	_this := this.(sql.Scanner)
	_arg0 := arg0.(any)
	err := _this.Scan(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_StmtClose(this any) SkyResult {
	_this := this.(*sql.Stmt)

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_StmtExec(this any, arg0 any) SkyResult {
	_this := this.(*sql.Stmt)
	var _arg0 []any
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(any))
	}
	res, err := _this.Exec(_arg0...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_StmtExecContext(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.Stmt)
	_arg0 := arg0.(context.Context)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := _this.ExecContext(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_StmtQuery(this any, arg0 any) SkyResult {
	_this := this.(*sql.Stmt)
	var _arg0 []any
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(any))
	}
	res, err := _this.Query(_arg0...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_StmtQueryContext(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.Stmt)
	_arg0 := arg0.(context.Context)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := _this.QueryContext(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_StmtQueryRow(this any, arg0 any) *sql.Row {
	_this := this.(*sql.Stmt)
	var _arg0 []any
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(any))
	}
	return _this.QueryRow(_arg0...)
}

func Sky_database_sql_StmtQueryRowContext(this any, arg0 any, arg1 any) *sql.Row {
	_this := this.(*sql.Stmt)
	_arg0 := arg0.(context.Context)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	return _this.QueryRowContext(_arg0, _arg1...)
}

func Sky_database_sql_TxCommit(this any) SkyResult {
	_this := this.(*sql.Tx)

	err := _this.Commit()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_TxExec(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := _this.Exec(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_TxExecContext(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := _this.ExecContext(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_TxPrepare(this any, arg0 any) SkyResult {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(string)
	res, err := _this.Prepare(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_TxPrepareContext(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	res, err := _this.PrepareContext(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_TxQuery(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := _this.Query(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_TxQueryContext(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := _this.QueryContext(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_database_sql_TxQueryRow(this any, arg0 any, arg1 any) *sql.Row {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(any))
	}
	return _this.QueryRow(_arg0, _arg1...)
}

func Sky_database_sql_TxQueryRowContext(this any, arg0 any, arg1 any, arg2 any) *sql.Row {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range sky_asList(arg2) {
		_arg2 = append(_arg2, v.(any))
	}
	return _this.QueryRowContext(_arg0, _arg1, _arg2...)
}

func Sky_database_sql_TxRollback(this any) SkyResult {
	_this := this.(*sql.Tx)

	err := _this.Rollback()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_database_sql_TxStmt(this any, arg0 any) *sql.Stmt {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(*sql.Stmt)
	return _this.Stmt(_arg0)
}

func Sky_database_sql_TxStmtContext(this any, arg0 any, arg1 any) *sql.Stmt {
	_this := this.(*sql.Tx)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(*sql.Stmt)
	return _this.StmtContext(_arg0, _arg1)
}

func Sky_database_sql_TxOptionsIsolation(this any) sql.IsolationLevel {
	_this := this.(*sql.TxOptions)

	return _this.Isolation
}

func Sky_database_sql_TxOptionsReadOnly(this any) bool {
	_this := this.(*sql.TxOptions)

	return _this.ReadOnly
}

// Auto-generated convenience wrapper: exec on DB returning rows affected
func Sky_database_sql_DBExecResult(db any, query any, args any) any {
	_db := db.(*sql.DB)
	_query := sky_asString(query)
	var _args []any
	if args != nil {
		if lst, ok := args.([]any); ok {
			_args = lst
		}
	}
	result, err := _db.Exec(_query, _args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	affected, _ := result.RowsAffected()
	return SkyOk(affected)
}

// Auto-generated convenience wrapper: query on DB returning list of dicts
func Sky_database_sql_DBQueryToMaps(db any, query any, args any) any {
	_db := db.(*sql.DB)
	_query := sky_asString(query)
	var _args []any
	if args != nil {
		if lst, ok := args.([]any); ok {
			_args = lst
		}
	}
	rows, err := _db.Query(_query, _args...)
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

// Auto-generated convenience wrapper: iterates Rows, scans all rows into list of dicts
func Sky_database_sql_RowsToMaps(rows any) any {
	r := rows.(*sql.Rows)
	defer r.Close()
	cols, err := r.Columns()
	if err != nil {
		return SkyErr(err.Error())
	}
	var results []any
	for r.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := r.Scan(ptrs...); err != nil {
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

// Auto-generated convenience wrapper: exec on Tx returning rows affected
func Sky_database_sql_TxExecResult(db any, query any, args any) any {
	_db := db.(*sql.Tx)
	_query := sky_asString(query)
	var _args []any
	if args != nil {
		if lst, ok := args.([]any); ok {
			_args = lst
		}
	}
	result, err := _db.Exec(_query, _args...)
	if err != nil {
		return SkyErr(err.Error())
	}
	affected, _ := result.RowsAffected()
	return SkyOk(affected)
}

// Auto-generated convenience wrapper: query on Tx returning list of dicts
// QueryToMaps is a convenience alias that delegates to DBQueryToMaps.
// Sky code uses Sql.queryToMaps which lowers to Database_Sql_QueryToMaps.
func Sky_database_sql_QueryToMaps(db any, query any, args any) any {
	return Sky_database_sql_DBQueryToMaps(db, query, args)
}

func Sky_database_sql_TxQueryToMaps(db any, query any, args any) any {
	_db := db.(*sql.Tx)
	_query := sky_asString(query)
	var _args []any
	if args != nil {
		if lst, ok := args.([]any); ok {
			_args = lst
		}
	}
	rows, err := _db.Query(_query, _args...)
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

