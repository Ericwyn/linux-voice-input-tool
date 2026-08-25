# 本地中文纠错服务与托盘开关方案概述

## 背景

当前项目默认使用本地 SenseVoiceSmall 完成语音识别。模型对普通中文有较好效果，但仍会出现三类需要在最终粘贴前处理的错误：

1. 等长错别字或同音字，例如“多现成”应为“多线程”。
2. 专业名词和英文音译，例如“瑞迪斯”应为 `Redis`。
3. 少字、多字等变长错误，例如“请打开浏览”应为“请打开浏览器”。

项目工作区中已经有一个用户词典实现，可以精确替换固定词语，但它直接由 `session.Controller` 持有，尚未形成统一的服务接口，也不能挂载不同的神经网络策略。需求希望先对两个小型本地模型做可重复评测，再把选中的策略接入最终识别链路，并在 system tray 增加一个用户可即时切换的开关。

## 当前现状

最终文本链路为：

```text
录音停止
  -> engine.Session.Finish
  -> session.Controller.transcribe
  -> corrector.Corrector（可选的精确词典）
  -> input.PasteText
```

关键现状：

1. `corrector/corrector.go` 只实现热重载精确词典，没有通用策略接口。
2. `config.CorrectionsConfig` 只有 `enabled` 和 `dict_path`。
3. `session.Controller.Reload` 会重建词典纠错器，同时也会重建识别引擎；不适合托盘开关每次都触发一次重模型 reload。
4. `ui/tray.go` 有热键开关和识别引擎选择，没有纠错开关。
5. 项目已经随发行包携带 `libonnxruntime.so`，可以复用 ONNX Runtime 加载 BERT MLM，而无需把 PyTorch/Python 作为产品运行依赖。
6. 当前没有独立的文本纠错 CLI，无法对一句或多句输入做模型横向评测。

## 目标

1. 新增进程内 `CorrectionService`，统一管理多个有序纠错策略，并允许运行时启停。
2. 首期实现两种模型策略：`macbert-mdcspell-v2` 与 `macbert4csc`；两者共享一个通用 BERT MLM ONNX 推理实现。
3. 保留用户精确词典，并固定在模型策略之后执行，使用户规则拥有最终决定权。
4. 新增本地评测命令，支持命令行参数、标准输入多行和 JSONL case 文件，输出每个模型的结果与耗时。
5. 先用单元测试验证服务编排、失败透传和 tokenizer，再用真实模型做本机评测。
6. 在 system tray 增加“启用语音纠错”复选框；点击后即时生效并持久化，不重载 ASR 模型。
7. 模型缺失、加载失败或推理失败时始终返回原始识别文本，不影响录音和粘贴。

## 非目标

1. 首期不新增 HTTP/RPC 端口。这里的“服务”是进程内 service layer；接口允许未来替换为本地 sidecar 或远程实现。
2. 首期不训练、微调模型，也不构造 SenseVoice 专属训练语料。
3. 首期模型只做等长中文字符替换，不解决插入、删除和整句语法重写。
4. 不对录音中的预览文本执行纠错，只处理 `Finish` 返回的最终文本。
5. 不自动修改 ASCII 单词、数字、标点和单字符之外的 WordPiece。
6. 不把两个神经模型串行执行；运行时只选择一个模型，评测命令负责横向对比两个模型。

## 已确认决策

1. Top 2 模型为 [Macropodus/macbert4mdcspell_v2](https://huggingface.co/Macropodus/macbert4mdcspell_v2) 和 [shibing624/macbert4csc-base-chinese](https://huggingface.co/shibing624/macbert4csc-base-chinese)。
2. 纠错服务采用进程内接口，不新增独立 HTTP 服务。
3. 产品运行时使用 ONNX Runtime；Python 只用于开发期下载、导出、量化和基准核对。
4. 默认模型策略先设为 `macbert-mdcspell-v2`，真实评测后若过纠率或延迟更差，则切换为 `macbert4csc`。
5. 最终顺序固定为“模型纠错 -> 用户词典 -> 粘贴”。
6. 单一托盘开关控制整个纠错服务；关闭后模型和词典均不执行。
7. 新旧配置兼容，功能默认关闭。

## 核心设计

### 服务与策略接口

```go
type Strategy interface {
    Name() string
    Apply(ctx context.Context, text string) (string, error)
    Close() error
}

type Result struct {
    Text     string
    Applied  []string
    Failures []Failure
    Duration time.Duration
}

type Service struct {
    enabled    bool
    strategies []Strategy
}
```

`Service.Correct` 按配置顺序执行策略。单个策略失败时保留进入该策略前的文本，记录错误并继续执行后续策略，因此模型失败后词典仍然可用。

### 策略组合

```text
CorrectionService
  -> BertMLMStrategy（运行时只配置一个）
       -> macbert-mdcspell-v2/model.int8.onnx
       或 macbert4csc/model.int8.onnx
  -> DictionaryStrategy（可选，最后执行）
```

两种 Top 2 模型都是 `BertForMaskedLM`、相同中文 MacBERT 词表和相同输入输出协议，因此不需要写两套推理代码，只需要两份模型描述：名称、模型路径、词表路径及默认阈值。

### BERT MLM 推理保护

```text
原文
  -> BasicTokenizer + WordPiece
  -> input_ids / attention_mask / token_type_ids
  -> ONNX logits [1, sequence_length, vocab_size]
  -> 只考察“原文单个汉字 <-> 预测单个汉字”的位置
  -> 同时满足 probability >= threshold 与 probability-original >= min_margin
  -> 写回原 rune 位置
```

保护规则：

1. `[CLS]`、`[SEP]`、标点、数字、ASCII、URL 和多 piece token 不修改。
2. 模型输出必须是单个汉字。
3. 默认最大 128 token；首期只处理该窗口内安全映射到原文的汉字，超出部分保持原文。按中文标点分段作为后续优化。
4. 默认高阈值，优先降低过纠而不是追求召回。
5. ONNX 异常、超时和模型缺失全部 fail-open。

### 托盘即时切换

新增菜单项：

```text
启用热键监听
启用语音纠错
---
sensevoice
sherpa
whisper
```

点击行为：

```text
tray.toggleCorrections
  -> 更新内存 cfg.Corrections.Enabled
  -> controller.SetCorrectionsEnabled(enabled)
  -> config.Save
  -> 更新 checkbox
```

该路径不调用 `Controller.Reload`，因此不会卸载或重新加载 SenseVoice。模型策略自身懒加载，第一次真正纠错时才创建 ONNX session。

### 本地评测入口

新增 `cmd/correction-eval`：

```bash
go run ./cmd/correction-eval \
  --models macbert-mdcspell-v2,macbert4csc \
  --text "少先队员因该为老人让坐"

printf '%s\n' \
  '少先队员因该为老人让坐' \
  '今天的天气很好' \
  | go run ./cmd/correction-eval --models macbert-mdcspell-v2,macbert4csc

go run ./cmd/correction-eval --cases docs/correction-eval.example.jsonl
```

JSONL 格式：

```json
{"input":"少先队员因该为老人让坐","expected":"少先队员应该为老人让座"}
{"input":"今天的天气很好","expected":"今天的天气很好"}
```

报告至少包含模型名、输入、输出、期望、是否命中、是否误改和耗时。没有 `expected` 时只展示模型输出。

## 配置设计

```yaml
corrections:
  enabled: false
  strategy: macbert-mdcspell-v2
  model_dir: ""
  runtime_path: ""
  confidence_threshold: 0.90
  min_margin: 0.20
  max_length: 128
  timeout_ms: 1500
  num_threads: 4
  dict_path: ""
```

字段规则：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 托盘总开关，兼容旧配置 |
| `strategy` | `macbert-mdcspell-v2` | 当前神经模型；空值表示只运行词典 |
| `model_dir` | `~/.linux-voice-input/models/correction` | 两个模型子目录的根目录 |
| `runtime_path` | 空 | 优先使用可执行文件旁 `lib/libonnxruntime.so`；开发构建可复用进程已加载的运行库 |
| `confidence_threshold` | `0.90` | 候选字符最低概率 |
| `min_margin` | `0.20` | 候选概率相对原字符概率的最低增量 |
| `max_length` | `128` | 单次模型最大 token 数 |
| `timeout_ms` | `1500` | 单次最终纠错软超时 |
| `num_threads` | `4` | ONNX Runtime CPU intra-op 线程数 |
| `dict_path` | 默认词典路径 | 精确词典，存在时最后执行 |

YAML 反序列化继续以 `DefaultConfig` 为底，因此旧配置缺少新增字段时自动获得默认值。

## 实现方案

1. `corrector/service.go`：策略接口、服务编排、运行时开关、fail-open 结果。
2. `corrector/corrector.go`：现有词典实现适配 `Strategy`。
3. `corrector/onnxbert/tokenizer.go`：BERT BasicTokenizer/WordPiece 与原文 rune 映射。
4. `corrector/onnxbert/strategy.go`：ONNX session 懒加载、推理、阈值和字符保护。
5. `corrector/factory.go`：根据 `CorrectionsConfig` 建立“一个模型 + 词典”的有序服务。
6. `cmd/correction-eval/main.go`：一句、多行、JSONL 的本地横向评测。
7. `scripts/correction/prepare_models.py`：下载 Top 2、导出 ONNX、动态 INT8 量化并保留 tokenizer 文件。
8. `session/session.go`：持有 `CorrectionService`；最终识别后调用；增加不重建 ASR 的即时启停方法。
9. `ui/tray.go`、`ui/app.go`：新增托盘 checkbox、持久化和 reload 后状态同步。
10. `config/config.go`、README：新增配置与使用说明。
11. `models/models.go`：注册两个项目 Release 模型包；CLI 与托盘按当前启用策略自动检查、下载并解压。

## 兼容与降级

1. `enabled: false` 时不加载或调用模型，现有行为不变。
2. 旧配置只有 `enabled`/`dict_path` 时，新增字段取默认值。
3. 模型文件缺失时返回原文并写日志；精确词典继续执行。
4. ONNX Runtime 不可用时返回原文，不导致 daemon 退出。
5. 用户可从托盘一键关闭，也可把 `strategy` 置空保留纯词典模式。
6. 回滚代码不会产生数据迁移问题；新增 YAML 字段会被旧版本忽略。

## 可观测性

每次启用后的最终纠错记录：策略名称、总耗时、是否改写、改写前后文本和错误摘要。模型加载记录模型路径、运行时版本和一次性加载耗时。评测 CLI 输出逐 case 延迟和汇总的正确数、误改数、平均/p95 延迟。

## 测试计划

1. 服务单测：关闭不调用、策略顺序、单策略失败继续、运行时切换、关闭资源。
2. 词典回归：现有热重载、最长匹配和不级联测试保持通过。
3. tokenizer 单测：中文、标点、ASCII、WordPiece、未知字符、原文 rune 映射。
4. 模型策略单测：通过假 ONNX runner 验证阈值、margin、只改单汉字和 fail-open。
5. 真实模型本地评测：Top 2 使用同一组错误句和正确句，记录结果、加载时间、逐句延迟与过纠。
6. session 集成测试：模型在词典前、禁用后原文透传、错误不影响粘贴结果。
7. UI 行为测试：托盘初始勾选状态、点击持久化、reload 后刷新，不触发 ASR reload。
8. 全量回归：`go test ./...`、`go vet ./...`、构建主程序和评测命令。

## 上线与回滚

功能默认关闭。首期由开发者通过评测命令选择模型，再手工从托盘启用。模型加载或推理出现问题时可即时取消勾选；无需重启、数据迁移或回滚 ASR。代码回滚只会忽略新增配置字段和模型目录。

## 建议实施顺序

1. 先写服务、tokenizer 和模型 runner 单测，并观察预期失败。
2. 实现最小服务与两模型通用 ONNX 策略，使单测通过。
3. 准备 Top 2 模型并运行本地评测，基于结果确定默认策略和阈值。
4. 接入 `session.Controller` 最终文本链路。
5. 增加托盘开关与配置持久化。
6. 完成 README、全量回归和手动托盘验证。

## 实施与本地验证结果（2026-08-25）

### Test-first 记录

本轮按“先测试、后实现”执行，分别观察到以下预期失败，再补最小实现使其通过：

1. `corrector/service_test.go`：`NewService` 尚不存在。
2. `corrector/onnxbert/*_test.go`：tokenizer、logits runner 和策略类型尚不存在。
3. `config/corrections_test.go` / `corrector/factory_test.go`：新配置字段与工厂尚不存在。
4. `cmd/correction-eval/main_test.go`：JSONL、多行输入和统计函数尚不存在。
5. `session/correction_test.go`：Controller 尚未持有服务或提供即时开关。
6. `ui/corrections_test.go`：托盘开关与持久化函数尚不存在。
7. `main_correction_test.go`：`--test` 尚未调用纠错服务。
8. `doctor/corrections_test.go`：环境检查尚不能报告纠错模型状态。

实现后以上测试均通过；`go test ./...`、`go vet ./...` 和纠错相关包的 race detector 均已完成且通过。

### 模型准备与运行时兼容

两个 checkpoint 均成功完成以下链路：

```text
Hugging Face PyTorch checkpoint
  -> torch.onnx.export（opset 17，动态 sequence）
  -> ONNX Runtime dynamic INT8 quantization
  -> Python ONNX shape 验证
  -> 纯 Go onnxruntime_go 推理
```

结果：

| 模型 | INT8 ONNX | Python ORT 验证前向 | Go 首次加载+首句 | Go 加载后单句 |
| --- | ---: | ---: | ---: | ---: |
| MDCSpell-v2 | 119,967,633 bytes（114.4 MiB） | 8.21ms | 约 459ms | 约 6–13ms |
| MacBERT4CSC | 约 114.4 MiB | 5.89ms | 约 433ms | 约 6–12ms |

第一次 Go 实测发现最新版 `onnxruntime_go` 请求 C API 29，而项目发行包的 ONNX Runtime 1.27.0 最高提供 API 27。最终把 wrapper 固定为 `github.com/yalue/onnxruntime_go v1.31.0`（请求 API 26），并已用项目自带 `dist/linux-voice-input/lib/libonnxruntime.so` 完整跑通。API 26 是 1.27 运行库支持的向后兼容接口。

### Top 2 横向结果

使用 `docs/correction-eval.example.jsonl` 的 15 条小样本，覆盖普通同音错字、ASR 风格错误、技术词、正确句和“现成/换成”等歧义正确句。统一阈值 `0.90`、margin `0.20`：

| 模型 | 完全匹配 | 正确/歧义句误改 | 结论 |
| --- | ---: | ---: | --- |
| MDCSpell-v2 | 12/15 | 0 | 作为默认策略 |
| MacBERT4CSC | 11/15 | 0 | 保留为可切换基准 |

两者都没有解决“缓存/缓成”领域词和包含多个 ASR 错误的复杂句，验证了用户精确词典仍需作为最后一层。将阈值降到 `0.75`、margin `0.10` 没有提高 MDCSpell 召回，MacBERT 反而把“缓成”改成了仍然错误的“缓程”，因此保留高阈值默认值。

### Release 自动下载

两个已验证模型已作为独立 `tar.bz2` 上传到项目 Release `correction-models-int8-v1`：

- `macbert-mdcspell-v2-int8-onnx.tar.bz2`
- `macbert4csc-int8-onnx.tar.bz2`

两个直链均已通过 HTTP 200 检查，并使用项目自身的 `DownloadMissing` 完整下载、解压和必需文件检查；两模型下载集成测试总耗时约 46 秒。`--download-models` 和托盘“下载模型”现在都会组合“当前识别引擎模型 + 已启用的当前纠错策略模型 + 运行库”。whisper 虽无本地 ASR 模型，启用纠错时仍会下载纠错模型。

## 风险与待确认问题

### 风险

1. `macbert-mdcspell-v2` 没有上游官方 ONNX；项目 Release 中的版本由本项目脚本导出和量化，因此升级上游 checkpoint 时必须重新转换和验证。
2. 两个模型都属于等长 CSC，无法解决插入、删除和英文生成；该能力留给后续 T5/FastCorrect 策略。
3. 模型在通用数据集上的指标不能代表 SenseVoice 输出，最终默认策略必须以本地语音错误集和正确句过纠率为准。
4. ONNX Runtime wrapper 必须保持在兼容项目运行库的版本；已固定 v1.31.0，并由真实模型 smoke test 覆盖。升级 wrapper 或 sherpa-onnx 运行库时需重新跑评测命令。

### 非阻塞待确认

1. 真实语音错误样本目前不足，本轮使用公开常见错字句与正确句做工程通路验证，不把小样本结果视作最终模型质量结论。
