# Changelog

## [Unreleased]

### 文本路径协议保真：对齐 axonhub pipeline 的 header 合并与 Responses 同格式直通

- **问题**：Codex 客户端经 `/v1/responses` 转发时，`Originator` / `Session-Id` / `X-Codex-*` 等协商头被丢弃，上游将网关识别为“套了网关”并返回 401（客户端看到 502）。
- **修复**：`TextHandler` 对齐 axonhub `pipeline.processRequest`：入站后挂载 `llmReq.RawRequest`，出站前执行 `httpclient.MergeInboundRequest` → `httpclient.FinalizeAuthHeaders` → 渠道参数/自定义 header；跨协议转换路径同样生效。
- **Responses 同格式直通**：新增 OpenAI Responses→Responses raw 直通，保留 Codex 扩展字段（`additional_tools`、`client_metadata` 等），仅重写顶层 `model`；流式 SSE 事件原样直通并 sidecar 聚合 usage。


### 移除分组健康检查（结果仅供展示，无调度用途）

- **移除后端**：`internal/grouphealth` 的 Service 编排、`op/group_health.go` repository、`model/group_health.go` 模型、`handlers/group_health.go` HTTP 接口（`/api/v1/group/health/*`），以及建表迁移 `migrate/010.go`、`group_health_probe_mode.go` 与 `SettingKeyGroupHealthEnabled` 设置项。
- **移除前端**：`api/endpoints/group-health.ts`、分组卡片健康徽章、首页健康摘要条/总览、可靠性设置开关，以及三语言 locale 对应文案。
- **保留探活能力**：`internal/grouphealth/probe.go` 的 `Prober` 被 AutoRank 主动探测与站点级被动退役（POR）复用，本次不动。

### 文本 API 转换迁移至 axonhub/llm（全量替换 + 各取所长）

- **Go 工具链升级**：`go 1.25` → `go 1.26`，并引入 `github.com/looplj/axonhub/llm@fc1d27da`（GitHub unstable 固定 sha）+ `wtj-0527/go-sse` fork replace。
- **协议转换迁移**：文本 HTTP 路径（`/v1/chat/completions`、`/v1/messages`、`/v1/responses`、`/v1/embeddings`）的协议转换从自研 `internal/transformer` 迁到 axonhub/llm，新增 `internal/relay/axonadapter`（格式映射 + transformer 工厂）与 `internal/relay/textrelay.go`（TextHandler 转发入口）。
- **保留本地网关特性（各取所长）**：迭代器选通道、渠道健康闭环（熔断/AutoRank/粘性/outlier/统计）、质量失败检测、路由学习、参数覆盖/自定义 header、同通道重试（指数退避）、首 token 超时、SSE 心跳、同格式直通（anthropic 字节稳定保 prompt caching）均已适配到 TextHandler。
- **volcengine 处置**：火山渠道改用 axonhub responses outbound（协议一致）并补齐火山特化（thinking/partial input/metadata/reasoning 白名单）。
- **路由切换**：`/v1/chat/completions`、`/v1/messages`、`/v1/responses`、`/v1/embeddings` 一次性切到 TextHandler；`/v1/responses/compact`、`/v1/responses`(WS) 与图片路由保留自研。
- **审计与补偿**：`internal/relay/axonadapter/MIGRATION_AUDIT.md` 记录自研边界修复与 axonhub 能力对照（volcengine Responses 特化、孤儿 tool_use、finishreason canonical 层）。

### 自动排序 (AutoRank) 全面优化

- **快照精确恢复**：`auto_rank_snapshots` 新增 `sample_trail` 列，保存窗口内最近 20 条样本的时间序列（时间偏移/成败/耗时/探测标记）。重启后按样本时间线精确重建窗口（时间分布、失败位置、逐条延迟），不再使用"最近 failures 条为失败"的近似假设；旧快照行自动回退近似重建，兼容升级。
- **快照差异化同步**：落库从"全表 DELETE + INSERT"改为按 `(group_id, channel_id, model_name)` 差异化同步（无变化跳过、变化行 UPDATE、消失键 DELETE），流量稳定时几乎零写入，降低大部署下 DB 周期压力。
- **探索策略**：探索选择从纯轮转改为"欠采样优先"（realSamples 升序，同欠采样度按最近被提供顺序轮转），与主动探测任务的排序口径对齐，冷启动样本更快积累。
- **TTFB 相对惩罚默认开启**：`auto_rank_ttfb_enabled` 默认值改为 `true`（权重保持温和的 20，且带样本置信度打折保护）。
  > **已有部署注意**：数据库已存在的 `false` 值不会被默认值覆盖，需在「设置 → 可靠性 → 自动排序 → TTFB 惩罚」中手动开启。
- **min_samples 默认 3 → 5**：`auto_rank_min_samples` 默认值改为 `5`，降低 Wilson 置信下界在少量样本下的排序震荡。
  > **已有部署注意**：同样需手动调整，或接受现有值。
- **调度性能**：单候选快路径（跳过份额/竞技池计算，仍保持状态与记账）；档位/竞技池/份额计算移出组级锁，锁内仅做状态写回与选择。
- **可观测性**：分组模型列表的健康徽章 tooltip 新增"样本时间线"（✓ 成功 / ✗ 失败 / p 探测，从旧到新），直观展示排序依据。
