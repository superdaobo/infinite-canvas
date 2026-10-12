package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// collabPostgresMarker 用于把 PostgreSQL 契约测试放到独立子进程运行，
// 因为 repository.DB() 是进程级单例，无法在同一进程内切换驱动。
const collabPostgresMarker = "COLLAB_ROOM_POSTGRES_CHILD"

// setupCollabDB 用指定驱动初始化测试库并串行化连接。
func setupCollabDB(t *testing.T, driver, dsn string) {
	t.Helper()
	config.Cfg = config.Config{StorageDriver: driver, DatabaseDSN: dsn, AILogDir: t.TempDir()}
	db, err := repository.DB()
	if err != nil {
		t.Fatalf("初始化 %s 测试库失败: %v", driver, err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	connection.SetMaxOpenConns(1)
}

func hasRoom(list model.RoomList, roomID string) bool {
	for _, item := range list.Items {
		if item.ID == roomID {
			return true
		}
	}
	return false
}

func collabContext(userID string) context.Context {
	return WithUser(context.Background(), model.AuthUser{ID: userID})
}

// TestCollabRoomContractSQLite 在 SQLite 上跑房间/成员持久化契约。
func TestCollabRoomContractSQLite(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	runCollabRoomContract(t)
}

// TestCollabRoomContractPostgres 在目标 PostgreSQL 上跑同一契约。
// 本机没有 Docker/Postgres 时通过未设置 COLLAB_TEST_POSTGRES_DSN 自动跳过。
func TestCollabRoomContractPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("COLLAB_TEST_POSTGRES_DSN"))
	if os.Getenv(collabPostgresMarker) == "1" {
		setupCollabDB(t, "postgres", dsn)
		runCollabRoomContract(t)
		return
	}
	if dsn == "" {
		t.Skip("未设置 COLLAB_TEST_POSTGRES_DSN，跳过 PostgreSQL 契约测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollabRoomContractPostgres$", "-test.timeout=80s")
	cmd.Env = append(os.Environ(), collabPostgresMarker+"=1", "COLLAB_TEST_POSTGRES_DSN="+dsn)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PostgreSQL 契约测试失败: %v\n%s", err, output)
	}
}

// runCollabRoomContract 是驱动无关的房间/成员契约用例，正反例齐全。
func runCollabRoomContract(t *testing.T) {
	t.Helper()
	userA := newID("collab-user-a")
	userB := newID("collab-user-b")
	ctxA := collabContext(userA)
	ctxB := collabContext(userB)

	// 正例：同一用户可拥有多个 room。
	roomA1, err := CreateRoom(ctxA, "A 的房间 1")
	if err != nil {
		t.Fatalf("A 创建房间 1 失败: %v", err)
	}
	roomA2, err := CreateRoom(ctxA, "A 的房间 2")
	if err != nil {
		t.Fatalf("A 创建房间 2 失败: %v", err)
	}
	if roomA1.ID == roomA2.ID {
		t.Fatal("同一用户的两个房间 ID 不应相同")
	}
	if roomA1.OwnerID != userA || roomA2.OwnerID != userA {
		t.Fatalf("房间 owner 归属错误: %q %q", roomA1.OwnerID, roomA2.OwnerID)
	}

	listA, err := CurrentUserRooms(ctxA, model.Query{})
	if err != nil {
		t.Fatalf("A 读取房间列表失败: %v", err)
	}
	if listA.Total < 2 || !hasRoom(listA, roomA1.ID) || !hasRoom(listA, roomA2.ID) {
		t.Fatalf("A 应能看到自己的两个房间，实际 total=%d items=%v", listA.Total, listA.Items)
	}
	for _, item := range listA.Items {
		if item.Role != model.RoomRoleOwner {
			t.Fatalf("A 在自己房间的角色应为 owner，实际 %q", item.Role)
		}
	}

	// 反例：不同用户未授权看不到其他 room。
	roomB, err := CreateRoom(ctxB, "B 的房间")
	if err != nil {
		t.Fatalf("B 创建房间失败: %v", err)
	}
	listB, err := CurrentUserRooms(ctxB, model.Query{})
	if err != nil {
		t.Fatalf("B 读取房间列表失败: %v", err)
	}
	if hasRoom(listB, roomA1.ID) || hasRoom(listB, roomA2.ID) {
		t.Fatal("B 不应看到 A 的房间")
	}
	listA, err = CurrentUserRooms(ctxA, model.Query{})
	if err != nil {
		t.Fatalf("A 再次读取房间列表失败: %v", err)
	}
	if hasRoom(listA, roomB.ID) {
		t.Fatal("A 不应看到 B 的房间")
	}

	// 正例：成员可读取房间详情；反例：非成员/不存在房间统一 not_found。
	detail, err := CurrentUserRoom(ctxA, roomA1.ID)
	if err != nil || detail.ID != roomA1.ID || detail.Role != model.RoomRoleOwner {
		t.Fatalf("A 读取自己的房间详情失败: %+v %v", detail, err)
	}
	if _, err := CurrentUserRoom(ctxB, roomA1.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("B 读取 A 的房间应返回 ErrRoomNotFound，实际 %v", err)
	}
	if _, err := CurrentUserRoom(ctxA, newID("room-missing")); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("读取不存在房间应返回 ErrRoomNotFound，实际 %v", err)
	}

	// 正例：分页可用且两页不重复。
	page1, err := CurrentUserRooms(ctxA, model.Query{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("A 第 1 页读取失败: %v", err)
	}
	page2, err := CurrentUserRooms(ctxA, model.Query{Page: 2, PageSize: 1})
	if err != nil {
		t.Fatalf("A 第 2 页读取失败: %v", err)
	}
	if len(page1.Items) != 1 || len(page2.Items) != 1 || page1.Total < 2 {
		t.Fatalf("分页结果异常: page1=%+v page2=%+v", page1, page2)
	}
	if page1.Items[0].ID == page2.Items[0].ID {
		t.Fatal("分页两页不应返回同一条记录")
	}

	// 反例：非成员删除 → not_found；成员但非 owner 删除 → forbidden。
	if err := DeleteCurrentUserRoom(ctxB, roomA1.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("B 删除 A 的房间应返回 ErrRoomNotFound，实际 %v", err)
	}
	now := time.Now().UTC()
	if err := repository.SaveRoomMember(model.RoomMember{
		RoomID: roomA1.ID, UserID: userB, Role: model.RoomRoleEditor, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入 editor 成员关系失败: %v", err)
	}
	if err := DeleteCurrentUserRoom(ctxB, roomA1.ID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("editor 删除房间应返回 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := CurrentUserRoom(ctxA, roomA1.ID); err != nil {
		t.Fatalf("越权删除失败后房间应仍然存在: %v", err)
	}

	// 正例：owner 删除成功，成员关系级联删除，其他房间不受影响。
	if err := DeleteCurrentUserRoom(ctxA, roomA1.ID); err != nil {
		t.Fatalf("owner 删除房间失败: %v", err)
	}
	if _, err := CurrentUserRoom(ctxA, roomA1.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("删除后房间应不存在，实际 %v", err)
	}
	if _, ok, err := repository.FindRoomMember(roomA1.ID, userB); err != nil || ok {
		t.Fatalf("删除房间后成员关系应被清理: ok=%v err=%v", ok, err)
	}
	if _, err := CurrentUserRoom(ctxA, roomA2.ID); err != nil {
		t.Fatalf("删除一个房间不应影响同用户的其他房间: %v", err)
	}

	// 反例：未登录（context 无用户）时全部拒绝。
	if _, err := CreateRoom(context.Background(), "匿名房间"); !errors.Is(err, ErrRoomInvalid) {
		t.Fatalf("未登录创建房间应返回 ErrRoomInvalid，实际 %v", err)
	}
	if _, err := CurrentUserRooms(context.Background(), model.Query{}); !errors.Is(err, ErrRoomInvalid) {
		t.Fatalf("未登录读取房间列表应返回 ErrRoomInvalid，实际 %v", err)
	}
	if _, err := CurrentUserRoom(context.Background(), roomA2.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("未登录读取房间详情应返回 ErrRoomNotFound，实际 %v", err)
	}
	if err := DeleteCurrentUserRoom(context.Background(), roomA2.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("未登录删除房间应返回 ErrRoomNotFound，实际 %v", err)
	}

	// 关键断言：删除协作房间不误触个人画布。
	project := model.CanvasProject{
		UserID:      userA,
		ID:          newID("canvas-project"),
		ProjectData: `{"id":"p1","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`,
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-01-01T00:00:00Z",
	}
	if _, err := repository.SaveUserCanvasProject(project); err != nil {
		t.Fatalf("写入个人画布项目失败: %v", err)
	}
	if err := DeleteCurrentUserRoom(ctxA, roomA2.ID); err != nil {
		t.Fatalf("删除第二个房间失败: %v", err)
	}
	projects, err := repository.ListUserCanvasProjects(userA)
	if err != nil {
		t.Fatalf("读取个人画布项目失败: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != project.ID || projects[0].ProjectData != project.ProjectData {
		t.Fatalf("删除协作房间不应改动个人画布，实际 %+v", projects)
	}
}
