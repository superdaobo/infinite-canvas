package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
)

// 本文件定义学校共享生成任务的领域模型与纯校验（C03-01 / #17 定义层）。
// 只包含提交人/执行人语义、幂等键派生、渠道参数校验与状态机；不含数据库表、
// FIFO 单执行槽（#18）、设备执行器（#19）或媒体回填（#22）。SchoolTask 暂不带
// gorm 标签，持久化表由 #18 落地。规范文本见 docs/collab-v1/school-contract.md。

// --- 渠道 ---

// SchoolChannel 是学校专用生成渠道。学校渠道与个人/其他模型渠道隔离，
// 不共用队列，也不受本契约以外的限制。
type SchoolChannel string

const (
	// SchoolChannelQwen 学校千问：仅文生图，不支持参考图生图。
	SchoolChannelQwen SchoolChannel = "qwen"
	// SchoolChannelH3 学校 H3：1–9 张有序参考图 + 4–15 秒整数视频。
	SchoolChannelH3 SchoolChannel = "h3"
)

// Valid 判断是否为已定义的学校渠道。
func (c SchoolChannel) Valid() bool {
	switch c {
	case SchoolChannelQwen, SchoolChannelH3:
		return true
	default:
		return false
	}
}

// IsImage 表示该渠道产出图片（文生图）。
func (c SchoolChannel) IsImage() bool { return c == SchoolChannelQwen }

// IsVideo 表示该渠道产出视频。
func (c SchoolChannel) IsVideo() bool { return c == SchoolChannelH3 }

// SchoolQueueScope 返回学校任务队列作用域：学校任务按房间共享一个 FIFO 单执行槽，
// 与 DeepSeek 等个人模型渠道完全隔离，不共用队列、不互相串行。
func SchoolQueueScope(roomID string) string {
	return "school:" + strings.TrimSpace(roomID)
}

// School H3 视频秒数边界（整数秒）。
const (
	SchoolH3MinVideoSeconds = 4
	SchoolH3MaxVideoSeconds = 15
)

// --- 状态机 ---

// SchoolTaskStatus 是学校任务状态机状态。
type SchoolTaskStatus string

const (
	SchoolTaskQueued    SchoolTaskStatus = "queued"
	SchoolTaskRunning   SchoolTaskStatus = "running"
	SchoolTaskSucceeded SchoolTaskStatus = "succeeded"
	SchoolTaskFailed    SchoolTaskStatus = "failed"
	SchoolTaskCancelled SchoolTaskStatus = "cancelled"
)

// Valid 判断是否为已定义的任务状态。
func (s SchoolTaskStatus) Valid() bool {
	switch s {
	case SchoolTaskQueued, SchoolTaskRunning, SchoolTaskSucceeded, SchoolTaskFailed, SchoolTaskCancelled:
		return true
	default:
		return false
	}
}

// Terminal 表示状态是否已终止（不再允许迁移）。
func (s SchoolTaskStatus) Terminal() bool {
	switch s {
	case SchoolTaskSucceeded, SchoolTaskFailed, SchoolTaskCancelled:
		return true
	default:
		return false
	}
}

// schoolTaskTransitions 定义合法状态迁移；终态不在键中，即无后继。
var schoolTaskTransitions = map[SchoolTaskStatus][]SchoolTaskStatus{
	SchoolTaskQueued:  {SchoolTaskRunning, SchoolTaskFailed, SchoolTaskCancelled},
	SchoolTaskRunning: {SchoolTaskSucceeded, SchoolTaskFailed, SchoolTaskCancelled},
}

// AllowedSchoolTaskTransitions 返回某状态的合法后继状态副本（可安全修改）。
func AllowedSchoolTaskTransitions(from SchoolTaskStatus) []SchoolTaskStatus {
	next := schoolTaskTransitions[from]
	out := make([]SchoolTaskStatus, len(next))
	copy(out, next)
	return out
}

// CanTransitionSchoolTask 判断状态迁移是否合法；同状态不算合法迁移。
func CanTransitionSchoolTask(from, to SchoolTaskStatus) bool {
	for _, candidate := range schoolTaskTransitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// --- 任务参数与渠道校验 ---

// SchoolTaskParams 是学校渠道的生成参数。ReferenceImages 为有序数组，
// 顺序即语义（第 i 张 -> 工作流字段 ref_image_i）；仅 H3 使用且必须 1–9 张。
type SchoolTaskParams struct {
	Prompt          string   `json:"prompt"`
	ReferenceImages []string `json:"referenceImages,omitempty"`
	VideoSeconds    int      `json:"videoSeconds,omitempty"`
	Size            string   `json:"size,omitempty"`
	Quality         string   `json:"quality,omitempty"`
}

// ValidateSchoolChannelParams 校验学校渠道参数。只约束学校渠道，不影响
// DeepSeek 等其他模型渠道；H3 参考图数量复用契约的 H3 校验。
func ValidateSchoolChannelParams(channel SchoolChannel, params SchoolTaskParams) error {
	if !channel.Valid() {
		return &contract.Error{Code: contract.CodeInvalidPayload, Message: fmt.Sprintf("未知学校渠道 %q", channel)}
	}
	switch channel {
	case SchoolChannelQwen:
		// 学校千问仅支持文生图，不接受参考图与视频秒数。
		if len(params.ReferenceImages) > 0 {
			return &contract.Error{Code: contract.CodeInvalidPayload, Message: "学校千问渠道仅支持文生图，不接受参考图"}
		}
		if params.VideoSeconds != 0 {
			return &contract.Error{Code: contract.CodeInvalidPayload, Message: "学校千问渠道不支持视频秒数"}
		}
	case SchoolChannelH3:
		if err := contract.ValidateH3ReferenceCount(len(params.ReferenceImages)); err != nil {
			return err
		}
		if params.VideoSeconds < SchoolH3MinVideoSeconds || params.VideoSeconds > SchoolH3MaxVideoSeconds {
			return &contract.Error{Code: contract.CodeInvalidPayload, Message: fmt.Sprintf(
				"学校 H3 视频秒数必须是 %d..%d 的整数秒，当前 %d",
				SchoolH3MinVideoSeconds, SchoolH3MaxVideoSeconds, params.VideoSeconds)}
		}
	}
	return nil
}

// --- 幂等键派生 ---

// schoolTaskIdempotencyNamespace 是幂等键命名空间，避免与其他摘要混淆。
const schoolTaskIdempotencyNamespace = "school-task-v1"

// SchoolTaskIdempotencyKey 派生稳定幂等键：同一 (roomID, submittedBy, clientTaskID)
// 恒等得到同一键。clientTaskID 由提交端生成，在房间/用户维度唯一。
func SchoolTaskIdempotencyKey(roomID, submittedBy, clientTaskID string) string {
	parts := []string{
		schoolTaskIdempotencyNamespace,
		strings.TrimSpace(roomID),
		strings.TrimSpace(submittedBy),
		strings.TrimSpace(clientTaskID),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// SchoolTaskID 由幂等键派生确定性任务 ID，重复提交命中同一任务。
func SchoolTaskID(roomID, submittedBy, clientTaskID string) string {
	return "school_" + SchoolTaskIdempotencyKey(roomID, submittedBy, clientTaskID)[:32]
}

// --- 任务 ---

// SchoolTask 是学校共享生成任务。
// 提交人 SubmittedBy 是真实用户账号（房间成员）；执行人 ExecutorID 是受限设备身份，
// 与用户 JWT 分离，领取任务时绑定。UpstreamTaskID 一旦绑定不可替换。
type SchoolTask struct {
	ID             string           `json:"id"`
	RoomID         string           `json:"roomId"`
	SubmittedBy    string           `json:"submittedBy"`
	ExecutorID     string           `json:"executorId,omitempty"`
	ClientTaskID   string           `json:"clientTaskId"`
	UpstreamTaskID string           `json:"upstreamTaskId,omitempty"`
	Channel        SchoolChannel    `json:"channel"`
	Status         SchoolTaskStatus `json:"status"`
	Params         SchoolTaskParams `json:"params"`
	Error          string           `json:"error,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}

// SchoolTaskPublicView 是学校任务对外可见字段，明确不含学校凭据、Cookie、
// 设备令牌、工作流明文 ID 等秘密；查询响应只返回该结构。
type SchoolTaskPublicView struct {
	ID           string           `json:"id"`
	RoomID       string           `json:"roomId"`
	SubmittedBy  string           `json:"submittedBy"`
	ExecutorID   string           `json:"executorId,omitempty"`
	ClientTaskID string           `json:"clientTaskId"`
	Channel      SchoolChannel    `json:"channel"`
	Status       SchoolTaskStatus `json:"status"`
	Error        string           `json:"error,omitempty"`
}

// PublicView 返回可对外暴露的任务视图，剥离内部上游任务号与参考图原值。
func (t SchoolTask) PublicView() SchoolTaskPublicView {
	return SchoolTaskPublicView{
		ID:           t.ID,
		RoomID:       t.RoomID,
		SubmittedBy:  t.SubmittedBy,
		ExecutorID:   t.ExecutorID,
		ClientTaskID: t.ClientTaskID,
		Channel:      t.Channel,
		Status:       t.Status,
		Error:        t.Error,
	}
}

// ValidateSchoolTask 校验任务归属与渠道参数，供提交与执行前二次校验复用。
func ValidateSchoolTask(task SchoolTask) error {
	if !contract.ValidID(strings.TrimSpace(task.RoomID)) {
		return &contract.Error{Code: contract.CodeInvalidPayload, Message: "房间 ID 非法"}
	}
	if !validSchoolIdentity(task.SubmittedBy) {
		return &contract.Error{Code: contract.CodeInvalidPayload, Message: "提交人账号无效"}
	}
	if strings.TrimSpace(task.ExecutorID) != "" && !validSchoolIdentity(task.ExecutorID) {
		return &contract.Error{Code: contract.CodeInvalidPayload, Message: "执行设备身份无效"}
	}
	if strings.TrimSpace(task.ClientTaskID) == "" {
		return &contract.Error{Code: contract.CodeInvalidPayload, Message: "clientTaskId 不能为空"}
	}
	if !task.Channel.Valid() {
		return &contract.Error{Code: contract.CodeInvalidPayload, Message: fmt.Sprintf("未知学校渠道 %q", task.Channel)}
	}
	if !task.Status.Valid() {
		return &contract.Error{Code: contract.CodeInvalidPayload, Message: fmt.Sprintf("未知任务状态 %q", task.Status)}
	}
	return ValidateSchoolChannelParams(task.Channel, task.Params)
}

// validSchoolIdentity 校验提交人/执行设备身份：非空且长度受限。
// 账号 ID 形态由账号体系决定，不强制 nanoid 字符集。
func validSchoolIdentity(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && len(id) <= contract.MaxIDLength
}
