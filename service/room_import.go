package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 本文件实现旧单人画布（整份 JSON）到共享房间的安全导入（C04-02 / #25）。
//
// 设计要点：
//   - 只读预扫描（mode=preview，默认）给出节点/边/媒体数量、8 类节点字段摘要与不可迁移
//     清单，绝不写库、绝不联网、绝不回显本地绝对路径或密钥；
//   - 写入（mode=initial）只允许导入空房间，复用 repository.ImportRoomGraph 的单事务原子
//     路径与 (roomID,userID,operationId) 幂等回执，重复导入不产生重复节点；
//   - 字段映射遵循 docs/collab-v1/contract.md：private/local metadata 不导入为公共字段，
//     凭据类字段所在节点整体跳过并报告；
//   - 媒体迁移只拉取来源白名单（COLLAB_MEDIA_IMPORT_ORIGINS）内的 http(s) 媒体，127.0.0.1
//     等不可达/非白名单来源标记为「素材未同步」并清除引用，绝不写入悬空引用；类型仍走
//     #20 白名单，SVG/主动内容一律拒绝。

const (
	importModePreview = "preview"
	importModeInitial = "initial"

	// importMediaTimeout 是单个媒体拉取的整体超时。
	importMediaTimeout = 60 * time.Second
	// 媒体缺失原因码（稳定分类，供报告与前端展示）。
	importMediaReasonLocalReference  = "local_reference"
	importMediaReasonServerReference = "server_reference"
	importMediaReasonNotAllowed      = "source_not_allowed"
	importMediaReasonFetchFailed     = "fetch_failed"
	importMediaReasonUnsupported     = "unsupported_type"
	importMediaReasonTooLarge        = "too_large"

	importMediaMissingMessage = "素材未同步：请在本机重新上传或让发布者补传"
)

// importMediaHTTPClient 是导入媒体拉取专用客户端：不跟随重定向、不使用代理，
// 只按来源白名单精确匹配的 origin 拉取，避免被重定向带到未授权地址。
var importMediaHTTPClient = &http.Client{
	Timeout: importMediaTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("导入媒体禁止跟随重定向")
	},
	Transport: newImportMediaTransport(),
}

func newImportMediaTransport() http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}

// roomImportPlan 是一次导入的预扫描结果：待写入的行、可变的 metadata 映射与媒体任务。
type roomImportPlan struct {
	rows     []model.RoomNode
	metas    []map[string]any
	edges    []model.RoomEdge
	targets  []roomImportMediaTarget
	report   model.RoomImportReport
	graphErr error
}

// roomImportMediaTarget 描述一个待迁移/待补传的媒体节点。
type roomImportMediaTarget struct {
	nodeIndex int
	sourceURL string // 空表示非 http(s) 引用，服务端无法拉取
	origin    string
	reason    string // sourceURL 为空时的缺失原因
	filename  string
}

// importNodeMapping 是单个旧节点映射到契约节点的结果。
type importNodeMapping struct {
	row            model.RoomNode
	meta           map[string]any
	privateDropped int
	localDropped   int
	skipReason     string
	skipMessage    string
}

// ImportRoomSnapshot 执行一次旧画布导入。mode 为空或 preview 时只读预扫描；
// 只有显式 initial 才会写入空房间。返回报告绝不包含本地绝对路径或密钥。
func ImportRoomSnapshot(ctx context.Context, roomID string, request model.RoomImportRequest) (model.RoomImportReport, error) {
	// 整份导入是对房间的初始化，仅 owner 可执行（editor/viewer 拒绝）。
	member, err := RequireRoomOwner(ctx, roomID)
	if err != nil {
		return model.RoomImportReport{}, err
	}
	roomID = strings.TrimSpace(roomID)
	mode, err := normalizeImportMode(request.Mode)
	if err != nil {
		return model.RoomImportReport{}, err
	}
	allow := importMediaAllowlist()
	plan := prescanRoomImport(roomID, request.Snapshot)
	plan.report.Mode = mode
	plan.report.Media.Referenced = len(plan.targets)

	if mode == importModePreview {
		fillPreviewMissingMedia(plan, allow)
		return plan.report, nil
	}

	digest, err := importSnapshotDigest(request.Snapshot)
	if err != nil {
		return model.RoomImportReport{}, err
	}
	operationID := importOperationID(roomID, request.Snapshot.ID, digest)

	// 幂等：命中同摘要回执直接返回既有报告，不重复迁移媒体、不重复写图。
	if existingDigest, reportJSON, ok, err := repository.FindRoomImportReceipt(roomID, member.UserID, operationID); err != nil {
		return model.RoomImportReport{}, err
	} else if ok {
		if existingDigest != digest {
			return model.RoomImportReport{}, contractFailure(contract.CodeOperationIDReused, "幂等键已被不同的导入请求使用", map[string]any{"operationId": operationID})
		}
		var stored model.RoomImportReport
		if err := json.Unmarshal([]byte(reportJSON), &stored); err != nil {
			return model.RoomImportReport{}, err
		}
		stored.Idempotent = true
		return stored, nil
	}

	// 候选图不合法（如旧画布含环状连线）时整体失败，不做部分导入。
	if plan.graphErr != nil {
		return model.RoomImportReport{}, plan.graphErr
	}

	migrateImportMedia(roomID, member.UserID, plan, allow)
	finalizeImportMetadata(plan)

	report := plan.report
	result, changes, err := repository.ImportRoomGraph(roomID, member.UserID, operationID, digest, plan.rows, plan.edges, &report)
	if err != nil {
		return model.RoomImportReport{}, err
	}
	// 提交成功后广播一次 changes；并发幂等重放（无变更）不广播。
	if !result.Idempotent && len(changes) > 0 {
		broadcastRoomChanges(roomID, model.RoomBatchResult{
			OperationID: operationID, Revision: result.Revision, Changes: changes,
		})
	}
	return *result, nil
}

// normalizeImportMode 归一化导入模式：空默认 preview，只接受 preview/initial。
func normalizeImportMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", importModePreview:
		return importModePreview, nil
	case importModeInitial:
		return importModeInitial, nil
	default:
		return "", contractFailure(contract.CodeInvalidPayload, "不支持的导入模式，只接受 preview 或 initial", map[string]any{"mode": mode})
	}
}

// prescanRoomImport 只读预扫描：解析、字段映射、跳过非法节点/连线、收集媒体任务与图校验。
func prescanRoomImport(roomID string, snapshot model.RoomImportSnapshot) *roomImportPlan {
	plan := &roomImportPlan{report: model.RoomImportReport{
		RoomID:       roomID,
		SourceID:     strings.TrimSpace(snapshot.ID),
		Nodes:        model.RoomImportNodeSummary{ByKind: map[string]int{}},
		SkippedNodes: []model.RoomImportSkippedNode{},
		SkippedEdges: []model.RoomImportSkippedNode{},
		MissingMedia: []model.RoomImportMissingMedia{},
	}}
	valid := make(map[string]bool, len(snapshot.Nodes))
	kinds := make(map[string]string, len(snapshot.Nodes))

	for _, node := range snapshot.Nodes {
		plan.report.Nodes.Total++
		mapping := mapImportNode(node)
		if mapping.skipReason != "" {
			plan.report.SkippedNodes = append(plan.report.SkippedNodes, model.RoomImportSkippedNode{
				ID: node.ID, Kind: strings.TrimSpace(node.Type), Reason: mapping.skipReason, Message: mapping.skipMessage,
			})
			continue
		}
		index := len(plan.rows)
		plan.rows = append(plan.rows, mapping.row)
		plan.metas = append(plan.metas, mapping.meta)
		valid[mapping.row.ID] = true
		kinds[mapping.row.ID] = mapping.row.Kind
		plan.report.Nodes.Imported++
		plan.report.Nodes.ByKind[mapping.row.Kind]++
		plan.report.Nodes.PrivateFieldsDropped += mapping.privateDropped
		plan.report.Nodes.LocalFieldsDropped += mapping.localDropped
		if target, ok := importMediaTargetFor(index, contract.NodeKind(mapping.row.Kind), mapping.meta, mapping.row.ID); ok {
			plan.targets = append(plan.targets, target)
		}
	}
	plan.report.Nodes.Skipped = plan.report.Nodes.Total - plan.report.Nodes.Imported

	// 旧数据里 groupId 可能指向普通节点；协作图要求编组归属必须是分组节点，这里移除该
	// 非法归属并告警，而不是让整份导入失败。
	for index := range plan.rows {
		groupID, _ := plan.metas[index]["groupId"].(string)
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			continue
		}
		if kind, ok := kinds[groupID]; ok && kind != string(contract.NodeKindGroup) {
			delete(plan.metas[index], "groupId")
			plan.report.Warnings = append(plan.report.Warnings,
				fmt.Sprintf("节点 %s 的 groupId 指向非分组节点，已移除该归属", plan.rows[index].ID))
		}
	}

	seenEdgeIDs := make(map[string]bool, len(snapshot.Connections))
	seenPairs := make(map[string]bool, len(snapshot.Connections))
	for _, connection := range snapshot.Connections {
		plan.report.Edges.Total++
		reason := importEdgeSkipReason(connection, valid, seenEdgeIDs, seenPairs)
		if reason != "" {
			plan.report.SkippedEdges = append(plan.report.SkippedEdges, model.RoomImportSkippedNode{
				ID: connection.ID, Kind: "edge", Reason: reason, Message: importEdgeSkipMessage(reason),
			})
			continue
		}
		from := strings.TrimSpace(connection.FromNodeID)
		to := strings.TrimSpace(connection.ToNodeID)
		seenEdgeIDs[strings.TrimSpace(connection.ID)] = true
		seenPairs[from+"\x00"+to] = true
		plan.edges = append(plan.edges, model.RoomEdge{ID: strings.TrimSpace(connection.ID), FromNodeID: from, ToNodeID: to})
		plan.report.Edges.Imported++
	}
	plan.report.Edges.Skipped = plan.report.Edges.Total - plan.report.Edges.Imported

	if err := validateImportCandidateGraph(plan); err != nil {
		plan.graphErr = err
		plan.report.Warnings = append(plan.report.Warnings,
			fmt.Sprintf("候选图未通过校验（%s）；initial 模式将整体失败", contractErrorCode(err)))
	}
	return plan
}

// importEdgeSkipReason 返回连线被跳过的原因码；空串表示可导入。
func importEdgeSkipReason(connection model.RoomImportEdge, valid, seenEdgeIDs, seenPairs map[string]bool) string {
	id := strings.TrimSpace(connection.ID)
	from := strings.TrimSpace(connection.FromNodeID)
	to := strings.TrimSpace(connection.ToNodeID)
	if !contract.ValidID(id) || !contract.ValidID(from) || !contract.ValidID(to) || from == to {
		return "invalid_edge"
	}
	if !valid[from] || !valid[to] {
		return "dangling_edge"
	}
	if seenEdgeIDs[id] || seenPairs[from+"\x00"+to] {
		return "duplicate_edge"
	}
	return ""
}

func importEdgeSkipMessage(reason string) string {
	switch reason {
	case "dangling_edge":
		return "连线端点对应的节点已被跳过或不存在"
	case "duplicate_edge":
		return "重复的连线 ID 或重复的起止节点"
	default:
		return "连线字段不满足契约"
	}
}

// mapImportNode 把一个旧节点映射为契约公共图节点，并裁剪 private/local 字段。
func mapImportNode(node model.RoomImportNode) importNodeMapping {
	kind := contract.NodeKind(strings.TrimSpace(node.Type))
	if !kind.Valid() {
		return importNodeMapping{skipReason: "unknown_kind", skipMessage: "节点类型不属于 8 类节点"}
	}
	if !contract.ValidID(node.ID) {
		return importNodeMapping{skipReason: "invalid_id", skipMessage: "节点 ID 非法"}
	}
	if !finiteNumber(node.Position.X) || !finiteNumber(node.Position.Y) {
		return importNodeMapping{skipReason: "invalid_position", skipMessage: "节点坐标必须是有限数值"}
	}
	if !finiteNumber(node.Width) || node.Width <= 0 || !finiteNumber(node.Height) || node.Height <= 0 {
		return importNodeMapping{skipReason: "invalid_size", skipMessage: "节点宽高必须是正有限数值"}
	}

	meta, privateDropped, localDropped, forbidden := sanitizeImportMetadata(node.Metadata)
	if forbidden {
		return importNodeMapping{skipReason: contract.CodeForbiddenField, skipMessage: importSkipMessage(contract.CodeForbiddenField)}
	}
	candidate := contract.Node{
		ID: node.ID, Kind: kind, Title: node.Title,
		Position: contract.Position{X: node.Position.X, Y: node.Position.Y},
		Width:    node.Width, Height: node.Height, Version: 1, Metadata: meta,
	}
	if err := contract.ValidateNode(&candidate); err != nil {
		code := contractErrorCode(err)
		return importNodeMapping{skipReason: code, skipMessage: importSkipMessage(code)}
	}
	return importNodeMapping{
		row: model.RoomNode{
			ID: node.ID, Kind: string(kind), Title: node.Title,
			PositionX: node.Position.X, PositionY: node.Position.Y,
			Width: node.Width, Height: node.Height, Version: 1,
		},
		meta:           meta,
		privateDropped: privateDropped,
		localDropped:   localDropped,
	}
}

// sanitizeImportMetadata 只保留进入公共图的 public/未知字段；private 字段（owner 独占）
// 与 local 字段（运行时状态）不导入为公共字段；命中凭据类键时整体拒绝（forbidden=true）。
func sanitizeImportMetadata(metadata map[string]any) (clean map[string]any, privateDropped, localDropped int, forbidden bool) {
	clean = map[string]any{}
	for key, value := range metadata {
		if contract.IsForbiddenBroadcastKey(key) {
			return nil, 0, 0, true
		}
		switch contract.ClassifyMetadataField(key) {
		case contract.ClassPrivate:
			privateDropped++
		case contract.ClassLocal:
			localDropped++
		default:
			clean[key] = value
		}
	}
	return clean, privateDropped, localDropped, false
}

// importSkipMessage 返回不泄露原始字段名/值的通用跳过说明。
func importSkipMessage(code string) string {
	switch code {
	case contract.CodeForbiddenField:
		return "节点包含凭据类字段，已跳过"
	case contract.CodeInvalidPayload:
		return "节点字段不满足契约（必填字段/宽高/metadata 大小等）"
	default:
		return "节点不满足导入校验，已跳过"
	}
}

// importMediaTargetFor 判断媒体节点是否带媒体引用，并给出拉取候选或缺失原因。
func importMediaTargetFor(index int, kind contract.NodeKind, meta map[string]any, nodeID string) (roomImportMediaTarget, bool) {
	switch kind {
	case contract.NodeKindImage, contract.NodeKindPanorama, contract.NodeKindVideo, contract.NodeKindAudio:
	default:
		return roomImportMediaTarget{}, false
	}
	content := strings.TrimSpace(importStringValue(meta["content"]))
	storageKey := strings.TrimSpace(importStringValue(meta["storageKey"]))
	if content == "" && storageKey == "" {
		return roomImportMediaTarget{}, false
	}
	if origin, ok := httpOrigin(content); ok {
		return roomImportMediaTarget{
			nodeIndex: index, sourceURL: content, origin: origin,
			filename: importMediaFilename(content, nodeID),
		}, true
	}
	reason := importMediaReasonLocalReference
	if strings.HasPrefix(storageKey, "server:") || strings.HasPrefix(content, "/api/") {
		reason = importMediaReasonServerReference
	}
	return roomImportMediaTarget{nodeIndex: index, reason: reason}, true
}

// fillPreviewMissingMedia 在只读预扫描里列出「initial 模式下会待补传」的媒体（非白名单
// 来源与本地/服务端引用），不发起任何网络请求。
func fillPreviewMissingMedia(plan *roomImportPlan, allow map[string]bool) {
	for _, target := range plan.targets {
		reason := target.reason
		if target.sourceURL != "" && !allow[target.origin] {
			reason = importMediaReasonNotAllowed
		}
		if reason == "" {
			continue
		}
		plan.report.Media.Missing++
		plan.report.MissingMedia = append(plan.report.MissingMedia, model.RoomImportMissingMedia{
			NodeID: plan.rows[target.nodeIndex].ID, Origin: target.origin, Reason: reason,
		})
	}
}

// migrateImportMedia 逐个迁移媒体：白名单来源拉取并登记为房间共享媒体，其余标记缺失并
// 清除引用（不写悬空引用）。类型仍走房间媒体白名单，SVG/主动内容一律拒绝。
func migrateImportMedia(roomID, actorID string, plan *roomImportPlan, allow map[string]bool) {
	for _, target := range plan.targets {
		meta := plan.metas[target.nodeIndex]
		nodeID := plan.rows[target.nodeIndex].ID

		if target.sourceURL == "" {
			markImportMediaMissing(plan, meta, nodeID, "", target.reason)
			continue
		}
		if !allow[target.origin] {
			markImportMediaMissing(plan, meta, nodeID, target.origin, importMediaReasonNotAllowed)
			continue
		}
		view, err := fetchAndStoreImportMedia(roomID, actorID, target)
		if err != nil {
			reason := importMediaReasonFetchFailed
			switch {
			case errors.Is(err, ErrMediaUnsupported):
				plan.report.Media.Rejected++
				reason = importMediaReasonUnsupported
			case errors.Is(err, ErrMediaTooLarge):
				reason = importMediaReasonTooLarge
			}
			markImportMediaMissing(plan, meta, nodeID, target.origin, reason)
			continue
		}
		// 迁移成功：改写为房间媒体引用，清除原始不可达地址。
		meta["storageKey"] = view.StorageKey
		meta["mimeType"] = view.MimeType
		meta["bytes"] = view.Bytes
		delete(meta, "content")
		plan.report.Media.Migrated++
	}
	plan.report.Media.Missing = plan.report.Media.Referenced - plan.report.Media.Migrated
}

// markImportMediaMissing 把节点媒体标记为未同步并清除不可达引用，绝不留下悬空引用。
func markImportMediaMissing(plan *roomImportPlan, meta map[string]any, nodeID, origin, reason string) {
	delete(meta, "content")
	delete(meta, "storageKey")
	meta["mediaError"] = importMediaMissingMessage
	plan.report.MissingMedia = append(plan.report.MissingMedia, model.RoomImportMissingMedia{
		NodeID: nodeID, Origin: origin, Reason: reason,
	})
}

// fetchAndStoreImportMedia 从白名单来源拉取媒体字节并按房间媒体白名单登记。
func fetchAndStoreImportMedia(roomID, actorID string, target roomImportMediaTarget) (model.RoomMediaView, error) {
	request, err := http.NewRequest(http.MethodGet, target.sourceURL, nil)
	if err != nil {
		return model.RoomMediaView{}, err
	}
	response, err := importMediaHTTPClient.Do(request)
	if err != nil {
		return model.RoomMediaView{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return model.RoomMediaView{}, fmt.Errorf("上游返回 %d", response.StatusCode)
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	return storeRoomMedia(roomID, actorID, target.filename, contentType, response.Body)
}

// finalizeImportMetadata 把迁移后的 metadata 映射序列化回待写入行。
func finalizeImportMetadata(plan *roomImportPlan) {
	for index := range plan.rows {
		if len(plan.metas[index]) == 0 {
			plan.rows[index].Metadata = ""
			continue
		}
		encoded, err := json.Marshal(plan.metas[index])
		if err != nil {
			plan.rows[index].Metadata = ""
			continue
		}
		plan.rows[index].Metadata = string(encoded)
	}
}

// validateImportCandidateGraph 用契约校验候选图（无环、无重复边、编组归属）。
func validateImportCandidateGraph(plan *roomImportPlan) error {
	nodes := make(map[string]contract.Node, len(plan.rows))
	for index, row := range plan.rows {
		nodes[row.ID] = contract.Node{
			ID: row.ID, Kind: contract.NodeKind(row.Kind), Title: row.Title,
			Position: contract.Position{X: row.PositionX, Y: row.PositionY},
			Width:    row.Width, Height: row.Height, Version: 1, Metadata: plan.metas[index],
		}
	}
	edges := make([]contract.Edge, 0, len(plan.edges))
	for _, row := range plan.edges {
		edges = append(edges, contract.Edge{ID: row.ID, FromNodeID: row.FromNodeID, ToNodeID: row.ToNodeID, Version: 1})
	}
	return contract.ValidateGraph(nodes, edges)
}

// importSnapshotDigest 计算快照的规范化摘要（节点/连线按 ID 排序，metadata map 键有序），
// 作为幂等键的一部分；只依赖输入快照，不受媒体迁移结果影响。
func importSnapshotDigest(snapshot model.RoomImportSnapshot) (string, error) {
	nodes := append([]model.RoomImportNode(nil), snapshot.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	connections := append([]model.RoomImportEdge(nil), snapshot.Connections...)
	sort.Slice(connections, func(i, j int) bool { return connections[i].ID < connections[j].ID })
	payload := struct {
		ID          string                 `json:"id"`
		Nodes       []model.RoomImportNode `json:"nodes"`
		Connections []model.RoomImportEdge `json:"connections"`
	}{strings.TrimSpace(snapshot.ID), nodes, connections}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// importOperationID 由 (roomID, sourceId, digest) 派生确定性幂等键，重复导入同摘要命中同一回执。
func importOperationID(roomID, sourceID, digest string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(roomID) + "\x00" + strings.TrimSpace(sourceID) + "\x00" + digest))
	return "imp_" + hex.EncodeToString(sum[:20])
}

// importMediaAllowlist 解析媒体来源白名单（逗号分隔的 scheme://host[:port]）。
func importMediaAllowlist() map[string]bool {
	allow := map[string]bool{}
	for _, item := range strings.Split(config.Cfg.CollabMediaImportOrigins, ",") {
		normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(item), "/"))
		if normalized != "" {
			allow[normalized] = true
		}
	}
	return allow
}

// httpOrigin 返回 http(s) URL 的 origin（scheme://host[:port]，不含路径/查询/凭据）。
func httpOrigin(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	return scheme + "://" + strings.ToLower(parsed.Host), true
}

// importMediaFilename 从 URL 路径推导文件名；无法推导时回落到节点 ID。
func importMediaFilename(sourceURL, nodeID string) string {
	if parsed, err := url.Parse(sourceURL); err == nil {
		if base := filepath.Base(parsed.Path); base != "" && base != "." && base != "/" && base != "\\" {
			return base
		}
	}
	return nodeID
}

func finiteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// importStringValue 只接受字符串类型的 metadata 值，其他类型返回空串。
func importStringValue(value any) string {
	text, _ := value.(string)
	return text
}

// contractErrorCode 取回错误里的契约错误码；非契约错误回落到 invalid_payload。
func contractErrorCode(err error) string {
	var contractErr *contract.Error
	if errors.As(err, &contractErr) {
		return contractErr.Code
	}
	return contract.CodeInvalidPayload
}
