'use strict';

const accountUI = (() => {
  const by = id => document.getElementById(id);
  const dialog = by('account-dialog');
  const panel = by('account-content');
  const gate = by('account-gate');
  const feedback = by('account-feedback');
  const tabButtons = [...dialog.querySelectorAll('[data-account-tab]')];
  const permissionNames = {
    'jobs.view': '查看自己的任务', 'jobs.viewAll': '查看所有人的任务', 'jobs.create': '创建下载',
    'jobs.control': '管理自己的任务', 'jobs.controlAll': '管理所有人的任务', 'jobs.logs': '查看任务日志',
    'jobs.files': '打开下载文件', 'jobs.ai': 'AI 辅助下载', 'queue.manage': '暂停与恢复队列',
    'settings.manage': '管理应用设置', 'storage.manage': '查看与清理缓存', 'storage.reveal': '打开本机数据和全部日志目录', 'tools.manage': '管理组件',
    'app.quit': '退出应用', 'users.manage': '管理用户', 'roles.manage': '管理角色'
  };
  let current = null;
  let deployment = 'desktop';
  let setup = false;
  let busy = false;
  let gateBusy = false;
  let tab = 'profile';
  let generation = 0;
  let access = null;
  if (!document.documentElement.dataset.theme) {
    document.documentElement.dataset.theme = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }

  function node(tag, classes, text) {
    const el = document.createElement(tag);
    if (classes) el.className = classes;
    if (text != null) el.textContent = text;
    return el;
  }
  function button(text, classes, fn) {
    const el = node('button', classes, text);
    el.type = 'button';
    if (fn) el.addEventListener('click', fn);
    return el;
  }
  function field(label, id, type, value = '') {
    const wrapper = node('label', 'account-field', label);
    const input = node('input');
    input.id = id;
    input.name = id;
    input.type = type;
    input.value = value;
    input.required = true;
    if (type === 'password') input.autocomplete = id === 'account-current-password' ? 'current-password' : 'new-password';
    wrapper.append(input);
    return wrapper;
  }
  function permission(key) { return current?.permissions?.includes(key) ?? false; }

  async function api(path, body, headers = {}) {
    const options = {cache: 'no-store', credentials: 'same-origin', headers: {...headers}};
    if (current?.id && path !== '/api/auth/session') options.headers['X-YTD-User'] = current.id;
    if (body !== undefined) {
      options.method = 'POST';
      options.headers['Content-Type'] = 'application/json';
      options.body = JSON.stringify(body);
    }
    const response = await fetch(path, options);
    let data;
    try { data = await response.json(); } catch { /* A stopped backend can return an empty response. */ }
    if (!response.ok) {
      if (response.status === 401 && current && path !== '/api/auth/session') {
        const session = await api('/api/auth/session');
        sync(session);
      }
      throw new Error(data?.error || '请求失败（' + response.status + '）');
    }
    if (!data || typeof data !== 'object') throw new Error('服务返回了无效的数据，请重新连接。');
    return data;
  }

  function expired() {
    if (!current) return;
    current = null;
    gate.hidden = false;
    if (dialog.open) dialog.close();
    location.reload();
  }
  function sync(session) {
    const user = session && ('user' in session ? session.user : session);
    if (!user?.id) { expired(); return false; }
    if (current && (current.id !== user.id || ['roles', 'permissions'].some(key => {
      const prev = new Set(current[key] || []);
      return prev.size !== (user[key] || []).length || user[key]?.some(value => !prev.has(value));
    }))) {
      expired();
      return false;
    }
    current = user;
    by('nav-account-label').textContent = user.username || '账户';
    by('nav-account').title = (user.username || '') + ' · 账户与权限';
    by('nav-account').setAttribute('aria-label', by('nav-account').title);
    for (const el of tabButtons) {
      const section = el.dataset.accountTab;
      el.hidden = section !== 'profile' && !permission(section + '.manage');
    }
    gate.hidden = true;
    return true;
  }
  function showGate(isSetup, needsToken = false) {
    setup = isSetup;
    gate.hidden = false;
    by('gate-title').textContent = setup ? '创建首个管理员' : '登录下载工作台';
    by('gate-help').textContent = setup ? '创建管理员后，可以为其他用户开通账户并分配角色。' : '使用管理员分配给你的工作台账户登录。';
    by('gate-token-field').hidden = !needsToken;
    by('gate-token').required = needsToken;
    if (!needsToken) by('gate-token').value = '';
    by('gate-form').hidden = false;
    by('gate-retry').hidden = true;
    by('gate-confirm-field').hidden = !setup;
    by('gate-confirm').required = setup;
    by('gate-password').autocomplete = setup ? 'new-password' : 'current-password';
    by('gate-password').placeholder = setup ? '至少 10 个字符' : '';
    by('gate-submit').textContent = setup ? '创建管理员并登录' : '登录';
    by('gate-username').focus();
  }
  async function start() {
    try {
      const session = await api('/api/auth/session');
      deployment = session.deployment || 'desktop';
      if (session.user) return sync(session);
      showGate(session.setupRequired, session.setupTokenRequired);
    } catch (error) {
      by('gate-title').textContent = '暂时无法连接工作台';
      by('gate-help').textContent = error.message;
      by('gate-form').hidden = true;
      by('gate-retry').hidden = false;
    }
    return false;
  }
  by('gate-retry').addEventListener('click', () => location.reload());
  by('gate-form').addEventListener('submit', async event => {
    event.preventDefault();
    if (gateBusy) return;
    const problem = by('gate-error');
    problem.hidden = true;
    const password = by('gate-password').value;
    if (setup && password !== by('gate-confirm').value) {
      problem.textContent = '两次输入的密码不同。';
      problem.hidden = false;
      by('gate-confirm').focus();
      return;
    }
    gateBusy = true;
    by('gate-submit').disabled = true;
    try {
      const headers = by('gate-token').required ? {'X-YTD-Setup-Token': by('gate-token').value.trim()} : {};
      await api('/api/auth/' + (setup ? 'setup' : 'login'), {username: by('gate-username').value.trim(), password}, headers);
      location.reload();
    } catch (error) {
      if (setup) {
        try {
          const session = await api('/api/auth/session');
          if (!session.setupRequired) showGate(false);
        } catch { /* Keep the original error for the current attempt. */ }
      }
      problem.textContent = error.message;
      problem.hidden = false;
    } finally {
      gateBusy = false;
      by('gate-submit').disabled = false;
    }
  });

  function message(text = '', error = false) {
    feedback.textContent = text;
    feedback.hidden = !text;
    feedback.dataset.tone = error ? 'error' : 'success';
    if (error && text) feedback.focus();
  }
  function setBusy(value) {
    busy = value;
    panel.inert = value;
    panel.setAttribute('aria-busy', String(value));
    for (const item of tabButtons) item.disabled = value;
    by('account-close').disabled = value;
  }
  async function perform(fn) {
    if (busy) return;
    message();
    setBusy(true);
    try { await fn(); } catch (error) { message(error.message, true); }
    finally { setBusy(false); }
  }
  function submitRow(text, back) {
    const row = node('div', 'account-buttons');
    const submit = node('button', 'primary', text);
    submit.type = 'submit';
    row.append(submit);
    if (back) row.append(button('返回', 'secondary', back));
    return row;
  }
  function profile() {
    const info = node('section', 'account-profile');
    const line = node('div', 'account-line');
    const user = node('div');
    user.append(node('h3', 'account-item-name', current.username),
      node('p', 'account-item-meta', '角色 · ' + (current.roleNames?.join('、') || '尚未分配角色')));
    line.append(user, button('退出登录', 'secondary', () => perform(async () => {
      await api('/api/auth/logout', {});
      expired();
    })));
    info.append(line);
    const details = node('details');
    details.append(node('summary', '', '查看我的权限（' + current.permissions.length + '）'));
    const perms = node('ul', 'account-permissions');
    for (const key of current.permissions) perms.append(node('li', '', permissionNames[key] || key));
    details.append(perms);
    info.append(details);
    const form = node('form', 'account-form');
    form.append(node('h3', '', '修改密码'),
      field('当前密码', 'account-current-password', 'password'),
      field('新密码（至少 10 个字符）', 'account-new-password', 'password'),
      field('确认新密码', 'account-confirm-password', 'password'), submitRow('更新密码'));
    form.addEventListener('submit', event => {
      event.preventDefault();
      if (by('account-new-password').value !== by('account-confirm-password').value) {
        message('两次输入的新密码不同。', true);
        return;
      }
      perform(async () => {
        const session = await api('/api/auth/password', {
          currentPassword: by('account-current-password').value, newPassword: by('account-new-password').value
        });
        if (sync(session)) { form.reset(); message('密码已更新。'); }
      });
    });
    panel.replaceChildren(info, form);
  }

  function choices(legend, items, selected, frozen = false) {
    const fieldset = node('fieldset', 'account-options');
    const grid = node('div', 'account-option-grid');
    fieldset.append(node('legend', '', legend), grid);
    for (const item of items) {
      const label = node('label', 'account-check');
      const input = node('input');
      input.type = 'checkbox';
      input.value = item.value;
      input.checked = selected.includes(item.value);
      input.disabled = frozen || !!item.disabled;
      label.append(input, document.createTextNode(item.label));
      grid.append(label);
    }
    return fieldset;
  }
  function selected(fieldset) {
    return [...fieldset.querySelectorAll('input:checked')].map(el => el.value);
  }
  function confirmDelete(form, entity, item) {
    const actions = form.querySelector('.account-buttons');
    const del = button('删除' + entity, 'quiet account-delete', () => {
      del.hidden = true;
      const confirm = node('section', 'account-confirm');
      const question = node('p', '', '确定删除' + entity + '「' + (item.username || item.name) + '」？');
      const controls = node('div', 'account-buttons');
      const cancel = button('保留' + entity, 'secondary', () => { confirm.remove(); del.hidden = false; del.focus(); });
      const accept = button('确认删除', 'secondary account-delete', () => perform(async () => {
        await api('/api/access/' + tab + '/delete', {id: item.id});
        if (tab === 'users' && item.id === current.id) { expired(); return; }
        await loadAccess('已删除' + entity + '。');
      }));
      controls.append(cancel, accept);
      confirm.append(question, controls);
      form.append(confirm);
      cancel.focus();
    });
    actions.append(del);
  }
  function userEditor(item) {
    message();
    const form = node('form', 'account-form');
    const name = field('用户名', 'account-edit-username', 'text', item?.username);
    name.lastChild.maxLength = 64;
    name.lastChild.autocomplete = 'off';
    const pass = field(item ? '重置密码（留空保留原密码）' : '初始密码（至少 10 个字符）', 'account-edit-password', 'password');
    pass.lastChild.required = !item;
    const viewer = access.roles.find(role => role.id === 'viewer');
    const defaults = viewer?.assignable ? ['viewer'] : [];
    const roles = choices('分配角色', access.roles.map(r => ({value: r.id, label: r.name, disabled: !r.assignable})), item?.roles || defaults);
    const disabled = node('label', 'account-check');
    const toggle = node('input'); toggle.type = 'checkbox'; toggle.checked = !!item?.disabled;
    disabled.append(toggle, document.createTextNode('禁用此账户'));
    form.append(node('h3', '', item ? '编辑用户' : '创建用户'), name, pass, roles, disabled, submitRow(item ? '保存用户' : '创建用户', list));
    if (item) confirmDelete(form, '用户', item);
    form.addEventListener('submit', event => {
      event.preventDefault();
      perform(async () => {
        const data = {username: name.lastChild.value.trim(), roles: selected(roles), disabled: toggle.checked};
        if (item) data.id = item.id;
        if (pass.lastChild.value) data.password = pass.lastChild.value;
        await api('/api/access/users/save', data);
        if (item?.id === current.id) {
          const session = await api('/api/auth/session');
          if (!sync(session)) return;
        }
        await loadAccess(item ? '已保存用户。' : '已创建用户。');
      });
    });
    panel.replaceChildren(form);
    name.lastChild.focus();
  }
  function roleEditor(item) {
    message();
    const frozen = !!item && (item.builtin || !item.manageable);
    const form = node('form', 'account-form');
    const name = field('角色名称', 'account-edit-role', 'text', item?.name);
    name.lastChild.readOnly = frozen;
    form.append(node('h3', '', frozen ? '查看角色' : item ? '编辑角色' : '创建角色'), name);
    const groups = [
      ['任务权限', access.permissions.filter(p => p.key.startsWith('jobs.'))],
      ['工作台权限', access.permissions.filter(p => !p.key.startsWith('jobs.'))]
    ];
    const sets = groups.map(([label, permissions]) =>
      choices(label, permissions.map(p => ({value: p.key, label: p.label, disabled: !permission(p.key)})), item?.permissions || [], frozen));
    sets[0].append(node('p', 'account-help account-scope', '跨用户管理需同时具备本人和他人的查看、管理权限。AI 辅助下载需要任务管理权限。多个角色的权限可以合并使用。'));
    form.append(...sets);
    if (frozen) form.append(button('返回角色列表', 'secondary', list));
    else {
      form.append(submitRow(item ? '保存角色' : '创建角色', list));
      if (item) confirmDelete(form, '角色', item);
    }
    form.addEventListener('submit', event => {
      event.preventDefault();
      if (frozen) return;
      perform(async () => {
        const data = {name: name.lastChild.value.trim(), permissions: sets.flatMap(selected)};
        if (item) data.id = item.id;
        await api('/api/access/roles/save', data);
        if (item && current.roles.includes(item.id) && !sync(await api('/api/auth/session'))) return;
        await loadAccess(item ? '已保存角色。' : '已创建角色。');
      });
    });
    panel.replaceChildren(form);
    if (!frozen) name.lastChild.focus();
  }
  function list() {
    message();
    if (!access) return;
    const users = tab === 'users';
    const heading = node('div', 'account-line');
    const items = users ? access.users : access.roles;
    const entity = users ? '用户' : '角色';
    const actions = node('div', 'account-buttons');
    actions.append(button('刷新', 'quiet', () => loadAccess()),
      button('创建' + entity, 'primary', () => users ? userEditor(null) : roleEditor(null)));
    heading.append(node('h3', '', entity + '（' + items.length + '）'), actions);
    const rows = node('ul', 'account-list');
    const roleNames = new Map(access.roles.map(r => [r.id, r.name]));
    for (const item of items) {
      const row = node('li', 'account-row');
      const info = node('div');
      const name = node('span', 'account-item-name', users ? item.username : item.name);
      info.append(name);
      if (users && item.disabled) info.append(node('span', 'account-status', '已禁用'));
      if (users && current.id === item.id) info.append(node('span', 'account-status', '本人'));
      if (!users && item.builtin) info.append(node('span', 'account-status', '内置'));
      info.append(node('p', 'account-item-meta', users ? ((item.roles || []).map(id => roleNames.get(id)).filter(Boolean).join('、') || '尚未分配角色') : (item.permissions || []).length + ' 项权限'));
      row.append(info);
      if (!users || item.manageable) {
        const canEdit = item.manageable && !item.builtin;
        const edit = button(canEdit ? '编辑' : '查看', 'secondary', () => users ? userEditor(item) : roleEditor(item));
        edit.setAttribute('aria-label', edit.textContent + (users ? item.username : item.name));
        row.append(edit);
      }
      rows.append(row);
    }
    panel.replaceChildren(heading, rows);
  }
  async function loadAccess(done = '') {
    const ticket = ++generation;
    message();
    access = null;
    panel.replaceChildren(node('p', 'account-help', '正在读取权限配置…'));
    try {
      const data = await api('/api/access');
      if (ticket !== generation || !dialog.open) return;
      access = data;
      list();
      if (done) message(done);
    } catch (error) {
      if (ticket !== generation || !dialog.open) return;
      panel.replaceChildren(button('重新读取', 'secondary', () => loadAccess()));
      message(error.message, true);
    }
  }
  function selectTab(section) {
    if (busy || (section !== 'profile' && !permission(section + '.manage'))) return;
    tab = section;
    generation++;
    message();
    for (const el of tabButtons) {
      if (el.dataset.accountTab === tab) el.setAttribute('aria-current', 'page');
      else el.removeAttribute('aria-current');
    }
    if (tab === 'profile') profile(); else loadAccess();
  }
  function open() {
    if (!current) return;
    if (!dialog.open) dialog.showModal();
    selectTab('profile');
    tabButtons[0].focus();
  }
  by('nav-account').addEventListener('click', open);
  for (const el of tabButtons) el.addEventListener('click', () => selectTab(el.dataset.accountTab));
  by('account-close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); });
  dialog.addEventListener('close', () => {
    generation++;
    access = null;
    panel.replaceChildren();
    message();
  });
  return {start, permission, sync, open, expired, isWeb: () => deployment === 'web', userID: () => current?.id || ''};
})();

function accountStart() { return accountUI.start(); }
function hasPermission(key) { return accountUI.permission(key); }
function accountUserID() { return accountUI.userID(); }
function openAccount() { accountUI.open(); }
function accountExpired() { accountUI.expired(); }
function syncAccountSession(session) { return accountUI.sync(session); }

function isWebDeployment() { return accountUI.isWeb(); }
