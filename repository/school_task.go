package repository

import (
	"errors"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// 学校任务队列持久化（C03-02 / #18）。所有单执行槽约束都在数据库层：
//   - 幂等唯一键 (room_id, submitted_by, client_task_id)；
//   - running_slot 唯一索引：只有 running 任务写非 NULL 槽位，其余为 NULL。
//
// 领取走单事务：先确认没有 running，再按 created_at/id FIFO 取下一个 queued
// 并置为 running。即使并发事务同时通过「无 running」检查，running_slot 唯一
// 索引也会让其中一个失败，因此不会双领。

// FindSchoolTaskByIdempotency 按幂等唯一键读取任务；不存在时返回 ok=false。
func FindSchoolTaskByIdempotency(roomID, submittedBy, clientTaskID string) (model.SchoolTask, bool, error) {
	db, err := DB()
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	var record model.SchoolTaskRecord
	err = db.First(&record,
		"room_id = ? AND submitted_by = ? AND client_task_id = ?",
		strings.TrimSpace(roomID),
		strings.TrimSpace(submittedBy),
		strings.TrimSpace(clientTaskID),
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.SchoolTask{}, false, nil
	}
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	return record.ToSchoolTask(), true, nil
}

// InsertSchoolTask 插入一条 queued 任务；命中幂等唯一键时返回 created=false，
// 由调用方读取既有任务后按幂等语义处理。
func InsertSchoolTask(task model.SchoolTask) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	record := model.NewSchoolTaskRecord(task)
	if err := db.Create(&record).Error; err != nil {
		if isDuplicateKeyError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FindSchoolTaskByID 按任务 ID 读取；不存在时返回 ok=false。
func FindSchoolTaskByID(taskID string) (model.SchoolTask, bool, error) {
	db, err := DB()
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	var record model.SchoolTaskRecord
	err = db.First(&record, "id = ?", strings.TrimSpace(taskID)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.SchoolTask{}, false, nil
	}
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	return record.ToSchoolTask(), true, nil
}

// ListSchoolTasksByRoom 返回房间内全部任务，按 created_at/id 升序（FIFO 可见顺序）。
func ListSchoolTasksByRoom(roomID string) ([]model.SchoolTask, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var records []model.SchoolTaskRecord
	if err := db.Where("room_id = ?", strings.TrimSpace(roomID)).
		Order("created_at ASC, id ASC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	tasks := make([]model.SchoolTask, 0, len(records))
	for _, record := range records {
		tasks = append(tasks, record.ToSchoolTask())
	}
	return tasks, nil
}

// FindRunningSchoolTask 返回当前 running 任务（单执行槽，最多一条）。
func FindRunningSchoolTask() (model.SchoolTask, bool, error) {
	db, err := DB()
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	var record model.SchoolTaskRecord
	err = db.First(&record, "status = ?", model.SchoolTaskRunning).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.SchoolTask{}, false, nil
	}
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	return record.ToSchoolTask(), true, nil
}

// ListRunningSchoolTasks 返回全部 running 任务，用于启动恢复统计（正常最多一条）。
func ListRunningSchoolTasks() ([]model.SchoolTask, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var records []model.SchoolTaskRecord
	if err := db.Where("status = ?", model.SchoolTaskRunning).Find(&records).Error; err != nil {
		return nil, err
	}
	tasks := make([]model.SchoolTask, 0, len(records))
	for _, record := range records {
		tasks = append(tasks, record.ToSchoolTask())
	}
	return tasks, nil
}

// CountStaleRunningSchoolTasks 统计心跳早于 cutoff（或从未心跳）的 running 任务数，
// 用于识别设备中断；不修改这些任务的状态（恢复语义要求 running 不被改写）。
func CountStaleRunningSchoolTasks(cutoff time.Time) (int64, error) {
	db, err := DB()
	if err != nil {
		return 0, err
	}
	var count int64
	err = db.Model(&model.SchoolTaskRecord{}).
		Where("status = ? AND (last_seen_at IS NULL OR last_seen_at < ?)", model.SchoolTaskRunning, cutoff).
		Count(&count).Error
	return count, err
}

// CountQueuedSchoolTasksByUser 统计某用户在某房间仍排队（queued）的任务数。
func CountQueuedSchoolTasksByUser(roomID, submittedBy string) (int64, error) {
	db, err := DB()
	if err != nil {
		return 0, err
	}
	var count int64
	err = db.Model(&model.SchoolTaskRecord{}).
		Where("room_id = ? AND submitted_by = ? AND status = ?",
			strings.TrimSpace(roomID), strings.TrimSpace(submittedBy), model.SchoolTaskQueued).
		Count(&count).Error
	return count, err
}

// ClaimNextQueuedSchoolTask 在单事务内领取下一个 queued 任务并置为 running：
// 仅当没有 running 任务时才领取，按 created_at/id FIFO 取最早的一条。
// 返回 ok=false 表示当前无可领取任务（队列空或单执行槽已被占用）。
func ClaimNextQueuedSchoolTask(executorID string) (model.SchoolTask, bool, error) {
	return ClaimNextQueuedSchoolTaskForAccount(executorID, "")
}

// ClaimNextQueuedSchoolTaskForAccount 与 ClaimNextQueuedSchoolTask 相同，但额外把
// 执行设备当前的学校账号指纹绑定写入任务（C03-03 / #19 账号绑定）。accountRef 为空
// 表示不绑定账号，沿用旧语义。
func ClaimNextQueuedSchoolTaskForAccount(executorID, accountRef string) (model.SchoolTask, bool, error) {
	executorID = strings.TrimSpace(executorID)
	if executorID == "" {
		return model.SchoolTask{}, false, errors.New("执行设备 ID 不能为空")
	}
	// 并发下 SQLite 可能返回 SQLITE_BUSY；重试后即可观察到已提交的 running 任务。
	for attempt := 0; attempt < 5; attempt++ {
		task, ok, err := claimQueuedSchoolTaskOnce(executorID, strings.TrimSpace(accountRef))
		if err == nil {
			return task, ok, nil
		}
		if !isRetryableLockError(err) {
			return model.SchoolTask{}, false, err
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return model.SchoolTask{}, false, nil
}

func claimQueuedSchoolTaskOnce(executorID, accountRef string) (model.SchoolTask, bool, error) {
	db, err := DB()
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	var claimed model.SchoolTask
	var ok bool
	err = db.Transaction(func(tx *gorm.DB) error {
		var runningCount int64
		if err := tx.Model(&model.SchoolTaskRecord{}).
			Where("status = ?", model.SchoolTaskRunning).Count(&runningCount).Error; err != nil {
			return err
		}
		if runningCount > 0 {
			return nil // 单执行槽已被占用
		}
		var record model.SchoolTaskRecord
		err := tx.Where("status = ?", model.SchoolTaskQueued).
			Order("created_at ASC, id ASC").First(&record).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		slot := model.SchoolTaskRunningSlot
		result := tx.Model(&model.SchoolTaskRecord{}).
			Where("id = ? AND status = ?", record.ID, model.SchoolTaskQueued).
			Updates(map[string]any{
				"status":             string(model.SchoolTaskRunning),
				"executor_id":        executorID,
				"school_account_ref": accountRef,
				"running_slot":       slot,
				"last_seen_at":       now,
				"updated_at":         now,
			})
		if result.Error != nil {
			// running_slot 唯一索引被并发事务抢占：本次没有领到任务。
			if isDuplicateKeyError(result.Error) {
				return nil
			}
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil // 目标任务已被其他事务领取
		}
		record.Status = string(model.SchoolTaskRunning)
		record.ExecutorID = executorID
		record.SchoolAccountRef = accountRef
		record.RunningSlot = &slot
		record.LastSeenAt = &now
		record.UpdatedAt = now
		claimed = record.ToSchoolTask()
		ok = true
		return nil
	})
	return claimed, ok, err
}

// ListSchoolTasksByExecutor 返回该执行设备领取过的任务，按创建顺序升序。
// 设备侧只能看到绑定到自己的任务，绝不返回其他设备或房间的任务。
func ListSchoolTasksByExecutor(executorID string) ([]model.SchoolTask, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	var records []model.SchoolTaskRecord
	if err := db.Where("executor_id = ?", strings.TrimSpace(executorID)).
		Order("created_at ASC, id ASC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	tasks := make([]model.SchoolTask, 0, len(records))
	for _, record := range records {
		tasks = append(tasks, record.ToSchoolTask())
	}
	return tasks, nil
}

// CancelQueuedSchoolTask 取消本人提交的 queued 任务；返回是否命中记录。
func CancelQueuedSchoolTask(roomID, taskID, submittedBy string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolTaskRecord{}).
		Where("id = ? AND room_id = ? AND submitted_by = ? AND status = ?",
			strings.TrimSpace(taskID), strings.TrimSpace(roomID), strings.TrimSpace(submittedBy), model.SchoolTaskQueued).
		Updates(map[string]any{
			"status":     string(model.SchoolTaskCancelled),
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// TouchRunningSchoolTask 更新 running 任务的心跳时间（仅限当前执行设备）。
func TouchRunningSchoolTask(taskID, executorID string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolTaskRecord{}).
		Where("id = ? AND executor_id = ? AND status = ?",
			strings.TrimSpace(taskID), strings.TrimSpace(executorID), model.SchoolTaskRunning).
		Updates(map[string]any{"last_seen_at": time.Now().UTC(), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// SetSchoolTaskUpstream 写入上游任务号；只在当前为空时生效（首次绑定不可替换）。
func SetSchoolTaskUpstream(taskID, upstreamTaskID string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolTaskRecord{}).
		Where("id = ? AND upstream_task_id = ''", strings.TrimSpace(taskID)).
		Updates(map[string]any{
			"upstream_task_id": strings.TrimSpace(upstreamTaskID),
			"updated_at":       time.Now().UTC(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// FinishRunningSchoolTask 结束 running 任务并释放执行槽（running_slot 置 NULL）。
// 只允许当前执行设备结束自己持有的 running 任务。
func FinishRunningSchoolTask(taskID, executorID string, status model.SchoolTaskStatus, errMsg string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolTaskRecord{}).
		Where("id = ? AND executor_id = ? AND status = ?",
			strings.TrimSpace(taskID), strings.TrimSpace(executorID), model.SchoolTaskRunning).
		Updates(map[string]any{
			"status":       string(status),
			"running_slot": nil,
			"error":        errMsg,
			"updated_at":   time.Now().UTC(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// RebindRunningSchoolTaskExecutor 把 running 任务重新绑定到新执行设备，
// 保留原 upstream_task_id 与 running 状态；note 作为可审计说明写入 error 字段。
func RebindRunningSchoolTaskExecutor(taskID, executorID, note string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Model(&model.SchoolTaskRecord{}).
		Where("id = ? AND status = ?", strings.TrimSpace(taskID), model.SchoolTaskRunning).
		Updates(map[string]any{
			"executor_id":  strings.TrimSpace(executorID),
			"error":        note,
			"last_seen_at": time.Now().UTC(),
			"updated_at":   time.Now().UTC(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// isDuplicateKeyError 识别各数据库驱动的唯一键冲突错误（跨 SQLite/MySQL/PostgreSQL）。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "constraint failed")
}

// isRetryableLockError 识别并发写锁冲突，可安全重试。
func isRetryableLockError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "deadlock") ||
		strings.Contains(message, "lock wait timeout")
}
