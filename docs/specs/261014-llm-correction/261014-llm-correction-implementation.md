# LLM 智能语音纠错实现方案

本篇是 `261014-llm-correction-overview.md` 的实现细节补充，列出逐模块的具体改造点、结构体、函数签名与测试细节。实现前请先确认 overview 末尾「待确认问题」。

## 0. 关键决策（已确认）

1. 纠错改为**单选模式** `correction_mode`：`off | local | llm | combined`。
2. 执行顺序固定：`rawText → 本地纠错 → LLM 纠错 → 去尾符号`，去尾符号恒为最后一步。
3. 占位符 `{{text}}`。
4. LLM 请求支持 `extra_params` 透传任意 JSON 参数（键名 + 值）。
5. 超时默认 2000ms，硬超时，fail-open。

## 1. config 层

文件：`config/config.go`

### 1.1 模式常量与字段

```go
const (
    CorrectionModeOff      = "off"
    CorrectionModeLocal    = "local"
    CorrectionModeLLM      = "llm"
    CorrectionModeCombined = "combined"
)
```

挂到 `Config`（`correction_mode` 放在 `Corrections` 之前，作为权威开关）：

```go
CorrectionMode string              `yaml:"correction_mode"`
Corrections    CorrectionsConfig   `yaml:"corrections"`
LLMCorrection  LLMCorrectionConfig `yaml:"llm_correction"`
```

判断方法：

```go
// UseLocalCorrection reports whether the local BERT+dictionary pass runs.
func (c *Config) UseLocalCorrection() bool {
    return c.CorrectionMode == CorrectionModeLocal || c.CorrectionMode == CorrectionModeCombined
}

// UseLLMCorrection reports whether the LLM pass runs.
func (c *Config) UseLLMCorrection() bool {
    return c.CorrectionMode == CorrectionModeLLM || c.CorrectionMode == CorrectionModeCombined
}

// NormalizeCorrectionMode maps empty/unknown values to a valid mode. Empty is
// resolved by migration (see Load); any other unknown value degrades to off.
func NormalizeCorrectionMode(mode string) string {
    switch mode {
    case CorrectionModeOff, CorrectionModeLocal, CorrectionModeLLM, CorrectionModeCombined:
        return mode
    default:
        return CorrectionModeOff
    }
}
```

### 1.2 新增 LLM 配置结构体

```go
// LLMCorrectionConfig configures the optional LLM-based final-text correction.
// It calls an OpenAI-compatible /v1/chat/completions endpoint. Any failure or
// timeout falls open to the input text, so the user never loses the recognized
// result to a flaky network.
type LLMCorrectionConfig struct {
    // BaseURL is the OpenAI-compatible API base, e.g. https://api.openai.com/v1.
    // The client appends "/chat/completions".
    BaseURL string `yaml:"base_url"`
    // APIKey is sent as "Authorization: Bearer <key>".
    APIKey string `yaml:"api_key"`
    // Model is the chat model name, e.g. gpt-4o-mini.
    Model string `yaml:"model"`
    // PromptTemplate must contain the placeholder "{{text}}", replaced by the
    // recognized text before the request is sent.
    PromptTemplate string `yaml:"prompt_template"`
    // TimeoutMs is a hard timeout for the whole request; on timeout the input
    // text is returned unchanged. Defaults to 2000.
    TimeoutMs int `yaml:"timeout_ms"`
    // ExtraParams is merged into the request JSON, letting the user set or
    // override arbitrary fields (max_tokens, top_p, even temperature) for
    // broad server compatibility. messages is never overridable (see client).
    ExtraParams map[string]any `yaml:"extra_params"`
}
```

### 1.3 占位符常量与默认 prompt

```go
const LLMCorrectionPlaceholder = "{{text}}"

const defaultLLMPromptTemplate = "你是一个中文语音转写纠错助手。" +
    "请将下面这段语音识别结果中的错别字、同音词、多字漏字和明显的口语顺序问题纠正过来，使其通顺准确。" +
    "只输出纠正后的文本，不要解释，不要加引号。\n\n原文：{{text}}"
```

### 1.4 DefaultConfig 默认值

```go
CorrectionMode: CorrectionModeOff, // 新装默认关闭；旧配置由 Load 迁移覆盖

LLMCorrection: LLMCorrectionConfig{
    BaseURL:        "",
    APIKey:         "",
    Model:          "",
    PromptTemplate: defaultLLMPromptTemplate,
    TimeoutMs:      2000,
    ExtraParams:    nil,
},
```

> 现有 `Corrections.Enabled` 默认 `false` 保持不变（仅供迁移读取）。

### 1.5 完整性校验与超时

```go
func (c *Config) LLMCorrectionComplete() (ok bool, missing []string) {
    l := c.LLMCorrection
    if strings.TrimSpace(l.BaseURL) == "" {
        missing = append(missing, "base_url")
    }
    if strings.TrimSpace(l.APIKey) == "" {
        missing = append(missing, "api_key")
    }
    if strings.TrimSpace(l.Model) == "" {
        missing = append(missing, "model")
    }
    if strings.TrimSpace(l.PromptTemplate) == "" {
        missing = append(missing, "prompt_template")
    } else if !strings.Contains(l.PromptTemplate, LLMCorrectionPlaceholder) {
        missing = append(missing, "prompt_template(缺少占位符 "+LLMCorrectionPlaceholder+")")
    }
    return len(missing) == 0, missing
}

func (c *Config) LLMCorrectionTimeout() time.Duration {
    ms := c.LLMCorrection.TimeoutMs
    if ms <= 0 {
        ms = 2000
    }
    return time.Duration(ms) * time.Millisecond
}
```

（config 包需 import `strings`、`time`。）

### 1.6 旧配置迁移

在 `Load()` 中，`yaml.Unmarshal` 之后、`ResolveModelPaths` 附近加入迁移：若 `correction_mode` 为空，用旧 `corrections.enabled` 推导，并标记需要回写。

```go
// migrateCorrectionMode fills correction_mode for configs written before the
// single-select mode existed: enabled:true -> local, otherwise -> off. It
// returns true when it changed cfg so Load rewrites the file once.
func migrateCorrectionMode(cfg *Config) bool {
    if cfg.CorrectionMode != "" {
        normalized := NormalizeCorrectionMode(cfg.CorrectionMode)
        if normalized != cfg.CorrectionMode {
            cfg.CorrectionMode = normalized
            return true
        }
        return false
    }
    if cfg.Corrections.Enabled {
        cfg.CorrectionMode = CorrectionModeLocal
    } else {
        cfg.CorrectionMode = CorrectionModeOff
    }
    return true
}
```

在 `Load` 的两处（主配置路径、legacy 路径）调用，并把返回值并入现有的 `migrated` 回写判断：

```go
migrated := migrateLegacyAPI(data, cfg)
migrated = migrateCorrectionMode(cfg) || migrated
cfg.ResolveModelPaths()
if migrated {
    if err := Save(cfg); err != nil {
        return nil, err
    }
}
```

> 新装（无配置文件）走 `DefaultConfig()`，`CorrectionMode` 已是 `off`，Save 时写入，无需迁移。

## 2. LLM 客户端

文件：`corrector/llm/client.go`（新建子包 `corrector/llm`）

请求体用 `map[string]any` 构建，先铺基础字段再合并 `extra_params`；`messages` 不可被覆盖。

```go
package llm

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "strings"
)

type Client struct {
    baseURL     string
    apiKey      string
    model       string
    extraParams map[string]any
    http        *http.Client
}

func NewClient(baseURL, apiKey, model string, extraParams map[string]any) *Client {
    return &Client{
        baseURL:     strings.TrimRight(baseURL, "/"),
        apiKey:      apiKey,
        model:       model,
        extraParams: extraParams,
        http:        &http.Client{}, // 超时由 ctx 控制
    }
}

type chatResponse struct {
    Choices []struct {
        Message struct {
            Content string `json:"content"`
        } `json:"message"`
    } `json:"choices"`
}

// Complete sends a single-user-message chat completion and returns the trimmed
// assistant content. ctx carries the hard timeout; a cancelled ctx aborts the
// in-flight request.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
    payload := map[string]any{
        "model":       c.model,
        "temperature": 0,
        "stream":      false,
    }
    // Merge user overrides first (may set max_tokens, top_p, override temperature...).
    for k, v := range c.extraParams {
        payload[k] = v
    }
    // messages is authoritative and must not be overridden by extra_params.
    payload["messages"] = []map[string]string{{"role": "user", "content": prompt}}

    buf, err := json.Marshal(payload)
    if err != nil {
        return "", fmt.Errorf("marshal request: %w", err)
    }

    url := c.baseURL + "/chat/completions"
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
    if err != nil {
        return "", fmt.Errorf("create request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer "+c.apiKey)

    resp, err := c.http.Do(req)
    if err != nil {
        return "", fmt.Errorf("send request: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
        return "", fmt.Errorf("LLM API error (status %d): %s", resp.StatusCode, string(body))
    }

    var result chatResponse
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return "", fmt.Errorf("decode response: %w", err)
    }
    if len(result.Choices) == 0 {
        return "", fmt.Errorf("LLM response has no choices")
    }
    text := strings.TrimSpace(result.Choices[0].Message.Content)
    if text == "" {
        return "", fmt.Errorf("LLM returned empty content")
    }
    return text, nil
}
```

> 决策（待确认 3）：`model`/`temperature`/`stream` 允许被 `extra_params` 覆盖（兼容性优先），仅 `messages` 保护。实现里把 `messages` 放在合并之后即可。

## 3. LLM Strategy

文件：`corrector/llm/strategy.go`

```go
package llm

import (
    "context"
    "strings"
)

type Strategy struct {
    client      *Client
    promptTmpl  string
    placeholder string
}

func NewStrategy(client *Client, promptTemplate, placeholder string) *Strategy {
    return &Strategy{client: client, promptTmpl: promptTemplate, placeholder: placeholder}
}

func (s *Strategy) Name() string { return "llm" }

func (s *Strategy) Apply(ctx context.Context, text string) (string, error) {
    prompt := strings.ReplaceAll(s.promptTmpl, s.placeholder, text)
    return s.client.Complete(ctx, prompt)
}

func (s *Strategy) Close() error { return nil }
```

> `corrector/llm` 不 import `corrector`，避免循环依赖；靠 Go 的隐式接口实现满足 `corrector.Strategy`。超时由 session 层传入的 ctx 控制。

## 4. corrector 工厂

文件：`corrector/factory.go`

### 4.1 本地 Service 的 enabled 改为模式驱动

```go
// 原: NewService(cfg.Corrections.Enabled, strategies...)
return NewService(cfg.UseLocalCorrection(), strategies...), nil
```

### 4.2 新增 LLM Service 工厂

```go
// NewLLMServiceFromConfig builds a correction service containing only the LLM
// strategy. It returns a disabled empty service when the config is incomplete,
// so the caller always holds a non-nil service that simply passes text through
// until the user fills in the config.
func NewLLMServiceFromConfig(cfg *config.Config) *Service {
    if ok, _ := cfg.LLMCorrectionComplete(); !ok {
        return NewService(false)
    }
    client := llm.NewClient(
        cfg.LLMCorrection.BaseURL,
        cfg.LLMCorrection.APIKey,
        cfg.LLMCorrection.Model,
        cfg.LLMCorrection.ExtraParams,
    )
    strategy := llm.NewStrategy(client, cfg.LLMCorrection.PromptTemplate, config.LLMCorrectionPlaceholder)
    return NewService(cfg.UseLLMCorrection(), strategy)
}
```

（factory import `linux-voice-input/corrector/llm`。）

## 5. session 层

文件：`session/session.go`

### 5.1 Controller 字段

```go
correctionService *corrector.Service
llmService        *corrector.Service // 新增
```

### 5.2 构建/重建/释放

`New`：

```go
c.correctionService = newCorrectionService(cfg)
c.llmService = corrector.NewLLMServiceFromConfig(cfg)
```

`Reload`（在重建 correctionService 之后）：

```go
if c.llmService != nil {
    _ = c.llmService.Close()
}
c.llmService = corrector.NewLLMServiceFromConfig(cfg)
```

`Close`：

```go
if c.llmService != nil {
    _ = c.llmService.Close()
    c.llmService = nil
}
```

> `newCorrectionService` 内部的 fallback（见现有 `session.go:125`）在 strategy 配置无效时降级为 dictionary-only，这段逻辑保留；注意它用 `cfg.Corrections.Enabled` 作日志判断，改为 `cfg.UseLocalCorrection()` 以对齐模式语义。

### 5.3 transcribe 串入 LLM 步

```go
correction := c.applyCorrections(text)      // 本地（correctionService 未启用则直通）
corrected := c.applyLLMCorrection(correction.Text) // 新增（llmService 未启用则直通）

finalText := corrected
c.mu.Lock()
removeTrailing := c.cfg.RemoveTrailingSymbols
c.mu.Unlock()
if removeTrailing {
    finalText = stripTrailingSymbols(finalText) // 恒为最后一步
}

return transcriptionResult{
    RawText:           text,
    Text:              finalText,
    AppliedStrategies: correction.Applied,
}
```

> `AppliedStrategies` 统计口径暂不纳入 LLM（保持稳定，属非目标）。

### 5.4 applyLLMCorrection

```go
// applyLLMCorrection runs the independent LLM correction service with its own
// hard timeout. Unlike the local ONNX path, an HTTP request is genuinely
// cancellable, so the timeout bounds real latency. Failures fall open to the
// input text (via the strategy error contract inside Service).
func (c *Controller) applyLLMCorrection(text string) string {
    c.mu.Lock()
    service := c.llmService
    timeout := c.cfg.LLMCorrectionTimeout()
    c.mu.Unlock()
    if service == nil || !service.Enabled() || text == "" {
        return text
    }

    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    defer cancel()

    result := service.Correct(ctx, text)
    for _, failure := range result.Failures {
        log.Printf("LLM correction failed (fail-open): %v", failure.Err)
    }
    if result.Text != text {
        log.Printf("LLM corrected in %dms: %s -> %s", result.Duration.Milliseconds(), text, result.Text)
    }
    return result.Text
}
```

### 5.5 模式启停入口

```go
// CorrectionMode returns the live mode string.
func (c *Controller) CorrectionMode() string {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.cfg.CorrectionMode
}

// SetCorrectionMode switches the correction pipeline. It updates the config
// mode and flips each service's Enabled accordingly, without rebuilding the ASR
// engine or reloading models. The LLM service only actually calls out when its
// config is complete (an incomplete config yields an empty, inert service).
func (c *Controller) SetCorrectionMode(mode string) {
    c.mu.Lock()
    c.cfg.CorrectionMode = mode
    local := c.correctionService
    llm := c.llmService
    useLocal := c.cfg.UseLocalCorrection()
    useLLM := c.cfg.UseLLMCorrection()
    c.mu.Unlock()
    if local != nil {
        local.SetEnabled(useLocal)
    }
    if llm != nil {
        llm.SetEnabled(useLLM)
    }
}
```

> 替换现有 `SetCorrectionsEnabled`（被 `ui.toggleCorrections` 调用）。该方法可删除或保留为 `SetCorrectionMode` 的薄封装；推荐直接改调用方。

## 6. ui 层

### 6.1 tray.go：纠错模式单选组

把现有「启用语音纠错」checkbox 替换为一个单选组，仿引擎单选组写法（`engineItems`）：

```go
// 顶部定义展示顺序与标签
var correctionModes = []struct{ Mode, Label string }{
    {config.CorrectionModeOff, "纠错: 关闭"},
    {config.CorrectionModeLocal, "纠错: 本地"},
    {config.CorrectionModeLLM, "纠错: LLM"},
    {config.CorrectionModeCombined, "纠错: 本地+LLM"},
}
```

setupTray 中：

```go
current := a.CorrectionMode()
for _, m := range correctionModes {
    mode := m.Mode
    item := menu.AddCheckbox(m.Label, mode == current, func() {
        a.selectCorrectionMode(mode)
    })
    a.correctionModeItems[mode] = item
}
```

`App` 结构体：把 `correctionsItem *systray.MenuItem` 替换为 `correctionModeItems map[string]*systray.MenuItem`（在 `NewApp` 初始化 map）。

### 6.2 app.go：selectCorrectionMode

```go
// CorrectionMode returns the live correction mode.
func (a *App) CorrectionMode() string {
    a.mu.Lock()
    defer a.mu.Unlock()
    return a.cfg.CorrectionMode
}

// selectCorrectionMode switches the correction pipeline from the tray single-
// select group. Switching to llm/combined first validates the LLM config; if
// incomplete it keeps the previous mode, does not persist, and notifies which
// fields are missing (mirrors the engine-switch + model-check pattern).
func (a *App) selectCorrectionMode(mode string) {
    go func() {
        a.mu.Lock()
        prev := a.cfg.CorrectionMode
        if mode == prev {
            a.mu.Unlock()
            return
        }
        needsLLM := mode == config.CorrectionModeLLM || mode == config.CorrectionModeCombined
        if needsLLM {
            if ok, missing := a.cfg.LLMCorrectionComplete(); !ok {
                a.mu.Unlock()
                a.refreshCorrectionModeItems(prev) // 恢复到原选项
                notify.NotifyAux("语音纠错",
                    "LLM 配置不完整，缺少: "+strings.Join(missing, ", ")+"；请在配置文件补全后重载", true)
                return
            }
        }
        a.cfg.CorrectionMode = mode
        cfgCopy := *a.cfg
        saveConfig := a.saveConfig
        a.mu.Unlock()

        a.controller.SetCorrectionMode(mode)
        if saveConfig == nil {
            saveConfig = config.Save
        }
        if err := saveConfig(&cfgCopy); err != nil {
            log.Printf("Save correction mode failed: %v", err)
        }
        a.refreshCorrectionModeItems(mode)
        a.notifyCorrectionMode(mode, &cfgCopy)
        log.Printf("Correction mode = %s", mode)
    }()
}

// refreshCorrectionModeItems checks only the selected mode's item (single-select).
func (a *App) refreshCorrectionModeItems(selected string) {
    for mode, item := range a.correctionModeItems {
        item.SetChecked(mode == selected)
    }
}

func (a *App) notifyCorrectionMode(mode string, cfg *config.Config) {
    switch mode {
    case config.CorrectionModeOff:
        notify.NotifyAux("语音纠错", "已关闭", true)
    case config.CorrectionModeLocal:
        // 复用现有本地模型就绪性检查提示
        a.notifyLocalCorrectionReady(cfg)
    case config.CorrectionModeLLM:
        notify.NotifyAux("语音纠错",
            fmt.Sprintf("已启用 LLM 纠错，超时 %dms，失败自动回退原文", cfg.LLMCorrection.TimeoutMs), true)
    case config.CorrectionModeCombined:
        notify.NotifyAux("语音纠错",
            fmt.Sprintf("已启用 本地+LLM 纠错，LLM 超时 %dms，失败自动回退原文", cfg.LLMCorrection.TimeoutMs), true)
    }
}
```

> `notifyLocalCorrectionReady` 由现有 `notifyCorrectionToggle` 的「启用」分支（模型就绪性检查）抽取而来；现有 `toggleCorrections`/`notifyCorrectionToggle` 删除或改造为上面的模式版本。

### 6.3 reloadConfig 同步单选态

```go
mode := config.NormalizeCorrectionMode(newCfg.CorrectionMode)
// ...
a.refreshCorrectionModeItems(mode)
```

> 若 `mode` 为 llm/combined 但配置不完整，`controller` 的 `llmService` 为空 Service（不发请求），但单选仍按配置显示；可选地在 reload 时校正：不完整则回落显示到 `local`/`off`。首版保持按配置显示，日志能看出未生效即可。

### 6.4 受影响的现有测试

- `session/correction_test.go:TestControllerSetCorrectionsEnabledDoesNotReplaceEngine`：`SetCorrectionsEnabled` 若被 `SetCorrectionMode` 取代，改为用 `SetCorrectionMode(CorrectionModeLocal)` 断言引擎不被替换。
- `ui/*` 若有引用 `correctionsItem`/`toggleCorrections` 的测试，改为模式单选组对应断言。
- `config` 现有测试不受影响（新增字段默认值不破坏旧断言），但新增迁移/模式测试。

## 7. 测试清单

| 测试文件 | 覆盖点 |
| --- | --- |
| `config/llm_correction_test.go` | 默认 `correction_mode=off`；`UseLocalCorrection`/`UseLLMCorrection` 四模式真值表；`LLMCorrectionComplete` 齐全/缺各字段/缺占位符；`LLMCorrectionTimeout` 默认回退；`NormalizeCorrectionMode` 未知值→off |
| `config/correction_migration_test.go` | 旧 YAML `corrections.enabled: true` 无 `correction_mode` → 迁移为 `local`；`false`/缺省 → `off`；已有合法 `correction_mode` 不被覆盖 |
| `corrector/llm/client_test.go` | `httptest.Server` mock：成功取 content；非 200；sleep>timeout 时 ctx 取消；空 choices；空 content；`extra_params` 合并进请求体且 `messages` 不被覆盖、`temperature` 可被覆盖 |
| `corrector/llm/strategy_test.go` | `{{text}}` 单次/多次替换；`Apply` 成功；client error 透传 |
| `corrector/factory_test.go` | `NewLLMServiceFromConfig` 完整→启用、不完整→禁用空；`NewServiceFromConfig` 的 enabled 跟随 `UseLocalCorrection` |
| `session/llm_correction_test.go` | `applyLLMCorrection` 禁用/空文本/mock 成功/mock 失败 fail-open；`SetCorrectionMode` 四模式对两个 Service enabled 的设置 |
| `ui/correction_mode_test.go`（可选） | `selectCorrectionMode`：切 llm/combined 不完整时不切换+通知+恢复原选项；完整时切换+保存+刷新单选 |

session 层测试复用 `session/correction_test.go` 的 `sessionTestStrategy`，用 `corrector.NewService(true, fakeStrategy)` 注入 `controller.llmService`，无需真实网络。

## 8. 验证命令

```bash
export GOCACHE=$(mktemp -d)
go vet ./...
go test ./...
go build ./...
```

## 9. README 更新点

- 配置示例：顶层加 `correction_mode: off`；新增 `llm_correction:` 块（含 `extra_params` 示例）。
- 字段表：新增 `correction_mode`（4 取值）与 LLM 的 6 行（base_url/api_key/model/prompt_template/timeout_ms/extra_params）。
- 托盘菜单说明：把「启用语音纠错」改为「纠错模式」单选组（关闭/本地/LLM/本地+LLM）。
- 增补「本地纠错 vs LLM 纠错 vs 联合」差异与选择建议，强调 LLM fail-open 与去尾符号为最后一步。
