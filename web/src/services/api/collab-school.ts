import { apiGet, apiPost } from "@/services/api/request";
import { useUserStore } from "@/stores/use-user-store";

// 协作 v1 学校共享生成任务队列（对应 Go handler/collab_school.go 与 model/school_task.go）。
// 队列按房间共享一个 FIFO 单执行槽：同一房间最多只有一个 running 任务，其余按提交顺序排队。
// 对外视图 SchoolTaskPublicView 不含上游任务号、学校凭据、设备令牌与时间戳（见 school-contract.md §7）。

export type SchoolChannel = "qwen" | "h3";

export type SchoolTaskStatus = "queued" | "running" | "succeeded" | "failed" | "cancelled";

// 对外公开任务视图：字段与 Go model.SchoolTaskPublicView 一一对应。
export type SchoolTaskPublicView = {
    id: string;
    roomId: string;
    submittedBy: string;
    executorId?: string;
    clientTaskId: string;
    channel: SchoolChannel;
    status: SchoolTaskStatus;
    error?: string;
};

// 房间队列视图：items 为 FIFO 可见顺序，running 为当前唯一执行中的任务（无则省略）。
export type SchoolTaskQueue = {
    items: SchoolTaskPublicView[];
    total: number;
    running?: SchoolTaskPublicView;
};

// 提交参数：千问仅文生图；H3 需要 1–9 张有序参考图与 4–15 秒整数视频。
export type SchoolTaskParams = {
    prompt: string;
    referenceImages?: string[];
    videoSeconds?: number;
    size?: string;
    quality?: string;
};

export type SchoolSubmitResult = {
    task: SchoolTaskPublicView;
    idempotent: boolean;
};

function collabToken() {
    const token = useUserStore.getState().token;
    if (!token) throw new Error("请先登录后再访问共享画布");
    return token;
}

function roomSchoolBase(roomId: string) {
    return `/api/v1/collab/rooms/${encodeURIComponent(roomId)}/school-tasks`;
}

// 读取房间学校任务队列（成员可见）。
export async function fetchRoomSchoolTasks(roomId: string) {
    return apiGet<SchoolTaskQueue>(roomSchoolBase(roomId), undefined, collabToken());
}

// 提交学校任务（owner/editor）。clientTaskId 由调用端生成，在 (roomId, 提交人) 维度唯一；
// 重复提交命中同一任务并返回 idempotent=true，不会触发第二次学校执行。
export async function submitRoomSchoolTask(roomId: string, input: { channel: SchoolChannel; clientTaskId: string; params: SchoolTaskParams }) {
    return apiPost<SchoolSubmitResult>(roomSchoolBase(roomId), input, collabToken());
}

// 取消本人提交的未开始（queued）任务；他人的或已开始的任务由服务端拒绝。
export async function cancelRoomSchoolTask(roomId: string, taskId: string) {
    return apiPost<SchoolTaskPublicView>(`${roomSchoolBase(roomId)}/${encodeURIComponent(taskId)}/cancel`, {}, collabToken());
}
