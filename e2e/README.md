# 无限画布协作 v1 端到端测试基线（e2e）

本目录为 issue #4（`C00-03`）交付的 Playwright 测试基建，用于在**不接触真实学校令牌 / 生产数据**的前提下，
机械验证原生画布的多人协作、并发、断线与学校任务回填。当前提供：

- 一条命令拉起本地 **Go 后端（sqlite）** + **Next.js 前端**；
- 驱动**两个相互独立的 browser context**（= 两个不同账号）；
- 当前即可跑通的基线用例，以及为后续协作 / 权限 / 学校功能预留的 `test.fixme` 骨架。

## 前置条件

| 依赖 | 版本 | 说明 |
| --- | --- | --- |
| Node.js | >= 20（本机 v24.12） | 运行 Playwright |
| npm | >= 10（本机 11.6） | 安装 e2e 与前端依赖 |
| Go | >= 1.25（本机 1.25.5） | 运行后端 `go run .` |
| Chromium | 由 Playwright 管理 | `npx playwright install chromium` |

**本机没有 Docker**，因此测试直接以本地进程方式启动后端与前端，数据库使用本地 sqlite 文件
`data/e2e.db`，不依赖 MySQL / Postgres / Redis。

## 一次性准备

```bash
# 1) 安装 e2e 依赖（按 package-lock.json 精确安装）
cd e2e
npm ci

# 2) 安装 Playwright 浏览器（首次或升级后）
npx playwright install chromium

# 3) 安装前端依赖（仓库使用 bun.lock；本机无 bun 时用 npm）
cd ../web
npm install          # 有 bun 时：bun install --frozen-lockfile
```

## 运行

```bash
cd e2e
npm test                 # 全量：自动拉起后端 8080 + 前端 3000，跑完后自动关闭
npm run test:baseline    # 只跑当前可跑通的基线用例
npm run test:headed      # 有头模式，便于观察
npm run report           # 查看 HTML 报告（playwright-report/）
```

无需手动启动任何服务：`playwright.config.ts` 的 `webServer` 会：

1. 在仓库根目录执行 `go run .`，注入 sqlite / JWT / 管理员环境变量，等待 `/api/health` 就绪；
2. 在 `web/` 执行 `npm run dev`，通过 `API_BASE_URL` 把前端 `/api/*` 代理到后端，等待 `/login` 就绪。

### 端口

| 服务 | 默认端口 | 覆盖方式 |
| --- | --- | --- |
| Go 后端 | 8080 | `E2E_BACKEND_PORT` |
| Next.js 前端 | 3000 | `E2E_FRONTEND_PORT`（注意前端 dev 脚本 `-p 3000` 固定，改动需同步 `web/package.json`） |

若本机 8080 / 3000 已被占用，请先释放端口或修改上述环境变量（`reuseExistingServer` 在本地会自动复用已就绪的服务）。

## 目录结构

```
e2e/
├─ playwright.config.ts        # 一条命令拉起双服务 + chromium 工程
├─ fixtures/
│  ├─ env.ts                   # 端口 / 路径统一解析
│  ├─ accounts.ts              # 注册真实测试账号、注入独立登录会话
│  ├─ canvas.ts                # 画布库 / 画布详情页操作封装
│  ├─ node-fixtures.ts         # 8 类节点与 H3 1–9 张参考图 fixture
│  └─ school-bridge-stub.ts    # 学校 Bridge / 结果上传测试替身（内存版）
└─ tests/
   ├─ baseline.spec.ts         # ✅ 当前可跑通
   ├─ collaboration.spec.ts    # ⏭ fixme：无刷新同编 / 冲突 / 断线恢复
   ├─ authorization.spec.ts    # ⏭ fixme：viewer 只读 / 未授权 403
   └─ school-queue.spec.ts     # ✅ 替身自检 + ⏭ fixme：学校队列单执行槽
```

## 用例清单

| 用例 | 状态 | 说明 |
| --- | --- | --- |
| 两个 context 分别以不同账号打开画布库 | ✅ 通过 | 双账号、独立会话、页面正常加载 |
| 单账号新建画布 + 新增文本节点 | ✅ 通过 | 画布库 → 详情页 → 工具栏基础操作 |
| 两账号画布库相互独立 | ✅ 通过 | 本地存储隔离（跨账号不可见） |
| 学校 Bridge 替身：单执行槽 / 1–9 张限制 | ✅ 通过 | 测试替身自检 |
| 无刷新实时同编 | ⏭ fixme | 待 C01-05 / C02-03 |
| 独立节点并发冲突（409） | ⏭ fixme | 待 C01-03 / C02-04 |
| WS 断线重连恢复 | ⏭ fixme | 待 C02-03 |
| viewer 只读负例 | ⏭ fixme | 待 C01-02 |
| 非成员访问 403 | ⏭ fixme | 待 C01-02 |
| 学校队列一次只运行一个任务 | ⏭ fixme | 待 C03-02 |

## 已知限制（无 Docker）

- 仅覆盖 **本地 sqlite** 单实例场景；MySQL / Postgres、多实例、真实 WS 集群未覆盖。
- 不接触真实学校 Cookie / Chrome Profile / Comfy Bridge / 生产 NAS，学校相关仅为**测试替身**，不代表真实执行。
- 前端以 `next dev` 运行（按需编译），首次访问较慢；未使用生产 `next build` 产物。
- 协作 / 权限 / 学校功能尚未在源码中实现，相关用例为 `test.fixme` 占位，去掉 `fixme` 后即可作为验收用例。
- 测试账号使用运行时唯一用户名，写入 `data/e2e.db`；如需干净库，删除该文件后重跑。

## 安全说明

测试只创建本地临时账号（随机用户名 + 固定弱口令），不写入任何凭据、Cookie、令牌或私有日志到仓库；
`data/e2e.db` 位于 `.gitignore` 覆盖的 `data/` 目录下。
