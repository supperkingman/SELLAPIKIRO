/*
 * custom-key-dashboard.js
 * Them THANH BO DEM + FILTER len dau tab "API Keys" trong trang admin.
 *
 * - Bo dem: tong / bat / tat / con han / tam dung / het han / vinh vien.
 * - Filter (loc ngay tren trinh duyet, khong tai lai trang):
 *     + trang thai bat/tat
 *     + trang thai han (con han / tam dung / het han / vinh vien)
 *     + tim theo ten
 * - Chip bo dem bam duoc -> dat nhanh filter tuong ung.
 *
 * Lay du lieu THANG tu admin API (ten day du + marker) roi map theo data-apikey-id
 * de an/hien card -> chinh xac, khong phu thuoc cac script custom khac da don marker.
 * Doc lap voi app.js. Duoc entrypoint.sh chen tu dong (self-healing).
 */
(function () {
  'use strict';

  var BAR_ID = 'keyDashboardBar';

  function adminPassword() {
    return sessionStorage.getItem('admin_password') ||
           localStorage.getItem('admin_password') || '';
  }

  // ---- Marker han (khop keyadmin + custom-key-controls) ----
  var pauseRe = /#pause=(\d+)/;
  var expDateRe = /#exp=(\d{4}-\d{2}-\d{2})/;
  var expUnixRe = /#exp=(\d+)/;
  var stripRe = /\s*#(?:exp=\d{4}-\d{2}-\d{2}|exp=\d+|pause=\d+)/g;

  function stripMarkers(name) {
    return (name || '').replace(stripRe, '').trim();
  }

  // Tra ve 'active' | 'paused' | 'expired' | 'permanent'
  function expiryClass(name, nowSec) {
    var m = pauseRe.exec(name || '');
    if (m) return 'paused';
    var expSec = null;
    m = expDateRe.exec(name || '');
    if (m) {
      var p = m[1].split('-');
      expSec = Math.floor(new Date(+p[0], +p[1] - 1, +p[2], 23, 59, 59).getTime() / 1000);
    } else {
      m = expUnixRe.exec(name || '');
      if (m) expSec = parseInt(m[1], 10);
    }
    if (expSec === null) return 'permanent';
    return expSec <= nowSec ? 'expired' : 'active';
  }

  // ---- Lay du lieu tu admin API ----
  async function fetchKeys() {
    var res = await fetch('/admin/api/api-keys', {
      headers: { 'X-Admin-Password': adminPassword() }
    });
    var data = await res.json().catch(function () { return {}; });
    return data.apiKeys || (Array.isArray(data) ? data : []);
  }

  // id -> { enabled, cls, name }
  var stateById = {};

  function classify(keys) {
    var now = Math.floor(Date.now() / 1000);
    var c = { total: 0, enabled: 0, disabled: 0, active: 0, paused: 0, expired: 0, permanent: 0 };
    stateById = {};
    keys.forEach(function (k) {
      var cls = expiryClass(k.name || '', now);
      stateById[k.id] = { enabled: !!k.enabled, cls: cls, name: stripMarkers(k.name || '') };
      c.total++;
      if (k.enabled) c.enabled++; else c.disabled++;
      c[cls]++;
    });
    return c;
  }

  // ---- Dung UI ----
  function chip(label, value, filterKey, filterVal, color) {
    return '<button type="button" class="kd-chip" data-fkey="' + (filterKey || '') +
      '" data-fval="' + (filterVal || '') + '" style="border:0;border-radius:8px;cursor:pointer;' +
      'padding:6px 12px;font-size:13px;color:#fff;background:' + color + ';display:flex;gap:6px;' +
      'align-items:center;"><b class="kd-num">' + value + '</b><span style="opacity:.85">' + label + '</span></button>';
  }

  function buildBar(container) {
    if (document.getElementById(BAR_ID)) return;
    var bar = document.createElement('div');
    bar.id = BAR_ID;
    bar.style.cssText = 'margin:0 0 12px;padding:12px;border-radius:12px;' +
      'background:rgba(255,255,255,.03);border:1px solid rgba(255,255,255,.08);';
    bar.innerHTML =
      '<div id="kdCounters" style="display:flex;flex-wrap:wrap;gap:8px;margin-bottom:10px;"></div>' +
      '<div style="display:flex;flex-wrap:wrap;gap:8px;align-items:center;">' +
      '<input id="kdSearch" type="text" placeholder="Tim theo ten key..." ' +
      'style="flex:1;min-width:160px;padding:6px 10px;border-radius:8px;border:1px solid rgba(255,255,255,.15);' +
      'background:rgba(255,255,255,.04);color:inherit;font-size:13px;" />' +
      '<select id="kdEnabled" style="padding:6px 10px;border-radius:8px;border:1px solid rgba(255,255,255,.15);' +
      'background:rgba(255,255,255,.04);color:inherit;font-size:13px;">' +
      '<option value="">Bat & tat</option><option value="true">Chi bat</option><option value="false">Chi tat</option></select>' +
      '<select id="kdExpiry" style="padding:6px 10px;border-radius:8px;border:1px solid rgba(255,255,255,.15);' +
      'background:rgba(255,255,255,.04);color:inherit;font-size:13px;">' +
      '<option value="">Moi han</option><option value="active">Con han</option>' +
      '<option value="paused">Tam dung</option><option value="expired">Het han</option>' +
      '<option value="permanent">Vinh vien</option></select>' +
      '<button id="kdReset" type="button" style="padding:6px 12px;border-radius:8px;border:0;cursor:pointer;' +
      'background:#475569;color:#fff;font-size:13px;">Xoa loc</button>' +
      '<span id="kdShown" style="font-size:12px;opacity:.7;margin-left:auto;"></span>' +
      '</div>';
    container.parentNode.insertBefore(bar, container);

    // Su kien filter (client-side, khong tai lai trang).
    document.getElementById('kdSearch').addEventListener('input', applyFilter);
    document.getElementById('kdEnabled').addEventListener('change', applyFilter);
    document.getElementById('kdExpiry').addEventListener('change', applyFilter);
    document.getElementById('kdReset').addEventListener('click', function () {
      document.getElementById('kdSearch').value = '';
      document.getElementById('kdEnabled').value = '';
      document.getElementById('kdExpiry').value = '';
      applyFilter();
    });
    // Chip bam de dat filter nhanh.
    bar.addEventListener('click', function (e) {
      var btn = e.target.closest ? e.target.closest('.kd-chip') : null;
      if (!btn) return;
      var fkey = btn.getAttribute('data-fkey');
      var fval = btn.getAttribute('data-fval');
      if (fkey === 'enabled') document.getElementById('kdEnabled').value = fval;
      else if (fkey === 'expiry') document.getElementById('kdExpiry').value = fval;
      applyFilter();
    });
  }

  function renderCounters(c) {
    var el = document.getElementById('kdCounters');
    if (!el) return;
    el.innerHTML =
      chip('Tong', c.total, '', '', '#334155') +
      chip('Bat', c.enabled, 'enabled', 'true', '#16a34a') +
      chip('Tat', c.disabled, 'enabled', 'false', '#6b7280') +
      chip('Con han', c.active, 'expiry', 'active', '#0ea5e9') +
      chip('Tam dung', c.paused, 'expiry', 'paused', '#f59e0b') +
      chip('Het han', c.expired, 'expiry', 'expired', '#ef4444') +
      chip('Vinh vien', c.permanent, 'expiry', 'permanent', '#8b5cf6');
  }

  function applyFilter() {
    var q = (document.getElementById('kdSearch') || {}).value || '';
    var fEnabled = (document.getElementById('kdEnabled') || {}).value || '';
    var fExpiry = (document.getElementById('kdExpiry') || {}).value || '';
    q = q.trim().toLowerCase();

    var cards = document.querySelectorAll('#apiKeysList .card[data-apikey-id]');
    var shown = 0;
    cards.forEach(function (card) {
      var id = card.getAttribute('data-apikey-id');
      var st = stateById[id];
      var ok = true;
      if (st) {
        if (fEnabled === 'true' && !st.enabled) ok = false;
        if (fEnabled === 'false' && st.enabled) ok = false;
        if (fExpiry && st.cls !== fExpiry) ok = false;
        if (q && st.name.toLowerCase().indexOf(q) === -1) ok = false;
      }
      card.style.display = ok ? '' : 'none';
      if (ok) shown++;
    });
    var lbl = document.getElementById('kdShown');
    if (lbl) lbl.textContent = 'Hien ' + shown + '/' + cards.length + ' key';
  }

  async function refresh() {
    var list = document.getElementById('apiKeysList');
    if (!list) return;
    buildBar(list);
    try {
      var keys = await fetchKeys();
      renderCounters(classify(keys));
      applyFilter();
    } catch (e) { /* bo qua neu chua dang nhap */ }
  }

  function init() {
    var observer = new MutationObserver(function () {
      if (window.__kdTimer) clearTimeout(window.__kdTimer);
      window.__kdTimer = setTimeout(refresh, 120);
    });
    observer.observe(document.body, { childList: true, subtree: true });
    refresh();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
