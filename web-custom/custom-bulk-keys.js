/*
 * custom-bulk-keys.js  -  Tinh nang tao API key hang loat cho Kiro-Go.
 *
 * Them o truong "So luong" vao modal "Add API Key". Khi nhap > 1 va bam Save,
 * script se goi POST /admin/api/api-keys nhieu lan, roi hien danh sach key vua
 * tao kem nut Copy / Tai CSV.
 *
 * File nay doc lap voi app.js cua Kiro-Go nen khong bi pha vo khi project cap nhat.
 * Duoc nap vao trang nho 1 the <script> chen san trong index.html (xem deploy-hook.sh).
 */
(function () {
  'use strict';

  var QTY_INPUT_ID = 'apiKeyForm_quantity';
  var QTY_GROUP_ID = 'apiKeyForm_quantity_group';
  var EXP_INPUT_ID = 'apiKeyForm_expiryDays';
  var EXP_DATE_INPUT_ID = 'apiKeyForm_expiryDate';
  var MAX_QTY = 500;
  var createMode = false;

  function adminPassword() {
    return sessionStorage.getItem('admin_password') ||
           localStorage.getItem('admin_password') || '';
  }

  // ---- Gioi han thoi gian dung key ----
  // So ngay hieu luc nhap o form -> ma hoa thanh marker '#exp=YYYY-MM-DD' trong ten key.
  // Cron check-key-expiry.sh se doc marker nay va TAT key khi qua han (khong xoa).
  function expiryDays() {
    var el = document.getElementById(EXP_INPUT_ID);
    var d = el ? parseInt(el.value, 10) : 0;
    return (isNaN(d) || d <= 0) ? 0 : d;
  }

  function expiryMarker() {
    var d = expiryDays();
    if (!d) return '';
    var dt = new Date();
    dt.setDate(dt.getDate() + d);
    var y = dt.getFullYear();
    var m = ('0' + (dt.getMonth() + 1)).slice(-2);
    var day = ('0' + dt.getDate()).slice(-2);
    return ' #exp=' + y + '-' + m + '-' + day;
  }

  // Doc han tu ten -> tra ve 'YYYY-MM-DD' de dien vao o date.
  // Ho tro MOI format (dong bo keyadmin): #exp=YYYY-MM-DD (nguyen ban),
  // #exp=<unix> (doi sang ngay local), #pause= (khong co ngay tuyet doi -> '').
  // Phai thu format ngay TRUOC format unix (vi \d+ se bat "2026" trong 2026-01-01).
  function parseMarkerDate(name) {
    name = name || '';
    var mm = /#exp=(\d{4}-\d{2}-\d{2})/.exec(name);
    if (mm) return mm[1];
    mm = /#exp=(\d+)/.exec(name);
    if (mm) {
      var d = new Date(parseInt(mm[1], 10) * 1000);
      var y = d.getFullYear();
      var m = ('0' + (d.getMonth() + 1)).slice(-2);
      var day = ('0' + d.getDate()).slice(-2);
      return y + '-' + m + '-' + day;
    }
    return '';
  }

  // Bo MOI marker han (#exp=date, #exp=unix, #pause=) khoi ten - dong bo keyadmin.
  // Format ngay dat truoc trong alternation de khong bi \d+ cat nham.
  function stripMarker(name) {
    return (name || '').replace(/\s*#(?:exp=\d{4}-\d{2}-\d{2}|exp=\d+|pause=\d+)/g, '').trim();
  }

  // Tu ngay (YYYY-MM-DD) -> marker ' #exp=...' (rong neu khong co ngay).
  function dateToMarker(dateStr) {
    return dateStr ? (' #exp=' + dateStr) : '';
  }

  // ---- Chen truong "So luong" vao modal (chi 1 lan) ----
  function injectQuantityField() {
    if (document.getElementById(QTY_GROUP_ID)) return;
    var creditInput = document.getElementById('apiKeyForm_creditLimit');
    if (!creditInput) return;
    var creditGroup = creditInput.closest('.form-group');
    if (!creditGroup || !creditGroup.parentNode) return;

    var wrap = document.createElement('div');
    wrap.className = 'form-group';
    wrap.id = QTY_GROUP_ID;
    wrap.innerHTML =
      '<label>So luong key can tao</label>' +
      '<input type="number" id="' + QTY_INPUT_ID + '" min="1" max="' + MAX_QTY + '" step="1" value="1" />' +
      '<small style="opacity:.7">Nhap > 1 de tao nhieu key cung luc. Ten se tu them hau to -1, -2, ... ' +
      'Chi ap dung khi tao moi (khong ap dung khi sua).</small>';
    creditGroup.parentNode.insertBefore(wrap, creditGroup.nextSibling);

    // Them o "So ngay hieu luc" ngay duoi o so luong.
    var expWrap = document.createElement('div');
    expWrap.className = 'form-group';
    expWrap.id = EXP_INPUT_ID + '_group';
    expWrap.innerHTML =
      '<label>So ngay hieu luc</label>' +
      '<input type="number" id="' + EXP_INPUT_ID + '" min="0" step="1" value="0" placeholder="0 = khong gioi han" />' +
      '<small style="opacity:.7">De 0 = vinh vien. Nhap so ngay (vd 30) -> key tu TAT khi het han ' +
      '(khong bi xoa, co the bat lai). Ap dung cho ca tao 1 key lan hang loat.</small>';
    creditGroup.parentNode.insertBefore(expWrap, wrap.nextSibling);

    // O "Ngay het han" (dung khi SUA key) - chon ngay truc tiep.
    var dateWrap = document.createElement('div');
    dateWrap.className = 'form-group';
    dateWrap.id = EXP_DATE_INPUT_ID + '_group';
    dateWrap.innerHTML =
      '<label>Ngay het han</label>' +
      '<input type="date" id="' + EXP_DATE_INPUT_ID + '" />' +
      '<small style="opacity:.7">De trong = vinh vien (khong het han). ' +
      'Doi ngay -> key tu TAT khi qua han (khong bi xoa).</small>';
    creditGroup.parentNode.insertBefore(dateWrap, expWrap.nextSibling);
  }

  function setQuantityVisible(show) {
    injectQuantityField();
    var group = document.getElementById(QTY_GROUP_ID);
    if (group) group.style.display = show ? '' : 'none';
    var expGroup = document.getElementById(EXP_INPUT_ID + '_group');
    if (expGroup) expGroup.style.display = show ? '' : 'none';
    // O ngay (date) chi dung khi SUA -> an khi tao moi.
    var dateGroup = document.getElementById(EXP_DATE_INPUT_ID + '_group');
    if (dateGroup) dateGroup.style.display = 'none';
    if (show) {
      var input = document.getElementById(QTY_INPUT_ID);
      if (input) input.value = '1';
      var expInput = document.getElementById(EXP_INPUT_ID);
      if (expInput) expInput.value = '0';
    }
  }

  // Chuan bi o ngay het han khi SUA key: hien o date, pre-fill tu marker trong ten,
  // va bo marker khoi o ten cho gon (se ghi lai khi Save).
  function prepareEditExpiry() {
    injectQuantityField();
    var qGroup = document.getElementById(QTY_GROUP_ID);
    if (qGroup) qGroup.style.display = 'none';
    var expGroup = document.getElementById(EXP_INPUT_ID + '_group');
    if (expGroup) expGroup.style.display = 'none';
    var dateGroup = document.getElementById(EXP_DATE_INPUT_ID + '_group');
    if (dateGroup) dateGroup.style.display = '';
    var nameEl = document.getElementById('apiKeyForm_name');
    var dateEl = document.getElementById(EXP_DATE_INPUT_ID);
    if (nameEl && dateEl) {
      dateEl.value = parseMarkerDate(nameEl.value) || '';
      nameEl.value = stripMarker(nameEl.value);
    }
  }

  // ---- Theo doi che do tao moi vs sua ----
  document.addEventListener('click', function (e) {
    if (!e.target.closest) return;
    if (e.target.closest('#addApiKeyBtn')) {
      createMode = true;
      setTimeout(function () { setQuantityVisible(true); }, 0);
    } else if (e.target.closest('[data-apikey-action="edit"]')) {
      createMode = false;
      setTimeout(function () { prepareEditExpiry(); }, 0);
    }
  }, true);

  // ---- Chan nut Save (capture phase) de xu ly tao hang loat ----
  document.addEventListener('click', function (e) {
    if (!e.target.closest) return;
    var saveBtn = e.target.closest('#apiKeyModalSaveBtn');
    if (!saveBtn) return;
    if (!createMode) {
      // Che do SUA: ghi lai marker #exp tu o ngay vao ten truoc khi app.js PUT.
      var dateElEdit = document.getElementById(EXP_DATE_INPUT_ID);
      var nameElEdit = document.getElementById('apiKeyForm_name');
      if (nameElEdit) {
        var baseName = stripMarker(nameElEdit.value);
        var markerEdit = dateElEdit ? dateToMarker(dateElEdit.value) : '';
        nameElEdit.value = (baseName + markerEdit).trim();
      }
      return; // de app.js xu ly PUT
    }

    var qtyEl = document.getElementById(QTY_INPUT_ID);
    var qty = qtyEl ? parseInt(qtyEl.value, 10) : 1;

    if (!qty || qty <= 1) {
      // 1 key: chen marker han vao ten roi de app.js tu tao key nhu binh thuong.
      var marker = expiryMarker();
      if (marker) {
        var nameEl = document.getElementById('apiKeyForm_name');
        if (nameEl && nameEl.value.indexOf('#exp=') === -1) {
          nameEl.value = ((nameEl.value || '').trim() + marker).trim();
        }
      }
      return; // de app.js xu ly
    }

    // >1 key: chan handler goc, tu xu ly
    e.preventDefault();
    e.stopImmediatePropagation();
    runBulkCreate(qty, saveBtn);
  }, true);

  function readForm() {
    var nameEl = document.getElementById('apiKeyForm_name');
    var enabledEl = document.getElementById('apiKeyForm_enabled');
    var tokenEl = document.getElementById('apiKeyForm_tokenLimit');
    var creditEl = document.getElementById('apiKeyForm_creditLimit');
    var tokenLimit = parseInt(tokenEl ? tokenEl.value : '0', 10);
    var creditLimit = parseFloat(creditEl ? creditEl.value : '0');
    return {
      name: (nameEl ? nameEl.value : '').trim(),
      enabled: enabledEl ? !!enabledEl.checked : true,
      tokenLimit: (isNaN(tokenLimit) || tokenLimit < 0) ? 0 : tokenLimit,
      creditLimit: (isNaN(creditLimit) || creditLimit < 0) ? 0 : creditLimit
    };
  }

  async function runBulkCreate(qty, saveBtn) {
    if (qty > MAX_QTY) qty = MAX_QTY;
    var base = readForm();
    var originalText = saveBtn.textContent;
    saveBtn.disabled = true;

    var created = [];
    var errorCount = 0;
    var marker = expiryMarker();

    for (var i = 1; i <= qty; i++) {
      saveBtn.textContent = 'Dang tao ' + i + '/' + qty + '...';
      var baseName = base.name ? (base.name + '-' + i) : '';
      // Gan marker han vao ten (neu co). Neu khong co ten goc -> dat 'key-N'.
      var finalName = baseName;
      if (marker) { finalName = (baseName || ('key-' + i)) + marker; }
      var payload = {
        name: finalName,
        enabled: base.enabled,
        tokenLimit: base.tokenLimit,
        creditLimit: base.creditLimit
      };
      try {
        var res = await fetch('/admin/api/api-keys', {
          method: 'POST',
          headers: { 'X-Admin-Password': adminPassword(), 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        var d = await res.json().catch(function () { return {}; });
        if (!res.ok || d.success === false) { errorCount++; continue; }
        created.push({ name: payload.name || '(khong ten)', key: d.key || '' });
      } catch (err) {
        errorCount++;
      }
    }

    saveBtn.disabled = false;
    saveBtn.textContent = originalText;

    // Dong modal tao moi qua nut Cancel cua app.js (de reset trang thai dung cach)
    var cancelBtn = document.getElementById('apiKeyModalCancelBtn');
    if (cancelBtn) cancelBtn.click();

    showResultsOverlay(created, errorCount);
  }

  // ---- Overlay hien ket qua + copy + tai CSV ----
  function showResultsOverlay(created, errorCount) {
    var existing = document.getElementById('bulkKeysOverlay');
    if (existing) existing.remove();

    var lines = created.map(function (k) { return k.name + ',' + k.key; });
    var csv = 'name,key\n' + lines.join('\n');
    var textForView = created.map(function (k) { return k.name + '  ->  ' + k.key; }).join('\n');

    var overlay = document.createElement('div');
    overlay.id = 'bulkKeysOverlay';
    overlay.setAttribute('style', [
      'position:fixed', 'inset:0', 'z-index:999999',
      'background:rgba(0,0,0,.6)', 'display:flex',
      'align-items:center', 'justify-content:center', 'padding:20px'
    ].join(';'));

    var box = document.createElement('div');
    box.setAttribute('style', [
      'background:#1e2230', 'color:#e6e8ef', 'border-radius:12px',
      'max-width:680px', 'width:100%', 'max-height:85vh', 'overflow:auto',
      'box-shadow:0 20px 60px rgba(0,0,0,.5)', 'padding:22px',
      'font-family:inherit'
    ].join(';'));

    var summary = 'Da tao thanh cong ' + created.length + ' key' +
      (errorCount ? (' (' + errorCount + ' loi)') : '') + '.';

    box.innerHTML =
      '<h3 style="margin:0 0 6px;font-size:18px;">' + summary + '</h3>' +
      '<p style="margin:0 0 12px;opacity:.75;font-size:13px;line-height:1.5;">' +
      'Day la lan duy nhat key goc duoc hien thi. Hay Copy hoac Tai CSV ngay de luu lai.</p>' +
      '<textarea id="bulkKeysText" readonly style="width:100%;height:240px;resize:vertical;' +
      'background:#11141d;color:#9be7a0;border:1px solid #333;border-radius:8px;padding:10px;' +
      'font-family:monospace;font-size:13px;white-space:pre;"></textarea>' +
      '<div style="display:flex;gap:10px;margin-top:14px;flex-wrap:wrap;">' +
      '<button id="bulkKeysCopy" style="flex:1;min-width:120px;padding:10px;border:0;border-radius:8px;' +
      'background:#3b82f6;color:#fff;cursor:pointer;font-size:14px;">Copy tat ca</button>' +
      '<button id="bulkKeysCsv" style="flex:1;min-width:120px;padding:10px;border:0;border-radius:8px;' +
      'background:#22a06b;color:#fff;cursor:pointer;font-size:14px;">Tai CSV</button>' +
      '<button id="bulkKeysClose" style="flex:1;min-width:120px;padding:10px;border:1px solid #555;' +
      'border-radius:8px;background:transparent;color:#e6e8ef;cursor:pointer;font-size:14px;">Dong</button>' +
      '</div>';

    overlay.appendChild(box);
    document.body.appendChild(overlay);
    document.getElementById('bulkKeysText').value = textForView;

    document.getElementById('bulkKeysCopy').addEventListener('click', function () {
      var ta = document.getElementById('bulkKeysText');
      ta.select();
      try { document.execCommand('copy'); } catch (e) {}
      if (navigator.clipboard) { navigator.clipboard.writeText(textForView).catch(function () {}); }
      this.textContent = 'Da copy!';
    });

    document.getElementById('bulkKeysCsv').addEventListener('click', function () {
      var blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
      var url = URL.createObjectURL(blob);
      var a = document.createElement('a');
      var stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '');
      a.href = url; a.download = 'api-keys-' + stamp + '.csv';
      document.body.appendChild(a); a.click(); a.remove();
      URL.revokeObjectURL(url);
    });

    document.getElementById('bulkKeysClose').addEventListener('click', function () {
      overlay.remove();
      location.reload(); // tai lai de danh sach key cap nhat
    });
  }
})();
