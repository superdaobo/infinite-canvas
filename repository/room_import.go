package repository

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// 本文件实现旧画布整份导入的原子写入（C04-02 / #25）。
//
// 语义与 #8 的操作批次一致：单事务全成功或全回滚；幂等键为
// (roomID,userID,operationID)，相同摘要直接返回既有报告，不重复写图、不重复推进 revision。
// 唯一差别是导入面向「整份旧快照」，节点/连线数量可超过单批 200 的上限，因此单独实现，
// 但复用同一张回执表（collab_room_operations）与历史表（collab_room_history）。
//
// 只允许写入空房间：房间 revision>0（已有成员操作）或除 owner 外还有成员时拒绝整份覆盖，
// 返回 room_not_empty，绝不把旧快照恢复成成员的现有编辑。

// FindRoomImportReceipt 读取导入幂等回执。返回 (请求摘要, 报告 JSON, 是否存在)。
func FindRoomImportReceipt(roomID, userID, operationID string) (string, string, bool, error) {
	db, err := DB()
	if err != nil {
		return "", "", false, err
	}
	var receipt model.RoomOperation
	err = db.First(
		&receipt,
		"room_id = ? AND user_id = ? AND operation_id = ?",
		strings.TrimSpace(roomID), strings.TrimSpace(userID), strings.TrimSpace(operationID),
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return receipt.RequestDigest, receipt.ResponseJSON, true, nil
}

// ImportRoomGraph 在单个事务内把整份旧画布节点/连线写入空房间，并写入幂等回执与审计历史。
// report 携带除 Applied/Idempotent/Revision 之外的报告字段；返回最终报告（含 revision）
// 与用于广播的变更列表。
func ImportRoomGraph(roomID, userID, operationID, digest string, nodes []model.RoomNode, edges []model.RoomEdge, report *model.RoomImportReport) (*model.RoomImportReport, []model.RoomChange, error) {
	db, err := DB()
	if err != nil {
		return nil, nil, err
	}
	roomID = strings.TrimSpace(roomID)
	userID = strings.TrimSpace(userID)

	var result *model.RoomImportReport
	var changes []model.RoomChange
	txErr := db.Transaction(func(tx *gorm.DB) error {
		// 权限：必须是可写成员（owner/editor）。
		var member model.RoomMember
		if err := tx.First(&member, "room_id = ? AND user_id = ?", roomID, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contractError(contract.CodeNotFound, "房间不存在或无权访问", map[string]any{"roomId": roomID})
			}
			return err
		}
		if !member.Role.CanWrite() {
			return contractError(contract.CodeForbidden, "只读成员不能导入", map[string]any{"roomId": roomID})
		}

		// 幂等：已存在回执时直接返回既有报告，不重复写图、不重复推进 revision。
		var receipt model.RoomOperation
		err := tx.First(&receipt, "room_id = ? AND user_id = ? AND operation_id = ?", roomID, userID, operationID).Error
		if err == nil {
			if receipt.RequestDigest != digest {
				return contractError(contract.CodeOperationIDReused, "operationId 已用于不同的导入请求", map[string]any{"operationId": operationID})
			}
			stored := &model.RoomImportReport{}
			if err := json.Unmarshal([]byte(receipt.ResponseJSON), stored); err != nil {
				return err
			}
			stored.Idempotent = true
			result = stored
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var room model.Room
		if err := tx.First(&room, "id = ?", roomID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contractError(contract.CodeNotFound, "房间不存在", map[string]any{"roomId": roomID})
			}
			return err
		}
		// 只允许写入空房间：拒绝用旧快照覆盖已有成员操作或已有其他成员。
		var otherMembers int64
		if err := tx.Model(&model.RoomMember{}).
			Where("room_id = ? AND user_id <> ?", roomID, room.OwnerID).
			Count(&otherMembers).Error; err != nil {
			return err
		}
		if room.Revision > 0 || otherMembers > 0 {
			return contractError(contract.CodeRoomNotEmpty,
				"房间已有成员编辑或成员，拒绝用旧快照覆盖；请新建空房间后导入",
				map[string]any{"roomId": roomID, "revision": room.Revision, "otherMembers": otherMembers})
		}

		if err := validateImportedGraph(nodes, edges); err != nil {
			return err
		}

		now := time.Now().UTC()
		changes = make([]model.RoomChange, 0, len(nodes)+len(edges))
		history := make([]model.RoomHistoryOp, 0, len(nodes)+len(edges))
		for index := range nodes {
			row := nodes[index]
			row.RoomID = roomID
			row.Version = 1
			row.CreatedAt = now
			row.UpdatedAt = now
			if err := tx.Create(&row).Error; err != nil {
				if isDuplicateKeyError(err) {
					return contractError(contract.CodeNodeExists, "导入节点已存在", map[string]any{"nodeId": row.ID})
				}
				return err
			}
			created := row
			changes = append(changes, model.RoomChange{Type: "upsert", Node: &created})
			history = append(history, model.RoomHistoryOp{
				Op: "create", Target: "node", ID: row.ID, After: nodeState(row), Active: true, Version: row.Version,
			})
		}
		for index := range edges {
			row := edges[index]
			row.RoomID = roomID
			row.Version = 1
			row.CreatedAt = now
			row.UpdatedAt = now
			if err := tx.Create(&row).Error; err != nil {
				if isDuplicateKeyError(err) {
					return contractError(contract.CodeEdgeChanged, "导入连线已存在", map[string]any{"edgeId": row.ID})
				}
				return err
			}
			created := row
			changes = append(changes, model.RoomChange{Type: "edge-upsert", Edge: &created})
			history = append(history, model.RoomHistoryOp{
				Op: "connect", Target: "edge", ID: row.ID, After: edgeState(row), Active: true, Version: row.Version,
			})
		}

		if err := tx.Model(&model.Room{}).Where("id = ?", roomID).
			Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		var refreshed model.Room
		if err := tx.First(&refreshed, "id = ?", roomID).Error; err != nil {
			return err
		}

		report.Applied = true
		report.Idempotent = false
		report.Revision = refreshed.Revision
		payload, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if err := tx.Create(&model.RoomOperation{
			RoomID: roomID, UserID: userID, OperationID: operationID,
			RequestDigest: digest, Revision: refreshed.Revision,
			ResponseJSON: string(payload), CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := writeRoomHistory(tx, roomID, userID, operationID, refreshed.Revision, history, now); err != nil {
			return err
		}
		result = report
		return nil
	})
	if txErr != nil {
		return nil, nil, txErr
	}
	return result, changes, nil
}

// validateImportedGraph 用契约校验整份导入图：端点存在、无自环、无重复连线、无环、
// 端点不重复的有向简单边，以及编组归属目标必须是分组节点。与在线写入路径同一套规则。
func validateImportedGraph(nodes []model.RoomNode, edges []model.RoomEdge) error {
	contractNodes := make(map[string]contract.Node, len(nodes))
	for _, row := range nodes {
		contractNodes[row.ID] = rowToContractNode(row)
	}
	contractEdges := make([]contract.Edge, 0, len(edges))
	for _, row := range edges {
		contractEdges = append(contractEdges, rowToContractEdge(row))
	}
	if err := contract.ValidateGraph(contractNodes, contractEdges); err != nil {
		return err
	}
	if err := validateUniqueEdgePairs(contractEdges); err != nil {
		return err
	}
	return validateGroupTargets(contractNodes)
}
