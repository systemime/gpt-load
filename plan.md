# 每凭证并发上限（cap）+ 亲和溢出 + 管理端观测 方案

> 目标：保持请求亲和（上游 prompt 缓存局部性）的同时，避免同一会话的并行子代理请求全部打到同一个 key，触发厂商单 key 并发限流。
> 形态：不做持续均衡，做准入控制 —— 平时全亲和，尖峰才溢出。
> cap 实测依据：2026-10-09 对 4 个可用 key × 2 模型（glm-5.3 / glm-5.3-flash）阶梯加压，见附录。

---

## 一、尖峰判定原则

**不预测，只测量。** 尖峰的唯一定义：某凭证的实时在途请求数（in-flight）≥ cap。

- 判定时刻：每个请求在 `scheduler.Next()` 选取凭证的瞬间读取计数；
- 判定条件：`preferred.inflight >= cap` → 本请求溢出，fair 调度选实时负载最低的 eligible 凭证；
- 同一会话的多条并行请求**逐条独立判定，先到先得**：前 cap 条保持亲和（最早请求正在建缓存），超出部分溢出；
- 不用预测 / 滑动窗口 / 延迟反馈：LLM 突发是秒级、请求时长 5-30s，窗口会漏判、反馈滞后；在途计数与被保护约束严格同构，零参数。

## 二、后端改动

### 2.1 cap 配置（全局设置，非硬编码）

| 文件 | 改动 |
|---|---|
| `internal/state/runtime_settings.go` | 新增 `SettingCredentialConcurrencyLimit = "credential_concurrency_limit"`；resolved 字段 `CredentialConcurrencyLimit int`；**默认 6**；校验 `0（不限）或 1..64`；快照透传 |
| `internal/control/settings.go` | API 字段映射 + 校验规则 |

### 2.2 每凭证信号量

- 位置：`internal/state` 凭证注册表（凭证生命周期已有管理点）；
- 原语：每凭证 `chan struct{}` 容量 = cap（标准库，零新依赖；不用 `x/time/rate` —— 厂商限的是并发不是速率）；
- 接口：`TryAcquire(credID) bool` / `Release(credID)` / `Inflight(credID) int`（读 `cap - len(ch)`）；
- cap 变更（设置保存触发 revision 前进）时**重建信号量**，语义与亲和缓存 `Configure` 一致：新请求用新 cap，在途请求自然排空；
- 信号量随凭证增删同步建毁。

### 2.3 调度与执行接入（`fair.go` 不动）

| 文件 | 改动 |
|---|---|
| `internal/scheduler/scheduler.go` 候选收集（~L276） | 在途 ≥ cap 的凭证跳过；**全池超限时豁免过滤**（退化为选在途最少者，永不比现状差） |
| `internal/gateway/handler.go` ~L1280 | 与 `acquireGroupConcurrency` 并排 `TryAcquire`；失败则标记 tried 重新 `Next()`（复用现有 attempt 循环）；`defer Release` |
| `internal/gateway/websocket_turn.go` ~L540 | 同上，websocket 轮次路径 |

溢出请求成功后由现有 `recordAffinitySuccess`（`affinity.go:83-90`）改写映射 → 会话后续跟随新 key，1-2 轮热身，自愈迁移，无需额外逻辑。

### 2.4 凭证负载数据端点（供新页面）

新增 `GET /api/credentials/load`（control 路由，管理员鉴权），返回：

```jsonc
{
  "observed_at_ms": 0,
  "credentials": [{
    "id": 0, "name": "", "status": "",
    "inflight": 0,                    // 实时在途（信号量读数）
    "windows": {                      // 1h / 5h / 24h
      "1h":  { "requests": 0, "success": 0, "affinity_hits": 0,
                "uncached_input_tokens": 0, "output_tokens": 0, "cache_read_tokens": 0 },
      "5h":  { "...": 0 }, "24h": { "...": 0 }
    },
    "errors_5h": { "rate_limited": 0, "connect": 0, "timeout": 0,
                    "upstream": 0, "canceled": 0, "scheduled": 0, "other": 0 }
  }],
  "local_rejects": { "1h": 0, "5h": 0, "24h": 0 }   // credential_id=0
}
```

- 实现：单条 SQL 聚合 `request_logs`（按 `completed_at_ms` 窗口），错误码按 watch 脚本同款分类映射（`upstream_rate_limited→rate_limited` 等）；
- 前端 10s 轮询刷新实时列，窗口列低频（60s）刷新即可（接口一次返回全部，前端拆分展示）。

## 三、前端改动（classic + modern 双前端）

### 3.1 设置项：全局配置页 → 路由/亲和区

| 前端 | 文件 | 改动 |
|---|---|---|
| classic | `features/settings/RoutingSettingsSection.vue` | `numericKeys` 增加 `credential_concurrency_limit`；输入框 max=64，说明文案 0=不限 |
| classic | `i18n/locales/{zh-CN,en-US,ja-JP}/settings.ts` | 三语文案 |
| modern | `features/settings/SettingsView.vue` | `routing` 数组与 `affinityNumbers` 增加；`SettingsNumberField` 复用 |

### 3.2 帮助图标（? tooltip）

- 组件：classic 用现有 `components/ui/AppTooltip.vue`；modern 用对应 tooltip 组件；
- 内容走 i18n（zh 为主），要点：

> **默认值 6 的依据（2026-10-09 实测，4 个团队标准版 key）**：
> - 阶梯加压（K=2/4/6/8/10/12/16，每档多轮）：**K≤6 全部通过、零 429**；K=8 约 1/3 轮次失败 1 条；K=10 几乎每轮失败 1-3 条；K=16 无一全过；
> - 单 key 硬顶 ≈ 8 并发，临界带抖动；429 响应 `code 1302 "您的账户已达到速率限制"`，疑似并发+到达速率混合限流；
> - **与模型无关**：glm-5.3 与 glm-5.3-flash 天花板一致（7-8），限流按 key 计；
> - 建议值 = 上限 8 − 2 余量 = 6；若套餐升档（社区口径 Max 档 8→16），可上调至 7-8，出 1302 则回调。

### 3.3 数据展示页：凭证负载

复用现有监控入口，最小改动：

| 前端 | 位置 | 形式 |
|---|---|---|
| classic | `features/monitor/` MonitorView | 新增 Tab「凭证负载」（与 日志/健康/检查器 并列），新组件 `CredentialLoadTab.vue` |
| modern | `features/` monitor 家族 | 新页面 `credential-load`，`app/navigation.ts` 注册菜单项 |

表格列（对齐 watch 脚本）：

```
凭证 | 状态 | 实时在途 | 60s请求 | [1h/5h/24h 窗口切换]
  → 窗口内: 请求数 份额(占比条) 成功率 亲和命中 消耗tok 缓存读
错误区(5h): 限流/连接/超时/上游/取消/调度/其他 计数 + 异常率
```

- 份额占比条复用现有进度条组件；成功率/亲和命中按 ≥95 / ≥80 / <80 三档着色（与日志页健康色一致）；
- 顶部一行汇总：总请求、本地拒绝计数。

## 四、缓存率预估

公式：缓存率 ≈ 1 − 溢出请求占比。

| 场景 | 溢出占比 | 预期缓存率 |
|---|---|---|
| 平均负载（在途 ~2，cap=6 几乎不触发） | <3% | 95-97% |
| 子代理突发常见（5-8 并行） | 10-20% | 88-93% |
| 极端（重突发 + cap 紧） | 25-35% | 80-87% |

当前基线 97-98%（已含 TTL 300 重粘成本）；429 被打掉的请求本就换 key 全冷，cap 将该损失前置且受控，净效果预计持平或更好。

## 五、实施顺序与验收

1. 后端：设置项 + 信号量 + 调度过滤 + 执行接入（含单测：cap 触发溢出、全池超限豁免、并发竞态、websocket 路径）；
2. 端点：`/api/credentials/load` + 聚合单测；
3. 前端：classic 设置项+tooltip → classic 凭证负载 Tab → modern 两项；
4. 验收：
   - `go test ./internal/...` 全绿；
   - 线上观察：watch 脚本 5h 异常表中 `限流` 归零；缓存读率降幅 ≤ 5 个点；
   - 页面数据与 watch 脚本同源同值（同一 SQL 口径）。

## 附录：实测记录（2026-10-09，172.16.4.10 出口）

- 方法：解密 4 个 active key，直连 `open.bigmodel.cn/api/coding/paas/v4/chat/completions`，`max_tokens=8` 微型探测，ThreadPoolExecutor 同时爆发 K 条，统计 HTTP 200/429；
- 多轮汇总：K=8 通过 7-8/8（约 1/3 轮掉 1）；K=10 通过 7-10/10；K=16 通过 7-14/16、无一全过；K≤6 全部通过；
- 429 体：`{"error":{"code":"1302","message":"您的账户已达到速率限制，请您控制请求频率"}}`；
- 模型对照（同 key）：glm-5.3 与 glm-5.3-flash 天花板一致 ≈8，限流按 key 不按模型；
- 套餐：团队标准版；社区档位参考（Lite 2→4 / Pro 4→8 / Max 8→16，按用量升档）无团队版明确条目，以实测为准；
- 官方核对入口：https://bigmodel.cn/claude-code 、https://open.bigmodel.cn/pricing 。

---

## 五、阶段 2：组级准入排队（有界等待）

> 需求：分组内 2 个 key、cap=6 时，理想并发 = 2×(6+2)=16（+2 为豁免浮动，固定值）。
> 到来 24 并发时，超出组预算的 8 个请求进入 FIFO 排队，等到槽位即放行，而不是立刻失败或无界透传。

### 语义

- **组预算** = Σ(组内 eligible 凭证的 cap + slack)，slack 固定 2（与阶段 1 的 ForceAcquire 豁免余量同源）；
- **触发点**：阶段 1 的"全池达 cap 豁免"处改造 —— 超过组预算的请求不再立即 ForceAcquire，而是进入该组的有界等待；
- **放行**：任一凭证在途降到 cap+slack 之下即按 FIFO 放行，放行时重走 TryAcquire（亲和映射可能已被改写，重新评估）；
- **超时**：等待超过上限（默认 30s，可配）返回 429 + Retry-After，复用 `reasonUpstreamRateLimited` 语义；
- **深度上限**：队列长度超过组预算×2 直接拒绝（防雪崩），同 429；
- **豁免不变**：响应续接（PreviousResponseID / WS requiredRef）不排队；WebSocket 新建轮首版不排队（lane 阻塞复杂度高），HTTP only。

### 实现（Go）

| # | 位置 | 内容 |
|---|---|---|
| 1 | `state/credential_slots.go` | Release 时广播通知（closed-channel broadcast 模式）；新增 `AcquireAnyWait(ids, budget, deadline) (uint, bool)`：等待任一凭证在途 < cap+slack，非阻塞优先、超时返回 false |
| 2 | `gateway/handler.go` 豁免分支 | `SelectionBeyondCap()` 时：未超组预算 → 照旧 ForceAcquire；超预算 → `AcquireAnyWait`（deadline = now + queue_timeout），成功重选，失败 429 |
| 3 | 设置 | `credential_queue_enabled`(默认 true)、`credential_queue_timeout_seconds`(默认 30)、slack 固定 2 常量（不配） |
| 4 | 观测 | `/api/credentials/load` 增加每组排队深度 `queued` 字段；watch 页加列 |

### 验收

- 单测：并发 24 请求 × 2 key(cap6) → 16 立即放行、8 排队、陆续放行无 429；深度超限/超时路径；
- 压测：排队开启后 `upstream_rate_limited`（5h 异常表）归零；
- 缓存率影响：排队请求最终仍走原 preferred（等待的就是它的槽位），缓存损失接近零 —— 这是排队优于"溢出到别的 key"的关键收益。
