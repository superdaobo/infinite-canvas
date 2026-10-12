package repository

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

// ApplyRoomBatch 在单个数据库事务内原子应用一批节点/连线操作。
//
// 语义（与 docs/collab-v1/contract.md 第 4 节对齐）：
//   - 全成功或全回滚，不存在部分成功；
//   - 版本冲突（expectedNodeVersion 与当前版本不一致）整批回滚并返回 version_conflict；
//   - 幂等键为 (roomID,userID,operationID)：相同摘要直接返回既有回执，不同摘要返回 operation_id_reused；
//   - 提交成功后房间 revision 恰好 +1，回执与 revision 同事务写入，确认与广播只在提交后发生。
func ApplyRoomBatch(roomID, userID, operationID, digest string, ops []contract.Operation) (model.RoomBatchResult, error) {
	db, err := DB()
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	roomID = strings.TrimSpace(roomID)
	userID = strings.TrimSpace(userID)

	var result model.RoomBatchResult
	txErr := db.Transaction(func(tx *gorm.DB) error {
		// 权限检查在事务生效前完成：必须是可写成员（owner/editor）。
		var member model.RoomMember
		if err := tx.First(&member, "room_id = ? AND user_id = ?", roomID, userID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return contractError(contract.CodeNotFound, "房间不存在或无权访问", map[string]any{"roomId": roomID})
			}
			return err
		}
		if !member.Role.CanWrite() {
			return contractError(contract.CodeForbidden, "只读成员不能修改房间", map[string]any{"roomId": roomID})
		}

		// 幂等：已存在回执时直接返回既有结果，不重复执行、不重复推进 revision。
		var receipt model.RoomOperation
		err := tx.First(&receipt, "room_id = ? AND user_id = ? AND operation_id = ?", roomID, userID, operationID).Error
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

		changes, historyOps, err := applyOperations(tx, roomID, ops)
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
		if err := tx.Create(&model.RoomOperation{
			RoomID:        roomID,
			UserID:        userID,
			OperationID:   operationID,
			RequestDigest: digest,
			Revision:      refreshed.Revision,
			ResponseJSON:  string(payload),
			CreatedAt:     now,
		}).Error; err != nil {
			return err
		}
		// 审计：同一事务内记录本次批次的逐目标变更（含前后版本与快照），供审计与撤销使用。
		return writeRoomHistory(tx, roomID, userID, operationID, refreshed.Revision, historyOps, now)
	})
	if txErr != nil {
		return model.RoomBatchResult{}, txErr
	}
	return result, nil
}

// FindRoomNode 读取房间内指定节点的当前行（含已删除标记）；不存在时 ok=false。
func FindRoomNode(roomID, nodeID string) (model.RoomNode, bool, error) {
	db, err := DB()
	if err != nil {
		return model.RoomNode{}, false, err
	}
	var row model.RoomNode
	err = db.First(&row, "room_id = ? AND id = ?", strings.TrimSpace(roomID), strings.TrimSpace(nodeID)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.RoomNode{}, false, nil
	}
	if err != nil {
		return model.RoomNode{}, false, err
	}
	return row, true, nil
}

// ListRoomGraph 读取房间公共图（不含已删除节点/连线），按 ID 升序稳定返回。
func ListRoomGraph(roomID string) ([]model.RoomNode, []model.RoomEdge, error) {
	db, err := DB()
	if err != nil {
		return nil, nil, err
	}
	roomID = strings.TrimSpace(roomID)
	var nodes []model.RoomNode
	if err := db.Where("room_id = ? AND deleted = ?", roomID, false).Order("id ASC").Find(&nodes).Error; err != nil {
		return nil, nil, err
	}
	var edges []model.RoomEdge
	if err := db.Where("room_id = ? AND deleted = ?", roomID, false).Order("id ASC").Find(&edges).Error; err != nil {
		return nil, nil, err
	}
	return nodes, edges, nil
}

// applyOperations 按顺序应用批次内每个操作，随后对整张房间图做一致性校验。
// 同时返回逐目标的历史明细（含操作前后行快照与版本），供同一事务内写审计与撤销依据。
func applyOperations(tx *gorm.DB, roomID string, ops []contract.Operation) ([]model.RoomChange, []model.RoomHistoryOp, error) {
	now := time.Now().UTC()
	changes := make([]model.RoomChange, 0, len(ops))
	history := make([]model.RoomHistoryOp, 0, len(ops))
	for _, op := range ops {
		switch op.Type {
		case contract.OpCreate:
			node, err := createNode(tx, roomID, *op.Node, now)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, model.RoomChange{Type: "upsert", Node: &node})
			history = append(history, model.RoomHistoryOp{
				Op: "create", Target: "node", ID: node.ID,
				After: nodeState(node), Active: true, Version: node.Version,
			})
		case contract.OpUpdate:
			node, before, err := updateNode(tx, roomID, op, now)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, model.RoomChange{Type: "upsert", Node: &node})
			history = append(history, model.RoomHistoryOp{
				Op: "update", Target: "node", ID: node.ID,
				Before: nodeState(before), After: nodeState(node), Active: true, Version: node.Version,
				PositionOnly: positionOnlyFields(op.Fields),
			})
		case contract.OpDelete:
			node, before, err := deleteNode(tx, roomID, op, now)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, model.RoomChange{Type: "delete", Node: &node})
			history = append(history, model.RoomHistoryOp{
				Op: "delete", Target: "node", ID: node.ID,
				Before: nodeState(before), After: nodeState(node), Active: true, Version: node.Version,
			})
		case contract.OpConnect:
			edge, before, err := connectEdge(tx, roomID, op, now)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, model.RoomChange{Type: "edge-upsert", Edge: &edge})
			history = append(history, model.RoomHistoryOp{
				Op: "connect", Target: "edge", ID: edge.ID,
				Before: edgeStatePtr(before), After: edgeState(edge), Active: true, Version: edge.Version,
			})
		case contract.OpDisconnect:
			edge, before, err := disconnectEdge(tx, roomID, op, now)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, model.RoomChange{Type: "edge-delete", Edge: &edge})
			history = append(history, model.RoomHistoryOp{
				Op: "disconnect", Target: "edge", ID: edge.ID,
				Before: edgeState(before), After: edgeState(edge), Active: true, Version: edge.Version,
			})
		default:
			return nil, nil, contractError(contract.CodeInvalidPayload, fmt.Sprintf("未知操作类型 %q", op.Type), nil)
		}
	}
	if err := validateRoomGraph(tx, roomID); err != nil {
		return nil, nil, err
	}
	return changes, history, nil
}

// positionOnlyFields 判断一次 update 是否只修改了坐标（连续拖动合并的判定条件）。
func positionOnlyFields(fields map[string]any) bool {
	if len(fields) != 1 {
		return false
	}
	_, ok := fields["position"]
	return ok
}

func nodeState(row model.RoomNode) *model.RoomHistoryState {
	copied := row
	return &model.RoomHistoryState{Node: &copied}
}

func edgeState(row model.RoomEdge) *model.RoomHistoryState {
	copied := row
	return &model.RoomHistoryState{Edge: &copied}
}

// edgeStatePtr 在连线此前不存在（connect 新建）时返回 nil，表示 Before 状态为“不存在”。
func edgeStatePtr(row *model.RoomEdge) *model.RoomHistoryState {
	if row == nil {
		return nil
	}
	return edgeState(*row)
}

func createNode(tx *gorm.DB, roomID string, node contract.Node, now time.Time) (model.RoomNode, error) {
	var existing model.RoomNode
	err := tx.First(&existing, "room_id = ? AND id = ?", roomID, node.ID).Error
	if err == nil {
		return model.RoomNode{}, contractError(contract.CodeNodeExists, fmt.Sprintf("节点 %s 已存在", node.ID), map[string]any{"nodeId": node.ID})
	}
	if err != gorm.ErrRecordNotFound {
		return model.RoomNode{}, err
	}
	metadata, err := marshalMetadata(node.Metadata)
	if err != nil {
		return model.RoomNode{}, err
	}
	row := model.RoomNode{
		RoomID: roomID, ID: node.ID, Kind: string(node.Kind), Title: node.Title,
		PositionX: node.Position.X, PositionY: node.Position.Y,
		Width: node.Width, Height: node.Height, Version: 1,
		Metadata: metadata, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&row).Error; err != nil {
		// 并发创建同一 ID 时由主键约束兜底，映射回 node_exists 而不是 500。
		if isDuplicateKeyError(err) {
			return model.RoomNode{}, contractError(contract.CodeNodeExists, fmt.Sprintf("节点 %s 已存在", node.ID), map[string]any{"nodeId": node.ID})
		}
		return model.RoomNode{}, err
	}
	return row, nil
}

// updateNode 更新节点并返回 (更新后的行, 更新前的行快照, error)。
func updateNode(tx *gorm.DB, roomID string, op contract.Operation, now time.Time) (model.RoomNode, model.RoomNode, error) {
	row, err := loadNodeForWrite(tx, roomID, op.ID)
	if err != nil {
		return model.RoomNode{}, model.RoomNode{}, err
	}
	if op.Version == nil || *op.Version != row.Version {
		return model.RoomNode{}, model.RoomNode{}, versionConflict(row)
	}
	before := row
	updated, err := applyNodeFields(row, op.Fields)
	if err != nil {
		return model.RoomNode{}, model.RoomNode{}, err
	}
	// 增量更新后的结果必须仍是合法节点（例如 text/config 必填 content 不能丢）。
	candidate := rowToContractNode(updated)
	if err := contract.ValidateNode(&candidate); err != nil {
		return model.RoomNode{}, model.RoomNode{}, err
	}
	updated.Version = row.Version + 1
	updated.UpdatedAt = now
	// 数据库条件 CAS：WHERE 带 version=? AND deleted=false，只有行仍处于读取时的
	// 版本且未被删除才写入，避免并发事务读取同一旧版本后相互覆盖。
	affected, err := casUpdateNode(tx, roomID, row.ID, row.Version, map[string]any{
		"title": updated.Title, "position_x": updated.PositionX, "position_y": updated.PositionY,
		"width": updated.Width, "height": updated.Height, "metadata": updated.Metadata,
		"version": updated.Version, "updated_at": now,
	})
	if err != nil {
		return model.RoomNode{}, model.RoomNode{}, err
	}
	if affected == 0 {
		return model.RoomNode{}, model.RoomNode{}, nodeWriteConflict(tx, roomID, row.ID, row)
	}
	return updated, before, nil
}

// deleteNode 软删除节点并返回 (删除后的墓碑行, 删除前的行快照, error)。
func deleteNode(tx *gorm.DB, roomID string, op contract.Operation, now time.Time) (model.RoomNode, model.RoomNode, error) {
	row, err := loadNodeForWrite(tx, roomID, op.ID)
	if err != nil {
		return model.RoomNode{}, model.RoomNode{}, err
	}
	if op.Version == nil || *op.Version != row.Version {
		return model.RoomNode{}, model.RoomNode{}, versionConflict(row)
	}
	before := row
	row.Deleted = true
	row.Version = row.Version + 1
	row.UpdatedAt = now
	// 数据库条件 CAS：仅当版本未变且尚未删除时才标记删除。
	affected, err := casUpdateNode(tx, roomID, row.ID, row.Version-1, map[string]any{
		"deleted": true, "version": row.Version, "updated_at": now,
	})
	if err != nil {
		return model.RoomNode{}, model.RoomNode{}, err
	}
	if affected == 0 {
		return model.RoomNode{}, model.RoomNode{}, nodeWriteConflict(tx, roomID, row.ID, row)
	}
	return row, before, nil
}

// connectEdge 新建或更新连线并返回 (结果行, 更新前的行快照或 nil（新建）, error)。
func connectEdge(tx *gorm.DB, roomID string, op contract.Operation, now time.Time) (model.RoomEdge, *model.RoomEdge, error) {
	edge := *op.Edge
	if err := ensureNodeExists(tx, roomID, edge.FromNodeID); err != nil {
		return model.RoomEdge{}, nil, err
	}
	if err := ensureNodeExists(tx, roomID, edge.ToNodeID); err != nil {
		return model.RoomEdge{}, nil, err
	}
	var existing model.RoomEdge
	err := tx.First(&existing, "room_id = ? AND id = ?", roomID, edge.ID).Error
	if err == gorm.ErrRecordNotFound {
		if edge.Version != 0 {
			return model.RoomEdge{}, nil, contractError(contract.CodeEdgeChanged, "连线不存在，无法按指定版本更新", map[string]any{"edgeId": edge.ID})
		}
		row := model.RoomEdge{
			RoomID: roomID, ID: edge.ID, FromNodeID: edge.FromNodeID, ToNodeID: edge.ToNodeID,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			// 并发创建同一连线 ID 时由主键约束兜底。
			if isDuplicateKeyError(err) {
				return model.RoomEdge{}, nil, contractError(contract.CodeEdgeChanged, "连线已存在", map[string]any{"edgeId": edge.ID})
			}
			return model.RoomEdge{}, nil, err
		}
		return row, nil, nil
	}
	if err != nil {
		return model.RoomEdge{}, nil, err
	}
	if existing.Deleted || edge.Version == 0 || edge.Version != existing.Version {
		return model.RoomEdge{}, nil, contractError(contract.CodeEdgeChanged, "连线版本已变化", map[string]any{"edgeId": existing.ID, "version": existing.Version})
	}
	before := existing
	expectedVersion := existing.Version
	existing.FromNodeID = edge.FromNodeID
	existing.ToNodeID = edge.ToNodeID
	existing.Version = expectedVersion + 1
	existing.UpdatedAt = now
	// 数据库条件 CAS：WHERE 带 version=? AND deleted=false，防止并发覆盖连线修改。
	affected, err := casUpdateEdge(tx, roomID, existing.ID, expectedVersion, map[string]any{
		"from_node_id": existing.FromNodeID, "to_node_id": existing.ToNodeID,
		"version": existing.Version, "updated_at": now,
	})
	if err != nil {
		return model.RoomEdge{}, nil, err
	}
	if affected == 0 {
		return model.RoomEdge{}, nil, edgeWriteConflict(tx, roomID, existing.ID)
	}
	return existing, &before, nil
}

// disconnectEdge 软删除连线并返回 (删除后的墓碑行, 删除前的行快照, error)。
func disconnectEdge(tx *gorm.DB, roomID string, op contract.Operation, now time.Time) (model.RoomEdge, model.RoomEdge, error) {
	var row model.RoomEdge
	err := tx.First(&row, "room_id = ? AND id = ?", roomID, op.ID).Error
	if err == gorm.ErrRecordNotFound {
		return model.RoomEdge{}, model.RoomEdge{}, contractError(contract.CodeNotFound, fmt.Sprintf("连线 %s 不存在", op.ID), map[string]any{"edgeId": op.ID})
	}
	if err != nil {
		return model.RoomEdge{}, model.RoomEdge{}, err
	}
	if row.Deleted {
		return model.RoomEdge{}, model.RoomEdge{}, contractError(contract.CodeEdgeChanged, fmt.Sprintf("连线 %s 已被删除", op.ID), map[string]any{"edgeId": op.ID})
	}
	if op.Version == nil || *op.Version != row.Version {
		return model.RoomEdge{}, model.RoomEdge{}, contractError(contract.CodeEdgeChanged, "连线版本已变化", map[string]any{"edgeId": row.ID, "version": row.Version})
	}
	before := row
	expectedVersion := row.Version
	row.Deleted = true
	row.Version = expectedVersion + 1
	row.UpdatedAt = now
	// 数据库条件 CAS：仅当版本未变且尚未删除时才标记删除。
	affected, err := casUpdateEdge(tx, roomID, row.ID, expectedVersion, map[string]any{
		"deleted": true, "version": row.Version, "updated_at": now,
	})
	if err != nil {
		return model.RoomEdge{}, model.RoomEdge{}, err
	}
	if affected == 0 {
		return model.RoomEdge{}, model.RoomEdge{}, edgeWriteConflict(tx, roomID, row.ID)
	}
	return row, before, nil
}

// validateRoomGraph 用契约校验整张房间图：端点存在、无自环、无重复连线、无环。
func validateRoomGraph(tx *gorm.DB, roomID string) error {
	var nodeRows []model.RoomNode
	if err := tx.Where("room_id = ? AND deleted = ?", roomID, false).Find(&nodeRows).Error; err != nil {
		return err
	}
	nodes := make(map[string]contract.Node, len(nodeRows))
	for _, row := range nodeRows {
		nodes[row.ID] = rowToContractNode(row)
	}
	var edgeRows []model.RoomEdge
	if err := tx.Where("room_id = ? AND deleted = ?", roomID, false).Find(&edgeRows).Error; err != nil {
		return err
	}
	edges := make([]contract.Edge, 0, len(edgeRows))
	for _, row := range edgeRows {
		edges = append(edges, rowToContractEdge(row))
	}
	return contract.ValidateGraph(nodes, edges)
}

func loadNodeForWrite(tx *gorm.DB, roomID, id string) (model.RoomNode, error) {
	var row model.RoomNode
	err := tx.First(&row, "room_id = ? AND id = ?", roomID, id).Error
	if err == gorm.ErrRecordNotFound {
		return model.RoomNode{}, contractError(contract.CodeNotFound, fmt.Sprintf("节点 %s 不存在", id), map[string]any{"nodeId": id})
	}
	if err != nil {
		return model.RoomNode{}, err
	}
	if row.Deleted {
		return model.RoomNode{}, contractError(contract.CodeNodeDeleted, fmt.Sprintf("节点 %s 已被删除", id), map[string]any{"nodeId": id, "deleted": true})
	}
	return row, nil
}

func ensureNodeExists(tx *gorm.DB, roomID, id string) error {
	var row model.RoomNode
	err := tx.Select("id", "deleted").First(&row, "room_id = ? AND id = ?", roomID, id).Error
	if err == gorm.ErrRecordNotFound || (err == nil && row.Deleted) {
		return contractError(contract.CodeInvalidPayload, fmt.Sprintf("连线端点 %s 不存在", id), map[string]any{"nodeId": id})
	}
	return err
}

func versionConflict(row model.RoomNode) error {
	return contractError(contract.CodeVersionConflict, "节点版本已变化", map[string]any{"node": rowToContractNode(row)})
}

// casUpdateNode 是节点的数据库条件 CAS：仅当 (room_id,id) 行仍处于 expectedVersion
// 且 deleted=false 时更新，返回受影响行数。0 行表示版本已被并发修改或删除。
func casUpdateNode(tx *gorm.DB, roomID, id string, expectedVersion int, values map[string]any) (int64, error) {
	result := tx.Model(&model.RoomNode{}).
		Where("room_id = ? AND id = ? AND version = ? AND deleted = ?", roomID, id, expectedVersion, false).
		Updates(values)
	return result.RowsAffected, result.Error
}

// casUpdateEdge 是连线的数据库条件 CAS，语义同 casUpdateNode。
func casUpdateEdge(tx *gorm.DB, roomID, id string, expectedVersion int, values map[string]any) (int64, error) {
	result := tx.Model(&model.RoomEdge{}).
		Where("room_id = ? AND id = ? AND version = ? AND deleted = ?", roomID, id, expectedVersion, false).
		Updates(values)
	return result.RowsAffected, result.Error
}

// nodeWriteConflict 在 CAS 影响 0 行时构造冲突错误：重读当前行，若已被删除返回
// node_deleted，否则返回 version_conflict 并携带最新节点，绝不上报成功。
func nodeWriteConflict(tx *gorm.DB, roomID, id string, fallback model.RoomNode) error {
	var current model.RoomNode
	if err := tx.First(&current, "room_id = ? AND id = ?", roomID, id).Error; err == nil {
		if current.Deleted {
			return contractError(contract.CodeNodeDeleted, fmt.Sprintf("节点 %s 已被删除", id), map[string]any{"nodeId": id, "deleted": true})
		}
		return versionConflict(current)
	}
	return versionConflict(fallback)
}

// edgeWriteConflict 在连线 CAS 影响 0 行时构造 edge_changed 错误，携带最新版本。
func edgeWriteConflict(tx *gorm.DB, roomID, id string) error {
	details := map[string]any{"edgeId": id}
	var current model.RoomEdge
	if err := tx.First(&current, "room_id = ? AND id = ?", roomID, id).Error; err == nil {
		details["version"] = current.Version
		details["deleted"] = current.Deleted
	}
	return contractError(contract.CodeEdgeChanged, "连线版本已变化", details)
}

// applyNodeFields 把 update 的增量字段合并进节点行。字段白名单：
// title / position / width / height / metadata（metadata 为浅合并）。
func applyNodeFields(row model.RoomNode, fields map[string]any) (model.RoomNode, error) {
	updated := row
	for key, value := range fields {
		switch key {
		case "title":
			title, ok := value.(string)
			if !ok {
				return model.RoomNode{}, contractError(contract.CodeInvalidPayload, "title 必须是字符串", nil)
			}
			updated.Title = title
		case "position":
			position, ok := value.(map[string]any)
			if !ok {
				return model.RoomNode{}, contractError(contract.CodeInvalidPayload, "position 必须是对象", nil)
			}
			x, okX := asFloat(position["x"])
			y, okY := asFloat(position["y"])
			if !okX || !okY {
				return model.RoomNode{}, contractError(contract.CodeInvalidPayload, "position.x/y 必须是数值", nil)
			}
			updated.PositionX = x
			updated.PositionY = y
		case "width":
			width, ok := asFloat(value)
			if !ok {
				return model.RoomNode{}, contractError(contract.CodeInvalidPayload, "width 必须是数值", nil)
			}
			updated.Width = width
		case "height":
			height, ok := asFloat(value)
			if !ok {
				return model.RoomNode{}, contractError(contract.CodeInvalidPayload, "height 必须是数值", nil)
			}
			updated.Height = height
		case "metadata":
			patch, ok := value.(map[string]any)
			if !ok {
				return model.RoomNode{}, contractError(contract.CodeInvalidPayload, "metadata 必须是对象", nil)
			}
			merged, err := mergeMetadata(updated.Metadata, patch)
			if err != nil {
				return model.RoomNode{}, err
			}
			updated.Metadata = merged
		default:
			return model.RoomNode{}, contractError(contract.CodeInvalidPayload, fmt.Sprintf("不支持的更新字段 %q", key), map[string]any{"field": key})
		}
	}
	return updated, nil
}

func mergeMetadata(existing string, patch map[string]any) (string, error) {
	current := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &current); err != nil {
			return "", err
		}
	}
	for key, value := range patch {
		current[key] = value
	}
	return marshalMetadata(current)
}

func marshalMetadata(metadata map[string]any) (string, error) {
	if len(metadata) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func rowToContractNode(row model.RoomNode) contract.Node {
	node := contract.Node{
		ID: row.ID, Kind: contract.NodeKind(row.Kind), Title: row.Title,
		Position: contract.Position{X: row.PositionX, Y: row.PositionY},
		Width:    row.Width, Height: row.Height, Version: row.Version,
	}
	if strings.TrimSpace(row.Metadata) != "" {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(row.Metadata), &metadata); err == nil {
			node.Metadata = metadata
		}
	}
	return node
}

func rowToContractEdge(row model.RoomEdge) contract.Edge {
	return contract.Edge{
		ID: row.ID, FromNodeID: row.FromNodeID, ToNodeID: row.ToNodeID, Version: row.Version,
	}
}

func contractError(code, message string, details map[string]any) error {
	return &contract.Error{Code: code, Message: message, Details: details}
}
