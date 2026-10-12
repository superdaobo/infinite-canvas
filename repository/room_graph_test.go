package repository

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 本文件验证图写入的数据库条件 CAS（P1 覆盖写入修复）。
// 使用文件型 SQLite（非 :memory:）+ 多个独立连接，真实走数据库 WHERE version=? AND deleted=false。

// graphTestDSN 返回一个文件型 SQLite DSN（WAL + busy_timeout，允许并发连接）。
func graphTestDSN(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "room-graph-cas.db")
	return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
}

// openGraphHandle 打开一个独立的 gorm 连接句柄（独占 1 个底层连接）。
func openGraphHandle(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开 SQLite 文件库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	// Windows 下必须显式关闭句柄，否则 TempDir 清理会因文件被占用而失败。
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func migrateGraph(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.RoomNode{}, &model.RoomEdge{}); err != nil {
		t.Fatalf("迁移图测试表失败: %v", err)
	}
}

func seedNode(t *testing.T, db *gorm.DB, roomID, id, title string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&model.RoomNode{
		RoomID: roomID, ID: id, Kind: "image", Title: title,
		Width: 10, Height: 10, Version: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("写入节点种子失败: %v", err)
	}
}

func seedEdge(t *testing.T, db *gorm.DB, roomID, id, from, to string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&model.RoomEdge{
		RoomID: roomID, ID: id, FromNodeID: from, ToNodeID: to,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("写入连线种子失败: %v", err)
	}
}

func loadNode(t *testing.T, db *gorm.DB, roomID, id string) model.RoomNode {
	t.Helper()
	var row model.RoomNode
	if err := db.First(&row, "room_id = ? AND id = ?", roomID, id).Error; err != nil {
		t.Fatalf("读取节点失败: %v", err)
	}
	return row
}

func loadEdge(t *testing.T, db *gorm.DB, roomID, id string) model.RoomEdge {
	t.Helper()
	var row model.RoomEdge
	if err := db.First(&row, "room_id = ? AND id = ?", roomID, id).Error; err != nil {
		t.Fatalf("读取连线失败: %v", err)
	}
	return row
}

// TestRoomNodeUpdateCASPreventsConcurrentOverwrite 构造审查描述的真实覆盖写入窗口：
// 两个独立连接读到同一旧版本后先后提交，后提交者被数据库 CAS 拒绝（RowsAffected==0），
// 先提交者的修改不被覆盖。SQLite 单写锁无法让两个写事务真正交叠，故用两个独立连接
// 复现「都读到 v1 → A 提交 → B 用 v1 提交」这一等价交错。
func TestRoomNodeUpdateCASPreventsConcurrentOverwrite(t *testing.T) {
	dsn := graphTestDSN(t)
	dbA := openGraphHandle(t, dsn)
	dbB := openGraphHandle(t, dsn)
	migrateGraph(t, dbA)
	roomID, nodeID := "room-cas-1", "img_cas_0001"
	seedNode(t, dbA, roomID, nodeID, "原始")

	// 两个独立连接读到同一版本 1。
	readA := loadNode(t, dbA, roomID, nodeID)
	readB := loadNode(t, dbB, roomID, nodeID)
	if readA.Version != 1 || readB.Version != 1 {
		t.Fatalf("两个连接应读到同一版本 1，实际 %d / %d", readA.Version, readB.Version)
	}

	// A 先提交。
	var affectedA int64
	if err := dbA.Transaction(func(tx *gorm.DB) error {
		var err error
		affectedA, err = casUpdateNode(tx, roomID, nodeID, readA.Version, map[string]any{
			"title": "A 的修改", "version": readA.Version + 1, "updated_at": time.Now().UTC(),
		})
		return err
	}); err != nil {
		t.Fatalf("A 提交失败: %v", err)
	}

	// B 用同一旧版本提交：数据库 CAS 必须拒绝（RowsAffected==0）。
	var affectedB int64
	if err := dbB.Transaction(func(tx *gorm.DB) error {
		var err error
		affectedB, err = casUpdateNode(tx, roomID, nodeID, readB.Version, map[string]any{
			"title": "B 的修改", "version": readB.Version + 1, "updated_at": time.Now().UTC(),
		})
		return err
	}); err != nil {
		t.Fatalf("B 提交返回错误（应为 0 行而非错误）: %v", err)
	}

	if affectedA != 1 {
		t.Fatalf("A 的 CAS 应影响 1 行，实际 %d", affectedA)
	}
	if affectedB != 0 {
		t.Fatalf("B 的陈旧版本 CAS 应影响 0 行，实际 %d", affectedB)
	}
	final := loadNode(t, dbA, roomID, nodeID)
	if final.Version != 2 || final.Title != "A 的修改" {
		t.Fatalf("B 不得覆盖 A 的修改，期望 version=2 title=A 的修改，实际 %+v", final)
	}

	// CAS 影响 0 行时映射为既有 version_conflict 语义，且携带最新版本。
	if err := nodeWriteConflict(dbB, roomID, nodeID, readB); err == nil {
		t.Fatal("nodeWriteConflict 应返回错误")
	} else if code := contractCodeOf(err); code != "version_conflict" {
		t.Fatalf("CAS 冲突应映射为 version_conflict，实际 %s", code)
	}
}

// TestRoomNodeDeleteCASRejectsStaleVersion 验证删除同样走数据库 CAS：
// 陈旧版本删除被拒绝（0 行），且不会误删已被并发更新的节点。
func TestRoomNodeDeleteCASRejectsStaleVersion(t *testing.T) {
	dsn := graphTestDSN(t)
	dbA := openGraphHandle(t, dsn)
	dbB := openGraphHandle(t, dsn)
	migrateGraph(t, dbA)
	roomID, nodeID := "room-cas-del", "img_cas_del_01"
	seedNode(t, dbA, roomID, nodeID, "原始")

	readB := loadNode(t, dbB, roomID, nodeID) // 读到 v1

	// A 先更新到 v2 并提交。
	var affectedUpdate int64
	if err := dbA.Transaction(func(tx *gorm.DB) error {
		var err error
		affectedUpdate, err = casUpdateNode(tx, roomID, nodeID, 1, map[string]any{
			"title": "A 更新", "version": 2, "updated_at": time.Now().UTC(),
		})
		return err
	}); err != nil || affectedUpdate != 1 {
		t.Fatalf("A 更新应成功，affected=%d err=%v", affectedUpdate, err)
	}

	// B 用陈旧 v1 删除：CAS 拒绝，节点仍存活且为 v2。
	var affectedDelete int64
	if err := dbB.Transaction(func(tx *gorm.DB) error {
		var err error
		affectedDelete, err = casUpdateNode(tx, roomID, nodeID, readB.Version, map[string]any{
			"deleted": true, "version": readB.Version + 1, "updated_at": time.Now().UTC(),
		})
		return err
	}); err != nil {
		t.Fatalf("陈旧删除不应返回错误，实际 %v", err)
	}
	if affectedDelete != 0 {
		t.Fatalf("陈旧版本删除应影响 0 行，实际 %d", affectedDelete)
	}
	after := loadNode(t, dbA, roomID, nodeID)
	if after.Deleted || after.Version != 2 || after.Title != "A 更新" {
		t.Fatalf("陈旧删除不得生效，实际 %+v", after)
	}

	// 正确版本删除成功。
	var affectedDeleteOK int64
	if err := dbA.Transaction(func(tx *gorm.DB) error {
		var err error
		affectedDeleteOK, err = casUpdateNode(tx, roomID, nodeID, 2, map[string]any{
			"deleted": true, "version": 3, "updated_at": time.Now().UTC(),
		})
		return err
	}); err != nil || affectedDeleteOK != 1 {
		t.Fatalf("正确版本删除应成功，affected=%d err=%v", affectedDeleteOK, err)
	}
	if deleted := loadNode(t, dbA, roomID, nodeID); !deleted.Deleted || deleted.Version != 3 {
		t.Fatalf("删除后应为 deleted=true version=3，实际 %+v", deleted)
	}
}

// TestRoomEdgeCASRejectsStaleVersion 验证连线更新/删除同样走数据库 CAS。
func TestRoomEdgeCASRejectsStaleVersion(t *testing.T) {
	dsn := graphTestDSN(t)
	dbA := openGraphHandle(t, dsn)
	dbB := openGraphHandle(t, dsn)
	migrateGraph(t, dbA)
	roomID, edgeID := "room-cas-edge", "edg_cas_0001"
	seedEdge(t, dbA, roomID, edgeID, "n1", "n2")

	readB := loadEdge(t, dbB, roomID, edgeID) // 读到 v1

	// A 先更新到 v2 并提交。
	var affectedA int64
	if err := dbA.Transaction(func(tx *gorm.DB) error {
		var err error
		affectedA, err = casUpdateEdge(tx, roomID, edgeID, 1, map[string]any{
			"to_node_id": "n3", "version": 2, "updated_at": time.Now().UTC(),
		})
		return err
	}); err != nil || affectedA != 1 {
		t.Fatalf("A 连线更新应成功，affected=%d err=%v", affectedA, err)
	}

	// B 用陈旧 v1 更新/删除：CAS 拒绝。
	var affectedB int64
	if err := dbB.Transaction(func(tx *gorm.DB) error {
		var err error
		affectedB, err = casUpdateEdge(tx, roomID, edgeID, readB.Version, map[string]any{
			"to_node_id": "n9", "version": readB.Version + 1, "updated_at": time.Now().UTC(),
		})
		return err
	}); err != nil || affectedB != 0 {
		t.Fatalf("陈旧连线 CAS 应影响 0 行，affected=%d err=%v", affectedB, err)
	}
	after := loadEdge(t, dbA, roomID, edgeID)
	if after.Version != 2 || after.ToNodeID != "n3" {
		t.Fatalf("陈旧写入不得覆盖连线，实际 %+v", after)
	}

	// edgeWriteConflict 映射为 edge_changed。
	if err := edgeWriteConflict(dbB, roomID, edgeID); err == nil {
		t.Fatal("edgeWriteConflict 应返回错误")
	} else if code := contractCodeOf(err); code != "edge_changed" {
		t.Fatalf("连线 CAS 冲突应映射为 edge_changed，实际 %s", code)
	}
}

// TestRoomNodeCASConcurrentNoLostUpdate 用文件型 SQLite + 8 个并发写入真实并发：
// 每个 goroutine 先读版本再按该版本 CAS 写入。断言同一期望版本不会被两个写入同时
// 成功（无丢失更新），且最终版本号 == 初始版本 + 成功次数。
func TestRoomNodeCASConcurrentNoLostUpdate(t *testing.T) {
	dsn := graphTestDSN(t)
	db := openGraphHandle(t, dsn)
	// 并发场景放开连接池。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	migrateGraph(t, db)
	roomID, nodeID := "room-cas-conc", "img_cas_conc_1"
	seedNode(t, db, roomID, nodeID, "原始")

	const writers = 8
	var wg sync.WaitGroup
	affected := make([]int64, writers)
	expected := make([]int, writers)
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var row model.RoomNode
			if err := db.First(&row, "room_id = ? AND id = ?", roomID, nodeID).Error; err != nil {
				errs[i] = err
				return
			}
			expected[i] = row.Version
			errs[i] = db.Transaction(func(tx *gorm.DB) error {
				got, err := casUpdateNode(tx, roomID, nodeID, row.Version, map[string]any{
					"title": fmt.Sprintf("writer-%d", i), "version": row.Version + 1, "updated_at": time.Now().UTC(),
				})
				affected[i] = got
				return err
			})
		}(i)
	}
	wg.Wait()

	successes := 0
	seenVersions := map[int]bool{}
	for i := 0; i < writers; i++ {
		if errs[i] != nil {
			t.Fatalf("并发 CAS 第 %d 个返回错误: %v", i, errs[i])
		}
		if affected[i] == 1 {
			successes++
			if seenVersions[expected[i]] {
				t.Fatalf("期望版本 %d 被多个写入同时成功，发生丢失更新", expected[i])
			}
			seenVersions[expected[i]] = true
		} else if affected[i] != 0 {
			t.Fatalf("CAS 影响行数只能为 0 或 1，实际 %d", affected[i])
		}
	}
	if successes < 1 {
		t.Fatal("并发写入至少应有 1 次成功")
	}
	final := loadNode(t, db, roomID, nodeID)
	if final.Version != 1+successes {
		t.Fatalf("最终版本应等于初始版本+成功次数=%d，实际 %d", 1+successes, final.Version)
	}
}

// contractCodeOf 取出 *contract.Error 的 Code，便于测试断言。
func contractCodeOf(err error) string {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}
