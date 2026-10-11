# 协作 v1 基线清点（C00-01 / Issue #2）

> 阶段：W0 · 责任 Agent：E · 对应 Epic：#1
> 本文只做现状清点，不包含任何凭据、Cookie、JWT、令牌、Chrome profile、数据库原件或学校敏感内容。

## 1. 基线 SHA、Git 状态、运行入口与依赖版本

### 1.1 基线 SHA 与分支

| 项 | 值 |
| --- | --- |
| 集成基线（`collab/v1`） | `0a5e3b0b619eb11f7806e1ee28e658e4679a2bc5` |
| 上游 `origin/main` HEAD | `0a5e3b0`（与规划起点一致） |
| 本任务 worktree | `D:/Documents/C_learn/tigerowo/wt/C00-01` |
| 本任务分支 | `feat/c00-01-baseline`（基于 `collab/v1`） |
| 版本文件 `VERSION` | `v0.8.1` |
| Go module | `github.com/tigerowo/infinite-canvas` |

真实输出：

```text
$ git rev-parse HEAD
0a5e3b0b619eb11f7806e1ee28e658e4679a2bc5

$ git rev-parse --abbrev-ref HEAD
feat/c00-01-baseline

$ git status --short
(空输出，工作区干净)

$ git log --oneline -3
0a5e3b0 feat(canvas): support stopping generation and task cleanup
c51c49d feat(channel): support custom parameter transformation
6571143 feat(channel): support model classification and update Codex Agent
```

### 1.2 运行入口

| 组件 | 入口 | 启动方式 |
| --- | --- | --- |
| 后端 Go API | `main.go` → `config.Load()` → `service.EnsureDefaultAdmin()` / `EnsureDefaultAgentSkills()` → 调度器 → `router.New().Run(":"+PORT)` | `go run .`，默认端口 `8080`，健康检查 `GET /api/health` 返回 `ok` |
| 路由 | `router/router.go`（`gin`），`/api` 公共路由 + `/api/v1`（`middleware.UserAuth`）业务路由 | 同上 |
| 前端 Next.js | `web/`，`package.json` scripts | 开发 `cd web && bun install && bun run dev`（`next dev --webpack -H 0.0.0.0 -p 3000`）；生产 `bun run build`（先 `build:bridge` 再 `next build`）+ `next start` |
| 容器 | `Dockerfile`、`docker-compose.yml` | `docker compose up -d --build`（本机未安装 Docker，未验证） |
| 本地 Agent / Comfy Bridge | `canvas-agent/`（Node）与 `canvas-agent/native/comfy-bridge/`（Go） | Agent 默认 `http://127.0.0.1:3210`；Bridge 结果服务默认 `127.0.0.1:8189` |

数据库由 `repository/db.go` 单例按 `STORAGE_DRIVER` 选择 `sqlite`（默认）/`mysql`/`postgres`，默认 DSN `data/infinite-canvas.db`（`data/` 已被 `.gitignore` 忽略）。

### 1.3 关键依赖版本

后端（`go.mod`，`go 1.25.0`）：

- `github.com/gin-gonic/gin v1.11.0`
- `gorm.io/gorm v1.31.1`、`gorm.io/driver/mysql v1.6.0`、`gorm.io/driver/postgres v1.6.0`、`github.com/glebarez/sqlite v1.11.0`
- `github.com/golang-jwt/jwt/v5 v5.3.1`、`golang.org/x/crypto v0.48.0`
- `github.com/robfig/cron/v3 v3.0.1`、`github.com/studio-b12/gowebdav v0.13.0`、`github.com/joho/godotenv v1.5.1`、`github.com/caarlos0/env/v11 v11.3.1`
- 备注：`go.mod` 当前**没有 WebSocket 库**（协作实时层需在后续 Issue 引入）。

前端（`web/package.json`）：

- `next 16.2.9`、`react 19.2.5`、`react-dom 19.2.5`、`typescript ^5`
- `antd ^6.4.2`、`@ant-design/pro-components 3.1.12-0`、`@ant-design/icons ^6.1.1`
- `zustand ^5.0.12`、`@tanstack/react-query ^5.100.9`、`tailwindcss ^4`
- `localforage ^1.10.0`（浏览器本地业务持久化）、`axios ^1.16.0`、`nanoid ^5.1.11`

### 1.4 本机工具链真实版本

```text
$ go version
go version go1.25.5 windows/amd64

$ node -v
v24.12.0

$ npm -v
11.6.2

$ git --version
git version 2.51.0.windows.1
```

- `bun`：**未安装**（前端 `dev`/`build` 脚本依赖 bun，本机需先安装 bun 才能跑前端构建）。
- `docker`：**未安装**（无法本地验证容器化与 Postgres 契约）。

## 2. 与交接文档 `c51c49d` 的差异

`c51c49d`（`feat(channel): support custom parameter transformation`）是当前基线 `0a5e3b0` 的**直接父提交**：

```text
$ git rev-parse 0a5e3b0^
c51c49d2110e84c6dabefba6480df8561bc3eed5

$ git log --oneline c51c49d..0a5e3b0
0a5e3b0 feat(canvas): support stopping generation and task cleanup
```

即 `0a5e3b0 = c51c49d + 1 个提交`（"支持停止生成与任务清理"）。差异规模：

```text
$ git diff --shortstat c51c49d 0a5e3b0
 47 files changed, 1012 insertions(+), 477 deletions(-)
```

主要变化（`git diff --name-status c51c49d 0a5e3b0`）：

- 新增：`repository/canvas_task_stop.go`、`service/canvas_task_stop.go`（停止/清理能力）。
- 后端改动：`handler/canvas_task.go`、`handler/video_task.go`、`handler/ai.go`、`handler/model_protocol.go`、`handler/model_parameter_translation.go`、`repository/{canvas_image_task,canvas_audio_task,video_task,workflow_task,comfy_bridge}.go`、`service/{video_task,workflow_task,model_protocol,model_parameter_translation,canvas_image_task,canvas_audio_task}.go`、`router/router.go`（1 行）。
- 前端改动：`web/src/app/(user)/canvas/[id]/canvas-client-page.tsx`（约 420 行）、`web/src/app/(user)/video/page.tsx`（约 217 行）、`web/src/app/(user)/canvas/types.ts`（4 行）、`web/src/services/api/{video,image,direct-ai,canvas-tasks,audio,channel-parameter-translation}.ts`、`web/src/lib/{model-channel,video-model-capabilities}.ts` 等。
- 文档/杂项：`CHANGELOG.md`、`README.md`、`VERSION`、`docs/backend/system-settings.md`、`docs/overview/features.md`；删除 `assets/88.webp`。

补充说明：交接文档（《画布部署现状与云端协作迁移说明》，见 §3.3）记录的是**另一台学校定制机**的状态（分支 `codex/school-h3-shared`、HEAD `edd4452` 且带未提交修改）。该机器与未提交修改**不在本工作区**，不能与本次基线混用；本节对比仅针对本仓库的 `0a5e3b0` 与 `c51c49d`。

## 3. 脱敏清点：学校工作流 / Bridge / 旧项目与媒体

> 以下只记录**存在性与数量**，不记录工作流内容、字段映射、令牌、Cookie、域名或数据库内容。

### 3.1 学校 H3 / 千问工作流

- 在本仓库（`0a5e3b0`）中**不存在**学校专用 H3/千问工作流源码或配置：全仓库检索无 `school` / `学校` 命中。
- 仓库内 `service/skills/h3-*.md` 共 **7 项**，是**提示词模板**（H3 视频生成类 Skill），**不是**学校 ComfyUI 工作流，不能等同。
- 学校定制机上存在（据交接文档，仅存在性）：实际载入 **7 个工作流**，其中**学校渠道 6 个、本地渠道 1 个**；默认生图已切到学校千问，默认视频为学校 H3 多图参考；H3 支持 **1–9 张参考图**、4–15 秒时长。
- 本机核对：学校定制机路径 `D:\Codex\work\school-h3-infinite-canvas` **在本机不存在**（`ls` 返回 No such file or directory），故工作流文件不可直接读取。

### 3.2 Comfy Bridge 配置

- Bridge **能力在本仓库存在**（通用实现，非学校专用）：`model/comfy_bridge.go`、`repository/comfy_bridge.go`、`service/comfy_bridge.go`、`handler/comfy_bridge.go`、`router/router.go` 中 `/api/bridge/comfy/*` 路由，以及本机桥程序 `canvas-agent/native/comfy-bridge/`（Go，`main.go`/`media.go`/`output_server.go`/`recovery.go`/`workflow.go`）。
- 学校/本地**两套 Bridge 的具体配置**（设备令牌、字段映射、私有恢复目录）只存在于学校定制机（据交接文档），**不在本工作区**；本仓库无任何 Bridge 令牌或学校域名。
- 本仓库默认 Bridge 结果服务端口 `127.0.0.1:8189`，本机 ComfyUI 默认 `127.0.0.1:8188`。

### 3.3 旧项目与不可直接访问的媒体（数量统计）

据交接文档《画布部署现状与云端协作迁移说明》§8（结构摘要，未导出内容）：

- 旧版 SQLite 共 **2 个画布项目**。
- 其中项目 `qwen-h3-campus-workflow` 有 **77 个节点**。
- 该项目的 **54 个节点**其媒体内容地址包含 `127.0.0.1`（即 `http://127.0.0.1:8189/outputs/...` 一类本机地址），**不能只复制 URL 迁移**，需读取真实媒体后重新上传；另有 `blob:http://127.0.0.1:13000/...` 类浏览器持久化素材，blob 地址本身不可迁移。
- 上述数据库**原件不在本工作区、未提交、未导出**，仅记录数量与存在性。

### 3.4 相关外部产物存在性（本机，仅存在性）

| 产物 | 路径 | 存在性 | 说明 |
| --- | --- | --- | --- |
| 学校 PoC Docker 部署包 | `D:/Download/学校画布多人生成_Docker部署包.zip` | 有（38,192 字节，21 个文件） | 学校共享画布 PoC（Node + node:sqlite），仅作队列/设备执行器语义参考，未纳入本仓库 |
| 交接文档 | `D:/Download/画布部署现状与云端协作迁移说明.md` | 有 | 本 §3 数据来源，只读引用 |
| 协作参考仓库 | `D:/Documents/C_learn/tigerowo/infinite-canvas-reference` | 有（分支 `codex/secure-collaboration`，HEAD `7538573`） | `huanxueshengmou/infinite-canvas-server`，协议语义参考；前端为 Vite/React，不可直接搬用 |
| 学校定制机安装目录 | `D:/Codex/work/school-h3-infinite-canvas` | **无** | 不在本机，学校工作流/数据库不可直接访问 |

以上外部产物的内容均**未**复制进本仓库或 PR。

## 4. 必须保持的回归用例清单

协作 v1 任何改动后，以下既有能力必须不回归（对应 `docs/overview/features.md`）：

1. **单人模式**
   - 未登录时画布项目与“我的素材”保存在浏览器本地（`localforage`/IndexedDB）；登录且账号同步可用时按时间戳合并到账号/云端。
   - 画布项目创建/重命名/删除/批量删除、导出为 JSON、从 JSON 导入；画布拖拽缩放、小地图、三种背景、框选/多选/全选、复制粘贴、撤销重做、连线高亮、快捷键。
2. **Agent**
   - 围绕选中/上游节点分析、规划并生成图片或视频，结果插回画布；Skill 选择；Codex / 本地 Agent（`canvas-agent`，默认 `127.0.0.1:3210`）连接与鉴权。
3. **导演台**
   - 导演台节点独立 3D 场景数据、关闭可恢复、随画布同步；角色/模型/机位、时间轴关键帧、截图发送为自动连线的图片节点；删除导演台节点只移除连线、已发送图片节点保留。
4. **工作流**
   - 公开/个人工作流模板、变量表单、AI 创建工作流、单图/多图系列工作流、参考图输入、结果进入生图历史；RunningHub/ComfyUI 渠道（含 `service/runninghub_*.go`、Comfy Bridge 任务领取/回传/租约）。
5. **存储**
   - 后端持久化任务（图片/视频/音频）刷新/切页/重开后恢复；后端全局轮询视频任务；停止单条任务并可刷新恢复；云文件引用检查与清理（保留被其他画布/素材引用的文件）；SQLite/MySQL/Postgres 三驱动契约。
6. **深浅主题**
   - 画布 `canvasThemes` / `useThemeStore` / Ant Design `ConfigProvider` token 下浅色与深色一致，无硬编码黑白导致主题不一致；摄像机面板、导演台、浮层随主题切换。

## 5. 合规确认

- 本任务只新增 `docs/collab-v1/baseline.md`，**未**触碰热点文件：`router/router.go`、`repository/db.go`、`web/src/app/(user)/canvas/types.ts`、`canvas-client-page.tsx`、`use-canvas-store.ts`。
- **未**提交：本地未提交修改、Cookie、JWT、令牌（含 Bridge 设备令牌）、Chrome profile、数据库原件、学校敏感信息。
- 学校工作流 / Bridge / 旧项目与媒体仅记录**存在性与数量**，未记录内容。
- 未合并 `main`、未部署生产、未改 `AGENTS.md`。

## 6. 未解决风险

- 本机无 `bun`：前端 `dev`/`build` 脚本无法直接执行，前端构建证据缺失。
- 本机无 `docker`：容器化与 Postgres 契约无法本地验证（影响后续 #27 NAS staging）。
- 学校定制机不在本机：学校工作流、字段映射、Bridge 令牌、旧数据库均不可直接访问，迁移须另行获取合法源文件与脱敏契约。
- `go.mod` 尚无 WebSocket 库；实时协作层（#10 等）依赖后续契约 Issue 冻结协议后引入。
