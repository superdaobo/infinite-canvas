/**
 * 学校 Bridge / 结果上传的测试替身（scaffold）。
 *
 * 目的：为 C03（学校持久化 FIFO 单执行槽）与 C04（媒体回填）用例预留一个
 * 「不接触真实学校令牌、不触发付费任务」的本地替身接口。当前仓库尚未实现
 * 学校队列与 Bridge，因此本文件只描述替身的契约与内存模型，供 test.fixme
 * 用例在功能落地后接入；不导入任何产品代码，也不会发起网络请求。
 *
 * 真实实现落地后，应把这里的接口替换为对产品队列/Bridge 的受控替身。
 */

export type SchoolTaskStatus = "queued" | "running" | "succeeded" | "failed";

export type SchoolTask = {
    id: string;
    roomId: string;
    submittedBy: string;
    /** H3 参考图数量，1–9。 */
    referenceCount: number;
    status: SchoolTaskStatus;
};

export type SchoolTaskResult = {
    taskId: string;
    mediaUrls: string[];
};

/**
 * 内存版学校执行槽替身：保证任意时刻最多一个 running 任务，并按入队顺序执行。
 * 仅用于用例构造预期，不代表产品实现。
 */
export class SchoolBridgeStub {
    private readonly queue: SchoolTask[] = [];
    private running: SchoolTask | null = null;
    private seq = 0;

    enqueue(input: Omit<SchoolTask, "id" | "status">): SchoolTask {
        if (input.referenceCount < 1 || input.referenceCount > 9) {
            throw new Error("referenceCount 必须在 1–9 之间");
        }
        const task: SchoolTask = { ...input, id: `school-task-${++this.seq}`, status: "queued" };
        this.queue.push(task);
        return task;
    }

    /** 取出下一个任务并标记为 running；已在运行或队列为空时返回 null。 */
    startNext(): SchoolTask | null {
        if (this.running || this.queue.length === 0) return null;
        const task = this.queue.shift()!;
        task.status = "running";
        this.running = task;
        return task;
    }

    complete(result: SchoolTaskResult): void {
        if (!this.running || this.running.id !== result.taskId) {
            throw new Error("完成的任务与当前执行槽不一致");
        }
        this.running.status = "succeeded";
        this.running = null;
    }

    /** 任意时刻处于 running 的任务数，供「一次只运行一个任务」断言使用。 */
    get runningCount(): number {
        return this.running ? 1 : 0;
    }

    get queuedCount(): number {
        return this.queue.length;
    }
}
