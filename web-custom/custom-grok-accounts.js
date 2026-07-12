/*
 * custom-grok-accounts.js
 * Grok CLI account list + detail popup (proxy, machineId...).
 * Theme-aware (light/dark via CSS variables).
 * Mount + inject by entrypoint.sh.
 */
(function () {
  'use strict';

  var SECTION_ID = 'grokAccountsSection';
  var LIST_ID = 'grokAccountsList';
  var MODAL_ID = 'grokDetailModal';
  var cache = {};

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
    try { return new Date(unix * 1000).toLocaleString(); }
    catch (e) { return String(unix); }
  }

  function quotaBadge(a) {
    var st = (a && a.quotaStatus) || '';
    var rem = a && a.quotaRemaining;
    var color = 'var(--muted-foreground,#525252)';
    var label = 'quota: —';
    if (st === 'ok' || st === 'active') {
      color = 'var(--success,#0f766e)';
      if (rem != null && rem >= 0) label = 'còn ~' + rem + (a.quotaLimit ? ('/' + a.quotaLimit) : '');
      else label = st === 'active' ? 'sẵn sàng' : 'quota OK';
    } else if (st === 'exhausted') {
      color = 'var(--destructive,#e54b4f)';
      label = 'HẾT quota';
    } else if (st === 'no_access') {
      // Transient chat-access denial (xAI). Health-checker auto-retries.
      color = 'var(--warning,#d97706)';
      label = 'tạm khoá · tự thử lại';
    } else if (st === 'error') {
      color = 'var(--destructive,#e54b4f)';
      label = 'quota lỗi';
    } else if (st === 'unknown') {
      color = 'var(--muted-foreground,#525252)';
      label = 'quota ?';
    }
    var tip = (a && a.quotaMessage) || '';
    if (a && a.quotaCheckedAt) {
      tip = (tip ? tip + ' · ' : '') + 'check ' + fmtTime(a.quotaCheckedAt);
    }
    return '<span title="' + esc(tip) + '" style="color:' + color + ';font-weight:600">' + esc(label) + '</span>';
  }

  function toast(msg, ok) {
    if (window.toast) {
      try { window.toast(msg, ok ? 'success' : 'error'); return; } catch (e) {}
    }
    alert(msg);
  }

  function genUUID() {
    if (crypto && crypto.randomUUID) return crypto.randomUUID();
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function (c) {
      var r = Math.random() * 16 | 0;
      var v = c === 'x' ? r : (r & 0x3 | 0x8);
      return v.toString(16);
    });
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
      '<span class="card-title"><i class="fa-solid fa-robot" style="color:var(--brand-purple,#9148ff);margin-right:8px"></i>Grok CLI (Grok Build)</span>' +
      '<div class="card-actions" style="display:flex;gap:8px;flex-wrap:wrap">' +
      '<button type="button" class="btn btn-outline btn-sm" id="grokRefreshBtn">' +
      '<i class="fa-solid fa-arrows-rotate"></i> <span class="btn-text">Refresh</span></button>' +
      '<button type="button" class="btn btn-outline btn-sm" id="grokQuotaAllBtn" title="Probe quota for all Grok accounts">' +
      '<i class="fa-solid fa-gauge-high"></i> <span class="btn-text">Check quota</span></button>' +
      '</div></div>' +
      '<p style="font-size:12.5px;color:var(--muted-foreground,#525252);margin:0 0 12px;line-height:1.5">' +
      'Pool <code>grokAccounts[]</code> riêng — không hiện trong danh sách Kiro. ' +
      'Bấm <b>avatar / hàng tài khoản / Chi tiết</b> để mở popup (proxy, machineId…). Import: <b>Add Account → Import Grok CLI</b>.' +
      '</p>' +
      '<div id="' + LIST_ID + '"><div style="color:var(--muted-foreground,#525252);font-size:13px">Đang tải…</div></div>';

    tab.appendChild(card);
    var btn = document.getElementById('grokRefreshBtn');
    if (btn) btn.addEventListener('click', loadGrokAccounts);
    var qb = document.getElementById('grokQuotaAllBtn');
    if (qb) qb.addEventListener('click', checkAllQuota);
    return card;
  }

  function ensureModal() {
    if (document.getElementById(MODAL_ID)) return;
    var wrap = document.createElement('div');
    wrap.id = MODAL_ID;
    wrap.className = 'grok-detail-overlay';
    wrap.style.cssText =
      'display:none;position:fixed;left:0;top:0;right:0;bottom:0;z-index:2147483000;' +
      'background:var(--modal-overlay,rgba(0,0,0,.42));align-items:center;justify-content:center;padding:16px;pointer-events:auto;';
    wrap.innerHTML =
      '<div role="dialog" aria-modal="true" class="grok-detail-panel" style="' +
      'width:min(560px,100%);max-height:90vh;overflow:auto;' +
      'background:var(--card,#ffffff);color:var(--card-foreground,#000000);' +
      'border:1px solid var(--border,#e4e4e4);border-radius:var(--radius-xl,12px);' +
      'box-shadow:0 16px 48px rgba(0,0,0,.12);padding:0">' +
      '<div style="display:flex;align-items:center;justify-content:space-between;padding:16px 18px;' +
      'border-bottom:1px solid var(--border,#e4e4e4);background:var(--muted,#f5f5f5)">' +
      '<div style="display:flex;align-items:center;gap:12px">' +
      '<div id="gdmAvatar" style="width:44px;height:44px;border-radius:50%;display:flex;align-items:center;justify-content:center;' +
      'background:var(--brand-purple,#9148ff);color:#fff;font-weight:700;font-size:16px"></div>' +
      '<div><div id="gdmTitle" style="font-weight:700;font-size:15px;color:var(--foreground,#000)">Grok account</div>' +
      '<div id="gdmSub" style="font-size:12px;color:var(--muted-foreground,#525252)"></div></div></div>' +
      '<button type="button" id="gdmClose" class="btn btn-secondary btn-sm" style="border-radius:999px">✕</button>' +
      '</div>' +
      '<div style="padding:16px 18px;display:flex;flex-direction:column;gap:14px;background:var(--card,#fff);color:var(--card-foreground,#000)">' +
      '<div style="display:grid;grid-template-columns:1fr 1fr;gap:10px;font-size:12.5px" id="gdmMeta"></div>' +
      '<div>' +
      '<label style="display:block;font-size:12px;color:var(--muted-foreground,#525252);margin-bottom:6px">Nickname</label>' +
      '<input id="gdmNickname" type="text" class="input" style="width:100%" placeholder="Optional label" />' +
      '</div>' +
      '<div>' +
      '<label style="display:block;font-size:12px;color:var(--muted-foreground,#525252);margin-bottom:6px">Display name</label>' +
      '<input id="gdmDisplayName" type="text" class="input" style="width:100%" placeholder="Optional" />' +
      '</div>' +
      '<div>' +
      '<label style="display:block;font-size:12px;color:var(--muted-foreground,#525252);margin-bottom:6px">Machine ID (UUID)</label>' +
      '<div style="display:flex;gap:8px;flex-wrap:wrap">' +
      '<input id="gdmMachineId" type="text" class="input" style="flex:1;min-width:180px" placeholder="UUID" />' +
      '<button type="button" class="btn btn-outline btn-sm" id="gdmGenMid">Generate</button>' +
      '</div>' +
      '<p style="margin:6px 0 0;font-size:11.5px;color:var(--muted-foreground,#525252)">Gửi kèm header <code>x-machine-id</code> / <code>x-grok-machine-id</code>.</p>' +
      '</div>' +
      '<div>' +
      '<label style="display:block;font-size:12px;color:var(--muted-foreground,#525252);margin-bottom:6px">Proxy URL (per account)</label>' +
      '<input id="gdmProxy" type="text" class="input" style="width:100%" placeholder="socks5://user:pass@host:port" />' +
      '<p style="margin:6px 0 0;font-size:11.5px;color:var(--muted-foreground,#525252)">http:// · https:// · socks5:// · socks5h:// — để trống = không proxy riêng.</p>' +
      '</div>' +
      '<div id="gdmMsg" style="font-size:12.5px;min-height:18px;color:var(--muted-foreground,#525252)"></div>' +
      '</div>' +
      '<div style="display:flex;justify-content:flex-end;gap:8px;padding:12px 18px;border-top:1px solid var(--border,#e4e4e4);' +
      'background:var(--muted,#f5f5f5);flex-wrap:wrap">' +
      '<button type="button" class="btn btn-secondary btn-sm" id="gdmCancel">Đóng</button>' +
      '<button type="button" class="btn btn-primary btn-sm" id="gdmSave">Lưu</button>' +
      '</div></div>';
    document.body.appendChild(wrap);

    wrap.addEventListener('click', function (e) {
      if (e.target === wrap) closeDetail();
    });
    document.getElementById('gdmClose').addEventListener('click', closeDetail);
    document.getElementById('gdmCancel').addEventListener('click', closeDetail);
    document.getElementById('gdmGenMid').addEventListener('click', function () {
      document.getElementById('gdmMachineId').value = genUUID();
    });
    document.getElementById('gdmSave').addEventListener('click', saveDetail);
  }

  function closeDetail() {
    var m = document.getElementById(MODAL_ID);
    if (m) m.style.setProperty('display', 'none', 'important');
  }

  function metaCell(label, value, extra) {
    return '<div style="padding:10px;border:1px solid var(--border,#e4e4e4);border-radius:10px;background:var(--muted,#f5f5f5)">' +
      '<span style="color:var(--muted-foreground,#525252)">' + esc(label) + '</span><br>' +
      '<b' + (extra || '') + '>' + value + '</b></div>';
  }

  async function openDetail(id) {
    ensureModal();
    id = String(id || '').trim();
    if (!id) return;
    var a = cache[id];
    if (!a) {
      try {
        var pw = getPassword();
        var res = await fetch('/admin/api/grok-accounts/' + encodeURIComponent(id), {
          headers: { 'X-Admin-Password': pw }
        });
        if (res.ok) {
          a = await res.json();
          cache[id] = a;
        }
      } catch (e) {}
    }
    if (!a) {
      toast('Không tìm thấy account', false);
      return;
    }
    var modal = document.getElementById(MODAL_ID);
    if (!modal) {
      toast('Không tạo được popup', false);
      return;
    }
    modal.dataset.id = id;
    var name = a.email || a.displayName || a.nickname || id;
    var initial = String(name).trim().charAt(0).toUpperCase() || 'G';
    document.getElementById('gdmAvatar').textContent = initial;
    document.getElementById('gdmTitle').textContent = name;
    document.getElementById('gdmSub').textContent = (a.id || '').slice(0, 8) + '… · ' + (a.enabled ? 'enabled' : 'disabled');
    document.getElementById('gdmMeta').innerHTML =
      metaCell('Tokens', esc(a.totalTokens || 0)) +
      metaCell('Credits', esc(Number(a.totalCredits || 0).toFixed(2))) +
      metaCell('Requests', esc(a.requestCount || 0)) +
      metaCell('Token exp', esc(fmtTime(a.expiresAt))) +
      metaCell('User ID', esc(a.userId || '—'), ' style="font-size:11px;word-break:break-all"') +
      metaCell('Last used', esc(fmtTime(a.lastUsed))) +
      metaCell('Quota', esc((a.quotaStatus || '—') + (a.quotaRemaining != null && a.quotaRemaining >= 0 ? (' · còn ' + a.quotaRemaining) : '')), '') +
      metaCell('Quota check', esc((a.quotaMessage || '—') + (a.quotaCheckedAt ? (' · ' + fmtTime(a.quotaCheckedAt)) : '')));
    document.getElementById('gdmNickname').value = a.nickname || '';
    document.getElementById('gdmDisplayName').value = a.displayName || '';
    document.getElementById('gdmMachineId').value = a.machineId || '';
    document.getElementById('gdmProxy').value = a.proxyURL || '';
    document.getElementById('gdmMsg').textContent = '';
    document.getElementById('gdmMsg').style.color = 'var(--muted-foreground,#525252)';
    modal.style.setProperty('display', 'flex', 'important');
    modal.style.setProperty('z-index', '2147483000', 'important');
    modal.style.setProperty('visibility', 'visible', 'important');
    modal.style.setProperty('opacity', '1', 'important');
    try { modal.scrollTop = 0; } catch (e) {}
  }

  async function saveDetail() {
    var modal = document.getElementById(MODAL_ID);
    var id = modal && modal.dataset.id;
    var pw = getPassword();
    if (!id || !pw) return;
    var body = {
      nickname: document.getElementById('gdmNickname').value.trim(),
      displayName: document.getElementById('gdmDisplayName').value.trim(),
      machineId: document.getElementById('gdmMachineId').value.trim(),
      proxyURL: document.getElementById('gdmProxy').value.trim()
    };
    var msg = document.getElementById('gdmMsg');
    msg.style.color = 'var(--muted-foreground,#525252)';
    msg.textContent = 'Đang lưu…';
    try {
      var res = await fetch('/admin/api/grok-accounts/' + encodeURIComponent(id), {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'X-Admin-Password': pw
        },
        body: JSON.stringify(body)
      });
      var d = {};
      try { d = await res.json(); } catch (e) {}
      if (!res.ok) {
        msg.style.color = 'var(--destructive,#e54b4f)';
        msg.textContent = d.error || ('HTTP ' + res.status);
        return;
      }
      msg.style.color = 'var(--success,#0f766e)';
      msg.textContent = 'Đã lưu';
      toast('Đã cập nhật Grok account', true);
      await loadGrokAccounts();
      setTimeout(closeDetail, 400);
    } catch (e) {
      msg.style.color = 'var(--destructive,#e54b4f)';
      msg.textContent = 'Lỗi mạng';
    }
  }

  function renderEmpty(msg) {
    var el = document.getElementById(LIST_ID);
    if (!el) return;
    el.innerHTML = '<div style="padding:14px;border:1px dashed color-mix(in oklab, var(--brand-purple,#9148ff) 35%, var(--border,#e4e4e4));' +
      'border-radius:12px;color:var(--muted-foreground,#525252);font-size:13px;background:var(--card,#fff)">' + esc(msg) + '</div>';
  }

  function avatarColor(s) {
    var h = 0;
    String(s || '').split('').forEach(function (ch) { h = (h * 31 + ch.charCodeAt(0)) >>> 0; });
    var hue = 250 + (h % 40);
    return 'hsl(' + hue + ' 62% 48%)';
  }

  function renderList(accounts) {
    var el = document.getElementById(LIST_ID);
    if (!el) return;
    cache = {};
    if (!accounts || !accounts.length) {
      renderEmpty('Chưa có tài khoản Grok. Bấm Add Account → Import Grok CLI.');
      return;
    }

    var html = accounts.map(function (a) {
      cache[a.id] = a;
      var statusColor = a.enabled ? 'var(--success,#0f766e)' : 'var(--destructive,#e54b4f)';
      var statusText = a.enabled ? 'enabled' : 'disabled';
      if (a.banStatus) statusText = a.banStatus;
      var label = a.email || a.displayName || a.nickname || a.id;
      var initial = String(label).trim().charAt(0).toUpperCase() || 'G';
      var midHint = a.machineId ? ('mid ' + String(a.machineId).slice(0, 8) + '…') : 'no machineId';
      var pxHint = a.proxyURL ? 'proxy ✓' : 'no proxy';
      return (
        '<div class="account-item grok-account-row" data-grok-id="' + esc(a.id) + '" style="' +
        'border:1px solid color-mix(in oklab, var(--brand-purple,#9148ff) 28%, var(--border,#e4e4e4));' +
        'border-radius:14px;padding:14px 16px;margin-bottom:10px;' +
        'background:color-mix(in oklab, var(--brand-purple,#9148ff) 6%, var(--card,#fff));' +
        'cursor:pointer;color:var(--card-foreground,#000)">' +
        '<div style="display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;align-items:flex-start">' +
        '<div style="display:flex;gap:12px;min-width:0;flex:1">' +
        '<div class="grok-avatar" data-grok-open="' + esc(a.id) + '" title="Mở chi tiết" style="' +
        'width:44px;height:44px;border-radius:50%;flex-shrink:0;display:flex;align-items:center;justify-content:center;' +
        'background:' + avatarColor(label) + ';color:#fff;font-weight:700;font-size:16px;cursor:pointer;' +
        'box-shadow:0 0 0 2px color-mix(in oklab, var(--brand-purple,#9148ff) 30%, transparent)">' + esc(initial) + '</div>' +
        '<div style="min-width:0">' +
        '<div style="font-weight:600;font-size:14px;color:var(--foreground,#000)">' + esc(label) +
        ' <span style="font-size:11px;padding:2px 8px;border-radius:999px;' +
        'background:color-mix(in oklab, var(--brand-purple,#9148ff) 14%, var(--muted,#f5f5f5));' +
        'color:var(--brand-purple,#9148ff);margin-left:6px">Grok</span></div>' +
        '<div style="font-size:12px;color:var(--muted-foreground,#525252);margin-top:4px">' +
        esc(a.displayName || a.nickname || '') +
        (a.id ? ' · <code style="font-size:11px">' + esc(a.id.slice(0, 8)) + '…</code>' : '') +
        '</div>' +
        '<div style="font-size:12px;color:var(--muted-foreground,#525252);margin-top:6px;display:flex;gap:12px;flex-wrap:wrap">' +
        '<span>status: <b style="color:' + statusColor + '">' + esc(statusText) + '</b></span>' +
        '<span>tokens: <b style="color:var(--foreground,#000)">' + esc(a.totalTokens || 0) + '</b></span>' +
        '<span>credits: <b style="color:var(--foreground,#000)">' + esc(a.totalCredits || 0) + '</b></span>' +
        '<span>reqs: <b style="color:var(--foreground,#000)">' + esc(a.requestCount || 0) + '</b></span>' +
        '<span>' + esc(midHint) + '</span>' +
        '<span>' + esc(pxHint) + '</span>' +
        '<span>' + quotaBadge(a) + '</span>' +
        '<span>exp: ' + esc(fmtTime(a.expiresAt)) + '</span>' +
        '</div></div></div>' +
        '<div class="grok-actions" style="display:flex;gap:6px;flex-wrap:wrap">' +
        '<button type="button" class="btn btn-outline btn-xs" data-grok-act="detail" data-id="' + esc(a.id) + '">Chi tiết</button>' +
        '<button type="button" class="btn btn-outline btn-xs" data-grok-act="test" data-id="' + esc(a.id) + '">Test</button>' +
        '<button type="button" class="btn btn-outline btn-xs" data-grok-act="quota" data-id="' + esc(a.id) + '">Check quota</button>' +
        '<button type="button" class="btn btn-secondary btn-xs" data-grok-act="toggle" data-id="' + esc(a.id) + '" data-enabled="' + (a.enabled ? '1' : '0') + '">' +
        (a.enabled ? 'Disable' : 'Enable') + '</button>' +
        '<button type="button" class="btn btn-danger btn-xs" data-grok-act="delete" data-id="' + esc(a.id) + '">Delete</button>' +
        '</div></div></div>'
      );
    }).join('');
    el.innerHTML = html;
  }

  async function testOneGrok(id) {
    var pw = getPassword();
    if (!pw || !id) return;
    toast('Đang test tài khoản…', true);
    try {
      var res = await fetch('/admin/api/grok-accounts/' + encodeURIComponent(id) + '/test', {
        method: 'POST',
        headers: { 'X-Admin-Password': pw }
      });
      var d = {};
      try { d = await res.json(); } catch (e) {}
      var ok = d && d.ok === true;
      var msg = ok
        ? ('OK · ' + (d.reply || 'hello') + (d.ms ? (' · ' + d.ms + 'ms') : ''))
        : (d.error || d.status || ('HTTP ' + res.status));
      toast(msg, ok);
      loadGrokAccounts();
    } catch (e) {
      toast('Test lỗi: ' + (e && e.message || e), false);
    }
  }

  async function checkOneQuota(id) {
    var pw = getPassword();
    if (!pw || !id) return;
    toast('Đang check quota…', true);
    try {
      var res = await fetch('/admin/api/grok-accounts/' + encodeURIComponent(id) + '/quota', {
        method: 'POST',
        headers: { 'X-Admin-Password': pw }
      });
      var d = {};
      try { d = await res.json(); } catch (e) {}
      if (!res.ok && !d.quotaStatus) {
        toast(d.error || ('HTTP ' + res.status), false);
        return;
      }
      var st = d.quotaStatus || 'unknown';
      var msg = st;
      if (d.quotaRemaining != null && d.quotaRemaining >= 0) msg += ' · còn ' + d.quotaRemaining;
      if (d.quotaMessage) msg += ' — ' + d.quotaMessage;
      toast(msg, st === 'ok' || st === 'unknown');
      await loadGrokAccounts();
    } catch (e) {
      toast('Lỗi mạng khi check quota', false);
    }
  }

  async function checkAllQuota() {
    var pw = getPassword();
    if (!pw) return;
    var btn = document.getElementById('grokQuotaAllBtn');
    if (btn) btn.disabled = true;
    toast('Đang check quota tất cả Grok…', true);
    try {
      var res = await fetch('/admin/api/grok-accounts/quota/refresh', {
        method: 'POST',
        headers: { 'X-Admin-Password': pw }
      });
      var d = {};
      try { d = await res.json(); } catch (e) {}
      if (!res.ok) {
        toast(d.error || ('HTTP ' + res.status), false);
        return;
      }
      var ok = 0, ex = 0, er = 0;
      (d.results || []).forEach(function (r) {
        if (r.quotaStatus === 'ok') ok++;
        else if (r.quotaStatus === 'exhausted') ex++;
        else er++;
      });
      toast('Quota: OK=' + ok + ' · Hết=' + ex + ' · Khác=' + er, ex === 0);
      await loadGrokAccounts();
    } catch (e) {
      toast('Lỗi mạng', false);
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  async function loadGrokAccounts() {
    ensureSection();
    ensureModal();
    var el = document.getElementById(LIST_ID);
    var pw = getPassword();
    if (!pw) {
      renderEmpty('Chưa đăng nhập admin — không tải được Grok accounts.');
      return;
    }
    if (el) el.innerHTML = '<div style="color:var(--muted-foreground,#525252);font-size:13px">Đang tải…</div>';
    try {
      var res = await fetch('/admin/api/grok-accounts', {
        headers: { 'X-Admin-Password': pw }
      });
      var d = {};
      try { d = await res.json(); } catch (e) {}
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
      try { d = await res.json(); } catch (e) {}
      if (!res.ok) {
        alert(d.error || ('HTTP ' + res.status));
        return;
      }
      loadGrokAccounts();
    } catch (e) {
      alert('Lỗi mạng');
    }
  }

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
      try { d = await res.json(); } catch (e) {}
      if (!res.ok) {
        alert(d.error || ('HTTP ' + res.status));
        return;
      }
      loadGrokAccounts();
    } catch (e) {
      alert('Lỗi mạng');
    }
  }

  function onClick(e) {
    var t = e.target;
    if (!t || !t.closest) return;

    var btn = t.closest('[data-grok-act]');
    if (btn && btn.getAttribute('data-grok-act')) {
      var act = btn.getAttribute('data-grok-act');
      var id = btn.getAttribute('data-id');
      e.preventDefault();
      e.stopPropagation();
      if (act === 'delete') { deleteGrok(id); return; }
      if (act === 'toggle') {
        var en = btn.getAttribute('data-enabled') === '1';
        setEnabled(id, !en);
        return;
      }
      if (act === 'detail') { openDetail(id); return; }
      if (act === 'test') { testOneGrok(id); return; }
      if (act === 'quota') { checkOneQuota(id); return; }
      return;
    }

    var open = t.closest('[data-grok-open]');
    if (open) {
      e.preventDefault();
      e.stopPropagation();
      openDetail(open.getAttribute('data-grok-open'));
      return;
    }

    if (t.closest('.grok-actions')) return;
    var row = t.closest('.grok-account-row');
    if (row && row.getAttribute('data-grok-id')) {
      e.preventDefault();
      e.stopPropagation();
      openDetail(row.getAttribute('data-grok-id'));
    }
  }

  function init() {
    ensureSection();
    ensureModal();
    document.addEventListener('click', onClick, true);

    var obs = new MutationObserver(function () { ensureSection(); });
    obs.observe(document.body, { childList: true, subtree: true });

    loadGrokAccounts();
    setInterval(function () {
      var tab = document.getElementById('tabAccounts');
      if (tab && !tab.classList.contains('hidden')) ensureSection();
    }, 5000);

    try {
      if (typeof window.loadAccounts === 'function') {
        var orig = window.loadAccounts;
        window.loadAccounts = async function () {
          var r = await orig.apply(this, arguments);
          loadGrokAccounts();
          return r;
        };
      }
    } catch (e) {}
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  window.loadGrokAccounts = loadGrokAccounts;
  window.openGrokAccountDetail = openDetail;
})();
