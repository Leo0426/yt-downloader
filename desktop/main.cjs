'use strict';

const {app, BrowserWindow, Menu, clipboard, dialog, ipcMain, nativeTheme} = require('electron');
const {spawn} = require('node:child_process');
const path = require('node:path');
const readline = require('node:readline');
const {registerSniffer} = require('./sniffer.cjs');
const icon = path.join(__dirname, 'assets', 'icon.png');
const backendName = process.platform === 'win32' ? 'downloader-core.exe' : 'downloader-core';

app.setName('YT Downloader');
app.enableSandbox();

let window;
let child;
let childClosed;
let service;
let showSniffer;
let quitting = false;

function startCore() {
  const location = app.isPackaged ? process.resourcesPath : path.join(__dirname, '.dev');
  const args = ['-no-browser', '-parent-pipe'];
  if (process.env.YTDL_DATA_DIR) args.push('-data-dir', process.env.YTDL_DATA_DIR);
  child = spawn(path.join(location, backendName), args, {
    stdio: ['pipe', 'pipe', 'pipe'],
    windowsHide: true,
  });
  // A preexisting instance can return the launch URL and exit before the pipe is closed.
  child.stdin.on('error', () => {});
  childClosed = new Promise(resolve => child.once('close', resolve));
  let stderr = '';
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', data => { stderr = (stderr + data).slice(-4096); });
  return new Promise((resolve, reject) => {
    const lines = readline.createInterface({input: child.stdout});
    const timer = setTimeout(() => reject(new Error('启动下载服务超时')), 20000);
    const fail = error => { clearTimeout(timer); reject(error); };
    child.on('error', fail);
    childClosed.then(code => fail(new Error(stderr.trim() || '下载服务退出，代码 ' + code)));
    lines.on('line', line => {
      let url;
      try { url = new URL(line.trim()); } catch { return; }
      if (url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port &&
          url.pathname === '/start' && /^[0-9a-f]{64}$/.test(url.searchParams.get('token') || '')) {
        clearTimeout(timer);
        resolve(url);
      }
    });
  });
}

function revealWindow() {
  if (!window || window.isDestroyed()) return;
  if (window.isMinimized()) window.restore();
  window.show();
  window.focus();
}

function localFrame(event) {
  return window && event.sender === window.webContents &&
    event.senderFrame === window.webContents.mainFrame &&
    event.senderFrame.url === new URL('/', service).href;
}

// Native operations share the account session of the main window. A renderer
// can request an operation, but the core supplies the effective permissions.
async function requireAccess(permission) {
  if (!window || window.isDestroyed()) throw new Error('请先登录下载器');
  const url = new URL('/api/auth/session', service);
  const name = 'ytd_account_' + url.port;
  const [account] = await window.webContents.session.cookies.get({url: url.href, name});
  if (!account) throw new Error('请先登录下载器');
  const launch = 'ytd_session_' + url.port + '=' + service.searchParams.get('token');
  const response = await fetch(url, {headers: {Cookie: launch + '; ' + name + '=' + account.value}, redirect: 'error', signal: AbortSignal.timeout(4000)});
  if (!response.ok) throw new Error('无法验证账户');
  const {user} = await response.json();
  if (!user?.permissions?.includes(permission)) throw new Error('当前账户没有此操作的权限');
  return user.id;
}

function nativeHandler(name, handle, permission) {
  ipcMain.handle(name, async (event, ...args) => {
    if (!localFrame(event)) throw new Error('无效的应用窗口');
    if (permission) await requireAccess(permission);
    return handle(...args);
  });
}

function registerDesktopFeatures() {
  showSniffer = registerSniffer({service, icon, requireAccess});
  nativeHandler('app:sniffer', showSniffer);
  nativeHandler('app:folder', async current => {
    const selection = await dialog.showOpenDialog(window, {
      title: '选择视频保存位置',
      defaultPath: typeof current === 'string' && path.isAbsolute(current) ? current : undefined,
      properties: ['openDirectory', 'createDirectory'],
    });
    return selection.canceled ? null : selection.filePaths[0];
  }, 'settings.manage');
  nativeHandler('app:appearance', (theme, glass) => {
    if (!['system', 'light', 'dark'].includes(theme) || typeof glass !== 'boolean') return;
    nativeTheme.themeSource = theme;
    if (process.platform === 'darwin') {
      window.setVibrancy(glass ? 'under-window' : null);
      window.setBackgroundColor(glass ? '#00000000' : nativeTheme.shouldUseDarkColors ? '#141c28' : '#f3f5f8');
    }
  });
  nativeHandler('app:paste', () => clipboard.readText(), 'jobs.create');
  nativeHandler('app:quit', () => { setImmediate(() => app.quit()); }, 'app.quit');
  nativeHandler('app:progress', (value, active) => {
    if (active !== true) window.setProgressBar(-1);
    else if (Number.isFinite(value) && value >= 0 && value <= 1) window.setProgressBar(value);
    else window.setProgressBar(2, {mode: 'indeterminate'});
  });
}

function createWindow() {
  window = new BrowserWindow({
    title: 'YT Downloader',
    width: 1160,
    height: 800,
    minWidth: 640,
    minHeight: 520,
    backgroundColor: nativeTheme.shouldUseDarkColors ? '#141c28' : '#f3f5f8',
    show: false,
    icon,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      preload: path.join(__dirname, 'preload.cjs'),
    },
  });
  const contents = window.webContents;
  contents.setWindowOpenHandler(() => ({action: 'deny'}));
  const guard = (event, next) => {
    if (new URL(next).origin !== service.origin) event.preventDefault();
  };
  contents.on('will-navigate', guard);
  contents.on('will-redirect', guard);
  contents.on('did-start-navigation', (_event, _url, _inPlace, isMainFrame) => { if (isMainFrame) window?.setProgressBar(-1); });
  contents.session.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
  contents.session.setPermissionCheckHandler(() => false);
  window.once('ready-to-show', revealWindow);
  window.on('closed', () => { window = undefined; app.quit(); });
  window.loadURL(service.href).catch(error => {
    if (!quitting) {
      dialog.showErrorBox('无法打开下载界面', error.message);
      app.quit();
    }
  });

  const isMac = process.platform === 'darwin';
  const report = error => {
    if (window && !window.isDestroyed()) window.webContents.send('app:notice', error.message);
  };
  const quitFromMenu = () => requireAccess('app.quit').then(() => app.quit()).catch(report);
  const send = name => {
    if (window && !window.isDestroyed()) window.webContents.send('app:command', name);
  };
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    ...(isMac ? [{label: 'YT Downloader', submenu: [
      {label: '关于 YT Downloader', role: 'about'},
      {type: 'separator'},
      {label: '退出 YT Downloader', accelerator: 'Cmd+Q', click: quitFromMenu},
    ]}] : []),
    {label: '文件', submenu: [
      {label: '设置…', accelerator: 'CmdOrCtrl+,', click: () => send('settings')},
      {label: '网页嗅探…', accelerator: 'CmdOrCtrl+Shift+S', click: () => showSniffer().catch(report)},
      {label: '添加链接', accelerator: 'CmdOrCtrl+L', click: () => send('links')},
      {label: '选择保存位置', accelerator: 'CmdOrCtrl+Shift+O', click: () => send('folder')},
      {label: '搜索任务', accelerator: 'CmdOrCtrl+F', click: () => send('search')},
      ...(!isMac ? [{type: 'separator'}, {label: '退出', click: quitFromMenu}] : []),
    ]},
    {label: '编辑', submenu: [
      {label: '撤销', role: 'undo'}, {label: '重做', role: 'redo'}, {type: 'separator'},
      {label: '剪切', role: 'cut'}, {label: '复制', role: 'copy'},
      {label: '粘贴', role: 'paste'}, {label: '全选', role: 'selectAll'},
    ]},
    {label: '窗口', submenu: [
      {label: '最小化', role: 'minimize'}, {label: '关闭窗口', role: 'close'},
    ]},
  ]));
}

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.on('second-instance', revealWindow);
  app.on('activate', revealWindow);
  app.on('window-all-closed', () => app.quit());
  app.on('before-quit', event => {
    if (!child || quitting) return;
    event.preventDefault();
    quitting = true;
    child.stdin.end();
    const force = setTimeout(() => child.kill(), 7000);
    childClosed.finally(() => { clearTimeout(force); app.quit(); });
  });
  app.whenReady().then(async () => {
    if (process.platform === 'darwin') app.dock.setIcon(icon);
    service = await startCore();
    if (quitting) return;
    registerDesktopFeatures();
    createWindow();
  }).catch(error => {
    if (!quitting) dialog.showErrorBox('YT Downloader 启动失败', error.message);
    app.quit();
  });
}
