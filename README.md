# Linux Voice Input

一个面向 Ubuntu/X11 桌面的语音输入小工具。

通过 Ubuntu Desktop 自定义快捷键触发：第一次触发开始录音，第二次触发停止录音、调用 SiliconFlow 语音识别接口，并把识别结果粘贴到当前输入框。

## 演示

![Linux Voice Input Demo](assets/demo.gif)

## 设计方案与优势

本项目没有实现一个完整的 Linux 输入法引擎，也没有常驻抢焦点的 UI，而是采用：

```text
Ubuntu 自定义快捷键 -> 单实例 toggle 进程 -> 录音 -> 语音识别 API -> 剪贴板 + Ctrl+V 粘贴
```

这个方案的灵感来自下面这条简单命令：

```bash
echo -n "测试输入" | xclip -selection clipboard && xdotool key --clearmodifiers ctrl+v
```

也就是先把文本写入 X11 剪贴板，再模拟一次 `Ctrl+V`，让文本进入当前焦点所在的输入框。本项目只是把其中的"测试输入"替换成语音识别后的文本。

相比传统输入法或完整语音输入法方案，这个方案更轻便：

- **不接入 IBus/Fcitx 输入法框架**：不用实现输入法 engine，不需要处理候选词、预编辑文本、输入上下文等复杂协议。
- **不替换现有输入法**：可以继续使用原来的中文输入法、英文输入、快捷键和输入习惯，语音输入只是一个额外工具。
- **不与原输入法冲突**：最终文本通过剪贴板粘贴进入目标输入框，不依赖当前输入法状态，也不会切换或劫持输入法。
- **安装和配置更简单**：不需要修改系统输入法框架，不需要注册新的输入法，也不需要切换输入源；只要配置一个 Ubuntu 自定义快捷键即可使用。
- **兼容性更直接**：只要目标应用支持标准剪贴板粘贴，通常就能输入；对浏览器、编辑器、聊天软件、IDE 等都比较友好。
- **更容易排查问题**：核心链路是录音、API、剪贴板、粘贴四步，日志里能直接看到每一步状态。

和传统输入法方案的核心差异：

| 维度 | 本项目 | 传统输入法/完整语音输入法 |
| --- | --- | --- |
| 系统集成方式 | 快捷键触发 + 剪贴板粘贴 | 接入 IBus/Fcitx 等输入法框架 |
| 是否替换输入法 | 不替换，只作为辅助工具 | 通常需要作为输入法/输入源启用 |
| 与现有输入法关系 | 不影响原有输入法，互不冲突 | 可能涉及输入法切换、候选框、输入上下文 |
| 实现复杂度 | 低，主要关注录音和识别链路 | 高，需要处理输入法协议和桌面环境差异 |
| 兼容方式 | 依赖目标应用支持粘贴 | 依赖输入法框架和应用输入上下文 |
| 适合场景 | 快速语音输入、辅助输入、个人工具 | 深度系统集成、长期作为主输入法使用 |

因此当前实现更适合定位为一个 **轻量语音输入辅助工具**，而不是替代系统输入法。

## 工作方式

程序本身不抢全局快捷键，而是交给 Ubuntu Desktop 的自定义快捷键启动。

流程：

1. 第一次执行 `linux-voice-input`
   - 检测到没有运行中的实例
   - 记录当前焦点窗口
   - 开始录音
   - 通过桌面通知提示正在录音
   - 监听 Unix Socket 等待停止指令

2. 第二次执行 `linux-voice-input`
   - 检测到已有实例
   - 向已有实例发送 `stop` 指令
   - 自己退出

3. 第一个实例收到 `stop`
   - 停止录音
   - 如果录音小于 1 秒，认为是误触发并忽略
   - 保存临时 WAV
   - 调用 SiliconFlow 语音识别 API
   - 写入剪贴板
   - 激活首次记录的窗口
   - 模拟 `Ctrl+V` 粘贴
   - 退出

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

- `portaudio19-dev`：麦克风录音
- `xclip`：写入 X11 剪贴板
- `xdotool`：获取/激活窗口并模拟 `Ctrl+V`
- `libnotify-bin`：提供 `notify-send` 桌面通知 fallback
- `libglib2.0-bin`：提供 `gdbus`，用于更新和关闭同一条 DBus 通知

当前优先支持 **X11**。Wayland 下 `xclip` / `xdotool` 可能不可用。

## 构建

```bash
go build -o linux-voice-input .
```

## 配置文件

首次运行会自动创建配置文件；如果检测到旧路径 `~/.config/linux-voice-input/config.yaml`，会自动迁移到新路径：

```text
~/.linux-voice-input/config.yaml
```

示例：

```yaml
api:
  key: "sk-xxxxxxxx"
  base_url: "https://api.siliconflow.cn/v1"
  model: "FunAudioLLM/SenseVoiceSmall"
audio:
  sample_rate: 16000
notifications:
  start: true
  success: true
  ignored: true
  record_start_failed: true
  record_stop_failed: true
  save_failed: true
  transcribe_failed: true
  empty_result: true
  paste_failed: true
restore_clipboard: true
```

字段说明：

- `api.key`：SiliconFlow API Key，必须配置
- `api.base_url`：SiliconFlow API 地址
- `api.model`：语音识别模型，默认 `FunAudioLLM/SenseVoiceSmall`
- `audio.sample_rate`：录音采样率，默认 16000
- `notifications.start`：开始录音时是否通知
- `notifications.success`：识别并输入成功后是否通知
- `notifications.ignored`：录音过短被忽略时是否通知
- `notifications.record_start_failed`：录音启动失败时是否通知
- `notifications.record_stop_failed`：录音停止失败时是否通知
- `notifications.save_failed`：保存临时录音失败时是否通知
- `notifications.transcribe_failed`：语音识别失败时是否通知
- `notifications.empty_result`：未识别到文字时是否通知
- `notifications.paste_failed`：粘贴失败时是否通知
- `restore_clipboard`：粘贴后是否恢复原剪贴板内容

## Ubuntu 快捷键配置

打开：

```text
Settings -> Keyboard -> Keyboard Shortcuts -> Custom Shortcuts
```

添加自定义快捷键：

- Name: `Linux Voice Input`
- Command:

```bash
/home/ericwyn/dev/golang/linux-voice-input/linux-voice-input
```

- Shortcut: 例如 `Alt+Shift+G`

使用方式：

1. 把光标放到目标输入框
2. 按一次快捷键开始录音
3. 再按一次快捷键停止录音并自动输入

## 桌面通知

程序优先通过 DBus 发送桌面通知，并复用同一个 notification id 更新同一条通知，避免刷屏；如果 DBus 不可用，会 fallback 到 `notify-send`。

常见通知状态：

- `录音中`
- `识别中`
- `已输入：...`
- `录音过短，已忽略`
- `识别失败`
- `粘贴失败`

其中 `录音中` 和 `识别中` 是中间状态，会更新同一条通知；成功、失败、忽略等结束状态会继续更新同一条通知，并在 3 秒后自动关闭。

## 日志

程序会记录运行日志到配置文件所在的同一个目录：

```text
~/.linux-voice-input/
```

当天日志文件：

```text
~/.linux-voice-input/run-YYYY-MM-DD.log
```

便捷入口：

```text
~/.linux-voice-input/run.log
```

查看实时日志：

```bash
tail -f ~/.linux-voice-input/run.log
```

日志保留策略：

- 只保留最近 3 天的 `run-*.log`
- 程序启动时会自动清理更早的日志

## 临时音频文件

录音临时文件路径：

```text
/tmp/linux-voice-input.wav
```

清理策略：

- 正常流程下，识别结束后会自动删除该 WAV 文件
- 如果程序异常退出，可能残留这个临时文件
- 下次运行会覆盖同名文件，不会无限堆积历史 WAV

## 误触发保护

录音时长小于 1 秒时，程序认为是误触发：

- 不保存 WAV
- 不调用语音识别 API
- 不粘贴内容
- 显示通知：`录音过短，已忽略`

## 排查问题

如果没有自动输入：

1. 确认当前是 X11 会话
2. 确认已安装 `xclip` 和 `xdotool`
3. 查看日志：

```bash
tail -f ~/.linux-voice-input/run.log
```

4. 手动测试粘贴链路：

```bash
echo -n "测试输入" | xclip -selection clipboard && xdotool key --clearmodifiers ctrl+v
```

如果识别失败：

1. 检查 `~/.linux-voice-input/config.yaml` 中的 `api.key`
2. 检查网络是否能访问 `https://api.siliconflow.cn`
3. 查看日志中的 API 错误信息
