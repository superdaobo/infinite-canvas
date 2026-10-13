package service

import (
	"reflect"
	"testing"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
)

// 学校渠道参数构建与校验测试（C03-04 / #21）：H3 有序 1–9 张参考图、千问文生图
// 图片参数兼容与提示词占位替换。全部为纯函数用例，只走 mock 语义，不接触真实学校设备。

// TestBuildSchoolReferenceInputs_OrderRoundTrip 验证 1/2/9 张参考图的顺序映射与读回。
func TestBuildSchoolReferenceInputs_OrderRoundTrip(t *testing.T) {
	for _, count := range []int{1, 2, 9} {
		refs := make([]string, count)
		for i := range refs {
			refs[i] = "img-" + string(rune('a'+i))
		}
		inputs, err := BuildSchoolReferenceInputs(refs)
		if err != nil {
			t.Fatalf("%d 张参考图应通过，实际 %v", count, err)
		}
		if len(inputs) != count {
			t.Fatalf("应产生 %d 个固定输入，实际 %d", count, len(inputs))
		}
		for index, input := range inputs {
			wantField := "ref_image_" + string(rune('0'+index))
			if input.Field != wantField {
				t.Fatalf("第 %d 张字段应为 %s，实际 %s", index, wantField, input.Field)
			}
			if input.NodeID != refs[index] {
				t.Fatalf("第 %d 张节点应保持顺序 %s，实际 %s", index, refs[index], input.NodeID)
			}
		}
		// 第 0 张带无编号别名 ref_image，其余不带。
		if inputs[0].Alias != "ref_image" {
			t.Fatalf("第 0 张应带别名 ref_image，实际 %q", inputs[0].Alias)
		}
		if count > 1 && inputs[1].Alias != "" {
			t.Fatalf("非第 0 张不应带别名，实际 %q", inputs[1].Alias)
		}
		// 读回顺序必须与输入一致。
		readBack := SchoolReferenceNodeIDs(inputs)
		for index := range refs {
			if readBack[index] != refs[index] {
				t.Fatalf("读回第 %d 张应为 %s，实际 %s", index, refs[index], readBack[index])
			}
		}
	}
}

// TestBuildSchoolReferenceInputs_Rejects 验证 0/10 张、重复与非法 ID 被拒。
func TestBuildSchoolReferenceInputs_Rejects(t *testing.T) {
	cases := []struct {
		name string
		refs []string
	}{
		{"零张被拒", nil},
		{"十张被拒", []string{"i1", "i2", "i3", "i4", "i5", "i6", "i7", "i8", "i9", "i10"}},
		{"重复被拒", []string{"img-a", "img-a"}},
		{"非法 ID 被拒", []string{"!!"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildSchoolReferenceInputs(tc.refs); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
				t.Fatalf("应 invalid_payload，实际 %v", err)
			}
		})
	}
}

// TestValidateSchoolImageReferences 验证非图片引用被明确拒绝，图片引用按序通过。
func TestValidateSchoolImageReferences(t *testing.T) {
	ok, err := ValidateSchoolImageReferences([]SchoolImageReference{
		{NodeID: "img-a", Kind: contract.NodeKindImage},
		{NodeID: "img-b", Kind: contract.NodeKindImage},
	})
	if err != nil || len(ok) != 2 || ok[0] != "img-a" || ok[1] != "img-b" {
		t.Fatalf("图片引用应按序通过：%v err=%v", ok, err)
	}
	if _, err := ValidateSchoolImageReferences([]SchoolImageReference{
		{NodeID: "img-a", Kind: contract.NodeKindImage},
		{NodeID: "vid-b", Kind: contract.NodeKindVideo},
	}); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("非图片引用应 invalid_payload，实际 %v", err)
	}
}

// TestBuildSchoolUpstreamInput_H3 验证 H3 稳定输入：工作流、能力、秒数与有序字段。
func TestBuildSchoolUpstreamInput_H3(t *testing.T) {
	input, err := BuildSchoolUpstreamInput(model.SchoolChannelH3, model.SchoolTaskParams{
		Prompt:          "以 @图片1 为首帧，参考 @图片2",
		ReferenceImages: []string{"img-a", "img-b"},
		VideoSeconds:    15,
	})
	if err != nil {
		t.Fatalf("合法 H3 参数应通过，实际 %v", err)
	}
	if input.WorkflowID != SchoolH3WorkflowID || input.Capability != "video" {
		t.Fatalf("H3 应固定工作流与能力，实际 %s/%s", input.WorkflowID, input.Capability)
	}
	if input.Prompt != "以 <Picture 1> 为首帧，参考 <Picture 2>" {
		t.Fatalf("提示词占位应替换，实际 %q", input.Prompt)
	}
	if input.VideoSeconds != 15 {
		t.Fatalf("秒数应为 15，实际 %d", input.VideoSeconds)
	}
	if ids := SchoolReferenceNodeIDs(input.ReferenceInputs); len(ids) != 2 || ids[0] != "img-a" || ids[1] != "img-b" {
		t.Fatalf("参考图字段应保持顺序，实际 %v", ids)
	}

	for _, seconds := range []int{3, 16} {
		if _, err := BuildSchoolUpstreamInput(model.SchoolChannelH3, model.SchoolTaskParams{
			ReferenceImages: []string{"img-a"}, VideoSeconds: seconds,
		}); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
			t.Fatalf("H3 秒数 %d 应被拒，实际 %v", seconds, err)
		}
	}
}

// TestBuildSchoolUpstreamInput_Qwen 验证千问文生图：拒绝参考图、固定工作流、数量默认 1。
func TestBuildSchoolUpstreamInput_Qwen(t *testing.T) {
	input, err := BuildSchoolUpstreamInput(model.SchoolChannelQwen, model.SchoolTaskParams{Prompt: "海边小镇", Size: "16:9"})
	if err != nil {
		t.Fatalf("合法千问参数应通过，实际 %v", err)
	}
	if input.WorkflowID != SchoolQwenWorkflowID || input.Capability != "image" {
		t.Fatalf("千问应固定工作流与能力，实际 %s/%s", input.WorkflowID, input.Capability)
	}
	if input.Count != 1 {
		t.Fatalf("千问数量未指定应默认 1，实际 %d", input.Count)
	}
	if len(input.ReferenceInputs) != 0 {
		t.Fatalf("千问不应产生参考图字段，实际 %v", input.ReferenceInputs)
	}
	if _, err := BuildSchoolUpstreamInput(model.SchoolChannelQwen, model.SchoolTaskParams{
		Prompt: "p", ReferenceImages: []string{"img-a"},
	}); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("千问带参考图应被拒，实际 %v", err)
	}
}

// TestBuildSchoolUpstreamInput_PromptPlaceholder 验证越界占位在构建阶段被拒。
func TestBuildSchoolUpstreamInput_PromptPlaceholder(t *testing.T) {
	if _, err := BuildSchoolUpstreamInput(model.SchoolChannelH3, model.SchoolTaskParams{
		Prompt: "参考 @图片3", ReferenceImages: []string{"img-a", "img-b"}, VideoSeconds: 5,
	}); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("越界占位应被拒，实际 %v", err)
	}
	if _, err := BuildSchoolUpstreamInput(model.SchoolChannelQwen, model.SchoolTaskParams{
		Prompt: "参考 @图片1",
	}); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("千问出现图片占位应被拒，实际 %v", err)
	}
}

// TestSchoolChannelWorkflowHelpers 验证未知渠道返回契约错误。
func TestSchoolChannelWorkflowHelpers(t *testing.T) {
	if _, err := SchoolChannelWorkflowID(model.SchoolChannel("doubao")); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("未知渠道工作流应 invalid_payload，实际 %v", err)
	}
	if _, err := SchoolChannelCapability(model.SchoolChannel("doubao")); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("未知渠道能力应 invalid_payload，实际 %v", err)
	}
}

// TestBuildSchoolUpstreamInput_StableSnapshot 验证同一份参数恒等得到同一份输入，
// 且构建后修改调用方切片/字段不会改写已构建输入（排队中的任务不被后续画布编辑改变）。
func TestBuildSchoolUpstreamInput_StableSnapshot(t *testing.T) {
	refs := []string{"img-a", "img-b", "img-c"}
	params := model.SchoolTaskParams{
		Prompt:          "融合 @图片1 @图片2 @图片3",
		ReferenceImages: refs,
		VideoSeconds:    9,
	}
	first, err := BuildSchoolUpstreamInput(model.SchoolChannelH3, params)
	if err != nil {
		t.Fatalf("首次构建应通过，实际 %v", err)
	}
	second, err := BuildSchoolUpstreamInput(model.SchoolChannelH3, params)
	if err != nil {
		t.Fatalf("再次构建应通过，实际 %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("同一参数应得到同一输入：\n%+v\n%+v", first, second)
	}
	// 构建后修改调用方数据，不应影响已构建的输入（稳定快照）。
	refs[0] = "img-mutated"
	params.Prompt = "changed"
	params.VideoSeconds = 4
	ids := SchoolReferenceNodeIDs(first.ReferenceInputs)
	if ids[0] != "img-a" || ids[1] != "img-b" || ids[2] != "img-c" {
		t.Fatalf("参考图顺序被后续编辑改写：%v", ids)
	}
	if first.Prompt != "融合 <Picture 1> <Picture 2> <Picture 3>" {
		t.Fatalf("提示词被后续编辑改写：%q", first.Prompt)
	}
	if first.VideoSeconds != 9 {
		t.Fatalf("秒数被后续编辑改写：%d", first.VideoSeconds)
	}
}
