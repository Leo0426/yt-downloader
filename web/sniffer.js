'use strict';

const byId = id => document.getElementById(id);
const bridge = window.sniffer;
const selection = new Set();
const queued = new Set();
const rows = new Map();
let state = {pageURL: '', capturing: true, candidates: []};
let saving = false;
let defaultFormat = 'mp4';

function message(text, error = false) {
  const box = byId('message');
  box.textContent = text;
  box.classList.toggle('error', error);
}

function displayBytes(bytes) {
  if (!bytes) return '';
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(0) + ' KiB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MiB';
}

function group(item) {
  if (item.kind === '音频') return 'audio';
  if (item.kind === 'HLS' || item.kind === 'DASH') return 'stream';
  return 'video';
}

function visibleItems() {
  const filter = byId('filter').value;
  return state.candidates.filter(item => filter === 'all' || group(item) === filter);
}

function outputFormat() { return byId('sniff-output').value || defaultFormat; }
function selectionKey(url, format = outputFormat()) { return format + '\n' + url; }

function available(item) {
  const format = outputFormat();
  const acceptsAudio = format === 'm4a' || format === 'mp3';
  return (group(item) !== 'audio' || acceptsAudio) && !queued.has(selectionKey(item.url, format));
}

function makeRow(item) {
  const url = new URL(item.url);
  let name = url.pathname.split('/').filter(Boolean).at(-1) || url.hostname;
  try { name = decodeURIComponent(name); } catch { /* keep the encoded name */ }
  const node = document.createElement('li');
  node.className = 'candidate';

  const checkbox = document.createElement('input');
  checkbox.type = 'checkbox';
  checkbox.setAttribute('aria-label', '选择 ' + name);
  checkbox.addEventListener('change', () => {
    if (checkbox.checked) selection.add(item.url);
    else selection.delete(item.url);
    renderResults();
  });
  const label = document.createElement('label');
  const details = document.createElement('span');
  details.className = 'candidate-body';
  details.title = item.url;
  const title = document.createElement('span');
  title.className = 'candidate-title';
  title.textContent = name;
  const meta = document.createElement('span');
  meta.className = 'candidate-meta';
  const kind = document.createElement('span');
  kind.className = 'candidate-kind';
  const size = document.createElement('span');
  size.className = 'candidate-size';
  const host = document.createElement('span');
  host.className = 'candidate-host';
  host.textContent = url.host;
  const status = document.createElement('span');
  status.className = 'candidate-status';
  meta.append(kind, size, host);
  details.append(title, meta, status);
  label.append(checkbox, details);

  const copy = document.createElement('button');
  copy.type = 'button';
  copy.className = 'icon-button';
  copy.title = '复制媒体地址';
  copy.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-copy"/></svg>';
  copy.setAttribute('aria-label', '复制 ' + name + ' 的媒体地址');
  copy.addEventListener('click', async () => {
    try { await bridge.copy(item.url); message('媒体地址已复制。'); }
    catch (error) { message(error.message, true); }
  });
  node.append(label, copy);
  return {node, checkbox, kind, size, status};
}

function renderResults() {
  const visible = visibleItems();
  const list = byId('candidates');
  const keys = new Set(visible.map(item => item.url));
  for (const [url, row] of rows) {
    if (!keys.has(url)) {
      row.node.remove();
      rows.delete(url);
    }
  }
  visible.forEach((item, index) => {
    let row = rows.get(item.url);
    if (!row) { row = makeRow(item); rows.set(item.url, row); }
    const submitted = queued.has(selectionKey(item.url));
    const selectable = available(item);
    row.checkbox.disabled = !selectable || saving;
    row.checkbox.checked = selection.has(item.url) || submitted;
    row.node.classList.toggle('selected', selection.has(item.url));
    row.node.classList.toggle('queued', submitted);
    row.kind.textContent = item.kind;
    row.size.textContent = group(item) === 'stream' && item.bytes ? '清单 ' + displayBytes(item.bytes) : displayBytes(item.bytes);
    row.size.hidden = !item.bytes;
    row.status.textContent = submitted ? '已提交到队列' : selectable ? '' : '选择 M4A 或 MP3 即可下载';
    row.status.hidden = !row.status.textContent;
    if (list.children[index] !== row.node) list.insertBefore(row.node, list.children[index] || null);
  });

  const total = state.candidates.length;
  const filtered = byId('filter').value !== 'all';
  byId('count').textContent = filtered ? `${visible.length} / ${total}` : String(total);
  byId('count').title = `显示 ${visible.length} 项，共 ${total} 项`;
  byId('clear').disabled = !state.candidates.length || saving;
  byId('empty').hidden = visible.length > 0;
  if (!visible.length) {
    byId('empty-title').textContent = filtered ? '此分类暂无资源' : state.capturing ? '等待媒体资源' : '嗅探已暂停';
    byId('empty-message').textContent = filtered ? '可切换资源类型查看其他结果。'
      : state.capturing ? '打开网页并播放媒体后，可在此勾选要下载的资源。' : '点击「继续嗅探」以收集新的媒体资源。';
  }
  const choices = visible.filter(available);
  const visibleSelected = choices.filter(item => selection.has(item.url)).length;
  const toggle = byId('toggle-all');
  toggle.disabled = !choices.length || saving;
  toggle.checked = visibleSelected > 0 && visibleSelected === choices.length;
  toggle.indeterminate = visibleSelected > 0 && visibleSelected < choices.length;
  const hiddenSelected = selection.size - visibleSelected;
  byId('queue-hint').hidden = hiddenSelected === 0;
  byId('queue-hint').textContent = hiddenSelected ? `包含其他分类中已选的 ${hiddenSelected} 项资源。` : '';
  byId('sniff-output').disabled = saving;
  byId('enqueue').disabled = !selection.size || saving;
  byId('enqueue-label').textContent = saving ? '正在提交…' : selection.size ? `将 ${selection.size} 项加入下载队列` : '加入下载队列';
}

function render(next) {
  const pageChanged = next.pageURL !== state.pageURL;
  state = next;
  const live = new Set(state.candidates.map(item => item.url));
  for (const url of selection) if (!live.has(url)) selection.delete(url);
  for (const key of queued) if (!live.has(key.slice(key.indexOf('\n') + 1))) queued.delete(key);

  const address = byId('address');
  if (pageChanged && document.activeElement !== address && state.pageURL) {
    address.value = state.pageURL;
    address.setCustomValidity('');
  }
  byId('back').disabled = !state.canBack;
  byId('forward').disabled = !state.canForward;
  byId('reload').disabled = !state.pageURL;
  byId('capture').toggleAttribute('data-paused', !state.capturing);
  byId('capture-icon').setAttribute('href', state.capturing ? '#i-pause' : '#i-play');
  byId('capture-label').textContent = state.capturing ? '暂停嗅探' : '继续嗅探';
  const status = byId('capture-status');
  status.classList.toggle('paused', !state.capturing);
  status.classList.toggle('active', state.capturing && state.shown);
  const statusLabel = !state.capturing ? '已暂停' : state.shown ? '正在嗅探' : '等待网页';
  if (status.textContent !== statusLabel) status.textContent = statusLabel;
  byId('capture-limit').hidden = !state.clipped;
  byId('welcome').hidden = !!state.shown;
  byId('preview-title').textContent = state.error ? '网页未能打开' : '网页预览';
  byId('preview-message').textContent = state.error || '在地址栏输入网址并按 Enter，在此播放需要下载的媒体。';
  byId('retry-page').hidden = !state.error || !state.pageURL;
  byId('open-label').textContent = state.loading ? '加载中…' : '打开';

  renderResults();
}

byId('address').addEventListener('input', event => event.target.setCustomValidity(''));
byId('open-site').addEventListener('submit', async event => {
  event.preventDefault();
  const field = byId('address');
  const raw = field.value.trim();
  const address = /^[a-z][a-z0-9+.-]*:/i.test(raw) && !/^[^/]+:\d+(?:\/|$)/.test(raw) ? raw : 'https://' + raw;
  let url;
  try { url = new URL(address); } catch { /* display a localised validation message below */ }
  if (!url || !['https:', 'http:'].includes(url.protocol) || /[%\s]/.test(url.hostname) || url.username || url.password) {
    field.setCustomValidity('请输入有效的 HTTP 或 HTTPS 网页地址。');
    field.reportValidity();
    return;
  }
  if (url.origin === location.origin) {
    field.setCustomValidity('请输入需要查找媒体的外部网页地址。');
    field.reportValidity();
    return;
  }
  try {
    await bridge.navigate(url.href);
    if (field.value.trim() === raw) {
      field.value = url.href;
      field.blur();
    }
    message('');
  } catch (error) {
    message(error.message, true);
    field.focus();
  }
});
byId('retry-page').addEventListener('click', () => bridge.action('reload').catch(error => message(error.message, true)));
for (const name of ['back', 'forward', 'reload', 'capture', 'clear']) {
  byId(name).addEventListener('click', async () => {
    try { await bridge.action(name); if (name === 'clear') message(''); }
    catch (error) { message(error.message, true); }
  });
}
byId('filter').addEventListener('change', renderResults);
byId('sniff-output').addEventListener('change', () => {
  for (const item of state.candidates) if (!available(item)) selection.delete(item.url);
  message('');
  renderResults();
});
byId('toggle-all').addEventListener('change', event => {
  for (const item of visibleItems().filter(available)) {
    if (event.target.checked) selection.add(item.url);
    else selection.delete(item.url);
  }
  renderResults();
});

byId('enqueue').addEventListener('click', async () => {
  const urls = state.candidates.filter(item => selection.has(item.url) && available(item)).map(item => item.url);
  if (!urls.length || saving || !state.ownerId) return;
  const format = outputFormat();
  saving = true;
  renderResults();
  try {
    const response = await fetch('/api/jobs', {
      method: 'POST', headers: {'Content-Type': 'application/json', 'X-YTD-User': state.ownerId}, body: JSON.stringify({urls, format}),
    });
    const result = await response.json();
    if (!response.ok) throw new Error(result.error || '无法加入下载队列。');
    for (const url of urls) {
      selection.delete(url);
      queued.add(selectionKey(url, format));
    }
    message(`已加入 ${result.added} 项${result.skipped ? `，跳过 ${result.skipped} 项重复地址` : ''}。`);
  } catch (error) { message(error.message, true); }
  finally { saving = false; renderResults(); }
});

if (bridge) {
  fetch('/api/state').then(response => response.json()).then(saved => {
    const candidate = saved.preferences?.download?.format;
    if (['mp4', 'mkv', 'm4a', 'mp3'].includes(candidate)) defaultFormat = candidate;
    byId('sniff-output').options[0].textContent = '默认 · ' + defaultFormat.toUpperCase();
    for (const item of state.candidates) if (!available(item)) selection.delete(item.url);
    renderResults();
  }).catch(() => {});
  bridge.onState(render);
  bridge.state().then(render).catch(error => message(error.message, true));
  byId('address').focus();
} else {
  byId('capture-status').textContent = '未连接';
  byId('capture-status').classList.add('paused');
  byId('preview-title').textContent = '在桌面应用中打开网页嗅探';
  byId('preview-message').textContent = '启动 YT Downloader 后，点击导航中的「网页嗅探」。';
  message('请从桌面应用打开网页嗅探。', true);
  for (const control of document.querySelectorAll('button,input,select')) control.disabled = true;
}
