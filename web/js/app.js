const API = '';

// HTML 转义（防 XSS）
function esc(s) {
  if (s === null || s === undefined) return '';
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#x27;');
}

// ==================== 工具 ====================
let authRedirecting = false; // 401 只引导一次，避免多个轮询同时触发重复跳转
async function api(path, options = {}) {
  const token = localStorage.getItem('token');
  // 默认超时：无超时的 fetch 在连接挂起（公网抖动）时会永久 pending，
  // 页面停在"登录页/主界面都未显示"的空白态；默认 60s，可传 timeoutMs 覆盖
  const init = { ...options };
  const timeoutMs = init.timeoutMs !== undefined ? init.timeoutMs : 60000;
  delete init.timeoutMs;
  if (!init.signal && timeoutMs > 0 && typeof AbortSignal !== 'undefined' && AbortSignal.timeout) {
    init.signal = AbortSignal.timeout(timeoutMs);
  }
  // 跨境链路对连接的掐断是按概率的（同一批请求有的成功有的 RESET）：
  // GET 幂等，网络层失败（TypeError "Failed to fetch"）自动换新连接重试
  // 一次，失败率从 p 降到 p²；POST 不重试，防重复提交
  const isGet = !init.method || String(init.method).toUpperCase() === 'GET';
  for (let attempt = 0; ; attempt++) {
    try {
      return await apiOnce(path, init, token);
    } catch (e) {
      if (!(isGet && attempt === 0 && e instanceof TypeError)) throw e;
      await new Promise(r => setTimeout(r, 350 + Math.random() * 450));
    }
  }
}

async function apiOnce(path, init, token) {
  const res = await fetch(API + '/api' + path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: 'Bearer ' + token } : {}),
      ...init.headers,
    },
  });
  // 仅当 401 来自鉴权中间件（固定文案 未登录/登录已过期）才清令牌回登录页；
  // 业务接口的 401（影巢登录未通过、115 OpenAPI 校验失败等）按普通错误提示，
  // 否则点一次影巢登录就把后台登录态误清了
  if (res.status === 401) {
    const data401 = await res.json().catch(() => ({}));
    const msg401 = data401.error || data401.message || '';
    if (msg401 === '未登录' || msg401 === '登录已过期') {
      if (!authRedirecting) {
        authRedirecting = true;
        localStorage.removeItem('token');
        stopLogPoll();
        showAuth(false);
        toast('登录已过期，请重新登录');
        setTimeout(() => { authRedirecting = false; }, 1000);
      }
      throw new Error('登录已过期');
    }
    throw new Error(msg401 || '请求失败');
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.message || data.error || '请求失败');
  return data;
}

// 统一的「测试结果」渲染：所有配置卡的状态横幅走这里
// showTestResult(el, ok, '连接成功', 'emby v4.9.5.0 · 2 个媒体库')
// showTestPending(el, '测试中...') / showTestResult(el, null) = 清空
function showTestResult(el, ok, title, detail) {
  if (!el) return;
  if (ok === null) { el.innerHTML = ''; return; }
  const cls = ok === 'pend' ? 'pend' : (ok ? 'ok' : 'err');
  el.innerHTML = '<div class="test-banner ' + cls + '">'
    + '<span class="tb-ico">' + (ok === 'pend' ? '…' : (ok ? '✓' : '✕')) + '</span>'
    + '<div class="tb-body"><div class="tb-title">' + esc(title || '') + '</div>'
    + (detail ? '<div class="tb-detail">' + esc(detail) + '</div>' : '')
    + '</div></div>';
}
function showTestPending(el, text) { showTestResult(el, 'pend', text || '测试中…'); }

function toast(msg) {
  let el = document.getElementById('toast');
  if (!el) {
    el = document.createElement('div');
    el.id = 'toast';
    el.style.cssText = 'position:fixed;top:20px;left:50%;transform:translateX(-50%);background:#1d2129;color:#fff;padding:8px 20px;border-radius:4px;font-size:14px;z-index:2000;box-shadow:0 2px 8px rgba(0,0,0,.2);transition:opacity .3s';
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.style.opacity = '1';
  clearTimeout(el._t);
  el._t = setTimeout(() => { el.style.opacity = '0'; }, 2000);
}

// ==================== 页面标题映射 ====================
const PAGE_TITLES = {
  'sync': ['115 账号同步', '全量 / 增量 / 分享同步'],
  'organize': ['自动整理', '基础配置 / 识别规则 / 分类策略 / 洗版 / 重命名'],
  'upload-download': ['上传下载', '监控上传 / 转存下载'],
  'media-transfer': ['影视转存', '观影种子搜索 / 115 离线下载'],
  'dashboard': ['总览面板', '容量 / STRM / 整理 / 任务总览'],
  'config-accounts': ['账号管理', '管理各云盘账号配置'],
  'config-system': ['系统配置', 'STRM / TMDB / 代理 / EMBY 配置'],
  'config-message': ['消息配置', '企业微信与 TG 机器人'],
  'config-extension': ['扩展功能', '签到 / TG 搜索 / 封面生成等插件'],
  'tgsub': ['订阅管理', 'TG 频道关键词订阅 / 命中通知 / 自动转存'],
  'logs': ['实时日志', '同步与整理操作的服务端与本地日志'],
  'cd2': ['CloudDrive2', '多云盘聚合 · 跨网盘整理'],
};

// ==================== 前端路由（真实路径，与后端 NoRoute 回退 index.html 配合） ====================
// 切换页面时地址栏变为 /sync、/plugins 等真实路径；直接访问/刷新/收藏/前进后退均可恢复。
const PAGE_PATHS = {
  'dashboard': '/',
  'sync': '/sync',
  'organize': '/organize',
  'upload-download': '/upload-download',
  'media-transfer': '/media-transfer',
  'config-accounts': '/accounts',
  'config-system': '/settings',
  'config-message': '/message',
  'logs': '/logs',
  'config-extension': '/plugins',
  'tgsub': '/subscriptions',
  'cd2': '/cd2',
};
const PATH_PAGES = Object.fromEntries(Object.entries(PAGE_PATHS).map(([p, path]) => [path, p]));

function pathToPageId(path) { return PATH_PAGES[path] || null; }

function syncRoute(id) {
  const want = PAGE_PATHS[id] || '/';
  if (location.pathname !== want) history.pushState(null, '', want);
}

// 浏览器前进/后退时按当前路径切页（showPage 内 syncRoute 不会再压栈）
window.addEventListener('popstate', () => {
  const id = pathToPageId(location.pathname);
  if (id) showPage(id);
});

function showPage(id) {
  if (!id) return;
  if (id === 'logs') {
    startLogPoll(); // 内部已首发加载，此前这里再调一次造成进页双请求
  } else { stopLogPoll(); }
  if (id === 'dashboard') loadDashboard();
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.querySelectorAll('.menu-item').forEach(n => n.classList.remove('active'));
  const page = document.getElementById('page-' + id);
  const nav = document.querySelector(`.menu-item[data-page="${id}"]`);
  if (page) page.classList.add('active');
  if (nav) nav.classList.add('active');
  const title = PAGE_TITLES[id] || [id, ''];
  document.getElementById('page-title').textContent = title[0];
  document.getElementById('page-desc').textContent = title[1];
  const mobileTitle = document.getElementById('mobile-page-title');
  if (mobileTitle) mobileTitle.textContent = title[0];
  // 记住当前页面，刷新后恢复
  localStorage.setItem('current-page', id);
  // 地址栏同步为真实路径（/sync、/plugins 等），可刷新/收藏/前进后退
  syncRoute(id);
  // 移动端切页面后关闭侧边栏
  if (window.innerWidth <= 768) closeSidebar();
  // 加载对应数据
  if (id === 'config-system') {
    loadTmdb();
    loadConfigs();
    updateStrmExample();
  }
  if (id === 'config-accounts') { loadAccount(); }
  if (id === 'config-extension') ck115LoadPage();
  if (id === 'organize') {
    loadConfigs();
    loadCategory();
    scrapeLoadPage();
    loadWash();
  }
  if (id === 'sync') { loadConfigs(); previewCron(); }
  if (id === 'upload-download') { loadConfigs(); startOfflineTasksPoll(); }
  else stopOfflineTasksPoll();
  if (id === 'media-transfer') { gyLoadPage(); pansouLoadPage(); mukakuLoadPage(); re0LoadPage(); }
  if (id === 'cd2') cd2LoadUI();
  if (id === 'tgsub') tgSubLoadPage();
  if (id === 'config-message') loadConfigs();
  if (id === 'dashboard') loadGuide();
  // 恢复上次停留的 Tab（所有含 tab 的页面通用）
  const savedTab = localStorage.getItem('current-tab-page-' + id);
  if (savedTab) switchTab('page-' + id, savedTab);
  // 隐藏页面里初始化的 CodeMirror 测量为 0（高亮/行号错位、高度塌缩），显示后重算
  refreshVisibleCM(document.getElementById('page-' + id));
}

// ==================== 移动端侧边栏 ====================
function toggleSidebar(forceOpen) {
  const sidebar = document.querySelector('.sidebar');
  const overlay = document.getElementById('sidebar-overlay');
  const isOpen = sidebar.classList.contains('open');
  if (forceOpen === true || !isOpen) {
    sidebar.classList.add('open');
    overlay.classList.add('show');
  } else {
    closeSidebar();
  }
}
function closeSidebar() {
  document.querySelector('.sidebar')?.classList.remove('open');
  document.getElementById('sidebar-overlay')?.classList.remove('show');
}

// Tab 切换（通用，作用于指定页面容器）
function switchTab(pageId, tabName) {
  const page = document.getElementById(pageId);
  if (!page) return;
  // 记忆的 tab 可能已随功能下线被移除：回落到首个可用 tab
  if (tabName && !page.querySelector(`.tab[data-tab="${tabName}"]`)) {
    const first = page.querySelector('.tab[data-tab]');
    if (first) tabName = first.dataset.tab;
  }
  // 只作用于外层 tab/panel（data-tab/data-panel）；子 tab（data-subtab/
  // data-subpanel，如基础配置的 115）由 orgSubTab 自己管理
  page.querySelectorAll('.tab[data-tab]').forEach(t => t.classList.remove('active'));
  page.querySelectorAll('.tab-panel[data-panel]').forEach(p => p.classList.remove('active'));
  const tab = page.querySelector(`.tab[data-tab="${tabName}"]`);
  const panel = page.querySelector(`.tab-panel[data-panel="${tabName}"]`);
  if (tab) tab.classList.add('active');
  if (panel) {
    panel.classList.add('active');
    // 元素可见后重新计算自适应高度
    panel.querySelectorAll('textarea.auto-resize').forEach(autoResizeTextarea);
  }
  // 隐藏面板里初始化的 CodeMirror 宽高测量为 0：面板显示后必须 refresh
  refreshVisibleCM(panel || page);
  // 记住当前 Tab（所有页面通用）
  localStorage.setItem('current-tab-' + pageId, tabName);
}

// 刷新作用域内可见的 CodeMirror 实例（display 变化后浏览器需重排，
// 立即刷一次 + 下一帧再刷一次，保证测量准确）
function refreshVisibleCM(scope) {
  (window._cmInstances || []).forEach(cm => {
    try {
      const wrap = cm.getWrapperElement();
      if ((!scope || scope.contains(wrap)) && wrap.offsetParent !== null) {
        cm.refresh();
        requestAnimationFrame(() => { try { cm.refresh(); } catch (e) {} });
      }
    } catch (e) {}
  });
}

// ==================== 认证 ====================
// 账号来源：容器环境变量 AUTH_USER / AUTH_PASSWORD（未配置时首启自动生成
// 随机密码，见容器日志）。网页注册功能已移除
async function checkAuth() {
  try {
    // 短超时：鉴权状态挂起时快速落到登录页，而不是让整页停在空白
    const data = await api('/auth/status', { timeoutMs: 10000 });
    if (!localStorage.getItem('token')) {
      showAuth(!data.initialized);
    } else {
      showMain();
    }
  } catch (e) {
    showAuth(false);
  }
}

function showAuth(notInitialized) {
  stopTaskPoll(); // 登录页不轮询任务状态（否则每 5 秒刷一屏 401 报错）
  document.getElementById('auth-page').style.display = 'flex';
  document.getElementById('main-app').style.display = 'none';
  // 地址栏显示 /login；但保留深链接路径（如直接访问 /plugins）以便登录后跳回目标页
  if (location.pathname === '/' || location.pathname === '/login') {
    history.replaceState(null, '', '/login');
  }
  document.getElementById('auth-heading').textContent = notInitialized
    ? '尚未设置管理员账号'
    : '请输入账号密码登录';
  document.getElementById('auth-subtitle').innerHTML = notInitialized
    ? '请在 docker-compose 中配置环境变量 <b>AUTH_USER</b> / <b>AUTH_PASSWORD</b> 后重启容器<br>（未配置时首次启动已生成随机账号，见容器日志）'
    : '115 媒体库管理 · STRM 自动生成';
  document.getElementById('auth-submit').textContent = '登录';
}

function showMain() {
  document.getElementById('auth-page').style.display = 'none';
  document.getElementById('main-app').style.display = 'flex';
  startTaskPoll(); // 登录后才轮询任务状态（登录页轮询只会刷 401）
  loadVersion();
  // 优先按地址栏路径恢复（深链接 / 刷新），其次上次停留页面，默认仪表盘
  const fromPath = pathToPageId(location.pathname);
  const saved = localStorage.getItem('current-page');
  const target = fromPath || (saved && document.getElementById('page-' + saved) ? saved : 'dashboard');
  showPage(target);
}

async function handleAuth(e) {
  e.preventDefault();
  const username = document.getElementById('auth-username').value;
  const password = document.getElementById('auth-password').value;
  if (!username || !password) { toast('请输入账号和密码'); return; }
  try {
    const data = await api('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) });
    localStorage.setItem('token', data.token);
    localStorage.setItem('username', data.username);
    showMain();
    toast('登录成功');
  } catch (e) { toast(e.message); }
}

// ==================== TMDB 配置 ====================
let tmdbLangVal = 'zh-CN';
function setTmdbLang(val) {
  tmdbLangVal = val;
  document.querySelectorAll('#tmdb-language-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === val));
}

async function loadTmdb() {
  try {
    const data = await api('/config/tmdb');
    const c = data.data || data;
    document.getElementById('tmdb-api-url').value = c.api_url || 'https://api.tmdb.org';
    document.getElementById('tmdb-image-url').value = c.image_api_url || 'https://image.tmdb.org';
    document.getElementById('tmdb-api-key').value = c.api_key || '';
    setTmdbLang(c.language || 'zh-CN');
  } catch (e) {}
}

async function saveTmdb() {
  try {
    await api('/config/tmdb', { method: 'POST', body: JSON.stringify({
      api_url: document.getElementById('tmdb-api-url').value,
      image_api_url: document.getElementById('tmdb-image-url').value,
      api_key: document.getElementById('tmdb-api-key').value,
      language: tmdbLangVal,
    }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

// ==================== 二级分类 / 洗版 ====================
async function loadCategory() {
  try {
    const data = await api('/scrape/categories');
    if (data.config) {
      const catTa = document.getElementById('category-yaml');
      catTa.value = data.config;
      catTa._yamlRender && catTa._yamlRender();
    }
  } catch (e) {}
}

async function saveCategory() {
  const ta = document.getElementById('category-yaml');
  ta._yamlSync && ta._yamlSync();
  const yaml = ta.value;
  try {
    await api('/scrape/categories', { method: 'POST', body: JSON.stringify({ yaml }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

const DEFAULT_CATEGORY_YAML = `# 配置电影的分类策略
# 分类名即 115 目录名（支持多级，用 / 分隔）
# 整理后路径：{已存在目录}/{分类名}/{重命名文件}
movie:
  # 如已存在目录为「影视库」，整理结果为：影视库/电影/动画电影/xxx
  电影/动画电影:
    # 匹配 genre_ids 内容类型，16是动漫
    genre_ids: '16'
  电影/华语电影:
    # 匹配语种
    original_language: 'zh,cn,bo,za'
  # 未匹配以上条件时，返回最后一个
  电影/外语电影:

# 配置电视剧的分类策略
tv:
  电视剧/国漫:
    genre_ids: '16'
    origin_country: 'CN,TW,HK'
  电视剧/日番:
    genre_ids: '16'
    origin_country: 'JP'
  电视剧/国产剧:
    origin_country: 'CN,TW,HK'
  电视剧/欧美剧:
    origin_country: 'US,FR,GB,DE,ES,IT,NL,PT,RU'
  电视剧/日韩剧:
    origin_country: 'JP,KP,KR,TH,IN,SG'
  # 未匹配以上分类，则命名为未分类
  电视剧/未分类:

`;

function resetCategory(btn) {
  resetConfig('category', btn);
}

async function loadWash() {
  try {
    const data = await api('/scrape/wash');
    if (data.config) {
      const washTa = document.getElementById('wash-yaml');
      washTa.value = data.config;
      washTa._yamlRender && washTa._yamlRender();
    }
  } catch (e) {}
}
async function saveWash() {
  const ta = document.getElementById('wash-yaml');
  ta._yamlSync && ta._yamlSync();
  const yaml = ta.value;
  try {
    await api('/scrape/wash', { method: 'POST', body: JSON.stringify({ yaml }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

// ==================== 后缀标签输入 ====================
function addTag(e) {
  if (e.key !== 'Enter') return;
  e.preventDefault();
  const containerId = e.target.dataset.tags;
  const container = document.getElementById(containerId);
  const val = e.target.value.trim().replace(/^\.+/, '').toLowerCase();
  if (!val) return;
  if (container.querySelector(`.tag[data-val="${val}"]`)) { e.target.value = ''; return; }
  const tag = document.createElement('span');
  tag.className = 'tag';
  tag.dataset.val = val;
  tag.innerHTML = val + '<span class="tag-close" onclick="removeTag(this)">×</span>';
  container.insertBefore(tag, e.target);
  e.target.value = '';
}
function removeTag(el) {
  el.closest('.tag').remove();
}
function getTags(containerId) {
  return Array.from(document.querySelectorAll(`#${containerId} .tag`)).map(t => t.dataset.val);
}

// 全量同步确认气泡（全量重建耗时长且仅手动触发）
function confirmFullSync(btn) {
  closeConfirmBubble();
  document.removeEventListener('click', closeConfirmBubbleOnOutside);
  const bubble = document.createElement('div');
  bubble.id = 'confirm-bubble';
  bubble.className = 'confirm-bubble';
  bubble.innerHTML = '<div class="cb-text">确定全量同步？</div><div class="cb-actions"><button class="cb-cancel">取消</button><button class="cb-ok cb-danger">确定同步</button></div>';
  document.body.appendChild(bubble);
  // fixed 定位到按钮上方
  const rect = btn.getBoundingClientRect();
  bubble.style.position = 'fixed';
  bubble.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
  bubble.style.left = 'auto';
  bubble.style.bottom = (window.innerHeight - rect.top + 8) + 'px';
  bubble.classList.add('show');
  bubble.querySelector('.cb-cancel').onclick = (e) => { e.stopPropagation(); closeConfirmBubble(); };
  bubble.querySelector('.cb-ok').onclick = (e) => {
    e.stopPropagation();
    closeConfirmBubble();
    pollTaskStatus();
    startFullSync();
  };
  setTimeout(() => {
    document.addEventListener('click', closeConfirmBubbleOnOutside, { once: true });
  }, 0);
}

// 增量同步确认气泡
function confirmIncrementalSync(btn) {
  closeConfirmBubble();
  document.removeEventListener('click', closeConfirmBubbleOnOutside);
  const bubble = document.createElement('div');
  bubble.id = 'confirm-bubble';
  bubble.className = 'confirm-bubble';
  bubble.innerHTML = '<div class="cb-text">确定增量同步？</div><div class="cb-actions"><button class="cb-cancel">取消</button><button class="cb-ok cb-danger">确定同步</button></div>';
  document.body.appendChild(bubble);
  // fixed 定位到按钮上方
  const rect = btn.getBoundingClientRect();
  bubble.style.position = 'fixed';
  bubble.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
  bubble.style.left = 'auto';
  bubble.style.bottom = (window.innerHeight - rect.top + 8) + 'px';
  bubble.classList.add('show');
  bubble.querySelector('.cb-cancel').onclick = (e) => { e.stopPropagation(); closeConfirmBubble(); };
  bubble.querySelector('.cb-ok').onclick = (e) => {
    e.stopPropagation();
    closeConfirmBubble();
    pollTaskStatus();
    startIncrementalSync();
  };
  setTimeout(() => {
    document.addEventListener('click', closeConfirmBubbleOnOutside, { once: true });
  }, 0);
}

// Cron 未来运行时间预览（防抖 500ms）
let cronPreviewTimer = null;
let cronPreviewLast = ''; // 上次已展示的表达式（相同则跳过，避免闪烁）
async function previewCron() {
  clearTimeout(cronPreviewTimer);
  cronPreviewTimer = setTimeout(async () => {
    const expr = document.getElementById('incr-cron').value.trim();
    const el = document.getElementById('incr-cron-preview');
    if (!expr) { el.textContent = '请填写 cron 表达式'; cronPreviewLast = ''; return; }
    if (expr === cronPreviewLast) return; // 未变化不重绘
    try {
      const d = await api('/sync/cron-preview', { method: 'POST', body: JSON.stringify({ cron: expr }) });
      const times = d.next || [];
      if (times.length === 0) {
        el.textContent = '○ 未来一年内不会触发，请检查表达式';
      } else {
        el.innerHTML = times.map((t, i) => `第 ${i + 1} 次: ${esc(t)}`).join('<br>');
      }
      cronPreviewLast = expr;
    } catch (e) {
      el.textContent = '✗ ' + (e.message || '表达式无效');
      cronPreviewLast = '';
    }
  }, 500);
}

// ==================== 任务状态（同步/整理互斥提示） ====================
let taskPollTimer = null;
async function pollTaskStatus() {
  const bar = document.getElementById('task-status-bar');
  const btns = ['btn-fullsync', 'btn-incrsync', 'btn-organize'].map(id => document.getElementById(id)).filter(Boolean);
  try {
    const st = await api('/sync/status');
    if (st.running) {
      bar.style.display = 'block';
      bar.innerHTML = '▶ ' + esc(st.task || '任务') + ' 正在执行（已运行 ' + esc(st.elapsed || '-') + '）'
        + (st.progress ? '<br><span style="color:var(--text-2)">▸ ' + esc(st.progress) + '</span>' : '');
      btns.forEach(b => { b.disabled = true; b.style.opacity = '.5'; });
    } else {
      // 空闲时展示最近任务（简易任务中心：成败/耗时/开始时间）
      const runs = st.recent || [];
      if (runs.length) {
        bar.style.display = 'block';
        bar.innerHTML = '<span style="font-size:12px;color:var(--text-3)">最近任务：</span>' + runs.slice(0, 3).map(r =>
          '<span style="font-size:12px;margin-right:14px">' + (r.ok ? '✓' : '✗') + ' ' + esc(r.name) + '（' + esc(r.elapsed) + '，' + esc(r.start) + '）</span>').join('');
      } else {
        bar.style.display = 'none';
      }
      btns.forEach(b => { b.disabled = false; b.style.opacity = ''; });
    }
  } catch (e) { /* 静默 */ }
}
function startTaskPoll() {
  stopTaskPoll();
  pollTaskStatus();
  taskPollTimer = setInterval(pollTaskStatus, 5000);
}
function stopTaskPoll() {
  if (taskPollTimer) { clearInterval(taskPollTimer); taskPollTimer = null; }
}

// ==================== 密钥输入遮罩 ====================
// 所有 type=password 的输入框支持双击切换明文（title 提示）
document.addEventListener('dblclick', e => {
  const el = e.target;
  if (el && el.tagName === 'INPUT' && el.type === 'password') {
    el.type = 'text';
    el.dataset.masked = '1';
    el.title = '双击恢复遮罩';
  } else if (el && el.tagName === 'INPUT' && el.type === 'text' && el.dataset.masked === '1') {
    el.type = 'password';
    el.title = '双击显示明文';
  }
});

// ==================== 首启引导 ====================
async function loadGuide() {
  const card = document.getElementById('guide-card');
  if (!card) return;
  try {
    const g = await api('/system/guide');
    if (g.coreDone) { card.style.display = 'none'; return; }
    const steps = [
      [g.pan115, '绑定 115 账号（账号管理 → 扫码登录）', 'config-accounts'],
      [g.tmdb, '填写 TMDB API Key（系统配置 → TMDB 配置）', 'config-system'],
      [g.orgDirs, '选择待整理目录（自动整理 → 基础配置）', 'organize'],
      [g.synced, '执行首次全量同步（账号同步 → 开始全量同步）', 'sync'],
      [g.emby, '对接 Emby（可选，系统配置 → EMBY 管理）', 'config-system'],
      [g.notify, '配置通知（可选，消息配置）', 'config-message'],
    ];
    document.getElementById('guide-steps').innerHTML = steps.map(([ok, text, page]) =>
      '<div>' + (ok ? '✅' : '⬜') + ' <span style="' + (ok ? 'text-decoration:line-through;color:var(--text-3)' : '') + '">' + text + '</span>'
      + (ok ? '' : ' <a href="javascript:void(0)" onclick="showPage(\'' + page + '\')" style="color:var(--primary)">去设置 →</a>') + '</div>').join('');
    card.style.display = '';
  } catch (e) { /* 引导失败静默 */ }
}

// ==================== 目录选择器 ====================
let dirPicker = { mode: '115', cid: '0', path: '', trail: [], history: [] }; // trail: 115 逐级目录名
let dirPickerTarget = 'full-cid'; // 选择后回填的输入框 id

function showDirPicker(title) {
  document.getElementById('dir-picker-title').textContent = title;
  document.getElementById('dir-picker-modal').style.display = 'flex';
}
function closeDirPicker() {
  document.getElementById('dir-picker-modal').style.display = 'none';
}

function open115DirPicker(targetId) {
  dirPickerTarget = targetId || 'full-cid';
  dirPicker = { mode: '115', cid: '0', path: '', trail: [], history: [] };
  showDirPicker('选择 115 目录');
  load115Dirs('0');
}
function openLocalDirPicker(targetId) {
  dirPickerTarget = targetId || 'full-local';
  dirPicker = { mode: 'local', cid: '0', path: '', history: [] };
  showDirPicker('选择本地目录');
  loadLocalDirs('');
}
function openCd2DirPicker(targetId) {
  dirPickerTarget = targetId || 'cd2-root';
  dirPicker = { mode: 'cd2', path: '/', history: [] };
  showDirPicker('选择 CD2 目录');
  loadCd2Dirs('/');
}

// load115Dirs 加载 115 目录；opts.enter = 进入的目录名，opts.restore = 上级恢复的 {cid, trail}
async function load115Dirs(cid, opts) {
  const list = document.getElementById('dir-picker-list');
  list.innerHTML = '<div class="dir-empty">加载中...</div>';
  try {
    if (opts && opts.enter) {
      dirPicker.history.push({ cid: dirPicker.cid, trail: [...dirPicker.trail] });
      dirPicker.trail.push(opts.enter);
    } else if (opts && opts.restore) {
      dirPicker.trail = opts.restore;
    } else {
      dirPicker.trail = []; // 根目录 / 手动跳转
    }
    const data = await api('/storage/115/dirs?cid=' + encodeURIComponent(cid));
    dirPicker.cid = cid;
    document.getElementById('dir-picker-path').textContent = dirPicker.trail.length ? '/' + dirPicker.trail.join('/') : '根目录';
    const items = data.data || [];
    let note = '';
    if (!items.length && data.count > 0) {
      note = '目录共有 ' + data.count + ' 个条目，但没有识别到文件夹（通道: ' + (data.channel || '?') + '，来源: ' + (data.origin || '?') + '）';
    }
    renderDirList(items, cid, note);
  } catch (e) {
    list.innerHTML = '<div class="dir-empty">' + esc(e.message || '加载失败') + '</div>';
  }
}
async function loadLocalDirs(path) {
  const list = document.getElementById('dir-picker-list');
  list.innerHTML = '<div class="dir-empty">加载中...</div>';
  try {
    const data = await api('/storage/local/dirs?path=' + encodeURIComponent(path));
    dirPicker.path = path;
    document.getElementById('dir-picker-path').textContent = path || '计算机';
    const note = data.truncated ? '目录较大，仅显示前 1000 个文件夹，可直接在上方输入路径' : '';
    renderDirList(data.data || [], path, note);
  } catch (e) {
    list.innerHTML = '<div class="dir-empty">' + esc(e.message || '加载失败') + '</div>';
  }
}
async function loadCd2Dirs(path) {
  const list = document.getElementById('dir-picker-list');
  list.innerHTML = '<div class="dir-empty">加载中...</div>';
  try {
    const data = await api('/cd2/dirs?path=' + encodeURIComponent(path || '/'));
    dirPicker.path = data.path || path || '/';
    document.getElementById('dir-picker-path').textContent = dirPicker.path;
    renderDirList(data.data || [], dirPicker.path, '');
  } catch (e) {
    list.innerHTML = '<div class="dir-empty">' + esc(e.message || '加载失败') + '</div>';
  }
}

// 手动输入路径/cid 直接跳转（115 支持 "/路径/跳转" 和纯数字 cid 两种写法）
async function dirPickerJump() {
  const input = document.getElementById('dir-picker-input');
  const v = (input.value || '').trim();
  if (!v) return;
  if (dirPicker.mode === '115') {
    if (/^\d+$/.test(v)) {
      load115Dirs(v, {});
      return;
    }
    // 路径写法：后端逐段解析成 cid 再跳转，并把面包屑设为该路径
    try {
      const data = await api('/storage/115/resolve?path=' + encodeURIComponent(v));
      if (data && data.cid) {
        dirPicker.trail = v.replace(/^\/+|\/+$/g, '').split('/').filter(Boolean);
        load115Dirs(data.cid, { restore: dirPicker.trail });
      }
    } catch (e) {
      document.getElementById('dir-picker-list').innerHTML =
        '<div class="dir-empty">' + esc(e.message || '路径无法解析') + '</div>';
    }
  } else if (dirPicker.mode === 'cd2') {
    loadCd2Dirs(v.startsWith('/') ? v : '/' + v);
  } else {
    loadLocalDirs(v);
  }
}

function renderDirList(items, current, note) {
  const list = document.getElementById('dir-picker-list');
  if (!items.length) {
    list.innerHTML = '<div class="dir-empty">' + (note || '该目录下没有子文件夹') + '</div>';
    return;
  }
  const noteHtml = note ? '<div class="dir-empty" style="opacity:.7">' + note + '</div>' : '';
  list.innerHTML = noteHtml + items.map((it, i) => {
    const name = it.name || it.path;
    return `<div class="dir-item" data-index="${i}"><span class="dir-icon">▸</span><span>${esc(name)}</span></div>`;
  }).join('');
  list.querySelectorAll('.dir-item').forEach(el => {
    el.addEventListener('click', () => {
      const it = items[parseInt(el.dataset.index)];
      if (dirPicker.mode === '115') {
        load115Dirs(it.cid, { enter: it.name });
      } else if (dirPicker.mode === 'cd2') {
        loadCd2Dirs(it.path);
      } else {
        loadLocalDirs(it.path);
      }
    });
  });
}

function dirPickerUp() {
  if (dirPicker.mode === '115') {
    const prev = dirPicker.history.pop();
    if (!prev) return; // 已在根目录
    load115Dirs(prev.cid, { restore: prev.trail });
  } else if (dirPicker.mode === 'cd2') {
    const parts = (dirPicker.path || '/').replace(/\/+$/, '').split('/').filter(Boolean);
    parts.pop();
    loadCd2Dirs('/' + parts.join('/'));
  } else {
    loadLocalDirs(parentPath(dirPicker.path || ''));
  }
}

function parentPath(p) {
  p = p.replace(/[\\/]+$/, '');
  const idx = Math.max(p.lastIndexOf('\\'), p.lastIndexOf('/'));
  if (idx <= 0) return '';
  return p.slice(0, idx + 1);
}

function confirmDirPicker() {
  const target = document.getElementById(dirPickerTarget);
  if (dirPicker.mode === '115') {
    if (target) {
      // 输入框显示可读路径，真实 cid 存 dataset 供同步/保存使用；
      // dataset.path 记录 cid 对应的路径，用于检测用户手改路径后的 cid 失配
      target.dataset.cid = dirPicker.cid;
      target.dataset.path = dirPicker.trail.length ? '/' + dirPicker.trail.join('/') : '';
      target.value = dirPicker.trail.length ? '/' + dirPicker.trail.join('/') : '';
      target.placeholder = '根目录';
    }
  } else if (dirPicker.mode === 'cd2') {
    if (target) target.value = dirPicker.path || '/';
  } else {
    if (target) target.value = dirPicker.path || '/media';
    // 本地目录选择即时生效：监控目录这类"选完即用"的配置自动保存，
    // 避免忘记点保存导致刷新后丢失
    if (dirPickerTarget === 'monitor-dir') {
      saveConfig('monitor').then(() => toast('监控目录已保存')).catch(() => {});
    }
  }
  closeDirPicker();
}

// resolveInputCID 把输入框里手填的路径解析成 cid（调后端逐段匹配），
// 成功后写回 dataset.cid/dataset.path。防抖后由输入事件触发，
// 也在同步/保存前主动 await 一次兜底
async function resolveInputCID(inputId) {
  const el = document.getElementById(inputId);
  if (!el) return;
  const v = (el.value || '').trim();
  if (!v || /^\d+$/.test(v)) return;
  if (el.dataset.cid && el.dataset.cid !== '0' && el.dataset.path === v) return; // cid 与路径已对上号
  try {
    const data = await api('/storage/115/resolve?path=' + encodeURIComponent(v));
    if (data && data.cid) {
      el.dataset.cid = data.cid;
      el.dataset.path = v;
    }
  } catch (e) { /* 解析失败保留原 dataset.cid，提交时由 resolveCID 拦截 */ }
}

// attachCIDResolvers 给 115 目录输入框挂防抖解析（手填路径自动换算 cid）
const CID_INPUTS = ['full-cid', 'share-folder', 'org-pending', 'org-existing', 'org-redundant'];
function attachCIDResolvers() {
  CID_INPUTS.forEach(id => {
    const el = document.getElementById(id);
    if (!el) return;
    let timer = null;
    el.addEventListener('input', () => {
      clearTimeout(timer);
      timer = setTimeout(() => resolveInputCID(id), 600);
    });
  });
}

// resolveCID 取输入框对应的 115 cid。
// 优先级：纯数字输入 > 与当前路径匹配的 dataset.cid；都不满足返回 ''。
// 关键：dataset.cid 只有在记录的 dataset.path 等于当前输入值时才可信，
// 否则用户手改过路径而 cid 还是旧目录的——静默用旧 cid 会同步错目录
function resolveCID(inputId) {
  const el = document.getElementById(inputId);
  const v = (el.value || '').trim();
  if (/^\d+$/.test(v)) return v;
  if (el.dataset && el.dataset.cid && el.dataset.cid !== '0') {
    if (el.dataset.path === undefined || el.dataset.path === v) return el.dataset.cid;
  }
  return '';
}

// ==================== 全量同步 ====================
async function startFullSync() {
  // 手填路径先解析成 cid（防抖解析可能还没触发），解析不了就拒绝执行，
  // 绝不能拿旧 dataset.cid 静默同步错误的目录
  await resolveInputCID('full-cid');
  const cid = resolveCID('full-cid');
  if (!cid || cid === '0') { toast('无法识别 115 目录：请点「选择目录」重新选择，或直接输入纯数字 cid'); return; }
  const videoExt = getTags('video-ext');
  if (!videoExt.length) { toast('请至少保留一个视频文件后缀'); return; }
  try {
    toast('全量同步进行中（受 API 间隔限制可能持续数分钟）...');
    appendLog('开始全量同步（视频生成 STRM，图片/字幕/NFO 落盘）...');
    const data = await api('/sync/full', { method: 'POST', body: JSON.stringify({
      cid: cid,
      local_path: document.getElementById('full-local').value,
      video_ext: videoExt,
      image_ext: getTags('image-ext'),
      data_ext: getTags('data-ext'),
    }) });
    toast(data.message || '全量同步完成');
    appendLog(`任务完成: 全量同步 · 视频 ${data.total} 个（生成 STRM ${data.created}），附属文件 ${data.assets_total} 个（下载 ${data.assets_downloaded}，跳过 ${data.assets_skipped}，失败 ${data.assets_failed}）；详细耗时见服务端日志`);
  } catch (e) { toast(e.message); }
}

// ==================== 转存 / 订阅（占位） ====================
function switchUDTab(tab) {
  document.querySelectorAll('#page-upload-download .tab').forEach(t => t.classList.toggle('active', t.dataset.tab === tab));
  document.querySelectorAll('#page-upload-download .tab-panel').forEach(p => p.classList.toggle('active', p.dataset.panel === tab));
}
let transferOrganizeVal = localStorage.getItem('transfer-organize') !== 'false';
// 刷新后恢复开关高亮显示（值已从 localStorage 读取，只差 UI 状态）
document.querySelectorAll('#transfer-organize-switch .seg-item').forEach(el => {
  el.classList.toggle('active', String(el.dataset.value) === String(transferOrganizeVal));
});
function setTransferOrganize(v, silent) {
  transferOrganizeVal = v;
  localStorage.setItem('transfer-organize', v ? 'true' : 'false');
  if (silent) return;
  document.querySelectorAll('#transfer-organize-switch .seg-item').forEach(el => {
    el.classList.toggle('active', String(el.dataset.value) === String(v));
  });
}

// ==================== 离线任务面板（表格 + 进度条 + 分页） ====================
let offlineTasksTimer = null;
let offlineTasksCache = [];
let offlineTasksPage = 1;
const OFFLINE_PAGE_SIZE = 20;

async function loadOfflineTasks() {
  const box = document.getElementById('offline-tasks');
  if (!box) return;
  const hint = document.getElementById('offline-tasks-hint');
  try {
    const d = await api('/offline/tasks');
    let items = d.data || [];
    if (!Array.isArray(items)) items = items.tasks || items.list || [];
    offlineTasksCache = items;
    if (!items.length) {
      box.innerHTML = '<span style="color:var(--text-3)">暂无离线任务</span>';
      document.getElementById('offline-tasks-pager').innerHTML = '';
      hint.textContent = '';
      return;
    }
    hint.textContent = '共 ' + items.length + ' 个 · ' + new Date().toLocaleTimeString();
    renderOfflineTasks();
  } catch (e) {
    box.innerHTML = '<span style="color:var(--danger)">' + escHtml(e.message) + '</span>';
  }
}

function offlineTaskRow(t) {
  // name 同时进 title 属性：esc() 含引号转义（escHtml 不转引号，属性可被截断）
  const name = esc(t.name || t.task_name || '?');
  const st = t.status;
  const pct = (() => {
    const p = t.percent;
    if (typeof p === 'number') return p >= 0 ? p : -1;
    if (typeof p === 'string' && p && !isNaN(parseFloat(p))) return parseFloat(p);
    return -1;
  })();
  let pill;
  if (st === -1)      pill = '<span class="otag" style="background:#ffece8;color:#b3231d">失败</span>';
  else if (st === 2)  pill = '<span class="otag" style="background:#eafaf0;color:#00874a">完成</span>';
  else if (st === 1 || (pct >= 0 && pct < 100))
                      pill = '<span class="otag otag-live" style="background:#e8f1ff;color:#1664d9">下载中</span>';
  else                pill = '<span class="otag" style="background:var(--fill-2);color:var(--text-3)">等待</span>';

  const size = t.size && Number(t.size) > 0 ? fmtSize(Number(t.size)) : '—';
  const doneAt = (t.del_time && Number(t.del_time) > 0)
    ? (() => { const d = new Date(Number(t.del_time) * 1000);
        return ('0' + (d.getMonth() + 1)).slice(-2) + '-' + ('0' + d.getDate()).slice(-2)
          + ' ' + ('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2); })()
    : '';

  // 主区第二行：下载中 → 进度条；完成 → 大小 · 完成时间；等待/失败 → 大小
  let sub;
  if (st === 1 || (pct >= 0 && pct < 100)) {
    const w = Math.max(0, Math.min(100, pct < 0 ? 0 : pct)).toFixed(1);
    sub = '<div class="otk-bar"><i style="width:' + w + '%"></i></div>'
        + '<span class="otk-pct">' + w + '%</span>';
  } else {
    sub = '<span>' + size + '</span>' + (doneAt ? '<span class="otk-dot">·</span><span>' + doneAt + '</span>' : '');
  }
  const side = st === 1 || (pct >= 0 && pct < 100) ? size : '';

  return '<div class="otk-row">' + pill +
    '<div class="otk-main"><div class="otk-name" title="' + name + '">' + name + '</div>' +
    '<div class="otk-sub">' + sub + '</div></div>' +
    (side ? '<div class="otk-side">' + escHtml(side) + '</div>' : '') +
    '</div>';
}

function renderOfflineTasks() {
  const box = document.getElementById('offline-tasks');
  const pager = document.getElementById('offline-tasks-pager');
  const total = offlineTasksCache.length;
  const pages = Math.max(1, Math.ceil(total / OFFLINE_PAGE_SIZE));
  if (offlineTasksPage > pages) offlineTasksPage = pages;
  if (offlineTasksPage < 1) offlineTasksPage = 1;
  const start = (offlineTasksPage - 1) * OFFLINE_PAGE_SIZE;
  const slice = offlineTasksCache.slice(start, start + OFFLINE_PAGE_SIZE);
  // 状态统计（全量，非当前页）
  let nDone = 0, nDown = 0, nFail = 0, nWait = 0;
  offlineTasksCache.forEach(t => {
    const p = typeof t.percent === 'number' ? t.percent : -1;
    if (t.status === 2) nDone++;
    else if (t.status === -1) nFail++;
    else if (t.status === 1 || (p >= 0 && p < 100)) nDown++;
    else nWait++;
  });
  const stat = (color, label, n) => '<span style="display:inline-flex;align-items:center;gap:5px;margin-right:14px;font-size:12.5px;color:var(--text-2)">' +
    '<i style="width:8px;height:8px;border-radius:50%;background:' + color + ';flex:none"></i>' + label + ' ' + n + '</span>';
  const summary = '<div style="display:flex;align-items:center;gap:2px;margin:2px 0 10px;flex-wrap:wrap">'
    + stat('var(--success)', '完成', nDone)
    + (nDown ? stat('var(--primary)', '下载中', nDown) : '')
    + (nFail ? stat('var(--danger)', '失败', nFail) : '')
    + (nWait ? stat('var(--text-3)', '等待', nWait) : '')
    + '</div>';
  box.innerHTML = summary +
    '<div class="otk">' + slice.map(offlineTaskRow).join('') + '</div>';
  pager.innerHTML = pages <= 1 ? '' :
    '<button class="btn btn-outline btn-sm" ' + (offlineTasksPage <= 1 ? 'disabled' : '') + ' onclick="offlineTasksNav(-1)">‹ 上一页</button>' +
    '<span style="font-size:13px;color:var(--text-3);align-self:center;margin:0 10px">第 ' + offlineTasksPage + ' / ' + pages + ' 页</span>' +
    '<button class="btn btn-outline btn-sm" ' + (offlineTasksPage >= pages ? 'disabled' : '') + ' onclick="offlineTasksNav(1)">下一页 ›</button>';
}
function offlineTasksNav(d) {
  offlineTasksPage += d;
  renderOfflineTasks();
}
function startOfflineTasksPoll() {
  stopOfflineTasksPoll();
  loadOfflineTasks();
  offlineTasksTimer = setInterval(() => {
    const page = document.getElementById('page-upload-download');
    if (!page || !page.classList.contains('active')) { stopOfflineTasksPoll(); return; }
    loadOfflineTasks();
  }, 30000);
}
function stopOfflineTasksPoll() {
  if (offlineTasksTimer) { clearInterval(offlineTasksTimer); offlineTasksTimer = null; }
}

async function transfer() {
  const url = document.getElementById('transfer-link').value.trim();
  if (!url) { toast('请填写链接'); return; }

  // 自动判断链接类型
  const isShare = url.includes('115.com/s/');
  const endpoint = isShare ? '/share/receive' : '/offline/add';

  // 提取码：行内输入框优先；其次从 URL 提取（?password=xxx 或 #xxx）；再试链接后附加的字段
  let code = (document.getElementById('transfer-code')?.value || '').trim();
  let cleanUrl = url;
  if (isShare) {
    const m = url.match(/[?#](?:password=)?([a-zA-Z0-9]{4,})/);
    if (m && !code) { code = m[1]; cleanUrl = url.split(/[?#]/)[0]; }
    if (!code) {
      // "链接 提取码" 同框写法
      const parts = url.split(/\s+/);
      if (parts.length >= 2) { cleanUrl = parts[0]; code = parts[1]; }
    }
    if (!code) {
      toast('115 分享链接需要提取码，请填写后重试');
      document.getElementById('transfer-code')?.focus();
      return;
    }
  }

  const body = {
    url: cleanUrl,
    code: code,
    target_cid: '',
    organize: transferOrganizeVal,
  };

  toast(isShare ? '转存进行中...' : '离线下载提交中...');
  try {
    const d = await api(endpoint, { method: 'POST', body: JSON.stringify(body) });
    toast(d.message || '完成');
    appendLog(`✓ ${isShare ? '转存' : '离线下载'}: ${d.message || ''}`);
    loadOfflineTasks();
    // 清空输入框
    document.getElementById('transfer-link').value = '';
    if (transferOrganizeVal) {
      appendLog('⏳ 自动整理+增量同步将在转存完成后触发...');
    }
  } catch (e) {
    toast('失败: ' + e.message);
    appendLog(`✗ ${isShare ? '转存' : '离线下载'} 失败: ${e.message}`);
  }
}
// 网络连接测试：并发探测 115 / TMDB / GitHub / 代理连通性
async function networkCheck(btn) {
  const el = document.getElementById('proxy-test-result');
  btn.disabled = true; btn.textContent = '测试中…';
  if (el) el.innerHTML = '<div class="test-banner pend"><span class="tb-ico">…</span><div class="tb-body"><div class="tb-title">正在探测外部网络…</div></div></div>';
  try {
    const d = await api('/network/check');
    if (el) el.innerHTML = (d.results || []).map(r =>
      '<div class="test-banner ' + (r.ok ? 'ok' : 'err') + '">'
      + '<span class="tb-ico">' + (r.ok ? '✓' : '✕') + '</span>'
      + '<div class="tb-body"><div class="tb-title">' + esc(r.name) + (r.ok ? ' 可达' : ' 不可达')
      + (r.ok ? '' : '</div><div class="tb-detail">' + esc(r.error || ''))
      + '</div></div>'
      + (r.ok ? '<div class="tb-detail" style="margin-left:auto;flex:none">' + r.latency_ms + 'ms</div>' : '')
      + '</div>').join('');
  } catch (e) {
    if (el) showTestResult(el, false, '网络测试失败', e.message);
  } finally {
    btn.disabled = false; btn.textContent = '网络连接测试';
  }
}

async function testProxy() {
  const url = document.getElementById('proxy-url').value.trim();
  if (!url) { toast('请先填写代理地址'); return; }
  const el = document.getElementById('proxy-test-result');
  showTestPending(el, '正在通过代理探测 google_204…');
  try {
    const d = await api('/proxy/test', { method: 'POST', body: JSON.stringify({ url }) });
    if (d.ok) showTestResult(el, true, '代理可用', '延迟 ' + d.latency_ms + 'ms');
    else showTestResult(el, false, '代理不可用', d.error || '连接失败');
  } catch (e) { showTestResult(el, false, '代理不可用', e.message); }
}

// ==================== 115 扫码登录 ====================
let qrcodeTimer = null;
let qrPollApi = '/storage/qrcode';  // 当前轮询接口（OpenAPI 启用时切换）
let qrAutoRefresh = 0;              // 二维码过期自动刷新次数（手动打开弹窗时清零）
async function openQrCode(isAuto) {
  if (!isAuto) qrAutoRefresh = 0;
  document.getElementById('qrcode-modal').style.display = 'flex';
  document.getElementById('qrcode-img').innerHTML = '二维码加载中...';
  // OPENAPI 启用且填了 AppID → 走开放平台授权；否则走 Cookie 扫码
  const appid = (document.getElementById('acc-appid') || {}).value || '';
  if (openapiEnabled && appid.trim()) {
    qrPollApi = '/storage/open/qrcode';
    document.getElementById('qrcode-status').textContent = '正在获取开放平台授权二维码...';
    try {
      const data = await api('/storage/open/qrcode', { method: 'POST', body: JSON.stringify({ type: '115', app_id: appid.trim() }) });
      if (data.qrcode) {
        document.getElementById('qrcode-img').innerHTML = `<img src="${data.qrcode}" style="width:170px;height:170px">`;
        document.getElementById('qrcode-status').textContent = '请使用 115 手机 App 扫码（开放平台授权）';
        startQrCodePolling(data.uid, data.time, data.sign);
      } else {
        document.getElementById('qrcode-img').innerHTML = '获取二维码失败';
        document.getElementById('qrcode-status').textContent = data.error || '请稍后重试';
      }
    } catch (e) {
      document.getElementById('qrcode-img').innerHTML = '<div style="padding:40px 0;color:var(--text-3)">二维码获取失败</div>';
      document.getElementById('qrcode-status').textContent = e.message || '请稍后重试';
    }
    return;
  }
  qrPollApi = '/storage/qrcode';
  document.getElementById('qrcode-status').textContent = '正在获取登录二维码...';
  try {
    const device = document.getElementById('acc-device').value;
    const data = await api('/storage/qrcode', { method: 'POST', body: JSON.stringify({ type: '115', device }) });
    if (data.qrcode) {
      document.getElementById('qrcode-img').innerHTML = `<img src="${data.qrcode}" style="width:170px;height:170px">`;
      document.getElementById('qrcode-status').textContent = '请使用 115 手机 App 扫描二维码';
      // 开始轮询扫码状态
      startQrCodePolling(data.uid, data.time, data.sign);
    } else {
      document.getElementById('qrcode-img').innerHTML = '获取二维码失败';
      document.getElementById('qrcode-status').textContent = data.error || '请稍后重试';
    }
  } catch (e) {
    document.getElementById('qrcode-img').innerHTML = '<div style="padding:40px 0;color:var(--text-3)">二维码获取失败</div>';
    document.getElementById('qrcode-status').textContent = e.message || '请稍后重试';
  }
}

function startQrCodePolling(uid, time, sign) {
  const session = {};          // 本次轮询的会话令牌
  qrcodeTimer = session;       // 记录当前会话，closeQrCode 会置空
  let errCount = 0;            // 连续失败计数，用于指数退避
  (async function poll() {
    if (qrcodeTimer !== session) return;   // 已关闭或新会话
    try {
      const data = await api(qrPollApi + '/status', { method: 'POST', body: JSON.stringify({ uid, time, sign }) });
      if (qrcodeTimer !== session) return;
      errCount = 0;  // 成功则重置退避
      const status = data.status;
      if (status === 'scanned') {
        document.getElementById('qrcode-status').textContent = '已扫码，请在手机上确认登录...';
        poll();
      } else if (status === 'success') {
        document.getElementById('qrcode-status').textContent = '登录成功！';
        // 更新账号信息显示
        const box = document.getElementById('acc-status-box');
        box.style.display = 'block';
        document.getElementById('acc-menu-username').textContent = data.username || '-';
        document.getElementById('acc-capacity').textContent = '已绑定';
        setTimeout(() => { closeQrCode(); toast('115 账号绑定成功'); checkCookie(); }, 1200);
      } else if (status === 'expired' || status === 'cancelled') {
        if (status === 'expired' && qrAutoRefresh < 3) {
          // 二维码有效期约 2 分钟，过期自动换新码重试（最多 3 次），不用手动关开弹窗
          qrAutoRefresh++;
          document.getElementById('qrcode-status').textContent = '二维码已过期，正在自动刷新（第 ' + qrAutoRefresh + '/3 次），请稍候重新扫码...';
          openQrCode(true);
          return;
        }
        document.getElementById('qrcode-status').textContent = status === 'expired' ? '二维码已过期，请关闭后重新获取' : '已取消登录';
      } else {
        poll();  // waiting，继续长轮询
      }
    } catch (e) {
      if (qrcodeTimer !== session) return;
      // 网络故障（fetch 抛 TypeError）才退避重试；服务端返回的错误（如 115 拒绝登录/IP风控）
      // 属于确定性失败，重试只会加重风控，直接展示并停止
      if (e instanceof TypeError) {
        errCount++;
        const delay = Math.min(30000, 1000 * Math.pow(2, errCount - 1));
        document.getElementById('qrcode-status').textContent = '网络波动，' + (delay / 1000) + ' 秒后重试...';
        setTimeout(poll, delay);
      } else {
        qrcodeTimer = null;  // 停止轮询
        document.getElementById('qrcode-status').textContent = e.message || '登录失败';
      }
    }
  })();
}

function closeQrCode() {
  qrcodeTimer = null;  // 停止轮询
  document.getElementById('qrcode-modal').style.display = 'none';
}

let openapiEnabled = false;
function setOpenapi(val) {
  openapiEnabled = val;
  document.querySelectorAll('#openapi-switch .seg-item').forEach(el => {
    el.classList.toggle('active', String(el.dataset.value) === String(val));
  });
}

function checkCookie() {
  // 后端使用扫码登录后保存的 Cookie 进行检测，无需 cookie 文件路径
  toast('正在检测 Cookie 可用性...');
  api('/storage/check', { method: 'POST', body: JSON.stringify({ type: '115' }) })
    .then(data => {
      if (!data.valid) {
        toast('Cookie 无效：' + (data.message || '未知原因'));
        return;
      }
      updateAccCard(data);
      toast('Cookie 检测成功');
    })
    .catch(e => toast('Cookie 检测失败：' + (e.message || '无效')));
}

// 填充账号信息卡片：头像/会员/容量进度条/UID/通道
function updateAccCard(data) {
  const box = document.getElementById('acc-status-box');
  box.style.display = 'flex';
  const avatar = document.getElementById('acc-avatar');
  const ph = avatar.nextElementSibling;
  if (data.avatar) {
    avatar.src = data.avatar; avatar.style.display = ''; ph.style.display = 'none';
  } else { avatar.style.display = 'none'; ph.style.display = 'flex'; }
  document.getElementById('acc-menu-username').textContent = data.username || '-';
  document.getElementById('acc-channel').textContent = '通道：' + (data.channel || '-');
  document.getElementById('acc-uid').textContent = data.user_id || '-';
  // 会员：0=非会员；forever=终身；expire=到期时间戳（秒）
  const badge = document.getElementById('acc-vip-badge');
  const vipText = document.getElementById('acc-vip');
  if (data.vip_forever === 1 || data.vip_forever === true) {
    badge.style.display = ''; badge.textContent = '终身VIP';
    vipText.textContent = '终身会员';
  } else if (data.vip > 0) {
    badge.style.display = ''; badge.textContent = 'VIP';
    vipText.textContent = data.vip_expire > 0 ? '会员，到期 ' + new Date(data.vip_expire * 1000).toLocaleDateString() : '会员';
  } else {
    badge.style.display = 'none';
    vipText.textContent = '非会员';
  }
  document.getElementById('acc-capacity').textContent = data.capacity || '-';
  const used = Number(data.used_size || 0), total = Number(data.total_size || 0);
  if (total > 0) {
    const pct = Math.min(100, Math.round(used / total * 1000) / 10);
    document.getElementById('acc-bar').style.width = pct + '%';
    document.getElementById('acc-bar-text').textContent = '已用 ' + pct + '%';
  }
  // 登录设备列表（可折叠，排查风控/异常登录）
  const devBox = document.getElementById('acc-devices');
  if (Array.isArray(data.devices) && data.devices.length) {
    const rows = data.devices.map(d => {
      const t = d.utime > 0 ? new Date(d.utime * 1000).toLocaleString() : '';
      return '<div style="padding:2px 0">' + (d.is_current ? '🟢 ' : '· ') +
        esc(d.name || d.device || '未知设备') + '　' + esc(d.ip || '') + (d.city ? '（' + esc(d.city) + '）' : '') +
        (t ? '　' + t : '') + '</div>';
    }).join('');
    devBox.innerHTML = '<summary style="cursor:pointer;color:var(--text-2)">登录设备（' + data.devices.length + '）</summary>' + rows;
    devBox.style.display = '';
  } else {
    devBox.style.display = 'none';
  }
}



async function loadAccount() {
  try {
    const data = await api('/storage');
    const acc = (data.data || []).find(s => s.type === '115');
    if (!acc) return;
    document.getElementById('acc-cookie-path').value = acc.cookie_path || '/config/115-cookies.txt';
    // 回显用户保存的设备选择（值不在选项中时保持默认网页端）
    const devSel = document.getElementById('acc-device');
    if (devSel && acc.device && [...devSel.options].some(o => o.value === acc.device)) devSel.value = acc.device;
    document.getElementById('acc-interval').value = acc.interval || 3.0;
    document.getElementById('acc-appid').value = acc.app_id || '';
    setOpenapi(!!acc.openapi_enabled);
    // 静默刷新账号卡片（真实容量/会员/头像）
    api('/storage/check', { method: 'POST', body: JSON.stringify({ type: '115' }) })
      .then(d => { if (d.valid) updateAccCard(d); })
      .catch(() => {});
  } catch (e) {}
}

function resetAccount() {
  ['acc-cookie-path', 'acc-appid', 'acc-interval'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.value = '';
  });
  document.getElementById('acc-cookie-path').value = '/config/115-cookies.txt';
  document.getElementById('acc-device').value = 'web';
  document.getElementById('acc-interval').value = '3.0';
  setOpenapi(false);
  document.getElementById('acc-status-box').style.display = 'none';
  toast('配置已重置');
}

async function saveAccount() {
  try {
    await api('/storage', { method: 'POST', body: JSON.stringify({
      name: '115主号',
      type: '115',
      device: document.getElementById('acc-device').value,
      cookie_path: document.getElementById('acc-cookie-path').value,
      interval: parseFloat(document.getElementById('acc-interval').value) || 3.0,
      openapi_enabled: openapiEnabled,
      app_id: document.getElementById('acc-appid').value,
    }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

// ==================== 重命名规则实时示例 ====================
const RENAME_VARS = {
  '{title}': '钢铁侠',
  '{en_title}': 'Iron Man',
  '{original_name}': '钢铁侠.2008.2160p.UHD.BluRay.mkv',
  '{year}': '2008',
  '{tmdb_id}': '1726',
  '{first_letter}': 'G',
  '{ext}': '.mkv',
  '{custom_regex_match}': '自定义',
  '{season_episode}': 'S01E01',
  '{season_num}': '1',
  '{episode_num}': '1',
  '{season_name}': '东海篇',
  '{episode_name}': '我是路飞',
  '{season_year}': '1999',
  '{disc_num}': '1',
  '{resource_pix}': '2160p',
  '{fps}': '60FPS',
  '{resource_version}': 'IMAX',
  '{resource_source}': 'NF',
  '{resource_type}': 'BluRay',
  '{resource_effect}': 'DV.HDR',
  '{video_encode}': 'H265.10bit',
  '{audio_encode}': 'TrueHD.7.1',
  '{resource_team}': 'TnT',
};

function renderRenameExample(rule, vars) {
  const V = vars || RENAME_VARS;
  let s = rule || '';
  // 按变量名长度降序替换，避免 {season} 先于 {season_episode} 被误替换
  const keys = Object.keys(V).sort((a, b) => b.length - a.length);
  // 第一步：处理 <...> 块语法（块内变量非空则输出块内容，否则丢弃整个块）
  // 支持 <{name}...>、<?{name}...>、<...{var}...> 等形式
  // 简化处理：遍历替换 <...> 块
  let prev;
  do {
    prev = s;
    // 匹配最内层的 <...>
    const m = s.match(/<([^<>]*)>/);
    if (!m) break;
    const blockContent = m[1];
    // 检查块内是否有非空变量
    let hasNonEmpty = false;
    let rendered = blockContent;
    for (const k of keys) {
      if (blockContent.includes(k)) {
        hasNonEmpty = true;
        rendered = rendered.split(k).join(V[k]);
      }
    }
    // 块内有变量且至少一个被替换 → 输出替换后的内容；否则丢弃
    s = s.replace(m[0], hasNonEmpty ? rendered : '');
  } while (s !== prev);
  // 第二步：替换裸 {变量名}
  for (const k of keys) {
    s = s.split(k).join(V[k]);
  }
  // 第三步：[[ ]] → { }
  s = s.replace(/\[\[/g, '{').replace(/\]\]/g, '}');
  return s;
}

function updateRenameExample() {
  const pairs = [
    ['rename-movie-folder', 'ex-movie-folder'],
    ['rename-movie-file', 'ex-movie-file'],
    ['rename-tv-folder', 'ex-tv-folder'],
    ['rename-tv-file', 'ex-tv-file'],
  ];
  pairs.forEach(([inputId, exId]) => {
    const input = document.getElementById(inputId);
    const ex = document.getElementById(exId);
    if (input && ex) ex.textContent = renderRenameExample(input.value);
  });
  syncRenamePresetUI();
}

// ==================== 重命名命名规范预设（电影/剧集） ====================
// 手动改过模板后与预设不再一致 → 按钮全部弹起（自定义状态），不自动套用
const RENAME_PRESETS = {
  movie: {
    default: {
      folder: '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]',
      file: '{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}',
    },
    lite: {
      folder: '{title} ({year})',
      file: '{title}.{year}<.{resource_pix}>{ext}',
    },
    full: {
      folder: '[{first_letter}]-{title}-{year}-[tmdb={tmdb_id}]',
      file: '{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}><{custom_regex_match}><[tmdb{tmdb_id}]{ext}',
    },
  },
  tv: {
    default: {
      folder: '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]',
      file: '{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}',
    },
    lite: {
      folder: '{title} ({year})',
      file: '{title} - {season_episode}<.{resource_pix}>{ext}',
    },
    full: {
      folder: '[{first_letter}]-{title}-{year}-[tmdb={tmdb_id}]',
      file: '{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}><{custom_regex_match}><[tmdb{tmdb_id}]{ext}',
    },
  },
};

function applyRenamePreset(type, key) {
  const p = (RENAME_PRESETS[type] || {})[key];
  if (!p) return;
  setVal('rename-' + type + '-folder', p.folder);
  setVal('rename-' + type + '-file', p.file);
  document.querySelectorAll('#rename-' + type + '-folder, #rename-' + type + '-file').forEach(t => autoResizeTextarea(t));
  updateRenameExample(); // 含 syncRenamePresetUI
}

// 当前值与哪个预设一致就点亮对应按钮；都不一致 = 自定义（全部弹起）
function syncRenamePresetUI() {
  for (const type of ['movie', 'tv']) {
    const folder = (val('rename-' + type + '-folder') || '').trim();
    const file = (val('rename-' + type + '-file') || '').trim();
    let hit = '';
    for (const [k, p] of Object.entries(RENAME_PRESETS[type])) {
      if (folder === p.folder && file === p.file) { hit = k; break; }
    }
    document.querySelectorAll('#rn-presets-' + type + ' .rn-preset').forEach(b =>
      b.classList.toggle('active', b.dataset.p === hit));
  }
}

// 输入框自适应高度
function autoResizeTextarea(el) {
  el.style.height = 'auto';
  el.style.height = el.scrollHeight + 'px';
}


// ==================== CloudDrive2（多云盘聚合：只做整理） ====================
// 独立菜单页（连接账号 + 实时监控整理）；STRM 由项目原生增量同步生成，
// 播放走 /d/ 直链，CD2 不在播放路径上

let cd2OrgEnabled = false;

function cd2Gather() {
  return {
    endpoint: val('cd2-endpoint').trim(),
    username: val('cd2-username').trim(),
    password: val('cd2-password').trim(),
    org_enabled: cd2OrgEnabled,
    // 三个目录均自动派生：目标根探测 CD2 的 115 挂载，
    // 监控/已存在取「自动整理 → 基础配置」映射
  };
}

function setCd2Org(v) {
  cd2OrgEnabled = v;
  document.querySelectorAll('#cd2-org-switch .seg-item').forEach(n => {
    n.classList.toggle('active', n.dataset.value === String(v));
  });
}

let cd2StatusTimer = null;

async function cd2WatchStatus() {
  const el = document.getElementById('cd2-watch-status');
  if (!el) return;
  try {
    const d = await api('/cd2/org/status');
    const s = d.data || {};
    if (!s.enabled) {
      el.textContent = '○ 未开启';
      el.style.color = 'var(--text-3)';
    } else if (s.running) {
      el.textContent = '● 监控中（已整理 ' + (s.organized || 0) + ' 个单元' +
        (s.last_event ? '，最近事件 ' + s.last_event : '') + '）';
      el.style.color = '#1f8a4c';
    } else {
      el.textContent = '● 启动中…' + (s.last_err ? '（' + s.last_err + '）' : '');
      el.style.color = 'var(--warning)';
    }
  } catch (e) {
    el.textContent = '状态加载失败';
    el.style.color = 'var(--danger)';
  }
}

async function cd2LoadUI() {
  try {
    const d = await api('/cd2/config');
    const c = d.data || {};
    setVal('cd2-endpoint', c.endpoint || '');
    setVal('cd2-username', c.username || '');
    setVal('cd2-password', c.password || '');
    setCd2Org(!!c.org_enabled);
    const dd = document.getElementById('cd2-derived-dirs');
    if (dd) {
      if (c.root_path) {
        dd.innerHTML = '整理目标根：<b>' + esc(c.root_path) + '</b><br>监控目录：' + esc(c.org_pending || '（识别中…）')
          + '<br>已存在目录：' + esc(c.org_existing || '（识别中…）');
      } else {
        dd.textContent = '未识别（保存并开启后自动探测 CD2 的 115 媒体库挂载）';
      }
    }
  } catch (e) { /* 首次为空 */ }
  cd2WatchStatus();
  // 状态自动刷新（离开页面后元素不在即空转，重新进入会重置定时器）
  clearInterval(cd2StatusTimer);
  cd2StatusTimer = setInterval(cd2WatchStatus, 15000);
}

async function cd2Save(btn) {
  btn.disabled = true;
  try {
    const d = await api('/cd2/config', { method: 'POST', body: JSON.stringify(cd2Gather()) });
    toast(d.message || '配置已保存');
    cd2LoadUI(); // 刷新派生目录显示（保存时后端会按 115 目录刷新）
  } catch (e) { toast('保存失败：' + e.message); }
  btn.disabled = false;
}

async function cd2Test(btn) {
  // 先保存再测试：测试用的是服务端已保存的凭证
  const result = document.getElementById('cd2-test-result');
  btn.disabled = true;
  result.textContent = '测试中…';
  result.style.color = 'var(--text-3)';
  try {
    await api('/cd2/config', { method: 'POST', body: JSON.stringify(cd2Gather()) });
    const d = await api('/cd2/test', { method: 'POST' });
    result.textContent = '✓ ' + (d.message || '连接成功');
    result.style.color = '#1f8a4c';
  } catch (e) {
    result.textContent = '✗ ' + e.message;
    result.style.color = 'var(--danger)';
  }
  btn.disabled = false;
}

async function cd2OrgRun(btn) {
  if (!confirm('立即整理监控目录下所有待处理内容？')) return;
  btn.disabled = true;
  try {
    const d = await api('/cd2/org/run', { method: 'POST' });
    toast(d.message || '整理已开始');
  } catch (e) { toast(e.message); }
  btn.disabled = false;
}

// ==================== 媒体库封面生成 ====================
let coverGenStyle = '1';

function coverGenModalClose() { document.getElementById('covergen-modal').style.display = 'none'; }

function coverGenPickStyle(el) {
  document.querySelectorAll('#covergen-styles .cg-style-card').forEach(n => n.classList.remove('active'));
  el.classList.add('active');
  coverGenStyle = el.dataset.v;
}

async function coverGenConfig() {
  document.getElementById('covergen-modal').style.display = 'flex';
  coverGenLoadList();
  try {
    const d = await api('/covergen/config');
    const c = d.data || {};
    setVal('covergen-cron', c.cron || '0 0 * * *');
    setVal('covergen-strategy', c.strategy || 'added');
    setVal('covergen-blacklist', c.blacklist || '');
    setVal('covergen-advanced', c.advanced || '');
    coverGenStyle = c.style || '1';
    document.querySelectorAll('#covergen-styles .cg-style-card').forEach(n =>
      n.classList.toggle('active', n.dataset.v === coverGenStyle));
  } catch (e) { /* 首次为空 */ }
}

function coverGenCronPreview() {
  const box = document.getElementById('covergen-next');
  const expr = document.getElementById('covergen-cron').value.trim() || '0 0 * * *';
  api('/sync/cron-preview', { method: 'POST', body: JSON.stringify({ cron: expr }) })
    .then(d => { box.textContent = d.next || ''; })
    .catch(() => { box.textContent = ''; });
}

async function coverGenSave(btn) {
  btn.disabled = true;
  try {
    await api('/covergen/config', {
      method: 'POST',
      body: JSON.stringify({
        cron: val('covergen-cron').trim() || '0 0 * * *',
        style: coverGenStyle,
        strategy: val('covergen-strategy'),
        blacklist: val('covergen-blacklist'),
        advanced: val('covergen-advanced'),
      }),
    });
    toast('配置已保存');
  } catch (e) { toast('保存失败：' + e.message); }
  btn.disabled = false;
}

async function coverGenRun(btn) {
  if (!confirm('立即生成全部媒体库封面？（已配置 Emby 时会同步推送为媒体库主页图）')) return;
  btn.disabled = true;
  try {
    const d = await api('/covergen/run', { method: 'POST' });
    toast(d.message || '生成已开始');
    setTimeout(coverGenLoadList, 8000);
  } catch (e) { toast(e.message); }
  btn.disabled = false;
}

async function coverGenLoadList() {
  const box = document.getElementById('covergen-list');
  if (!box) return;
  try {
    const d = await api('/covergen/list');
    const items = d.data || [];
    box.innerHTML = items.length ? items.map(it =>
      '<div style="border:1px solid var(--border);border-radius:8px;overflow:hidden">'
      + '<img src="/api/covergen/preview?name=' + encodeURIComponent(it.name) + '&t=' + Date.now() + '" loading="lazy" style="width:100%;display:block">'
      + '<div style="padding:4px 8px;font-size:11.5px;color:var(--text-3)">' + esc(it.name) + '<span style="float:right">' + esc(it.time || '') + '</span></div></div>'
    ).join('') : '<span style="color:var(--text-3);font-size:12.5px">还没有生成过封面，点「立即生成」试试</span>';
  } catch (e) { box.innerHTML = '<span style="color:var(--danger)">加载失败</span>'; }
}

// ==================== TG 订阅管理 ====================
async function tgSubLoadPage() {
  await tgSubRenderAll();
}

async function tgSubFetchCfg() {
  const d = await api('/tgsub/config');
  return d.data || { sources: [], items: [], interval_min: 30 };
}

async function tgSubPersist(cfg, btn, doneMsg) {
  if (btn) btn.disabled = true;
  try {
    await api('/tgsub/config', { method: 'POST', body: JSON.stringify(cfg) });
    toast(doneMsg || '已保存');
    await tgSubRenderAll();
  } catch (e) { toast('保存失败：' + e.message); }
  if (btn) btn.disabled = false;
}

// 渲染统一表格：类型/状态筛选 → TG群+关键词订阅合并列表（对齐管理端表格范式）
function tgSubFilterVals() {
  const t = document.getElementById('tgsub-f-type');
  const st = document.getElementById('tgsub-f-status');
  return {
    type: t ? t.value : '',
    status: st ? st.value : '',
  };
}

function tgSubFilterReset() {
  const t = document.getElementById('tgsub-f-type');
  const st = document.getElementById('tgsub-f-status');
  if (t) t.value = '';
  if (st) st.value = '';
  tgSubRenderAll(true);
}

// 状态徽标 + 开关（停用的行整行淡化）
function tgSubStatusCell(enabled, kind, id) {
  return '<label style="display:inline-flex;align-items:center;gap:5px;cursor:pointer;font-size:12px" '
    + 'onclick="event.stopPropagation()">'
    + '<input type="checkbox" ' + (enabled ? 'checked' : '') + ' onchange="tgSubToggle(\'' + kind + '\',' + id + ',this.checked)">'
    + (enabled ? '<span style="color:#00874a">启用</span>' : '<span style="color:var(--text-3)">停用</span>')
    + '</label>';
}

async function tgSubRenderAll(forceQuery) {
  let cfg;
  try { cfg = await tgSubFetchCfg(); } catch (e) {
    document.getElementById('tgsub-table').innerHTML = '<span style="color:var(--danger)">加载失败</span>';
    return;
  }
  const f = tgSubFilterVals();
  const srcs = (cfg.sources || []).map(x => Object.assign({ _kind: 'source' }, x, { enabled: x.enabled !== false }));
  const items = (cfg.items || []).map(x => Object.assign({ _kind: 'item' }, x, { enabled: x.enabled !== false }));
  let rows = srcs.concat(items);
  if (f.type === 'source') rows = rows.filter(r => r._kind === 'source');
  if (f.type === 'item') rows = rows.filter(r => r._kind === 'item');
  if (f.status === 'on') rows = rows.filter(r => r.enabled);
  if (f.status === 'off') rows = rows.filter(r => !r.enabled);

  const box = document.getElementById('tgsub-table');
  if (!rows.length) {
    box.innerHTML = '<div class="dash-empty">' + (f.type || f.status ? '没有符合筛选条件的条目' : '还没有订阅，点右上角「＋ 新增」添加') + '</div>';
  } else {
    const trs = rows.map(r => {
      const kindTag = r._kind === 'source'
        ? '<span class="otag" style="background:#e8f1ff;color:#1c64d9">TG群</span>'
        : '<span class="otag" style="background:#fff4e5;color:#b26a00">关键词订阅</span>';
      let main, sub;
      if (r._kind === 'source') {
        main = esc(r.name || r.url);
        sub = [esc(r.url), r.note ? esc(r.note) : '', '优先级 ' + (r.priority || 10)].filter(Boolean)
          .join('<span class="otk-dot">·</span>');
      } else {
        main = esc(r.keyword);
        sub = [r.channels ? esc(String(r.channels).split('\n').join(' ')) : '全部TG群',
          r.auto ? '<span style="color:#00874a">自动转存</span>' : '',
          r.last_hit ? '最近命中 ' + esc(r.last_hit) : ''].filter(Boolean)
          .join('<span class="otk-dot">·</span>');
      }
      const id = String(r.id);
      return '<div class="otk-row"' + (!r.enabled ? ' style="opacity:.55"' : '') + '>'
        + kindTag
        + '<div class="otk-main"><div class="otk-name">' + main + '</div>'
        + '<div class="otk-sub">' + sub + '</div></div>'
        + tgSubStatusCell(r.enabled, r._kind, id)
        + '<button class="btn btn-outline" style="padding:3px 10px;font-size:12px" onclick="tgSubEditModal(\'' + r._kind + '\',' + id + ')">编辑</button>'
        + '<button class="btn btn-outline" style="padding:3px 10px;font-size:12px" onclick="tgSubDelRow(\'' + r._kind + '\',' + id + ')">删除</button>'
        + '</div>';
    }).join('');
    box.innerHTML = '<div class="otk">' + trs + '</div>';
  }
}

// 启用/停用切换
async function tgSubToggle(kind, id, on) {
  const cfg = await tgSubFetchCfg();
  const list = kind === 'source' ? (cfg.sources || []) : (cfg.items || []);
  for (const r of list) {
    if (String(r.id) === String(id)) r.enabled = on;
  }
  await tgSubPersist(cfg, null, on ? '已启用' : '已停用');
}

// 统一删除（类型 + id）
async function tgSubDelRow(kind, id) {
  const cfg = await tgSubFetchCfg();
  if (kind === 'source') {
    cfg.sources = (cfg.sources || []).filter(x => String(x.id) !== String(id));
  } else {
    cfg.items = (cfg.items || []).filter(x => String(x.id) !== String(id));
  }
  await tgSubPersist(cfg, null, '已删除');
}

// ========== 新增/编辑弹窗 ==========
let tgSubEditKind = 'item';
let tgSubEditId = 0; // 0=新增

function tgSubAddModal() {
  tgSubEditModal('item', 0); // 默认关键词订阅，弹窗内可切类型
}

function tgSubEditModal(kind, id) {
  tgSubEditKind = kind;
  tgSubEditId = id || 0;
  const isEdit = tgSubEditId > 0;
  document.getElementById('tgsub-edit-title').textContent = (isEdit ? '编辑' : '新增') + (kind === 'source' ? 'TG群' : '关键词订阅');
  const rbSource = document.querySelector('input[name="tgsub-etype"][value="source"]');
  const rbItem = document.querySelector('input[name="tgsub-etype"][value="item"]');
  if (isEdit) {
    // 编辑态锁类型（改类型等于换实体）
    if (rbSource) rbSource.disabled = true;
    if (rbItem) rbItem.disabled = true;
    (kind === 'source' ? rbSource : rbItem).checked = true;
  } else {
    if (rbSource) rbSource.disabled = false;
    if (rbItem) rbItem.disabled = false;
    if (rbItem) rbItem.checked = true;
  }
  tgSubEditTypeSwitch();
  // 回填
  document.getElementById('tgsub-e-keyword').value = '';
  document.getElementById('tgsub-e-channels').value = '';
  document.getElementById('tgsub-e-auto').checked = false;
  document.getElementById('tgsub-e-name').value = '';
  document.getElementById('tgsub-e-url').value = '';
  document.getElementById('tgsub-e-priority').value = '10';
  document.getElementById('tgsub-e-note').value = '';
  if (isEdit) {
    tgSubFetchCfg().then(cfg => {
      const list = kind === 'source' ? (cfg.sources || []) : (cfg.items || []);
      const r = list.find(x => String(x.id) === String(tgSubEditId));
      if (!r) return;
      if (kind === 'source') {
        document.getElementById('tgsub-e-name').value = r.name || '';
        document.getElementById('tgsub-e-url').value = r.url || '';
        document.getElementById('tgsub-e-priority').value = r.priority || 10;
        document.getElementById('tgsub-e-note').value = r.note || '';
      } else {
        document.getElementById('tgsub-e-keyword').value = r.keyword || '';
        document.getElementById('tgsub-e-channels').value = r.channels || '';
        document.getElementById('tgsub-e-auto').checked = !!r.auto;
      }
    });
  }
  document.getElementById('tgsub-edit-modal').style.display = 'flex';
}

function tgSubEditClose() {
  document.getElementById('tgsub-edit-modal').style.display = 'none';
}

function tgSubEditTypeSwitch() {
  const isSource = (document.querySelector('input[name="tgsub-etype"]:checked') || {}).value === 'source';
  document.getElementById('tgsub-edit-item').style.display = isSource ? 'none' : '';
  document.getElementById('tgsub-edit-source').style.display = isSource ? '' : 'none';
}

async function tgSubEditSave(btn) {
  const isSource = (document.querySelector('input[name="tgsub-etype"]:checked') || {}).value === 'source';
  const cfg = await tgSubFetchCfg();
  if (isSource) {
    const name = document.getElementById('tgsub-e-name').value.trim();
    const u = document.getElementById('tgsub-e-url').value.trim();
    if (!name || !u) { toast('订阅名称和地址必填'); return; }
    const fields = {
      type: 'tg', name: name, url: u,
      priority: parseInt(document.getElementById('tgsub-e-priority').value, 10) || 10,
      note: document.getElementById('tgsub-e-note').value.trim(),
    };
    cfg.sources = cfg.sources || [];
    if (tgSubEditId > 0) {
      for (const x of cfg.sources) if (String(x.id) === String(tgSubEditId)) Object.assign(x, fields);
    } else {
      cfg.sources.push(Object.assign({ enabled: true, last_id: 0 }, fields));
    }
    await tgSubPersist(cfg, btn, tgSubEditId > 0 ? 'TG群已更新' : 'TG群已添加');
  } else {
    const kw = document.getElementById('tgsub-e-keyword').value.trim();
    if (!kw) { toast('请填写关键词'); return; }
    const fields = {
      keyword: kw,
      channels: document.getElementById('tgsub-e-channels').value.trim(),
      auto: document.getElementById('tgsub-e-auto').checked,
    };
    cfg.items = cfg.items || [];
    if (tgSubEditId > 0) {
      for (const x of cfg.items) if (String(x.id) === String(tgSubEditId)) Object.assign(x, fields);
    } else {
      cfg.items.push(Object.assign({ enabled: true, last_id: 0 }, fields));
    }
    await tgSubPersist(cfg, btn, tgSubEditId > 0 ? '订阅已更新' : '订阅已添加');
  }
  tgSubEditClose();
}



async function tgSubDel(id) {
  const cfg = await tgSubFetchCfg();
  cfg.items = (cfg.items || []).filter(it => String(it.id) !== String(id));
  await tgSubPersist(cfg, null, '已删除');
}



// ==================== GPT 测试连接 ====================
async function testGPT() {
  const url = val('org-gpt-url');
  const key = val('org-gpt-key');
  const model = val('org-gpt-model');
  const btn = document.getElementById('gpt-test-btn');
  const result = document.getElementById('gpt-test-result');
  if (!url || !model) { toast('请先填写 API 地址和模型名称'); return; }
  btn.disabled = true;
  btn.textContent = '测试中...';
  showTestPending(result, '正在连接 GPT 服务…');
  try {
    const data = await api('/config/test-gpt', { method: 'POST', body: JSON.stringify({ url, key, model }) });
    if (data.success) showTestResult(result, true, 'GPT 连接成功', data.message || (model + ' 可用'));
    else showTestResult(result, false, 'GPT 连接失败', data.error || '未知错误');
  } catch (e) {
    showTestResult(result, false, 'GPT 连接失败', e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = '测试连接';
  }
}

async function testTmdb() {
  const btn = document.getElementById('tmdb-test-btn');
  const result = document.getElementById('tmdb-test-result');
  btn.disabled = true;
  btn.textContent = '测试中...';
  showTestPending(result, '正在请求 TMDB…');
  try {
    const data = await api('/config/test-tmdb', { method: 'POST', body: JSON.stringify({
      api_url: val('tmdb-api-url'),
      api_key: val('tmdb-api-key'),
      language: tmdbLangVal,
    }) });
    if (data.success) showTestResult(result, true, 'TMDB 连接成功', data.message || undefined);
    else showTestResult(result, false, 'TMDB 连接失败', data.error || '请检查 API Key');
  } catch (e) {
    showTestResult(result, false, 'TMDB 连接失败', e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = '测试连接';
  }
}

// EMBY 入库刷新
let embyStyleVal = 'unix';
let embyEnabledVal = true;
let migratedEmbyEnabled = false;
let migratedEmbyStyle = false;
function setEmbyStyle(val) {
  embyStyleVal = val;
  document.querySelectorAll('#emby-refresh-style-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === val));
  testEmbyPath();
}
function setEmbyEnabled(val) {
  embyEnabledVal = val;
  document.querySelectorAll('#emby-refresh-enabled-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === String(val)));
}
function testEmbyPath() {
  const input = document.getElementById('emby-path-test-input')?.value || '';
  const rule = document.getElementById('emby-path-mapping')?.value || '';
  const out = document.getElementById('emby-path-test-output');
  if (!out) return;
  if (!input) { out.textContent = ''; out.style.display = 'none'; return; }
  out.style.display = 'block';
  let result = input;
  if (rule && rule.includes('#')) {
    const [src, dst] = rule.split('#');
    if (src && input.startsWith(src)) result = dst + input.slice(src.length);
  }
  if (embyStyleVal === 'windows') result = result.replace(/\//g, '\\');
  out.textContent = result;
}

// 测试 Emby 连接：用当前输入框的值（未保存也可测）请求 /System/Info
async function testEmbyConnection() {
  const result = document.getElementById('emby-test-result');
  const btn = event && event.target ? event.target.closest('button') : null;
  const serverURL = document.getElementById('emby-server-url').value.trim();
  const apiKey = document.getElementById('emby-api-key').value.trim();
  showTestResult(result, null);
  if (!serverURL) {
    showTestResult(result, false, '请先填写 Emby 服务器地址');
    return;
  }
  if (btn) { btn.disabled = true; btn.textContent = '测试中…'; }
  showTestPending(result, '正在连接 Emby 服务器…');
  try {
    const d = await api('/config/test-emby', { method: 'POST', body: JSON.stringify({ server_url: serverURL, api_key: apiKey }) });
    if (d.ok) showTestResult(result, true, 'Emby 连接成功',
      (d.server_name || 'Emby') + (d.version ? ' · v' + d.version : '') + ' · ' + (d.library_count || 0) + ' 个媒体库');
    else showTestResult(result, false, 'Emby 连接失败', d.error || '未知错误');
  } catch (e) {
    showTestResult(result, false, 'Emby 连接失败', e.message);
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = '测试连接'; }
  }
}

// Emby Webhook 接收地址：token 首次自动生成并落库，直接展示可复制地址（推送什么事件由 Emby 侧决定）
let embyNotifyToken = '';
function applyEmbyNotify(v) {
  embyNotifyToken = (v && v.token) || '';
  // 兼容旧版：token 存在旧 webhook 地址里的场景
  if (!embyNotifyToken && v && v.webhook) {
    const m = v.webhook.match(/[?&]token=([^&]+)/);
    if (m) embyNotifyToken = m[1];
  }
  if (!embyNotifyToken) {
    saveEmbyNotifyToken(genEmbyNotifyToken());
  }
  const input = document.getElementById('emby-notify-token');
  if (input && document.activeElement !== input) input.value = embyNotifyToken;
  renderEmbyWebhookUrl();
}
function genEmbyNotifyToken() {
  const buf = new Uint8Array(8);
  crypto.getRandomValues(buf);
  return [...buf].map(b => b.toString(16).padStart(2, '0')).join('');
}
function saveEmbyNotifyToken(t) {
  if (!t) { toast('token 不能为空'); return; }
  if (!/^[a-zA-Z0-9_-]{4,64}$/.test(t)) { toast('token 仅支持 4-64 位字母/数字/中横线/下划线'); return; }
  embyNotifyToken = t;
  api('/config/setting', { method: 'POST', body: JSON.stringify({ key: 'emby-notify', value: JSON.stringify({ token: t }) }) })
    .then(() => {
      const input = document.getElementById('emby-notify-token');
      if (input) input.value = embyNotifyToken;
      renderEmbyWebhookUrl();
    })
    .catch(() => { toast('token 保存失败'); });
}
function renderEmbyWebhookUrl() {
  // token 输入框编辑中实时取输入值预览；展示地址与后端校验一致（保存后生效）
  const input = document.getElementById('emby-notify-token');
  if (input && document.activeElement === input) embyNotifyToken = input.value.trim();
  const el = document.getElementById('emby-webhook-url');
  if (el) el.textContent = location.origin + '/api/emby/webhook?token=' + (embyNotifyToken || '');
}
function rotateEmbyWebhookToken() {
  const t = genEmbyNotifyToken();
  const input = document.getElementById('emby-notify-token');
  if (input) input.value = t;
  saveEmbyNotifyToken(t);
  toast('已生成新 token，请到 Emby 更新回调地址');
}
// 消息通知
let msgWecomEnabled = false;
let msgTgEnabled = false;
let msgFeishuEnabled = false;
let msgOnebotEnabled = false;
let msgQqoffEnabled = false;
let onebotTargetType = 'group';
let onebotEventToken = '';
function switchMsgTab(tab) {
  document.querySelectorAll('#page-config-message .tab').forEach(t => t.classList.toggle('active', t.dataset.tab === tab));
  document.querySelectorAll('#page-config-message .tab-panel').forEach(p => p.classList.toggle('active', p.dataset.panel === tab));
}
function setMsgEnabled(type, val) {
  if (type === 'wecom') msgWecomEnabled = val;
  if (type === 'tg') msgTgEnabled = val;
  if (type === 'feishu') msgFeishuEnabled = val;
  if (type === 'onebot') msgOnebotEnabled = val;
  if (type === 'qqoff') msgQqoffEnabled = val;
  document.querySelectorAll('#msg-' + type + '-enabled-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === String(val)));
}

// OneBot：目标类型切换 + 事件回调地址展示 + token 自动生成
function setMsgOnebotTargetType(v) {
  onebotTargetType = v;
  document.querySelectorAll('#msg-onebot-target-type-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === v));
}
function renderOnebotCallback() {
  const el = document.getElementById('onebot-callback-url');
  if (el) el.textContent = location.origin + '/onebot/event?token=' + (onebotEventToken || '<保存后生成>');
}
function ensureOnebotEventToken() {
  if (!onebotEventToken) {
    const cs = 'abcdef0123456789';
    onebotEventToken = Array.from({ length: 16 }, () => cs[Math.floor(Math.random() * cs.length)]).join('');
  }
  return onebotEventToken;
}

async function testMessage(btn) {
  const result = btn ? btn.closest('.control')?.querySelector('.test-result') : null;
  btn.disabled = true;
  btn.textContent = '发送中...';
  showTestPending(result, '正在发送测试消息…');
  try {
    const data = await api('/message/test', { method: 'POST' });
    if (data.success) showTestResult(result, true, '测试消息已发送', '请到企业微信 / Telegram 查收');
    else showTestResult(result, false, '发送失败', data.error || '请检查消息配置');
  } catch (e) {
    showTestResult(result, false, '发送失败', e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = '测试通知';
  }
}

// ==================== 整理 ====================
async function startOrganize() {
  const btn = event?.target;
  if (btn) { btn.disabled = true; btn.textContent = '执行中...'; }
  toast('整理任务执行中...');
  appendLog('开始执行整理任务');
  try {
    const t0 = Date.now();
    const data = await api('/organize/pipeline', { method: 'POST', body: JSON.stringify({ sync_after: true }) });
    toast(data.message || '整理完成');
    if (data.steps) {
      data.steps.forEach(s => {
        const icon = s.status === '完成' ? '✓' : s.status === '失败' ? '✗' : '○';
        appendLog(`${icon} ${esc(s.step)}: ${esc(s.message)}`);
      });
    }
    // 按部汇总：一行一部
    (data.shows || []).forEach(sh => {
      appendLog(`★ 入库: ${esc(sh.title)} (${esc(sh.year || '-')} → ${esc(sh.target)}`);
    });
    // 逐项详情：识别出的标题/年份/分类/目标路径
    (data.details || []).forEach(d => {
      const icon = d.status === 'success' ? '✓' : d.status === 'exists' ? '○' : '✗';
      appendLog(`${icon} ${esc(d.file_name)} ${esc(d.message)}`);
    });
    appendLog(`任务完成: 自动整理, 耗时 ${((Date.now() - t0) / 1000).toFixed(1)} 秒`);
  } catch (e) {
    toast(e.message);
    appendLog('✗ 整理执行失败: ' + e.message);
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = '开始整理'; }
  }
}

// ==================== 通用配置保存/回显（STRM / 代理 / EMBY） ====================
// 读取输入框/textarea 的值（安全）
function val(id) {
  const el = document.getElementById(id);
  return el ? el.value : '';
}
function setVal(id, v) {
  const el = document.getElementById(id);
  if (el && v !== undefined && v !== null) el.value = v;
}

// STRM 分段开关
let strmFormatVal = 'pick_code_name';
let strmExistVal = 'overwrite';
let strmExtVal = true;

function updateStrmExample() {
  const ex = document.getElementById('strm-format-example');
  if (!ex) return;
  const domain = (document.getElementById('strm-domain')?.value || '').replace(/\/+$/, '');
  if (!domain) { ex.textContent = ''; return; }
  const pickcode = 'abchrb6gnrw0hhh80';
  const name = '钢铁侠.2008.1080p.BluRay.X264.DTS-TnT.mkv';
  const id = strmExtVal ? pickcode + '.mkv' : pickcode;
  // pick_code:      /d/{pickcode}[.ext]
  // pick_code_name: /d/{pickcode}[.ext]?/{原文件名}（?/ 后的名字供播放器识别）
  ex.textContent = strmFormatVal === 'pick_code'
    ? `${domain}/d/${id}`
    : `${domain}/d/${id}?/${name}`;
}

function setStrmFormat(val) {
  strmFormatVal = val;
  document.querySelectorAll('#strm-format-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === val));
  updateStrmExample();
}
function setStrmExist(val) {
  strmExistVal = val;
  document.querySelectorAll('#strm-exist-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === val));
}
function setStrmExt(val) {
  strmExtVal = val;
  document.querySelectorAll('#strm-ext-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === String(val)));
  updateStrmExample();
}

function collectConfig(key) {
  if (key === 'strm') {
    return {
      domain: document.getElementById('strm-domain').value,
      format: strmFormatVal,
      exist: strmExistVal,
      keep_ext: strmExtVal,
    };
  }
  if (key === 'proxy') {
    return { url: document.getElementById('proxy-url').value };
  }
  if (key === 'emby-notify') {
    return { token: embyNotifyToken };
  }
  if (key === 'message') {
    if (!onebotEventToken) ensureOnebotEventToken();
    renderOnebotCallback();
    return {
      wecom: { corp_id: val('msg-wecom-corp-id'), secret: val('msg-wecom-secret'), agent_id: val('msg-wecom-agent-id'), api_url: val('msg-wecom-api-url'), token: val('msg-wecom-token'), encoding_aes_key: val('msg-wecom-aes-key'), enabled: msgWecomEnabled },
      tg: { token: val('msg-tg-token'), chat_id: val('msg-tg-chat-id'), enabled: msgTgEnabled },
      feishu: { webhook: val('msg-feishu-webhook'), secret: val('msg-feishu-secret'), enabled: msgFeishuEnabled },
      qq_onebot: { url: val('msg-onebot-url'), token: val('msg-onebot-token'), target_type: onebotTargetType, target: val('msg-onebot-target'), admin: val('msg-onebot-admin'), event_token: onebotEventToken, enabled: msgOnebotEnabled },
      qq_official: { app_id: val('msg-qqoff-appid'), secret: val('msg-qqoff-secret'), group_id: val('msg-qqoff-group'), enabled: msgQqoffEnabled },
    };
  }
  if (key === 'org-basic') {
    // 影视库不再单独配置，直接使用全量同步的媒体库
    return {
      pending: resolveCID('org-pending'),
      pending_path: val('org-pending'),
      existing: resolveCID('org-existing'),
      existing_path: val('org-existing'),
      redundant: resolveCID('org-redundant'),
      redundant_path: val('org-redundant'),
      enrich: {
        enabled: enrichOpts.enabled, mode: enrichOpts.mode,
        missing: enrichOpts.missing, conflict_low: enrichOpts.clow,
        conflict_high: enrichOpts.chigh, full_named: enrichOpts.full
      }
    };
  }
  if (key === 'org-recognize') {
    return {
      replace_rules: val('org-replace-rules'),
      min_size: val('org-min-size'),
      release_groups: val('org-release-groups'),
    };
  }
  if (key === 'org-gpt') {
    return {
      url: val('org-gpt-url'),
      key: val('org-gpt-key'),
      model: val('org-gpt-model'),
    };
  }
  if (key === 'org-rename') {
    return {
      movie_folder: val('rename-movie-folder'),
      movie_file: val('rename-movie-file'),
      tv_folder: val('rename-tv-folder'),
      tv_file: val('rename-tv-file'),
    };
  }
  if (key === 'emby') {
    return {
      server_url: val('emby-server-url'),
      api_key: val('emby-api-key'),
      path_mapping: val('emby-path-mapping'),
      style: embyStyleVal,
      refresh_enabled: embyEnabledVal,
    };
  }
  if (key === 'full') {
    return {
      cid: resolveCID('full-cid'),
      path: val('full-cid'),
      local_path: val('full-local'),
      video_ext: getTags('video-ext'),
      image_ext: getTags('image-ext'),
      data_ext: getTags('data-ext'),
    };
  }
  if (key === 'incr') {
    return { cron: val('incr-cron') };
  }
  if (key === 'share') {
    return { folder: resolveCID('share-folder'), folder_path: val('share-folder') };
  }
  if (key === 'monitor') {
    return { dir: val('monitor-dir') }; // 目标固定用全量同步的媒体库（target 字段已废弃）
  }
  return null;
}

function applyConfig(key, v) {
  if (!v) return;
  if (key === 'strm') {
    if (v.domain !== undefined) document.getElementById('strm-domain').value = v.domain;
    if (v.format) setStrmFormat(v.format);
    if (v.exist) setStrmExist(v.exist);
    if (v.keep_ext !== undefined) setStrmExt(v.keep_ext === true || v.keep_ext === 'true');
    updateStrmExample();
  } else if (key === 'proxy') {
    if (v.url !== undefined) document.getElementById('proxy-url').value = v.url;
  } else if (key === 'emby-notify') {
    applyEmbyNotify(v);
  } else if (key === 'message') {
    if (v.wecom) {
      setVal('msg-wecom-corp-id', v.wecom.corp_id);
      setVal('msg-wecom-secret', v.wecom.secret);
      setVal('msg-wecom-agent-id', v.wecom.agent_id);
      setVal('msg-wecom-api-url', v.wecom.api_url || 'https://qyapi.weixin.qq.com');
      setVal('msg-wecom-token', v.wecom.token);
      setVal('msg-wecom-aes-key', v.wecom.encoding_aes_key);
      setMsgEnabled('wecom', v.wecom.enabled === true || v.wecom.enabled === 'true');
    }
    if (v.feishu) {
      setVal('msg-feishu-webhook', v.feishu.webhook);
      setVal('msg-feishu-secret', v.feishu.secret);
      setMsgEnabled('feishu', v.feishu.enabled === true || v.feishu.enabled === 'true');
    }
    if (v.qq_onebot) {
      setVal('msg-onebot-url', v.qq_onebot.url);
      setVal('msg-onebot-token', v.qq_onebot.token);
      setMsgOnebotTargetType(v.qq_onebot.target_type || 'group');
      setVal('msg-onebot-target', v.qq_onebot.target);
      setVal('msg-onebot-admin', v.qq_onebot.admin);
      if (v.qq_onebot.event_token) { onebotEventToken = v.qq_onebot.event_token; }
      renderOnebotCallback();
      setMsgEnabled('onebot', v.qq_onebot.enabled === true || v.qq_onebot.enabled === 'true');
    }
    if (v.qq_official) {
      setVal('msg-qqoff-appid', v.qq_official.app_id);
      setVal('msg-qqoff-secret', v.qq_official.secret);
      setVal('msg-qqoff-group', v.qq_official.group_id);
      setMsgEnabled('qqoff', v.qq_official.enabled === true || v.qq_official.enabled === 'true');
    }
    if (v.tg) {
      setVal('msg-tg-token', v.tg.token);
      setVal('msg-tg-chat-id', v.tg.chat_id);
      setMsgEnabled('tg', v.tg.enabled === true || v.tg.enabled === 'true');
    }
	} else if (key === 'org-basic') {
    // 输入框显示可读路径，cid 存 dataset（兼容旧数据：值本身是数字 cid）；
    // 同步记录 dataset.path，用于检测用户手改路径后的 cid 失配
    const pairs = [['org-pending', 'pending'], ['org-existing', 'existing'], ['org-redundant', 'redundant']];
    pairs.forEach(([id, k]) => {
      const el = document.getElementById(id);
      if (!el) return;
      el.dataset.cid = v[k] || '';
      const shown = v[k + '_path'] !== undefined ? v[k + '_path'] : (v[k] || '');
      el.dataset.path = shown;
      setVal(id, shown);
    });
    if (v.enrich) loadEnrichOpts(v.enrich);
  } else if (key === 'org-recognize') {
    setVal('org-replace-rules', v.replace_rules);
    setVal('org-min-size', v.min_size);
    setVal('org-release-groups', v.release_groups);
  } else if (key === 'org-gpt') {
    setVal('org-gpt-url', v.url);
    setVal('org-gpt-key', v.key);
    setVal('org-gpt-model', v.model);
  } else if (key === 'org-rename') {
    setVal('rename-movie-folder', v.movie_folder);
    setVal('rename-movie-file', v.movie_file);
    setVal('rename-tv-folder', v.tv_folder);
    setVal('rename-tv-file', v.tv_file);
    updateRenameExample();
  } else if (key === 'emby') {
    setVal('emby-server-url', v.server_url);
    setVal('emby-api-key', v.api_key);
    setVal('emby-path-mapping', v.path_mapping);
    if (v.style) {
      setEmbyStyle(v.style === 'windows' || v.style === 'Windows风格' ? 'windows' : 'unix');
      migratedEmbyStyle = true; // 新版已有值时不再被旧 emby-refresh.style 覆盖
    }
    if (v.refresh_enabled !== undefined) {
      setEmbyEnabled(v.refresh_enabled === true || v.refresh_enabled === 'true');
      migratedEmbyEnabled = true; // 新版已有值时不再被旧 emby-refresh.enabled 覆盖
    }
    testEmbyPath();
	} else if (key === 'full') {
    const cidEl = document.getElementById('full-cid');
    if (cidEl) {
      cidEl.dataset.cid = v.cid || '';
      const shown = v.path !== undefined ? v.path : (v.cid || '');
      cidEl.dataset.path = shown;
      setVal('full-cid', shown);
    }
    setVal('full-local', v.local_path);
    if (v.video_ext) setTags('video-ext', v.video_ext);
    if (v.image_ext) setTags('image-ext', v.image_ext);
    if (v.data_ext) setTags('data-ext', v.data_ext);
  } else if (key === 'incr') {
    setVal('incr-cron', v.cron);
	} else if (key === 'share') {
    const el = document.getElementById('share-folder');
    if (el) {
      el.dataset.cid = v.folder || '';
      const shown = v.folder_path !== undefined ? v.folder_path : (v.folder || '');
      el.dataset.path = shown;
      setVal('share-folder', shown);
    }
  } else if (key === 'monitor') {
    setVal('monitor-dir', v.dir);
  }
}

// setTags 用数组重建标签输入框的标签集
function setTags(containerId, values) {
  const container = document.getElementById(containerId);
  if (!container) return;
  container.querySelectorAll('.tag').forEach(t => t.remove());
  (values || []).forEach(v => {
    const tag = document.createElement('span');
    tag.className = 'tag';
    tag.dataset.val = v;
    tag.innerHTML = v + '<span class="tag-close" onclick="removeTag(this)">×</span>';
    container.insertBefore(tag, container.querySelector('.tag-add'));
  });
}

// warnDirOverlap 检查路径与媒体库目录的包含关系（相等或互为前缀），
// 提示用户自动整理可能误处理库内文件（如把库里的分类目录搬进冗余）
function dirOverlapWarn(a, b) {
  const norm = p => (p || '').trim().replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase();
  const na = norm(a), nb = norm(b);
  if (!na || !nb || na === '/') return false;
  return na === nb || na.startsWith(nb + '/') || nb.startsWith(na + '/');
}
function warnShareDirOverlap() {
  const lib = document.getElementById('full-cid');
  const share = document.getElementById('share-folder');
  if (lib && share && dirOverlapWarn(lib.value, share.value)) {
    toast('⚠ 转存目录与媒体库目录存在包含关系，自动整理会跳过以防误搬库内容，请修正目录配置');
    appendLog('⚠ 转存目录与媒体库存在包含关系：' + share.value + ' vs ' + lib.value);
  }
}
function warnPendingDirOverlap() {
  const lib = document.getElementById('full-cid');
  const pending = document.getElementById('org-pending');
  // 待整理目录本就允许建在媒体库内部（常见布局），只警告"覆盖整个库"的危险方向
  if (lib && pending && pending.value) {
    const norm = p => (p || '').trim().replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase();
    const np = norm(pending.value), nl = norm(lib.value);
    if (np && nl && np !== '/' && (np === nl || nl.startsWith(np + '/'))) {
      toast('⚠ 待整理目录覆盖到媒体库，库内条目会被跳过（不会被误整理），建议待整理与媒体库平级或建在库内');
    }
  }
}

// firstUnresolvedCID 返回第一个"有输入文本但解析不出 cid"的输入框 id（全解析成功返回 ''）
function firstUnresolvedCID(ids) {
  for (const id of ids) {
    const el = document.getElementById(id);
    if (!el) continue;
    if ((el.value || '').trim() && !resolveCID(id)) return id;
  }
  return '';
}

async function saveConfig(key) {
  // 含 115 目录的配置：先把手填路径解析成 cid；解析失败直接拦截，
  // 避免存入空 cid 或与显示路径不符的旧 cid
  let cidFields = [];
  if (key === 'full') cidFields = ['full-cid'];
  if (key === 'share') cidFields = ['share-folder'];
  if (key === 'org-basic') cidFields = ['org-pending', 'org-existing', 'org-redundant'];
  if (cidFields.length) {
    await Promise.all(cidFields.map(resolveInputCID));
    if (firstUnresolvedCID(cidFields)) {
      toast('目录路径无法识别：请点「选择目录」重新选择，或输入纯数字 cid');
      return;
    }
  }
  const value = collectConfig(key);
  if (value === null) { toast('该配置暂未支持保存'); return; }
  // 防重复点击：请求期间禁用事件源按钮
  const btn = (typeof event !== 'undefined' && event && event.target && event.target.closest) ? event.target.closest('button') : null;
  const origText = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '保存中...'; }
  try {
    await api('/config/setting', { method: 'POST', body: JSON.stringify({ key, value: JSON.stringify(value) }) });
    toast('保存成功');
    if (key === 'share') warnShareDirOverlap();
    if (key === 'org-basic') warnPendingDirOverlap();
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = origText; } }
}

// 重置配置（带气泡确认）
function resetConfig(key, btn) {
  // 移除已有气泡和旧的监听器
  closeConfirmBubble();
  document.removeEventListener('click', closeConfirmBubbleOnOutside);
  // 创建气泡
  const bubble = document.createElement('div');
  bubble.id = 'confirm-bubble';
  bubble.className = 'confirm-bubble';
  bubble.innerHTML = '<div class="cb-text">确定重置此配置？</div><div class="cb-actions"><button class="cb-cancel">取消</button><button class="cb-ok">确定</button></div>';
  document.body.appendChild(bubble);
  // fixed 定位到按钮上方
  const rect = btn.getBoundingClientRect();
  bubble.style.position = 'fixed';
  bubble.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
  bubble.style.left = 'auto';
  bubble.style.bottom = (window.innerHeight - rect.top + 8) + 'px';
  bubble.classList.add('show');
  bubble.querySelector('.cb-cancel').onclick = (e) => { e.stopPropagation(); closeConfirmBubble(); };
  bubble.querySelector('.cb-ok').onclick = (e) => {
    e.stopPropagation();
    closeConfirmBubble();
    doResetConfig(key, btn);
  };
  // 延迟注册点击外部关闭（跳过当前事件循环）
  setTimeout(() => {
    document.addEventListener('click', closeConfirmBubbleOnOutside, { once: true });
  }, 0);
}

// 各配置的默认值
const DEFAULT_CONFIGS = {
  'emby': { server_url: '', api_key: '', path_mapping: '', style: 'unix', refresh_enabled: true },
  'strm': { domain: '', format: 'pick_code_name', keep_ext: 'true', exist: 'overwrite' },
  'proxy': { url: '' },
  'emby-notify': { token: '' },
  'org-basic': { pending: '', pending_path: '', existing: '', existing_path: '', redundant: '', redundant_path: '', enrich: { enabled: false, mode: 'standard', missing: 'rename', conflict_low: 'rename', conflict_high: 'rename', full_named: 'keep' } },
  'org-recognize': { replace_rules: '', release_groups: '', min_size: '0' },
  'org-gpt': { url: 'https://api.siliconflow.cn/v1', key: '', model: '' },
  'org-rename': {
    movie_folder: '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]',
    movie_file: '{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}',
    tv_folder: '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]',
    tv_file: '{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}',
  },
  'monitor': { dir: '', target: '' },
  'message': { wecom: { corp_id: '', secret: '', agent_id: '', api_url: 'https://qyapi.weixin.qq.com', token: '', encoding_aes_key: '', enabled: false }, tg: { token: '', chat_id: '', enabled: false }, feishu: { webhook: '', secret: '', enabled: false }, qq_onebot: { url: '', token: '', target_type: 'group', target: '', admin: '', event_token: '', enabled: false }, qq_official: { app_id: '', secret: '', group_id: '', enabled: false } },
  'full': { cid: '', local_path: '/media', video_ext: ['mp4','mkv','ts','avi','mov','rmvb','webm','flv','m2ts','wmv','mpg','iso'], image_ext: ['jpg','png','jpeg','webp'], data_ext: ['ass','srt','ssa','sub'] },
  'incr': { cron: '*/10 8-23 * * *' },
  'share': { folder: '' },
  'tmdb': { api_key: '', api_url: 'https://api.tmdb.org', image_url: 'https://image.tmdb.org', language: 'zh-CN' },
};

async function doResetConfig(key, btn) {
  try {
    const defaults = DEFAULT_CONFIGS[key] || {};
    // 特殊处理：使用独立保存接口的配置直接设默认值
    if (key === 'tmdb') {
      await api('/config/tmdb', { method: 'POST', body: JSON.stringify({
        api_url: defaults.api_url, image_api_url: defaults.image_url, api_key: defaults.api_key, language: defaults.language
      }) });
      setVal('tmdb-api-url', defaults.api_url);
      setVal('tmdb-image-url', defaults.image_url);
      setVal('tmdb-api-key', defaults.api_key);
      toast('配置已恢复默认值');
      return;
    }
    if (key === 'category') {
      await api('/scrape/categories', { method: 'POST', body: JSON.stringify({ yaml: DEFAULT_CATEGORY_YAML }) });
      const rstTa = document.getElementById('category-yaml');
      rstTa.value = DEFAULT_CATEGORY_YAML;
      rstTa._yamlRender && rstTa._yamlRender();
      toast('配置已恢复默认值');
      return;
    }
    if (['full', 'incr', 'share'].includes(key)) {
      // 这些配置走通用 setting 接口，直接保存默认值
      await api('/config/setting', { method: 'POST', body: JSON.stringify({ key, value: JSON.stringify(defaults) }) });
    } else {
      await api('/config/setting', { method: 'POST', body: JSON.stringify({ key, value: JSON.stringify(defaults) }) });
    }
    applyConfig(key, defaults);
    toast('配置已恢复默认值');
  } catch (e) { toast(e.message); }
}

function closeConfirmBubble() {
  const b = document.getElementById('confirm-bubble');
  if (b) b.remove();
}

function closeConfirmBubbleOnOutside(e) {
  // 点击气泡内部或重置按钮本身时不关闭
  if (!e.target.closest('#confirm-bubble') && !e.target.closest('[onclick*="resetConfig"]')) {
    closeConfirmBubble();
  }
}

async function loadConfigs() {
  migratedEmbyEnabled = false;
  migratedEmbyStyle = false;
  const keys = ['emby', 'full', 'strm', 'proxy', 'emby-notify', 'org-basic', 'org-recognize', 'org-gpt', 'org-rename', 'message', 'incr', 'share', 'monitor'];
  // 并行拉取，避免逐个等待导致 cid 等字段迟迟不回填
  await Promise.all(keys.map(async (key) => {
    try {
      const data = await api('/config/setting?key=' + key);
      if (!data.value) return;
      applyConfig(key, JSON.parse(data.value));
    } catch (e) { console.error('[配置回填失败]', key, e && e.message); }
  }));
  // emby-notify 可能尚无配置（首次使用），走同一逻辑生成 token 并展示地址
  if (!document.getElementById('emby-webhook-url')?.textContent) applyEmbyNotify({});
  // 旧版 emby-refresh 配置迁移：在 emby 配置应用完成后统一回填，避免并行竞态
  try {
    const data = await api('/config/setting?key=emby-refresh');
    if (data.value) {
      const v = JSON.parse(data.value);
      const mapEl = document.getElementById('emby-path-mapping');
      if (v.path_rule && mapEl && !mapEl.value.trim()) mapEl.value = v.path_rule;
      if (v.style && !migratedEmbyStyle) setEmbyStyle(v.style === 'windows' || v.style === 'Windows风格' ? 'windows' : 'unix');
      if (v.enabled !== undefined && !migratedEmbyEnabled) setEmbyEnabled(v.enabled === true || v.enabled === 'true');
      testEmbyPath();
    }
  } catch (e) {}
}

// ==================== 媒体信息补全配置 ====================
const enrichOpts = { enabled: false, mode: 'standard', missing: 'rename', clow: 'rename', chigh: 'rename', full: 'keep' };
function setEnrichOpt(key, v) {
  enrichOpts[key] = v;
  const map = { enabled: 'enrich-enabled-switch', mode: 'enrich-mode-switch', missing: 'enrich-missing-switch', clow: 'enrich-clow-switch', chigh: 'enrich-chigh-switch', full: 'enrich-full-switch' };
  document.querySelectorAll('#' + map[key] + ' .seg-item').forEach(el => {
    el.classList.toggle('active', el.dataset.value === String(v));
  });
}
function loadEnrichOpts(v) {
  if (!v) return;
  // 保存侧字段名是 conflict_low/conflict_high/full_named，回填侧历史用 clow/chigh/full——两者都兼容
  const norm = {
    mode: v.mode, missing: v.missing,
    clow: v.conflict_low !== undefined ? v.conflict_low : v.clow,
    chigh: v.conflict_high !== undefined ? v.conflict_high : v.chigh,
    full: v.full_named !== undefined ? v.full_named : v.full,
  };
  if (v.enabled !== undefined) setEnrichOpt('enabled', v.enabled === true || v.enabled === 'true');
  ['mode', 'missing', 'clow', 'chigh', 'full'].forEach(k => { if (norm[k]) setEnrichOpt(k, norm[k]); });
}
// org-basic 保存时附带补全配置

// ==================== 插件：一键创建 Emby 媒体库 ====================
let embyLibItems = [];
function renderEmbyLibTable(data, box, withCreateButton) {
  const existsCount = data.data.length - embyLibItems.length;
  const rows = data.data.map(x => `<tr>
    <td>${esc(x.name)}</td>
    <td>${x.type_label}</td>
    <td style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc(x.emby_path)}">${esc(x.emby_path)}</td>
    <td>${x.exists ? '<span style="color:#f0ad4e">已存在</span>' : '<span style="color:#27ae60">将创建</span>'}</td>
  </tr>`).join('');
  box.innerHTML = `
    <table>
      <thead><tr><th>库名</th><th>类型</th><th>Emby 路径</th><th>状态</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    <div style="margin-top:8px;display:flex;align-items:center;gap:10px">
      ${withCreateButton ? `<button class="btn btn-primary btn-sm" ${embyLibItems.length ? '' : 'disabled'} onclick="embyLibCreate()">创建 ${embyLibItems.length} 个库</button>` : ''}
      <span style="color:var(--text-3)">${existsCount ? existsCount + ' 个已存在将跳过' : ''}</span>
    </div>`;
  const mbtn = document.getElementById('emby-lib-create-btn');
  if (mbtn) { mbtn.disabled = !embyLibItems.length; mbtn.textContent = `创建 ${embyLibItems.length} 个库`; }
}
// kind: 'modal'（配置规则弹窗）| 'panel'（立即运行结果面板）
async function embyLibScanTo(kind) {
  const box = document.getElementById(kind === 'modal' ? 'emby-lib-modal-body' : 'emby-lib-result');
  box.innerHTML = '扫描中...';
  try {
    const data = await api('/plugin/emby-libraries');
    if (!data.emby_configured) {
      box.innerHTML = '<span style="color:#e74c3c">未配置 Emby 服务器，请先在「系统配置 → EMBY 管理」填写地址与 API 密钥</span>';
      return false;
    }
    if (!data.data || !data.data.length) {
      box.innerHTML = '未在媒体库根目录下发现分类目录（需要 根/分类/子目录 或 根/分类 结构）';
      return false;
    }
    embyLibItems = data.data.filter(x => !x.exists);
    renderEmbyLibTable(data, box, kind !== 'modal');
    return true;
  } catch (e) {
    box.innerHTML = '<span style="color:#e74c3c">' + esc(e.message) + '</span>';
    return false;
  }
}
function embyLibConfig() {
  document.getElementById('emby-lib-modal').style.display = 'flex';
  embyLibScanTo('modal');
}
function embyLibScanAgain() { embyLibScanTo('modal'); }
function closeEmbyLibModal() { document.getElementById('emby-lib-modal').style.display = 'none'; }
async function embyLibRun() {
  const box = document.getElementById('emby-lib-result');
  const btn = document.getElementById('emby-lib-run-btn');
  box.style.display = 'block';
  box.innerHTML = '扫描中...';
  if (btn) btn.disabled = true;
  try {
    const ok = await embyLibScanTo('panel');
    if (!ok) return;
    if (!embyLibItems.length) { toast('没有需要创建的媒体库，全部已存在'); return; }
    if (!confirm(`将创建 ${embyLibItems.length} 个 Emby 媒体库（${embyLibItems.map(x => x.name).join('、')}），确认执行？`)) return;
    await embyLibCreate();
  } catch (e) {
    box.innerHTML = '<span style="color:#e74c3c">' + esc(e.message) + '</span>';
  } finally {
    if (btn) btn.disabled = false;
  }
}
async function embyLibCreate() {
  const inModal = document.getElementById('emby-lib-modal').style.display !== 'none';
  const target = document.getElementById(inModal ? 'emby-lib-modal-body' : 'emby-lib-result');
  target.innerHTML = '创建中...';
  const mbtn = document.getElementById('emby-lib-create-btn');
  if (mbtn) mbtn.disabled = true;
  try {
    const data = await api('/plugin/emby-libraries', { method: 'POST', body: JSON.stringify({ items: embyLibItems }) });
    toast(data.message || '完成');
  } catch (e) {
    target.innerHTML = '<span style="color:#e74c3c">' + esc(e.message) + '</span>';
    return;
  }
  setTimeout(() => embyLibScanTo(inModal ? 'modal' : 'panel'), 600);
}

// ==================== 媒体信息补全 ====================

// ==================== 115 离线任务面板 ====================
function fmtSize(n) {
  if (!n || n <= 0) return '-';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + u[i];
}


// ==================== YAML 编辑器高亮（透明文本域 + 着色层） ====================
function escHtml(t) {
  return String(t).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// 值部分着色：字符串/数字/布尔/行内注释
// 把已有 textarea 升级为高亮编辑器：包一层 wrap，加着色 pre，输入/滚动同步
function attachYamlHighlight(id) {
  const ta = document.getElementById(id);
  if (!ta || ta.dataset.yamlHl) return;
  ta.dataset.yamlHl = '1';
  // CodeMirror 5 懒加载：编辑器所在面板可见时才拉取资源并挂载
  // （隐藏面板永不与视口相交——用户真正打开分类/洗版策略才发 vendor 请求，
  // 首屏请求只剩 HTML+app.js；弱网下每个并行请求都是被重置的候选）。
  // 资源加载失败退化为普通文本域——无高亮但行为绝对精确
  const mount = () => {
    ensureCodeMirror().then(() => {
      const ta2 = document.getElementById(id);
      if (!window.CodeMirror || !ta2 || ta2._yamlRender) return;
      const cm = CodeMirror.fromTextArea(ta2, {
        mode: 'yaml',
        lineNumbers: true,
        styleActiveLine: true,
        lineWrapping: false,
        tabSize: 2,
        indentUnit: 2,
      });
      cm.setSize('100%', 'auto');
      // 兼容旧读写端：
      //   写：el.value = X; el._yamlRender()   → 同步进编辑器
      //   读：el._yamlSync() 后再读 el.value
      ta2._yamlRender = () => { cm.setValue(ta2.value); cm.refresh(); refreshVisibleCM(ta2.parentNode); };
      ta2._yamlSync = () => cm.save();
      if (ta2.value) cm.setValue(ta2.value); // 懒加载期间可能已回填内容
      window._cmInstances = window._cmInstances || [];
      window._cmInstances.push(cm);
    }).catch(() => { /* 加载失败保持纯文本域 */ });
  };
  if (typeof IntersectionObserver === 'undefined') { mount(); return; }
  const io = new IntersectionObserver((entries) => {
    if (entries.some(e => e.isIntersecting)) {
      io.disconnect();
      mount();
    }
  });
  io.observe(ta);
}

// ensureCodeMirror 按需加载 CodeMirror 5（CSS+主库+yaml 模式+active-line，
// 顺序注入；任一步失败 reject，调用方降级为纯文本域）
function ensureCodeMirror() {
  if (window.CodeMirror) return Promise.resolve();
  if (!ensureCodeMirror._p) {
    ensureCodeMirror._p = (async () => {
      const load = (src) => new Promise((res, rej) => {
        const s = document.createElement('script');
        s.src = src;
        s.onload = res;
        s.onerror = () => rej(new Error('load fail: ' + src));
        document.body.appendChild(s);
      });
      if (!document.getElementById('cm-style')) {
        const link = document.createElement('link');
        link.id = 'cm-style';
        link.rel = 'stylesheet';
        link.href = '/vendor/cm5/codemirror.min.css';
        document.head.appendChild(link);
      }
      await load('/vendor/cm5/codemirror.min.js');
      await load('/vendor/cm5/mode/yaml.min.js');
      await load('/vendor/cm5/active-line.min.js');
    })().catch(e => { ensureCodeMirror._p = null; throw e; }); // 失败可重试
  }
  return ensureCodeMirror._p;
}

// ==================== 日志级别 ====================
async function loadDashboard() {
  try {
    const d = await api('/dashboard');
    /* ---- 存储空间 ---- */
    const st = d.storage || {};
    const stTotal = Number(st.total || 0), stUsed = Number(st.used || 0);
    setTxt('dash-storage-used', st.used_h || humanBytes(stUsed));
    const stPct = stTotal > 0 ? Math.min(100, stUsed / stTotal * 100) : 0;
    setTxt('dash-storage-pct', '已使用 ' + stPct.toFixed(1) + '%');
    setBar('dash-storage-bar', stPct);
    setTxt('dash-storage-detail', '可用 ' + (st.free_h || humanBytes(stTotal - stUsed)) + ' / 总容量 ' + (st.total_h || humanBytes(stTotal)));
    /* ---- 媒体统计 ---- */
    const md = d.media || {};
    setTxt('dash-movies', fmtN(md.movies));
    setTxt('dash-tvs', fmtN(md.tvs));
    setTxt('dash-movies-month', '+' + (md.movies_month || 0) + ' 本月新增');
    setTxt('dash-tvs-month', '+' + (md.tvs_month || 0) + ' 本月新增');
    const strm = d.strm || {};
    setTxt('dash-strm-count', fmtN(strm.total));
    setTxt('dash-strm-invalid', strm.invalid || 0);
    setTxt('dash-organized', fmtN(d.organized));
    setTxt('dash-synced', fmtN(d.synced_files));
    /* ---- 最近整理（海报行） ---- */
    const recent = d.recent_media || [];
    const rb = document.getElementById('dash-recent');
    if (recent.length) {
      rb.innerHTML = recent.map(m =>
        '<div class="dash-recent-item">'
        + (m.poster ? '<img src="/api/poster' + esc(m.poster) + '" loading="lazy" onerror="this.style.opacity=0">' : '<img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=">')
        + '<div class="t">' + esc(m.title) + ' <span style="color:var(--text-3)">' + esc(m.year || '') + '</span><div style="font-size:11.5px;color:var(--text-3)">' + esc(m.category || m.type || '') + '</div></div>'
        + '<div class="m">' + esc(m.at) + '</div></div>').join('');
    } else {
      rb.innerHTML = '<div class="dash-empty">暂无整理记录</div>';
    }
    /* ---- 近 7 天入库（SVG 柱图） ---- */
    renderWeekly(d.weekly || [], d.week_total || 0);
    /* ---- 系统状态 ---- */
    const sys = d.sys || {};
    if (sys.mem_percent !== undefined) {
      setTxt('dash-mem-pct', sys.mem_percent.toFixed(1) + '%');
      setBar('dash-mem-bar', sys.mem_percent);
      setTxt('dash-mem-detail', humanBytes(sys.mem_used_mb * 1048576) + ' / ' + humanBytes(sys.mem_total_mb * 1048576));
    } else {
      setTxt('dash-mem-pct', '-'); setTxt('dash-mem-detail', '不可用');
    }
    if (sys.cpu_percent !== undefined) {
      setTxt('dash-cpu-pct', sys.cpu_percent.toFixed(1) + '%');
      setBar('dash-cpu-bar', sys.cpu_percent);
    } else {
      setTxt('dash-cpu-pct', '-');
    }
    /* ---- 我的媒体库分类卡（点击打开观影门户；Emby 源时 = Emby 媒体库） ---- */
    const em = d.emby || {};
    let cats = d.categories || [];
    if (em.libraries && em.libraries.length) {
      cats = em.libraries.map(l => ({
        name: l.name, count: l.count,
        posters: (l.collage || []).map(p => '/api/embyimg?path=' + encodeURIComponent(p) + '&maxWidth=200'),
      }));
    }
    const cb = document.getElementById('dash-cats');
    if (cats.length) {
      cb.innerHTML = cats.map(c => {
        let inner;
        if (c.posters && c.posters.length) {
          inner = '<div class="collage">' + [0,1,2,3].map(i =>
            c.posters[i] ? '<img src="' + esc(c.posters[i]) + '" loading="lazy">' : '<div style="background:var(--fill-2)"></div>').join('') + '</div>';
        } else {
          inner = '<div class="cat-placeholder">' + esc((c.name || '?').slice(0, 4)) + '</div>';
        }
        return '<div class="dash-cat" title="' + esc(c.name) + ' · ' + c.count + ' 部（点击打开观影门户）" onclick="openPortal()">' + inner
          + '<div class="cat-name">' + esc(c.name) + '</div>'
          + '<div class="cat-count">' + fmtN(c.count) + '</div></div>';
      }).join('');
    } else {
      cb.innerHTML = '<div class="dash-empty">暂无入库记录 · 整理或同步后这里会显示分类卡片</div>';
    }
    /* ---- 最新入库海报墙（Emby 源时 = Emby 最新入库，点击打开观影门户） ---- */
    const pb = document.getElementById('dash-posters');
    if (em.recent && em.recent.length) {
      pb.innerHTML = em.recent.map(m =>
        '<div class="dash-poster" title="' + esc(m.name + ' ' + (m.year || '')) + '（点击打开观影门户）" onclick="openPortal()">'
        + '<img src="/api/embyimg?path=' + encodeURIComponent('Items/' + m.id + '/Images/Primary') + '&maxWidth=200" loading="lazy">'
        + '<div class="p-title">' + esc(m.name) + '</div></div>').join('');
    } else if (recent.length) {
      pb.innerHTML = recent.map(m =>
        '<div class="dash-poster" title="' + esc(m.title + ' ' + (m.year || '')) + '">'
        + (m.poster ? '<img src="/api/poster' + esc(m.poster) + '" loading="lazy">'
                    : '<div class="p-none">🎬</div>')
        + '<div class="p-title">' + esc(m.title) + '</div></div>').join('');
    } else {
      pb.innerHTML = '<div class="dash-empty">暂无入库</div>';
    }
  } catch (e) { console.error('[仪表盘] 加载失败:', e); }
}

function setTxt(id, v) { const el = document.getElementById(id); if (el) el.textContent = v; }
// 账号菜单动作：跳到代理 Tab 并自动执行网络连接测试
function menuNetworkTest() {
  toggleAccountMenu();
  showPage('config-system');
  switchTab('page-config-system', 'proxy');
  setTimeout(() => {
    const btn = document.getElementById('network-check-btn');
    if (btn) networkCheck(btn);
  }, 400);
}
// 账号菜单动作：新标签打开使用文档
function menuDocs() {
  window.open('https://strmhub.rth1.xyz/', '_blank');
}
// 新标签页打开观影门户（同主机 6688 端口）
function openPortal() {
  window.open(location.protocol + '//' + location.hostname + ':6688', '_blank');
}
function setBar(id, pct) {
  const el = document.getElementById(id);
  if (!el) return;
  el.style.width = Math.max(2, Math.min(100, pct)) + '%';
  el.className = pct > 85 ? 'danger' : (pct > 65 ? 'warn' : '');
}
function fmtN(n) { return Number(n || 0).toLocaleString('en-US'); }
function humanBytes(b) {
  b = Number(b || 0);
  if (b <= 0) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return b.toFixed(i > 1 ? 2 : 0) + ' ' + u[i];
}
// 近 7 天入库柱状图（内联 SVG，无外部依赖）
function renderWeekly(weekly, total) {
  const box = document.getElementById('dash-weekly');
  if (!box) return;
  const max = Math.max(1, ...weekly.map(w => w.count));
  const W = 320, H = 130, pad = 6, bw = W / weekly.length;
  let bars = '';
  weekly.forEach((w, i) => {
    const h = w.count > 0 ? Math.max(4, (H - 30) * w.count / max) : 2;
    const x = i * bw + bw * 0.18, y = H - 18 - h;
    bars += '<rect x="' + x.toFixed(1) + '" y="' + y.toFixed(1) + '" width="' + (bw * 0.64).toFixed(1)
      + '" height="' + h.toFixed(1) + '" rx="3" fill="#7c3aed"'
      + (w.count ? ' opacity="0.9"' : ' opacity="0.25"') + '></rect>';
    if (w.count) bars += '<text x="' + (x + bw * 0.32).toFixed(1) + '" y="' + (y - 4).toFixed(1)
      + '" font-size="10" fill="#4e5969" text-anchor="middle">' + w.count + '</text>';
    bars += '<text x="' + (x + bw * 0.32).toFixed(1) + '" y="' + (H - 5) + '" font-size="9.5" fill="#86909c" text-anchor="middle">' + esc(w.day.slice(3)) + '</text>';
  });
  box.innerHTML = '<svg viewBox="0 0 ' + W + ' ' + H + '">' + bars + '</svg>';
  setTxt('dash-week-total', '最近一周入库 ' + total + ' 部' + (total ? ' 🎉' : ' 😴'));
}
// 快捷操作（复用既有确认与执行逻辑）
// 版本号（左下角 footer 显示）+ 刷新时自动检查 GitHub 是否有新版本
// versionPollTimer 构建中的轮询定时器（构建完成后自动把灰标签换成「有新版本」）
let versionPollTimer = null;

async function loadVersion() {
  let local = '';
  try {
    const data = await api('/version');
    local = String(data.version || '');
  } catch (e) {}
  const el = document.getElementById('footer-version');
  if (el && local) {
    el.textContent = 'StrmHub v' + local.slice(0, 7);
    // 版本号可点击：随时手动检查更新（不依赖"有新版本"提示出现）
    el.style.cursor = 'pointer';
    el.title = '点击检查更新';
    el.onclick = openUpdateModal;
  }
  // 本地 dev 构建无版本可比，跳过更新检查
  if (!el || !local || local === 'dev') return;
  try {
    const d = await api('/version/latest');
    if (d.latest && d.latest.slice(0, 7) !== local.slice(0, 7)) {
      // 版本号右侧同行显示小按钮（flex 不换行，避免窄侧栏下被挤到下一行）。
      // 镜像是否可更新以 GitHub Actions 构建状态为准：构建中显示灰标签、
      // 构建失败显示红标签，只有构建成功才出现「有新版本」按钮（点了才有用）
      const flex = () => {
        el.style.display = 'flex'; el.style.alignItems = 'center';
        el.style.gap = '6px'; el.style.whiteSpace = 'nowrap';
      };
      if (d.build === 'building') {
        flex();
        el.innerHTML = '<span>StrmHub v' + local.slice(0, 7) + '</span>' +
          '<span style="flex:none;font-size:11px;padding:2px 8px;border-radius:4px;background:var(--border);color:var(--text-3)">镜像构建中…</span>';
        // 构建期间每分钟自动复查，完成后自动换成可点击的「有新版本」
        if (!versionPollTimer) {
          versionPollTimer = setTimeout(() => { versionPollTimer = null; loadVersion(); }, 60000);
        }
      } else if (d.build === 'failed') {
        flex();
        el.innerHTML = '<span>StrmHub v' + local.slice(0, 7) + '</span>' +
          '<span style="flex:none;font-size:11px;padding:2px 8px;border-radius:4px;background:var(--danger);color:#fff;cursor:pointer" onclick="openUpdateModal()">构建失败</span>';
      } else {
        flex();
        el.innerHTML = '<span>StrmHub v' + local.slice(0, 7) + '</span>' +
          '<button class="btn btn-sm" onclick="openUpdateModal()" style="background:var(--primary);color:#fff;border:none;flex:none;font-size:11px;height:22px;padding:0 8px;border-radius:4px;cursor:pointer">有新版本</button>';
      }
    }
  } catch (e) {}
}

// ==================== 账号（下拉菜单 / 修改用户名密码 / 退出登录） ====================
function toggleAccountMenu(ev) {
  if (ev) ev.stopPropagation();
  const m = document.getElementById('account-menu');
  const show = m.style.display !== 'block';
  m.style.display = show ? 'block' : 'none';
  if (show) {
    document.getElementById('account-menu-user').textContent = localStorage.getItem('username') || '-';
    // 动态定位到【可见的】触发按钮正下方（页面上有移动端/桌面端两个按钮，
    // 取错隐藏的那个会得到全零坐标，菜单叠在顶栏上像"点了没反应"）
    let btn = null;
    document.querySelectorAll('[data-account-btn]').forEach(b => {
      if (!btn && b.offsetParent !== null) btn = b;
    });
    if (btn) {
      const r = btn.getBoundingClientRect();
      m.style.top = Math.round(r.bottom + 6) + 'px';
      m.style.right = Math.max(8, Math.round(window.innerWidth - r.right)) + 'px';
      m.style.left = 'auto';
    }
  }
}
document.addEventListener('click', e => {
  const m = document.getElementById('account-menu');
  if (m && m.style.display === 'block' && !e.target.closest('#account-menu') && !e.target.closest('[data-account-btn]')) {
    m.style.display = 'none';
  }
});

function logoutNow() {
  localStorage.clear();
  history.pushState(null, '', '/login');
  location.reload();
}

// ==================== 应用内更新面板 ====================
async function openUpdateModal() {
  const mask = document.getElementById('update-modal');
  const body = document.getElementById('update-modal-body');
  const btn = document.getElementById('update-apply-btn');
  mask.style.display = '';
  body.textContent = '获取更新内容中...';
  btn.disabled = false; btn.textContent = '立即更新'; btn.style.display = '';
  try {
    const d = await api('/version/changes');
    // 已是最新：明确告知而不是"发现新版本 vX（当前 vX）"
    if (d.uptodate) {
      document.getElementById('update-modal-title').textContent = '已是最新版本';
      body.innerHTML = '<p>✓ 当前 v' + String(d.current || '').slice(0, 7) + ' 已是最新。' +
        (d.error ? '<br><span style="font-size:12px;color:var(--warning)">' + escHtml(d.error) + '</span>' : '') + '</p>';
      document.getElementById('update-apply-btn').style.display = 'none';
      return;
    }
    // 镜像构建状态（GitHub Actions）：构建中/失败时不可更新——
    // 提交已推送但镜像未发布，此时点更新只会拉到旧镜像并白重启一次
    if (d.build === 'building' || d.build === 'failed') {
      document.getElementById('update-modal-title').textContent =
        d.build === 'building'
          ? '新版本构建中 v' + String(d.latest || '').slice(0, 7)
          : '新版本构建失败 v' + String(d.latest || '').slice(0, 7);
      btn.disabled = true;
      btn.textContent = d.build === 'building' ? '镜像构建中…' : '构建失败，不可更新';
    } else {
      document.getElementById('update-modal-title').textContent =
        '发现新版本 v' + String(d.latest || '').slice(0, 7) + '（当前 v' + String(d.current || '').slice(0, 7) + '）';
    }
    if (!d.commits || !d.commits.length) {
      body.innerHTML = d.error
        ? '<p>更新内容获取失败：' + escHtml(d.error) + '</p><p>可点「查看 GitHub」直接查看提交记录。</p>'
        : '<p>未获取到提交记录，可点「查看 GitHub」查看。</p>';
      return;
    }
    if (d.build === 'building' && !updateBuildPollTimer) {
      updateBuildPollTimer = setTimeout(() => { updateBuildPollTimer = null; openUpdateModal(); }, 45000);
    }
    body.innerHTML =
      (d.build === 'building' ? '<p style="color:var(--warning)">⏳ 镜像还在 GitHub Actions 构建中（通常 3~8 分钟），完成后本按钮自动可用。</p>' : '') +
      (d.build === 'failed' ? '<p style="color:var(--danger)">✗ 最新提交的 CI 构建失败，镜像未发布；请到 GitHub Actions 查看失败原因。</p>' : '') +
      '<p style="margin-bottom:6px"><b>更新内容（' + d.commits.length + ' 个提交）：</b></p>' +
      d.commits.map(cm =>
        '<div style="display:flex;gap:8px;padding:5px 0;border-bottom:1px solid var(--border);align-items:baseline">' +
        '<code style="color:var(--text-3);flex:none">' + String(cm.sha).slice(0, 7) + '</code>' +
        '<span>' + escHtml(cm.message) + '</span></div>').join('');
  } catch (e) {
    body.innerHTML = '<p>获取失败：' + escHtml(e.message) + '</p>';
  }
}
let updatePollTimer = null;
let updateBuildPollTimer = null;
function closeUpdateModal() {
  document.getElementById('update-modal').style.display = 'none';
  if (updatePollTimer) { clearInterval(updatePollTimer); updatePollTimer = null; }
  if (updateBuildPollTimer) { clearTimeout(updateBuildPollTimer); updateBuildPollTimer = null; }
}

async function applyUpdate(btn) {
  btn.disabled = true; btn.textContent = '更新中...';
  const body = document.getElementById('update-modal-body');
  let latest = '';
  try {
    const d = await api('/update/apply', { method: 'POST' });
    if (d.message && d.message.includes('已是最新')) {
      btn.textContent = '已是最新'; body.insertAdjacentHTML('afterbegin', '<p>✓ ' + escHtml(d.message) + '</p>');
      return;
    }
    latest = d.latest || '';
    body.insertAdjacentHTML('afterbegin',
      '<p>✓ 镜像已拉取，容器重启中（预计 10~30 秒）…页面将自动刷新。</p>');
    btn.textContent = '重启中...';
  } catch (e) {
    // 无 docker.sock 等场景：错误信息自带配置指引
    btn.disabled = false; btn.textContent = '立即更新';
    body.insertAdjacentHTML('afterbegin',
      '<p style="color:var(--danger);white-space:pre-line">✗ ' + escHtml(e.message || '更新失败') + '</p>');
    return;
  }
  // 轮询服务恢复：版本变化或服务可达即刷新（关闭弹窗即取消，
  // 此前关掉弹窗后最长 3 分钟仍会自动 reload）
  let tries = 0;
  updatePollTimer = setInterval(async () => {
    tries++;
    try {
      const d = await api('/version');
      const v = String(d.version || '');
      if (v && v !== 'dev' && (!latest || v.slice(0, 7) === latest.slice(0, 7))) {
        clearInterval(updatePollTimer); updatePollTimer = null;
        toast('✓ 已更新到 v' + v.slice(0, 7));
        setTimeout(() => location.reload(), 800);
      }
    } catch (e) { /* 重启期间不可达，继续等 */ }
    if (tries > 60) {
      clearInterval(updatePollTimer); updatePollTimer = null;
      btn.disabled = false; btn.textContent = '重试';
    }
  }, 3000);
}

// ==================== 日志 ====================
let logPollTimer = null;
// 操作日志面板已移除（日志页只保留服务端任务日志）；
// appendLog 保留为空操作，历史调用点无需逐一清理
function appendLog(line) {}
function openLog() {
  showPage('logs');
  startLogPoll();
}
// 服务端日志轮询：日志页可见时每 3 秒刷新，离开自动停止
function startLogPoll() {
  stopLogPoll();
  loadSystemLogs();
  logPollTimer = setInterval(() => {
    const page = document.getElementById('page-logs');
    if (!page || !page.classList.contains('active')) { stopLogPoll(); return; }
    loadSystemLogs();
  }, 3000);
}
function stopLogPoll() {
  if (logPollTimer) { clearInterval(logPollTimer); logPollTimer = null; }
}
async function loadSystemLogs(manual) {
  const viewer = document.getElementById('server-log-viewer');
  if (!viewer) return;
  // 手动点击时给出明确反馈；自动轮询静默刷新（避免每 3 秒闪烁）
  if (manual === true && viewer.dataset.refreshing !== '1') {
    viewer.dataset.refreshing = '1';
    viewer.textContent = '正在刷新日志…';
    await new Promise(r => setTimeout(r, 150));
  } else if (viewer.textContent === '暂无日志...') {
    viewer.textContent = '加载中...';
  }
  try {
    const data = await api('/system/logs');
    const lines = String(data.logs || '暂无日志').split('\n').filter(l => l.trim() !== '');
    viewer.textContent = lines.length ? lines.reverse().join('\n') : '暂无日志';
  } catch (e) {
    console.error('[日志] 加载失败:', e);
    viewer.textContent = '加载失败: ' + (e.message || '未知错误');
  } finally {
    delete viewer.dataset.refreshing;
  }
}

// 清空日志：截断 app.log（写入器为追加模式，截断后新日志继续写入同一文件）
async function clearSystemLogs(btn) {
  if (!confirm('确定清空任务日志？清空后不可恢复（新日志会继续正常写入）。')) return;
  btn.disabled = true; btn.textContent = '清空中…';
  try {
    await api('/system/logs/clear', { method: 'POST' });
    toast('日志已清空');
    loadSystemLogs();
  } catch (e) { toast(e.message); } finally {
    btn.disabled = false; btn.textContent = '清空日志';
  }
}

// ==================== 初始化 ====================
window.addEventListener('DOMContentLoaded', () => {
  document.getElementById('auth-form').addEventListener('submit', handleAuth);
  // auth-switch 已移除
  document.querySelectorAll('.menu-item').forEach(item => {
    item.addEventListener('click', () => { if (item.dataset.page) showPage(item.dataset.page); });
  });
  // Tab 切换（通用）。子 tab（data-subtab，如基础配置的 115）
  // 有自己的 orgSubTab 内联处理，跳过通用绑定——否则会以 undefined tabName
  // 再跑一遍 switchTab，把所有面板的 active 全摘掉（内容整体空白）
  document.querySelectorAll('.page .tab').forEach(tab => {
    if (tab.dataset.subtab) return;
    tab.addEventListener('click', () => switchTab(tab.closest('.page').id, tab.dataset.tab));
  });
  document.getElementById('btn-log').addEventListener('click', openLog);
  const btnLogMobile = document.getElementById('btn-log-mobile');
  if (btnLogMobile) btnLogMobile.addEventListener('click', openLog);
  // 输入框自适应高度
  document.querySelectorAll('textarea.auto-resize').forEach(ta => {
    ta.addEventListener('input', () => autoResizeTextarea(ta));
    autoResizeTextarea(ta);
  });
  updateRenameExample();
  attachCIDResolvers();
  attachYamlHighlight('category-yaml');
  attachYamlHighlight('wash-yaml');
  checkAuth();
});

// 登录状态徽标（观影/123 统一，原影巢段迁入）：on=绿 off=灰 warn=橙
function renderLoginBadge(el, state, sub) {
  if (!el) return;
  el.style.color = '';
  el.style.fontSize = '';
  const cls = state === 'on' ? 'on' : (state === 'warn' ? 'warn' : 'off');
  const text = state === 'on' ? '已登录' : (state === 'warn' ? '即将过期' : '未登录');
  el.innerHTML = '<span class="login-badge ' + cls + '"><span class="dot"></span>' + text
    + (sub ? ' <span class="sub">' + esc(sub) + '</span>' : '') + '</span>';
}

// ==================== 影视转存 · 不太灵影视（bt0 系影视库） ====================
// TMDB 选片 → 不太灵影视站内搜索 → 资源列表（VIP token）。搜索匿名可用；
// 资源需在配置里粘贴 token 或用验证码登录。链接按协议分流：
// 115 分享自动转存 / 磁力 ed2k 离线下载 / 其他网盘打开原链。

let mukakuVideos = [];
let mukakuRes = [];
let mukakuCurTitle = '';

function mukakuModalOpen(html, title) {
  document.getElementById('mk-modal-title').textContent = title || '选择影视';
  document.getElementById('mk-modal-body').innerHTML = html;
  document.getElementById('mk-modal').style.display = 'flex';
}
function mukakuModalClose() {
  document.getElementById('mk-modal').style.display = 'none';
}

async function mukakuLoadPage() {
  try {
    const d = await api('/mukaku/config');
    document.getElementById('mk-base').value = d.base_url || '';
    document.getElementById('mk-username').value = d.username || '';
    const st = document.getElementById('mk-token-status');
    const tk = document.getElementById('mk-token');
    if (d.has_token) {
      tk.placeholder = '已保存（粘贴新值可覆盖）';
      st.textContent = '✓ token ' + (d.token_at || '');
      st.style.color = 'var(--success)';
    } else {
      tk.placeholder = '粘贴浏览器 localStorage 里的 token';
      st.textContent = '未配置（资源需 VIP）';
    }
  } catch (e) { console.error('[不太灵影视] 配置回填失败:', e.message); }
}

async function mukakuSaveBase(btn) {
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '保存中...'; }
  try {
    await api('/mukaku/config', {
      method: 'POST',
      body: JSON.stringify({ base_url: document.getElementById('mk-base').value.trim() }),
    });
    toast('保存成功');
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

async function mukakuClearToken(btn) {
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '...'; }
  try {
    await api('/mukaku/config', { method: 'POST', body: JSON.stringify({ access_token: '' }) });
    toast('已清除 token');
    await mukakuLoadPage();
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

async function mukakuRefreshCaptcha() {
  const img = document.getElementById('mk-captcha-img');
  try {
    const d = await api('/mukaku/captcha');
    img.src = d.img;
    img.dataset.key = d.key || '';
    img.style.display = '';
  } catch (e) { toast(e.message); }
}

async function mukakuLogin(btn) {
  const username = document.getElementById('mk-username').value.trim();
  const password = document.getElementById('mk-password').value;
  const code = document.getElementById('mk-code').value.trim();
  if (!username || !password) { toast('请填写账号和密码'); return; }
  const img = document.getElementById('mk-captcha-img');
  const key = img ? (img.dataset.key || '') : '';
  if (!key || !code) { toast('请先获取并输入图形验证码'); return; }
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '登录中…'; }
  try {
    await api('/mukaku/login', {
      method: 'POST',
      body: JSON.stringify({ username: username, password: password, code: code, key: key }),
    });
    toast('登录成功，token 已保存');
    await mukakuLoadPage();
  } catch (e) {
    toast(e.message);
    mukakuRefreshCaptcha();
  }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

// 站内影片搜索（结果即弹窗列表，点击进资源）
async function mukakuSearchSite(kw) {
  const body = document.getElementById('mk-modal-body');
  if (!kw) { toast('请输入搜索关键词'); return; }
  mukakuCurTitle = kw;
  body.innerHTML = '<span style="color:var(--text-3)">不太灵影视搜索「' + esc(kw) + '」中…</span>';
  document.getElementById('mk-modal-title').textContent = '不太灵影视 · ' + kw;
  mukakuVideos = [];
  try {
    const d = await api('/mukaku/search?kw=' + encodeURIComponent(kw));
    mukakuVideos = d.data || [];
    if (!mukakuVideos.length) {
      body.innerHTML = '<span style="color:var(--text-3)">不太灵影视没有找到「' + esc(kw) + '」相关影片</span>'
        + '<br><a href="javascript:void(0)" style="font-size:13px" onclick="mukakuSearch()">← 重新选片</a>';
      return;
    }
    mukakuRenderVideos();
  } catch (e) {
    body.innerHTML = '<span style="color:var(--danger)">' + esc(e.message) + '</span>'
      + '<br><a href="javascript:void(0)" style="font-size:13px" onclick="mukakuSearch()">← 重新选片</a>';
  }
}

function mukakuRenderVideos() {
  const body = document.getElementById('mk-modal-body');
  const items = mukakuVideos;
  const cards = items.map((it, i) => {
    const poster = it.image
      ? '<img src="' + esc(it.image) + '" onerror="this.style.display=\'none\'" style="width:52px;height:78px;object-fit:cover;border-radius:6px;background:var(--fill-2);flex:none">'
      : '<div style="width:52px;height:78px;border-radius:6px;background:var(--fill-2);flex:none"></div>';
    const meta = [it.year, it.quality, it.doub && it.doub !== '0' ? '豆 ' + it.doub : '', it.imdb && it.imdb !== '0' ? 'IMDb ' + it.imdb : '']
      .filter(Boolean).join(' · ');
    return '<div onclick="mukakuOpenResources(' + i + ')" '
      + 'style="display:flex;gap:12px;padding:10px;border-radius:8px;cursor:pointer;border:1px solid var(--border, #e5e6eb)" '
      + 'onmouseover="this.style.background=\'var(--fill-1,#f7f8fa)\'" onmouseout="this.style.background=\'\'">'
      + poster
      + '<div style="min-width:0;flex:1">'
      + '<b style="font-size:14px">' + esc(it.title) + '</b> '
      + '<span style="font-size:12px;color:var(--text-3)">' + esc(it.otitle || '') + '</span>'
      + '<div style="font-size:12.5px;color:var(--text-2);margin-top:5px">' + esc(meta) + '</div>'
      + '</div><div class="otk-side" style="color:var(--primary);align-self:center">看资源 ›</div>'
      + '</div>';
  }).join('');
  mukakuModalOpen('<div style="display:flex;flex-direction:column;gap:10px">' + cards + '</div>',
    '不太灵影视 · ' + mukakuCurTitle + '（' + items.length + ' 个影片）');
}

// 拉取影片资源列表（需 VIP token）
async function mukakuOpenResources(i) {
  const v = mukakuVideos[i];
  if (!v) return;
  mukakuCurTitle = v.title;
  const body = document.getElementById('mk-modal-body');
  body.innerHTML = '<span style="color:var(--text-3)">读取「' + esc(v.title) + '」资源中…</span>';
  document.getElementById('mk-modal-title').textContent = '不太灵影视 · ' + v.title;
  try {
    const d = await api('/mukaku/resources?id=' + v.id);
    mukakuRes = d.data || [];
    if (!mukakuRes.length) {
      body.innerHTML = '<span style="color:var(--text-3)">没有读到资源：站方仅对 VIP 开放资源列表。'
        + '请确认已粘贴有效 token 或验证码登录成功。</span>'
        + '<br><a href="javascript:void(0)" style="font-size:13px" onclick="mukakuSearchSite(mukakuCurTitle)">← 返回影片列表</a>';
      return;
    }
    mukakuRenderResources();
  } catch (e) {
    body.innerHTML = '<span style="color:var(--danger)">' + esc(e.message) + '</span>'
      + '<br><a href="javascript:void(0)" style="font-size:13px" onclick="mukakuSearchSite(mukakuCurTitle)">← 返回影片列表</a>';
  }
}

function mukakuRenderResources() {
  const body = document.getElementById('mk-modal-body');
  const rows = mukakuRes.map((it, i) => {
    let side, click;
    if (it.action === 'transfer') {
      side = '<div class="otk-side" style="color:var(--primary)">转存 ›</div>';
      click = 'onclick="mukakuTransfer(' + i + ',this)"';
    } else if (it.action === 'offline') {
      side = '<div class="otk-side" style="color:var(--primary)">离线下载 ›</div>';
      click = 'onclick="mukakuOffline(' + i + ',this)"';
    } else {
      side = '<div class="otk-side" style="color:var(--text-3)">打开链接 ↗</div>';
      click = 'onclick="mukakuOpenLink(' + i + ')"'; // 下标回调防注入（esc 在事件属性上下文无效）
    }
    const meta = [it.code ? '<span style="color:#b26a00">提取码 ' + esc(it.code) + '</span>' : '']
      .filter(Boolean).join('<span class="otk-dot">·</span>');
    return '<div class="otk-row" style="cursor:pointer" ' + click + '>'
    + '<div class="otk-main"><div class="otk-name" title="' + esc(it.link) + '">' + esc(it.seed_name || it.link) + '</div>'
    + (meta ? '<div class="otk-sub">' + meta + '</div>' : '')
    + '<div class="gy-st" style="font-size:12px;margin-top:4px;color:var(--text-3)"></div></div>'
    + side
    + '</div>';
  }).join('');
  mukakuModalOpen('<div style="margin-bottom:10px;font-size:12px;color:var(--text-3)">共 '
    + mukakuRes.length + ' 条资源 · 115 分享点击转存，磁力/ed2k 点击离线下载</div>'
    + '<div class="otk">' + rows + '</div>'
    + '<div style="margin-top:12px"><a href="javascript:void(0)" style="font-size:13px;color:var(--primary)" onclick="mukakuSearchSite(mukakuCurTitle)">← 返回影片列表</a></div>',
    '不太灵影视 · ' + mukakuCurTitle);
}

function mukakuOpenLink(i) {
  const it = mukakuRes[i];
  if (it && it.link) window.open(it.link, '_blank');
}

async function mukakuTransfer(i, el) {
  const it = mukakuRes[i];
  if (!it) return;
  const st = el.querySelector('.gy-st');
  if (st && st.dataset.done === '1') { toast('该资源已转存过'); return; }
  if (st) { st.style.color = 'var(--text-3)'; st.textContent = '转存中…'; }
  try {
    const r = await api('/share/receive', {
      method: 'POST',
      body: JSON.stringify({ url: it.link, code: it.code || '', target_cid: '', organize: true }),
    });
    if (st) { st.style.color = 'var(--success)'; st.dataset.done = '1'; st.textContent = '✓ ' + (r.message || '已转存'); }
    toast('已转存 115，完成后自动整理入库');
  } catch (e) {
    if (st) { st.style.color = 'var(--danger)'; st.textContent = '✗ ' + e.message; }
  }
}

async function mukakuOffline(i, el) {
  const it = mukakuRes[i];
  if (!it) return;
  const st = el.querySelector('.gy-st');
  if (st && st.dataset.done === '1') { toast('该资源已提交过'); return; }
  if (st) { st.style.color = 'var(--text-3)'; st.textContent = '提交 115 离线下载中…'; }
  try {
    await api('/offline/add', {
      method: 'POST',
      body: JSON.stringify({ url: it.link, organize: true }),
    });
    if (st) { st.style.color = 'var(--success)'; st.dataset.done = '1'; st.textContent = '✓ 已提交离线下载'; }
    toast('已提交 115 离线下载，完成后自动整理入库');
  } catch (e) {
    if (st) { st.style.color = 'var(--danger)'; st.textContent = '✗ ' + e.message; }
  }
}

// ==================== 影视刮削（原生 NFO + 海报） ====================
// TMDB → 本地媒体库标准元数据（movie.nfo/tvshow.nfo + poster/fanart/季海报），
// 落盘后由「监控上传」自动回传 115。

async function scrapeLoadPage() {
  try {
    const d = await api('/scrape/config');
    const cfg = d.cfg || {};
    document.getElementById('scrape-root').value = cfg.local_root || '';
    scrapeOpts = {
      write_nfo: cfg.write_nfo !== false,
      write_images: cfg.write_images !== false,
      force: !!cfg.force,
      auto_after_organize: !!cfg.auto_after_organize,
    };
    Object.entries(scrapeOpts).forEach(([k, v]) => setScrapeOpt(k, v));
  } catch (e) { console.error('[刮削] 配置回填失败:', e.message); }
}

let scrapeOpts = { write_nfo: true, write_images: true, force: false, auto_after_organize: false };

function setScrapeOpt(key, v) {
  scrapeOpts[key] = v;
  const map = { write_nfo: 'scrape-nfo-switch', write_images: 'scrape-img-switch', force: 'scrape-force-switch', auto_after_organize: 'scrape-auto-switch' };
  document.querySelectorAll('#' + map[key] + ' .seg-item').forEach(el => {
    el.classList.toggle('active', el.dataset.value === String(v));
  });
}

function scrapeCfgFromUI() {
  return Object.assign({ local_root: document.getElementById('scrape-root').value.trim() }, scrapeOpts);
}

async function scrapeSaveConfig(btn) {
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '保存中...'; }
  try {
    await api('/scrape/config', { method: 'POST', body: JSON.stringify(scrapeCfgFromUI()) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

async function scrapeRun(btn) {
  const cfg = scrapeCfgFromUI();
  if (!cfg.local_root) { toast('请先填写本地媒体库根目录'); return; }
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '启动中…'; }
  try {
    await api('/scrape/config', { method: 'POST', body: JSON.stringify(cfg) });
    await api('/scrape/run', { method: 'POST' });
    toast('刮削已开始，进度与结果见运行日志');
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

async function scrapeStop() {
  try { await api('/scrape/stop', { method: 'POST' }); toast('已请求停止'); }
  catch (e) { toast(e.message); }
}

// ==================== 影视转存 · 盘搜（PanSou 聚合） ====================
// 开源项目 PanSou 实例聚合 TG 频道/插件的网盘分享。115 分享行点击转存、
// 磁力/ed2k 行点击离线下载、其他网盘行打开原链接手动转存。

let pansouItems = [];
let pansouFilter = '';

async function pansouLoadPage() {
  try {
    const d = await api('/pansou/config');
    document.getElementById('pansou-base').value = d.base_url || 'https://pansou.app';
  } catch (e) { console.error('[盘搜] 配置回填失败:', e.message); }
}

async function pansouSaveConfig(btn) {
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '保存中...'; }
  try {
    const d = await api('/pansou/config', {
      method: 'POST',
      body: JSON.stringify({ base_url: document.getElementById('pansou-base').value.trim() }),
    });
    document.getElementById('pansou-base').value = d.base_url || '';
    toast('保存成功');
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

async function pansouResetConfig(btn) {
  document.getElementById('pansou-base').value = 'https://pansou.app';
  await pansouSaveConfig(btn);
}

function pansouModalOpen(html, title) {
  document.getElementById('pansou-modal-title').textContent = title || '选择影视';
  document.getElementById('pansou-modal-body').innerHTML = html;
  document.getElementById('pansou-modal').style.display = 'flex';
}
function pansouModalClose() {
  document.getElementById('pansou-modal').style.display = 'none';
}

async function pansouSearchSite(kw) {
  const body = document.getElementById('pansou-modal-body');
  if (!kw) { toast('请输入搜索关键词'); return; }
  body.innerHTML = '<span style="color:var(--text-3)">盘搜聚合搜索「' + esc(kw) + '」（多源并发，约需数秒）…</span>';
  document.getElementById('pansou-modal-title').textContent = '盘搜 · ' + kw;
  pansouItems = [];
  pansouFilter = '';
  try {
    const d = await api('/pansou/search?kw=' + encodeURIComponent(kw));
    pansouItems = d.data || [];
    if (!pansouItems.length) {
      body.innerHTML = '<span style="color:var(--text-3)">没有搜索到「' + esc(kw) + '」的网盘分享</span>'
        + '<br><a href="javascript:void(0)" style="font-size:13px" onclick="pansouSearch()">← 重新选片</a>';
      return;
    }
    pansouRenderList();
  } catch (e) {
    body.innerHTML = '<span style="color:var(--danger)">' + esc(e.message) + '</span>'
      + '<br><a href="javascript:void(0)" style="font-size:13px" onclick="pansouSearch()">← 重新选片</a>';
  }
}

const PANSOU_TYPE_LABEL = { '115': '115 网盘', baidu: '百度网盘', uc: 'UC 网盘', xunlei: '迅雷云盘' };

function pansouSetFilter(key) {
  pansouFilter = key;
  pansouRenderList();
}

function pansouRenderList() {
  const box = document.getElementById('pansou-modal-body');
  if (!box) return;
  // 类型计数（按服务端排序顺序）
  const counts = {};
  for (const it of pansouItems) counts[it.cloud_type] = (counts[it.cloud_type] || 0) + 1;
  const types = [...new Set(pansouItems.map(it => it.cloud_type))];
  const pill = (label, key, count) => {
    const active = pansouFilter === key;
    const bg = active ? 'var(--primary)' : 'var(--fill-2)';
    const fg = active ? '#fff' : 'var(--text-2)';
    return '<span onclick="pansouSetFilter(\'' + key + '\')" '
      + 'style="cursor:pointer;padding:3px 12px;border-radius:999px;background:' + bg + ';color:' + fg + ';font-size:12.5px;line-height:20px">'
      + label + ' ' + count + '</span>';
  };
  let html = '<div style="display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin:0 0 10px;font-size:12px;color:var(--text-3)">类型'
    + pill('全部', '', pansouItems.length)
    + types.map(t => pill(PANSOU_TYPE_LABEL[t] || t, t, counts[t])).join('')
    + '</div>';

  const items = pansouFilter ? pansouItems.filter(it => it.cloud_type === pansouFilter) : pansouItems;
  html += '<div class="otk">' + items.map((it, pi) => {
    const i = pansouItems.indexOf(it); // pi 是过滤副本下标，动作行需原数组下标
    const label = PANSOU_TYPE_LABEL[it.cloud_type] || it.cloud_type;
    const typeTag = '<span class="otag" style="background:var(--fill-2);color:var(--text-2)">' + esc(label) + '</span>';
    const pass = it.password ? '<span style="color:#b26a00">提取码 ' + esc(it.password) + '</span>' : '';
    const meta = [pass, (it.datetime || '').replace('T', ' ').slice(0, 16)].filter(Boolean)
      .join('<span class="otk-dot">·</span>');
    let side, click;
    if (it.action === 'transfer') {
      side = '<div class="otk-side" style="color:var(--primary)">转存 ›</div>';
      click = 'onclick="pansouTransfer(' + i + ',this)"';
    } else if (it.action === 'offline') {
      side = '<div class="otk-side" style="color:var(--primary)">离线下载 ›</div>';
      click = 'onclick="pansouOffline(' + i + ',this)"';
    } else {
      side = '<div class="otk-side" style="color:var(--text-3)">打开链接 ↗</div>';
      click = 'onclick="pansouOpenLink(' + i + ')"'; // 下标回调防注入（esc 在事件属性上下文无效）
    }
    return '<div class="otk-row" style="cursor:pointer" ' + click + '>'
    + typeTag
    + '<div class="otk-main"><div class="otk-name" title="' + esc(it.url) + '">' + esc(it.note || it.url) + '</div>'
    + '<div class="otk-sub">' + meta + '</div>'
    + '<div class="gy-st" style="font-size:12px;margin-top:4px;color:var(--text-3)"></div></div>'
    + side
    + '</div>';
  }).join('') + '</div>';
  box.innerHTML = html;
}

// 115 分享行：转存到目标目录（提取码可能为空 = 无密码分享）
function pansouOpenLink(i) {
  const it = pansouItems[i];
  if (it && it.url) window.open(it.url, '_blank');
}

async function pansouTransfer(i, el) {
  const it = pansouItems[i];
  if (!it) return;
  const st = el.querySelector('.gy-st');
  if (st && st.dataset.done === '1') { toast('该资源已转存过'); return; }
  if (st) { st.style.color = 'var(--text-3)'; st.textContent = '转存中…'; }
  try {
    const r = await api('/share/receive', {
      method: 'POST',
      body: JSON.stringify({ url: it.url, code: it.password || '', target_cid: '', organize: true }),
    });
    if (st) { st.style.color = 'var(--success)'; st.dataset.done = '1'; st.textContent = '✓ ' + (r.message || '已转存'); }
    toast('已转存 115，完成后自动整理入库');
  } catch (e) {
    if (st) { st.style.color = 'var(--danger)'; st.textContent = '✗ ' + e.message; }
  }
}

// 磁力/ed2k 行：提交 115 离线下载
async function pansouOffline(i, el) {
  const it = pansouItems[i];
  if (!it) return;
  const st = el.querySelector('.gy-st');
  if (st && st.dataset.done === '1') { toast('该资源已提交过'); return; }
  if (st) { st.style.color = 'var(--text-3)'; st.textContent = '提交 115 离线下载中…'; }
  try {
    await api('/offline/add', {
      method: 'POST',
      body: JSON.stringify({ url: it.url, organize: true }),
    });
    if (st) { st.style.color = 'var(--success)'; st.dataset.done = '1'; st.textContent = '✓ 已提交离线下载'; }
    toast('已提交 115 离线下载，完成后自动整理入库');
  } catch (e) {
    if (st) { st.style.color = 'var(--danger)'; st.textContent = '✗ ' + e.message; }
  }
}

// ==================== 整理·基础配置 子tab（115） ====================
function orgSubTab(name) {
  const page = document.getElementById('page-organize');
  if (!page) return;
  page.querySelectorAll('[data-subtab]').forEach(t => t.classList.toggle('active', t.dataset.subtab === name));
  page.querySelectorAll('[data-subpanel]').forEach(pn => pn.classList.toggle('active', pn.dataset.subpanel === name));
}

// ==================== TMDB 选片弹窗（观影/盘搜/不太灵影视共用） ====================
// 关键词 → /tmdb/search → 海报选片卡片弹窗；onPick(标题) 回调、onSkip 兜底。
// 行内 onclick 只带数组下标，标题转义问题天然规避。

let _tmdbPickItems = [];
let _tmdbPickCb = null;
let _tmdbPickSkip = null;

function tmdbPickCardClick(i) {
  const it = _tmdbPickItems[i];
  if (it && _tmdbPickCb) _tmdbPickCb(it.title, it);
}

function tmdbPickSkipClick() {
  if (_tmdbPickSkip) _tmdbPickSkip();
}

async function tmdbPickFlow(query, opts) {
  // opts: { openModal(html,title), onPick(title), onSkip?, skipLabel? }
  const open = opts.openModal;
  _tmdbPickCb = opts.onPick;
  _tmdbPickSkip = opts.onSkip || null;
  open('<span style="color:var(--text-3)">TMDB 匹配中…</span>', '选择影视');
  const skipLink = _tmdbPickSkip
    ? '<br><a href="javascript:void(0)" style="font-size:13px" onclick="tmdbPickSkipClick()">' + esc(opts.skipLabel || '跳过 TMDB，直接用关键词搜索') + '</a>'
    : '';
  try {
    const d = await api('/tmdb/search?query=' + encodeURIComponent(query));
    const items = d.data || [];
    if (!items.length) {
      open('<span style="color:var(--text-3)">' + esc(d.hint || '未找到匹配的影视条目') + '</span>' + skipLink, '选择影视');
      return;
    }
    _tmdbPickItems = items;
    const cards = items.map((it, i) => {
      const poster = it.poster
        ? '<img src="/api/tmdb/img?path=' + encodeURIComponent(it.poster) + '&size=w154" '
          + 'onerror="this.style.display=\'none\'" style="width:60px;height:90px;object-fit:cover;border-radius:6px;background:var(--fill-2);flex:none">'
        : '<div style="width:60px;height:90px;border-radius:6px;background:var(--fill-2);display:flex;align-items:center;justify-content:center;font-size:22px;color:var(--text-3);flex:none">▨</div>';
      const typeTag = it.media_type === 'tv'
        ? '<span class="otag" style="background:#eef0ff;color:#5b5fc7">剧集</span>'
        : '<span class="otag" style="background:#fff4e5;color:#b26a00">电影</span>';
      const overview = String(it.overview || '').slice(0, 100);
      return '<div onclick="tmdbPickCardClick(' + i + ')" '
        + 'style="display:flex;gap:12px;padding:10px;border-radius:8px;cursor:pointer;border:1px solid var(--border, #e5e6eb)" '
        + 'onmouseover="this.style.background=\'var(--fill-1,#f7f8fa)\'" onmouseout="this.style.background=\'\'">'
        + poster
        + '<div style="min-width:0;flex:1">'
        + '<div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap">' + typeTag
        + '<b style="font-size:14px">' + esc(it.title) + '</b>'
        + '<span style="font-size:12px;color:var(--text-3)">' + esc(it.year || '') + '</span>'
        + (it.vote ? '<span style="font-size:12px;color:#e6a23c">★ ' + it.vote.toFixed(1) + '</span>' : '')
        + '</div>'
        + (overview ? '<div style="font-size:12.5px;color:var(--text-2);line-height:1.6;margin-top:5px;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden">' + esc(overview) + '…</div>' : '')
        + '</div></div>';
    }).join('');
    open('<div style="display:flex;flex-direction:column;gap:10px">' + cards + '</div>', '选择影视（' + items.length + ' 个结果）');
  } catch (e) {
    open('<span style="color:var(--danger)">' + esc(e.message) + '</span>' + skipLink, '选择影视');
  }
}

// —— 三个渠道的搜索入口（共用 tmdbPickFlow）——
async function gySearch() {
  const q = document.getElementById('gy-query').value.trim();
  if (!q) { toast('请输入影视名称或 TMDB ID'); return; }
  tmdbPickFlow(q, {
    openModal: gyModalOpen,
    onPick: title => gySearchSite(title, ''),
    onSkip: () => gySearchSite(document.getElementById('gy-query').value.trim(), ''),
    skipLabel: '跳过 TMDB，直接用关键词搜观影',
  });
}

async function pansouSearch() {
  const q = document.getElementById('pansou-query').value.trim();
  if (!q) { toast('请输入影视名称或 TMDB ID'); return; }
  tmdbPickFlow(q, {
    openModal: pansouModalOpen,
    onPick: title => pansouSearchSite(title),
    onSkip: () => pansouSearchSite(document.getElementById('pansou-query').value.trim()),
    skipLabel: '跳过 TMDB，直接用关键词搜盘搜',
  });
}

async function mukakuSearch() {
  const q = document.getElementById('mk-query').value.trim();
  if (!q) { toast('请输入影视名称或 TMDB ID'); return; }
  tmdbPickFlow(q, {
    openModal: mukakuModalOpen,
    onPick: title => mukakuSearchSite(title),
    onSkip: () => mukakuSearchSite(document.getElementById('mk-query').value.trim()),
    skipLabel: '跳过 TMDB，直接用关键词搜不太灵影视',
  });
}

// ==================== 影视转存 · 观影 ====================
// 站点为 CSR + PoW 反爬 + 站内登录：后端自动过反爬验证，账号密码登录后
// 会话 Cookie 持久化（失效自动重登）。种子搜索 → 磁力提交 115 离线下载，
// 完成后自动整理入库。

// ==================== 影视转存 · RE0 ====================
// RE0（影视资料与分享社区）官方 OpenAPI：应用 Secret + OAuth 用户授权。
// 片名 → TMDB 候选 → 站内资源列表（网盘类型/分辨率/解锁积分）→ 解锁拿
// 115 分享链接自动转存到接收目录（完成后自动整理入库）。

let re0Items = [];

async function re0LoadPage() {
  try {
    const d = await api('/re0/config');
    document.getElementById('re0-base').value = d.base_url || 'https://re0.me';
    document.getElementById('re0-client-id').value = d.client_id || '';
    document.getElementById('re0-secret').value = d.client_secret || '';
    re0RenderStatus(d);
  } catch (e) { console.error('[RE0] 配置回填失败:', e.message); }
}

function re0RenderStatus(d) {
  const el = document.getElementById('re0-status');
  if (!el) return;
  if (d.authorized) {
    el.innerHTML = '<span style="color:var(--ok)">✓ 已授权' + (d.authorized_as ? '（' + esc(d.authorized_as) + '）' : '') + '</span>';
  } else if (d.client_id && d.client_secret) {
    el.innerHTML = '<span style="color:#b26a00">应用已配置，尚未授权</span>';
  } else {
    el.textContent = '未配置';
  }
}

async function re0SaveConfig(btn) {
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '保存中...'; }
  try {
    await api('/re0/config', {
      method: 'POST',
      body: JSON.stringify({
        base_url: document.getElementById('re0-base').value.trim(),
        client_id: document.getElementById('re0-client-id').value.trim(),
        client_secret: document.getElementById('re0-secret').value.trim(),
      }),
    });
    toast('保存成功');
    const d = await api('/re0/config');
    document.getElementById('re0-secret').value = d.client_secret || '';
    re0RenderStatus(d);
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

// 跳转 RE0 官方授权页；回调地址 = 当前 StrmHub 地址 + /api/re0/oauth/callback
async function re0Authorize(btn) {
  try {
    const redirectUri = location.origin + '/api/re0/oauth/callback';
    const d = await api('/re0/oauth/start?redirect_uri=' + encodeURIComponent(redirectUri));
    window.open(d.authorize_url, '_blank');
    toast('请在 RE0 授权页确认，完成后回到这里点「检查状态」');
  } catch (e) { toast(e.message); }
}

async function re0Check(btn) {
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '检查中...'; }
  try {
    const d = await api('/re0/check');
    const el = document.getElementById('re0-status');
    if (el) {
      if (d.authorized) {
        el.innerHTML = '<span style="color:var(--ok)">✓ 应用' + esc(d.app_name || '') + '正常 · 已授权' + (d.user ? '（' + esc(d.user) + '）' : '') + '</span>';
      } else {
        el.innerHTML = '<span style="color:var(--ok)">✓ 应用' + esc(d.app_name || '') + '正常</span> <span style="color:#b26a00">尚未授权用户</span>';
      }
    }
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

function re0ModalOpen(html, title) {
  document.getElementById('re0-modal-title').textContent = title || 'RE0 资源';
  document.getElementById('re0-modal-body').innerHTML = html;
  document.getElementById('re0-modal').style.display = 'flex';
}
function re0ModalClose() {
  document.getElementById('re0-modal').style.display = 'none';
}

async function re0Search() {
  const q = document.getElementById('re0-query').value.trim();
  if (!q) { toast('请输入影视名称或 TMDB ID'); return; }
  re0ModalOpen('<span style="color:var(--text-3)">TMDB 匹配「' + esc(q) + '」并查询 RE0 站内资源…</span>', 'RE0 · ' + q);
  try {
    const d = await api('/re0/search?query=' + encodeURIComponent(q));
    re0Items = d.data || [];
    if (!re0Items.length) {
      re0ModalOpen('<span style="color:var(--text-3)">TMDB 未匹配到影视条目</span>', 'RE0 · ' + q);
      return;
    }
    re0RenderResults(d.hint || '');
  } catch (e) {
    re0ModalOpen('<span style="color:var(--danger)">' + esc(e.message) + '</span>', 'RE0 · ' + q);
  }
}

function re0RenderResults(hint) {
  const mediaLabel = { movie: '电影', tv: '剧集' };
  let html = hint ? '<div style="color:var(--danger);margin-bottom:10px">' + esc(hint) + '</div>' : '';
  for (let ci = 0; ci < re0Items.length; ci++) {
    const it = re0Items[ci];
    const res = it.resources || [];
    html += '<div style="margin:0 0 14px;padding-bottom:12px;border-bottom:1px solid var(--fill-2)">'
      + '<div style="display:flex;gap:8px;align-items:center;margin-bottom:6px">'
      + '<span class="otag" style="background:var(--fill-2);color:var(--text-2)">' + esc(mediaLabel[it.media_type] || it.media_type) + '</span>'
      + '<strong>' + esc(it.title) + '</strong>'
      + (it.year ? '<span style="color:var(--text-3)">' + esc(it.year) + '</span>' : '')
      + (it.vote ? '<span style="color:var(--text-3)">★' + esc(String(it.vote)) + '</span>' : '')
      + '</div>';
    if (it.resources_err) {
      html += '<div style="color:var(--danger);font-size:12.5px">' + esc(it.resources_err) + '</div>';
    } else if (!res.length) {
      html += '<div style="color:var(--text-3);font-size:12.5px">站内暂无资源</div>';
    } else {
      html += '<div class="otk">' + res.map((r, ri) => {
        const idx = re0Items[ci].resources.indexOf(r);
        const pan = r.pan_type ? '<span class="otag" style="background:var(--fill-2);color:var(--text-2)">' + esc(r.pan_type) + '</span>' : '';
        const spec = (r.video_resolution || []).join(' / ');
        const points = r.is_unlocked
          ? '<span style="color:var(--ok)">已解锁</span>'
          : (r.unlock_points != null ? '<span style="color:#b26a00">' + r.unlock_points + ' 积分</span>' : '<span style="color:var(--text-3)">积分未知</span>');
        const btn = '<button class="btn btn-primary" style="padding:3px 12px;font-size:12.5px" '
          + 'onclick="re0Unlock(' + ci + ',' + ri + ',this)">' + (r.is_unlocked ? '获取链接' : '解锁并转存') + '</button>';
        return '<div style="display:flex;gap:8px;align-items:center;padding:5px 0;flex-wrap:wrap">'
          + pan
          + '<span>' + esc(r.title || r.slug) + '</span>'
          + (spec ? '<span style="color:var(--text-3);font-size:12px">' + esc(spec) + '</span>' : '')
          + (r.share_size ? '<span style="color:var(--text-3);font-size:12px">' + esc(r.share_size) + '</span>' : '')
          + points
          + '<span style="margin-left:auto">' + btn + '</span>'
          + '</div>';
      }).join('') + '</div>';
    }
    html += '</div>';
  }
  re0ModalOpen(html, 'RE0 资源');
}

async function re0Unlock(ci, ri, btn) {
  const it = re0Items[ci];
  const r = it.resources[ri];
  const orig = btn.textContent;
  btn.disabled = true; btn.textContent = '处理中...';
  try {
    const is115 = (r.pan_type || '') === '115';
    const d = await api('/re0/unlock', {
      method: 'POST',
      body: JSON.stringify({ media_type: it.media_type, tmdb_id: it.id, slug: r.slug, transfer: is115 }),
    });
    if (d.transferred) {
      btn.outerHTML = '<span style="color:var(--ok)">✓ 已转存' + (d.transfer_msg ? '（' + esc(d.transfer_msg) + '）' : '') + '</span>';
      toast('解锁成功，115 分享已转存，完成后自动整理入库');
    } else if (d.url) {
      btn.outerHTML = '<span style="color:var(--ok)">✓ 已解锁</span>';
      window.open(d.url, '_blank');
      toast('解锁成功，已打开分享链接');
    } else {
      btn.disabled = false; btn.textContent = orig;
      toast('解锁成功但未返回链接');
    }
  } catch (e) {
    btn.disabled = false; btn.textContent = orig;
    toast(e.message);
  }
}

async function gyLoadPage() {
  try {
    const d = await api('/guanying/config');
    document.getElementById('gy-base').value = d.base_url || '';
    document.getElementById('gy-username').value = d.username || '';
    document.getElementById('gy-password').value = d.password || '';
    gyRenderAuth(d.logged_in);
    // 配置已即时回填；登录态异步校准（不阻塞输入框显示）
    api('/guanying/check').then(r => gyRenderAuth(r.logged_in)).catch(() => {});
  } catch (e) { console.error('[观影] 配置回填失败:', e.message); }
}

let gyLoggedIn = false;

function gyRenderAuth(loggedIn) {
  gyLoggedIn = loggedIn;
  const btn = document.getElementById('gy-auth-btn');
  const el = document.getElementById('gy-login-status');
  if (btn) {
    btn.textContent = loggedIn ? '退出登录' : '登录';
    btn.classList.toggle('btn-primary', !loggedIn);
    btn.classList.toggle('btn-warning', loggedIn);
  }
  if (el) {
    const user = (document.getElementById('gy-username') || {}).value || '';
    renderLoginBadge(el, loggedIn ? 'on' : 'off', loggedIn ? user : '');
  }
}

// 登录/退出共用一个按钮，按当前状态分发
function gyAuthClick(btn) {
  if (gyLoggedIn) {
    gyLogout(btn);
  } else {
    gyLogin(btn);
  }
}

async function gySaveConfig(btn) {
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '保存中...'; }
  try {
    await api('/guanying/config', {
      method: 'POST',
      body: JSON.stringify({
        base_url: document.getElementById('gy-base').value.trim(),
        username: document.getElementById('gy-username').value.trim(),
        password: document.getElementById('gy-password').value,
      }),
    });
    toast('保存成功');
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

async function gyResetConfig(btn) {
  if (!confirm('确定重置观影配置？站点地址将恢复默认，账号密码会被清空并退出登录。')) return;
  try {
    await api('/guanying/config', {
      method: 'POST',
      body: JSON.stringify({ base_url: '', username: '', password: '' }),
    });
    await api('/guanying/logout', { method: 'POST' });
    toast('已重置');
    gyLoadPage();
  } catch (e) { toast(e.message); }
}

async function gyLogin(btn) {
  const username = document.getElementById('gy-username').value.trim();
  const password = document.getElementById('gy-password').value;
  if (!username || !password) { toast('请填写观影账号和密码'); return; }
  btn.disabled = true;
  btn.textContent = '登录中';
  try {
    const d = await api('/guanying/login', {
      method: 'POST',
      body: JSON.stringify({ base_url: document.getElementById('gy-base').value.trim(), username, password }),
    });
    toast(d.message || '登录成功');
    gyRenderAuth(true);
  } catch (e) {
    toast(e.message);
    gyRenderAuth(false);
  } finally {
    btn.disabled = false;
  }
}

async function gyLogout(btn) {
  btn.disabled = true;
  btn.textContent = '退出中';
  try {
    await api('/guanying/logout', { method: 'POST' });
    toast('已退出登录');
    gyRenderAuth(false);
  } catch (e) {
    toast(e.message);
  } finally {
    btn.disabled = false;
  }
}

function gyModalOpen(html, title) {
  document.getElementById('gy-modal-title').textContent = title || '选择影视';
  document.getElementById('gy-modal-body').innerHTML = html;
  document.getElementById('gy-modal').style.display = 'flex';
}
function gyModalClose() {
  document.getElementById('gy-modal').style.display = 'none';
}

let gyCurTitle = '';  // 已选影片名
let gyCurZy = '';     // 当前资源分类（空=全部）
let gyLastZy = {};    // 最近一次的分类分组（切分类时高亮用）

// 搜索入口：TMDB 匹配条目，弹窗海报墙 + 简介让用户选择
// 选定影片 → 弹窗切到观影种子列表（按资源分类分组）
async function gySearchSite(title, zy) {
  gyCurTitle = title;
  gyCurZy = zy || '';
  const body = document.getElementById('gy-modal-body');
  body.innerHTML = '<div id="gy-zy-tabs"></div><div id="gy-torrent-list"><span style="color:var(--text-3)">搜索观影种子中…</span></div>';
  document.getElementById('gy-modal-title').textContent = '观影种子 · ' + title;
  try {
    const d = await api('/guanying/search?query=' + encodeURIComponent(title) + (zy ? '&zy=' + encodeURIComponent(zy) : ''));
    const items = d.data || [];
    // 分类 tabs（「全部」查询时服务端返回分组；切分类沿用缓存的分组高亮当前项）
    if (d.zy && Object.keys(d.zy).length) gyLastZy = d.zy;
    const tabEl = document.getElementById('gy-zy-tabs');
    if (tabEl) tabEl.outerHTML = Object.keys(gyLastZy).length ? buildGyTabs(gyLastZy) : '';
    const list = document.getElementById('gy-torrent-list');
    if (!items.length) {
      let hint = '观影站内没有找到「' + esc(title) + '」的种子';
      const dbg = d.debug || {};
      if (dbg.page_len !== undefined) {
        hint += '<br><span style="font-size:12px;color:var(--text-3)">诊断: 页面 ' + dbg.page_len
          + ' 字节 · 标题「' + esc(dbg.title || '') + '」· ' + (dbg.nologin ? '受限页' : '非受限页') + '</span>';
      }
      list.innerHTML = '<span style="color:var(--text-3)">' + hint + '</span>';
      return;
    }
    gyLastItems = items;
    gySort = { key: '', dir: -1 };
    gyRenderTorrentList();
  } catch (e) {
    document.getElementById('gy-torrent-list').innerHTML = '<span style="color:var(--danger)">' + esc(e.message) + '</span>';
  }
}

// ===== 种子列表：点击表头排序（大小/做种/时间）=====
let gyLastItems = [];
let gySort = { key: '', dir: -1 };

function gySizeBytes(s) {
  const m = String(s || '').match(/^([\d.]+)\s*([KMGT])/i);
  if (!m) return 0;
  const mult = { K: 1 << 10, M: 1 << 20, G: 1 << 30, T: 1 << 40 }[m[2].toUpperCase()] || 0;
  return parseFloat(m[1]) * mult;
}
function gyTimeSec(s) {
  const m = String(s || '').match(/^(\d+)\s*(小时|天|周|月|年)/);
  if (!m) return 0;
  const mult = { '小时': 3600, '天': 86400, '周': 604800, '月': 2592000, '年': 31536000 }[m[2]] || 0;
  return parseFloat(m[1]) * mult;
}
function gySeeds(v) {
  const n = parseInt(v, 10);
  return isNaN(n) ? 0 : n;
}

function gySortBy(key) {
  if (gySort.key === key) {
    gySort.dir = -gySort.dir; // 再点一次反向
  } else {
    // 首次点击：大小/做种取最大在前，时间取最新在前（距今秒数最小）
    gySort.key = key;
    gySort.dir = key === 'time' ? 1 : -1;
  }
  gyRenderTorrentList();
}

function gyRenderTorrentList() {
  const list = document.getElementById('gy-torrent-list');
  if (!list) return;
  // 排序按钮：与资源分类 tab 同款胶囊样式，激活高亮，再点一次反向
  const pill = (label, key) => {
    const active = gySort.key === key && key !== '';
    const arrow = active ? (gySort.dir === -1 ? ' ↓' : ' ↑') : '';
    const bg = active ? 'var(--primary)' : 'var(--fill-2)';
    const fg = active ? '#fff' : 'var(--text-2)';
    return '<span onclick="gySortBy(\'' + key + '\')" '
      + 'style="cursor:pointer;padding:3px 12px;border-radius:999px;background:' + bg + ';color:' + fg + ';font-size:12.5px;line-height:20px">'
      + label + arrow + '</span>';
  };
  const items = gyLastItems.slice();
  if (gySort.key === 'size') items.sort((a, b) => (gySizeBytes(a.size) - gySizeBytes(b.size)) * gySort.dir);
  if (gySort.key === 'seeds') items.sort((a, b) => (gySeeds(a.seeds) - gySeeds(b.seeds)) * gySort.dir);
  if (gySort.key === 'time') items.sort((a, b) => (gyTimeSec(a.time) - gyTimeSec(b.time)) * gySort.dir);
  list.innerHTML = '<div style="display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin:0 0 10px;font-size:12px;color:var(--text-3)">排序'
    + pill('默认', '') + pill('大小', 'size') + pill('做种', 'seeds') + pill('时间', 'time')
    + '</div>'
    + '<div class="otk">' + items.map(it => {
      const gi = gyLastItems.indexOf(it); // 行点击走下标，path 不再拼进内联事件
      const meta = [it.size, it.time, it.seeds !== undefined && it.seeds !== '' ? '做种 ' + it.seeds : '']
        .filter(Boolean).map(esc).join('</span><span class="otk-dot">·</span><span>');
      return '<div class="otk-row" style="cursor:pointer" onclick="gySubmitIdx(' + gi + ',this)">'
      + '<span class="otag" style="background:#eafaf0;color:#00874a">种子</span>'
      + '<div class="otk-main"><div class="otk-name" title="' + esc(it.title) + '">' + esc(it.title) + '</div>'
      + '<div class="otk-sub"><span>' + meta + '</span></div>'
      + '<div class="gy-st" style="font-size:12px;margin-top:4px;color:var(--text-3)"></div></div>'
      + '<div class="otk-side" style="color:var(--primary)">离线下载 ›</div>'
      + '</div>';
    }).join('') + '</div>';
}

// 点击种子行：提取磁力并直接提交 115 离线下载（一步完成）
async function gySubmit(path, el) {
  const st = el.querySelector('.gy-st');
  if (st && st.dataset.done === '1') { toast('该种子已提交过离线下载'); return; }
  if (st) { st.style.color = 'var(--text-3)'; st.textContent = '提取磁力…'; }
  try {
    const d = await api('/guanying/resources?path=' + encodeURIComponent(path));
    if (!d.magnet) throw new Error('该条目没有磁力链接');
    if (st) { st.textContent = '提交离线…'; }
    const r = await api('/guanying/offline', {
      method: 'POST',
      body: JSON.stringify({ magnet: d.magnet }),
    });
    if (st) { st.style.color = 'var(--success)'; st.dataset.done = '1'; st.textContent = '✓ ' + (r.message || '已提交离线下载'); }
    toast('已提交 115 离线下载，完成后自动整理入库');
  } catch (e) {
    if (st) { st.style.color = 'var(--danger)'; st.textContent = '✗ ' + e.message; }
  }
}

function buildGyTabs(zy) {
  const tab = (name, label, count, active) => {
    const safeName = String(name).replace(/\\/g, '\\\\').replace(/'/g, "\\'");
    const bg = active ? 'var(--primary)' : 'var(--fill-2)';
    const fg = active ? '#fff' : 'var(--text-2)';
    return '<span onclick="gySearchSite(gyCurTitle, \'' + safeName + '\')" '
      + 'style="cursor:pointer;padding:3px 12px;border-radius:999px;background:' + bg + ';color:' + fg + ';font-size:12.5px;line-height:20px">'
      + esc(label) + (count !== '' ? ' ' + count : '') + '</span>';
  };
  let html = tab('', '全部', '', gyCurZy === '');
  Object.keys(zy).forEach(name => {
    const n = zy[name];
    html += tab(name, name, typeof n === 'number' ? n : '', gyCurZy === name);
  });
  return '<div id="gy-zy-tabs" style="margin-bottom:10px;display:flex;flex-wrap:wrap;gap:6px">' + html + '</div>';
}
// ==================== 115 每日签到（扩展功能插件） ====================
// 每日在设定时间窗口内随机时刻自动签到领积分；也可点「立即签到」手动执行。

function ck115Config() {
  document.getElementById('ck115-modal').style.display = 'flex';
  ck115LoadPage();
}
function closeCk115Modal() {
  document.getElementById('ck115-modal').style.display = 'none';
}

function setCk115Enabled(v) {
  document.querySelectorAll('#ck115-switch .seg-item').forEach(el => {
    el.classList.toggle('active', String(el.dataset.value) === String(v));
  });
}

async function ck115LoadPage() {
  const st = document.getElementById('ck115-status');
  try {
    const d = await api('/115checkin/config');
    setCk115Enabled(!!d.enabled);
    document.getElementById('ck115-cron').value = d.cron || '0 8 * * *';
    ck115Preview();
    if (st) {
      let html = '';
      if (!d.enabled) html = '未开启';
      else if (d.signed_today) html = '<span style="color:var(--success)">✓ 今日已签到</span>';
      else html = '今日未签到';
      if (d.last_result) {
        html += '<br>上次：' + esc(d.last_result) + (d.last_result_at ? '（' + esc(d.last_result_at) + '）' : '');
      }
      if (d.status_error) {
        html += '<br><span style="color:var(--text-3)">状态查询：' + esc(d.status_error) + '</span>';
      }
      st.innerHTML = html;
      st.style.color = '';
    }
  } catch (e) {
    if (st) st.textContent = '✗ ' + e.message;
  }
}

// cron 下次执行时间预览（复用同步页的预览接口）
async function ck115Preview() {
  const box = document.getElementById('ck115-next');
  if (!box) return;
  const expr = document.getElementById('ck115-cron').value.trim() || '0 8 * * *';
  try {
    const d = await api('/sync/cron-preview', {
      method: 'POST',
      body: JSON.stringify({ cron: expr }),
    });
    box.textContent = '下次执行：' + (d.next || []).join('、');
  } catch (e) {
    box.textContent = e.message;
  }
}

async function ck115Save(btn) {
  const orig = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = '保存中...'; }
  try {
    const enableEl = document.querySelector('#ck115-switch .seg-item.active');
    await api('/115checkin/config', {
      method: 'POST',
      body: JSON.stringify({
        enabled: enableEl ? enableEl.dataset.value === 'true' : false,
        cron: document.getElementById('ck115-cron').value.trim() || '0 8 * * *',
      }),
    });
    toast('保存成功');
    await ck115LoadPage();
  } catch (e) { toast(e.message); }
  finally { if (btn) { btn.disabled = false; btn.textContent = orig; } }
}

async function ck115Run(btn) {
  const st = document.getElementById('ck115-status');
  const card = document.getElementById('ck115-result');
  const orig = btn.textContent;
  btn.disabled = true;
  btn.textContent = '签到中…';
  const show = (ok, text) => {
    const cls = ok ? 'var(--success)' : '#e74c3c';
    const line = '<span style="color:' + cls + '">' + (ok ? '✓ ' : '✗ ') + esc(text) + '</span>'
      + '<span style="color:var(--text-3)"> · ' + new Date().toLocaleTimeString() + '</span>';
    if (card) { card.style.display = 'block'; card.innerHTML = line; }
    if (st) { st.style.color = cls; st.textContent = (ok ? '✓ ' : '✗ ') + text; }
  };
  try {
    const d = await api('/115checkin/run', { method: 'POST' });
    show(true, d.message || '签到成功');
    toast(d.message || '签到成功');
  } catch (e) {
    show(false, e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = orig;
  }
}
