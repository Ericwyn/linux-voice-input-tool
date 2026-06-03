# Linux Voice Input

一个面向 Ubuntu/X11 桌面的语音输入小工具。

通过 Ubuntu Desktop 自定义快捷键触发：第一次触发开始录音，第二次触发停止录音、调用 SiliconFlow 语音识别接口，并把识别结果粘贴到当前输入框。

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
