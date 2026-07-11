/*
 * custom-key-expiry-display.js
 * Hien thi NGAY HET HAN duoi thong tin moi API key trong trang admin.
 *
 * Han su dung duoc ma hoa trong ten key dang "#exp=YYYY-MM-DD" (do custom-bulk-keys.js
 * them khi tao). Script nay:
 *   - Theo doi danh sach key (#apiKeysList) render lai.
 *   - Voi moi card key: doc ten, tach marker #exp, chen 1 dong badge "Het han: ...".
 *   - Don marker #exp=... khoi ten hien thi cho gon (chi anh huong hien thi, khong doi du lieu).
 *
 * Doc lap voi app.js -> khong vo khi fork cap nhat. Duoc entrypoint.sh chen tu dong.
 */
(function () {
  'use strict';

  var MARK_ATTR = 'data-exp-processed';

  // ---- Marker han dung (khop keyadmin/main.go va custom-key-controls.js) ----
  //   #exp=<unix>     -> con han, het han vao thoi diem unix (giay)
  //   #pause=<giay>   -> dong ho tam dung, con lai bay nhieu giay
  //   #exp=YYYY-MM-DD -> dinh dang cu (het han cuoi ngay do, gio local)
  var pauseRe = /#pause=(\d+)/;
  var expDateRe = /#exp=(\d{4}-\d{2}-\d{2})/;
  var expUnixRe = /#exp=(\d+)/;
  var stripRe = /\s*#(?:exp=\d{4}-\d{2}-\d{2}|exp=\d+|pause=\d+)/g;

  // Tra ve { mode:'none'|'active'|'paused', expMs, secondsLeft }.
  // expDate phai thu TRUOC expUnix (vi \d+ se bat "2026" trong 2026-01-01).
  function parseMarker(text) {
    var m = pauseRe.exec(text || '');
    if (m) return { mode: 'paused', secondsLeft: parseInt(m[1], 10) };
    m = expDateRe.exec(text || '');
    if (m) {
      var p = m[1].split('-');
      return { mode: 'active', expMs: new Date(+p[0], +p[1] - 1, +p[2], 23, 59, 59).getTime() };
    }
    m = expUnixRe.exec(text || '');
    if (m) return { mode: 'active', expMs: parseInt(m[1], 10) * 1000 };
    return { mode: 'none' };
  }

  function breakdown(secs) {
    if (secs < 0) secs = 0;
    return {
      days: Math.floor(secs / 86400),
      hours: Math.floor((secs % 86400) / 3600),
      minutes: Math.floor((secs % 3600) / 60),
      seconds: secs % 60
    };
  }

  function fmtCountdown(b) {
    return b.days + ' ngày ' + b.hours + ' giờ ' + b.minutes + ' phút ' + b.seconds + ' giây';
  }

  // mode='active' -> value = expMs (tuyet doi); mode='paused' -> value = secondsLeft.
  function labelFor(mode, value) {
    if (mode === 'paused') {
      return {
        color: '#94a3b8', bg: 'rgba(148,163,184,0.15)',
        text: 'Tạm dừng (còn ' + fmtCountdown(breakdown(value)) + ')'
      };
    }
    var secs = Math.floor((value - Date.now()) / 1000);
    if (secs < 0) {
      return { color: '#ef4444', bg: 'rgba(239,68,68,0.15)', text: 'Đã hết hạn' };
    }
    var warn = secs <= 3 * 86400;
    return {
      color: warn ? '#f59e0b' : '#10b981',
      bg: warn ? 'rgba(245,158,11,0.15)' : 'rgba(16,185,129,0.15)',
      text: 'Còn ' + fmtCountdown(breakdown(secs))
    };
  }

  // Ve/cap nhat noi dung badge theo thoi gian hien tai (goi lai moi giay).
  function renderBadge(badge) {
    var mode = badge.getAttribute('data-exp-mode');
    var value = parseFloat(badge.getAttribute('data-exp-value'));
    var l = labelFor(mode, value);
    badge.innerHTML = '<span style="display:inline-block;background:' + l.bg + ';color:' + l.color +
      ';padding:1px 8px;border-radius:4px;font-weight:500;">' +
      '<i class="fa-regular fa-clock" style="margin-right:4px;"></i>' + l.text + '</span>';
  }

  function buildBadge(mode, value) {
    var div = document.createElement('div');
    div.className = 'text-xs';
    div.setAttribute('data-exp-badge', '1');
    div.setAttribute('data-exp-mode', mode);
    div.setAttribute('data-exp-value', value);
    div.style.cssText = 'margin-top:2px;';
    renderBadge(div);
    return div;
  }

  // Cap nhat tat ca badge dang hien thi (dem lui theo giay).
  function tick() {
    document.querySelectorAll('[data-exp-badge]').forEach(renderBadge);
  }

  function processCards() {
    var cards = document.querySelectorAll('#apiKeysList .card[data-apikey-id]');
    cards.forEach(function (card) {
      if (card.getAttribute(MARK_ATTR) === '1') return;

      // Tim span ten (font-semibold) de doc marker.
      var nameSpan = card.querySelector('.font-semibold');
      var rawText = nameSpan ? nameSpan.textContent : card.textContent;
      var st = parseMarker(rawText);

      card.setAttribute(MARK_ATTR, '1'); // danh dau da xu ly (du co han hay khong)

      if (st.mode === 'none') return;

      // Don marker khoi ten hien thi cho gon (khong doi du lieu that tren server).
      if (nameSpan) {
        nameSpan.textContent = nameSpan.textContent.replace(stripRe, '').trim();
      }

      var value = st.mode === 'paused' ? st.secondsLeft : st.expMs;

      // Chen badge vao khoi grid thong tin (noi chua tokens/credits/requests).
      var grid = card.querySelector('div[style*="grid"]');
      if (grid) {
        grid.appendChild(buildBadge(st.mode, value));
      } else {
        card.appendChild(buildBadge(st.mode, value));
      }
    });
  }

  function init() {
    // Chay lai moi khi DOM doi (danh sach key render lai sau load/toggle/xoa).
    var observer = new MutationObserver(function () {
      // Debounce nhe de tranh chay lien tuc.
      if (window.__expTimer) clearTimeout(window.__expTimer);
      window.__expTimer = setTimeout(processCards, 60);
    });
    observer.observe(document.body, { childList: true, subtree: true });
    processCards();
    // Dem lui theo giay: cap nhat noi dung badge moi 1s (khong ve lai card).
    if (window.__expTick) clearInterval(window.__expTick);
    window.__expTick = setInterval(tick, 1000);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
