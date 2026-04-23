package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func valueString(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(v)
	}
}

func TestQueryBuilderBuildsExpectedSQL(t *testing.T) {
	query, args := Query("test").
		Select("id", "name").
		Distinct().
		Where("name = ?", "alpha").
		Where("value IS NOT NULL").
		OrderBy("name DESC").
		OrderBy("id ASC").
		Limit(10).
		Offset(5).
		Build()

	if query != "SELECT DISTINCT id, name FROM test WHERE name = ? AND value IS NOT NULL ORDER BY name DESC, id ASC LIMIT 10 OFFSET 5" {
		t.Fatalf("unexpected query: %s", query)
	}
	if len(args) != 1 || args[0] != "alpha" {
		t.Fatalf("unexpected args: %#v", args)
	}

	query, args = Query("test").Build()
	if query != "SELECT * FROM test" {
		t.Fatalf("unexpected default query: %s", query)
	}
	if len(args) != 0 {
		t.Fatalf("expected no args, got %#v", args)
	}
}

func TestReadOperations(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	for i, name := range []string{"alpha", "beta", "gamma"} {
		_, err := db.ExecContext(ctx, "INSERT INTO test (name, value) VALUES (?, ?)", name, fmt.Sprintf("v%d", i+1))
		if err != nil {
			t.Fatalf("seed insert failed: %v", err)
		}
	}

	row, err := db.Get(ctx, "test", []string{"id", "name", "value"}, "name = ?", "alpha")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if valueString(row["name"]) != "alpha" || valueString(row["value"]) != "v1" {
		t.Fatalf("unexpected row: %#v", row)
	}

	if _, err := db.Get(ctx, "test", []string{"id"}, "name = ?", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}

	rows, err := db.List(ctx, "test", []string{"id", "name"}, "")
	if err != nil {
		t.Fatalf("List without where failed: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}

	rows, err = db.List(ctx, "test", []string{"id", "name"}, "name <> ?", "gamma")
	if err != nil {
		t.Fatalf("List with where failed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	rows, err = db.Query(ctx, "SELECT id, name FROM test WHERE name <> ? ORDER BY id", "gamma")
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 queried rows, got %d", len(rows))
	}
	if valueString(rows[0]["name"]) != "alpha" {
		t.Fatalf("unexpected query result: %#v", rows[0])
	}

	row, err = db.QueryRow(ctx, "SELECT id, name FROM test WHERE name = ?", "beta")
	if err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}
	if valueString(row["name"]) != "beta" {
		t.Fatalf("unexpected query row: %#v", row)
	}

	if _, err := db.QueryRow(ctx, "SELECT id FROM test WHERE name = ?", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows from QueryRow, got %v", err)
	}

	count, err := db.Count(ctx, "test", "")
	if err != nil {
		t.Fatalf("Count without where failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected count 3, got %d", count)
	}

	count, err = db.Count(ctx, "test", "name LIKE ?", "a%")
	if err != nil {
		t.Fatalf("Count with where failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}

	exists, err := db.Exists(ctx, "test", "name = ?", "gamma")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Fatal("expected row to exist")
	}

	exists, err = db.Exists(ctx, "test", "name = ?", "missing")
	if err != nil {
		t.Fatalf("Exists missing failed: %v", err)
	}
	if exists {
		t.Fatal("expected row to not exist")
	}
}

func TestReadOperationsWithTransaction(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	for _, name := range []string{"alpha", "beta"} {
		_, err := db.ExecContext(ctx, "INSERT INTO test (name, value) VALUES (?, ?)", name, name+"_value")
		if err != nil {
			t.Fatalf("seed insert failed: %v", err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	defer tx.Rollback()

	row, err := db.GetWithTx(ctx, tx.Tx, "test", []string{"name", "value"}, "name = ?", "alpha")
	if err != nil {
		t.Fatalf("GetWithTx failed: %v", err)
	}
	if valueString(row["value"]) != "alpha_value" {
		t.Fatalf("unexpected tx row: %#v", row)
	}

	rows, err := db.ListWithTx(ctx, tx.Tx, "test", []string{"name"}, "")
	if err != nil {
		t.Fatalf("ListWithTx failed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	rows, err = db.ListWithTx(ctx, tx.Tx, "test", []string{"name"}, "name = ?", "beta")
	if err != nil {
		t.Fatalf("ListWithTx with where failed: %v", err)
	}
	if len(rows) != 1 || valueString(rows[0]["name"]) != "beta" {
		t.Fatalf("unexpected filtered tx rows: %#v", rows)
	}

	rows, err = db.QueryWithTx(ctx, tx.Tx, "SELECT name FROM test WHERE name = ?", "beta")
	if err != nil {
		t.Fatalf("QueryWithTx failed: %v", err)
	}
	if len(rows) != 1 || valueString(rows[0]["name"]) != "beta" {
		t.Fatalf("unexpected QueryWithTx result: %#v", rows)
	}

	row, err = db.QueryRowWithTx(ctx, tx.Tx, "SELECT name FROM test WHERE name = ?", "beta")
	if err != nil {
		t.Fatalf("QueryRowWithTx failed: %v", err)
	}
	if valueString(row["name"]) != "beta" {
		t.Fatalf("unexpected QueryRowWithTx result: %#v", row)
	}

	if _, err := db.QueryRowWithTx(ctx, tx.Tx, "SELECT name FROM test WHERE name = ?", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows from QueryRowWithTx, got %v", err)
	}
	if _, err := db.GetWithTx(ctx, tx.Tx, "test", []string{"name"}, "name = ?", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows from GetWithTx, got %v", err)
	}

	count, err := db.CountWithTx(ctx, tx.Tx, "test", "name LIKE ?", "%a%")
	if err != nil {
		t.Fatalf("CountWithTx failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected tx count 2, got %d", count)
	}
}

func TestReadHelpers(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	_, err := db.ExecContext(ctx, "INSERT INTO test (name, value) VALUES (?, ?)", "alpha", "value")
	if err != nil {
		t.Fatalf("seed insert failed: %v", err)
	}

	rows, err := db.DB.QueryContext(ctx, "SELECT id, name FROM test")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if !rows.Next() {
		t.Fatal("expected one row")
	}
	if _, err := scanRowToMap(rows, nil); err == nil || !strings.Contains(err.Error(), "no columns specified") {
		t.Fatalf("expected missing columns error, got %v", err)
	}
	rows.Close()

	rows, err = db.DB.QueryContext(ctx, "SELECT id, name FROM test")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	results, err := scanRows(rows, nil)
	if err != nil {
		t.Fatalf("scanRows failed: %v", err)
	}
	if len(results) != 1 || valueString(results[0]["name"]) != "alpha" {
		t.Fatalf("unexpected scanRows result: %#v", results)
	}

	rows, err = db.DB.QueryContext(ctx, "SELECT id, name FROM test")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	results, err = scanRows(rows, []string{"id"})
	if err != nil {
		t.Fatalf("scanRows mismatch should continue, got %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected skipped rows on scan mismatch, got %#v", results)
	}

	if _, err := db.queryRows(ctx, "SELECT missing FROM test", nil, nil); err == nil || !strings.Contains(err.Error(), "sqlite: query") {
		t.Fatalf("expected wrapped query error, got %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	defer tx.Rollback()

	if _, err := queryRowsTx(ctx, tx.Tx, "SELECT missing FROM test", nil, nil); err == nil || !strings.Contains(err.Error(), "sqlite: query tx") {
		t.Fatalf("expected wrapped tx query error, got %v", err)
	}
}

func TestWriteOperations(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	result, err := db.Insert(ctx, "test", map[string]any{
		"id":    1,
		"name":  "alpha",
		"value": "one",
	})
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("expected 1 affected row, got %#v", result)
	}

	if _, err := db.Insert(ctx, "test", map[string]any{}); err == nil || !strings.Contains(err.Error(), "no data provided") {
		t.Fatalf("expected insert no data error, got %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	inserted, err := db.InsertWithTx(ctx, tx.Tx, "test", map[string]any{
		"id":    2,
		"name":  "beta",
		"value": "two",
	})
	if err != nil {
		t.Fatalf("InsertWithTx failed: %v", err)
	}
	if inserted.RowsAffected != 1 {
		t.Fatalf("unexpected tx insert result: %#v", inserted)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	affected, err := db.Update(ctx, "test", map[string]any{"value": "updated"}, "name = ?", "alpha")
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 updated row, got %d", affected)
	}

	affected, err = db.Update(ctx, "test", map[string]any{"value": "all"}, "")
	if err != nil {
		t.Fatalf("Update without where failed: %v", err)
	}
	if affected != 2 {
		t.Fatalf("expected 2 updated rows, got %d", affected)
	}

	if _, err := db.Update(ctx, "test", map[string]any{}, ""); err == nil || !strings.Contains(err.Error(), "no data provided") {
		t.Fatalf("expected update no data error, got %v", err)
	}

	upserted, err := db.Upsert(ctx, "test", map[string]any{
		"id":    1,
		"name":  "alpha",
		"value": "upserted",
	}, []string{"id"})
	if err != nil {
		t.Fatalf("Upsert update path failed: %v", err)
	}
	if upserted.RowsAffected == 0 {
		t.Fatalf("expected upsert to affect rows, got %#v", upserted)
	}

	upserted, err = db.Upsert(ctx, "test", map[string]any{
		"id":    3,
		"name":  "gamma",
		"value": "three",
	}, []string{"id"})
	if err != nil {
		t.Fatalf("Upsert insert path failed: %v", err)
	}
	if upserted.RowsAffected == 0 {
		t.Fatalf("expected upsert insert to affect rows, got %#v", upserted)
	}

	if _, err := db.Upsert(ctx, "test", map[string]any{}, []string{"id"}); err == nil || !strings.Contains(err.Error(), "no data provided") {
		t.Fatalf("expected upsert no data error, got %v", err)
	}
	if _, err := db.Upsert(ctx, "test", map[string]any{"id": 4}, nil); err == nil || !strings.Contains(err.Error(), "no conflict columns provided") {
		t.Fatalf("expected upsert conflict columns error, got %v", err)
	}

	row, err := db.QueryRow(ctx, "SELECT value FROM test WHERE id = 1")
	if err != nil {
		t.Fatalf("query updated row failed: %v", err)
	}
	if valueString(row["value"]) != "upserted" {
		t.Fatalf("unexpected upserted value: %#v", row)
	}

	deleted, err := db.Delete(ctx, "test", "id = ?", 3)
	if err != nil {
		t.Fatalf("Delete with where failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected to delete 1 row, got %d", deleted)
	}

	deleted, err = db.Delete(ctx, "test", "")
	if err != nil {
		t.Fatalf("Delete without where failed: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected to delete 2 rows, got %d", deleted)
	}

	if _, err := db.Delete(ctx, "missing_table", ""); err == nil || !strings.Contains(err.Error(), "sqlite: delete from") {
		t.Fatalf("expected delete error, got %v", err)
	}

	if got := joinColumns([]string{"id", "name"}); got != "id, name" {
		t.Fatalf("unexpected joinColumns result: %s", got)
	}
	if got := joinStrings([]string{"one", "two", "three"}); got != "one, two, three" {
		t.Fatalf("unexpected joinStrings result: %s", got)
	}
}

func TestReadWriteErrorPaths(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	if _, err := db.Get(ctx, "missing_table", []string{"id"}, "id = ?", 1); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("expected Get error for missing table, got %v", err)
	}
	if _, err := db.List(ctx, "missing_table", []string{"id"}, ""); err == nil || !strings.Contains(err.Error(), "sqlite: query") {
		t.Fatalf("expected List error for missing table, got %v", err)
	}
	if _, err := db.QueryRow(ctx, "SELECT id FROM missing_table"); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("expected QueryRow error for missing table, got %v", err)
	}
	if _, err := db.Count(ctx, "missing_table", ""); err == nil || !strings.Contains(err.Error(), "sqlite: count") {
		t.Fatalf("expected Count error for missing table, got %v", err)
	}
	if _, err := db.Exists(ctx, "missing_table", ""); err == nil || !strings.Contains(err.Error(), "sqlite: count") {
		t.Fatalf("expected Exists error for missing table, got %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	defer tx.Rollback()

	if _, err := db.GetWithTx(ctx, tx.Tx, "missing_table", []string{"id"}, "id = ?", 1); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("expected GetWithTx error for missing table, got %v", err)
	}
	if _, err := db.CountWithTx(ctx, tx.Tx, "missing_table", ""); err == nil || !strings.Contains(err.Error(), "sqlite: count") {
		t.Fatalf("expected CountWithTx error for missing table, got %v", err)
	}

	if _, err := db.Insert(ctx, "missing_table", map[string]any{"id": 1}); err == nil || !strings.Contains(err.Error(), "sqlite: insert into") {
		t.Fatalf("expected Insert error for missing table, got %v", err)
	}
	if _, err := db.Update(ctx, "missing_table", map[string]any{"value": "x"}, ""); err == nil || !strings.Contains(err.Error(), "sqlite: update") {
		t.Fatalf("expected Update error for missing table, got %v", err)
	}
	if _, err := db.Upsert(ctx, "missing_table", map[string]any{"id": 1}, []string{"id"}); err == nil || !strings.Contains(err.Error(), "sqlite: upsert into") {
		t.Fatalf("expected Upsert error for missing table, got %v", err)
	}
}

func TestTransactionHelpersAndRetry(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	if _, err := tx.Insert(ctx, "test", map[string]any{"id": 1, "name": "tx", "value": "one"}); err != nil {
		t.Fatalf("tx.Insert failed: %v", err)
	}
	if _, err := tx.Update(ctx, "test", map[string]any{"value": "two"}, "id = ?", 1); err != nil {
		t.Fatalf("tx.Update failed: %v", err)
	}
	if _, err := tx.Upsert(ctx, "test", map[string]any{"id": 1, "name": "tx", "value": "three"}, []string{"id"}); err != nil {
		t.Fatalf("tx.Upsert failed: %v", err)
	}

	row, err := tx.Get(ctx, "test", []string{"name", "value"}, "id = ?", 1)
	if err != nil {
		t.Fatalf("tx.Get failed: %v", err)
	}
	if valueString(row["value"]) != "three" {
		t.Fatalf("unexpected tx.Get row: %#v", row)
	}

	rows, err := tx.List(ctx, "test", []string{"name"}, "")
	if err != nil {
		t.Fatalf("tx.List failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 tx.List row, got %d", len(rows))
	}

	rows, err = tx.Query(ctx, "SELECT name FROM test WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("tx.Query failed: %v", err)
	}
	if len(rows) != 1 || valueString(rows[0]["name"]) != "tx" {
		t.Fatalf("unexpected tx.Query rows: %#v", rows)
	}

	count, err := tx.Count(ctx, "test", "id = ?", 1)
	if err != nil {
		t.Fatalf("tx.Count failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected tx count 1, got %d", count)
	}

	deleted, err := tx.Delete(ctx, "test", "id = ?", 1)
	if err != nil {
		t.Fatalf("tx.Delete failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected tx delete 1, got %d", deleted)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "already completed") {
		t.Fatalf("expected commit completed error, got %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after commit should be nil, got %v", err)
	}

	if err := db.WithTransaction(ctx, nil, func(tx *Transaction) error {
		_, err := tx.Insert(ctx, "test", map[string]any{"name": "persisted", "value": "yes"})
		return err
	}); err != nil {
		t.Fatalf("WithTransaction success failed: %v", err)
	}

	beforeRollback, err := db.Count(ctx, "test", "")
	if err != nil {
		t.Fatalf("Count before rollback failed: %v", err)
	}

	expectedErr := errors.New("boom")
	err = db.WithTransaction(ctx, nil, func(tx *Transaction) error {
		if _, err := tx.Insert(ctx, "test", map[string]any{"name": "rolled_back", "value": "no"}); err != nil {
			return err
		}
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected rollback error %v, got %v", expectedErr, err)
	}

	afterRollback, err := db.Count(ctx, "test", "")
	if err != nil {
		t.Fatalf("Count after rollback failed: %v", err)
	}
	if afterRollback != beforeRollback {
		t.Fatalf("expected rollback to preserve count %d, got %d", beforeRollback, afterRollback)
	}

	attempts := 0
	err = db.WithTransactionRetry(ctx, nil, 3, func(tx *Transaction) error {
		attempts++
		if attempts < 3 {
			return errors.New("database is locked")
		}
		_, err := tx.Insert(ctx, "test", map[string]any{"name": "retried", "value": "ok"})
		return err
	})
	if err != nil {
		t.Fatalf("WithTransactionRetry success failed: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 retry attempts, got %d", attempts)
	}

	attempts = 0
	err = db.WithTransactionRetry(ctx, nil, 3, func(tx *Transaction) error {
		attempts++
		return errors.New("not retryable")
	})
	if err == nil || err.Error() != "not retryable" {
		t.Fatalf("expected non-retryable error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected non-retryable path to stop at 1 attempt, got %d", attempts)
	}

	attempts = 0
	err = db.WithTransactionRetry(ctx, nil, 2, func(tx *Transaction) error {
		attempts++
		return errors.New("database is busy")
	})
	if err == nil || !strings.Contains(err.Error(), "failed after 2 retries") {
		t.Fatalf("expected retry exhaustion error, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 exhausted attempts, got %d", attempts)
	}

	if isRetryableError(nil) {
		t.Fatal("nil error should not be retryable")
	}
	if !isRetryableError(errors.New("database is locked")) {
		t.Fatal("expected locked error to be retryable")
	}
	if isRetryableError(errors.New("plain failure")) {
		t.Fatal("plain failure should not be retryable")
	}

	closedDB := setupTestDB(t, DefaultConfig(":memory:"))
	if err := closedDB.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if _, err := closedDB.BeginTx(ctx, nil); err == nil || !strings.Contains(err.Error(), "begin transaction") {
		t.Fatalf("expected BeginTx error on closed db, got %v", err)
	}
}

func TestTransactionRollbackPath(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	if _, err := tx.Insert(ctx, "test", map[string]any{"name": "rolled_back", "value": "no"}); err != nil {
		t.Fatalf("tx.Insert failed: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("second Rollback should be nil, got %v", err)
	}
	if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "already completed") {
		t.Fatalf("expected commit after rollback to fail, got %v", err)
	}

	count, err := db.Count(ctx, "test", "")
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected rollback to remove inserted row, got %d", count)
	}
}

func TestConfigAndConnectionHelpers(t *testing.T) {
	db, err := Open(Config{
		MaxOpenConns:    0,
		MaxIdleConns:    4,
		BusyTimeout:     1500 * time.Millisecond,
		ConnMaxLifetime: time.Minute,
		ConnMaxIdleTime: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if db.cfg.DSN != ":memory:" {
		t.Fatalf("expected default DSN, got %s", db.cfg.DSN)
	}
	if db.cfg.MaxOpenConns != 1 {
		t.Fatalf("expected MaxOpenConns adjusted to 1, got %d", db.cfg.MaxOpenConns)
	}
	if db.cfg.MaxIdleConns != 1 {
		t.Fatalf("expected MaxIdleConns adjusted to 1, got %d", db.cfg.MaxIdleConns)
	}

	secondaryDB, err := Open(Config{
		DSN:          ":memory:",
		MaxOpenConns: 2,
		MaxIdleConns: 0,
	})
	if err != nil {
		t.Fatalf("secondary Open failed: %v", err)
	}
	defer secondaryDB.Close()
	if secondaryDB.cfg.MaxIdleConns != 1 {
		t.Fatalf("expected MaxIdleConns defaulted to 1, got %d", secondaryDB.cfg.MaxIdleConns)
	}

	pragmas := (&DB{cfg: Config{BusyTimeout: 2 * time.Second}}).buildPragmas()
	if len(pragmas) != 2 {
		t.Fatalf("expected minimal pragmas, got %#v", pragmas)
	}
	if pragmas[0] != "PRAGMA busy_timeout = 2000" || pragmas[1] != "PRAGMA optimize" {
		t.Fatalf("unexpected minimal pragmas: %#v", pragmas)
	}

	pragmas = (&DB{cfg: DefaultConfig("test.db")}).buildPragmas()
	joined := strings.Join(pragmas, "\n")
	for _, want := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = -64000",
		"PRAGMA mmap_size = 268435456",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA wal_autocheckpoint = 1000",
		"PRAGMA journal_size_limit = 67108864",
		"PRAGMA foreign_keys = ON",
		"PRAGMA optimize",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected pragma %q in %#v", want, pragmas)
		}
	}

	ctx := context.Background()
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
	if err := db.WithConn(ctx, func(conn *sql.Conn) error {
		return errors.New("callback failed")
	}); err == nil || err.Error() != "callback failed" {
		t.Fatalf("expected callback error, got %v", err)
	}
}

func TestWALHelpers(t *testing.T) {
	db, _ := setupFileTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.ExecContext(ctx, "INSERT INTO test (name) VALUES (?)", fmt.Sprintf("item-%d", i)); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
	}

	size, err := db.WALSize(ctx)
	if err != nil {
		t.Fatalf("WALSize failed: %v", err)
	}
	if size < 0 {
		t.Fatalf("expected non-negative WAL size, got %d", size)
	}

	db.checkpointWAL(ctx)
}

func TestClosedDBHelperErrorPaths(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if err := db.WithConn(ctx, func(conn *sql.Conn) error { return nil }); err == nil || !strings.Contains(err.Error(), "get connection") {
		t.Fatalf("expected WithConn closed-db error, got %v", err)
	}
	if _, err := db.WALSize(ctx); err == nil {
		t.Fatal("expected WALSize to fail on closed db")
	}
	if _, err := db.IsWALMode(ctx); err == nil {
		t.Fatal("expected IsWALMode to fail on closed db")
	}
	if _, err := db.IntegrityCheck(ctx); err == nil {
		t.Fatal("expected IntegrityCheck to fail on closed db")
	}
	if err := db.WithTransaction(ctx, nil, func(tx *Transaction) error { return nil }); err == nil || !strings.Contains(err.Error(), "begin transaction") {
		t.Fatalf("expected WithTransaction begin error, got %v", err)
	}
}
