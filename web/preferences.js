'use strict';

const settingsFields = ['out-dir', 'output-format', 'resolution', 'concurrency', 'compatible', 'timeout', 'retries', 'limit', 'ai-protocol', 'ai-base-url', 'ai-model', 'ai-key', 'theme', 'glass'];
const systemDark = matchMedia('(prefers-color-scheme: dark)');
let settingsDirty = false;
let settingsSaving = false;
let settingsCategory = 'download';
let appliedAppearance = '';
let savedForm;
let modelListRequest;
let previousAIProtocol = 'openai';
let draftModelService = '';

function appearance(prefs) {
  const dark = prefs.theme === 'dark' || (prefs.theme === 'system' && systemDark.matches);
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  document.documentElement.dataset.glass = prefs.glass ? 'on' : 'off';
  const key = prefs.theme + '/' + prefs.glass;
  if (nativeDesktop?.appearance && appliedAppearance !== key) {
    appliedAppearance = key;
    nativeDesktop.appearance(prefs.theme, prefs.glass).catch(() => { appliedAppearance = ''; });
  }
}

function savedConfiguration() {
  return {outDir: state.outDir, preferences: state.preferences};
}

function readAISettings() {
  return {protocol: byId('ai-protocol').value, baseURL: byId('ai-base-url').value.trim(), model: byId('ai-model').value.trim()};
}

function sameAIService(first, second) {
  return (first?.protocol || 'openai') === (second?.protocol || 'openai') &&
    (first?.baseURL || '').replace(/\/+$/, '') === (second?.baseURL || '').replace(/\/+$/, '');
}

function modelService(ai) {
  if (!ai?.baseURL) return '';
  let address = ai.baseURL;
  try { address = new URL(address).href; } catch { return ''; }
  const family = ai.protocol === 'anthropic' ? 'anthropic' : 'openai';
  return family + ' ' + address.replace(/\/+$/, '');
}

function matchModelToService() {
  const ai = readAISettings();
  const service = modelService(ai);
  if (service === draftModelService) return;
  const saved = state.preferences?.ai;
  if (draftModelService || !ai.model) {
    byId('ai-model').value = service && service === modelService(saved) ? saved.model : '';
    draftModelService = service;
  }
}

function readSettings() {
  return {
    outDir: byId('out-dir').value,
    preferences: {
      theme: byId('theme').querySelector(':checked').value,
      concurrency: byId('concurrency').valueAsNumber,
      glass: byId('glass').checked,
      ai: readAISettings(),
      download: {
        format: byId('output-format').value,
        maxResolution: Number(byId('resolution').value),
        compatibleMP4: byId('compatible').checked,
        timeoutSeconds: byId('timeout').valueAsNumber,
        retries: byId('retries').valueAsNumber,
        rateLimitKiB: byId('limit').valueAsNumber,
      },
    },
  };
}

function hasSettingsChanges() {
  return !!byId('ai-key').value.trim() || JSON.stringify(readSettings()) !== JSON.stringify(savedForm);
}

function fillSettings(config) {
  const {theme, glass, download, ai} = config.preferences;
  byId('out-dir').value = config.outDir;
  for (const choice of byId('theme').querySelectorAll('input')) choice.checked = choice.value === theme;
  byId('glass').checked = glass;
  byId('output-format').value = download.format || 'mp4';
  byId('resolution').value = download.maxResolution;
  byId('concurrency').value = config.preferences.concurrency ?? 2;
  byId('compatible').checked = download.compatibleMP4;
  byId('timeout').value = download.timeoutSeconds;
  byId('retries').value = download.retries;
  byId('limit').value = download.rateLimitKiB;
  if (!sameAIService(readAISettings(), ai) || byId('ai-key').value) clearModelList();
  byId('ai-protocol').value = ai?.protocol || 'openai';
  previousAIProtocol = byId('ai-protocol').value;
  byId('ai-base-url').value = ai?.baseURL || '';
  byId('ai-model').value = ai?.model || '';
  draftModelService = modelService(ai);
  byId('ai-key').value = '';
}

function localAIService(url) {
  return url.hostname === 'localhost' || url.hostname === '[::1]' || /^127\.\d+\.\d+\.\d+$/.test(url.hostname);
}

function aiAddressError(baseURL) {
  let url;
  try { url = new URL(baseURL); } catch { return '请输入有效的 AI 服务地址。'; }
  if (url.username || url.password || url.search || url.hash) return 'AI 服务地址不能包含凭据、查询参数或片段。';
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && localAIService(url))) return '云服务需使用 HTTPS，本机服务也可使用 HTTP。';
  return '';
}

function missingAIField(ai) {
  if (!ai?.baseURL) return 'ai-base-url';
  if (!ai.model) return byId('ai-model-list').hidden ? 'ai-model' : 'ai-model-list';
  return '';
}

function showModelPicker(show) {
  byId('ai-model-list').hidden = !show;
  byId('ai-model').hidden = show;
  byId('ai-model-label').htmlFor = show ? 'ai-model-list' : 'ai-model';
}

function clearModelList() {
  modelListRequest?.abort();
  modelListRequest = null;
  const list = byId('ai-model-list');
  list.replaceChildren();
  showModelPicker(false);
  display('ai-model-status', '');
  delete byId('ai-model-status').dataset.error;
}

function officialAIAddress(protocol) {
  return protocol === 'anthropic' ? 'https://api.anthropic.com/v1' : 'https://api.openai.com/v1';
}

function switchAIProtocol(event) {
  const previousAddress = officialAIAddress(previousAIProtocol);
  previousAIProtocol = event.target.value;
  const nextAddress = officialAIAddress(previousAIProtocol);
  const field = byId('ai-base-url');
  if (nextAddress === previousAddress) return;
  if (!field.value.trim()) {
    byId('ai-model').value = '';
    return;
  }
  let address;
  try { address = new URL(field.value).href; } catch { return; }
  if (address.replace(/\/+$/, '') === previousAddress) field.value = nextAddress;
}

function syncAIControls() {
  const ai = readAISettings();
  byId('ai-key').placeholder = sameAIService(ai, state.preferences?.ai) && state.aiKeySet ? '本次已设置，填写可替换' : '输入服务商密钥';
  const anthropic = ai.protocol === 'anthropic';
  byId('ai-base-url').placeholder = officialAIAddress(ai.protocol);
  byId('use-official-ai').hidden = !!ai.baseURL;
  const guidance = anthropic ? '填写 Anthropic 或兼容服务提供的基础地址，含版本路径，如 /v1。' : '云服务使用 HTTPS，本地 Ollama 可填 http://127.0.0.1:11434/v1。';
  byId('ai-url-help').textContent = guidance + (isWebDeployment() ? ' 地址由下载服务器访问，在 Docker 中 127.0.0.1 指向容器自身。' : '');
  const button = byId('fetch-ai-models');
  button.disabled = !loaded || !ai.baseURL || settingsSaving || !!modelListRequest;
  button.textContent = modelListRequest ? '正在获取…' : '获取模型列表';
  const list = byId('ai-model-list');
  if (!list.hidden && list.value !== ai.model) {
    list.value = ai.model;
    if (list.selectedIndex < 0) showModelPicker(false);
  }
}

async function fetchAIModels() {
  const ai = readAISettings();
  const addressError = aiAddressError(ai.baseURL);
  if (addressError) {
    byId('ai-model-status').dataset.error = 'true';
    display('ai-model-status', addressError);
    byId('ai-base-url').focus();
    return;
  }
  clearModelList();
  const controller = new AbortController();
  const requestedFromButton = document.activeElement === byId('fetch-ai-models');
  modelListRequest = controller;
  syncAIControls();
  display('ai-model-status', '正在向服务获取模型列表…');
  try {
    const aiKey = byId('ai-key').value.trim();
    const response = await request('/api/ai/models', aiKey ? {ai, aiKey} : {ai}, controller.signal);
    const {models} = await response.json();
    if (controller.signal.aborted) return;
    if (!Array.isArray(models)) throw new Error('服务返回的模型列表格式无效。');
    if (models.length) {
      const list = byId('ai-model-list');
      const placeholder = new Option('选择可用模型', '');
      placeholder.disabled = true;
      const options = document.createDocumentFragment();
      options.append(placeholder);
      const current = byId('ai-model').value.trim();
      if (current && !models.includes(current)) options.append(new Option(current + '（当前设置）', current));
      for (const model of models) options.append(new Option(model, model));
      const manual = new Option('手动输入模型 ID…', '');
      manual.dataset.manual = 'true';
      options.append(manual);
      list.replaceChildren(options);
      if (current) list.value = current;
      else list.selectedIndex = 0;
      // A slow response can arrive after the user starts entering a model by hand.
      if (document.activeElement !== byId('ai-model')) {
        showModelPicker(true);
        if (requestedFromButton && document.activeElement === document.body && list.checkVisibility()) list.focus({preventScroll: true});
      }
      display('ai-model-status', `已获取 ${models.length} 个模型。`);
    } else {
      display('ai-model-status', '服务返回的模型列表为空，可手动填写模型 ID。');
    }
  } catch (error) {
    if (!controller.signal.aborted) {
      byId('ai-model-status').dataset.error = 'true';
      display('ai-model-status', error.message.replace(/[。.!！]+$/, '') + '。也可以手动填写模型 ID。');
    }
  } finally {
    if (modelListRequest === controller) {
      modelListRequest = null;
      syncAIControls();
    }
  }
}

function isAudioFormat(format) {
  return format === 'm4a' || format === 'mp3';
}

function syncFormatControls() {
  const defaults = state.preferences?.download?.format || 'mp4';
  const batchFormat = byId('batch-format');
  const quality = byId('batch-quality');
  batchFormat.options[0].textContent = defaults.toUpperCase() + '（默认）';
  const audio = isAudioFormat(batchFormat.value || defaults);
  if (audio && quality.value !== 'audio') {
    quality.dataset.videoQuality = quality.value;
    quality.value = 'audio';
  } else if (!audio && quality.value === 'audio') {
    quality.value = quality.dataset.videoQuality || '';
  }
  quality.disabled = audio;
  const selected = byId('output-format').value;
  byId('resolution').disabled = isAudioFormat(selected);
  byId('resolution-help').textContent = isAudioFormat(selected) ? '仅保存音频时无需设置分辨率。' : '按画面短边计算，可在添加任务时单独指定。';
  byId('compatible').disabled = selected !== 'mp4';
  byId('compatible-help').textContent = selected === 'mp4' ? '使用 H.264 视频与 AAC 音频，必要时重新编码。' : '选择 MP4 视频时可启用。';
}

function syncSettings() {
  byId('save-settings').disabled = !loaded || !settingsDirty || settingsSaving;
  byId('settings-save-label').textContent = settingsSaving ? '正在保存…' : '保存设置';
  byId('discard-settings').hidden = !settingsDirty;
  byId('discard-settings').disabled = settingsSaving;
  byId('preference-content').inert = !state.preferences;
  const status = settingsSaving ? '正在保存' : settingsDirty ? '有未保存的更改' : loaded ? '当前设置已生效' : '等待下载服务';
  byId('save-state').classList.toggle('modified', settingsDirty);
  if (byId('save-state').textContent !== status) byId('save-state').textContent = status;
  for (const id of ['export-settings', 'import-settings', 'reset-settings']) byId(id).disabled = !loaded || settingsSaving;
  if (!state.preferences) return;
  if (!settingsDirty && !settingsSaving) {
    fillSettings(savedConfiguration());
    savedForm = readSettings();
  }
  syncAIControls();
  appearance(settingsDirty ? readSettings().preferences : state.preferences);
  const cap = state.preferences.download.maxResolution;
  byId('batch-quality').options[0].textContent = (cap ? cap + 'p' : '最佳画质') + '（默认）';
  syncFormatControls();
}

function editSettings() {
  settingsDirty = hasSettingsChanges();
  clearSettingsError();
  syncSettings();
}

function clearSettingsError() {
  display('settings-error', '');
  const invalid = byId('preference-content').querySelector('[aria-invalid]');
  if (invalid) {
    invalid.removeAttribute('aria-invalid');
    invalid.removeAttribute('aria-errormessage');
  }
}

function configureSettingsAccess() {
  const settings = hasPermission('settings.manage');
  const storage = hasPermission('storage.manage');
  const reveal = !isWebDeployment() && hasPermission('storage.reveal');
  for (const tab of document.querySelectorAll('.preference-tabs button')) {
    const category = tab.id.slice(4);
    tab.hidden = category === 'tools' ? !hasPermission('tools.manage') : category === 'storage' ? !(settings || storage || reveal) : !settings;
  }
  byId('clear-cache').closest('.setting').hidden = !storage;
  byId('open-data').closest('.setting').hidden = !reveal;
  byId('glass').closest('.setting').hidden = isWebDeployment();
  for (const id of ['export-settings', 'reset-settings']) byId(id).closest('.setting').hidden = !settings;
  document.querySelector('.save-controls').hidden = !settings;
  const first = document.querySelector('.preference-tabs button:not([hidden])');
  if (first && byId('tab-' + settingsCategory).hidden) showSettingsCategory(first.id.slice(4));
}

function showSettingsCategory(category) {
  if (byId('tab-' + category).hidden) return;
  settingsCategory = category;
  for (const button of document.querySelectorAll('.preference-tabs button')) {
    const selected = button.id === 'tab-' + category;
    button.setAttribute('aria-selected', String(selected));
    button.tabIndex = selected ? 0 : -1;
    byId(button.getAttribute('aria-controls')).hidden = !selected;
  }
  if (category === 'storage' && hasPermission('storage.manage')) refreshCache();
}

function validationError(config) {
  const prefs = config?.preferences;
  const d = prefs?.download;
  const between = (value, min, max) => Number.isInteger(value) && value >= min && value <= max;
  if (typeof config?.outDir !== 'string' || !config.outDir.trim()) return '请填写默认保存位置。';
  if (!['system', 'light', 'dark'].includes(prefs?.theme) || typeof prefs?.glass !== 'boolean') return '外观配置无效。';
  if (!['mp4', 'mkv', 'm4a', 'mp3'].includes(d?.format ?? 'mp4')) return '输出格式无效。';
  if (!between(prefs.concurrency ?? 2, 1, 6)) return '同时下载的任务数需要在 1 到 6 之间。';
  if (![0, 480, 720, 1080, 1440, 2160].includes(d?.maxResolution) || typeof d?.compatibleMP4 !== 'boolean') return '画质配置无效。';
  if (!between(d.timeoutSeconds, 5, 180)) return '连接超时需在 5 到 180 秒之间。';
  if (!between(d.retries, 0, 10)) return '自动重试次数需在 0 到 10 之间。';
  if (!between(d.rateLimitKiB, 0, 1048576)) return '限速需在 0 到 1048576 KiB/s 之间。';
  if (prefs.ai != null) {
    const {baseURL, model, protocol} = prefs.ai;
    if (protocol !== undefined && !['', 'openai', 'openai-responses', 'anthropic'].includes(protocol)) return 'AI 服务协议无效。';
    if (typeof baseURL !== 'string' || typeof model !== 'string') return 'AI 服务配置无效。';
    if (!!baseURL.trim() !== !!model.trim()) return '请同时填写 AI 服务地址和模型名称，或将两项留空。';
    if (baseURL.trim()) return aiAddressError(baseURL);
  }
  return '';
}

async function saveSettings() {
  if (settingsSaving || !settingsDirty || !loaded || !hasPermission('settings.manage')) return;
  const draft = readSettings();
  const config = {...draft, outDir: draft.outDir.trim()};
  const aiKey = byId('ai-key').value.trim();
  const error = validationError(config) || (aiKey && !config.preferences.ai.baseURL ? '请先填写 AI 服务地址和模型名称。' : '');
  if (error) {
    const {baseURL, model} = config.preferences.ai;
    let invalid = byId('preference-content').querySelector('input:invalid');
    if (!invalid && !config.outDir) invalid = byId('out-dir');
    if (!invalid && (model || baseURL || aiKey)) invalid = byId(missingAIField(config.preferences.ai) || 'ai-base-url');
    if (invalid) {
      showSettingsCategory(invalid.closest('[role=tabpanel]').id.slice(6));
      invalid.setAttribute('aria-invalid', 'true');
      invalid.setAttribute('aria-errormessage', 'settings-error');
      invalid.focus();
    }
    display('settings-error', error);
    return;
  }
  settingsSaving = true;
  syncSettings();
  try {
    const response = await request('/api/settings', aiKey ? {...config, aiKey} : config);
    Object.assign(state, await response.json());
    savedForm = {...config, outDir: state.outDir};
    if (byId('out-dir').value === draft.outDir) byId('out-dir').value = state.outDir;
    if (sameAIService(readAISettings(), config.preferences.ai) && byId('ai-key').value.trim() === aiKey) byId('ai-key').value = '';
    settingsDirty = hasSettingsChanges();
    updateDirectory();
    clearSettingsError();
    if (byId('settings-panel').hidden) notify('设置已保存。');
  } catch (error) { display('settings-error', error.message); }
  finally { settingsSaving = false; syncSettings(); }
}

async function chooseFolder(saveImmediately = false) {
  if (!nativeDesktop) {
    setPage(true);
    showSettingsCategory('download');
    byId('out-dir').focus();
    return;
  }
  try {
    const selected = await nativeDesktop.chooseFolder(saveImmediately ? state.outDir : byId('out-dir').value);
    if (!selected) return;
    if (saveImmediately) {
      const previousDir = state.outDir;
      const response = await request('/api/settings', {outDir: selected});
      Object.assign(state, await response.json());
      if (settingsDirty) {
        savedForm.outDir = state.outDir;
        if (byId('out-dir').value === previousDir) byId('out-dir').value = state.outDir;
        settingsDirty = hasSettingsChanges();
      }
      updateDirectory();
      syncSettings();
      notify('已更新默认保存位置。');
    } else {
      byId('out-dir').value = selected;
      editSettings();
    }
  } catch (error) { notify(error.message); }
}

function exportSettings() {
  const data = JSON.stringify({version: 1, ...savedConfiguration()}, null, 2) + '\n';
  const url = URL.createObjectURL(new Blob([data], {type: 'application/json'}));
  const link = element('a');
  link.href = url;
  link.download = 'yt-downloader-settings.json';
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 30000);
}

async function readConfiguration(file) {
  if (!file) return;
  try {
    if (file.size > 1024 * 1024) throw new Error('配置文件过大。');
    const config = JSON.parse(await file.text());
    if (config?.version !== 1) throw new Error('无法识别此配置文件的版本。');
    const error = validationError(config);
    if (error) throw new Error(error);
    fillSettings(config);
    editSettings();
    notify(settingsDirty ? '配置已载入。保存后生效。' : '导入的配置与已保存设置一致。');
  } catch (error) { display('settings-error', error instanceof SyntaxError ? '配置文件的 JSON 格式无效。' : error.message); }
}

function initPreferences() {
  const audioOption = new Option('仅音频', 'audio');
  audioOption.hidden = true;
  audioOption.disabled = true;
  byId('batch-quality').add(audioOption);
  byId('batch-format').addEventListener('change', syncFormatControls);
  byId('ai-protocol').addEventListener('input', switchAIProtocol);
  byId('use-official-ai').addEventListener('click', () => {
    const address = byId('ai-base-url');
    address.value = officialAIAddress(byId('ai-protocol').value);
    address.dispatchEvent(new Event('input', {bubbles: true}));
    address.focus();
  });
  for (const id of ['ai-base-url', 'ai-protocol']) {
    byId(id).addEventListener('input', () => {
      matchModelToService();
      byId('ai-key').value = '';
      clearModelList();
    });
  }
  byId('ai-base-url').addEventListener('change', () => { draftModelService = modelService(readAISettings()); });
  byId('ai-key').addEventListener('input', clearModelList);
  byId('fetch-ai-models').addEventListener('click', fetchAIModels);
  byId('ai-model-list').addEventListener('change', event => {
    if (event.target.selectedOptions[0]?.dataset.manual) {
      showModelPicker(false);
      const input = byId('ai-model');
      input.focus();
      input.select();
    } else if (event.target.value) {
      byId('ai-model').value = event.target.value;
      editSettings();
    }
  });
  for (const id of settingsFields) byId(id).addEventListener('input', editSettings);
  systemDark.addEventListener('change', syncSettings);
  byId('save-settings').addEventListener('click', saveSettings);
  byId('discard-settings').addEventListener('click', () => {
    settingsDirty = false;
    clearSettingsError();
    syncSettings();
    byId('tab-' + settingsCategory).focus();
    notify('已撤销未保存的更改。');
  });
  document.addEventListener('keydown', event => {
    if (byId('settings-panel').hidden || document.querySelector('dialog[open]')) return;
    if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 's') {
      event.preventDefault();
      saveSettings();
    }
  });
  byId('save-settings').setAttribute('aria-keyshortcuts', 'Meta+S Control+S');
  byId('settings-save-shortcut').textContent = isMac ? '⌘ S' : 'Ctrl S';
  byId('save-settings').title = isMac ? '保存设置（⌘ S）' : '保存设置（Ctrl + S）';
  byId('edit-destination').addEventListener('click', () => chooseFolder(true));
  byId('browse-folder').hidden = !nativeDesktop;
  byId('browse-folder').addEventListener('click', () => chooseFolder());
  byId('export-settings').addEventListener('click', exportSettings);
  byId('import-settings').addEventListener('click', () => byId('config-file').click());
  byId('config-file').addEventListener('change', event => {
    readConfiguration(event.target.files[0]);
    event.target.value = '';
  });
  byId('reset-settings').addEventListener('click', async () => {
    try {
      const response = await request('/api/default-settings');
      fillSettings(await response.json());
      editSettings();
      notify(settingsDirty ? '已载入默认设置。保存后生效。' : '当前已使用默认设置。');
    } catch (error) { display('settings-error', error.message); }
  });
  for (const id of ['logs', 'data']) {
    byId('open-' + id).addEventListener('click', async () => {
      try { await request('/api/reveal-' + id, {}); }
      catch (error) { notify(error.message); }
    });
  }
  const tabs = [...document.querySelectorAll('.preference-tabs button')];
  for (const button of tabs) {
    button.addEventListener('click', () => showSettingsCategory(button.id.slice(4)));
    button.addEventListener('keydown', event => {
      const visible = tabs.filter(tab => !tab.hidden);
      const index = visible.indexOf(button);
      const target = {ArrowRight: (index + 1) % visible.length, ArrowLeft: (index + visible.length - 1) % visible.length, Home: 0, End: visible.length - 1}[event.key];
      if (target === undefined) return;
      event.preventDefault();
      const next = visible[target];
      next.click();
      next.focus();
    });
  }
}
