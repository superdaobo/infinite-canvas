"use client";

import { useState } from "react";
import { AlertTriangle, Ban, CheckCircle2, Clock, Image as ImageIcon, Loader2, RefreshCw, Send, Video, X } from "lucide-react";

import { canvasThemes } from "@/lib/canvas-theme";
import { useThemeStore } from "@/stores/use-theme-store";
import type { SchoolChannel, SchoolTaskPublicView, SchoolTaskStatus } from "@/services/api/collab-school";
import { useCollabSchoolQueue } from "./use-collab-school-queue";

// 共享房间的学校任务队列浮层（C03-06 / #23）：展示全房间共享的 FIFO 队列、当前唯一 running 任务、
// 个人取消（仅本人 queued）与执行器异常提示。提交入口仅 owner/editor 可见（viewer 无生成权限）。

const STATUS_LABEL: Record<SchoolTaskStatus, string> = {
    queued: "排队中",
    running: "执行中",
    succeeded: "已完成",
    failed: "失败",
    cancelled: "已取消",
};
const STATUS_COLOR: Record<SchoolTaskStatus, string> = {
    queued: "#78716c",
    running: "#3b82f6",
    succeeded: "#22c55e",
    failed: "#ef4444",
    cancelled: "#a8a29e",
};
const CHANNEL_LABEL: Record<SchoolChannel, string> = { qwen: "图片", h3: "视频" };

type SubmitForm = {
    channel: SchoolChannel;
    prompt: string;
    videoSeconds: number;
    referenceText: string;
};

const EMPTY_FORM: SubmitForm = { channel: "qwen", prompt: "", videoSeconds: 5, referenceText: "" };

// 客户端预校验：与后端 model.ValidateSchoolChannelParams 保持一致，减少无效提交。
function validateForm(form: SubmitForm): string | null {
    if (!form.prompt.trim()) return "请填写生成提示词";
    if (form.channel === "h3") {
        const refs = form.referenceText.split("\n").map((line) => line.trim()).filter(Boolean);
        if (refs.length < 1 || refs.length > 9) return "H3 视频需要 1–9 张有序参考图（每行一个 URL）";
        if (!Number.isInteger(form.videoSeconds) || form.videoSeconds < 4 || form.videoSeconds > 15) return "H3 视频秒数必须是 4–15 的整数秒";
    }
    return null;
}

function StatusTag({ status }: { status: SchoolTaskStatus }) {
    const color = STATUS_COLOR[status];
    return (
        <span className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px]" style={{ color, background: `${color}1a` }}>
            {status === "running" ? <Loader2 className="size-3 animate-spin" /> : status === "succeeded" ? <CheckCircle2 className="size-3" /> : <Clock className="size-3" />}
            {STATUS_LABEL[status]}
        </span>
    );
}

// 单条任务行：排队位置、渠道、提交人、状态；仅本人 queued 任务可取消。
function TaskRow({
    task,
    position,
    name,
    cancellable,
    busy,
    onCancel,
    highlight,
}: {
    task: SchoolTaskPublicView;
    position?: number;
    name: string;
    cancellable: boolean;
    busy: boolean;
    onCancel: () => void;
    highlight?: boolean;
}) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const ChannelIcon = task.channel === "h3" ? Video : ImageIcon;
    return (
        <li
            className="flex items-center gap-2 rounded-md border px-2 py-1.5"
            style={{ borderColor: highlight ? "#3b82f6" : theme.toolbar.border, background: highlight ? "rgba(59,130,246,.08)" : "transparent" }}
        >
            <span className="w-6 shrink-0 text-center text-[11px] opacity-60">{position ? `#${position}` : "—"}</span>
            <ChannelIcon className="size-3.5 shrink-0 opacity-70" />
            <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5">
                    <span className="truncate text-xs font-medium">{name}</span>
                    <span className="shrink-0 text-[11px] opacity-50">{CHANNEL_LABEL[task.channel]}</span>
                </div>
                {task.error ? <div className="truncate text-[11px]" style={{ color: "#ef4444" }} title={task.error}>{task.error}</div> : null}
            </div>
            <StatusTag status={task.status} />
            {cancellable ? (
                <button
                    type="button"
                    disabled={busy}
                    onClick={onCancel}
                    title="取消该任务"
                    className="flex shrink-0 items-center gap-0.5 rounded border px-1.5 py-0.5 text-[11px] transition hover:opacity-80 disabled:opacity-40"
                    style={{ borderColor: theme.toolbar.border }}
                >
                    {busy ? <Loader2 className="size-3 animate-spin" /> : <Ban className="size-3" />}
                    取消
                </button>
            ) : task.status === "queued" ? (
                <span className="shrink-0 text-[11px] opacity-40" title="只能取消本人提交的未开始任务">仅本人可取消</span>
            ) : task.status === "running" ? (
                <span className="shrink-0 text-[11px] opacity-40" title="已提交学校的任务不会直接取消">执行中不可取消</span>
            ) : null}
        </li>
    );
}

export function CollabSchoolQueuePanel({ roomId, open, onClose, canSubmit }: { roomId: string; open: boolean; onClose: () => void; canSubmit: boolean }) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const { queue, loading, error, submitting, cancelBusyId, actionError, clearActionError, executorHint, memberName, userId, refresh, submit, cancel } = useCollabSchoolQueue(roomId, open);
    const [form, setForm] = useState<SubmitForm>(EMPTY_FORM);
    const [formError, setFormError] = useState<string | null>(null);
    const [submittedNotice, setSubmittedNotice] = useState<string | null>(null);

    if (!open) return null;

    const items = queue?.items ?? [];
    const running = queue?.running ?? null;
    // 排队位置按 FIFO 可见顺序计算（running 不占排队位）。
    const queuedPosition = new Map<string, number>();
    let position = 0;
    for (const task of items) {
        if (task.status === "queued") {
            position += 1;
            queuedPosition.set(task.id, position);
        }
    }
    const others = items.filter((task) => task.id !== running?.id);

    async function handleSubmit() {
        const invalid = validateForm(form);
        if (invalid) {
            setFormError(invalid);
            return;
        }
        setFormError(null);
        setSubmittedNotice(null);
        try {
            const refs = form.referenceText.split("\n").map((line) => line.trim()).filter(Boolean);
            const result = await submit(form.channel, {
                prompt: form.prompt.trim(),
                ...(form.channel === "h3" ? { referenceImages: refs, videoSeconds: form.videoSeconds } : {}),
            });
            setForm(EMPTY_FORM);
            const queuedAhead = (queue?.items ?? []).filter((task) => task.status === "queued").length;
            setSubmittedNotice(result.idempotent ? "该任务此前已提交，未重复执行。" : `已加入队列，前方还有 ${queuedAhead} 个排队任务。`);
        } catch {
            // 错误已由 hook 写入 actionError，这里只保证提示可见。
        }
    }

    const inputStyle = { background: theme.canvas.background, borderColor: theme.toolbar.border, color: theme.node.text } as const;

    return (
        <div
            className="absolute right-4 top-4 z-[86] flex max-h-[calc(100vh-2rem)] w-[340px] flex-col rounded-lg border text-sm backdrop-blur"
            style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
        >
            <div className="flex items-center gap-2 border-b px-3 py-2" style={{ borderColor: theme.toolbar.border }}>
                <span className="font-medium">学校队列</span>
                <span className="rounded px-1.5 py-0.5 text-[11px] opacity-70" style={{ background: theme.canvas.background }}>
                    共 {queue?.total ?? 0}
                </span>
                <div className="flex-1" />
                <button type="button" onClick={() => void refresh()} className="rounded p-1 transition hover:opacity-70" title="刷新队列">
                    <RefreshCw className={`size-3.5 ${loading ? "animate-spin" : ""}`} />
                </button>
                <button type="button" onClick={onClose} className="rounded p-1 transition hover:opacity-70" title="关闭">
                    <X className="size-3.5" />
                </button>
            </div>

            <div className="min-h-0 flex-1 space-y-3 overflow-y-auto px-3 py-2">
                {executorHint ? (
                    <div
                        className="flex items-start gap-2 rounded-md border px-2 py-1.5 text-xs"
                        style={{ borderColor: executorHint.level === "error" ? "#ef4444" : executorHint.level === "stalled" ? "#f59e0b" : theme.toolbar.border }}
                    >
                        <AlertTriangle className="mt-0.5 size-3.5 shrink-0" style={{ color: executorHint.level === "error" ? "#ef4444" : executorHint.level === "stalled" ? "#f59e0b" : theme.node.muted }} />
                        <div className="space-y-0.5">
                            <div className="font-medium">{executorHint.title}</div>
                            <div className="opacity-70">{executorHint.detail}</div>
                        </div>
                    </div>
                ) : null}

                {actionError ? (
                    <div className="flex items-start gap-2 rounded-md border px-2 py-1.5 text-xs" style={{ borderColor: "#ef4444" }}>
                        <AlertTriangle className="mt-0.5 size-3.5 shrink-0" style={{ color: "#ef4444" }} />
                        <span className="flex-1" style={{ color: "#ef4444" }}>{actionError}</span>
                        <button type="button" onClick={clearActionError} className="opacity-70 transition hover:opacity-100" title="关闭">
                            <X className="size-3" />
                        </button>
                    </div>
                ) : null}

                {submittedNotice ? (
                    <div className="rounded-md border px-2 py-1.5 text-xs" style={{ borderColor: theme.toolbar.border }}>
                        <span className="opacity-80">{submittedNotice}</span>
                    </div>
                ) : null}

                {running ? (
                    <section className="space-y-1.5">
                        <div className="text-[11px] font-medium opacity-60">当前执行</div>
                        <ul>
                            <TaskRow task={running} name={memberName(running.submittedBy)} cancellable={false} busy={false} onCancel={() => undefined} highlight />
                        </ul>
                    </section>
                ) : null}

                <section className="space-y-1.5">
                    <div className="text-[11px] font-medium opacity-60">队列（按提交顺序）</div>
                    {error ? (
                        <div className="rounded-md border px-2 py-2 text-xs" style={{ borderColor: "#ef4444", color: "#ef4444" }}>{error}</div>
                    ) : !items.length ? (
                        <div className="py-4 text-center text-xs opacity-50">暂无学校任务</div>
                    ) : (
                        <ul className="space-y-1">
                            {others.map((task) => (
                                <TaskRow
                                    key={task.id}
                                    task={task}
                                    position={queuedPosition.get(task.id)}
                                    name={memberName(task.submittedBy)}
                                    cancellable={task.submittedBy === userId && task.status === "queued"}
                                    busy={cancelBusyId === task.id}
                                    onCancel={() => void cancel(task.id).catch(() => undefined)}
                                />
                            ))}
                        </ul>
                    )}
                </section>

                {canSubmit ? (
                    <section className="space-y-2 border-t pt-2" style={{ borderColor: theme.toolbar.border }}>
                        <div className="text-[11px] font-medium opacity-60">提交学校任务</div>
                        <div className="flex gap-1">
                            {(["qwen", "h3"] as SchoolChannel[]).map((channel) => (
                                <button
                                    key={channel}
                                    type="button"
                                    onClick={() => setForm((current) => ({ ...current, channel }))}
                                    className="flex flex-1 items-center justify-center gap-1 rounded-md border px-2 py-1 text-xs transition hover:opacity-80"
                                    style={{
                                        borderColor: form.channel === channel ? "#3b82f6" : theme.toolbar.border,
                                        background: form.channel === channel ? "rgba(59,130,246,.08)" : "transparent",
                                    }}
                                >
                                    {channel === "h3" ? <Video className="size-3.5" /> : <ImageIcon className="size-3.5" />}
                                    {CHANNEL_LABEL[channel]}
                                </button>
                            ))}
                        </div>
                        <textarea
                            value={form.prompt}
                            onChange={(event) => setForm((current) => ({ ...current, prompt: event.target.value }))}
                            rows={2}
                            placeholder="生成提示词"
                            className="w-full resize-none rounded-md border px-2 py-1 text-xs outline-none"
                            style={inputStyle}
                        />
                        {form.channel === "h3" ? (
                            <>
                                <div className="flex items-center gap-2 text-xs">
                                    <span className="opacity-70">视频秒数</span>
                                    <input
                                        type="number"
                                        min={4}
                                        max={15}
                                        value={form.videoSeconds}
                                        onChange={(event) => setForm((current) => ({ ...current, videoSeconds: Number(event.target.value) }))}
                                        className="w-16 rounded-md border px-2 py-1 text-xs outline-none"
                                        style={inputStyle}
                                    />
                                    <span className="opacity-50">4–15 秒</span>
                                </div>
                                <textarea
                                    value={form.referenceText}
                                    onChange={(event) => setForm((current) => ({ ...current, referenceText: event.target.value }))}
                                    rows={2}
                                    placeholder="参考图 URL，每行一个（1–9 张，顺序即语义）"
                                    className="w-full resize-none rounded-md border px-2 py-1 text-xs outline-none"
                                    style={inputStyle}
                                />
                            </>
                        ) : null}
                        {formError ? <div className="text-xs" style={{ color: "#ef4444" }}>{formError}</div> : null}
                        <button
                            type="button"
                            disabled={submitting}
                            onClick={() => void handleSubmit()}
                            className="flex w-full items-center justify-center gap-1 rounded-md border px-3 py-1.5 text-xs font-medium transition hover:opacity-80 disabled:opacity-40"
                            style={{ borderColor: theme.toolbar.border }}
                        >
                            {submitting ? <Loader2 className="size-3.5 animate-spin" /> : <Send className="size-3.5" />}
                            提交到共享队列
                        </button>
                    </section>
                ) : (
                    <div className="border-t pt-2 text-xs opacity-50" style={{ borderColor: theme.toolbar.border }}>
                        只读成员（viewer）不能提交学校任务。
                    </div>
                )}
            </div>
        </div>
    );
}
