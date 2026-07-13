/*
 * custom-codex-accounts.js
 * Thêm card "OpenAI Codex" vào tab Accounts của Kiro-Go admin.
 *
 * - GET/POST /admin/api/codex-accounts (pool riêng codexAccounts[])
 * - Test từng account, bật/tắt, xoá
 * - Ô "% chia sang Codex" (POST /admin/api/codex-split)
 *
 * Mount + chèn bởi entrypoint.sh (self-healing).
 */
(function () {
  'use strict';

  var SECTION_ID = 'codexAccountsCard';
  var LIST_ID = 'codexAccountsList';

  function getPassword() {
    return sessionStorage.getItem('admin_password') ||
           localStorage.getItem('admin_password') || '';
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function toast(msg, ok) {
    try {
      if (ok && window.toastPrimary) return window.toastPrimary(msg);
      if (!ok && window.toastError) return window.toastError(msg);
      if (window.toastWarning) return window.toastWarning(msg);
    } catch (e) { }
  }

  function fmtTime(unix) {
    if (!unix) return '—';
    try { return new Date(unix * 1000).toLocaleString(); } catch (e) { return '—'; }
  }

  function ensureSection() {
    var tab = document.getElementById('tabAccounts');
    if (!tab) return null;
    var existing = document.getElementById(SECTION_ID);
    if (existing) return existing;

    var card = document.createElement('div');
    card.className = 'card';
    card.id = SECTION_ID;
    card.style.marginTop = '16px';
    card.innerHTML =
      '<div class="card-header" style="display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap">' +
      '<span class="card-title"><i class="fa-solid fa-terminal" style="color:#10a37f;margin-right:8px"></i>OpenAI Codex</span>' +
      '<div class="card-actions" style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">' +
      '<div style="display:flex;align-items:center;gap:6px;padding:4px 8px;border:1px solid var(--border,#e5e5e5);border-radius:8px" title="% request chuyển sang Codex trong khi Kiro vẫn còn tài khoản (0 = tắt)">' +
      '<i class="fa-solid fa-code-branch" style="color:#10a37f"></i>' +
      '<span style="font-size:12.5px;color:var(--muted-foreground,#525252)">Chia sang Codex</span>' +
      '<input type="number" id="codexSplitInput" min="0" max="100" step="1" value="0" ' +
      'style="width:58px;padding:3px 6px;border:1px solid var(--border,#e5e5e5);border-radius:6px;font-size:13px;text-align:right" />' +
      '<span style="font-size:12.5px;color:var(--muted-foreground,#525252)">%</span>' +
      '<button type="button" class="btn btn-outline btn-sm" id="codexSplitSaveBtn"><i class="fa-solid fa-check"></i></button>' +
      '</div>' +
      '<button type="button" class="btn btn-outline btn-sm" id="codexRefreshBtn">' +
      '<i class="fa-solid fa-arrows-rotate"></i> <span class="btn-text">Refresh</span></button>' +
      '<button type="button" class="btn btn-outline btn-sm" id="codexTestAllBtn" title="Test tất cả tài khoản Codex">' +
      '<i class="fa-solid fa-plug-circle-check"></i> <span class="btn-text">Test all</span></button>' +
      '</div></div>' +
      '<p style="font-size:12.5px;color:var(--muted-foreground,#525252);margin:0 0 12px;line-height:1.5">' +
      'Pool <code>codexAccounts[]</code> riêng — không hiện trong danh sách Kiro. Import: <b>Add Account → Import Codex</b>. ' +
      '<b>Chia sang Codex</b>: ví dụ 50% thì cứ 100 request có ~50 sang Codex (chỉ áp dụng khi cả hai pool còn tài khoản).' +
      '</p>' +
      '<div id="' + LIST_ID + '"><div style="color:var(--muted-foreground,#525252);font-size:13px">Đang tải…</div></div>';

    tab.appendChild(card);
    var rb = document.getElementById('codexRefreshBtn');
    if (rb) rb.addEventListener('click', loadCodexAccounts);
    var tb = document.getElementById('codexTestAllBtn');
    if (tb) tb.addEventListener('click', testAllCodex);
    var ssb = document.getElementById('codexSplitSaveBtn');
    if (ssb) ssb.addEventListener('click', saveCodexSplit);
    loadCodexSplit();
    return card;
  }

  async function loadCodexSplit() {
    var pw = getPassword();
    if (!pw) return;
    try {
      var res = await fetch('/admin/api/codex-split', { headers: { 'X-Admin-Password': pw } });
      if (!res.ok) return;
      var data = await res.json();
      var inp = document.getElementById('codexSplitInput');
      if (inp && typeof data.percent === 'number') inp.value = data.percent;
    } catch (e) { /* non-fatal */ }
  }

  async function saveCodexSplit() {
    var pw = getPassword();
    if (!pw) { toast('Chưa nhập mật khẩu admin', false); return; }
    var inp = document.getElementById('codexSplitInput');
    if (!inp) return;
    var p = parseInt(inp.value, 10);
    if (isNaN(p) || p < 0 || p > 100) { toast('Giá trị phải từ 0 đến 100', false); return; }
    try {
      var res = await fetch('/admin/api/codex-split', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Admin-Password': pw },
        body: JSON.stringify({ percent: p })
      });
      var data = await res.json();
      if (res.ok && data.success) {
        toast('Đã lưu: ' + data.percent + '% request sẽ chuyển sang Codex', true);
        inp.value = data.percent;
      } else {
        toast(data.error || 'Lưu thất bại', false);
      }
    } catch (e) {
      toast('Lỗi mạng khi lưu', false);
    }
  }

  function accRow(a) {
    var status = a.enabled ? '<span style="color:#34d399">enabled</span>' : '<span style="color:#f87171">disabled</span>';
    var quota = a.quotaStatus ? ' · <span style="color:var(--muted-foreground,#8b93a9)">' + esc(a.quotaStatus) + '</span>' : '';
    return '<div class="codex-row" data-id="' + esc(a.id) + '" style="display:flex;align-items:center;justify-content:space-between;gap:10px;padding:8px 10px;border:1px solid var(--border,#e5e5e5);border-radius:8px;margin-bottom:6px;flex-wrap:wrap">' +
      '<div style="min-width:0;flex:1">' +
      '<div style="font-weight:600;font-size:13px">' + esc(a.email || a.displayName || a.id) +
      (a.planType ? ' <span style="font-size:11px;color:#10a37f;border:1px solid #10a37f;border-radius:4px;padding:0 4px">' + esc(a.planType) + '</span>' : '') +
      '</div>' +
      '<div style="font-size:11.5px;color:var(--muted-foreground,#8b93a9)">' + status + quota +
      ' · tokens=' + (a.totalTokens || 0) + ' · credits=' + (a.totalCredits || 0) +
      ' · exp=' + fmtTime(a.expiresAt) + '</div>' +
      '</div>' +
      '<div style="display:flex;gap:6px;flex-wrap:wrap">' +
      '<button type="button" class="btn btn-outline btn-sm codex-test" data-id="' + esc(a.id) + '"><i class="fa-solid fa-plug-circle-check"></i></button>' +
      '<button type="button" class="btn btn-outline btn-sm codex-toggle" data-id="' + esc(a.id) + '" data-enabled="' + (a.enabled ? '1' : '0') + '">' +
      (a.enabled ? '<i class="fa-solid fa-pause"></i>' : '<i class="fa-solid fa-play"></i>') + '</button>' +
      '<button type="button" class="btn btn-outline btn-sm codex-del" data-id="' + esc(a.id) + '" style="color:#f87171"><i class="fa-solid fa-trash"></i></button>' +
      '</div>' +
      '</div>';
  }

  async function loadCodexAccounts() {
    var listEl = document.getElementById(LIST_ID);
    var pw = getPassword();
    if (!pw || !listEl) return;
    listEl.innerHTML = '<div style="color:var(--muted-foreground,#525252);font-size:13px">Đang tải…</div>';
    try {
      var res = await fetch('/admin/api/codex-accounts', { headers: { 'X-Admin-Password': pw } });
      var d = await res.json();
      var accs = (d && d.accounts) || [];
      if (!accs.length) {
        listEl.innerHTML = '<div style="color:var(--muted-foreground,#8b93a9);font-size:13px">Chưa có tài khoản Codex. Add Account → Import Codex.</div>';
        return;
      }
      listEl.innerHTML = accs.map(accRow).join('');
      bindRowActions();
    } catch (e) {
      listEl.innerHTML = '<div style="color:#f87171;font-size:13px">Lỗi tải danh sách Codex.</div>';
    }
  }
  window.loadCodexAccounts = loadCodexAccounts;

  function bindRowActions() {
    var listEl = document.getElementById(LIST_ID);
    if (!listEl) return;
    listEl.querySelectorAll('.codex-test').forEach(function (b) {
      b.addEventListener('click', function () { testOne(b.getAttribute('data-id'), b); });
    });
    listEl.querySelectorAll('.codex-toggle').forEach(function (b) {
      b.addEventListener('click', function () { toggleOne(b.getAttribute('data-id'), b.getAttribute('data-enabled') !== '1'); });
    });
    listEl.querySelectorAll('.codex-del').forEach(function (b) {
      b.addEventListener('click', function () { delOne(b.getAttribute('data-id')); });
    });
  }

  async function testOne(id, btn) {
    var pw = getPassword();
    if (!pw) return;
    if (btn) { btn.disabled = true; btn.innerHTML = '<i class="fa-solid fa-spinner fa-spin"></i>'; }
    try {
      var res = await fetch('/admin/api/codex-accounts/' + encodeURIComponent(id) + '/test', {
        method: 'POST', headers: { 'X-Admin-Password': pw }
      });
      var d = await res.json();
      toast(d.ok ? 'Codex OK' : ('Codex lỗi: ' + (d.error || d.status)), !!d.ok);
    } catch (e) {
      toast('Lỗi mạng khi test', false);
    }
    if (btn) { btn.disabled = false; btn.innerHTML = '<i class="fa-solid fa-plug-circle-check"></i>'; }
  }

  async function toggleOne(id, enable) {
    var pw = getPassword();
    if (!pw) return;
    try {
      await fetch('/admin/api/codex-accounts/' + encodeURIComponent(id) + '/enabled', {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Admin-Password': pw },
        body: JSON.stringify({ enabled: enable })
      });
      loadCodexAccounts();
    } catch (e) { toast('Lỗi mạng', false); }
  }

  async function delOne(id) {
    var pw = getPassword();
    if (!pw) return;
    if (!confirm('Xoá tài khoản Codex này?')) return;
    try {
      await fetch('/admin/api/codex-accounts/' + encodeURIComponent(id), {
        method: 'DELETE', headers: { 'X-Admin-Password': pw }
      });
      loadCodexAccounts();
    } catch (e) { toast('Lỗi mạng', false); }
  }

  async function testAllCodex() {
    var pw = getPassword();
    if (!pw) return;
    var btn = document.getElementById('codexTestAllBtn');
    if (btn) { btn.disabled = true; }
    try {
      var res = await fetch('/admin/api/codex-accounts/test', { method: 'POST', headers: { 'X-Admin-Password': pw } });
      var d = await res.json();
      toast('Codex test: ' + (d.ok || 0) + ' OK, ' + (d.failed || 0) + ' lỗi', (d.failed || 0) === 0);
    } catch (e) { toast('Lỗi mạng', false); }
    if (btn) { btn.disabled = false; }
    loadCodexAccounts();
  }

  function init() {
    var observer = new MutationObserver(function () {
      var tab = document.getElementById('tabAccounts');
      if (tab && !document.getElementById(SECTION_ID)) {
        ensureSection();
        loadCodexAccounts();
      }
    });
    observer.observe(document.body, { childList: true, subtree: true });
    if (document.getElementById('tabAccounts')) {
      ensureSection();
      loadCodexAccounts();
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
