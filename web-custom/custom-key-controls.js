/*
 * custom-key-controls.js
 * Them nut "Tam dung / Tiep tuc" dem gio het han va "+ Gio" cho tung API key
 * trong trang admin. Dong bo dinh dang marker voi keyadmin (bot Telegram):
 *   #exp=<unix>     -> con han, het han vao thoi diem unix (giay)
 *   #pause=<giay>   -> dong ho tam dung, con lai bay nhieu giay
 *   #exp=YYYY-MM-DD -> dinh dang cu (van doc duoc, het han cuoi ngay do)
 *   (khong marker)  -> vinh vien
 *
 * Nut goi TRUC TIEP admin API cua kiro-go (same-origin, kem X-Admin-Password) de
 * PUT lai ten key -> khong phu thuoc keyadmin/CORS. Doc lap voi app.js.
 */
(function () {
  'use strict';

  var CTRL_ATTR = 'data-ctrl-processed';

  function adminPassword() {
    return sessionStorage.getItem('admin_password') ||
           localStorage.getItem('admin_password') || '';
  }

  // ---- Marker han dung (khop keyadmin/main.go) ----
  var pauseRe = /#pause=(\d+)/;
  var expDateRe = /#exp=(\d{4}-\d{2}-\d{2})/;
  var expUnixRe = /#exp=(\d+)/;
  var stripRe = /\s*#(?:exp=\d{4}-\d{2}-\d{2}|exp=\d+|pause=\d+)/g;

  function stripMarkers(name) {
    return (name || '').replace(stripRe, '').trim();
  }

  // Tra ve { mode: 'none'|'active'|'paused', expMs, secondsLeft }
  function parseExpiry(name, nowMs) {
    var m = pauseRe.exec(name || '');
    if (m) return { mode: 'paused', secondsLeft: parseInt(m[1], 10) };
    m = expDateRe.exec(name || '');
    if (m) {
      var p = m[1].split('-');
      var expMs = new Date(+p[0], +p[1] - 1, +p[2], 23, 59, 59).getTime();
      return { mode: 'active', expMs: expMs, secondsLeft: Math.floor((expMs - nowMs) / 1000) };
    }
    m = expUnixRe.exec(name || '');
    if (m) {
      var e = parseInt(m[1], 10) * 1000;
      return { mode: 'active', expMs: e, secondsLeft: Math.floor((e - nowMs) / 1000) };
    }
    return { mode: 'none' };
  }

  function withExpUnix(name, expSec) {
    return (stripMarkers(name) + ' #exp=' + expSec).trim();
  }
  function withPause(name, seconds) {
    if (seconds < 0) seconds = 0;
    return (stripMarkers(name) + ' #pause=' + seconds).trim();
  }

  // ---- Goi admin API cua kiro-go ----
  async function fetchName(id) {
    var res = await fetch('/admin/api/api-keys', {
      headers: { 'X-Admin-Password': adminPassword() }
    });
    var data = await res.json().catch(function () { return {}; });
    var keys = data.apiKeys || (Array.isArray(data) ? data : []);
    for (var i = 0; i < keys.length; i++) {
      if (keys[i].id === id) return keys[i].name || '';
    }
    return null;
  }

  async function putName(id, newName) {
    var res = await fetch('/admin/api/api-keys/' + id, {
      method: 'PUT',
      headers: { 'X-Admin-Password': adminPassword(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: newName })
    });
    return res.ok;
  }

  // ---- Hanh dong ----
  async function doPauseToggle(id, btn) {
    var name = await fetchName(id);
    if (name === null) { alert('Khong tim thay key.'); return; }
    var st = parseExpiry(name, Date.now());
    var newName;
    if (st.mode === 'paused') {
      newName = withExpUnix(name, Math.floor(Date.now() / 1000) + st.secondsLeft);
    } else if (st.mode === 'active') {
      newName = withPause(name, st.secondsLeft > 0 ? st.secondsLeft : 0);
    } else {
      alert('Key nay vinh vien (khong co han) - khong co gi de tam dung. Dung "+ Gio" de dat han truoc.');
      return;
    }
    btn.disabled = true;
    if (await putName(id, newName)) location.reload();
    else { btn.disabled = false; alert('Thao tac that bai.'); }
  }

  async function doAddHours(id, btn) {
    var raw = prompt('Cong them bao nhieu gio? (nhap so am de tru bot)', '24');
    if (raw === null) return;
    var hours = parseFloat(raw);
    if (isNaN(hours) || hours === 0) return;
    var addSec = Math.round(hours * 3600);

    var name = await fetchName(id);
    if (name === null) { alert('Khong tim thay key.'); return; }
    var now = Math.floor(Date.now() / 1000);
    var st = parseExpiry(name, Date.now());
    var newName;
    if (st.mode === 'paused') {
      newName = withPause(name, st.secondsLeft + addSec);
    } else if (st.mode === 'active') {
      var exp = Math.floor(st.expMs / 1000) + addSec;
      newName = withExpUnix(name, exp < now ? now : exp);
    } else {
      var e = now + addSec;
      newName = withExpUnix(name, e < now ? now : e);
    }
    btn.disabled = true;
    if (await putName(id, newName)) location.reload();
    else { btn.disabled = false; alert('Thao tac that bai.'); }
  }

  // ---- Render nut vao tung card ----
  function mkBtn(label, title, bg) {
    var b = document.createElement('button');
    b.type = 'button';
    b.textContent = label;
    b.title = title;
    b.style.cssText = 'margin-right:6px;margin-top:4px;padding:2px 10px;border:0;border-radius:6px;' +
      'font-size:12px;cursor:pointer;color:#fff;background:' + bg + ';';
    return b;
  }

  function processCards() {
    var cards = document.querySelectorAll('#apiKeysList .card[data-apikey-id]');
    cards.forEach(function (card) {
      if (card.getAttribute(CTRL_ATTR) === '1') return;
      card.setAttribute(CTRL_ATTR, '1');
      var id = card.getAttribute('data-apikey-id');
      if (!id) return;

      var row = document.createElement('div');
      row.setAttribute('data-ctrl-row', '1');
      row.style.cssText = 'margin-top:6px;';

      var pauseBtn = mkBtn('Tam dung / Tiep tuc', 'Tam dung hoac tiep tuc dem gio het han', '#6366f1');
      pauseBtn.addEventListener('click', function () { doPauseToggle(id, pauseBtn); });

      var addBtn = mkBtn('+ Gio', 'Cong them (hoac tru) so gio han dung', '#0ea5e9');
      addBtn.addEventListener('click', function () { doAddHours(id, addBtn); });

      row.appendChild(pauseBtn);
      row.appendChild(addBtn);

      var grid = card.querySelector('div[style*="grid"]');
      if (grid && grid.parentNode) grid.parentNode.insertBefore(row, grid.nextSibling);
      else card.appendChild(row);
    });
  }

  function init() {
    var observer = new MutationObserver(function () {
      if (window.__ctrlTimer) clearTimeout(window.__ctrlTimer);
      window.__ctrlTimer = setTimeout(processCards, 80);
    });
    observer.observe(document.body, { childList: true, subtree: true });
    processCards();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
