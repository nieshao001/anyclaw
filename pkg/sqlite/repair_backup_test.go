package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupFileTestDB(t *testing.T) (*DB, string) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	cfg := DefaultConfig(dbPath)
	cfg.MaxOpenConns = 1
	cfg.MaxIdleConns = 1

	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
		os.Remove(dbPath)
		os.Remove(dbPath + "-wal")
		os.Remove(dbPath + "-shm")
	})

	return db, dbPath
}

func TestDefaultManagerConfigs(t *testing.T) {
	backupCfg := DefaultBackupConfig("backups")
	if backupCfg.BackupDir != "backups" {
		t.Fatalf("unexpected backup dir: %s", backupCfg.BackupDir)
	}
	if backupCfg.MaxBackups != 10 || backupCfg.Interval != time.Hour || backupCfg.Compress {
		t.Fatalf("unexpected default backup config: %#v", backupCfg)
	}

	repairCfg := DefaultRepairConfig()
	if !repairCfg.AutoRepair || !repairCfg.CreateBackup || repairCfg.MaxRepairTime != 5*time.Minute {
		t.Fatalf("unexpected default repair config: %#v", repairCfg)
	}
}

func TestBackupManagerLifecycleAndListing(t *testing.T) {
	db, _ := setupFileTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())

	if _, err := db.ExecContext(ctx, "CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table failed: %v", err)
	}

	backupDir := filepath.Join(t.TempDir(), "backups")
	bm := NewBackupManager(BackupConfig{
		BackupDir:  backupDir,
		MaxBackups: 5,
		Interval:   time.Hour,
	})

	if err := bm.Start(ctx, db); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := bm.Start(ctx, db); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}

	cancel()
	bm.Wait()

	validNew := filepath.Join(backupDir, "backup_20260424_100000_001.db")
	validOld := filepath.Join(backupDir, "backup_20260423_100000_001.db")
	if err := os.WriteFile(validOld, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old backup failed: %v", err)
	}
	if err := os.WriteFile(validNew, []byte("new"), 0o644); err != nil {
		t.Fatalf("write new backup failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "backup_invalid.db"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("write invalid backup failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "notes.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("write note failed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(backupDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested failed: %v", err)
	}

	backups, err := bm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 valid backups, got %#v", backups)
	}
	if backups[0].Path != validNew || backups[1].Path != validOld {
		t.Fatalf("expected backups sorted newest first, got %#v", backups)
	}

	latest, err := bm.LatestBackup()
	if err != nil {
		t.Fatalf("LatestBackup failed: %v", err)
	}
	if latest.Path != validNew {
		t.Fatalf("unexpected latest backup: %#v", latest)
	}
}

func TestBackupRestoreAndCheckpointBranches(t *testing.T) {
	db, dbPath := setupFileTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO test (name) VALUES ('before_backup')"); err != nil {
		t.Fatalf("seed insert failed: %v", err)
	}

	if err := db.Checkpoint(ctx, ""); err != nil {
		t.Fatalf("Checkpoint default mode failed: %v", err)
	}

	backupDir := filepath.Join(t.TempDir(), "backups")
	bm := NewBackupManager(DefaultBackupConfig(backupDir))

	backupPath, err := bm.BackupOnce(ctx, db)
	if err != nil {
		t.Fatalf("BackupOnce failed: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close before restore failed: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte("corrupted"), 0o644); err != nil {
		t.Fatalf("corrupt db file failed: %v", err)
	}
	if err := bm.RestoreFromBackup(ctx, db, backupPath); err != nil {
		t.Fatalf("RestoreFromBackup failed: %v", err)
	}

	reopened, err := Open(DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("reopen restored db failed: %v", err)
	}
	defer reopened.Close()

	var count int
	if err := reopened.QueryRowContext(ctx, "SELECT COUNT(*) FROM test").Scan(&count); err != nil {
		t.Fatalf("count after restore failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected restored row count 1, got %d", count)
	}

	if err := bm.RestoreFromBackup(ctx, db, filepath.Join(backupDir, "missing.db")); err == nil || !strings.Contains(err.Error(), "backup file not found") {
		t.Fatalf("expected missing backup error, got %v", err)
	}

	memDB, err := Open(InMemoryConfig())
	if err != nil {
		t.Fatalf("open in-memory db failed: %v", err)
	}
	defer memDB.Close()

	if err := bm.RestoreFromBackup(ctx, memDB, backupPath); err == nil || !strings.Contains(err.Error(), "cannot restore to in-memory") {
		t.Fatalf("expected in-memory restore error, got %v", err)
	}
}

func TestRepairManagerHelpers(t *testing.T) {
	rm := NewRepairManager(DefaultRepairConfig())
	tmpDir := t.TempDir()

	newer := filepath.Join(tmpDir, "20260424.db")
	older := filepath.Join(tmpDir, "20260423.db")
	if err := os.WriteFile(older, []byte("old"), 0o644); err != nil {
		t.Fatalf("write older backup failed: %v", err)
	}
	if err := os.WriteFile(newer, []byte("new"), 0o644); err != nil {
		t.Fatalf("write newer backup failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "ignore.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("write ignored file failed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested failed: %v", err)
	}

	backups, err := rm.listBackupFiles(tmpDir)
	if err != nil {
		t.Fatalf("listBackupFiles failed: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backup files, got %#v", backups)
	}
	if backups[0] != newer || backups[1] != older {
		t.Fatalf("expected descending backup order, got %#v", backups)
	}

	dst := filepath.Join(tmpDir, "copy.db")
	if err := rm.copyFile(newer, dst); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}
	contents, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(contents) != "new" {
		t.Fatalf("unexpected copied contents: %q", string(contents))
	}
	if err := rm.copyFile(filepath.Join(tmpDir, "missing.db"), dst); err == nil {
		t.Fatal("expected copyFile to fail for missing source")
	}

	if !containsStr("integrity check failed", "check") {
		t.Fatal("expected containsStr to find substring")
	}
	if containsStr("sqlite", "postgres") {
		t.Fatal("did not expect containsStr to match")
	}
	if !stringsHasSuffix("backup.db", ".db") {
		t.Fatal("expected stringsHasSuffix to match")
	}
	if stringsHasSuffix("backup.db", ".sqlite") {
		t.Fatal("did not expect suffix to match")
	}
	if stringsHasSuffix("db", ".sqlite") {
		t.Fatal("did not expect long suffix to match short string")
	}

	files := []string{"b", "c", "a"}
	sortByTime(files)
	if strings.Join(files, ",") != "c,b,a" {
		t.Fatalf("unexpected sorted files: %#v", files)
	}
}

func TestBackupManagerErrorBranches(t *testing.T) {
	tmpDir := t.TempDir()
	bm := NewBackupManager(DefaultBackupConfig(filepath.Join(tmpDir, "backups")))
	bm.Stop()

	memDB, err := Open(InMemoryConfig())
	if err != nil {
		t.Fatalf("open in-memory db failed: %v", err)
	}
	defer memDB.Close()

	var callbackErr error
	bm.cfg.OnBackupError = func(err error) {
		callbackErr = err
	}
	if _, err := bm.BackupOnce(context.Background(), memDB); err == nil || callbackErr == nil {
		t.Fatalf("expected BackupOnce error callback, err=%v callback=%v", err, callbackErr)
	}

	fileDB, _ := setupFileTestDB(t)
	ctx := context.Background()
	if _, err := fileDB.ExecContext(ctx, "CREATE TABLE test (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	if err := bm.performBackup(ctx, fileDB, filepath.Join(tmpDir, "missing", "backup.db")); err == nil || !strings.Contains(err.Error(), "create backup file") {
		t.Fatalf("expected performBackup create error, got %v", err)
	}
}

func TestRepairChecksAndRepairsIssues(t *testing.T) {
	db, dbPath := setupFileTestDB(t)
	ctx := context.Background()

	statements := []string{
		"CREATE TABLE parent (id INTEGER PRIMARY KEY)",
		"CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id))",
		"PRAGMA foreign_keys = OFF",
		"INSERT INTO child (id, parent_id) VALUES (1, 999)",
		"PRAGMA foreign_keys = ON",
	}
	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec %q failed: %v", stmt, err)
		}
	}

	rm := NewRepairManager(RepairConfig{
		AutoRepair:    false,
		CreateBackup:  false,
		MaxRepairTime: time.Second,
	})

	result, err := rm.CheckDatabase(ctx, db)
	if err != nil {
		t.Fatalf("CheckDatabase failed: %v", err)
	}
	if len(result.IssuesFound) == 0 {
		t.Fatal("expected foreign key issues to be reported")
	}
	if len(result.IssuesFixed) != 0 || result.Success {
		t.Fatalf("expected no fixes during check-only path, got %#v", result)
	}

	issues, err := rm.runIntegrityCheck(ctx, db)
	if err != nil {
		t.Fatalf("runIntegrityCheck failed: %v", err)
	}
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "foreign key violation") {
		t.Fatalf("expected foreign key violation in issues, got %#v", issues)
	}

	var detected []string
	var fixed []string

	repairManager := NewRepairManager(RepairConfig{
		AutoRepair:    true,
		CreateBackup:  true,
		MaxRepairTime: time.Second,
		OnIssueDetected: func(issue string) {
			detected = append(detected, issue)
		},
		OnIssueFixed: func(fix string) {
			fixed = append(fixed, fix)
		},
	})

	repairResult, err := repairManager.repair(ctx, db, []string{
		"integrity check: simulated",
		"foreign key violation: table=child, rowid=1, parent=parent",
		"some other issue",
	})
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if len(repairResult.IssuesFixed) != 3 {
		t.Fatalf("expected 3 repair actions, got %#v", repairResult.IssuesFixed)
	}
	if !repairResult.Success {
		t.Fatalf("expected repair success, got %#v", repairResult)
	}
	if repairResult.BackupCreated == "" {
		t.Fatal("expected repair backup to be created")
	}
	if _, err := os.Stat(repairResult.BackupCreated); err != nil {
		t.Fatalf("expected repair backup file: %v", err)
	}
	if len(detected) != 3 || len(fixed) != 2 {
		t.Fatalf("unexpected callback counts: detected=%#v fixed=%#v", detected, fixed)
	}

	recoveredDir := filepath.Join(t.TempDir(), "recover")
	if err := os.MkdirAll(recoveredDir, 0o755); err != nil {
		t.Fatalf("mkdir recover dir failed: %v", err)
	}
	backupCopy := filepath.Join(recoveredDir, "snapshot.db")
	if err := os.WriteFile(backupCopy, []byte("backup"), 0o644); err != nil {
		t.Fatalf("write backup snapshot failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close before recovery failed: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte("broken"), 0o644); err != nil {
		t.Fatalf("write broken db failed: %v", err)
	}
	if err := repairManager.RecoverFromBackup(ctx, db, recoveredDir); err != nil {
		t.Fatalf("RecoverFromBackup failed: %v", err)
	}

	brokenBackups, err := filepath.Glob(dbPath + ".broken.*")
	if err != nil {
		t.Fatalf("glob broken backups failed: %v", err)
	}
	if len(brokenBackups) == 0 {
		t.Fatal("expected broken database backup to be created")
	}
}

func TestRepairErrorPaths(t *testing.T) {
	db := setupTestDB(t, DefaultConfig(":memory:"))
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE repair_test (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create table failed: %v", err)
	}

	rm := NewRepairManager(DefaultRepairConfig())

	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if err := rm.QuickFix(ctx, db); err == nil || !strings.Contains(err.Error(), "quick fix") {
		t.Fatalf("expected QuickFix error on closed db, got %v", err)
	}

	checkResult, err := NewRepairManager(RepairConfig{
		AutoRepair:    false,
		CreateBackup:  false,
		MaxRepairTime: time.Second,
	}).CheckDatabase(ctx, db)
	if err != nil {
		t.Fatalf("CheckDatabase on closed db should surface issue, got %v", err)
	}
	if len(checkResult.IssuesFound) == 0 || !strings.Contains(checkResult.IssuesFound[0], "integrity check failed") {
		t.Fatalf("expected integrity issue on closed db, got %#v", checkResult)
	}

	if err := rm.RecoverFromBackup(ctx, db, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no backups found") {
		t.Fatalf("expected no backups found error, got %v", err)
	}
	if err := rm.RecoverFromBackup(ctx, db, filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "list backups") {
		t.Fatalf("expected list backups error, got %v", err)
	}
}

func TestRepairDatabaseIssuePath(t *testing.T) {
	db, _ := setupFileTestDB(t)
	ctx := context.Background()

	statements := []string{
		"CREATE TABLE parent (id INTEGER PRIMARY KEY)",
		"CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id))",
		"PRAGMA foreign_keys = OFF",
		"INSERT INTO child (id, parent_id) VALUES (1, 42)",
		"PRAGMA foreign_keys = ON",
	}
	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec %q failed: %v", stmt, err)
		}
	}

	result, err := NewRepairManager(RepairConfig{
		AutoRepair:    true,
		CreateBackup:  false,
		MaxRepairTime: time.Second,
	}).RepairDatabase(ctx, db)
	if err != nil {
		t.Fatalf("RepairDatabase failed: %v", err)
	}
	if len(result.IssuesFound) == 0 || len(result.IssuesFixed) == 0 {
		t.Fatalf("expected repair path to record issues and fixes, got %#v", result)
	}
}
