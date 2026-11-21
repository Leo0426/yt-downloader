'use strict';

const byId = id => document.getElementById(id);
const nativeDesktop = window.desktop;
const isMac = /Mac/.test(navigator.platform);
const labels = {queued: '等待中', running: '下载中', done: '已完成', cached: '已复用', failed: '失败', canceled: '已取消'};
const active = new Set(['queued', 'running']);
const finished = new Set(['done', 'cached']);
let state = {jobs: [], tools: [], outDir: '', busy: false};
let selectedFilter = 'all';
let searchTerm = '';
let nativeProgress = '';
let pendingSubmission = false;
let shuttingDown = false;
let loaded = false;
let refreshing;
let shownLog = '';
let logRequest = 0;
let toastTimer;
const jobRows = new Map();

function element(tag, className, content) {
  const el = document.createElement(tag);
  if (className) el.className = className;
  if (content !== undefined) el.textContent = content;
  return el;
}

function display(id, content) {
  const el = byId(id);
  if (el.textContent !== (content || '')) el.textContent = content || '';
  el.hidden = !content;
}

function setConnection(label, online) {
  byId('connection-dot').classList.toggle('online', online);
  const status = byId('connection-text');
  if (status.textContent !== label) status.textContent = label;
}

function notify(message) {
  clearTimeout(toastTimer);
  display('toast', message);
  toastTimer = setTimeout(() => display('toast', ''), 5000);
}

async function request(path, data, signal) {
  const options = {cache: 'no-store', signal, headers: {'X-YTD-User': accountUserID()}};
  if (data !== undefined) {
    options.method = 'POST';
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(data);
  }
  const response = await fetch(path, options);
  if (!response.ok) {
    if (response.status === 401) accountExpired();
    const body = await response.text();
    let message = body;
    try { message = JSON.parse(body).error || body; } catch { /* Some errors are plain text. */ }
    throw new Error(message || `请求失败（${response.status}）`);
  }
  return response;
}

function directoryName(path) {
  return path.replace(/[\\/]+$/, '').split(/[\\/]/).pop() || path;
}

function updateDirectory() {
  const path = state.outDir || '';
  byId('folder-path').textContent = path || '默认下载目录';
  byId('folder-path').title = path;
  if (isWebDeployment() && state.downloadRoot) {
    byId('dir-help').textContent = '下载根目录为 ' + state.downloadRoot + '。可填写根内的绝对路径，或 movies 等子目录名。';
  }
  byId('edit-destination').title = '更改保存位置' + (path ? ' · ' + path : '');
}

function configureWorkspace() {
  const view = hasPermission('jobs.view');
  const create = hasPermission('jobs.create');
  const settings = hasPermission('settings.manage');
  document.querySelector('.task-nav').hidden = !view;
  document.querySelector('.composer').hidden = !create;
  document.querySelector('.queue-metrics').hidden = !view;
  document.querySelector('.tasks').hidden = !view;
  byId('access-empty').hidden = view || create;
  byId('pause-queue').hidden = !hasPermission('queue.manage');
  byId('quit').hidden = isWebDeployment() || !hasPermission('app.quit');
  byId('nav-sniffer').hidden = !create || !nativeDesktop?.openSniffer;
  byId('nav-settings').hidden = !settings && !hasPermission('tools.manage') && !hasPermission('storage.manage') && (isWebDeployment() || !hasPermission('storage.reveal'));
  if (isWebDeployment()) {
    byId('destination-label').textContent = '保存到服务器';
    byId('out-dir').placeholder = '例如 videos 或下载根目录内的绝对路径';
  }
  byId('edit-destination').disabled = !settings;
  document.querySelector('.directory-change').hidden = !settings;
  byId('nav-queue').querySelector('span').textContent = hasPermission('jobs.viewAll') ? '全部任务' : '我的任务';
  byId('view-name').textContent = byId('nav-queue').querySelector('span').textContent;
  configureSettingsAccess();
}

function setPage(settings) {
  if (settings && byId('nav-settings').hidden) return;
  byId('queue-panel').hidden = settings;
  byId('settings-panel').hidden = !settings;
  syncNavigation(settings);
  if (settings) showSettingsCategory(settingsCategory);
  window.scrollTo(0, 0);
  document.title = `${settings ? '设置' : '下载工作台'} · YT Downloader`;
}

function syncNavigation(settings) {
  for (const button of document.querySelectorAll('.filter, #nav-settings')) {
    const current = settings ? button.id === 'nav-settings' : button.dataset.filter === selectedFilter;
    button.classList.toggle('active', current);
    if (button.dataset.filter) button.setAttribute('aria-pressed', String(current));
    if (current) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  }
}

function filtersMatch(status) {
  if (selectedFilter === 'active') return active.has(status);
  if (selectedFilter === 'finished') return finished.has(status);
  return selectedFilter === 'all' || selectedFilter === status;
}

function icon(name) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('class', 'icon');
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS(ns, 'use');
  use.setAttribute('href', '/icons.svg#' + name);
  svg.append(use);
  return svg;
}

function action(actionName, text, symbol) {
  const button = element('button', symbol ? 'action icon-only' : 'action');
  button.type = 'button';
  button.dataset.action = actionName;
  if (symbol) {
    button.append(icon(symbol));
    button.setAttribute('aria-label', text);
    button.title = text;
  } else button.textContent = text;
  return button;
}

function transferPercent(job) {
  const match = /^(\d+(?:\.\d+)?)%/.exec(job.message || '');
  return match ? Math.min(100, Number(match[1])) : null;
}

function syncNativeProgress(running = []) {
  if (!nativeDesktop) return;
  const values = running.map(transferPercent);
  const known = values.length && values.every(value => value !== null);
  const fraction = known ? values.reduce((total, value) => total + value, 0) / (100 * values.length) : null;
  const key = running.length + '/' + fraction;
  if (key === nativeProgress) return;
  nativeProgress = key;
  nativeDesktop.progress(fraction, running.length > 0).catch(() => {});
}

function describeSource(raw) {
  try {
    const url = new URL(raw);
    const id = url.searchParams.get('v');
    const host = url.hostname.replace(/^www\./, '');
    if (id && host === 'youtube.com') return {host: 'YouTube', title: 'YouTube · ' + id};
    const slug = url.pathname.split('/').filter(Boolean).at(-1);
    if (slug) {
      let title = slug;
      try { title = decodeURIComponent(slug); } catch { /* keep the encoded name */ }
      return {host, title: title.replace(/[_-]+/g, ' ')};
    }
    return {host, title: host};
  } catch { return {host: raw, title: raw}; }
}

const jobDate = new Intl.DateTimeFormat('zh-CN', {month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false});

function makeJobRow(job) {
  const row = element('article', 'job-row');
  row.dataset.id = job.id;
  row.dataset.status = job.status;
  const main = element('div', 'job-main');
  const source = describeSource(job.url);
  const format = job.options?.format || 'mp4';
  const audio = isAudioFormat(format);
  const name = job.title || (job.path && directoryName(job.path).replace(/\.(mp4|mkv|m4a|mp3)$/i, '')) || source.title;
  const title = element('h3', 'job-title', name);
  title.id = 'job-title-' + job.id;
  row.setAttribute('aria-labelledby', title.id);
  title.title = name;
  const mark = element('span', 'media-mark');
  mark.append(icon(audio ? 'music' : 'film'));
  const created = new Date(job.created);
  const date = Number.isFinite(created.getTime()) ? jobDate.format(created) : '';
  const owner = hasPermission('jobs.viewAll') ? job.ownerName || (job.ownerId ? '已删除用户' : '历史任务') : '';
  const meta = element('div', 'job-url', [owner, source.host, date].filter(Boolean).join(' · '));
  meta.title = [owner, job.url, date && created.toLocaleString()].filter(Boolean).join(' · ');
  const identity = element('div', 'job-identity');
  identity.append(title, meta);
  main.append(mark, identity);

  const output = element('div', 'job-output');
  output.append(element('span', 'job-format', format.toUpperCase()));
  const cap = job.options?.maxResolution;
  const profile = audio ? '音频' : cap ? '≤ ' + cap + 'p' : '最佳画质';
  output.append(element('span', 'job-quality', profile));
  output.title = [format.toUpperCase(), profile, format === 'mp4' && job.options?.compatibleMP4 ? 'H.264 / AAC' : ''].filter(Boolean).join(' · ');

  const status = element('div', 'job-state');
  const line = element('div', 'status-line');
  const badge = element('span', 'status-badge', labels[job.status] || job.status);
  if (Object.hasOwn(labels, job.status)) badge.classList.add(job.status);
  line.append(badge);
  status.append(line);
  let detail = '';
  if (job.status === 'running') {
    const percent = transferPercent(job);
    if (percent !== null) line.append(element('span', 'job-percent', percent + '%'));
    const progress = element('progress', 'job-progress');
    progress.max = 100;
    if (percent !== null) progress.value = percent;
    progress.setAttribute('aria-label', '当前下载进度');
    status.append(progress);
    detail = (job.message || '').replace(/^\d+(?:\.\d+)?%\s*/, '').replace(/^of\s+/i, '').replace(/\s+at\s+/i, ' · ').replace(/\s+ETA\s+/i, ' · 剩余 ');
  } else if (job.status === 'failed') detail = job.failure?.summary || job.message || '';
  if (detail) {
    const message = element('p', 'job-message', detail);
    message.title = detail;
    status.append(message);
  }
  const aiNote = jobAINote(job);
  if (aiNote) {
    const note = element('p', 'job-ai-note', aiNote);
    note.title = aiNote;
    status.append(note);
  }
  const actions = element('div', 'job-actions');
  const control = hasPermission('jobs.control') && (job.ownerId === accountUserID() || hasPermission('jobs.controlAll'));
  if (hasPermission('jobs.files') && finished.has(job.status) && job.path) {
    if (isWebDeployment()) {
      const link = element('a', 'action', '下载文件');
      link.href = '/api/file?' + new URLSearchParams({id: job.id});
      link.download = '';
      link.setAttribute('aria-label', '下载文件 · ' + name);
      actions.append(link);
    } else actions.append(action('open', '播放'), action('reveal', '打开目录', 'folder'));
  }
  if (control && hasPermission('jobs.ai') && job.status === 'failed') actions.append(action('ai-retry', 'AI 辅助下载'));
  if (control && (job.status === 'failed' || job.status === 'canceled')) actions.append(action('retry', '重试', job.status === 'failed' ? 'undo' : ''));
  if (control && active.has(job.status)) actions.append(action('cancel', '取消'));
  if (hasPermission('jobs.logs')) actions.append(action('log', job.status === 'failed' ? '原因与日志' : '日志', 'log'));
  if (control) actions.append(action('delete', '删除任务', 'trash'));
  row.append(main, output, status, actions);
  return row;
}

function jobAINote(job) {
  if (job.aiNote) return 'AI 辅助 · ' + job.aiNote;
  if (job.aiRetry && job.status === 'queued') return 'AI 辅助 · 等待队列调度';
  if (job.aiRetry && job.status === 'running') return 'AI 辅助下载中';
  return '';
}

function emptyMessage() {
  const empty = element('div', 'empty');
  const symbol = element('span', 'empty-art');
  symbol.setAttribute('aria-hidden', 'true');
  symbol.append(icon(searchTerm ? 'search' : 'download'));
  empty.append(symbol);
  let title = '暂无下载任务';
  let detail = hasPermission('jobs.create') ? '添加链接后，在这里查看下载进度和文件。' : '当前可查看的队列为空。';
  let resetLabel = hasPermission('jobs.create') ? '添加链接' : '';
  if (searchTerm) {
    title = '没有找到匹配的任务';
    detail = '试试视频标题、文件名或链接中的其他关键词。';
    resetLabel = '清除搜索';
  } else if (selectedFilter !== 'all') {
    title = {active: '暂无进行中的任务', finished: '暂无已完成的任务', failed: '暂无失败任务', canceled: '暂无已取消的任务'}[selectedFilter] || '当前分类暂无任务';
    detail = '可以返回全部任务查看其他下载记录。';
    resetLabel = '查看全部任务';
  }
  empty.append(element('p', 'empty-title', title), element('span', 'empty-description', detail));
  if (resetLabel) {
    const reset = element('button', 'action empty-reset', resetLabel);
    reset.type = 'button';
    reset.addEventListener('click', () => {
      if (searchTerm) {
        byId('search-jobs').value = '';
        searchTerm = '';
        renderJobs();
        byId('search-jobs').focus();
      } else if (selectedFilter === 'all') {
        byId('urls').focus();
      } else {
        setFilter('all');
        document.querySelector('[data-filter="all"]').focus();
      }
    });
    empty.append(reset);
  }
  return empty;
}

function renderJobs() {
  if (!state.preferences) return;
  const list = byId('jobs');
  const running = [];
  const waiting = [];
  const history = [];
  for (const job of state.jobs) {
    if (!filtersMatch(job.status)) continue;
    if (searchTerm && ![job.url, job.title || '', directoryName(job.path || ''), describeSource(job.url).title]
      .some(value => value.toLowerCase().includes(searchTerm))) continue;
    if (job.status === 'running') running.push(job);
    else if (job.status === 'queued') waiting.push(job);
    else history.push(job);
  }
  const visible = [...running, ...waiting, ...history.reverse()];
  byId('total-count').textContent = visible.length;
  const focus = document.activeElement;
  const focusId = focus.closest('.job-row')?.dataset.id;
  const focusAction = focus.dataset.action;
  const keys = new Set(visible.map(job => job.id));
  for (const [id, value] of jobRows) {
    if (!keys.has(id)) {
      value.row.remove();
      jobRows.delete(id);
    }
  }
  if (!visible.length) {
    const signature = 'empty-' + selectedFilter + '/' + !!searchTerm;
    if (list.dataset.empty !== signature) list.replaceChildren(emptyMessage());
    list.dataset.empty = signature;
    if (focusId) byId('task-title').focus();
    return;
  }
  if (list.querySelector('.empty')) list.replaceChildren();
  delete list.dataset.empty;
  visible.forEach((job, index) => {
    const signature = JSON.stringify([job.url, job.title, job.created, job.ownerId, job.ownerName, job.status, job.message, job.path, job.options, job.failure, job.aiNote, !!job.aiRetry]);
    let cached = jobRows.get(job.id);
    if (cached?.signature !== signature) {
      const row = makeJobRow(job);
      if (cached) cached.row.replaceWith(row);
      cached = {signature, row};
      jobRows.set(job.id, cached);
    }
    if (list.children[index] !== cached.row) list.insertBefore(cached.row, list.children[index] || null);
  });
  if (focusId && focusAction && !focus.isConnected) {
    const row = jobRows.get(focusId)?.row;
    const replacement = row?.querySelector(`[data-action="${focusAction}"]`) || row?.querySelector('button') || byId('task-title');
    replacement.focus({preventScroll: true});
  }
}

function renderOverview() {
  const counts = {running: 0, queued: 0, done: 0, failed: 0, canceled: 0};
  for (const job of state.jobs) {
    if (finished.has(job.status)) counts.done++;
    else if (Object.hasOwn(counts, job.status)) counts[job.status]++;
  }
  byId('metric-running').textContent = counts.running;
  byId('metric-waiting').textContent = counts.queued;
  byId('metric-done').textContent = counts.done;
  byId('stat-failed').textContent = counts.failed;
  byId('stat-total').textContent = state.jobs.length;
  byId('stat-canceled').textContent = counts.canceled;
  document.querySelector('[data-filter="canceled"]').hidden = !counts.canceled && selectedFilter !== 'canceled';
}

function sizeLabel(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 ** 2) return (bytes / 1024).toFixed(1) + ' KiB';
  if (bytes < 1024 ** 3) return (bytes / 1024 ** 2).toFixed(1) + ' MiB';
  return (bytes / 1024 ** 3).toFixed(1) + ' GiB';
}

let clearing = false;
let cacheKnown = false;
let cacheQuery = 0;
async function refreshCache() {
  const query = ++cacheQuery;
  cacheKnown = false;
  byId('clear-cache').disabled = true;
  byId('cache-size').textContent = '正在统计…';
  try {
    const response = await request('/api/cache');
    const {bytes, objects, partialBytes, stages} = await response.json();
    if (query !== cacheQuery) return;
    cacheKnown = !!(bytes || objects || partialBytes || stages);
    byId('cache-size').textContent = `共 ${sizeLabel(bytes)} · 已完成 ${objects} 个 · 未完成 ${stages} 个（${sizeLabel(partialBytes)}）`;
  } catch (error) {
    if (query === cacheQuery) byId('cache-size').textContent = '读取缓存统计失败。' + error.message;
  } finally { syncClearButton(); }
}

function syncClearButton() {
  byId('clear-cache').disabled = clearing || !cacheKnown || !loaded || state.busy || state.jobs.some(job => active.has(job.status));
}

byId('clear-cache').addEventListener('click', async () => {
  if (!window.confirm('清理下载缓存？缓存副本和未完成的下载分片将被删除，已导出的媒体文件会保留。')) return;
  clearing = true;
  syncClearButton();
  try {
    await request('/api/cache/clear', {});
    await refreshCache();
    notify('已清理缓存。');
  } catch (error) { notify(error.message); }
  finally { clearing = false; syncClearButton(); }
});

let toolsSignature = '';
function renderTools() {
  const signature = JSON.stringify(state.tools);
  if (signature !== toolsSignature) {
    toolsSignature = signature;
    const rows = state.tools.map(tool => {
      const row = element('li', 'tool');
      row.append(element('span', 'tool-name', tool.name), element('span', 'tool-version', tool.version || '待安装'));
      if (tool.path) row.append(element('code', 'tool-path', tool.path));
      return row;
    });
    byId('tools').replaceChildren(...rows);
  }
  byId('update').disabled = !!state.busy;
  byId('update').title = state.busy ? '当前操作完成后可以更新' : '';
  display('maintenance', state.maintenance);
  byId('maintenance-details').hidden = !state.maintenanceLog;
  if (byId('maintenance-log').textContent !== (state.maintenanceLog || '')) byId('maintenance-log').textContent = state.maintenanceLog || '';
}

function refresh() {
  if (!refreshing && !shuttingDown) {
    refreshing = readState().finally(() => { refreshing = undefined; });
  }
  return refreshing;
}

async function readState() {
  try {
    const response = await request('/api/state');
    const latest = await response.json();
    if (shuttingDown) return;
    if (!syncAccountSession(latest.session)) return;
    state = {...latest, jobs: (latest.jobs || []).map(job => ({...job, ownerName: latest.ownerNames?.[job.ownerId]})), tools: latest.tools || []};
    loaded = true;
    byId('add').disabled = pendingSubmission || !hasPermission('jobs.create');
    setConnection(isWebDeployment() ? '下载服务在线' : '本地服务在线', true);
    display('connection-error', '');
    updateDirectory();
    byId('cache-dir').textContent = state.cacheDir || '加载中…';
    syncSettings();
    renderPause();
    renderOverview();
    renderJobs();
    if (shownLog) {
      const job = state.jobs.find(item => item.id === shownLog);
      if (job) display('log-ai-note', jobAINote(job));
    }
    syncNativeProgress(state.jobs.filter(job => job.status === 'running'));
    renderTools();
    syncClearButton();
  } catch (error) {
    if (shuttingDown) return;
    loaded = false;
    syncClearButton();
    byId('add').disabled = true;
    byId('update').disabled = true;
    syncNativeProgress();
    renderPause();
    syncSettings();
    setConnection('连接中断', false);
    if (!state.preferences) byId('jobs').querySelector('.empty-title').textContent = '等待连接下载服务';
    display('connection-error', `暂时无法连接下载服务，将自动重试。${error.message}`);
  }
}

function inspectLinks(text) {
  const urls = new Set();
  let invalid = 0;
  let duplicates = 0;
  let firstInvalid;
  for (const match of text.matchAll(/\S+/g)) {
    const token = match[0];
    const raw = token.replace(/\\([:/.?&=_#-])/g, '$1').replace(/\\$/, '');
    let url;
    try { url = new URL(raw); } catch { /* Invalid entries are reported together below. */ }
    // Go decodes escapes in the path and fragment, but preserves the raw query.
    const badEscape = url && /%(?![\da-f]{2})/i.test(url.pathname + url.hash);
    if (!url || !['https:', 'http:'].includes(url.protocol) || !url.hostname || url.username || url.password || badEscape) {
      invalid++;
      if (!firstInvalid) firstInvalid = {start: match.index, end: match.index + token.length};
    } else if (urls.has(url.href)) duplicates++;
    else urls.add(url.href);
  }
  return {urls: [...urls], invalid, duplicates, firstInvalid};
}

function updateLinkCount() {
  const {urls, invalid, duplicates} = inspectLinks(urlInput.value);
  const parts = [];
  if (urls.length) parts.push(urls.length + ' 个链接');
  if (duplicates) parts.push(duplicates + ' 个重复');
  if (invalid) parts.push(invalid + ' 项待修正');
  display('link-count', parts.join(' · '));
}

byId('add-form').addEventListener('submit', async event => {
  event.preventDefault();
  if (pendingSubmission || !loaded || !hasPermission('jobs.create')) return;
  display('input-error', '');
  const {urls, invalid, duplicates, firstInvalid} = inspectLinks(urlInput.value);
  if (invalid || !urls.length || urls.length > 500) {
    const reason = invalid ? '请将选中的内容改为有效的 HTTP 或 HTTPS 链接，每个链接用空格或换行分隔。'
      : urls.length ? '每批最多可提交 500 个链接，请分批加入。' : '请先输入视频或音频链接。';
    display('input-error', reason);
    urlInput.setAttribute('aria-invalid', 'true');
    urlInput.focus();
    if (firstInvalid) urlInput.setSelectionRange(firstInvalid.start, firstInvalid.end);
    return;
  }
  const outDir = state.outDir;
  const format = byId('batch-format').value;
  const resolution = byId('batch-quality').disabled ? '' : byId('batch-quality').value;
  if (!outDir && hasPermission('settings.manage')) {
    setPage(true);
    showSettingsCategory('download');
    notify('请先设置默认保存位置。');
    byId('out-dir').focus();
    return;
  }
  const submittedText = byId('urls').value;
  pendingSubmission = true;
  byId('add').disabled = true;
  byId('add-label').textContent = '正在加入…';
  try {
    const response = await request('/api/jobs', {urls, ...(hasPermission('settings.manage') ? {outDir} : {}), format, ...(resolution === '' ? {} : {resolution: Number(resolution)})});
    const result = await response.json();
    if (urlInput.value === submittedText) {
      urlInput.value = '';
      updateLinkCount();
    }
    const skipped = (result.skipped || 0) + duplicates;
    if (!result.added && skipped) notify('相同的任务已在队列中。');
    else notify(`已加入 ${result.added} 个任务${skipped ? `，跳过 ${skipped} 个重复链接` : ''}。`);
    byId('search-jobs').value = '';
    searchTerm = '';
    setFilter('all');
    await refresh();
  } catch (error) {
    display('input-error', error.message);
  } finally {
    pendingSubmission = false;
    byId('add').disabled = !loaded;
    byId('add-label').textContent = '加入队列';
  }
});

function setFilter(value) {
  selectedFilter = value;
  const viewNames = {all: hasPermission('jobs.viewAll') ? '全部任务' : '我的任务', active: '进行中', finished: '已完成', failed: '失败任务', canceled: '已取消'};
  byId('view-name').textContent = viewNames[value];
  if (byId('queue-panel').hidden) setPage(false);
  else syncNavigation(false);
  renderJobs();
}

async function readLog() {
  const id = shownLog;
  const generation = ++logRequest;
  const output = byId('log-content');
  const atEnd = output.scrollHeight - output.scrollTop - output.clientHeight < 8;
  try {
    const response = await request('/api/log?' + new URLSearchParams({id}));
    const content = await response.text();
    if (id !== shownLog || generation !== logRequest) return;
    output.textContent = content || '日志尚未生成。';
    if (atEnd) output.scrollTop = output.scrollHeight;
  } catch (error) {
    if (id === shownLog && generation === logRequest) output.textContent = error.message;
  }
}

const deleteDialog = byId('delete-dialog');
const confirmDelete = byId('confirm-delete');
const keepJob = byId('keep-job');

byId('jobs').addEventListener('click', async event => {
  const button = event.target.closest('button[data-action]');
  if (!button) return;
  const id = button.closest('.job-row').dataset.id;
  const name = button.dataset.action;
  const job = state.jobs.find(item => item.id === id);
  if (!job) return;
  if (name === 'delete') {
    deleteDialog.dataset.jobId = id;
    byId('delete-job-name').textContent = button.closest('.job-row').querySelector('.job-title').textContent;
    display('delete-error', '');
    deleteDialog.showModal();
    return;
  }
  if (name === 'log') {
    shownLog = id;
    byId('log-url').textContent = job.url;
    const diagnosis = job.status === 'failed' ? job.failure : null;
    byId('log-diagnosis').hidden = !diagnosis;
    byId('log-cause').textContent = diagnosis?.summary || '';
    byId('log-advice').textContent = diagnosis?.advice || '';
    display('log-ai-note', jobAINote(job));
    byId('log-content').textContent = '正在读取日志…';
    byId('save-log').href = '/api/log?' + new URLSearchParams({id});
    byId('save-log').download = `${id}.log`;
    byId('log-dialog').showModal();
    await readLog();
    return;
  }
  if (name === 'ai-retry' && !state.aiConfigured) {
    if (!hasPermission('settings.manage')) {
      notify('请联系有设置权限的用户配置 AI 服务。');
      return;
    }
    const field = missingAIField(state.preferences?.ai);
    if (field) {
      setPage(true);
      showSettingsCategory('ai');
      byId(field).focus();
      notify('请先保存 AI 设置，再回到失败任务启动辅助下载。');
      return;
    }
  }
  button.disabled = true;
  try {
    await request('/api/' + name, {id});
    if (name === 'retry' || name === 'ai-retry') {
      setFilter('all');
      notify(name === 'ai-retry' ? '已提交 AI 辅助下载请求。' : '已提交重试。');
    }
    await refresh();
  } catch (error) {
    notify(error.message);
  } finally {
    button.disabled = false;
  }
});

keepJob.addEventListener('click', () => deleteDialog.close());
deleteDialog.addEventListener('cancel', event => {
  if (confirmDelete.disabled) event.preventDefault();
});
deleteDialog.addEventListener('close', () => {
  const row = jobRows.get(deleteDialog.dataset.jobId)?.row;
  const target = row?.querySelector('[data-action="delete"]') || byId('task-title');
  target.focus();
});
confirmDelete.addEventListener('click', async () => {
  const id = deleteDialog.dataset.jobId;
  confirmDelete.disabled = keepJob.disabled = true;
  confirmDelete.textContent = '正在删除…';
  display('delete-error', '');
  try {
    await request('/api/delete', {id});
    // Let any snapshot issued before the deletion finish before removing the row.
    await refreshing;
    state.jobs = state.jobs.filter(job => job.id !== id);
    renderOverview();
    renderJobs();
    deleteDialog.close();
    notify('已删除任务。');
    refresh();
  } catch (error) {
    display('delete-error', error.message);
  } finally {
    confirmDelete.disabled = keepJob.disabled = false;
    confirmDelete.textContent = '删除任务';
  }
});

byId('update').addEventListener('click', async () => {
  byId('update').disabled = true;
  try {
    await request('/api/update', {});
    notify('已开始检查组件更新。');
    await refresh();
  } catch (error) {
    notify(error.message);
    byId('update').disabled = !!state.busy;
  }
});

byId('quit').addEventListener('click', () => {
  byId('quit-message').textContent = state.busy || state.jobs.some(job => active.has(job.status)) ? '当前有操作正在进行，退出将中断本地服务。' : '本地下载服务将关闭。';
  byId('quit-dialog').showModal();
});
byId('keep-open').addEventListener('click', () => byId('quit-dialog').close());
byId('confirm-quit').addEventListener('click', async () => {
  byId('confirm-quit').disabled = true;
  try {
    if (nativeDesktop) await nativeDesktop.quit();
    else await request('/api/quit', {});
    shuttingDown = true;
    byId('quit-dialog').close();
    setConnection('服务已关闭', false);
    display('connection-error', '应用已退出，可以关闭此页面。');
    for (const button of document.querySelectorAll('button')) button.disabled = true;
  } catch (error) {
    byId('quit-dialog').close();
    notify(error.message);
    byId('confirm-quit').disabled = false;
  }
});

byId('nav-settings').addEventListener('click', () => setPage(true));
let pausePending = false;
function renderPause() {
  const button = byId('pause-queue');
  button.disabled = !loaded || pausePending;
  byId('pause-label').textContent = state.paused ? '恢复队列' : '暂停队列';
  button.setAttribute('aria-pressed', String(!!state.paused));
  byId('queue-paused').hidden = !state.paused;
  const activity = !state.paused && '队列就绪';
  const limit = state.preferences?.concurrency;
  display('queue-summary', loaded ? [activity, limit && '并发上限 ' + limit].filter(Boolean).join(' · ') : '等待连接下载服务');
}

byId('pause-queue').addEventListener('click', async () => {
  const paused = !state.paused;
  pausePending = true;
  renderPause();
  try {
    await request('/api/queue', {paused});
    state.paused = paused;
    await refresh();
  } catch (error) { notify(error.message); }
  finally { pausePending = false; renderPause(); }
});

const urlInput = byId('urls');
const composer = document.querySelector('.composer');
let dragDepth = 0;
function addLinks(text) {
  if (!text.trim()) return false;
  urlInput.value = [urlInput.value.trim(), text.trim()].filter(Boolean).join('\n');
  urlInput.focus();
  urlInput.dispatchEvent(new Event('input'));
  return true;
}
composer.addEventListener('dragenter', event => {
  event.preventDefault();
  if (++dragDepth === 1) composer.classList.add('dragging');
});
composer.addEventListener('dragover', event => {
  event.preventDefault();
  event.dataTransfer.dropEffect = 'copy';
});
composer.addEventListener('dragleave', () => {
  dragDepth = Math.max(0, dragDepth - 1);
  if (!dragDepth) composer.classList.remove('dragging');
});
composer.addEventListener('drop', event => {
  event.preventDefault();
  dragDepth = 0;
  composer.classList.remove('dragging');
  const text = event.dataTransfer.getData('text/plain') ||
    event.dataTransfer.getData('text/uri-list').split(/\r?\n/).filter(line => !line.startsWith('#')).join('\n');
  if (!addLinks(text)) notify('请拖入视频链接。');
});
window.addEventListener('dragover', event => event.preventDefault());
window.addEventListener('drop', event => event.preventDefault());

byId('search-jobs').addEventListener('input', event => {
  searchTerm = event.target.value.trim().toLowerCase();
  renderJobs();
});
byId('submit-shortcut').textContent = isMac ? '⌘ ↵' : 'Ctrl + Enter';
byId('add').setAttribute('aria-keyshortcuts', isMac ? 'Meta+Enter' : 'Control+Enter');
if (nativeDesktop?.openSniffer) {
  byId('nav-sniffer').addEventListener('click', () => nativeDesktop.openSniffer().catch(error => notify(error.message)));
}
if (nativeDesktop?.paste || navigator.clipboard?.readText) {
  byId('paste-links').hidden = false;
  byId('paste-links').addEventListener('click', async () => {
    try {
      const text = nativeDesktop?.paste ? await nativeDesktop.paste() : await navigator.clipboard.readText();
      if (!addLinks(text)) {
        urlInput.focus();
        notify('剪贴板里没有文本。');
      }
    } catch {
      urlInput.focus();
      notify('无法读取剪贴板，请按 ' + (isMac ? '⌘ + V' : 'Ctrl + V') + ' 粘贴链接。');
    }
  });
}
if (nativeDesktop) {
  nativeDesktop.onNotice?.(notify);
  nativeDesktop.onCommand(command => {
    if (!accountUserID() || document.querySelector('dialog[open]')) return;
    if (command === 'folder' && hasPermission('settings.manage')) {
      chooseFolder(true);
      return;
    }
    if (command === 'settings' && !byId('nav-settings').hidden) {
      setPage(true);
      byId('tab-' + settingsCategory).focus();
      return;
    }
    if (!(command === 'search' && hasPermission('jobs.view') || command === 'links' && hasPermission('jobs.create'))) return;
    setPage(false);
    byId(command === 'search' ? 'search-jobs' : 'urls').focus();
  });
}

urlInput.addEventListener('input', () => {
  urlInput.removeAttribute('aria-invalid');
  display('input-error', '');
  updateLinkCount();
});
byId('urls').addEventListener('keydown', event => {
  if (!event.isComposing && (event.ctrlKey || event.metaKey) && event.key === 'Enter') {
    event.preventDefault();
    byId('add-form').requestSubmit();
  }
});
document.querySelector('.filters').addEventListener('click', event => {
  const button = event.target.closest('[data-filter]');
  if (button) setFilter(button.dataset.filter);
});
document.querySelector('.filters').addEventListener('keydown', event => {
  const buttons = [...event.currentTarget.querySelectorAll('[data-filter]:not([hidden])')];
  const index = buttons.indexOf(event.target);
  if (index < 0) return;
  const next = {ArrowLeft: (index + buttons.length - 1) % buttons.length, ArrowRight: (index + 1) % buttons.length,
    ArrowUp: (index + buttons.length - 1) % buttons.length, ArrowDown: (index + 1) % buttons.length,
    Home: 0, End: buttons.length - 1}[event.key];
  if (next === undefined) return;
  event.preventDefault();
  setFilter(buttons[next].dataset.filter);
  buttons[next].focus();
});
byId('refresh-log').addEventListener('click', readLog);
byId('close-log').addEventListener('click', () => byId('log-dialog').close());
byId('log-dialog').addEventListener('close', () => { shownLog = ''; logRequest++; });
byId('add').disabled = true;
byId('update').disabled = true;
async function poll() {
  await refresh();
  if (!shuttingDown) setTimeout(poll, document.hidden ? 5000 : state.busy ? 800 : 1800);
}
accountStart().then(signedIn => {
  if (!signedIn) return;
  initPreferences();
  configureWorkspace();
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
  poll();
});
