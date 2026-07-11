/*
 * custom-import-account.js
 * Chen mot lua chon "Import bang text" vao popup Add Account cua Kiro-Go.
 *
 * Cach hoat dong:
 *  - Modal Add Account render mot .method-list gom cac .method-card (data-method).
 *  - Ta dung MutationObserver de phat hien modal, roi append them 1 card moi.
 *  - Bat click card do (capture phase) truoc handler cua app -> hien form dan JSON.
 *  - Tai dung phien dang nhap hien tai (mat khau admin da luu) -> POST /admin/api/accounts.
 *
 * Duoc mount + chen boi entrypoint.sh (self-healing, khong mat khi container recreate).
 */
(function () {
  'use strict';

  var METHOD = 'textimport';

  function getPassword() {
    return sessionStorage.getItem('admin_password') ||
           localStorage.getItem('admin_password') || '';
  }

  // Tach input thanh mang account object: 1 object, 1 mang [], hoac nhieu dong JSON.
  function parseAccounts(raw) {
    raw = (raw || '').trim();
    if (!raw) return [];
    try {
      var j = JSON.parse(raw);
      if (Array.isArray(j)) return j;
      if (j && typeof j === 'object') return [j];
    } catch (e) { /* thu tach theo dong */ }
    var out = [];
    raw.split(/\r?\n/).forEach(function (line) {
      line = line.trim();
      if (!line) return;
      try { out.push(JSON.parse(line)); } catch (e) { /* bo qua dong hong */ }
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

  function refreshLists() {
    try { if (window.loadAccounts) window.loadAccounts(); } catch (e) { }
    try { if (window.loadStats) window.loadStats(); } catch (e) { }
  }

  // Hien form dan text ngay trong modal body.
  function showTextImport() {
    var body = document.getElementById('modalBody');
    var title = document.getElementById('modalTitle');
    if (!body) return;
    if (title) title.textContent = 'Import bằng text';

    body.innerHTML =
      '<p class="help-block" style="margin-bottom:12px">' +
      'Dán nội dung xuất từ <code>export-account.ps1</code>. ' +
      'Hỗ trợ nhiều tài khoản (mỗi dòng 1 JSON hoặc 1 mảng JSON).</p>' +
      '<textarea id="ci_data" rows="7" style="width:100%;font-family:monospace;font-size:12px;' +
      'padding:10px;border-radius:8px;border:1px solid var(--border,#334);background:rgba(255,255,255,.03);' +
      'color:inherit;resize:vertical" placeholder=\'{"nickname":"...","refreshToken":"...","authMethod":"external_idp",...}\'></textarea>' +
      '<div id="ci_msg" style="margin-top:10px;font-size:13px;line-height:1.6"></div>' +
      '<div class="modal-footer" style="margin-top:16px;display:flex;gap:8px;justify-content:flex-end">' +
      '<button class="btn btn-secondary" type="button" id="ci_back">Quay lại</button>' +
      '<button class="btn btn-primary" type="button" id="ci_do">Import</button>' +
      '</div>';

    var backBtn = document.getElementById('ci_back');
    var doBtn = document.getElementById('ci_do');
    if (backBtn) backBtn.addEventListener('click', function () {
      if (window.showModal) window.showModal('add');
    });
    if (doBtn) doBtn.addEventListener('click', runImport);
  }

  async function runImport() {
    var dataEl = document.getElementById('ci_data');
    var msgEl = document.getElementById('ci_msg');
    var doBtn = document.getElementById('ci_do');
    var pw = getPassword();
    var accounts = parseAccounts(dataEl ? dataEl.value : '');

    if (!pw) { toast('err', 'Phiên đăng nhập hết hạn, đăng nhập lại.'); return; }
    if (accounts.length === 0) { if (msgEl) msgEl.innerHTML = '<span style="color:#f87171">Không đọc được dữ liệu JSON.</span>'; return; }

    if (doBtn) { doBtn.disabled = true; doBtn.textContent = 'Đang import...'; }

    var ok = 0, fail = 0, lines = [];
    for (var i = 0; i < accounts.length; i++) {
      var acc = accounts[i];
      if (!acc.refreshToken) { fail++; lines.push('• Bỏ qua: thiếu refreshToken'); continue; }
      try {
        var res = await fetch('/admin/api/accounts', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Admin-Password': pw },
          body: JSON.stringify(acc)
        });
        var d = {};
        try { d = await res.json(); } catch (e) { }
        if (res.ok && d.success) { ok++; lines.push('• ' + (acc.nickname || d.id || 'account') + ' — OK'); }
        else { fail++; lines.push('• ' + (acc.nickname || 'account') + ' — lỗi: ' + (d.error || res.status)); }
      } catch (e) { fail++; lines.push('• ' + (acc.nickname || 'account') + ' — lỗi mạng'); }
    }

    if (doBtn) { doBtn.disabled = false; doBtn.textContent = 'Import'; }
    if (msgEl) {
      var color = fail === 0 ? '#34d399' : (ok > 0 ? '#fbbf24' : '#f87171');
      msgEl.innerHTML = '<div style="color:' + color + '"><b>Xong: ' + ok + ' thành công, ' + fail + ' lỗi</b></div>' +
        '<div style="color:var(--text-dim,#8b93a9);margin-top:6px">' + lines.join('<br>') + '</div>';
    }
    if (ok > 0) {
      refreshLists();
      toast('ok', 'Đã import ' + ok + ' tài khoản');
    }
  }

  // Chen card vao method-list khi modal Add xuat hien.
  function tryInjectCard() {
    var list = document.querySelector('#modalBody .method-list');
    if (!list) return;
    if (list.querySelector('[data-method="' + METHOD + '"]')) return;
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'method-card';
    btn.setAttribute('data-method', METHOD);
    btn.innerHTML =
      '<span class="method-icon"><i class="fa-solid fa-paste" aria-hidden="true"></i></span>' +
      '<span class="method-body">' +
      '<span class="method-title">Import bằng text</span>' +
      '<span class="method-desc">Dán JSON xuất từ export-account.ps1</span>' +
      '</span>' +
      '<span class="method-arrow" aria-hidden="true"><i class="fa-solid fa-chevron-right"></i></span>';
    list.appendChild(btn);
  }

  function init() {
    // Theo doi DOM de chen card moi lan modal Add duoc render.
    var observer = new MutationObserver(function () { tryInjectCard(); });
    observer.observe(document.body, { childList: true, subtree: true });

    // Bat click card cua ta TRUOC handler cua app (capture phase) de app khong
    // goi showModal('textimport') (khong ton tai) va de ta hien form rieng.
    document.addEventListener('click', function (e) {
      var card = e.target.closest ? e.target.closest('[data-method="' + METHOD + '"]') : null;
      if (!card) return;
      e.stopPropagation();
      e.preventDefault();
      showTextImport();
    }, true);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
