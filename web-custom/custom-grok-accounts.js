/*
 * custom-grok-accounts.js
 * Hien thi danh sach Grok CLI (grokAccounts[]) tren tab Accounts.
 * Pool rieng — khong tron vao #accountsList (Kiro/Claude).
 *
 * Mount + inject boi entrypoint.sh.
 */
(function () {
  'use strict';

  var SECTION_ID = 'grokAccountsSection';
  var LIST_ID = 'grokAccountsList';

  function getPassword() {
    return sessionStorage.getItem('admin_password') ||
           localStorage.getItem('admin_password') || '';
  }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function fmtTime(unix) {
    if (!unix) return '—';
    try {
      return new Date(unix * 1000).toLocaleString();
    } catch (e) {
      return String(unix);
    }
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
      '<span class="card-title"><i class="fa-solid fa-robot" style="color:#a855f7;margin-right:8px"></i>Grok CLI (Grok Build)</span>' +
      '<div class="card-actions" style="display:flex;gap:8px;flex-wrap:wrap">' +
      '<button type="button" class="btn btn-outline btn-sm" id="grokRefreshBtn">' +
      '<i class="fa-solid fa-arrows-rotate"></i> <span class="btn-text">Refresh</span></button>' +
      '</div></div>' +
      '<p style="font-size:12.5px;color:var(--text-dim,#8b93a9);margin:0 0 12px;line-height:1.5">' +
      'Pool <code>grokAccounts[]</code> riêng — không hiện trong danh sách Kiro phía trên. ' +
      'Import qua <b>Add Account → Import Grok CLI</b>. Model: <code>grok-4.5*</code>.' +
      '</p>' +
      '<div id="' + LIST_ID + '"><div style="color:var(--text-dim,#8b93a9);font-size:13px">Đang tải…</div></div>';

    // append after the main accounts card
    tab.appendChild(card);

    var btn = document.getElementById('grokRefreshBtn');
    if (btn) btn.addEventListener('click', loadGrokAccounts);
    return card;
  }

  function renderEmpty(msg) {
    var el = document.getElementById(LIST_ID);
    if (!el) return;
    el.innerHTML = '<div style="padding:14px;border:1px dashed rgba(168,85,247,.35);border-radius:12px;' +
      'color:var(--text-dim,#8b93a9);font-size:13px">' + esc(msg) + '</div>';
  }

  function renderList(accounts) {
    var el = document.getElementById(LIST_ID);
    if (!el) return;
    if (!accounts || !accounts.length) {
      renderEmpty('Chưa có tài khoản Grok. Bấm Add Account → Import Grok CLI và dán export từ 9router.');
      return;
    }

    var html = accounts.map(function (a) {
      var statusColor = a.enabled ? '#34d399' : '#f87171';
      var statusText = a.enabled ? 'enabled' : 'disabled';
      if (a.banStatus) statusText = a.banStatus;
      return (
        '<div class="account-item" data-grok-id="' + esc(a.id) + '" style="' +
        'border:1px solid rgba(168,85,247,.22);border-radius:14px;padding:14px 16px;margin-bottom:10px;' +
        'background:linear-gradient(135deg,rgba(168,85,247,.08),rgba(99,102,241,.05))">' +
        '<div style="display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;align-items:flex-start">' +
        '<div style="min-width:0">' +
        '<div style="font-weight:600;font-size:14px">' + esc(a.email || a.displayName || a.nickname || a.id) +
        ' <span style="font-size:11px;padding:2px 8px;border-radius:999px;background:rgba(168,85,247,.2);color:#d8b4fe;margin-left:6px">Grok</span></div>' +
        '<div style="font-size:12px;color:var(--text-dim,#8b93a9);margin-top:4px">' +
        esc(a.displayName || a.nickname || '') +
        (a.id ? ' · <code style="font-size:11px">' + esc(a.id.slice(0, 8)) + '…</code>' : '') +
        '</div>' +
        '<div style="font-size:12px;color:var(--text-dim,#8b93a9);margin-top:6px;display:flex;gap:12px;flex-wrap:wrap">' +
        '<span>status: <b style="color:' + statusColor + '">' + esc(statusText) + '</b></span>' +
        '<span>tokens: <b>' + esc(a.totalTokens || 0) + '</b></span>' +
        '<span>credits: <b>' + esc(a.totalCredits || 0) + '</b></span>' +
        '<span>reqs: <b>' + esc(a.requestCount || 0) + '</b></span>' +
        '<span>exp: ' + esc(fmtTime(a.expiresAt)) + '</span>' +
        '</div></div>' +
        '<div style="display:flex;gap:6px;flex-wrap:wrap">' +
        '<button type="button" class="btn btn-secondary btn-xs" data-grok-act="toggle" data-id="' + esc(a.id) + '" data-enabled="' + (a.enabled ? '1' : '0') + '">' +
        (a.enabled ? 'Disable' : 'Enable') + '</button>' +
        '<button type="button" class="btn btn-danger btn-xs" data-grok-act="delete" data-id="' + esc(a.id) + '">Delete</button>' +
        '</div></div></div>'
      );
    }).join('');
    el.innerHTML = html;
  }

  async function loadGrokAccounts() {
    ensureSection();
    var el = document.getElementById(LIST_ID);
    var pw = getPassword();
    if (!pw) {
      renderEmpty('Chưa đăng nhập admin — không tải được Grok accounts.');
      return;
    }
    if (el) el.innerHTML = '<div style="color:var(--text-dim,#8b93a9);font-size:13px">Đang tải…</div>';
    try {
      var res = await fetch('/admin/api/grok-accounts', {
        headers: { 'X-Admin-Password': pw }
      });
      var d = {};
      try { d = await res.json(); } catch (e) { }
      if (!res.ok) {
        renderEmpty(d.error || ('HTTP ' + res.status));
        return;
      }
      renderList(d.accounts || []);
    } catch (e) {
      renderEmpty('Lỗi mạng khi tải Grok accounts.');
    }
  }

  async function deleteGrok(id) {
    var pw = getPassword();
    if (!pw || !id) return;
    if (!confirm('Xóa tài khoản Grok này?')) return;
    try {
      var res = await fetch('/admin/api/grok-accounts/' + encodeURIComponent(id), {
        method: 'DELETE',
        headers: { 'X-Admin-Password': pw }
      });
      var d = {};
      try { d = await res.json(); } catch (e) { }
      if (!res.ok) {
        alert(d.error || ('HTTP ' + res.status));
        return;
      }
      loadGrokAccounts();
    } catch (e) {
      alert('Lỗi mạng');
    }
  }

  // Toggle: delete+re-add is heavy; use PUT if available — currently only DELETE.
  // Minimal: call delete is enough for operators; enable/disable needs API.
  // Add quick disable via re-import enabled:false using POST upsert after GET... skip for now.
  // Implement enable/disable by POSTing full row if we fetch first — overkill.
  // Backend has SetGrokAccountEnabled but no HTTP route yet. Wire a small route? 
  // For UI completeness, wire DELETE only and toggle via new endpoint inline patch below.

  async function setEnabled(id, enabled) {
    var pw = getPassword();
    if (!pw || !id) return;
    try {
      var res = await fetch('/admin/api/grok-accounts/' + encodeURIComponent(id) + '/enabled', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Admin-Password': pw
        },
        body: JSON.stringify({ enabled: !!enabled })
      });
      var d = {};
      try { d = await res.json(); } catch (e) { }
      if (!res.ok) {
        // fallback message if route missing
        alert(d.error || ('HTTP ' + res.status + ' — thử Delete rồi import lại'));
        return;
      }
      loadGrokAccounts();
    } catch (e) {
      alert('Lỗi mạng');
    }
  }

  function onClick(e) {
    var btn = e.target.closest ? e.target.closest('[data-grok-act]') : null;
    if (!btn) return;
    var act = btn.getAttribute('data-grok-act');
    var id = btn.getAttribute('data-id');
    if (act === 'delete') deleteGrok(id);
    if (act === 'toggle') {
      var en = btn.getAttribute('data-enabled') === '1';
      setEnabled(id, !en);
    }
  }

  function init() {
    ensureSection();
    document.addEventListener('click', onClick);

    // load when Accounts tab visible / after login
    var obs = new MutationObserver(function () {
      ensureSection();
    });
    obs.observe(document.body, { childList: true, subtree: true });

    // initial + periodic soft refresh when tabAccounts active
    loadGrokAccounts();
    setInterval(function () {
      var tab = document.getElementById('tabAccounts');
      if (tab && !tab.classList.contains('hidden')) {
        // avoid hammering: only ensure section, user can Refresh
        ensureSection();
      }
    }, 5000);

    // hook loadAccounts if present so after Kiro list reload we also refresh Grok
    try {
      if (typeof window.loadAccounts === 'function') {
        var orig = window.loadAccounts;
        window.loadAccounts = async function () {
          var r = await orig.apply(this, arguments);
          loadGrokAccounts();
          return r;
        };
      }
    } catch (e) { }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  // expose for import script
  window.loadGrokAccounts = loadGrokAccounts;
})();
