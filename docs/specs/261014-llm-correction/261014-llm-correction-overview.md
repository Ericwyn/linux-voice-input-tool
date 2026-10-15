# LLM 智能语音纠错方案概述

## 背景

当前项目在语音识别完成后，提供了一套本地纠错能力（见 `260825-text-correction-service`）：一个可选的本地 BERT 模型（macbert-mdcspell-v2 / macbert4csc）加上用户自定义词典。这套能力有两个固有局限：

1. **纠错范围窄**：本地 BERT 是逐字的拼写纠错（CSC），只能改单字形近/音近错误，无法处理语义级别的问题，例如漏字、多字、口语顺序颠倒、同音词组、专有名词整体纠正等。
2. **词典需要手工维护**：用户词典依赖人工逐条录入，覆盖面有限。

而现在 LLM 已经非常擅长「把一段 ASR 口语转写顺一遍」：既能纠错别字，也能顺语序、补漏字、规范专有名词，且无需本地模型文件。因此希望增加一个**基于 LLM 的智能纠错**：在识别结果产出后、粘贴前，调用一个 OpenAI 兼容的 `/v1/chat/completions` 接口，让 LLM 对识别文本做一次整体润色纠错。

如果不做，用户只能继续依赖本地 CSC + 词典，无法利用 LLM 的语义纠错能力。

## 当前现状

### 主链路

录音停止后的最终文本处理链路（见 `session/session.go`）：

```text
StopAndTranscribe
  -> session.Finish(recorder)        // 引擎产出原始识别文本 rawText
  -> Controller.transcribe()
       -> applyCorrections(text)     // 本地 BERT + 词典纠错
       -> stripTrailingSymbols()     // 去尾部符号（261014 新增）
  -> pasteAndRecordStatistics()      // 粘贴到目标窗口并记统计
```

### 关键模块

| 模块 | 作用 | 关键符号 |
| --- | --- | --- |
| `corrector/service.go` | 按顺序执行一组纠错策略，fail-open | `Service`、`Strategy` 接口（`Name/Apply/Close`）、`Result` |
| `corrector/factory.go` | 从配置构建生产管线：BERT 策略 + 词典策略 | `NewServiceFromConfig` |
| `corrector/corrector.go` | 词典策略实现 | `New(dictPath)` |
| `corrector/onnxbert/` | 本地 BERT 策略实现 | `onnxbert.New` |
| `session/session.go` | 录音/识别/纠错/粘贴控制器 | `Controller.applyCorrections`、`Controller.correctionService` |
| `config/config.go` | 全局配置，YAML 序列化 | `Config`、`CorrectionsConfig`、`WhisperConfig` |
| `ui/app.go` / `ui/tray.go` | 托盘菜单与开关 | `toggleCorrections`、`notifyCorrectionToggle`、引擎单选组 `engineItems` |
| `api/whisper.go` | 现有 OpenAI 兼容 HTTP 客户端写法参考 | `Client.Transcribe` |

### 关键数据结构

`corrector.Strategy` 是现成的扩展点，fail-open 契约非常适合 LLM 这种「可能超时/失败、失败就用原文」的场景：

```go
type Strategy interface {
    Name() string
    Apply(ctx context.Context, text string) (string, error)
    Close() error
}
```

`Service.Correct(ctx, text)` 会把每个策略串联，某个策略返回 error 时跳过它、保留上一个有效文本继续（见 `corrector/service.go:103`）。

### 当前纠错开关与托盘

- 现有「启用语音纠错」是一个 **checkbox**（`ui/tray.go:49`、`ui/app.go:toggleCorrections`），对应 `cfg.Corrections.Enabled` 一个 bool。
- 引擎选择是一个 **单选组**（`ui/tray.go:60` 的 `engineItems`，`ui/app.go:selectEngine`/`refreshEngineItems`）：多个 checkbox，只有当前项打勾，对应 `cfg.Engine` 一个字符串。本次纠错模式要仿照引擎单选组来做。
- `notifyCorrectionToggle`（`ui/app.go:203`）已有「开启时校验前置条件（模型文件是否就绪），不满足则提示」的先例——正是本次「切到 LLM/联合时校验配置完整性」要对齐的模式。

### 当前纠错超时

`session/session.go:applyCorrections` 用 `cfg.Corrections.TimeoutMs`（默认 1500ms）包一个 `context.WithTimeout`。注意：**ONNX 模型调用无法被 context 强制打断**，所以这个超时对本地 BERT 是「软超时」。但对 LLM 这种纯 HTTP 调用，context 可以真正中断请求，超时是硬的。

## 目标

1. 新增基于 LLM 的智能纠错：识别结果产出后、粘贴前，调用 OpenAI 兼容的 `/v1/chat/completions`，用 LLM 返回文本作为该步输出。
2. 把纠错能力整合成一个**单选模式**（类似引擎选择），四选一：
   - `off` 关闭纠错
   - `local` 本地纠错（现有 BERT + 词典）
   - `llm` LLM 纠错
   - `combined` 联合纠错（本地 → LLM）
3. 配置文件新增 `llm_correction` 块：接口地址、API Key、模型、prompt 模板（含占位符）、超时、额外请求参数。
4. 托盘用一个单选组展示四种模式；切到 `llm` 或 `combined` 时校验 LLM 配置完整性，缺字段则**不切换**、保持原模式，并弹通知说明缺哪些。
5. LLM 超时（默认 2000ms）或任何失败时 **fail-open**：用进入 LLM 这一步前的文本作为输出，不阻塞、不中断粘贴。
6. `remove_trailing_symbols`（去尾符号，261014 新增，默认开）是**整个流程的最后一步**，无论用哪种纠错模式都在最后执行；即便 LLM 补回了句尾标点也会被去掉。

## 非目标

1. 不替换、不改动本地 BERT + 词典纠错的内部实现；它只是四种模式中的一种。
2. 不实现流式（SSE `stream=true`）返回；首版用一次性 JSON 返回。
3. 不做多轮对话、函数调用、多模型路由、重试退避。
4. 不在识别预览（录音中）阶段调用 LLM；只在最终文本阶段调一次。
5. 不新增设置窗口；LLM 的地址/密钥/模型/prompt 仍通过编辑 config.yaml + 托盘「重载配置」生效，模式切换走托盘单选组。
6. 不做 LLM 返回内容的安全审查/越狱防护；默认信任用户自己配置的服务端。
7. `temperature` 默认写死 `0`，不单独做配置项；需要调参通过 `extra_params` 透传。

## 核心设计

### 总体原则

1. **纠错模式单选**：新增一个 `correction_mode` 字符串（`off|local|llm|combined`），作为纠错的唯一真相来源，取代原来 `corrections.enabled` 这个 bool 的开关角色。托盘用单选组呈现，和引擎选择一致。
2. **两个独立 Service，由模式驱动启停**：`Controller` 持有本地纠错 `correctionService` 和 LLM 纠错 `llmService` 两个实例。模式决定各自是否 `Enabled`：
   - `local` → 仅本地启用
   - `llm` → 仅 LLM 启用
   - `combined` → 两个都启用
   - `off` → 两个都禁用
3. **执行顺序固定**：`rawText → 本地纠错 → LLM 纠错 → 去尾符号 → 粘贴`。本地先把明显错别字顺一遍，再交给 LLM 做语义润色，最后统一去尾部符号。各步在对应 Service 未启用时是零成本直通。
4. **LLM 复用 Strategy**：LLM 纠错实现为一个 `corrector.Strategy`（新增 `corrector/llm` 子包），天然继承 fail-open。
5. **硬超时 + fail-open**：LLM 调用用独立的 `context.WithTimeout(llm_correction.timeout_ms)`；超时/网络错/非 2xx/解析失败/返回空，一律回退到输入文本，仅记日志。

### 为什么用单选模式而不是两个独立 bool

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| A: 两个独立 checkbox（本地开关 + LLM 开关） | 实现直观 | 托盘出现两个勾选项，组合语义（都关/都开）要用户自己拼；和引擎选择的交互不一致 | 不选 |
| B: 单选模式（off/local/llm/combined），仿引擎单选组 | 交互与引擎一致；状态清晰互斥；一眼看懂当前用的是哪种纠错 | 需要把现有 `corrections.enabled` 迁移成模式 | **选它（用户指定）** |

### 纠错执行流程

```text
Controller.transcribe(recorder, session)
  text = session.Finish(recorder)              // rawText
  text = applyCorrections(text)                // 本地 BERT + 词典；correctionService 未启用则直通
  text = applyLLMCorrection(text)              // 新增；llmService 未启用则直通
  if cfg.RemoveTrailingSymbols:
      text = stripTrailingSymbols(text)        // 永远最后一步（高优先级）
  return transcriptionResult{RawText: rawText, Text: text, ...}
```

说明：

1. `RawText` 仍是引擎原始文本，用于统计，不受纠错影响。
2. 去尾符号在最后，兜住本地/LLM 任何一步补回来的句尾标点。

### 配置完整性校验

切到 `llm` 或 `combined` 模式时，校验 `llm_correction` 必填字段是否齐全（见字段表「必填」列）。校验失败：**不切换模式**（保持原来的勾选项）、不写回配置，弹通知列出缺失项。这与 `notifyCorrectionToggle` 现有行为一致。

`off`/`local` 模式切换不需要校验（本地纠错的模型就绪性沿用现有「下载模型」提示链路）。

## 数据结构 / 协议设计

### 新增模式字段与常量

```go
const (
    CorrectionModeOff      = "off"
    CorrectionModeLocal    = "local"
    CorrectionModeLLM      = "llm"
    CorrectionModeCombined = "combined"
)

type Config struct {
    // ...
    // CorrectionMode selects the final-text correction pipeline:
    // off | local | llm | combined. It is the single source of truth for
    // whether local and/or LLM correction run, replacing the old
    // corrections.enabled boolean (migrated on load).
    CorrectionMode string `yaml:"correction_mode"`

    Corrections   CorrectionsConfig   `yaml:"corrections"`
    LLMCorrection LLMCorrectionConfig `yaml:"llm_correction"` // 新增
    // ...
}
```

辅助判断：

```go
func (c *Config) UseLocalCorrection() bool {
    return c.CorrectionMode == CorrectionModeLocal || c.CorrectionMode == CorrectionModeCombined
}
func (c *Config) UseLLMCorrection() bool {
    return c.CorrectionMode == CorrectionModeLLM || c.CorrectionMode == CorrectionModeCombined
}
```

> `corrections.enabled` 字段保留，仅用于旧配置迁移（见「兼容与降级」），运行时不再直接作为开关；`correction_mode` 是权威。

### 新增配置块 `LLMCorrectionConfig`

```go
type LLMCorrectionConfig struct {
    BaseURL        string         `yaml:"base_url"`        // 如 https://api.openai.com/v1
    APIKey         string         `yaml:"api_key"`         // Bearer token
    Model          string         `yaml:"model"`           // 如 gpt-4o-mini
    PromptTemplate string         `yaml:"prompt_template"` // 含占位符 {{text}}
    TimeoutMs      int            `yaml:"timeout_ms"`      // 默认 2000
    ExtraParams    map[string]any `yaml:"extra_params"`    // 透传到请求 JSON 的额外参数
}
```

字段说明：

| 字段 | YAML | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `BaseURL` | `base_url` | **是** | `""` | 接口基址，内部拼 `/chat/completions` |
| `APIKey` | `api_key` | **是** | `""` | `Authorization: Bearer <key>` |
| `Model` | `model` | **是** | `""` | chat 模型名 |
| `PromptTemplate` | `prompt_template` | **是** | 见下 | 含占位符 `{{text}}` 的 prompt |
| `TimeoutMs` | `timeout_ms` | 否 | `2000` | 硬超时，超时回退原文 |
| `ExtraParams` | `extra_params` | 否 | 空 | 自定义键值，合并进请求 JSON（见下） |

### 占位符约定

prompt 模板用 `{{text}}` 作为占位符，替换为识别文本（用户已确认用此格式）：

- 模板里**不含** `{{text}}` → 视为配置不完整（校验不通过），识别文本无处注入。
- 替换用 `strings.ReplaceAll`，支持多次出现。

默认模板（`DefaultConfig` 给出，开箱即用）：

```text
你是一个中文语音转写纠错助手。请将下面这段语音识别结果中的错别字、同音词、多字漏字和明显的口语顺序问题纠正过来，使其通顺准确。只输出纠正后的文本，不要解释，不要加引号。

原文：{{text}}
```

### `/v1/chat/completions` 请求/响应

请求体以 `map[string]any` 构建，先铺基础字段，再把 `extra_params` 覆盖/追加上去（因此 `extra_params` 可覆盖 `temperature`、追加 `max_tokens`/`top_p` 等任意参数，兼容各家服务端）：

```json
{
  "model": "gpt-4o-mini",
  "messages": [{ "role": "user", "content": "<渲染后的 prompt>" }],
  "temperature": 0,
  "stream": false
}
```

- `temperature: 0` 作为默认，追求确定性；用户可用 `extra_params.temperature` 覆盖。
- `extra_params` 示例：

```yaml
llm_correction:
  extra_params:
    max_tokens: 500
    top_p: 0.9
```

响应只取第一条 choice 的 `message.content`：

```json
{ "choices": [ { "message": { "role": "assistant", "content": "<纠正后的文本>" } } ] }
```

### 新增 LLM 客户端与 Strategy

- `corrector/llm/client.go`：OpenAI 兼容 chat 客户端。用 `http.NewRequestWithContext` 绑定 ctx 实现硬超时；请求体用 map 合并 `extra_params`；返回 `choices[0].message.content` 的 `TrimSpace` 结果。
- `corrector/llm/strategy.go`：实现 `corrector.Strategy`：`Name()="llm"`；`Apply` 渲染 prompt → 调接口 → 返回文本，错误原样抛出（由 Service fail-open）；`Close()` 返回 nil。

## 实现方案

详见 `261014-llm-correction-implementation.md`，模块级清单：

1. **config 层**：新增 `correction_mode` + 常量 + `UseLocalCorrection`/`UseLLMCorrection`；新增 `LLMCorrectionConfig`（含 `ExtraParams`）；`DefaultConfig` 默认值；`LLMCorrectionComplete` 完整性校验；`LLMCorrectionTimeout`；`Load` 里从旧 `corrections.enabled` 迁移出 `correction_mode`。
2. **corrector/api 层**：新增 `corrector/llm` 子包（client + strategy）；新增 `corrector.NewLLMServiceFromConfig`；`NewServiceFromConfig` 的 enabled 改用 `cfg.UseLocalCorrection()`。
3. **session 层**：`Controller` 新增 `llmService`；`New`/`Reload`/`Close` 管理它；新增 `applyLLMCorrection`；新增 `SetCorrectionMode`/`CorrectionMode`；`transcribe` 串入 LLM 步。
4. **ui 层**：托盘把「启用语音纠错」checkbox 换成纠错模式单选组（4 项）；新增 `selectCorrectionMode`，切到 llm/combined 时校验完整性；`reloadConfig` 同步单选态。
5. **文档**：更新 `README.md`。

## 兼容与降级

1. **旧配置迁移**：旧 config.yaml 无 `correction_mode`。`Load` 中若 `correction_mode` 为空，则按旧 `corrections.enabled` 推导：`true → local`，`false/缺省 → off`，并回写文件（和现有 `migrateLegacyAPI` 回写一个套路）。未知的 `correction_mode` 值归一到 `off`。
2. **旧配置无 `llm_correction` 块**：`DefaultConfig` 先铺默认值再 Unmarshal，所以旧配置 `TimeoutMs=2000`、默认 prompt、地址/密钥/模型为空 → `combined`/`llm` 需要的校验不通过 → 不会误发请求。
3. **LLM 调用失败降级**：超时/网络错/非 2xx/JSON 解析失败/`choices` 空/content 空 → fail-open 返回输入文本，仅记日志。
4. **模式=llm/combined 但配置不完整**：托盘切换时就会被校验拦下不切换；即便手工把 `correction_mode: llm` 写进文件但字段缺失，Controller 构建 `llmService` 时校验不通过 → 空 Service（`Enabled()` 为 false）→ 直通不发请求。
5. **回滚**：`correction_mode: off` 或删除 `llm_correction` 块即可回到纯本地/无纠错；无持久化脏数据（统计记 RawText 与最终 Text，不区分来源）。

## 可观测性

1. LLM 成功：`LLM corrected in <ms>ms: <before> -> <after>`。
2. LLM 失败/超时：`LLM correction failed (fail-open): <err>`。
3. 模式切换：`Correction mode = <mode>`。
4. 不新增 metrics（项目当前无）；统计日文件不区分纠错来源，保持现状。

## 测试计划

1. **单元测试**
   - config：`correction_mode` 默认值；`UseLocalCorrection`/`UseLLMCorrection` 四模式真值表；`LLMCorrectionComplete` 齐全/缺各字段/缺占位符；`LLMCorrectionTimeout` 默认回退；旧 YAML（`corrections.enabled: true/false`、无 `correction_mode`）迁移成 `local`/`off`；旧 YAML 无 `llm_correction` 块时默认值正确。
   - prompt 渲染：`{{text}}` 单次/多次/缺失。
   - LLM client：`httptest.Server` mock，覆盖成功、非 200、超时（sleep > ctx timeout）、空 choices、空 content；验证 `extra_params` 被正确合并进请求体（含覆盖 temperature）。
   - LLM strategy：`Apply` 成功返回纠正文本；client 报错时返回 error。
   - factory：`NewLLMServiceFromConfig` 完整配置得启用服务、不完整得禁用空服务。
   - session：`applyLLMCorrection` 在禁用/空文本/mock 成功/mock 失败下的输出；四种模式下 `SetCorrectionMode` 对两个 Service enabled 的设置。
2. **集成/手动**
   - 真实或 mock 的 OpenAI 兼容服务，录一段带错别字的话，验证联合纠错结果；验证去尾符号在 LLM 补标点后仍生效。
   - 断网/错 base_url，验证 2s 后 fail-open、不卡顿。
   - 托盘：配置不完整时切到 LLM/联合 → 不切换 + 通知；补全重载后 → 可切换。
3. **回归**：现有 `config`/`session`/`corrector`/`engine`/`ui` 测试全绿（注意 `TestControllerSetCorrectionsEnabledDoesNotReplaceEngine` 等涉及旧开关的测试需同步调整为模式语义）。

## 上线与回滚

1. **开关**：`correction_mode`，默认 `off`（新装）或迁移值（旧用户）。
2. **无数据迁移**（指统计/模型数据）；仅配置字段迁移，自动回写一次。
3. **回滚**：`correction_mode: off`。

## 建议实施顺序

1. config：模式常量 + 字段 + 判断方法 + `LLMCorrectionConfig` + 默认值 + 校验 + 超时 + 迁移 + 测试。
2. `corrector/llm` client + strategy + mock 测试（含 extra_params）。
3. `corrector.NewLLMServiceFromConfig`；`NewServiceFromConfig` 改用 `UseLocalCorrection`。
4. session：`llmService` + `applyLLMCorrection` + `SetCorrectionMode`/`CorrectionMode` + 测试。
5. ui：纠错模式单选组 + `selectCorrectionMode` + reload 同步 + 调整相关测试。
6. README。
7. 全量 `go vet` + `go test ./...` + `go build ./...`。

## 风险与待确认问题

### 已确认决策（用户已给出）

1. 占位符用 `{{text}}`。
2. `remove_trailing_symbols` 为最后一步、最高优先级，LLM 补的标点也照去。
3. 纠错改为单选模式，含 本地 / LLM / 联合 三种工作模式（本方案另加 `off` 以支持关闭）。
4. `temperature` 不单独配置；通过 `extra_params` 自定义透传任意请求 JSON 参数（键名+值）。
5. 用经典 `/v1/chat/completions` 非流式；超时默认 2000ms，失败 fail-open 用原文。

### 待确认问题

1. **是否接受第 4 个模式 `off`（关闭纠错）**：你只提了三种工作模式，但需要一个关闭入口。方案默认加 `关闭纠错`。若你更想要别的方式（比如保留一个总开关 checkbox + 三选一），告诉我。（**可能阻塞 UI 结构，建议先定**）
2. **旧用户迁移默认值**：旧配置 `corrections.enabled: true` 迁成 `local`、`false` 迁成 `off`。认可这个映射吗？
3. **`extra_params` 覆盖策略**：方案允许 `extra_params` 覆盖基础字段（含 `temperature`，甚至 `model`/`messages`）。是否需要保护 `model`/`messages`/`stream` 不被覆盖？（建议至少保护 `messages`，其余允许覆盖，兼容性最好。）
