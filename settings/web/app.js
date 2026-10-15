"use strict";

// Token is carried in the URL (?token=...) by the daemon when it opens the
// browser. Every API call sends it back; the server checks it.
const TOKEN = new URLSearchParams(location.search).get("token") || "";

// HINTS describes every field by its dot-path: a Chinese label, an optional
// control-type override (enum | password | textarea | json), enum options, and
// a one-line description. Anything not listed still auto-renders from its value
// type, but the goal is that every real config field has a label + desc.
const HINTS = {
  // ---- 常规 ----
  "engine":                 { label: "识别引擎", type: "enum", options: ["sensevoice", "sherpa", "whisper"], desc: "全局语音识别引擎：sensevoice 本地默认 / sherpa 本地 / whisper 在线" },
  "hotkey":                 { label: "全局热键", desc: "触发录音开关的组合键，如 alt+shift+d（修饰键：ctrl/alt/shift/super）" },
  "hotkey_enabled":         { label: "启用全局热键", desc: "守护进程启动时是否监听全局热键" },
  "restore_clipboard":      { label: "恢复剪贴板", desc: "粘贴后尽量恢复原剪贴板内容；旧剪贴板无法完整读取时仍优先输入识别文字" },
  "remove_trailing_symbols":{ label: "去除末尾符号", desc: "粘贴前去掉识别结果末尾的标点、符号与空格（不影响句中标点，始终是最后一步）" },
  "audio.sample_rate":      { label: "采样率", desc: "录音采样率，默认 16000" },

  // ---- 模型: SenseVoice ----
  "sensevoice.model_dir":   { label: "模型目录", desc: "SenseVoice 模型所在目录，model/tokens 路径由它自动推导" },
  "sensevoice.model":       { label: "模型文件", desc: "SenseVoice 模型路径（通常由模型目录自动推导，无需手填）" },
  "sensevoice.tokens":      { label: "词表文件", desc: "SenseVoice tokens.txt（通常自动推导）" },
  "sensevoice.language":    { label: "识别语种", type: "enum", options: ["auto", "zh", "en", "yue", "ja", "ko"], desc: "auto 自动检测中/粤/英/日/韩" },
  "sensevoice.use_itn":     { label: "逆文本正则化", desc: "开启后输出带标点并做数字规范化" },
  "sensevoice.num_threads": { label: "推理线程数", desc: "CPU 推理线程数，默认 4" },
  "sensevoice.preview_interval_ms":     { label: "预览基准间隔(ms)", desc: "录音时实时预览的基准重跑间隔；填 0 关闭实时预览" },
  "sensevoice.preview_max_interval_ms": { label: "预览间隔上限(ms)", desc: "自适应退避的间隔上限；填 0 不封顶" },
  "sensevoice.preview_backoff_factor":  { label: "预览退避系数", desc: "下次预览的空闲时间 = 上次识别耗时 × 该系数，默认 1.0" },

  // ---- 模型: Sherpa ----
  "sherpa.model_dir":   { label: "模型目录", desc: "Sherpa 多个模型所在目录，其余路径由它自动推导" },
  "sherpa.encoder":     { label: "编码器路径", desc: "流式模型 encoder（通常自动推导，无需手填）" },
  "sherpa.decoder":     { label: "解码器路径", desc: "流式模型 decoder（通常自动推导）" },
  "sherpa.tokens":      { label: "词表路径", desc: "流式模型 tokens.txt（通常自动推导）" },
  "sherpa.num_threads": { label: "推理线程数", desc: "CPU 推理线程数，默认 4" },
  "sherpa.rule1_min_trailing_silence": { label: "断句静音阈值1(秒)", desc: "较长静音后结束一段话，默认 2.4" },
  "sherpa.rule2_min_trailing_silence": { label: "断句静音阈值2(秒)", desc: "较短静音后结束一段话，默认 1.2" },
  "sherpa.offline_model":  { label: "离线模型路径", desc: "非流式识别模型（通常自动推导）" },
  "sherpa.offline_tokens": { label: "离线词表路径", desc: "非流式模型 tokens.txt（通常自动推导）" },
  "sherpa.punct_model":    { label: "标点模型路径", desc: "为识别结果添加标点的模型（通常自动推导）" },

  // ---- 模型: Whisper（在线）----
  "whisper.key":      { label: "API Key", type: "password", desc: "SiliconFlow API Key，使用 whisper 引擎时必填" },
  "whisper.base_url": { label: "接口地址", desc: "在线识别 API 基址" },
  "whisper.model":    { label: "模型", desc: "在线识别模型，默认 FunAudioLLM/SenseVoiceSmall" },

  // ---- 纠错 ----
  "correction_mode":  { label: "纠错模式", type: "enum", options: ["off", "local", "llm", "combined"], desc: "off 关闭 / local 本地 / llm 大模型 / combined 本地+大模型" },

  "corrections.enabled":    { label: "（旧）启用本地纠错", desc: "已被上方“纠错模式”取代，仅用于旧配置迁移，可忽略" },
  "corrections.strategy":   { label: "本地模型策略", type: "enum", options: ["", "macbert-mdcspell-v2", "macbert4csc"], desc: "本地纠错神经模型；空值表示仅用用户词典" },
  "corrections.model_dir":  { label: "纠错模型目录", desc: "本地纠错模型根目录" },
  "corrections.runtime_path": { label: "ONNX 运行库路径", desc: "libonnxruntime 路径；留空自动复用应用自带运行库" },
  "corrections.confidence_threshold": { label: "置信度阈值", desc: "候选字最低概率，默认 0.9；越高越保守、越少误改" },
  "corrections.min_margin": { label: "最低概率增量", desc: "候选字相对原字的最低概率增量，默认 0.2" },
  "corrections.max_length": { label: "最大处理长度", desc: "单次模型最多处理的 token 数，默认 128；超出部分保持原文" },
  "corrections.timeout_ms": { label: "本地纠错超时(ms)", desc: "单次本地纠错软超时，默认 1500" },
  "corrections.num_threads":{ label: "推理线程数", desc: "本地纠错 CPU 推理线程数，默认 4" },
  "corrections.dict_path":  { label: "用户词典路径", desc: "留空则用 ~/.linux-voice-input/corrections.txt" },

  "llm_correction.base_url":        { label: "接口地址", desc: "OpenAI 兼容基址，内部自动拼 /chat/completions" },
  "llm_correction.api_key":         { label: "API Key", type: "password", desc: "作 Authorization: Bearer 发送" },
  "llm_correction.model":           { label: "模型名", desc: "chat 模型名，如 gpt-4o-mini / doubao-seed-..." },
  "llm_correction.prompt_template": { label: "Prompt 模板", type: "textarea", desc: "必须含占位符 {{text}}，粘贴前替换为识别文本" },
  "llm_correction.timeout_ms":      { label: "超时(ms)", desc: "硬超时，超时或任何失败自动回退原文，默认 2000" },
  "llm_correction.extra_params":    { label: "额外请求参数", type: "json", desc: '合并进请求 JSON，如 {"thinking":{"type":"disabled"}}；messages 不可被覆盖' },

  // ---- 通知 ----
  "notifications.start":               { label: "开始/识别中通知", desc: "开始录音、识别中时弹通知" },
  "notifications.success":             { label: "成功通知", desc: "成功输入文字后弹通知" },
  "notifications.ignored":             { label: "忽略通知", desc: "录音过短被忽略时弹通知" },
  "notifications.record_start_failed": { label: "录音启动失败通知", desc: "录音启动失败时弹通知" },
  "notifications.record_stop_failed":  { label: "录音停止失败通知", desc: "录音停止失败时弹通知" },
  "notifications.save_failed":         { label: "保存失败通知", desc: "音频保存失败时弹通知" },
  "notifications.transcribe_failed":   { label: "识别失败通知", desc: "语音识别失败时弹通知" },
  "notifications.empty_result":        { label: "空结果通知", desc: "未识别到内容时弹通知" },
  "notifications.paste_failed":        { label: "粘贴失败通知", desc: "粘贴到目标窗口失败时弹通知" },

  // ---- 统计 ----
  "statistics.enabled": { label: "启用使用统计", desc: "记录成功输入的匿名日汇总（字数/耗时等），不保存识别文本" },
};

const GROUP_LABELS = {
  "sherpa": "Sherpa 引擎",
  "sensevoice": "SenseVoice 引擎",
  "whisper": "Whisper 引擎（在线）",
  "audio": "音频",
  "notifications": "通知开关",
  "corrections": "本地纠错",
  "llm_correction": "LLM 纠错",
  "statistics": "统计设置",
};

// Config sections become the first nav items. Any top-level key not claimed
// here lands in a trailing "其他" section so new keys still appear.
const SECTIONS = [
  { id: "general",       label: "常规", keys: ["engine", "hotkey", "hotkey_enabled", "restore_clipboard", "remove_trailing_symbols", "audio"] },
  { id: "models",        label: "模型", keys: ["sensevoice", "sherpa", "whisper"] },
  { id: "correction",    label: "纠错", keys: ["correction_mode", "corrections", "llm_correction"] },
  { id: "notifications", label: "通知", keys: ["notifications"] },
  { id: "statistics",    label: "统计", keys: ["statistics"] },
];

let originalTree = null;   // last-fetched config
let features = { stats: false, doctor: false, download: false };
const fields = [];          // { path, kind, el, invalidEl }
let activeNav = "general";
let downloadTimer = null;

const $content = document.getElementById("content");
const $nav = document.getElementById("nav");
const $status = document.getElementById("status");
const $search = document.getElementById("search");

function api(path, opts = {}) {
  const headers = Object.assign({ "X-Config-Token": TOKEN }, opts.headers || {});
  return fetch(path, Object.assign({}, opts, { headers }));
}

function setStatus(msg, kind) {
  $status.textContent = msg;
  $status.className = "status " + (kind || "");
  $status.hidden = !msg;
}

function hintFor(path) { return HINTS[path] || {}; }
function labelFor(path, key) { return hintFor(path).label || key; }

// --- init ---

async function init() {
  try {
    features = await (await fetch("/api/features")).json();
  } catch (_) { /* keep defaults (all off) */ }

  try {
    const resp = await api("/api/config");
    if (!resp.ok) throw new Error(await resp.text());
    originalTree = await resp.json();
  } catch (e) {
    $content.innerHTML = '<div class="no-match">加载失败：' + escapeHtml(String(e)) + "</div>";
    return;
  }
  renderAll();
}

function navItems() {
  const items = [];
  const claimed = new Set();
  for (const s of SECTIONS) {
    const present = s.keys.filter((k) => k in originalTree);
    if (present.length) { items.push({ id: s.id, label: s.label, kind: "config", keys: present }); }
    for (const k of s.keys) claimed.add(k);
  }
  const leftover = Object.keys(originalTree).filter((k) => !claimed.has(k));
  if (leftover.length) items.push({ id: "other", label: "其他", kind: "config", keys: leftover });
  if (features.stats) items.push({ id: "stats-view", label: "统计信息", kind: "stats" });
  if (features.doctor || features.download) items.push({ id: "tools", label: "工具", kind: "tools" });
  return items;
}

function renderAll() {
  fields.length = 0;
  $content.innerHTML = "";
  $nav.innerHTML = "";

  const items = navItems();
  if (!items.some((it) => it.id === activeNav)) activeNav = items[0].id;

  for (const it of items) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "nav-item" + (it.id === activeNav ? " active" : "");
    btn.textContent = it.label;
    btn.dataset.nav = it.id;
    btn.addEventListener("click", () => selectNav(it.id));
    $nav.appendChild(btn);

    const panel = document.createElement("section");
    panel.className = "panel";
    panel.dataset.nav = it.id;
    panel.hidden = it.id !== activeNav;

    if (it.kind === "config") renderConfigInto(panel, it.keys, it.label);
    else if (it.kind === "stats") renderStatsInto(panel);
    else if (it.kind === "tools") renderToolsInto(panel);

    $content.appendChild(panel);
  }
}

// --- config rendering ---

function renderConfigInto(panel, keys, sectionLabel) {
  const scalars = [];
  const objects = [];
  for (const key of keys) {
    const val = originalTree[key];
    if (isPlainObject(val) && hintFor(key).type !== "json") objects.push([key, val]);
    else scalars.push([key, val]);
  }
  if (scalars.length) panel.appendChild(renderGroup("", scalars, "", sectionLabel));
  for (const [key, val] of objects) panel.appendChild(renderGroup(key, Object.entries(val), key + "."));
}

function renderGroup(groupKey, entries, prefix, explicitLabel) {
  const group = document.createElement("section");
  group.className = "group";
  const head = document.createElement("div");
  head.className = "group-head";
  const label = explicitLabel || GROUP_LABELS[groupKey] || groupKey;
  head.innerHTML = '<span>' + escapeHtml(label) + '</span>' +
    (prefix ? '<span class="group-key">' + escapeHtml(groupKey) + '</span>' : "");
  group.appendChild(head);
  const body = document.createElement("div");
  body.className = "group-body";
  for (const [key, val] of entries) body.appendChild(renderField(prefix + key, key, val));
  group.appendChild(body);
  return group;
}

function renderField(path, key, value) {
  const h = hintFor(path);
  const field = document.createElement("div");
  field.className = "field";
  field.dataset.path = path;
  field.dataset.search = (labelFor(path, key) + " " + path + " " + (h.desc || "")).toLowerCase();

  const labelBox = document.createElement("div");
  labelBox.className = "field-label";
  labelBox.innerHTML =
    '<span class="field-name">' + escapeHtml(labelFor(path, key)) + '</span>' +
    '<span class="field-key">' + escapeHtml(path) + '</span>' +
    (h.desc ? '<span class="field-desc">' + escapeHtml(h.desc) + '</span>' : "");
  field.appendChild(labelBox);

  const control = document.createElement("div");
  control.className = "field-control";
  control.appendChild(buildControl(path, key, value, h));
  field.appendChild(control);
  return field;
}

function buildControl(path, key, value, h) {
  const type = h.type || inferType(value);

  if (type === "json") {
    const ta = document.createElement("textarea");
    ta.className = "code"; ta.rows = 4;
    ta.value = value == null ? "" : JSON.stringify(value, null, 2);
    const errEl = document.createElement("div"); errEl.className = "hint-err"; errEl.hidden = true;
    const wrap = document.createElement("div"); wrap.appendChild(ta); wrap.appendChild(errEl);
    fields.push({ path, kind: "json", el: ta, invalidEl: errEl });
    return wrap;
  }
  if (typeof value === "boolean" || type === "bool") {
    const sw = document.createElement("label"); sw.className = "switch";
    const input = document.createElement("input"); input.type = "checkbox"; input.checked = !!value;
    const slider = document.createElement("span"); slider.className = "slider";
    sw.appendChild(input); sw.appendChild(slider);
    fields.push({ path, kind: "bool", el: input });
    return sw;
  }
  if (type === "enum") {
    const sel = document.createElement("select");
    for (const opt of h.options) {
      const o = document.createElement("option");
      o.value = opt; o.textContent = opt === "" ? "（空）" : opt;
      if (String(value) === String(opt)) o.selected = true;
      sel.appendChild(o);
    }
    fields.push({ path, kind: "string", el: sel });
    return sel;
  }
  if (type === "number" || typeof value === "number") {
    const input = document.createElement("input");
    input.type = "number"; input.step = "any";
    input.value = value == null ? "" : String(value);
    const errEl = document.createElement("div"); errEl.className = "hint-err"; errEl.hidden = true;
    const wrap = document.createElement("div"); wrap.appendChild(input); wrap.appendChild(errEl);
    fields.push({ path, kind: "number", el: input, invalidEl: errEl });
    return wrap;
  }
  if (type === "password") {
    const input = document.createElement("input");
    input.type = "password"; input.autocomplete = "off";
    input.value = value == null ? "" : String(value);
    fields.push({ path, kind: "string", el: input });
    return input;
  }
  if (type === "textarea") {
    const ta = document.createElement("textarea"); ta.rows = 4;
    ta.value = value == null ? "" : String(value);
    fields.push({ path, kind: "string", el: ta });
    return ta;
  }
  const input = document.createElement("input");
  input.type = "text";
  input.value = value == null ? "" : String(value);
  fields.push({ path, kind: "string", el: input });
  return input;
}

function inferType(value) {
  if (typeof value === "boolean") return "bool";
  if (typeof value === "number") return "number";
  if (isPlainObject(value) || Array.isArray(value)) return "json";
  return "text";
}

// --- stats panel ---

async function renderStatsInto(panel) {
  const group = document.createElement("section");
  group.className = "group";
  group.innerHTML = '<div class="group-head"><span>使用统计</span></div>';
  const body = document.createElement("div");
  body.innerHTML = '<div class="stats-disabled">加载中…</div>';
  group.appendChild(body);
  panel.appendChild(group);

  try {
    const resp = await api("/api/stats");
    if (!resp.ok) throw new Error(await resp.text());
    const data = await resp.json();
    body.innerHTML = "";
    if (!data.enabled) {
      const note = document.createElement("div");
      note.className = "stats-disabled";
      note.textContent = "统计已关闭。可在“统计”配置中开启后开始记录（历史数据保留）。";
      body.appendChild(note);
    }
    for (const period of [data.today, data.recent, data.all]) {
      if (!period) continue;
      body.appendChild(statsPeriodBlock(period));
    }
  } catch (e) {
    body.innerHTML = '<div class="stats-disabled">加载统计失败：' + escapeHtml(String(e)) + "</div>";
  }
}

function statsPeriodBlock(p) {
  const frag = document.createDocumentFragment();
  const title = document.createElement("div");
  title.className = "stats-period-title";
  title.textContent = p.label;
  frag.appendChild(title);
  const grid = document.createElement("div");
  grid.className = "stats-grid";
  const cards = [
    ["成功输入", p.successful_inputs],
    ["字符数", p.characters],
    ["汉字数", p.han_characters],
    ["被纠错次数", p.corrected_inputs],
    ["录音时长", p.recording_duration],
    ["端到端时长", p.input_duration],
  ];
  for (const [label, value] of cards) {
    const card = document.createElement("div");
    card.className = "stat-card";
    card.innerHTML = '<div class="stat-value">' + escapeHtml(String(value)) + '</div>' +
      '<div class="stat-label">' + escapeHtml(label) + '</div>';
    grid.appendChild(card);
  }
  frag.appendChild(grid);
  return frag;
}

// --- tools panel ---

function renderToolsInto(panel) {
  if (features.download) {
    const group = document.createElement("section");
    group.className = "group";
    group.innerHTML = '<div class="group-head"><span>下载模型</span></div>';
    const row = document.createElement("div");
    row.className = "tool-row";
    row.innerHTML = '<span class="tool-desc">下载当前配置所需的识别模型、纠错模型与运行库。</span>';
    const btn = document.createElement("button");
    btn.type = "button"; btn.className = "btn btn-primary"; btn.textContent = "开始下载";
    row.appendChild(btn);
    group.appendChild(row);
    const console = document.createElement("pre");
    console.className = "console"; console.id = "dl-console"; console.hidden = true;
    group.appendChild(console);
    btn.addEventListener("click", () => startDownload(btn, console));
    panel.appendChild(group);
  }

  if (features.doctor) {
    const group = document.createElement("section");
    group.className = "group";
    group.innerHTML = '<div class="group-head"><span>环境检查</span></div>';
    const row = document.createElement("div");
    row.className = "tool-row";
    row.innerHTML = '<span class="tool-desc">检查显示服务器、xdotool、通知服务、模型与运行库是否就绪。</span>';
    const btn = document.createElement("button");
    btn.type = "button"; btn.className = "btn btn-ghost"; btn.textContent = "运行检查";
    row.appendChild(btn);
    group.appendChild(row);
    const list = document.createElement("div");
    list.className = "doctor-list"; list.id = "doctor-list"; list.hidden = true;
    group.appendChild(list);
    btn.addEventListener("click", () => runDoctor(btn, list));
    panel.appendChild(group);
  }
}

async function startDownload(btn, consoleEl) {
  btn.disabled = true;
  consoleEl.hidden = false;
  consoleEl.textContent = "启动中…";
  try {
    await api("/api/download", { method: "POST" });
  } catch (e) {
    consoleEl.textContent = "启动失败：" + String(e);
    btn.disabled = false;
    return;
  }
  if (downloadTimer) clearInterval(downloadTimer);
  downloadTimer = setInterval(async () => {
    try {
      const st = await (await api("/api/download")).json();
      consoleEl.textContent = (st.log || []).join("\n");
      consoleEl.scrollTop = consoleEl.scrollHeight;
      if (!st.running) {
        clearInterval(downloadTimer); downloadTimer = null;
        btn.disabled = false;
        btn.textContent = st.error ? "重试下载" : "重新下载";
      }
    } catch (_) { /* keep polling */ }
  }, 800);
}

async function runDoctor(btn, listEl) {
  btn.disabled = true;
  listEl.hidden = false;
  listEl.innerHTML = '<div class="doctor-detail">检查中…</div>';
  try {
    const items = await (await api("/api/doctor")).json();
    listEl.innerHTML = "";
    for (const it of items) {
      const row = document.createElement("div");
      row.className = "doctor-item";
      row.innerHTML =
        '<span class="doctor-mark ' + (it.ok ? "ok" : "bad") + '">' + (it.ok ? "✓" : "✗") + '</span>' +
        '<span class="doctor-name">' + escapeHtml(it.name) + '</span>' +
        '<span class="doctor-detail">' + escapeHtml(it.detail) + '</span>';
      listEl.appendChild(row);
    }
  } catch (e) {
    listEl.innerHTML = '<div class="doctor-detail">检查失败：' + escapeHtml(String(e)) + "</div>";
  }
  btn.disabled = false;
}

// --- nav ---

function selectNav(id) {
  activeNav = id;
  for (const btn of $nav.querySelectorAll(".nav-item")) {
    btn.classList.toggle("active", btn.dataset.nav === id);
  }
  if ($search.value.trim() === "") {
    for (const panel of $content.querySelectorAll(".panel")) {
      panel.hidden = panel.dataset.nav !== id;
    }
  }
}

// --- saving ---

async function save() {
  const out = structuredClone(originalTree);
  let hasError = false;
  for (const f of fields) {
    if (f.invalidEl) { f.invalidEl.hidden = true; f.el.classList.remove("invalid"); }
    let v;
    if (f.kind === "bool") {
      v = f.el.checked;
    } else if (f.kind === "number") {
      const raw = f.el.value.trim();
      if (raw === "") v = 0;
      else { const n = Number(raw); if (Number.isNaN(n)) { markInvalid(f, "请输入数字"); hasError = true; continue; } v = n; }
    } else if (f.kind === "json") {
      const raw = f.el.value.trim();
      if (raw === "") v = null;
      else { try { v = JSON.parse(raw); } catch (e) { markInvalid(f, "JSON 格式错误：" + e.message); hasError = true; continue; } }
    } else {
      v = f.el.value;
    }
    setPath(out, f.path, v);
  }
  if (hasError) { setStatus("有字段填写有误，请检查标红项", "err"); return; }

  setStatus("保存中…", "");
  try {
    const resp = await api("/api/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(out),
    });
    if (!resp.ok) throw new Error(await resp.text());
    originalTree = out;
    setStatus("已保存并应用 ✓", "ok");
  } catch (e) {
    setStatus("保存失败：" + String(e), "err");
  }
}

function markInvalid(f, msg) {
  f.el.classList.add("invalid");
  if (f.invalidEl) { f.invalidEl.textContent = msg; f.invalidEl.hidden = false; }
}

// --- search (config fields only) ---

function applySearch() {
  const q = $search.value.trim().toLowerCase();
  if (q === "") {
    for (const panel of $content.querySelectorAll(".panel")) panel.hidden = panel.dataset.nav !== activeNav;
    for (const field of $content.querySelectorAll(".field")) field.classList.remove("hidden", "match");
    for (const group of $content.querySelectorAll(".group")) group.style.display = "";
    removeNoMatch();
    return;
  }
  let anyVisible = false;
  // Only config panels have .field; non-config panels (stats/tools) are hidden
  // while searching, and config panels are filtered field-by-field.
  for (const panel of $content.querySelectorAll(".panel")) {
    if (panel.querySelector(".field") === null) { panel.hidden = true; continue; }
    let panelVisible = false;
    for (const field of panel.querySelectorAll(".field")) {
      const hit = field.dataset.search.includes(q);
      field.classList.toggle("hidden", !hit);
      field.classList.toggle("match", hit);
      if (hit) { panelVisible = true; anyVisible = true; }
    }
    for (const group of panel.querySelectorAll(".group")) {
      group.style.display = group.querySelectorAll(".field:not(.hidden)").length ? "" : "none";
    }
    panel.hidden = !panelVisible;
  }
  if (!anyVisible) ensureNoMatch(); else removeNoMatch();
}

function ensureNoMatch() {
  if (document.getElementById("no-match")) return;
  const nm = document.createElement("div");
  nm.id = "no-match"; nm.className = "no-match"; nm.textContent = "没有匹配的配置项";
  $content.appendChild(nm);
}
function removeNoMatch() { const nm = document.getElementById("no-match"); if (nm) nm.remove(); }

// --- helpers ---

function isPlainObject(v) { return v !== null && typeof v === "object" && !Array.isArray(v); }

function setPath(obj, path, value) {
  const parts = path.split(".");
  let cur = obj;
  for (let i = 0; i < parts.length - 1; i++) {
    if (!isPlainObject(cur[parts[i]])) cur[parts[i]] = {};
    cur = cur[parts[i]];
  }
  cur[parts[parts.length - 1]] = value;
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => (
    { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]
  ));
}

// --- wire up ---

document.getElementById("save").addEventListener("click", save);
document.getElementById("reset").addEventListener("click", () => {
  if (originalTree) renderAll();
  $search.value = "";
  applySearch();
  setStatus("已放弃未保存的更改", "");
});
$search.addEventListener("input", applySearch);

init();
