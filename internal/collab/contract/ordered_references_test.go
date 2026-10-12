package contract

import (
	"errors"
	"fmt"
	"testing"
)

// TestValidateOrderedReferences 覆盖学校 H3 有序参考图的 1/2/9 张正例与
// 0/10 张、重复、非法 ID 负例，并确认顺序即语义（第 i 张映射到 ref_image_i，不重排）。
func TestValidateOrderedReferences(t *testing.T) {
	for _, count := range []int{1, 2, 9} {
		refs := make([]string, count)
		for index := range refs {
			refs[index] = "img_ref_" + string(rune('a'+index))
		}
		if err := ValidateOrderedReferences(refs); err != nil {
			t.Fatalf("%d 张有序参考图应合法：%v", count, err)
		}
		fields, err := H3ReferenceMapping(refs)
		if err != nil {
			t.Fatalf("%d 张参考图映射失败：%v", count, err)
		}
		for index := range refs {
			want := fmt.Sprintf("ref_image_%d", index)
			if fields[index] != want {
				t.Fatalf("第 %d 张应映射到 %s，实际 %s", index, want, fields[index])
			}
		}
	}

	// 0 张（低于下限）与 10 张（超过上限）都应被拒绝，错误码为 invalid_payload。
	if err := ValidateOrderedReferences(nil); err == nil {
		t.Fatal("0 张参考图应被拒绝")
	} else {
		assertInvalidPayload(t, err)
	}
	ten := make([]string, 10)
	for index := range ten {
		ten[index] = "img_ref_" + string(rune('a'+index))
	}
	if err := ValidateOrderedReferences(ten); err == nil {
		t.Fatal("10 张参考图应被拒绝")
	} else {
		assertInvalidPayload(t, err)
	}

	// 重复参考图应被拒绝。
	if err := ValidateOrderedReferences([]string{"img_ref_a", "img_ref_a"}); err == nil {
		t.Fatal("重复参考图应被拒绝")
	} else {
		assertInvalidPayload(t, err)
	}

	// 非法 ID 应被拒绝。
	if err := ValidateOrderedReferences([]string{"img_ref_a", "bad id!"}); err == nil {
		t.Fatal("非法参考图 ID 应被拒绝")
	} else {
		assertInvalidPayload(t, err)
	}
}

func assertInvalidPayload(t *testing.T, err error) {
	t.Helper()
	var contractErr *Error
	if !errors.As(err, &contractErr) {
		t.Fatalf("期望 *contract.Error，实际 %T: %v", err, err)
	}
	if contractErr.Code != CodeInvalidPayload {
		t.Fatalf("期望错误码 %s，实际 %s", CodeInvalidPayload, contractErr.Code)
	}
}
