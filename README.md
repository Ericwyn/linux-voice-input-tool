# Linux Voice Input

一个面向 Ubuntu/X11 桌面的轻量语音输入工具。

通过 Ubuntu Desktop 自定义快捷键触发：第一次触发开始录音，第二次触发停止录音并识别，把识别结果粘贴到当前输入框。

支持两种识别引擎：
- **sherpa（推荐）**：本地三模型 pipeline，完全离线，边录边识别
- **whisper**：在线 API（SiliconFlow），需要联网

## 识别流程（sherpa 引擎）

```text
录音中：流式 ASR 实时预览（通知显示）
    ↓ 停止录音
离线大模型 ASR 精确识别
    ↓
CT-Transformer 标点恢复
    ↓
剪贴板 + Ctrl+V 粘贴
```

三个模型各司其职：

| 阶段 | 模型 | 作用 |
| --- | --- | --- |
| 实时预览 | Streaming Paraformer | 录音中实时显示部分识别结果 |
| 精确识别 | Paraformer-large (离线) | 停止后对完整音频做高准确率识别 |
| 标点恢复 | CT-Transformer | 对识别结果添加标点符号 |

## 系统依赖

Ubuntu/Debian：

```bash
sudo apt install -y \
  portaudio19-dev \
  xclip \
  xdotool \
  libnotify-bin \
  libglib2.0-bin
```

依赖说明：

- `portaudio19-dev`：麦克风录音（编译时需要）
- `xclip`：写入 X11 剪贴板
- `xdotool`：获取/激活窗口并模拟 `Ctrl+V`
- `libnotify-bin`：提供 `notify-send` 桌面通知 fallback
- `libglib2.0-bin`：提供 `gdbus`，用于更新和关闭同一条 DBus 通知

当前优先支持 **X11**。Wayland 下 `xclip` / `xdotool` 可能不可用。

## 构建

```bash
go build -o linux-voice-input .
```

需要 Go 1.22+ 和 CGO（sherpa-onnx 依赖 C 共享库，通过 Go 模块自动引入）。

## 模型下载

使用 sherpa 引擎需要下载三个模型，存放在 `~/.linux-voice-input/models/` 目录下：

```bash
mkdir -p ~/.linux-voice-input/models
cd ~/.linux-voice-input/models

# 1. 流式 ASR 模型（实时预览用，中英双语）
wget https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-paraformer-bilingual-zh-en.tar.bz2
tar -xjf sherpa-onnx-streaming-paraformer-bilingual-zh-en.tar.bz2
rm sherpa-onnx-streaming-paraformer-bilingual-zh-en.tar.bz2

# 2. 离线 ASR 大模型（精确识别用，中文）
wget https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-paraformer-zh-2023-09-14.tar.bz2
tar -xjf sherpa-onnx-paraformer-zh-2023-09-14.tar.bz2
rm sherpa-onnx-paraformer-zh-2023-09-14.tar.bz2

# 3. 标点恢复模型（中英文标点）
wget https://github.com/k2-fsa/sherpa-onnx/releases/download/punctuation-models/sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12.tar.bz2
tar -xjf sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12.tar.bz2
rm sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12.tar.bz2
```

下载完成后目录结构：

```
~/.linux-voice-input/models/
├── sherpa-onnx-streaming-paraformer-bilingual-zh-en/   (~30MB)
│   ├── encoder.int8.onnx
│   ├── decoder.int8.onnx
│   └── tokens.txt
├── sherpa-onnx-paraformer-zh-2023-09-14/               (~220MB)
│   ├── model.int8.onnx
│   └── tokens.txt
└── sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12/  (~280MB)
    └── model.onnx
```

模型来源：
- 流式模型基于阿里 FunASR Paraformer 在线版，由 [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) 项目转换
- 离线模型基于阿里 FunASR Paraformer-large，准确率与在线 API 相当
- 标点模型基于阿里 FunASR CT-Transformer，支持中英文标点恢复

## 配置文件

首次运行会自动创建配置文件：

```text
~/.linux-voice-input/config.yaml
```

### 方式一：本地识别（推荐，使用 sherpa-onnx）

只需设置 `api.engine: sherpa`，其余使用默认值即可（默认指向上面下载的模型路径）：

```yaml
api:
  engine: "sherpa"
audio:
  sample_rate: 16000
restore_clipboard: true
```

完整配置（一般不需要手动填写，程序有默认值）：

```yaml
api:
  engine: "sherpa"
sherpa:
  encoder: "/home/<user>/.linux-voice-input/models/sherpa-onnx-streaming-paraformer-bilingual-zh-en/encoder.int8.onnx"
  decoder: "/home/<user>/.linux-voice-input/models/sherpa-onnx-streaming-paraformer-bilingual-zh-en/decoder.int8.onnx"
  tokens: "/home/<user>/.linux-voice-input/models/sherpa-onnx-streaming-paraformer-bilingual-zh-en/tokens.txt"
  offline_model: "/home/<user>/.linux-voice-input/models/sherpa-onnx-paraformer-zh-2023-09-14/model.int8.onnx"
  offline_tokens: "/home/<user>/.linux-voice-input/models/sherpa-onnx-paraformer-zh-2023-09-14/tokens.txt"
  punct_model: "/home/<user>/.linux-voice-input/models/sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12/model.onnx"
  num_threads: 4
  rule1_min_trailing_silence: 2.4
  rule2_min_trailing_silence: 1.2
audio:
  sample_rate: 16000
restore_clipboard: true
```

### 方式二：在线识别（使用 SiliconFlow API）

```yaml
api:
  engine: "whisper"
  key: "sk-xxxxxxxx"
  base_url: "https://api.siliconflow.cn/v1"
  model: "FunAudioLLM/SenseVoiceSmall"
audio:
  sample_rate: 16000
restore_clipboard: true
```

### 配置字段说明

| 字段 | 说明 |
| --- | --- |
| `api.engine` | 识别引擎：`"sherpa"`（本地，推荐）或 `"whisper"`（在线） |
| `api.key` | SiliconFlow API Key，whisper 引擎必须配置 |
| `api.base_url` | SiliconFlow API 地址 |
| `api.model` | 在线识别模型，默认 `FunAudioLLM/SenseVoiceSmall` |
| `sherpa.encoder` | 流式 Paraformer encoder 模型路径 |
| `sherpa.decoder` | 流式 Paraformer decoder 模型路径 |
| `sherpa.tokens` | 流式模型 tokens 文件路径 |
| `sherpa.offline_model` | 离线 Paraformer-large 模型路径 |
| `sherpa.offline_tokens` | 离线模型 tokens 文件路径 |
| `sherpa.punct_model` | CT-Transformer 标点模型路径 |
| `sherpa.num_threads` | 推理线程数，默认 4 |
| `sherpa.rule1_min_trailing_silence` | 较长静音后结束一段话（秒），默认 2.4 |
| `sherpa.rule2_min_trailing_silence` | 较短静音后结束一段话（秒），默认 1.2 |
| `audio.sample_rate` | 录音采样率，默认 16000 |
| `restore_clipboard` | 粘贴后是否恢复原剪贴板内容 |

## 测试模式

使用 `--test` 参数启动测试模式，自动录音 3 秒并识别，方便验证配置是否正确：

```bash
./linux-voice-input --test
```

输出示例：

```
[TEST] 开始录音，持续 3 秒...
[TEST] 录音结束
[TEST] 保存录音到: /home/user/.linux-voice-input/test-temp.wav
[TEST] 开始识别...
[TEST] 流式识别结果 (耗时 15ms): 我们在测试语音识别
[TEST] 使用离线大模型重新识别...
[TEST] 离线模型识别结果 (耗时 680ms): 我们在测试语音识别
[TEST] 添加标点...
[TEST] 加标点后 (耗时 95ms): 我们在测试语音识别。
[TEST] 识别结果: 我们在测试语音识别。
```

## Ubuntu 快捷键配置

打开：

```text
Settings -> Keyboard -> Keyboard Shortcuts -> Custom Shortcuts
```

添加自定义快捷键：

- Name: `Linux Voice Input`
- Command: `/path/to/linux-voice-input`
- Shortcut: 例如 `Alt+Shift+G`

使用方式：

1. 把光标放到目标输入框
2. 按一次快捷键开始录音
3. 再按一次快捷键停止录音并自动输入

## 桌面通知

使用 sherpa 引擎时，录音过程中通知会持续更新显示实时识别文字；最终结果通知 3 秒后自动关闭。

常见通知状态：

- `录音中: <实时识别文字>`
- `已输入：<最终结果>`
- `录音过短，已忽略`
- `识别失败`

## 日志

日志目录：`~/.linux-voice-input/`

```bash
tail -f ~/.linux-voice-input/run.log
```

日志包含配置信息、各模型处理耗时、识别结果等，方便排查问题。自动保留最近 3 天。

## 排查问题

如果没有自动输入：

1. 确认当前是 X11 会话
2. 确认已安装 `xclip` 和 `xdotool`
3. 查看日志：`tail -f ~/.linux-voice-input/run.log`
4. 手动测试粘贴链路：

```bash
echo -n "测试输入" | xclip -selection clipboard && xdotool key --clearmodifiers ctrl+v
```

如果识别为空：

1. 运行 `./linux-voice-input --test` 验证模型是否正常
2. 检查模型文件是否完整下载
3. 检查日志中的错误信息

## 误触发保护

录音时长小于 1 秒时忽略，不识别、不粘贴、通知提示"录音过短，已忽略"。
