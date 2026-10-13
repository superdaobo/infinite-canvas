package repository

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// 本文件实现协作历史审计与撤销/重做的服务端约束（协作 v1，#11）。
//
// 设计要点：
//   - 每个成功批次（operationId）写一行 collab_room_history，逐目标记录 Before/After
//     行快照与前后版本，供审计与撤销/重做使用；与节点/连线/回执同事务写入。
//   - 撤销/重做按用户隔离：只有历史记录的 actor 本人（且仍是可写成员）能操作。
//   - 撤销/重做把每个目标在 Before/After 状态之间切换，切换时以行版本做条件 CAS：
//     若目标已被他人修改（版本前进），整条历史失效并返回 version_conflict，绝不覆盖。
//   - 连续拖动（单条 position-only update）会合并进上一条同类历史，减少历史噪声。

// historyID 生成一个不可猜测的历史记录 ID。
func historyID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("hist_%d", time.Now().UnixNano())
	}
	return "hist_" + hex.EncodeToString(buf)
}

// toggleOperationID 返回撤销/重做批次的 operationId（确定性，便于日志追踪）。
func toggleOperationID(historyID string, toAfter bool) string {
	if toAfter {
		return "redo_" + historyID
	}
	return "undo_" + historyID
}

// writeRoomHistory 在同一事务内记录一次成功批次的审计历史。
// 若本次是单条“连续拖动”更新且紧邻上一条同类历史，则合并进上一条而非新增。
func writeRoomHistory(tx *gorm.DB, roomID, actorID, operationID string, revision int64, ops []model.RoomHistoryOp, now time.Time) error {
	if len(ops) == 0 {
		return nil
	}
	merged, err := mergeRoomHistory(tx, roomID, actorID, revision, ops, now)
	if err != nil {
		return err
	}
	if merged {
		return nil
	}
	encoded, err := json.Marshal(ops)
	if err != nil {
		return err
	}
	return tx.Create(&model.RoomHistory{
		ID:          historyID(),
		RoomID:      roomID,
		OperationID: operationID,
		ActorID:     actorID,
		Revision:    revision,
		Summary:     historySummary(ops),
		OpsJSON:     string(encoded),
		Status:      model.RoomHistoryApplied,
		CreatedAt:   now,
		UpdatedAt:   now,
	}).Error
}

// mergeRoomHistory 尝试把单条 position-only 更新合并进上一条同类历史。
// 合并条件（全部满足才合并）：
//   - 本次批次恰好一个操作，且是节点 update 且只改坐标（拖动）；
//   - 本房间最新一条历史紧邻本次（revision-1），属于同一 actor，状态为 applied；
//   - 该条历史也恰好是同一节点的 position-only update 且处于生效态。
//
// 合并后保留最早的 Before（撤销回到拖动前）与最新的 After/Version，并累计合并次数。
func mergeRoomHistory(tx *gorm.DB, roomID, actorID string, revision int64, ops []model.RoomHistoryOp, now time.Time) (bool, error) {
	if len(ops) != 1 || ops[0].Op != "update" || ops[0].Target != "node" || !ops[0].PositionOnly {
		return false, nil
	}
	var last model.RoomHistory
	err := tx.Where("room_id = ?", roomID).Order("revision DESC, created_at DESC").First(&last).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if last.ActorID != actorID || last.Status != model.RoomHistoryApplied || last.Revision != revision-1 {
		return false, nil
	}
	prevOps, err := decodeRoomHistoryOps(last)
	if err != nil {
		return false, err
	}
	if len(prevOps) != 1 || prevOps[0].Op != "update" || prevOps[0].Target != "node" ||
		prevOps[0].ID != ops[0].ID || !prevOps[0].Active {
		return false, nil
	}
	prevOps[0].After = ops[0].After
	prevOps[0].Version = ops[0].Version
	prevOps[0].Merged++
	encoded, err := json.Marshal(prevOps)
	if err != nil {
		return false, err
	}
	last.OpsJSON = string(encoded)
	last.Revision = revision
	last.Summary = historySummary(prevOps)
	last.UpdatedAt = now
	return true, tx.Save(&last).Error
}

// historySummary 生成人类可读的变更摘要（审计展示用）。
// 结果按 rune 截断到 historySummaryMaxRunes，避免大批次（如整份旧画布导入）的摘要
// 超过 collab_room_history.summary 的列长度。
func historySummary(ops []model.RoomHistoryOp) string {
	labels := map[string]string{
		"create": "创建", "update": "更新", "delete": "删除",
		"connect": "新建连线", "disconnect": "删除连线",
	}
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		kind := "节点"
		if op.Target == "edge" {
			kind = "连线"
		}
		label := labels[op.Op]
		if label == "" {
			label = op.Op
		}
		text := fmt.Sprintf("%s%s %s", label, kind, op.ID)
		if op.Merged > 0 {
			text = fmt.Sprintf("%s（合并 %d 次）", text, op.Merged)
		}
		parts = append(parts, text)
	}
	summary := strings.Join(parts, "；")
	runes := []rune(summary)
	if len(runes) > historySummaryMaxRunes {
		return string(runes[:historySummaryMaxRunes]) + "…"
	}
	return summary
}

// historySummaryMaxRunes 是摘要的最大 rune 数，需与 collab_room_history.summary 列长度一致。
const historySummaryMaxRunes = 1024

func decodeRoomHistoryOps(row model.RoomHistory) ([]model.RoomHistoryOp, error) {
	if strings.TrimSpace(row.OpsJSON) == "" {
		return []model.RoomHistoryOp{}, nil
	}
	var ops []model.RoomHistoryOp
	if err := json.Unmarshal([]byte(row.OpsJSON), &ops); err != nil {
		return nil, err
	}
	return ops, nil
}

// ListRoomHistory 按 revision 倒序返回房间的操作历史（审计用），limit 上限 500。
func ListRoomHistory(roomID string, limit int) ([]model.RoomHistory, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	var rows []model.RoomHistory
	if err := db.Where("room_id = ?", strings.TrimSpace(roomID)).
		Order("revision DESC, created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	for index := range rows {
		ops, err := decodeRoomHistoryOps(rows[index])
		if err != nil {
			return nil, err
		}
		rows[index].Ops = ops
	}
	return rows, nil
}

func findRoomHistoryTx(tx *gorm.DB, roomID, id string) (model.RoomHistory, bool, error) {
	var row model.RoomHistory
	err := tx.First(&row, "room_id = ? AND id = ?", roomID, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.RoomHistory{}, false, nil
	}
	if err != nil {
		return model.RoomHistory{}, false, err
	}
	return row, true, nil
}

// UndoRoomHistory 撤销一条属于当前用户的历史（按用户隔离）。
func UndoRoomHistory(roomID, actorID, historyID string) (model.RoomBatchResult, error) {
	return toggleRoomHistory(roomID, actorID, historyID, false)
}

// RedoRoomHistory 重做一条已撤销、且仍属于当前用户的历史。
func RedoRoomHistory(roomID, actorID, historyID string) (model.RoomBatchResult, error) {
	return toggleRoomHistory(roomID, actorID, historyID, true)
}

// toggleRoomHistory 是撤销/重做的共同实现：toAfter=true 为重做（切到 After），
// false 为撤销（切到 Before）。全程在单个事务内，复用条件 CAS 与 revision 推进。
func toggleRoomHistory(roomID, actorID, historyID string, toAfter bool) (model.RoomBatchResult, error) {
	db, err := DB()
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	roomID = strings.TrimSpace(roomID)
	actorID = strings.TrimSpace(actorID)
	historyID = strings.TrimSpace(historyID)

	var result model.RoomBatchResult
	txErr := db.Transaction(func(tx *gorm.DB) error {
		row, ok, err := findRoomHistoryTx(tx, roomID, historyID)
		if err != nil {
			return err
		}
		if !ok {
			return contractError(contract.CodeNotFound, "历史记录不存在", map[string]any{"historyId": historyID})
		}
		// 按用户隔离：只能撤销/重做自己的操作。
		if row.ActorID != actorID {
			return contractError(contract.CodeForbidden, "只能撤销或重做自己的操作", map[string]any{"historyId": historyID})
		}
		var member model.RoomMember
		if err := tx.First(&member, "room_id = ? AND user_id = ?", roomID, actorID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contractError(contract.CodeNotFound, "房间不存在或无权访问", map[string]any{"roomId": roomID})
			}
			return err
		}
		if !member.Role.CanWrite() {
			return contractError(contract.CodeForbidden, "只读成员不能撤销或重做", map[string]any{"roomId": roomID})
		}

		// 幂等：已处于目标状态时直接返回，不重复执行、不推进 revision。
		if (toAfter && row.Status == model.RoomHistoryApplied) || (!toAfter && row.Status == model.RoomHistoryUndone) {
			revision := row.Revision
			var room model.Room
			if err := tx.First(&room, "id = ?", roomID).Error; err == nil {
				revision = room.Revision
			}
			result = model.RoomBatchResult{
				OperationID: toggleOperationID(historyID, toAfter),
				Revision:    revision,
				Changes:     []model.RoomChange{},
				Idempotent:  true,
			}
			return nil
		}

		ops, err := decodeRoomHistoryOps(row)
		if err != nil {
			return err
		}
		changes, err := toggleHistoryOps(tx, roomID, historyID, ops, toAfter)
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

		encoded, err := json.Marshal(ops)
		if err != nil {
			return err
		}
		row.OpsJSON = string(encoded)
		row.UpdatedAt = now
		if toAfter {
			row.Status = model.RoomHistoryApplied
			row.UndoneBy = ""
		} else {
			row.Status = model.RoomHistoryUndone
			row.UndoneBy = toggleOperationID(historyID, false)
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		result = model.RoomBatchResult{
			OperationID: toggleOperationID(historyID, toAfter),
			Revision:    refreshed.Revision,
			Changes:     changes,
		}
		return nil
	})
	if txErr != nil {
		return model.RoomBatchResult{}, txErr
	}
	return result, nil
}

// toggleHistoryOps 把历史明细的每个目标切到目标状态（toAfter 决定 After/Before），
// 任一切换因版本不符失败即整批回滚，历史保持原状。
func toggleHistoryOps(tx *gorm.DB, roomID, historyID string, ops []model.RoomHistoryOp, toAfter bool) ([]model.RoomChange, error) {
	now := time.Now().UTC()
	changes := make([]model.RoomChange, 0, len(ops))
	for index := range ops {
		op := &ops[index]
		state := op.Before
		if toAfter {
			state = op.After
		}
		change, version, err := switchHistoryState(tx, roomID, historyID, op.Target, op.ID, state, op.Version, now)
		if err != nil {
			return nil, err
		}
		op.Version = version
		op.Active = toAfter
		changes = append(changes, change)
	}
	if err := validateRoomGraph(tx, roomID); err != nil {
		var contractErr *contract.Error
		if errors.As(err, &contractErr) {
			return nil, contractError(contract.CodeVersionConflict,
				"历史已失效：撤销或重做会使房间图不一致（依赖的节点或连线已变化）",
				map[string]any{"reason": "history_invalidated", "historyId": historyID, "cause": contractErr.Code})
		}
		return nil, err
	}
	return changes, nil
}

// switchHistoryState 把单个目标切换到 state（nil 表示“不存在”），期望当前版本为
// expectedVersion；成功返回广播变更与切换后的版本。
func switchHistoryState(tx *gorm.DB, roomID, historyID, target, id string, state *model.RoomHistoryState, expectedVersion int, now time.Time) (model.RoomChange, int, error) {
	switch target {
	case "node":
		if state == nil || state.Node == nil {
			return purgeNodeState(tx, roomID, historyID, id, expectedVersion)
		}
		return applyNodeState(tx, roomID, historyID, *state.Node, expectedVersion, now)
	case "edge":
		if state == nil || state.Edge == nil {
			return purgeEdgeState(tx, roomID, historyID, id, expectedVersion)
		}
		return applyEdgeState(tx, roomID, historyID, *state.Edge, expectedVersion, now)
	default:
		return model.RoomChange{}, 0, contractError(contract.CodeInvalidPayload,
			fmt.Sprintf("未知历史目标 %q", target), map[string]any{"historyId": historyID})
	}
}

// purgeNodeState 物理移除一个“此前不存在”的节点（撤销 create）。CAS 条件含版本。
func purgeNodeState(tx *gorm.DB, roomID, historyID, id string, expectedVersion int) (model.RoomChange, int, error) {
	var row model.RoomNode
	err := tx.First(&row, "room_id = ? AND id = ?", roomID, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if expectedVersion == 0 {
			return model.RoomChange{Type: "delete", Node: &model.RoomNode{RoomID: roomID, ID: id}}, 0, nil
		}
		return model.RoomChange{}, 0, historyConflict(historyID, "node", id, 0)
	}
	if err != nil {
		return model.RoomChange{}, 0, err
	}
	if expectedVersion == 0 || row.Version != expectedVersion {
		return model.RoomChange{}, 0, historyConflict(historyID, "node", id, row.Version)
	}
	result := tx.Where("room_id = ? AND id = ? AND version = ?", roomID, id, expectedVersion).Delete(&model.RoomNode{})
	if result.Error != nil {
		return model.RoomChange{}, 0, result.Error
	}
	if result.RowsAffected == 0 {
		return model.RoomChange{}, 0, historyConflict(historyID, "node", id, row.Version)
	}
	purged := row
	return model.RoomChange{Type: "delete", Node: &purged}, 0, nil
}

// applyNodeState 把节点切换为给定快照：行存在则条件 CAS 更新，行不存在则按 create 落库。
func applyNodeState(tx *gorm.DB, roomID, historyID string, node model.RoomNode, expectedVersion int, now time.Time) (model.RoomChange, int, error) {
	var current model.RoomNode
	err := tx.First(&current, "room_id = ? AND id = ?", roomID, node.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if expectedVersion != 0 {
			return model.RoomChange{}, 0, historyConflict(historyID, "node", node.ID, 0)
		}
		row := node
		row.RoomID = roomID
		row.Version = 1
		row.CreatedAt = now
		row.UpdatedAt = now
		if err := tx.Create(&row).Error; err != nil {
			if isDuplicateKeyError(err) {
				return model.RoomChange{}, 0, contractError(contract.CodeNodeExists,
					fmt.Sprintf("节点 %s 已存在", node.ID), map[string]any{"nodeId": node.ID, "historyId": historyID})
			}
			return model.RoomChange{}, 0, err
		}
		return nodeChange(row), 1, nil
	}
	if err != nil {
		return model.RoomChange{}, 0, err
	}
	if expectedVersion == 0 || current.Version != expectedVersion {
		return model.RoomChange{}, 0, historyConflict(historyID, "node", node.ID, current.Version)
	}
	newVersion := expectedVersion + 1
	affected, err := casUpdateNodeAny(tx, roomID, node.ID, expectedVersion, map[string]any{
		"title": node.Title, "position_x": node.PositionX, "position_y": node.PositionY,
		"width": node.Width, "height": node.Height, "metadata": node.Metadata,
		"deleted": node.Deleted, "version": newVersion, "updated_at": now,
	})
	if err != nil {
		return model.RoomChange{}, 0, err
	}
	if affected == 0 {
		return model.RoomChange{}, 0, historyConflict(historyID, "node", node.ID, current.Version)
	}
	updated := node
	updated.RoomID = roomID
	updated.Version = newVersion
	updated.CreatedAt = current.CreatedAt
	updated.UpdatedAt = now
	return nodeChange(updated), newVersion, nil
}

// purgeEdgeState 物理移除一条“此前不存在”的连线（撤销 connect）。
func purgeEdgeState(tx *gorm.DB, roomID, historyID, id string, expectedVersion int) (model.RoomChange, int, error) {
	var row model.RoomEdge
	err := tx.First(&row, "room_id = ? AND id = ?", roomID, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if expectedVersion == 0 {
			return model.RoomChange{Type: "edge-delete", Edge: &model.RoomEdge{RoomID: roomID, ID: id}}, 0, nil
		}
		return model.RoomChange{}, 0, historyConflict(historyID, "edge", id, 0)
	}
	if err != nil {
		return model.RoomChange{}, 0, err
	}
	if expectedVersion == 0 || row.Version != expectedVersion {
		return model.RoomChange{}, 0, historyConflict(historyID, "edge", id, row.Version)
	}
	result := tx.Where("room_id = ? AND id = ? AND version = ?", roomID, id, expectedVersion).Delete(&model.RoomEdge{})
	if result.Error != nil {
		return model.RoomChange{}, 0, result.Error
	}
	if result.RowsAffected == 0 {
		return model.RoomChange{}, 0, historyConflict(historyID, "edge", id, row.Version)
	}
	purged := row
	return model.RoomChange{Type: "edge-delete", Edge: &purged}, 0, nil
}

// applyEdgeState 把连线切换为给定快照：行存在则条件 CAS 更新，行不存在则按新建落库。
func applyEdgeState(tx *gorm.DB, roomID, historyID string, edge model.RoomEdge, expectedVersion int, now time.Time) (model.RoomChange, int, error) {
	var current model.RoomEdge
	err := tx.First(&current, "room_id = ? AND id = ?", roomID, edge.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if expectedVersion != 0 {
			return model.RoomChange{}, 0, historyConflict(historyID, "edge", edge.ID, 0)
		}
		row := edge
		row.RoomID = roomID
		row.Version = 1
		row.CreatedAt = now
		row.UpdatedAt = now
		if err := tx.Create(&row).Error; err != nil {
			if isDuplicateKeyError(err) {
				return model.RoomChange{}, 0, contractError(contract.CodeEdgeChanged,
					"连线已存在", map[string]any{"edgeId": edge.ID, "historyId": historyID})
			}
			return model.RoomChange{}, 0, err
		}
		return edgeChange(row), 1, nil
	}
	if err != nil {
		return model.RoomChange{}, 0, err
	}
	if expectedVersion == 0 || current.Version != expectedVersion {
		return model.RoomChange{}, 0, historyConflict(historyID, "edge", edge.ID, current.Version)
	}
	newVersion := expectedVersion + 1
	affected, err := casUpdateEdgeAny(tx, roomID, edge.ID, expectedVersion, map[string]any{
		"from_node_id": edge.FromNodeID, "to_node_id": edge.ToNodeID,
		"deleted": edge.Deleted, "version": newVersion, "updated_at": now,
	})
	if err != nil {
		return model.RoomChange{}, 0, err
	}
	if affected == 0 {
		return model.RoomChange{}, 0, historyConflict(historyID, "edge", edge.ID, current.Version)
	}
	updated := edge
	updated.RoomID = roomID
	updated.Version = newVersion
	updated.CreatedAt = current.CreatedAt
	updated.UpdatedAt = now
	return edgeChange(updated), newVersion, nil
}

func nodeChange(row model.RoomNode) model.RoomChange {
	if row.Deleted {
		return model.RoomChange{Type: "delete", Node: &row}
	}
	return model.RoomChange{Type: "upsert", Node: &row}
}

func edgeChange(row model.RoomEdge) model.RoomChange {
	if row.Deleted {
		return model.RoomChange{Type: "edge-delete", Edge: &row}
	}
	return model.RoomChange{Type: "edge-upsert", Edge: &row}
}

// historyConflict 构造历史失效冲突错误：相关目标已被他人修改（或不存在），
// 撤销/重做不会覆盖他人修改。
func historyConflict(historyID, target, id string, currentVersion int) error {
	return contractError(contract.CodeVersionConflict,
		"历史已失效：相关节点或连线已被他人修改，撤销/重做不会覆盖他人修改",
		map[string]any{
			"reason":         "history_invalidated",
			"historyId":      historyID,
			"target":         target,
			"targetId":       id,
			"currentVersion": currentVersion,
		})
}

// casUpdateNodeAny 是撤销/重做路径的节点条件 CAS：只约束版本（不约束 deleted），
// 以便同时支持“恢复已删除行”和“更新生效行”。版本不符返回 0 行。
func casUpdateNodeAny(tx *gorm.DB, roomID, id string, expectedVersion int, values map[string]any) (int64, error) {
	result := tx.Model(&model.RoomNode{}).
		Where("room_id = ? AND id = ? AND version = ?", roomID, id, expectedVersion).
		Updates(values)
	return result.RowsAffected, result.Error
}

// casUpdateEdgeAny 是撤销/重做路径的连线条件 CAS，语义同 casUpdateNodeAny。
func casUpdateEdgeAny(tx *gorm.DB, roomID, id string, expectedVersion int, values map[string]any) (int64, error) {
	result := tx.Model(&model.RoomEdge{}).
		Where("room_id = ? AND id = ? AND version = ?", roomID, id, expectedVersion).
		Updates(values)
	return result.RowsAffected, result.Error
}
