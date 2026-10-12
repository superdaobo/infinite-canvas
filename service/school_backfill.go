package service

import (
	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 学校结果回填的受约束服务端写入通道（修复 PR #47 审查 P1 队列堵塞）。
//
// 背景：任务提交人可能在任务执行期间被房主移除或降为 viewer。若结果回填仍以提交人
// 身份走普通用户写路径（ApplyRoomOperations），成员写权限检查会拒绝写入，任务便无法
// 走到 completeSchoolTaskSuccess，持续占用唯一 running 槽，堵塞整个房间队列。
//
// 这里改为：授权仍由 service 层在进入回填前完成（任务必须处于 running 且绑定到当前
// 执行设备，见 requireRunningSchoolTask / UploadSchoolTaskResult），一旦通过，就以
// **服务端内部通道**写入房间图，不再要求提交人当前仍是可写成员。节点 metadata 仍记录
// 真实 submittedBy，幂等回执也仍以提交人归属，审计信息不丢失。

// backfillSchoolResultViaServer 以服务端身份原子写入已授权的学校结果节点并广播。
//
// 安全约束：
//   - 只接受服务端构造的 create-only 信封（schoolResultEnvelope 生成），不接受用户请求体；
//   - 授权前置：只有 requireRunningSchoolTask 通过（running 且执行设备匹配）的调用才可达此处；
//   - actorID（任务提交人）仅用于幂等回执与审计归属，不据此授予房间写权限；
//   - 复用 #8 的契约信封校验、原子事务、幂等回执、revision 推进与提交后广播。
func backfillSchoolResultViaServer(roomID, submittedBy string, envelope contract.Envelope) (model.RoomBatchResult, error) {
	if err := contract.ValidateEnvelope(envelope); err != nil {
		return model.RoomBatchResult{}, err
	}
	digest, err := requestDigest(envelope)
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	result, err := repository.ApplyRoomBatchAsServer(roomID, submittedBy, envelope.OperationID, digest, envelope.Operations)
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	broadcastRoomChanges(roomID, result)
	return result, nil
}
