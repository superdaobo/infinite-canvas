---
title: 待测试
description: 当前版本已实现但仍需人工验证的变更项
---

# 待测试

- 共享房间画布只读入口（C02-01）：登录后访问 `/canvas/rooms/:roomId` 可通过 `GET /api/v1/collab/rooms/:id/graph` 加载房间公共图并只读渲染；该路由与单人项目 `/canvas/:id` 使用独立状态边界，不会写入个人画布本地存储或个人项目接口。可用 `NEXT_PUBLIC_ENABLE_CANVAS_COLLAB=false` 关闭共享入口回退单人体验。
- 共享房间实时接收（C02-03）：`/canvas/rooms/:roomId` 会用 `POST /api/v1/collab/rooms/:id/ws-ticket` 换取一次性 ticket，再连 `GET /api/v1/collab/rooms/:id/ws` 并发送首帧 `hello`（协议版本校验）。收到 `snapshot-*` 分帧快照、`changes` 增量、`presence` 在线成员、`cursors` 协作光标与 `error` 后更新画布；断网/切标签页后按当前 revision 自动重连并补发未确认操作，重连期间本地未确认草稿不清空。权限被撤销（4401/4403 或 `unauthorized`/`forbidden` 等错误码）后页面显示访问失效并停止接受广播。前后端分离开发时可用 `NEXT_PUBLIC_COLLAB_WS_BASE`（如 `ws://127.0.0.1:8080`）指向 Go 后端 WS 端点。
