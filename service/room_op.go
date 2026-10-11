package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// ApplyRoomOperations 校验一次幂等操作信封并在单事务内原子提交。
// 契约错误（*contract.Error）直接透传给 handler，由其按 StatusFor 映射 HTTP 状态。
func ApplyRoomOperations(ctx context.Context, roomID string, envelope contract.Envelope) (model.RoomBatchResult, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.RoomBatchResult{}, contractFailure(contract.CodeUnauthorized, "未登录或登录态无效", nil)
	}
	roomID = strings.TrimSpace(roomID)
	if envelope.RoomID != "" && envelope.RoomID != roomID {
		return model.RoomBatchResult{}, contractFailure(contract.CodeInvalidPayload, "路径房间 ID 与请求体不一致", map[string]any{"roomId": envelope.RoomID})
	}
	envelope.RoomID = roomID
	if err := contract.ValidateEnvelope(envelope); err != nil {
		return model.RoomBatchResult{}, err
	}
	for _, op := range envelope.Operations {
		if op.Type == contract.OpUpdate {
			if err := validateUpdateFields(op.Fields); err != nil {
				return model.RoomBatchResult{}, err
			}
		}
	}
	digest, err := requestDigest(envelope)
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	result, err := repository.ApplyRoomBatch(roomID, user.ID, envelope.OperationID, digest, envelope.Operations)
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	// 确认与广播只在事务提交后发生；幂等重放不重复广播。
	broadcastRoomChanges(roomID, result)
	return result, nil
}

// ReadRoomGraph 返回当前用户可访问房间的公共图快照。
func ReadRoomGraph(ctx context.Context, roomID string) (model.RoomGraph, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.RoomGraph{}, contractFailure(contract.CodeUnauthorized, "未登录或登录态无效", nil)
	}
	roomID = strings.TrimSpace(roomID)
	if _, ok, err := repository.FindRoomMember(roomID, user.ID); err != nil {
		return model.RoomGraph{}, err
	} else if !ok {
		return model.RoomGraph{}, contractFailure(contract.CodeNotFound, "房间不存在或无权访问", map[string]any{"roomId": roomID})
	}
	room, ok, err := repository.FindRoom(roomID)
	if err != nil {
		return model.RoomGraph{}, err
	}
	if !ok {
		return model.RoomGraph{}, contractFailure(contract.CodeNotFound, "房间不存在", map[string]any{"roomId": roomID})
	}
	nodes, edges, err := repository.ListRoomGraph(roomID)
	if err != nil {
		return model.RoomGraph{}, err
	}
	if nodes == nil {
		nodes = []model.RoomNode{}
	}
	if edges == nil {
		edges = []model.RoomEdge{}
	}
	return model.RoomGraph{RoomID: roomID, Revision: room.Revision, Nodes: nodes, Edges: edges}, nil
}

// validateUpdateFields 强制 update 的增量字段白名单：只允许 public/local 字段，
// 凭据类键返回 forbidden_field，私有 metadata 字段不允许经公共图写入。
func validateUpdateFields(fields map[string]any) error {
	for key, value := range fields {
		switch key {
		case "title", "position", "width", "height":
		case "metadata":
			metadata, ok := value.(map[string]any)
			if !ok {
				return contractFailure(contract.CodeInvalidPayload, "metadata 必须是对象", nil)
			}
			for metaKey := range metadata {
				if contract.IsForbiddenBroadcastKey(metaKey) {
					return contractFailure(contract.CodeForbiddenField, "metadata 含凭据类字段，禁止进入公共图", map[string]any{"field": metaKey})
				}
				if contract.ClassifyMetadataField(metaKey) == contract.ClassPrivate {
					return contractFailure(contract.CodeInvalidPayload, "私有 metadata 字段不能通过公共图更新", map[string]any{"field": metaKey})
				}
			}
		default:
			return contractFailure(contract.CodeInvalidPayload, "不支持的更新字段", map[string]any{"field": key})
		}
	}
	return nil
}

// requestDigest 计算请求体摘要（不含 operationId，作为幂等键本身）。
// encoding/json 对 map 键排序，摘要稳定可比。
func requestDigest(envelope contract.Envelope) (string, error) {
	payload := struct {
		ProtocolVersion string               `json:"protocolVersion"`
		RoomID          string               `json:"roomId"`
		Operations      []contract.Operation `json:"operations"`
	}{envelope.ProtocolVersion, envelope.RoomID, envelope.Operations}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func contractFailure(code, message string, details map[string]any) error {
	return &contract.Error{Code: code, Message: message, Details: details}
}
