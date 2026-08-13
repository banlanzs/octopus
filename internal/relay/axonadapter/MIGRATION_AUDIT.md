# 文本路径迁移边界修复等价性审计

本文件记录本地自研 transformer 的边界修复与 GitHub `looplj/axonhub@unstable`（`llm@v0.0.0-20260813091334-fc1d27dad411`）能力的对照结论，作为文本路径迁移（保守核心替换）的决策依据。

## 已覆盖（axonhub 有等价能力）

| 本地修复 | axonhub 对应 | 说明 |
|---------|-------------|------|
| `compat/gemini_signature_cache.go`（Gemini thoughtSignature 24h 缓存） | `transformer/gemini/{aggregator,inbound_convert,convert}.go` | 签名透传与聚合已覆盖 |
| DeepSeek thinking 剥离/回放 | `transformer/deepseek/outbound.go` | reasoning 处理已覆盖 |
| 消息交替规则（`alternation.go`） | `transformer/openai/model.go`、`responses/model.go` | 部分覆盖，边界需回归 |

## 缺失 / 需补偿

| 本地修复 | 缺失说明 | 建议 |
|---------|---------|------|
| volcengine Responses API 特化（`outbound/volcengine/response.go`） | axonhub `doubao` 是 openai/chat_completions 格式，无 Responses 的 `input.partial`、`thinking.type` 映射、`reasoning_effort` 模型白名单、`metadata` 置空 | 火山 Responses 渠道若迁到 doubao，需在 relay 层补偿；否则保留自研 volcengine outbound |
| 孤儿 tool_use 修复（`compat/tool_calls.go` 的 `FixOrphanedToolCalls`） | axonhub anthropic 无明确对应 | 迁移验证阶段逐项回归 Anthropic 严格 schema 场景 |
| 统一 finishreason canonical 层（`model/finishreason.go`） | axonhub 各供应商独立处理 finish_reason，无跨协议统一类型 | 本地 `quality`/metrics 若依赖 canonical 判定，需在适配层保留 |

## 结论

文本路径迁 axonhub 前，必须对「volcengine → doubao」场景做等价性补偿或保留自研路径；孤儿 tool_use 与 finishreason 判定需在迁移验证阶段作为重点回归项。
