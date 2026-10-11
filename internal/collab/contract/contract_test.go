package contract

import (
	"errors"
	"testing"
)

// sampleNodes 是 8 类节点的真实样例，public 字段完整、可机械校验。
func sampleNodes() []Node {
	return []Node{
		{
			ID: "img_5Kd9xQ2mNp7", Kind: NodeKindImage, Title: "首帧参考", Version: 3,
			Position: Position{X: 120, Y: -40}, Width: 1024, Height: 1024,
			Metadata: map[string]any{
				"prompt": "赛博朋克城市夜景，霓虹倒影", "negativePrompt": "模糊, 低质量",
				"model": "seedream-4.0", "channelId": "channel_zh_01",
				"size": "16:9", "quality": "high", "count": 1,
				"generationMode": "image", "generationType": "generation",
				"storageKey": "rooms/r1/nodes/img_5Kd9xQ2mNp7.png", "mimeType": "image/png", "bytes": 204800,
				"naturalWidth": 1024, "naturalHeight": 1024, "freeResize": false,
			},
		},
		{
			ID: "pano_7Qw3eR9tYb1", Kind: NodeKindPanorama, Title: "360 全景", Version: 2,
			Position: Position{X: 1500, Y: 0}, Width: 2048, Height: 1024,
			Metadata: map[string]any{
				"panoramaSourcePrompt": "雪山日出全景", "panoramaFinalPrompt": "雪山日出全景，8k",
				"panoramaProjection": "equirectangular", "storageKey": "rooms/r1/nodes/pano.png",
			},
		},
		{
			ID: "vid_2Mn8bV4cXz6", Kind: NodeKindVideo, Title: "15s 成片", Version: 1,
			Position: Position{X: 2600, Y: 0}, Width: 1280, Height: 720,
			Metadata: map[string]any{
				"prompt": "镜头推进", "model": "minimax_h3_image_audio_to_video_v2_15s",
				"seconds": "15", "vquality": "768p", "generateAudio": "true", "watermark": "false",
				"firstFrameNodeId": "img_5Kd9xQ2mNp7", "storageKey": "rooms/r1/nodes/vid.mp4", "durationMs": 15000,
			},
		},
		{
			ID: "aud_9Pl4kH6jRt2", Kind: NodeKindAudio, Title: "旁白", Version: 4,
			Position: Position{X: 2600, Y: 800}, Width: 480, Height: 120,
			Metadata: map[string]any{
				"audioVoice": "zh_female_01", "audioFormat": "mp3", "audioSpeed": "1.0",
				"audioInstructions": "沉稳", "storageKey": "rooms/r1/nodes/aud.mp3", "durationMs": 8200,
			},
		},
		{
			ID: "txt_3Rt7yU1iOp5", Kind: NodeKindText, Title: "提示词草稿", Version: 5,
			Position: Position{X: -400, Y: 0}, Width: 360, Height: 200,
			Metadata: map[string]any{"content": "黄昏下的海边小镇，暖色调", "fontSize": 14},
		},
		{
			ID: "grp_6Yu2iO8pAs3", Kind: NodeKindGroup, Title: "第一幕", Version: 1,
			Position: Position{X: -600, Y: -200}, Width: 900, Height: 700,
			Metadata: map[string]any{"groupId": "grp_6Yu2iO8pAs3"},
		},
		{
			ID: "cfg_8Io3pA5sDf9", Kind: NodeKindConfig, Title: "生成配置", Version: 2,
			Position: Position{X: -400, Y: 400}, Width: 320, Height: 240,
			Metadata: map[string]any{"content": `{"size":"16:9","quality":"high","count":1}`},
		},
		{
			ID: "dir_1As4dF6gHj7", Kind: NodeKindDirector, Title: "导演台", Version: 1,
			Position: Position{X: 800, Y: -600}, Width: 960, Height: 540,
			Metadata: map[string]any{
				"directorProject":   map[string]any{"shots": []any{}},
				"primaryImageId":    "img_5Kd9xQ2mNp7",
				"references":        []any{"img_5Kd9xQ2mNp7"},
				"cameraControl":     map[string]any{"enabled": true, "camera": "dolly", "lens": "35mm", "focalLength": 35, "aperture": 2.8},
				"multiShot":         "true",
				"klingImageNodeIds": []any{"img_5Kd9xQ2mNp7"},
			},
		},
	}
}

func TestValidateNode_AllEightKinds(t *testing.T) {
	nodes := sampleNodes()
	if len(nodes) != 8 {
		t.Fatalf("样例节点数应为 8，实际 %d", len(nodes))
	}
	for _, node := range nodes {
		node := node
		t.Run(string(node.Kind), func(t *testing.T) {
			if err := ValidateNode(&node); err != nil {
				t.Fatalf("合法样例被拒绝：%v", err)
			}
		})
	}
}

func TestValidateNode_RejectsBadInput(t *testing.T) {
	base := Node{ID: "img_5Kd9xQ2mNp7", Kind: NodeKindImage, Title: "t", Version: 1,
		Position: Position{X: 0, Y: 0}, Width: 10, Height: 10}

	cases := []struct {
		name string
		mut  func(n *Node)
		code string
	}{
		{"空 ID", func(n *Node) { n.ID = "" }, CodeInvalidPayload},
		{"未知种类", func(n *Node) { n.Kind = "widget" }, CodeInvalidPayload},
		{"零宽", func(n *Node) { n.Width = 0 }, CodeInvalidPayload},
		{"NaN 坐标", func(n *Node) { n.Position.X = nan() }, CodeInvalidPayload},
		{"版本为 0", func(n *Node) { n.Version = 0 }, CodeInvalidPayload},
		{"模型密钥进入公共图", func(n *Node) { n.Metadata = map[string]any{"apiKey": "sk-live"} }, CodeForbiddenField},
		{"Authorization 头", func(n *Node) { n.Metadata = map[string]any{"Authorization": "Bearer x"} }, CodeForbiddenField},
		{"text 缺少 content", func(n *Node) { n.Kind = NodeKindText }, CodeInvalidPayload},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			node := base
			node.Metadata = nil
			tc.mut(&node)
			err := ValidateNode(&node)
			assertCode(t, err, tc.code)
		})
	}
}

func TestValidateNode_ConfigContentMustBeJSONObject(t *testing.T) {
	node := Node{ID: "cfg_8Io3pA5sDf9", Kind: NodeKindConfig, Title: "c", Version: 1,
		Position: Position{X: 0, Y: 0}, Width: 10, Height: 10,
		Metadata: map[string]any{"content": "[1,2,3]"}}
	assertCode(t, ValidateNode(&node), CodeInvalidPayload)

	node.Metadata["content"] = `{"a":1}`
	if err := ValidateNode(&node); err != nil {
		t.Fatalf("合法 JSON 对象被拒绝：%v", err)
	}
}

func TestValidateOperation_AllTypes(t *testing.T) {
	version := 2
	newNode := Node{ID: "img_5Kd9xQ2mNp7", Kind: NodeKindImage, Title: "n", Version: 0,
		Position: Position{X: 1, Y: 2}, Width: 10, Height: 10}
	edge := Edge{ID: "edg_1Ab2Cd3Ef4", FromNodeID: "img_5Kd9xQ2mNp7", ToNodeID: "txt_3Rt7yU1iOp5"}

	valid := []Operation{
		{Type: OpCreate, Node: &newNode},
		{Type: OpUpdate, ID: "img_5Kd9xQ2mNp7", Version: &version, Fields: map[string]any{"title": "x"}},
		{Type: OpDelete, ID: "img_5Kd9xQ2mNp7", Version: &version},
		{Type: OpConnect, Edge: &edge},
		{Type: OpDisconnect, ID: "edg_1Ab2Cd3Ef4", Version: &version},
	}
	for _, op := range valid {
		if err := ValidateOperation(op); err != nil {
			t.Fatalf("合法操作 %s 被拒绝：%v", op.Type, err)
		}
	}

	bad := []struct {
		name string
		op   Operation
		code string
	}{
		{"create 缺 node", Operation{Type: OpCreate}, CodeInvalidPayload},
		{"create 版本非 0", Operation{Type: OpCreate, Node: &Node{ID: "img_5Kd9xQ2mNp7", Kind: NodeKindImage, Title: "n", Version: 1, Position: Position{}, Width: 1, Height: 1}}, CodeInvalidPayload},
		{"update 缺版本", Operation{Type: OpUpdate, ID: "img_5Kd9xQ2mNp7", Fields: map[string]any{"title": "x"}}, CodeInvalidPayload},
		{"update 空 fields", Operation{Type: OpUpdate, ID: "img_5Kd9xQ2mNp7", Version: &version, Fields: map[string]any{}}, CodeInvalidPayload},
		{"update 密钥字段", Operation{Type: OpUpdate, ID: "img_5Kd9xQ2mNp7", Version: &version, Fields: map[string]any{"secretToken": "x"}}, CodeForbiddenField},
		{"delete 缺版本", Operation{Type: OpDelete, ID: "img_5Kd9xQ2mNp7"}, CodeInvalidPayload},
		{"connect 自环", Operation{Type: OpConnect, Edge: &Edge{ID: "edg_1Ab2Cd3Ef4", FromNodeID: "img_5Kd9xQ2mNp7", ToNodeID: "img_5Kd9xQ2mNp7"}}, CodeInvalidPayload},
		{"disconnect 缺版本", Operation{Type: OpDisconnect, ID: "edg_1Ab2Cd3Ef4"}, CodeInvalidPayload},
		{"未知类型", Operation{Type: "rename", ID: "img_5Kd9xQ2mNp7"}, CodeInvalidPayload},
	}
	for _, tc := range bad {
		tc := tc
		t.Run(tc.name, func(t *testing.T) { assertCode(t, ValidateOperation(tc.op), tc.code) })
	}
}

func TestValidateEnvelope(t *testing.T) {
	version := 1
	ok := Envelope{
		ProtocolVersion: ProtocolVersion,
		RoomID:          "room_1Ab2Cd3Ef4",
		OperationID:     "op_9Zy8Xw7Vu6",
		Operations: []Operation{
			{Type: OpUpdate, ID: "img_5Kd9xQ2mNp7", Version: &version, Fields: map[string]any{"title": "x"}},
			{Type: OpUpdate, ID: "txt_3Rt7yU1iOp5", Version: &version, Fields: map[string]any{"title": "y"}},
		},
	}
	if err := ValidateEnvelope(ok); err != nil {
		t.Fatalf("合法信封被拒绝：%v", err)
	}

	badVersion := ok
	badVersion.ProtocolVersion = "2"
	assertCode(t, ValidateEnvelope(badVersion), CodeProtocolMismatch)

	duplicate := ok
	duplicate.Operations[1].ID = duplicate.Operations[0].ID
	assertCode(t, ValidateEnvelope(duplicate), CodeDuplicateTarget)

	empty := ok
	empty.Operations = nil
	assertCode(t, ValidateEnvelope(empty), CodeInvalidPayload)

	tooMany := ok
	tooMany.Operations = make([]Operation, MaxOperationsPerBatch+1)
	for i := range tooMany.Operations {
		id := "img_" + string(rune('a'+i%26)) + "0000000"
		tooMany.Operations[i] = Operation{Type: OpDelete, ID: id, Version: &version}
	}
	assertCode(t, ValidateEnvelope(tooMany), CodePayloadTooLarge)
}

func TestValidateGraph(t *testing.T) {
	nodes := map[string]Node{
		"txt_3Rt7yU1iOp5": {ID: "txt_3Rt7yU1iOp5", Kind: NodeKindText, Version: 1},
		"img_5Kd9xQ2mNp7": {ID: "img_5Kd9xQ2mNp7", Kind: NodeKindImage, Version: 1},
		"aud_9Pl4kH6jRt2": {ID: "aud_9Pl4kH6jRt2", Kind: NodeKindAudio, Version: 1},
	}
	ok := []Edge{
		{ID: "edg_1Ab2Cd3Ef4", FromNodeID: "txt_3Rt7yU1iOp5", ToNodeID: "img_5Kd9xQ2mNp7"},
		{ID: "edg_2Bc3De4Fg5", FromNodeID: "img_5Kd9xQ2mNp7", ToNodeID: "aud_9Pl4kH6jRt2"},
	}
	if err := ValidateGraph(nodes, ok); err != nil {
		t.Fatalf("合法图被拒绝：%v", err)
	}

	cycle := []Edge{
		{ID: "edg_1Ab2Cd3Ef4", FromNodeID: "txt_3Rt7yU1iOp5", ToNodeID: "img_5Kd9xQ2mNp7"},
		{ID: "edg_2Bc3De4Fg5", FromNodeID: "img_5Kd9xQ2mNp7", ToNodeID: "txt_3Rt7yU1iOp5"},
	}
	assertCode(t, ValidateGraph(nodes, cycle), CodeCycleDetected)

	missing := []Edge{{ID: "edg_3Cd4Ef5Gh6", FromNodeID: "txt_3Rt7yU1iOp5", ToNodeID: "vid_2Mn8bV4cXz6"}}
	assertCode(t, ValidateGraph(nodes, missing), CodeInvalidPayload)

	duplicate := []Edge{
		{ID: "edg_1Ab2Cd3Ef4", FromNodeID: "txt_3Rt7yU1iOp5", ToNodeID: "img_5Kd9xQ2mNp7"},
		{ID: "edg_1Ab2Cd3Ef4", FromNodeID: "img_5Kd9xQ2mNp7", ToNodeID: "aud_9Pl4kH6jRt2"},
	}
	assertCode(t, ValidateGraph(nodes, duplicate), CodeDuplicateTarget)
}

func TestClassifyMetadataField(t *testing.T) {
	cases := map[string]FieldClass{
		"prompt":           ClassPublic,
		"apiKey":           ClassPublic, // 凭据键由 IsForbiddenBroadcastKey 单独拒绝
		"composerContent":  ClassPrivate,
		"imageTaskId":      ClassPrivate,
		"videoTaskVideoId": ClassPrivate,
		"status":           ClassLocal,
		"progress":         ClassLocal,
		"startedAt":        ClassLocal,
		"unknownField":     ClassPublic,
	}
	for field, want := range cases {
		if got := ClassifyMetadataField(field); got != want {
			t.Errorf("ClassifyMetadataField(%q) = %s, 期望 %s", field, got, want)
		}
	}
}

func TestForbiddenBroadcastKey_Normalization(t *testing.T) {
	for _, key := range []string{"apiKey", "api_key", "API-KEY", "authorization", "Authorization", "master_key", "sessionId", "jwt"} {
		if !IsForbiddenBroadcastKey(key) {
			t.Errorf("%q 应被识别为凭据保留键", key)
		}
	}
	for _, key := range []string{"prompt", "model", "content", "storageKey"} {
		if IsForbiddenBroadcastKey(key) {
			t.Errorf("%q 不应被识别为凭据保留键", key)
		}
	}
}

func TestH3ReferenceSemantics(t *testing.T) {
	if _, err := H3ReferenceField(0); err != nil {
		t.Fatalf("下标 0 应合法：%v", err)
	}
	field, _ := H3ReferenceField(0)
	if field != "ref_image_0" {
		t.Fatalf("下标 0 字段应为 ref_image_0，实际 %s", field)
	}
	last, err := H3ReferenceField(8)
	if err != nil || last != "ref_image_8" {
		t.Fatalf("下标 8 字段应为 ref_image_8，实际 %s err=%v", last, err)
	}
	if alias, ok := H3ReferenceAlias(0); !ok || alias != "ref_image" {
		t.Fatalf("下标 0 应有别名 ref_image，实际 %s ok=%v", alias, ok)
	}
	if _, ok := H3ReferenceAlias(1); ok {
		t.Fatal("下标 1 不应有别名")
	}

	for count := H3MinReferenceImages; count <= H3MaxReferenceImages; count++ {
		order := make([]string, count)
		for i := range order {
			order[i] = "img_ref_" + string(rune('a'+i))
		}
		fields, err := H3ReferenceMapping(order)
		if err != nil {
			t.Fatalf("数量 %d 应合法：%v", count, err)
		}
		if len(fields) != count || fields[0] != "ref_image_0" || fields[count-1] != "ref_image_"+string(rune('0'+count-1)) {
			t.Fatalf("数量 %d 的顺序映射错误：%v", count, fields)
		}
	}

	if err := ValidateH3ReferenceCount(0); err == nil {
		t.Fatal("0 张参考图应被拒绝")
	}
	if err := ValidateH3ReferenceCount(10); err == nil {
		t.Fatal("10 张参考图应被拒绝")
	}
	if _, err := H3ReferenceField(9); err == nil {
		t.Fatal("下标 9 越界应被拒绝")
	}
}

func TestErrorStatusMapping(t *testing.T) {
	cases := map[string]int{
		CodeInvalidPayload:       400,
		CodeForbiddenField:       400,
		CodeProtocolMismatch:     400,
		CodeUnauthorized:         401,
		CodeForbidden:            403,
		CodePrivateNodeForbidden: 403,
		CodeNotFound:             404,
		CodeVersionConflict:      409,
		CodeNodeExists:           409,
		CodeNodeDeleted:          409,
		CodeEdgeChanged:          409,
		CodeOperationIDReused:    409,
		CodePayloadTooLarge:      413,
		CodeTooManyConnections:   429,
	}
	for code, want := range cases {
		if got := StatusFor(code); got != want {
			t.Errorf("StatusFor(%s) = %d, 期望 %d", code, got, want)
		}
	}
	if err := ValidateNode(nil); err == nil {
		t.Fatal("nil 节点应报错")
	}
}

// assertCode 断言 err 是 *Error 且 Code 匹配。
func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，实际无错误", want)
	}
	var contractErr *Error
	if !errors.As(err, &contractErr) {
		t.Fatalf("期望 *contract.Error，实际 %T", err)
	}
	if contractErr.Code != want {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", want, contractErr.Code, contractErr.Message)
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}
