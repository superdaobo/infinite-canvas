"use client";

import { useCallback, useEffect, useState } from "react";
import { App, Button, Divider, Empty, Modal, Popconfirm, Select, Space, Spin, Tag, Tooltip } from "antd";
import { Clock, Copy, Link2, ShieldCheck, Trash2, UserPlus, Users } from "lucide-react";
import dayjs from "dayjs";

import { canvasThemes } from "@/lib/canvas-theme";
import { useThemeStore } from "@/stores/use-theme-store";
import type { CollabRoom, CollabRoomRole } from "@/services/api/collab-rooms";
import {
    buildRoomInviteLink,
    createRoomInvite,
    fetchRoomInvites,
    fetchRoomMembers,
    inviteStatus,
    removeRoomMember,
    revokeRoomInvite,
    updateRoomMemberRole,
    type CollabRoomInvite,
    type CollabRoomMember,
} from "@/services/api/collab-sharing";
import { useCopyText } from "@/hooks/use-copy-text";

// 共享房间的成员与分享管理面板。任何成员都能查看成员列表；只有 owner 能看到并执行
// 改角色 / 移除成员 / 创建与撤销邀请（对应 security-boundaries.md 的授权矩阵）。
// 角色展示与操作入口均以服务端返回的 room.role 为准，前端隐藏只是体验，鉴权仍由后端执行。

const ROLE_LABEL: Record<CollabRoomRole, string> = { owner: "房主", editor: "可编辑", viewer: "只读" };
const ROLE_TAG_COLOR: Record<CollabRoomRole, string> = { owner: "gold", editor: "blue", viewer: "default" };
const INVITE_TTL_OPTIONS = [
    { label: "1 天", value: 24 },
    { label: "3 天", value: 72 },
    { label: "7 天", value: 168 },
    { label: "30 天", value: 720 },
];
const INVITE_STATUS_LABEL = { active: "有效", revoked: "已撤销", expired: "已过期" } as const;

export function CollabSharingPanel({ room, open, onClose }: { room: CollabRoom; open: boolean; onClose: () => void }) {
    const { message } = App.useApp();
    const copyText = useCopyText();
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const isOwner = room.role === "owner";

    const [members, setMembers] = useState<CollabRoomMember[]>([]);
    const [membersLoading, setMembersLoading] = useState(false);
    const [invites, setInvites] = useState<CollabRoomInvite[]>([]);
    const [invitesLoading, setInvitesLoading] = useState(false);
    const [busy, setBusy] = useState("");
    const [inviteRole, setInviteRole] = useState<"editor" | "viewer">("editor");
    const [inviteTtl, setInviteTtl] = useState(168);
    // 新建邀请的明文令牌只保留在本次会话内，用于展示和复制链接；关闭面板即丢弃。
    const [createdInvite, setCreatedInvite] = useState<{ id: string; link: string; role: "editor" | "viewer" } | null>(null);

    const loadMembers = useCallback(async () => {
        setMembersLoading(true);
        try {
            const data = await fetchRoomMembers(room.id);
            setMembers(data.items || []);
        } catch (error) {
            message.error(error instanceof Error ? error.message : "加载成员失败");
        } finally {
            setMembersLoading(false);
        }
    }, [message, room.id]);

    const loadInvites = useCallback(async () => {
        setInvitesLoading(true);
        try {
            const data = await fetchRoomInvites(room.id);
            setInvites(data.items || []);
        } catch (error) {
            message.error(error instanceof Error ? error.message : "加载邀请失败");
        } finally {
            setInvitesLoading(false);
        }
    }, [message, room.id]);

    useEffect(() => {
        if (!open) return;
        void loadMembers();
        if (isOwner) void loadInvites();
    }, [isOwner, loadInvites, loadMembers, open]);

    // 关闭面板后丢弃一次性邀请令牌，避免明文令牌长期留在内存。
    useEffect(() => {
        if (!open) setCreatedInvite(null);
    }, [open]);

    async function handleCreateInvite() {
        setBusy("create");
        try {
            const invite = await createRoomInvite(room.id, inviteRole, inviteTtl);
            setCreatedInvite({ id: invite.id, link: buildRoomInviteLink(room.id, invite.token), role: invite.role });
            await loadInvites();
        } catch (error) {
            message.error(error instanceof Error ? error.message : "创建邀请失败");
        } finally {
            setBusy("");
        }
    }

    async function handleRevoke(invite: CollabRoomInvite) {
        setBusy(`revoke-${invite.id}`);
        try {
            await revokeRoomInvite(room.id, invite.id);
            message.success("邀请已撤销");
            if (createdInvite?.id === invite.id) setCreatedInvite(null);
            await loadInvites();
        } catch (error) {
            message.error(error instanceof Error ? error.message : "撤销邀请失败");
        } finally {
            setBusy("");
        }
    }

    async function handleRoleChange(member: CollabRoomMember, role: "editor" | "viewer") {
        setBusy(`role-${member.userId}`);
        try {
            await updateRoomMemberRole(room.id, member.userId, role);
            message.success(`已将 ${member.displayName || member.username} 设为${ROLE_LABEL[role]}`);
            await loadMembers();
        } catch (error) {
            message.error(error instanceof Error ? error.message : "修改角色失败");
        } finally {
            setBusy("");
        }
    }

    async function handleRemove(member: CollabRoomMember) {
        setBusy(`remove-${member.userId}`);
        try {
            await removeRoomMember(room.id, member.userId);
            message.success(`已移除 ${member.displayName || member.username}`);
            await loadMembers();
        } catch (error) {
            message.error(error instanceof Error ? error.message : "移除成员失败");
        } finally {
            setBusy("");
        }
    }

    return (
        <Modal title="成员与分享" open={open} onCancel={onClose} footer={null} centered width={560}>
            <div className="space-y-5 pt-1 text-sm">
                <section className="space-y-2">
                    <div className="flex items-center gap-1.5 font-medium opacity-80">
                        <Users className="size-4" />
                        成员（{members.length}）
                    </div>
                    {membersLoading ? (
                        <div className="grid place-items-center py-6">
                            <Spin size="small" />
                        </div>
                    ) : members.length ? (
                        <ul>
                            {members.map((member) => (
                                <li key={member.userId} className="flex min-h-11 items-center gap-2 border-b py-1.5" style={{ borderColor: theme.toolbar.border }}>
                                    <span className="min-w-0 flex-1 truncate">{member.displayName || member.username}</span>
                                    <Tag color={ROLE_TAG_COLOR[member.role]}>{ROLE_LABEL[member.role]}</Tag>
                                    {isOwner && member.role !== "owner" ? (
                                        <Space size={4}>
                                            <Select
                                                size="small"
                                                value={member.role}
                                                style={{ width: 92 }}
                                                loading={busy === `role-${member.userId}`}
                                                onChange={(value) => void handleRoleChange(member, value)}
                                                options={[
                                                    { label: "可编辑", value: "editor" },
                                                    { label: "只读", value: "viewer" },
                                                ]}
                                            />
                                            <Popconfirm title="移除该成员？" description="移除后该成员将立即失去访问权限。" okText="移除" cancelText="取消" onConfirm={() => void handleRemove(member)}>
                                                <Tooltip title="移除成员">
                                                    <Button size="small" type="text" danger icon={<Trash2 className="size-4" />} loading={busy === `remove-${member.userId}`} />
                                                </Tooltip>
                                            </Popconfirm>
                                        </Space>
                                    ) : null}
                                </li>
                            ))}
                        </ul>
                    ) : (
                        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无成员" />
                    )}
                </section>

                {isOwner ? (
                    <section className="space-y-2">
                        <Divider style={{ margin: "4px 0" }} />
                        <div className="flex items-center gap-1.5 font-medium opacity-80">
                            <UserPlus className="size-4" />
                            创建邀请
                        </div>
                        <Space wrap>
                            <Select
                                value={inviteRole}
                                style={{ width: 108 }}
                                onChange={(value) => setInviteRole(value)}
                                options={[
                                    { label: "可编辑", value: "editor" },
                                    { label: "只读", value: "viewer" },
                                ]}
                            />
                            <Select value={inviteTtl} style={{ width: 108 }} onChange={(value) => setInviteTtl(value)} options={INVITE_TTL_OPTIONS} />
                            <Button type="primary" icon={<Link2 className="size-4" />} loading={busy === "create"} onClick={() => void handleCreateInvite()}>
                                生成邀请链接
                            </Button>
                        </Space>
                        {createdInvite ? (
                            <div className="flex items-center gap-2 rounded-md border px-2 py-1.5" style={{ borderColor: theme.toolbar.border }}>
                                <span className="shrink-0 text-xs opacity-70">{ROLE_LABEL[createdInvite.role]}邀请</span>
                                <span className="min-w-0 flex-1 truncate text-xs opacity-80">{createdInvite.link}</span>
                                <Button size="small" type="text" icon={<Copy className="size-4" />} onClick={() => copyText(createdInvite.link, "邀请链接已复制")}>
                                    复制
                                </Button>
                            </div>
                        ) : (
                            <p className="text-xs opacity-60">邀请链接只在生成时显示一次，请及时复制并发送给成员。</p>
                        )}
                    </section>
                ) : null}

                {isOwner ? (
                    <section className="space-y-2">
                        <Divider style={{ margin: "4px 0" }} />
                        <div className="flex items-center gap-1.5 font-medium opacity-80">
                            <ShieldCheck className="size-4" />
                            邀请链接状态
                        </div>
                        {invitesLoading ? (
                            <div className="grid place-items-center py-4">
                                <Spin size="small" />
                            </div>
                        ) : invites.length ? (
                            <ul>
                                {invites.map((invite) => {
                                    const status = inviteStatus(invite);
                                    return (
                                        <li key={invite.id} className="flex min-h-11 items-center gap-2 border-b py-1.5" style={{ borderColor: theme.toolbar.border }}>
                                            <Tag color={invite.role === "editor" ? "blue" : "default"}>{ROLE_LABEL[invite.role]}</Tag>
                                            <span className="inline-flex min-w-0 flex-1 items-center gap-1 text-xs opacity-70">
                                                <Clock className="size-3.5 shrink-0" />
                                                {status === "active" ? `${dayjs(invite.expiresAt).format("YYYY-MM-DD HH:mm")} 到期` : INVITE_STATUS_LABEL[status]}
                                            </span>
                                            <Tag color={status === "active" ? "green" : "default"}>{INVITE_STATUS_LABEL[status]}</Tag>
                                            {status === "active" ? (
                                                <Popconfirm title="撤销该邀请？" description="撤销后使用该链接的成员将无法再加入。" okText="撤销" cancelText="取消" onConfirm={() => void handleRevoke(invite)}>
                                                    <Button size="small" type="text" danger icon={<Trash2 className="size-4" />} loading={busy === `revoke-${invite.id}`} />
                                                </Popconfirm>
                                            ) : null}
                                        </li>
                                    );
                                })}
                            </ul>
                        ) : (
                            <p className="text-xs opacity-60">暂无邀请链接。</p>
                        )}
                    </section>
                ) : (
                    <>
                        <Divider style={{ margin: "4px 0" }} />
                        <p className="text-xs opacity-60">只有房主可以管理成员和分享链接。</p>
                    </>
                )}
            </div>
        </Modal>
    );
}
