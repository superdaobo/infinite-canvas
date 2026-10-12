package repository

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// ApplyRoomBatchAsServer 以受信任服务端身份在单个数据库事务内原子应用一批房间操作。
//
// 与 ApplyRoomBatch 的唯一区别：**不校验操作者当前是否是可写的房间成员**。
// 它专供受约束的服务端回填路径（学校结果回填）使用——该路径的授权在 service 层
// 完成：只有「执行设备持有本房间的 running 任务」时才会到达这里。因此任务提交人
// 即使已被房主移除或降为 viewer，其结果仍必须能回填并释放单执行槽（P1 队列堵塞）。
//
// 调用方必须保证：
//   - 仅限服务端内部调用，绝不可直接挂到任何 HTTP 入口；
//   - actorID 只用于幂等回执与审计归属，不据此授予房间写权限；
//   - 调用前已完成等价于「执行设备持有本房间 running 任务」的授权校验；
//   - 传入的 ops 由服务端构造（不接受用户请求体），并已通过契约信封校验。
//
// 其余语义与 ApplyRoomBatch 完全一致：全成功或全回滚、幂等键
// (roomID, actorID, operationID)、相同摘要重放返回既有回执、提交后 revision 恰好 +1。
func ApplyRoomBatchAsServer(roomID, actorID, operationID, digest string, ops []contract.Operation) (model.RoomBatchResult, error) {
	db, err := DB()
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	roomID = strings.TrimSpace(roomID)
	actorID = strings.TrimSpace(actorID)

	var result model.RoomBatchResult
	txErr := db.Transaction(func(tx *gorm.DB) error {
		// 幂等：已存在回执时直接返回既有结果，不重复执行、不重复推进 revision。
		var receipt model.RoomOperation
		err := tx.First(&receipt, "room_id = ? AND user_id = ? AND operation_id = ?", roomID, actorID, operationID).Error
		if err == nil {
			if receipt.RequestDigest != digest {
				return contractError(contract.CodeOperationIDReused, "operationId 已用于不同的请求体", map[string]any{"operationId": operationID})
			}
			if err := json.Unmarshal([]byte(receipt.ResponseJSON), &result); err != nil {
				return err
			}
			result.Idempotent = true
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}

		var room model.Room
		if err := tx.First(&room, "id = ?", roomID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return contractError(contract.CodeNotFound, "房间不存在", map[string]any{"roomId": roomID})
			}
			return err
		}

		changes, err := applyOperations(tx, roomID, ops)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		if err := tx.Model(&model.Room{}).Where("id = ?", roomID).
			Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		var refreshed model.Room
		if err := tx.First(&refreshed, "id = ?", roomID).Error; err != nil {
			return err
		}

		result = model.RoomBatchResult{OperationID: operationID, Revision: refreshed.Revision, Changes: changes}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.RoomOperation{
			RoomID:        roomID,
			UserID:        actorID,
			OperationID:   operationID,
			RequestDigest: digest,
			Revision:      refreshed.Revision,
			ResponseJSON:  string(payload),
			CreatedAt:     now,
		}).Error
	})
	if txErr != nil {
		return model.RoomBatchResult{}, txErr
	}
	return result, nil
}
