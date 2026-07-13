/*
 * custom-import-codex.js
 * Chèn card "Import Codex" vào popup Add Account của Kiro-Go.
 *
 * - POST /admin/api/codex-accounts (pool riêng codexAccounts[])
 * - Nhận JSON export từ 9router (provider=codex): 1 object / array / multi-line
 * - Không trộn vào accounts[] Kiro/Claude hay grokAccounts[]
 *
 * Mount + chèn bởi entrypoint.sh (self-healing).
 */
(function () {
  'use strict';

  var METHOD = 'codeximport';

  function getPassword() {
    return sessionStorage.getItem('admin_password') ||
           localStorage.getItem('admin_password') || '';
  }

  function parseAccounts(raw) {
    raw = (raw || '').trim();
    if (!raw) return [];
    try {
      var j = JSON.parse(raw);
      if (Array.isArray(j)) return j;
      if (j && typeof j === 'object') return [j];
    } catch (e) { /* try line-by-line */ }
    var out = [];
    raw.split(/\r?\n/).forEach(function (line) {
      line = line.trim();
      if (!line) return;
      try { out.push(JSON.parse(line)); } catch (e) { /* skip bad line */ }
    });
    return out;
  }

  function toast(kind, msg) {
    try {
      if (kind === 'ok' && window.toastPrimary) return window.toastPrimary(msg);
      if (kind === 'err' && window.toastError) return window.toastError(msg);
      if (window.toastWarning) return window.toastWarning(msg);
    } catch (e) { }
    alert(msg);
  }

  function hasToken(acc) {
    if (!acc || typeof acc !== 'object') return false;
    if (acc.accessToken || acc.refreshToken) return true;
    if (acc.data && typeof acc.data === 'object') {
      if (acc.data.accessToken || acc.data.refreshToken) return true;
    }
    return false;
  }

  function labelOf(acc, fallbackId) {
    return acc.email || acc.displayName || acc.nickname || acc.name || fallbackId || 'codex-account';
  }

  function showCodexImport() {
    var body = document.getElementById('modalBody');
    var title = document.getElementById('modalTitle');
    if (!body) return;
    if (title) title.textContent = 'Import Codex (OpenAI)';

    body.innerHTML =
      '<p class="help-block" style="margin-bottom:12px">' +
      'Dán JSON export từ 9router (connection provider <code>codex</code>). ' +
      'Lưu vào pool <b>riêng</b> <code>codexAccounts[]</code> — không trộn Kiro/Claude/Grok.</p>' +
      '<textarea id="cci_data" rows="8" style="width:100%;font-family:monospace;font-size:12px;' +
      'padding:10px;border-radius:8px;border:1px solid var(--border,#334);background:rgba(255,255,255,.03);' +
      'color:inherit;resize:vertical" placeholder=\'[{"email":"...","accessToken":"...","refreshToken":"...","expiresAt":...,"chatgptAccountId":"..."}]\'></textarea>' +
      '<div style="margin-top:10px;font-size:12px;line-height:1.55;color:var(--text-dim,#8b93a9);' +
      'padding:10px 12px;border-radius:10px;background:rgba(16,185,129,.08);border:1px solid rgba(16,185,129,.22)">' +
      '<b style="color:inherit">Credit khách:</b> model <code>gpt-5.x</code> trừ trên API key khách ' +
      '(1 credit ≈ 1000 tokens).' +
      '</div>' +
      '<div id="cci_msg" style="margin-top:10px;font-size:13px;line-height:1.6"></div>' +
      '<div class="modal-footer" style="margin-top:16px;display:flex;gap:8px;justify-content:flex-end;flex-wrap:wrap">' +
      '<button class="btn btn-secondary" type="button" id="cci_list">Xem Codex đã có</button>' +
      '<button class="btn btn-secondary" type="button" id="cci_back">Quay lại</button>' +
      '<button class="btn btn-primary" type="button" id="cci_do">Import Codex</button>' +
      '</div>';

    var backBtn = document.getElementById('cci_back');
    var doBtn = document.getElementById('cci_do');
    var listBtn = document.getElementById('cci_list');
    if (backBtn) backBtn.addEventListener('click', function () {
      if (window.showModal) window.showModal('add');
    });
    if (doBtn) doBtn.addEventListener('click', runImport);
    if (listBtn) listBtn.addEventListener('click', listExisting);
  }

  async function listExisting() {
    var msgEl = document.getElementById('cci_msg');
    var pw = getPassword();
    if (!pw) { toast('err', 'Phiên đăng nhập hết hạn, đăng nhập lại.'); return; }
    try {
      var res = await fetch('/admin/api/codex-accounts', {
        headers: { 'X-Admin-Password': pw }
      });
      var d = {};
      try { d = await res.json(); } catch (e) { }
      if (!res.ok) {
        if (msgEl) msgEl.innerHTML = '<span style="color:#f87171">' + (d.error || ('HTTP ' + res.status)) + '</span>';
        return;
      }
      var accs = d.accounts || [];
      if (!accs.length) {
        if (msgEl) msgEl.innerHTML = '<span style="color:#fbbf24">Chưa có tài khoản Codex nào.</span>';
        return;
      }
      var lines = accs.map(function (a) {
        return '• ' + (a.email || a.displayName || a.id) +
          ' · enabled=' + a.enabled +
          ' · tokens=' + (a.totalTokens || 0) +
          ' · credits=' + (a.totalCredits || 0);
      });
      if (msgEl) {
        msgEl.innerHTML =
          '<div style="color:#34d399"><b>Đang có ' + accs.length + ' Codex account</b></div>' +
          '<div style="color:var(--text-dim,#8b93a9);margin-top:6px">' + lines.join('<br>') + '</div>';
      }
    } catch (e) {
      if (msgEl) msgEl.innerHTML = '<span style="color:#f87171">Lỗi mạng khi tải danh sách.</span>';
    }
  }

  async function runImport() {
    var dataEl = document.getElementById('cci_data');
    var msgEl = document.getElementById('cci_msg');
    var doBtn = document.getElementById('cci_do');
    var pw = getPassword();
    var accounts = parseAccounts(dataEl ? dataEl.value : '');

    if (!pw) { toast('err', 'Phiên đăng nhập hết hạn, đăng nhập lại.'); return; }
    if (accounts.length === 0) {
      if (msgEl) msgEl.innerHTML = '<span style="color:#f87171">Không đọc được dữ liệu JSON.</span>';
      return;
    }

    if (doBtn) { doBtn.disabled = true; doBtn.textContent = 'Đang import...'; }

    var ok = 0, fail = 0, lines = [];
    for (var i = 0; i < accounts.length; i++) {
      var acc = accounts[i];
      if (!hasToken(acc)) {
        fail++;
        lines.push('[skip] missing accessToken/refreshToken');
        continue;
      }
      try {
        var res = await fetch('/admin/api/codex-accounts', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Admin-Password': pw },
          body: JSON.stringify(acc)
        });
        var d = {};
        try { d = await res.json(); } catch (e) { }
        if (res.ok && d.success) {
          ok++;
          lines.push((d.updated ? "[updated] " : "[new] ") + labelOf(acc, d.id));
        } else {
          fail++;
          lines.push('[fail] ' + labelOf(acc) + ' - ' + (d.error || res.status));
        }
      } catch (e) {
        fail++;
        lines.push('[fail] ' + labelOf(acc) + ' - network error');
      }
    }

    if (doBtn) { doBtn.disabled = false; doBtn.textContent = 'Import Codex'; }
    if (msgEl) {
      var color = fail === 0 ? '#34d399' : (ok > 0 ? '#fbbf24' : '#f87171');
      msgEl.innerHTML =
        '<div style="color:' + color + '"><b>Xong: ' + ok + ' thành công, ' + fail + ' lỗi</b></div>' +
        '<div style="color:var(--text-dim,#8b93a9);margin-top:6px">' + lines.join('<br>') + '</div>';
    }
    if (ok > 0) {
      toast("ok", "Imported/updated " + ok + " Codex account(s). See Accounts tab -> OpenAI Codex section.");
      try { if (window.loadCodexAccounts) window.loadCodexAccounts(); } catch (e) {}
    }
  }

  function tryInjectCard() {
    var list = document.querySelector('#modalBody .method-list');
    if (!list) return;
    if (list.querySelector('[data-method="' + METHOD + '"]')) return;

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'method-card';
    btn.setAttribute('data-method', METHOD);
    btn.innerHTML =
      '<span class="method-icon" style="background:linear-gradient(135deg,#10a37f,#0ea5e9);color:#fff">' +
      '<i class="fa-solid fa-terminal" aria-hidden="true"></i></span>' +
      '<span class="method-body">' +
      '<span class="method-title">Import Codex</span>' +
      '<span class="method-desc">Dán JSON export từ 9router (OpenAI Codex)</span>' +
      '</span>' +
      '<span class="method-arrow" aria-hidden="true"><i class="fa-solid fa-chevron-right"></i></span>';
    list.appendChild(btn);
  }

  function init() {
    var observer = new MutationObserver(function () { tryInjectCard(); });
    observer.observe(document.body, { childList: true, subtree: true });

    document.addEventListener('click', function (e) {
      var card = e.target.closest ? e.target.closest('[data-method="' + METHOD + '"]') : null;
      if (!card) return;
      e.stopPropagation();
      e.preventDefault();
      showCodexImport();
    }, true);

    tryInjectCard();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
