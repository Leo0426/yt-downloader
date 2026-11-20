'use strict';

const {BrowserWindow, WebContentsView, clipboard, ipcMain, session} = require('electron');
const {randomUUID} = require('node:crypto');
const path = require('node:path');
const {httpURL, identifyResponse} = require('./media-candidates.cjs');

const toolbarHeight = 60;
const sidebarWidth = 350;
const candidateLimit = 150;
const filter = {urls: ['https://*/*', 'http://*/*']};

function registerSniffer({service, icon, requireAccess}) {
  let current;

  function allowPage(raw) {
    const url = httpURL(raw);
    return url && url.origin !== service.origin;
  }

  function trustedPane(event) {
    const pane = current?.window.webContents;
    return pane && event.sender === pane && event.senderFrame === pane.mainFrame &&
      event.senderFrame?.url === service.origin + '/sniffer.html';
  }

  session.defaultSession.cookies.on('changed', (_event, cookie) => {
    if (cookie.name === 'ytd_account_' + service.port) current?.window.close();
  });

  async function authorize(item) {
    const owner = await requireAccess('jobs.create');
    if (item && (owner !== item.owner || item !== current || item.window.isDestroyed())) throw new Error('嗅探窗口的登录账户已变更');
    return owner;
  }

  function handle(channel, callback) {
    ipcMain.handle(channel, async (event, ...args) => {
      if (!trustedPane(event)) throw new Error('无效的网页嗅探窗口');
      const item = current;
      try { await authorize(item); }
      catch (error) { if (!item.window.isDestroyed()) item.window.close(); throw error; }
      return callback(item, ...args);
    });
  }

  handle('sniff:state', item => item.snapshot());
  handle('sniff:navigate', (item, url) => item.open(url));
  handle('sniff:action', (item, name) => item.action(name));
  handle('sniff:copy', (item, url) => item.copy(url));

  return async function showSniffer() {
    const owner = await authorize();
    if (current && current.owner !== owner) current.window.close();
    if (current?.owner === owner && !current.window.isDestroyed()) {
      if (current.window.isMinimized()) current.window.restore();
      current.window.show();
      current.window.focus();
      return;
    }

    // A new, non-persistent session for each browser window.
    const guestSession = session.fromPartition('media-preview-' + randomUUID(), {cache: false});
    guestSession.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
    guestSession.setPermissionCheckHandler(() => false);
    guestSession.on('will-download', event => event.preventDefault());

    const window = new BrowserWindow({
      title: '网页嗅探 · YT Downloader',
      width: 1240, height: 780, minWidth: 860, minHeight: 520,
      show: false, icon, backgroundColor: '#eef1f8',
      webPreferences: {
        contextIsolation: true, nodeIntegration: false, sandbox: true,
        preload: path.join(__dirname, 'sniffer-preload.cjs'),
      },
    });
    const view = new WebContentsView({
      webPreferences: {
        session: guestSession, sandbox: true, nodeIntegration: false,
        contextIsolation: true, webSecurity: true,
      },
    });
    const page = view.webContents;
    const pane = window.webContents;
    window.contentView.addChildView(view);
    view.setVisible(false);

    let pageURL = '';
    let loading = false;
    let error = '';
    let capturing = true;
    let clipped = false;
    let shown = false;
    let pendingUpdate;
    const candidates = new Map();

    function snapshot() {
      return {
        ownerId: owner, pageURL, loading, error, capturing, clipped, shown,
        canBack: page.navigationHistory.canGoBack(),
        canForward: page.navigationHistory.canGoForward(),
        candidates: [...candidates.values()],
      };
    }

    function notify() {
      if (pendingUpdate || window.isDestroyed()) return;
      pendingUpdate = setTimeout(() => {
        pendingUpdate = undefined;
        if (!pane.isDestroyed()) pane.send('sniff:changed', snapshot());
      }, 90);
    }

    function layout() {
      const [width, height] = window.getContentSize();
      view.setBounds({x: sidebarWidth, y: toolbarHeight, width: width - sidebarWidth, height: height - toolbarHeight});
    }

    function open(raw) {
      const url = httpURL(raw);
      if (!url || !allowPage(url.href)) throw new Error('请输入有效的网页地址（HTTP 或 HTTPS）。');
      pageURL = url.href;
      error = '';
      loading = true;
      shown = true;
      view.setVisible(true);
      notify();
      // did-fail-load provides the final error, including failures after redirects.
      page.loadURL(url.href).catch(() => {});
    }

    function action(name) {
      if (name === 'clear') {
        candidates.clear();
        clipped = false;
      } else if (name === 'capture') {
        capturing = !capturing;
      } else if (name === 'back' && page.navigationHistory.canGoBack()) {
        page.navigationHistory.goBack();
      } else if (name === 'forward' && page.navigationHistory.canGoForward()) {
        page.navigationHistory.goForward();
      } else if (name === 'reload' && pageURL) {
        if (shown) page.reload();
        else open(pageURL);
      }
      notify();
    }

    function copy(url) {
      if (!candidates.has(url)) throw new Error('这条媒体地址已清除。');
      clipboard.writeText(url);
    }

    const item = {window, owner, snapshot, open, action, copy};
    current = item;
    // Changes made in another browser session do not emit a local cookie event.
    const accessTimer = setInterval(() => authorize(item).catch(() => {
      if (!window.isDestroyed()) window.close();
    }), 2000);
    layout();
    window.on('resize', layout);

    const sourceOf = (details, legacyURL) => details.url || legacyURL;
    pane.setWindowOpenHandler(() => ({action: 'deny'}));
    for (const name of ['will-navigate', 'will-redirect']) {
      pane.on(name, (event, legacyURL) => {
        const next = sourceOf(event, legacyURL);
        if (!httpURL(next) || new URL(next).origin !== service.origin) event.preventDefault();
      });
    }

    page.setWindowOpenHandler(() => ({action: 'deny'}));
    for (const name of ['will-navigate', 'will-redirect']) {
      page.on(name, (event, legacyURL) => {
        const next = sourceOf(event, legacyURL);
        if (!allowPage(next)) event.preventDefault();
      });
    }
    page.on('will-frame-navigate', event => {
      const url = event.url;
      const transientFrame = !event.isMainFrame && (url === 'about:blank' || url.startsWith('blob:') || url.startsWith('data:'));
      if (!transientFrame && !allowPage(url)) event.preventDefault();
    });
    // Block the private local service even when a page uses a subresource or an iframe.
    guestSession.webRequest.onBeforeRequest(filter, (details, callback) => {
      callback({cancel: !allowPage(details.url)});
    });
    guestSession.webRequest.onHeadersReceived(filter, (details, callback) => {
      callback({});
      if (!capturing || (details.webContentsId && details.webContentsId !== page.id)) return;
      const item = identifyResponse(details);
      if (!item) return;
      const old = candidates.get(item.url);
      if (old) {
        if (!old.bytes && item.bytes) {
          old.bytes = item.bytes;
          notify();
        }
        return;
      }
      if (candidates.size >= candidateLimit) {
        if (!clipped) { clipped = true; notify(); }
        return;
      }
      candidates.set(item.url, item);
      notify();
    });

    page.on('did-start-loading', () => { loading = true; error = ''; notify(); });
    page.on('did-stop-loading', () => { loading = false; notify(); });
    page.on('did-navigate', (_event, url) => { pageURL = url; notify(); });
    page.on('did-navigate-in-page', (_event, url, isMainFrame) => {
      if (isMainFrame) { pageURL = url; notify(); }
    });
    page.on('did-fail-load', (_event, code, description, url, isMainFrame) => {
      if (!isMainFrame || code === -3) return; // ERR_ABORTED is common during redirect or superseded navigation.
      error = '网页加载失败（' + description + '）';
      loading = false;
      if (url) pageURL = url;
      shown = false;
      view.setVisible(false);
      notify();
    });
    page.on('render-process-gone', () => {
      error = '网页停止响应。请输入网址重新打开。';
      loading = false;
      shown = false;
      view.setVisible(false);
      notify();
    });

    window.once('ready-to-show', () => window.show());
    window.on('closed', () => {
      clearTimeout(pendingUpdate);
      clearInterval(accessTimer);
      guestSession.webRequest.onBeforeRequest(null);
      guestSession.webRequest.onHeadersReceived(null);
      if (!page.isDestroyed()) page.close({waitForBeforeUnload: false});
      void Promise.allSettled([
        guestSession.clearStorageData(),
        guestSession.clearAuthCache(),
        guestSession.closeAllConnections(),
      ]);
      if (current === item) current = undefined;
    });
    const launch = new URL(service.href);
    launch.searchParams.set('view', 'sniffer');
    window.loadURL(launch.href).catch(() => window.close());
  };
}

module.exports = {registerSniffer};
