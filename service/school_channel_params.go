package service

import (
	"fmt"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
)

// 学校渠道上游参数构建与校验（C03-04 / #21）。
//
// 本文件把房间内有序参考图与生成参数构建为设备执行器可提交的稳定输入，全部为纯函数：
// 同一份 model.SchoolTaskParams 恒等得到同一份输入，排队中的任务不因用户后续编辑画布而
// 改变（生成参数与媒体在提交时已快照进 school_tasks.params_json）。只做 mock 上游，
// 不接触真实学校设备 / Cookie / Chrome / Comfy Bridge。

// 学校渠道固定工作流标识（与 PoC queue-worker.mjs 对齐；真实标识由设备侧配置，本层只固定契约）。
const (
	// SchoolH3WorkflowID 是学校 H3 视频工作流标识（有序多图参考）。
	SchoolH3WorkflowID = "PANDA_H3_A40_FP8_multi_reference.api.json"
	// SchoolQwenWorkflowID 是学校千问文生图工作流标识。
	SchoolQwenWorkflowID = "Qwen_Image_21_school.api.json"
)

// SchoolChannelCapability 返回渠道对应的上游能力标识（PoC expectedCapability：video / image）。
func SchoolChannelCapability(channel model.SchoolChannel) (string, error) {
	switch channel {
	case model.SchoolChannelH3:
		return "video", nil
	case model.SchoolChannelQwen:
		return "image", nil
	default:
		return "", contractFailure(contract.CodeInvalidPayload, fmt.Sprintf("未知学校渠道 %q", channel), nil)
	}
}

// SchoolChannelWorkflowID 返回渠道固定工作流标识。
func SchoolChannelWorkflowID(channel model.SchoolChannel) (string, error) {
	switch channel {
	case model.SchoolChannelH3:
		return SchoolH3WorkflowID, nil
	case model.SchoolChannelQwen:
		return SchoolQwenWorkflowID, nil
	default:
		return "", contractFailure(contract.CodeInvalidPayload, fmt.Sprintf("未知学校渠道 %q", channel), nil)
	}
}

// SchoolReferenceInput 是一张有序参考图的固定输入映射（0 基顺序即语义）。
type SchoolReferenceInput struct {
	Index  int    `json:"index"`
	Field  string `json:"field"`
	Alias  string `json:"alias,omitempty"`
	NodeID string `json:"nodeId"`
}

// BuildSchoolReferenceInputs 把有序参考图节点 ID 映射为 H3 固定输入 ref_image_0..8。
// 顺序稳定、可读回：只校验数量 1..9、ID 合法、不重复（是否为图片节点由提交端先行确认）。
func BuildSchoolReferenceInputs(refs []string) ([]SchoolReferenceInput, error) {
	if err := contract.ValidateOrderedReferences(refs); err != nil {
		return nil, err
	}
	inputs := make([]SchoolReferenceInput, 0, len(refs))
	for index, nodeID := range refs {
		field, err := contract.H3ReferenceField(index)
		if err != nil {
			return nil, err
		}
		alias, _ := contract.H3ReferenceAlias(index)
		inputs = append(inputs, SchoolReferenceInput{Index: index, Field: field, Alias: alias, NodeID: nodeID})
	}
	return inputs, nil
}

// SchoolReferenceNodeIDs 从固定输入读回有序节点 ID（顺序即语义）。
func SchoolReferenceNodeIDs(inputs []SchoolReferenceInput) []string {
	ids := make([]string, 0, len(inputs))
	for _, input := range inputs {
		ids = append(ids, input.NodeID)
	}
	return ids
}

// SchoolImageReference 是一张候选参考图：节点 ID 与节点类别。
type SchoolImageReference struct {
	NodeID string
	Kind   contract.NodeKind
}

// ValidateSchoolImageReferences 校验候选参考图全部为图片节点（H3 只接受图片），
// 并复用有序参考图规则（1..9、ID 合法、不重复），返回按输入顺序的节点 ID。
func ValidateSchoolImageReferences(refs []SchoolImageReference) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for index, ref := range refs {
		if ref.Kind != contract.NodeKindImage {
			return nil, contractFailure(contract.CodeInvalidPayload,
				fmt.Sprintf("第 %d 张参考图不是图片节点（%s）", index+1, ref.Kind), nil)
		}
		ids = append(ids, ref.NodeID)
	}
	if err := contract.ValidateOrderedReferences(ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// SchoolUpstreamInput 是学校渠道提交给设备执行器的稳定输入，不因后续画布编辑而改变。
type SchoolUpstreamInput struct {
	Channel         model.SchoolChannel    `json:"channel"`
	WorkflowID      string                 `json:"workflowId"`
	Capability      string                 `json:"capability"`
	Prompt          string                 `json:"prompt"`
	ReferenceInputs []SchoolReferenceInput `json:"referenceInputs,omitempty"`
	VideoSeconds    int                    `json:"videoSeconds,omitempty"`
	Size            string                 `json:"size,omitempty"`
	Quality         string                 `json:"quality,omitempty"`
	Count           int                    `json:"count,omitempty"`
}

// BuildSchoolUpstreamInput 校验学校渠道参数并构建稳定上游输入：固定工作流标识与能力、
// 渲染提示词占位、按顺序固定参考图字段。纯函数，供设备执行器与结果回填复用。
func BuildSchoolUpstreamInput(channel model.SchoolChannel, params model.SchoolTaskParams) (SchoolUpstreamInput, error) {
	if err := model.ValidateSchoolChannelParams(channel, params); err != nil {
		return SchoolUpstreamInput{}, err
	}
	workflowID, err := SchoolChannelWorkflowID(channel)
	if err != nil {
		return SchoolUpstreamInput{}, err
	}
	capability, err := SchoolChannelCapability(channel)
	if err != nil {
		return SchoolUpstreamInput{}, err
	}
	prompt, err := model.RenderSchoolPrompt(params.Prompt, len(params.ReferenceImages))
	if err != nil {
		return SchoolUpstreamInput{}, err
	}
	input := SchoolUpstreamInput{
		Channel:      channel,
		WorkflowID:   workflowID,
		Capability:   capability,
		Prompt:       prompt,
		VideoSeconds: params.VideoSeconds,
		Size:         params.Size,
		Quality:      params.Quality,
		Count:        params.Count,
	}
	if channel == model.SchoolChannelH3 {
		refs, err := BuildSchoolReferenceInputs(params.ReferenceImages)
		if err != nil {
			return SchoolUpstreamInput{}, err
		}
		input.ReferenceInputs = refs
	}
	if channel == model.SchoolChannelQwen && input.Count == 0 {
		input.Count = 1
	}
	return input, nil
}
