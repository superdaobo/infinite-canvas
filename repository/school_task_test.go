package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// 学校队列数据库层约束测试（C03-02 / #18）。用文件型 SQLite + 多连接真实并发，
// 验证事务领取不双领、running_slot 唯一索引保证全局单执行槽、幂等唯一键。

// schoolQueueDBOnce 保证整个测试二进制只初始化一次文件型 SQLite（DB() 是进程级单例）。
var (
	schoolQueueDBOnce sync.Once
	schoolQueueDBDir  string
)

// TestMain 在全部用例结束后清理文件型测试库目录。
func TestMain(m *testing.M) {
	code := m.Run()
	if schoolQueueDBDir != "" {
		_ = os.RemoveAll(schoolQueueDBDir)
	}
	os.Exit(code)
}

// schoolQueueTestDB 用文件型 SQLite 初始化队列测试库（允许 8 个并发连接）。
func schoolQueueTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	schoolQueueDBOnce.Do(func() {
		dir, err := os.MkdirTemp("", "school-queue-test-*")
		if err != nil {
			panic(err)
		}
		schoolQueueDBDir = dir
		dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "school-queue.db")) +
			"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
		config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: dsn}
	})
	db, err := DB()
	if err != nil {
		t.Fatalf("初始化 SQLite 文件库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	if err := db.Exec("DELETE FROM school_tasks").Error; err != nil {
		t.Fatalf("清空 school_tasks 失败: %v", err)
	}
	return db
}

func queuedSchoolTask(index int) model.SchoolTask {
	now := time.Now().UTC()
	return model.SchoolTask{
		ID:           fmt.Sprintf("school_task_%03d", index),
		RoomID:       "room-school-queue",
		SubmittedBy:  fmt.Sprintf("user-%d", index%2),
		ClientTaskID: fmt.Sprintf("client-%03d", index),
		Channel:      model.SchoolChannelQwen,
		Status:       model.SchoolTaskQueued,
		Params:       model.SchoolTaskParams{Prompt: fmt.Sprintf("p-%d", index)},
		CreatedAt:    now.Add(time.Duration(index) * time.Millisecond),
		UpdatedAt:    now,
	}
}

// TestSchoolTaskQueuePersistence 覆盖并发领取、单 running 约束与幂等唯一键。
func TestSchoolTaskQueuePersistence(t *testing.T) {
	db := schoolQueueTestDB(t)
	const total = 8

	// --- 并发领取：恰好一个事务领到任务，数据库层只有一个 running ---
	for i := 0; i < total; i++ {
		if created, err := InsertSchoolTask(queuedSchoolTask(i)); err != nil || !created {
			t.Fatalf("插入 queued 任务失败: created=%v err=%v", created, err)
		}
	}
	var waitGroup sync.WaitGroup
	claimedIDs := make([]string, total)
	errorsByIndex := make([]error, total)
	for i := 0; i < total; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			task, ok, err := ClaimNextQueuedSchoolTask(fmt.Sprintf("exec-%d", index))
			if err != nil {
				errorsByIndex[index] = err
				return
			}
			if ok {
				claimedIDs[index] = task.ID
			}
		}(i)
	}
	waitGroup.Wait()

	claimed := 0
	var winner string
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("并发领取返回错误（第 %d 个）: %v", index, err)
		}
		if claimedIDs[index] != "" {
			claimed++
			winner = claimedIDs[index]
		}
	}
	if claimed != 1 {
		t.Fatalf("并发领取应恰好 1 个成功，实际 %d 个: %v", claimed, claimedIDs)
	}
	if winner != "school_task_000" {
		t.Fatalf("单执行槽应领到最早的 queued 任务，实际 %s", winner)
	}
	running, err := ListRunningSchoolTasks()
	if err != nil || len(running) != 1 || running[0].ID != winner {
		t.Fatalf("数据库应只有 1 个 running，实际 %+v err=%v", running, err)
	}

	// --- 唯一索引：直接写入第二个 running 槽位必须被数据库拒绝 ---
	if err := db.Exec("UPDATE school_tasks SET status='running', running_slot=1 WHERE id = ? AND id <> ?",
		"school_task_001", winner).Error; err == nil {
		t.Fatal("第二个 running_slot 应被唯一索引拒绝")
	}

	// --- 幂等唯一键：并发提交同一 (room,user,clientTaskId) 只落一条 ---
	if err := db.Exec("DELETE FROM school_tasks").Error; err != nil {
		t.Fatalf("清空 school_tasks 失败: %v", err)
	}
	var created int
	var waitGroup2 sync.WaitGroup
	flags := make([]bool, total)
	for i := 0; i < total; i++ {
		waitGroup2.Add(1)
		go func(index int) {
			defer waitGroup2.Done()
			task := queuedSchoolTask(0)
			task.ID = "school_task_idem"
			task.RoomID = "room-idem"
			task.SubmittedBy = "user-idem"
			task.ClientTaskID = "client-idem"
			ok, err := InsertSchoolTask(task)
			if err != nil {
				t.Errorf("并发幂等插入返回错误: %v", err)
				return
			}
			flags[index] = ok
		}(i)
	}
	waitGroup2.Wait()
	for _, ok := range flags {
		if ok {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("并发幂等插入应只有 1 条落库，实际 %d", created)
	}
	var count int64
	if err := db.Model(&model.SchoolTaskRecord{}).Count(&count).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("幂等键应只保留 1 条记录，实际 %d", count)
	}
	if _, ok, err := FindSchoolTaskByIdempotency("room-idem", "user-idem", "client-idem"); err != nil || !ok {
		t.Fatalf("幂等键应能读回唯一记录: ok=%v err=%v", ok, err)
	}
	if _, ok, err := FindSchoolTaskByIdempotency("room-idem", "user-idem", "client-other"); err != nil || ok {
		t.Fatalf("不同幂等键不应命中: ok=%v err=%v", ok, err)
	}
}

// TestSchoolTaskUpstreamBindingPersistence 验证上游任务号首次绑定不可替换、
// 结束任务释放执行槽，以及取消只作用于 queued。
func TestSchoolTaskUpstreamBindingPersistence(t *testing.T) {
	schoolQueueTestDB(t)
	task := queuedSchoolTask(0)
	if created, err := InsertSchoolTask(task); err != nil || !created {
		t.Fatalf("插入任务失败: %v", err)
	}
	claimed, ok, err := ClaimNextQueuedSchoolTask("exec-1")
	if err != nil || !ok || claimed.ID != task.ID {
		t.Fatalf("领取任务失败: ok=%v err=%v task=%+v", ok, err, claimed)
	}
	if set, err := SetSchoolTaskUpstream(task.ID, "upstream-1"); err != nil || !set {
		t.Fatalf("首次绑定上游任务号失败: set=%v err=%v", set, err)
	}
	if set, err := SetSchoolTaskUpstream(task.ID, "upstream-2"); err != nil || set {
		t.Fatalf("已绑定的上游任务号不可替换: set=%v err=%v", set, err)
	}
	stored, ok, err := FindSchoolTaskByID(task.ID)
	if err != nil || !ok || stored.UpstreamTaskID != "upstream-1" {
		t.Fatalf("上游任务号应保持首次绑定值，实际 %+v err=%v", stored, err)
	}
	// 取消 running 任务无效。
	if cancelled, err := CancelQueuedSchoolTask(task.RoomID, task.ID, task.SubmittedBy); err != nil || cancelled {
		t.Fatalf("取消 running 任务不应命中: cancelled=%v err=%v", cancelled, err)
	}
	// 结束任务后释放执行槽。
	if changed, err := FinishRunningSchoolTask(task.ID, "exec-1", model.SchoolTaskSucceeded, ""); err != nil || !changed {
		t.Fatalf("结束 running 任务失败: changed=%v err=%v", changed, err)
	}
	if running, err := ListRunningSchoolTasks(); err != nil || len(running) != 0 {
		t.Fatalf("结束后不应有 running，实际 %+v err=%v", running, err)
	}
	if _, _, err := ClaimNextQueuedSchoolTask("exec-2"); err != nil {
		t.Fatalf("结束后领取失败: %v", err)
	}

	// 取消 queued：仅本人命中。
	queued := queuedSchoolTask(1)
	if created, err := InsertSchoolTask(queued); err != nil || !created {
		t.Fatalf("插入 queued 任务失败: %v", err)
	}
	if cancelled, err := CancelQueuedSchoolTask(queued.RoomID, queued.ID, "someone-else"); err != nil || cancelled {
		t.Fatalf("他人取消不应命中: cancelled=%v err=%v", cancelled, err)
	}
	if cancelled, err := CancelQueuedSchoolTask(queued.RoomID, queued.ID, queued.SubmittedBy); err != nil || !cancelled {
		t.Fatalf("本人取消 queued 应命中: cancelled=%v err=%v", cancelled, err)
	}
	if _, _, err := FindSchoolTaskByID(queued.ID); err != nil {
		t.Fatalf("取消后任务应仍可读取: %v", err)
	}
}

// TestSchoolTaskRecordRoundTrip 验证领域模型与持久化行的无损互转。
func TestSchoolTaskRecordRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	lastSeen := now.Add(-time.Minute)
	task := model.SchoolTask{
		ID: "school_roundtrip", RoomID: "room-1", SubmittedBy: "user-1", ExecutorID: "exec-1",
		ClientTaskID: "client-1", UpstreamTaskID: "upstream-1", Channel: model.SchoolChannelH3,
		Status: model.SchoolTaskRunning,
		Params: model.SchoolTaskParams{Prompt: "p", ReferenceImages: []string{"a", "b"}, VideoSeconds: 6, Size: "1080x1920", Quality: "high"},
		Error:  "note", LastSeenAt: &lastSeen, CreatedAt: now, UpdatedAt: now,
	}
	record := model.NewSchoolTaskRecord(task)
	if record.RunningSlot == nil || *record.RunningSlot != model.SchoolTaskRunningSlot {
		t.Fatalf("running 任务应占用执行槽: %+v", record.RunningSlot)
	}
	restored := record.ToSchoolTask()
	if restored.UpstreamTaskID != task.UpstreamTaskID || restored.Channel != task.Channel || restored.Status != task.Status {
		t.Fatalf("往返后字段不一致: %+v", restored)
	}
	if len(restored.Params.ReferenceImages) != 2 || restored.Params.VideoSeconds != 6 || restored.Params.Prompt != "p" {
		t.Fatalf("参数 JSON 往返不一致: %+v", restored.Params)
	}
	if restored.LastSeenAt == nil || !restored.LastSeenAt.Equal(lastSeen) {
		t.Fatalf("心跳时间往返不一致: %+v", restored.LastSeenAt)
	}
	// 非 running 不占用执行槽。
	task.Status = model.SchoolTaskQueued
	if record := model.NewSchoolTaskRecord(task); record.RunningSlot != nil {
		t.Fatalf("queued 任务不应占用执行槽: %+v", record.RunningSlot)
	}
}

// TestIsDuplicateKeyError 验证唯一键冲突识别覆盖各驱动报文。
func TestIsDuplicateKeyError(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{"UNIQUE constraint failed: school_tasks.id", true},
		{"Error 1062 (23000): Duplicate entry 'x' for key 'id'", true},
		{`pq: duplicate key value violates unique constraint "school_tasks_pkey"`, true},
		{"no such table: school_tasks", false},
	}
	for _, testCase := range cases {
		if got := isDuplicateKeyError(errors.New(testCase.message)); got != testCase.want {
			t.Fatalf("isDuplicateKeyError(%q) = %v, want %v", testCase.message, got, testCase.want)
		}
	}
	if !isDuplicateKeyError(gorm.ErrDuplicatedKey) {
		t.Fatal("gorm.ErrDuplicatedKey 应被识别")
	}
}
