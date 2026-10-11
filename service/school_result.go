package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 学校任务成功结果的安全上传与共享节点回填（C03-05 / #22）。
//
// 复用 #18 的任务队列、#20 的房间媒体与 #8 的原子操作提交路径：
//   - 执行器上报成功结果前，先拉取并校验结果媒体（可访问、非空、类型与渠道匹配、大小受限）；
//   - 校验通过后写入房间媒体（归属任务提交人），再以确定性幂等键走 #8 原子提交路径
//     创建图片/视频节点并触发 WS 广播，最后把任务置 succeeded；
//   - 任一步失败都保持任务 running、不释放单执行槽、绝不记为 succeeded；
//   - 失败信息经脱敏，不泄露底层凭据。
//
// 本期只走 mock 上游（#18）与本地假数据，绝不接触真实学校设备/凭据。

// 学校结果相关错误码，补充契约 §8 未覆盖的回填场景。
const (
	// CodeSchoolResultMissing 缺少结果媒体（未提供 resultUrls 且无上游任务号），409。
	CodeSchoolResultMissing = "result_missing"
	// CodeSchoolResultInvalid 结果媒体不完整或类型不匹配，422。
	CodeSchoolResultInvalid = "result_invalid"
	// CodeSchoolResultUnreachable 结果媒体不可访问（下载失败/非 200），502。
	CodeSchoolResultUnreachable = "result_unreachable"
)

// MaxSchoolResultBytes 限制单个学校结果媒体大小（默认与房间媒体上限一致）。
// 声明为变量以便测试收紧阈值。
var MaxSchoolResultBytes = MaxRoomMediaBytes

// SchoolResultDownloader 把结果 URL 拉取为字节；默认走 SSRF 白名单客户端，
// 测试可替换为本地假实现。绝不使用 http.DefaultClient 直连用户可控 URL。
var SchoolResultDownloader = downloadSchoolResultURL

// SchoolUpstream 是执行器侧的上游客户端；本期默认 mock（#18），测试可替换。
var SchoolUpstream SchoolUpstreamClient = NewMockSchoolUpstreamClient()

// SchoolResultInput 是执行器上报的成功结果载荷。
type SchoolResultInput struct {
	// UpstreamTaskID 是 mock/上游任务号；为空时使用任务已绑定的上游号。
	UpstreamTaskID string `json:"upstreamTaskId,omitempty"`
	// ResultURLs 是上游返回的产物 URL 列表；本期只回填第一个产物。
	ResultURLs []string `json:"resultUrls,omitempty"`
	// ResultFilename / ResultContentType 是可选的结果媒体元信息。
	ResultFilename    string `json:"resultFilename,omitempty"`
	ResultContentType string `json:"resultContentType,omitempty"`
}

// SchoolResultBackfill 是成功结果回填的返回：任务公开视图、新节点、媒体与是否幂等命中。
type SchoolResultBackfill struct {
	Task       model.SchoolTaskPublicView `json:"task"`
	Node       model.RoomNode             `json:"node"`
	Media      model.RoomMediaView        `json:"media"`
	Idempotent bool                       `json:"idempotent"`
}

// schoolResultMedia 是通过校验的结果媒体。
type schoolResultMedia struct {
	bytes       []byte
	contentType string
	filename    string
}

// UploadSchoolTaskResult 执行器上报成功结果并按 #22 回填共享节点：
//  1. 校验任务正被该执行器持有（未完成）；
//  2. 拉取并校验结果媒体（可访问、非空、类型与渠道匹配、大小受限）；
//  3. 写入房间媒体（归属任务提交人，复用 #20）；
//  4. 以确定性幂等键走 #8 原子提交路径创建图片/视频节点并广播；
//  5. 任务置 succeeded 并释放单执行槽。
//
// 重复上报（任务已 succeeded）按幂等返回既有节点；任一校验失败都返回错误且任务保持
// running，绝不记为 succeeded、绝不提前释放执行槽。
func UploadSchoolTaskResult(ctx context.Context, executorID, taskID string, input SchoolResultInput) (SchoolResultBackfill, error) {
	executorID = strings.TrimSpace(executorID)
	task, err := requireRunningSchoolTask(executorID, taskID)
	if err != nil {
		// 已结束任务的重复上报：仅当已是 succeeded 且属于同一执行器时幂等返回既有结果。
		if existing, ok, findErr := repository.FindSchoolTaskByID(taskID); findErr == nil && ok &&
			existing.Status == model.SchoolTaskSucceeded && existing.ExecutorID == executorID {
			return existingSchoolResult(existing)
		}
		return SchoolResultBackfill{}, err
	}

	media, err := fetchAndValidateSchoolResult(ctx, task, input)
	if err != nil {
		return SchoolResultBackfill{}, err
	}
	view, err := storeRoomMedia(task.RoomID, task.SubmittedBy, media.filename, media.contentType, bytes.NewReader(media.bytes))
	if err != nil {
		return SchoolResultBackfill{}, err
	}
	node, idempotent, err := backfillSchoolResultNode(ctx, task, view)
	if err != nil {
		return SchoolResultBackfill{}, err
	}
	// 节点写入并广播成功后，才把任务记为 succeeded（校验通过才成功）。
	if err := completeSchoolTaskSuccess(task, executorID); err != nil {
		return SchoolResultBackfill{}, err
	}
	final, _, err := repository.FindSchoolTaskByID(task.ID)
	if err != nil {
		return SchoolResultBackfill{}, err
	}
	return SchoolResultBackfill{Task: final.PublicView(), Node: node, Media: view, Idempotent: idempotent}, nil
}

// fetchAndValidateSchoolResult 解析结果来源并做完整性/可访问性校验。
func fetchAndValidateSchoolResult(ctx context.Context, task model.SchoolTask, input SchoolResultInput) (schoolResultMedia, error) {
	urls := nonEmptyStrings(input.ResultURLs)
	if len(urls) == 0 {
		upstreamID := strings.TrimSpace(input.UpstreamTaskID)
		if upstreamID == "" {
			upstreamID = strings.TrimSpace(task.UpstreamTaskID)
		}
		if upstreamID == "" {
			return schoolResultMedia{}, contractFailure(CodeSchoolResultMissing,
				"缺少结果媒体：未提供 resultUrls 且任务未绑定上游任务号", nil)
		}
		fetched, err := SchoolUpstream.Result(ctx, upstreamID)
		if err != nil {
			return schoolResultMedia{}, contractFailure(CodeSchoolResultUnreachable, "获取上游结果失败", nil)
		}
		urls = nonEmptyStrings(fetched)
		if len(urls) == 0 {
			return schoolResultMedia{}, contractFailure(CodeSchoolResultMissing, "上游未返回结果媒体", nil)
		}
	}

	expected, ok := expectedSchoolResultKind(task.Channel)
	if !ok {
		return schoolResultMedia{}, contractFailure(contract.CodeInvalidPayload, "未知学校渠道，无法确定结果类型", nil)
	}
	raw, contentType, err := SchoolResultDownloader(ctx, urls[0])
	if err != nil {
		return schoolResultMedia{}, contractFailure(CodeSchoolResultUnreachable, "结果媒体不可访问或下载失败", nil)
	}
	if len(raw) == 0 {
		return schoolResultMedia{}, contractFailure(CodeSchoolResultInvalid, "结果媒体为空", nil)
	}
	if int64(len(raw)) > MaxSchoolResultBytes {
		return schoolResultMedia{}, contractFailure(CodeSchoolResultInvalid, "结果媒体超过大小限制", nil)
	}
	declared := strings.TrimSpace(input.ResultContentType)
	if declared == "" {
		declared = contentType
	}
	kind, mimeType, ok := classifyRoomMedia(declared, headBytes(raw))
	if !ok || kind != expected {
		return schoolResultMedia{}, contractFailure(CodeSchoolResultInvalid,
			"结果媒体类型与渠道不匹配或不是受支持的图片/视频", nil)
	}
	filename := normalizeRoomMediaFilename(input.ResultFilename)
	if strings.TrimSpace(input.ResultFilename) == "" {
		filename = normalizeRoomMediaFilename("school-" + task.ID + schoolResultExt(mimeType))
	}
	return schoolResultMedia{bytes: raw, contentType: mimeType, filename: filename}, nil
}

// backfillSchoolResultNode 以确定性幂等键走 #8 原子提交路径创建结果节点并广播。
// 只创建新节点，绝不更新/删除既有节点，因此不会用本地旧快照覆盖共享画布。
func backfillSchoolResultNode(ctx context.Context, task model.SchoolTask, media model.RoomMediaView) (model.RoomNode, bool, error) {
	nodeID := schoolResultNodeID(task)
	kind := contract.NodeKindImage
	if task.Channel.IsVideo() {
		kind = contract.NodeKindVideo
	}
	width, height := schoolResultNodeSize(task.Channel)
	envelope := contract.Envelope{
		ProtocolVersion: contract.ProtocolVersion,
		RoomID:          task.RoomID,
		OperationID:     schoolResultOperationID(task),
		Operations: []contract.Operation{{
			Type: contract.OpCreate,
			Node: &contract.Node{
				ID:       nodeID,
				Kind:     kind,
				Title:    schoolResultNodeTitle(task),
				Position: schoolResultNodePosition(task.RoomID),
				Width:    width,
				Height:   height,
				Version:  0,
				Metadata: schoolResultNodeMetadata(task, media),
			},
		}},
	}
	// 以任务提交人身份提交：复用 #8 的成员鉴权、幂等回执、revision 推进与提交后广播。
	submitterCtx := WithUser(ctx, model.AuthUser{ID: task.SubmittedBy})
	result, err := ApplyRoomOperations(submitterCtx, task.RoomID, envelope)
	if err != nil {
		// 节点已存在（先前回填已创建）：视为幂等命中，读取既有节点返回，绝不覆盖。
		if contractCode(err) == contract.CodeNodeExists {
			if row, ok, findErr := repository.FindRoomNode(task.RoomID, nodeID); findErr != nil {
				return model.RoomNode{}, false, findErr
			} else if ok {
				return row, true, nil
			}
		}
		return model.RoomNode{}, false, err
	}
	for index := range result.Changes {
		if change := result.Changes[index]; change.Node != nil && change.Node.ID == nodeID {
			return *change.Node, result.Idempotent, nil
		}
	}
	row, ok, err := repository.FindRoomNode(task.RoomID, nodeID)
	if err != nil {
		return model.RoomNode{}, false, err
	}
	if !ok {
		return model.RoomNode{}, false, contractFailure(contract.CodeNotFound, "结果节点写入后未找到", nil)
	}
	return row, result.Idempotent, nil
}

// completeSchoolTaskSuccess 把 running 任务置 succeeded 并释放单执行槽。
func completeSchoolTaskSuccess(task model.SchoolTask, executorID string) error {
	changed, err := repository.FinishRunningSchoolTask(task.ID, strings.TrimSpace(executorID), model.SchoolTaskSucceeded, "")
	if err != nil {
		return err
	}
	if changed {
		return nil
	}
	// 并发下已被结束：已是 succeeded 则幂等成功。
	current, ok, findErr := repository.FindSchoolTaskByID(task.ID)
	if findErr != nil {
		return findErr
	}
	if ok && current.Status == model.SchoolTaskSucceeded {
		return nil
	}
	return contractFailure(CodeSchoolTaskNotRunning, "任务已被结束或不属于该执行器", nil)
}

// existingSchoolResult 读取已成功任务的既有结果节点与媒体，用于幂等返回。
func existingSchoolResult(task model.SchoolTask) (SchoolResultBackfill, error) {
	node, ok, err := repository.FindRoomNode(task.RoomID, schoolResultNodeID(task))
	if err != nil {
		return SchoolResultBackfill{}, err
	}
	if !ok || node.Deleted {
		return SchoolResultBackfill{}, contractFailure(contract.CodeNotFound, "结果节点不存在", nil)
	}
	backfill := SchoolResultBackfill{Task: task.PublicView(), Node: node, Idempotent: true}
	if mediaID, ok := roomMediaIDFromStorageKey(node.Metadata, task.RoomID); ok {
		if media, found, findErr := repository.FindRoomMedia(task.RoomID, mediaID); findErr == nil && found {
			backfill.Media = roomMediaView(media)
		}
	}
	return backfill, nil
}

// schoolResultNodeID 由任务 ID 派生确定性节点 ID：同一任务重复回填恒得同一节点，
// 从根源上保证不产生多份结果节点。
func schoolResultNodeID(task model.SchoolTask) string {
	prefix := "img_"
	if task.Channel.IsVideo() {
		prefix = "vid_"
	}
	return prefix + task.ID
}

// schoolResultOperationID 由任务 ID 派生确定性幂等操作键，重复上报命中同一回执。
func schoolResultOperationID(task model.SchoolTask) string {
	return "schoolresult_" + task.ID
}

// schoolResultNodeSize 返回结果节点的展示尺寸（后端默认值，尊重契约的正有限宽高约束）。
func schoolResultNodeSize(channel model.SchoolChannel) (float64, float64) {
	if channel.IsVideo() {
		return 1280, 720
	}
	return 1024, 1024
}

// schoolResultNodePosition 依据房间现有节点数给出一个确定性错开的位置，避免与已有节点完全重叠。
func schoolResultNodePosition(roomID string) contract.Position {
	nodes, _, err := repository.ListRoomGraph(roomID)
	if err != nil {
		return contract.Position{X: 0, Y: 0}
	}
	offset := float64(len(nodes)) * 80
	return contract.Position{X: offset, Y: offset}
}

// schoolResultNodeMetadata 组装结果节点的公共 metadata，保留提交人/任务关联。
func schoolResultNodeMetadata(task model.SchoolTask, media model.RoomMediaView) map[string]any {
	metadata := map[string]any{
		"storageKey":  media.StorageKey,
		"mimeType":    media.MimeType,
		"bytes":       media.Bytes,
		"taskId":      task.ID,
		"submittedBy": task.SubmittedBy,
		"channel":     string(task.Channel),
	}
	if prompt := strings.TrimSpace(task.Params.Prompt); prompt != "" {
		metadata["prompt"] = prompt
	}
	if task.Channel.IsVideo() && task.Params.VideoSeconds > 0 {
		metadata["seconds"] = fmt.Sprintf("%d", task.Params.VideoSeconds)
	}
	return metadata
}

// schoolResultNodeTitle 生成结果节点标题（含渠道与任务关联，便于画布识别）。
func schoolResultNodeTitle(task model.SchoolTask) string {
	if task.Channel.IsVideo() {
		return "学校 H3 结果视频"
	}
	return "学校千问结果图片"
}

// roomMediaIDFromStorageKey 从节点 metadata 的 storageKey（room:<roomId>:<mediaId>）解析媒体 ID。
func roomMediaIDFromStorageKey(metadataJSON, roomID string) (string, bool) {
	var metadata map[string]any
	if strings.TrimSpace(metadataJSON) == "" {
		return "", false
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return "", false
	}
	key, _ := metadata["storageKey"].(string)
	prefix := "room:" + roomID + ":"
	if !strings.HasPrefix(strings.TrimSpace(key), prefix) {
		return "", false
	}
	mediaID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), prefix))
	if mediaID == "" {
		return "", false
	}
	return mediaID, true
}

// expectedSchoolResultKind 返回渠道对应的结果媒体类别。
func expectedSchoolResultKind(channel model.SchoolChannel) (model.RoomMediaKind, bool) {
	switch channel {
	case model.SchoolChannelQwen:
		return model.RoomMediaKindImage, true
	case model.SchoolChannelH3:
		return model.RoomMediaKindVideo, true
	default:
		return "", false
	}
}

// downloadSchoolResultURL 通过 SSRF 白名单客户端拉取结果媒体并校验完整性/可访问性。
func downloadSchoolResultURL(ctx context.Context, rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, "", errors.New("结果 URL 无效")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	response, err := SafeProxyHTTPClient().Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("结果媒体返回状态 %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxSchoolResultBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(raw)) > MaxSchoolResultBytes {
		return nil, "", errors.New("结果媒体超过大小限制")
	}
	if len(raw) == 0 {
		return nil, "", errors.New("结果媒体为空")
	}
	// 完整性：声明了 Content-Length 时，读取长度必须与之一致。
	if response.ContentLength > 0 && response.ContentLength != int64(len(raw)) {
		return nil, "", errors.New("结果媒体长度不完整")
	}
	return raw, response.Header.Get("Content-Type"), nil
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func headBytes(raw []byte) []byte {
	if len(raw) > 512 {
		return raw[:512]
	}
	return raw
}

func schoolResultExt(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	default:
		return ""
	}
}

// contractCode 提取契约错误码；非契约错误返回空串。
func contractCode(err error) string {
	var contractErr *contract.Error
	if errors.As(err, &contractErr) {
		return contractErr.Code
	}
	return ""
}

// --- 失败信息脱敏 ---

// maxSchoolFailureRunes 限制失败信息长度，避免写入超长/带载荷的日志。
const maxSchoolFailureRunes = 500

var (
	// schoolCredentialPair 匹配 key=value / key: value 形式的凭据键值对，整段替换。
	schoolCredentialPair = regexp.MustCompile(`(?i)\b(api[-_]?key|authorization|token|secret|password|passwd|cookie|session[-_]?id|credential|jwt|master[-_]?key)\b\s*[:=]\s*\S+`)
	// schoolBearerToken 匹配 Bearer 令牌。
	schoolBearerToken = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-+/=]{8,}`)
	// schoolOpaqueBlob 匹配疑似令牌/Cookie 的长不透明串（≥40 字符），作为兜底脱敏。
	schoolOpaqueBlob = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{40,}`)
)

// sanitizeSchoolFailure 清理执行器上报的失败信息：抹除凭据类键值对、Bearer 令牌与
// 长不透明串，并截断长度。绝不把底层凭据写入任务记录或对外视图。
func sanitizeSchoolFailure(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	message = schoolBearerToken.ReplaceAllString(message, "Bearer [已隐藏]")
	message = schoolCredentialPair.ReplaceAllString(message, "[已隐藏]")
	message = schoolOpaqueBlob.ReplaceAllString(message, "[已隐藏]")
	if utf8.RuneCountInString(message) > maxSchoolFailureRunes {
		message = string([]rune(message)[:maxSchoolFailureRunes])
	}
	return message
}
