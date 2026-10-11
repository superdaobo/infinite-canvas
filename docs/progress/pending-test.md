---
title: 待测试
description: 当前版本已实现但仍需人工验证的变更项
---

# 待测试

- 共享房间画布只读入口（C02-01）：登录后访问 `/canvas/rooms/:roomId` 可通过 `GET /api/v1/collab/rooms/:id/graph` 加载房间公共图并只读渲染；该路由与单人项目 `/canvas/:id` 使用独立状态边界，不会写入个人画布本地存储或个人项目接口。可用 `NEXT_PUBLIC_ENABLE_CANVAS_COLLAB=false` 关闭共享入口回退单人体验。
