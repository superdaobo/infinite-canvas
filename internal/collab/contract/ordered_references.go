package contract

import "fmt"

// ValidateOrderedReferences 校验学校 H3 渠道的有序参考图列表（0 基顺序即语义）。
//
// 规则（与 docs/collab-v1/contract.md 第 6 节一致，复用契约的 H3 参考图规则）：
//   - 数量必须在 H3MinReferenceImages..H3MaxReferenceImages（1..9），复用 ValidateH3ReferenceCount；
//   - 每张参考图必须是合法节点 ID（ValidID）；
//   - 同一张参考图不得重复出现。
//
// 本函数只校验、不重排：返回 nil 表示列表可直接按 ref_image_0..8 顺序提交，
// 顺序即语义，提交端不得重排。节点以 metadata.references 的有序数组表达该列表。
func ValidateOrderedReferences(refs []string) error {
	if err := ValidateH3ReferenceCount(len(refs)); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(refs))
	for index, id := range refs {
		if !ValidID(id) {
			return newError(CodeInvalidPayload, fmt.Sprintf("第 %d 张参考图 ID 非法：%q", index+1, id))
		}
		if _, ok := seen[id]; ok {
			return newError(CodeInvalidPayload, fmt.Sprintf("第 %d 张参考图与前面的参考图重复：%s", index+1, id))
		}
		seen[id] = struct{}{}
	}
	return nil
}
