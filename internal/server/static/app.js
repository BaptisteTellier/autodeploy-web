// app.js — small Alpine.js component + form helpers for autodeploy-web.
//
// All helpers run client-side so password / MFA / GUID generation never round-
// trips to the server (and is never logged).

// chipClass maps a state token to a status-chip CSS class. Twin of the Go
// template helper of the same name (embed.go) — keep both in sync so SSE live
// updates recolour badges consistently. State tokens are NOT translated.
window.chipClass = function (state) {
  switch (state) {
    case 'done': case 'ready': case 'success':
      return 'ad-chip-done';
    case 'running': case 'installing': case 'uploading':
    case 'wiring': case 'booting': case 'creating':
      return 'ad-chip-running';
    case 'failed': case 'error':
      return 'ad-chip-failed';
    default:
      return 'ad-chip-pending';
  }
};

function formApp() {
  return {
    // Mirror of the server-side Config struct (only the fields we react to).
    cfg: {
      ApplianceType: document.querySelector('[name=ApplianceType]')?.value || 'VSA',
      NodeExporter: document.querySelector('[name=NodeExporter]')?.checked || false,
      VeeamAdminPassword: '',
      VeeamSoPassword: '',
    },

    genPassword() { return genCompliantPassword(); },
    genBase32(n)  { return genBase32(n); },
    genGUID()     { return genGUID(); },

    async savePreset() {
      const name = document.getElementById('preset_name').value.trim();
      if (!name) { alert('Preset name required'); return; }
      const form = document.getElementById('config-form');
      const fd = new FormData(form);
      fd.set('preset_name', name);
      const res = await fetch('/configs', { method: 'POST', body: fd });
      if (res.ok) {
        location.href = '/new?preset=' + encodeURIComponent(name);
      } else {
        alert('Save failed: ' + (await res.text()));
      }
    },

    async deletePreset() {
      const sel = document.getElementById('preset_select');
      const name = sel ? sel.value : '';
      if (!name) { alert('Select a preset to delete first.'); return; }
      if (!confirm('Delete preset "' + name + '"? This cannot be undone.')) return;
      const res = await fetch('/configs/' + encodeURIComponent(name), { method: 'DELETE' });
      if (res.ok) {
        location.href = '/new'; // reload without the deleted ?preset=
      } else {
        alert('Delete failed: ' + (await res.text()));
      }
    },

    exportJSON() {
      const form = document.getElementById('config-form');
      const fd = new FormData(form);
      const obj = formToConfigJSON(fd);
      const blob = new Blob([JSON.stringify(obj, null, 2)], { type: 'application/json' });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      const host = (obj.Hostname || 'autodeploy').replace(/[^a-z0-9_-]+/gi, '_');
      a.download = `${host}.json`;
      document.body.appendChild(a); a.click(); a.remove();
    },

    async importJSON(ev) {
      const file = ev.target.files[0];
      if (!file) return;
      try {
        const text = await file.text();
        const data = JSON.parse(text);
        applyConfigToForm(data);
      } catch (e) {
        alert('Invalid JSON: ' + e.message);
      }
    },
  };
}

// --- Sortable tables ---------------------------------------------------------
// Click-to-sort headers for any <table class="sortable">, project-wide. A header
// cell is skipped when it is empty (e.g. a checkbox column) or carries
// data-nosort (e.g. an actions column). A body cell may set data-sort-value to
// override its sort key (e.g. a Unix epoch behind a "15:04:05" timestamp, or raw
// bytes behind a human-readable size); otherwise the visible text is used.
// A column sorts numerically when every value is a number, else alphabetically.
function initSortableTables(root) {
  (root || document).querySelectorAll('table.sortable').forEach(function (table) {
    const head = table.tHead;
    if (!head || !head.rows.length) return;
    const headers = Array.from(head.rows[0].cells);

    headers.forEach(function (th, col) {
      if (th.hasAttribute('data-nosort') || th.textContent.trim() === '') return;
      th.classList.add('cursor-pointer', 'select-none');
      const arrow = document.createElement('span');
      arrow.className = 'sort-arrow text-slate-300 ml-1';
      arrow.textContent = '↕';
      th.appendChild(arrow);

      th.addEventListener('click', function () {
        const body = table.tBodies[0];
        if (!body) return;
        const rows = Array.from(body.rows);
        const asc = th.getAttribute('data-dir') !== 'asc';

        // Reset the indicators on the other headers.
        headers.forEach(function (other) {
          if (other === th) return;
          other.removeAttribute('data-dir');
          const a = other.querySelector('.sort-arrow');
          if (a) { a.textContent = '↕'; a.className = 'sort-arrow text-slate-300 ml-1'; }
        });
        th.setAttribute('data-dir', asc ? 'asc' : 'desc');
        arrow.textContent = asc ? '▲' : '▼';
        arrow.className = 'sort-arrow text-slate-500 ml-1';

        const keyOf = function (row) {
          const cell = row.cells[col];
          if (!cell) return '';
          const dv = cell.getAttribute('data-sort-value');
          return (dv !== null ? dv : cell.textContent).trim();
        };
        const numeric = rows.every(function (r) {
          const v = keyOf(r);
          return v === '' || /^-?\d+(\.\d+)?$/.test(v);
        });
        rows.sort(function (a, b) {
          const x = keyOf(a), y = keyOf(b);
          if (numeric) {
            const nx = parseFloat(x) || 0, ny = parseFloat(y) || 0;
            return asc ? nx - ny : ny - nx;
          }
          return asc ? x.localeCompare(y) : y.localeCompare(x);
        });
        rows.forEach(function (r) { body.appendChild(r); });
      });
    });
  });
}

document.addEventListener('DOMContentLoaded', function () { initSortableTables(); });

// --- Deploy templates --------------------------------------------------------
// Save the current deploy form as a named template (a non-secret FormSnapshot),
// load via the dropdown (?preset=), or delete the selected one. Mirrors the ISO
// preset toolbar but posts the live form so the server builds the snapshot.
async function deploySaveTemplate() {
  const el = document.getElementById('deploy_preset_name');
  const name = (el ? el.value : '').trim();
  if (!name) { alert('Template name required'); return; }
  const form = document.getElementById('deploy-form');
  if (!form) return;
  const fd = new FormData(form);
  fd.set('preset_name', name);
  const res = await fetch('/deploy/presets', { method: 'POST', body: fd });
  if (res.ok) { location.href = '/deploy?preset=' + encodeURIComponent(name); }
  else { alert('Save failed: ' + (await res.text())); }
}

async function deployDeleteTemplate() {
  const sel = document.getElementById('deploy_preset_select');
  const name = sel ? sel.value : '';
  if (!name) { alert('Select a template to delete first.'); return; }
  if (!confirm('Delete template "' + name + '"?')) return;
  const res = await fetch('/deploy/presets/' + encodeURIComponent(name), { method: 'DELETE' });
  if (res.ok) { location.href = '/deploy'; }
  else { alert('Delete failed: ' + (await res.text())); }
}

// --- Generators ----------------------------------------------------------

// Veeam password: 16 chars, ensures at least 1 of each class and no 4-in-a-row
// of the same class.
function genCompliantPassword() {
  const upper = 'ABCDEFGHJKLMNPQRSTUVWXYZ';
  const lower = 'abcdefghijkmnpqrstuvwxyz';
  const digit = '23456789';
  const symbol = '!@#$%^&*()-_=+';
  const classes = [upper, lower, digit, symbol];

  function pick(s) { return s[Math.floor(crypto.getRandomValues(new Uint32Array(1))[0] / 0xffffffff * s.length)]; }

  // Build a 16-char password rotating classes to avoid 4-of-a-kind.
  let out = '';
  let lastClass = -1, run = 0;
  while (out.length < 16) {
    let c = Math.floor(Math.random() * 4);
    if (c === lastClass) { run++; } else { run = 1; lastClass = c; }
    if (run > 3) {
      c = (c + 1) % 4;
      run = 1;
      lastClass = c;
    }
    out += pick(classes[c]);
  }
  // Ensure all classes present (statistical safety net).
  if (!/[A-Z]/.test(out)) out = out.slice(0, -1) + pick(upper);
  if (!/[a-z]/.test(out)) out = out.slice(0, -1) + pick(lower);
  if (!/[0-9]/.test(out)) out = out.slice(0, -1) + pick(digit);
  if (!/[^A-Za-z0-9]/.test(out)) out = out.slice(0, -1) + pick(symbol);
  return out;
}

function genBase32(n) {
  const alpha = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  const buf = new Uint8Array(n);
  crypto.getRandomValues(buf);
  let out = '';
  for (let i = 0; i < n; i++) out += alpha[buf[i] % 32];
  return out;
}

function genGUID() {
  // RFC 4122 v4
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, x => x.toString(16).padStart(2, '0')).join('');
  return `${h.slice(0,8)}-${h.slice(8,12)}-${h.slice(12,16)}-${h.slice(16,20)}-${h.slice(20)}`;
}

// --- JSON ↔ Form ---------------------------------------------------------
// ⚠ These key sets mirror the Config struct in internal/config/schema.go.
// Adding a non-string config field means updating the matching set here —
// see the 8-file checklist in schema.go above the Config struct.

const STRING_BOOL_KEYS = new Set([
  'VeeamAdminIsMfaEnabled', 'VeeamSoIsMfaEnabled', 'VeeamSoIsEnabled', 'NtpRunSync',
]);
const REAL_BOOL_KEYS = new Set([
  'InPlace','CreateBackup','CleanupCFGFiles','CFGOnly','UseDHCP',
  'ExternalManagersInstallationEnabled','HighAvailabilityEnabled',
  'NodeExporter','NodeExporterTLSEnabled','LicenseVBRTune','VCSPConnection',
  'RestoreConfig','VIASingleDisk','Debug',
]);
const INT_KEYS = new Set(['GrubTimeout','ExternalManagersInstallationTimeout','HighAvailabilityTimeout']);
const ARRAY_KEYS = new Set(['DNSServers','NtpServer']);

function formToConfigJSON(fd) {
  const out = {};
  // Initialise booleans to false (unchecked checkboxes are absent from FormData).
  for (const k of REAL_BOOL_KEYS) out[k] = false;
  for (const k of STRING_BOOL_KEYS) out[k] = "false";

  for (const [k, v] of fd.entries()) {
    if (k === 'preset_name') continue;
    if (REAL_BOOL_KEYS.has(k)) { out[k] = (v === 'on' || v === 'true'); continue; }
    if (STRING_BOOL_KEYS.has(k)) { out[k] = (v === 'on' || v === 'true') ? "true" : "false"; continue; }
    if (INT_KEYS.has(k)) { out[k] = parseInt(v, 10) || 0; continue; }
    if (ARRAY_KEYS.has(k)) {
      out[k] = String(v).split(/[\n,]+/).map(s => s.trim()).filter(Boolean);
      continue;
    }
    out[k] = v;
  }
  return out;
}

function applyConfigToForm(data) {
  for (const [k, v] of Object.entries(data)) {
    const el = document.querySelector(`[name="${CSS.escape(k)}"]`);
    if (!el) continue;
    if (el.type === 'checkbox') {
      el.checked = (v === true || v === 'true' || v === 'True' || v === 1);
    } else if (ARRAY_KEYS.has(k)) {
      el.value = Array.isArray(v) ? v.join('\n') : String(v || '');
    } else {
      el.value = (v === null || v === undefined) ? '' : String(v);
    }
    el.dispatchEvent(new Event('change', { bubbles: true }));
    // Password fields need an input event so Alpine's x-model / cfg reactive
    // state (used by the summary panel) picks up the new value.
    if (k === 'VeeamAdminPassword' || k === 'VeeamSoPassword') {
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }
  }
}

// --- Wizard ------------------------------------------------------------------

function wizardApp(initialIsos = [], msgs = {}) {
  const TOTAL = 6; // Source · System · Network · Accounts · Advanced · Review
  // The six advanced feature keys, in display order. Labels/icons/descriptions
  // come from msgs.adv (passed in from the template so they stay translatable).
  const ADV_KEYS = ['ntp', 'ha', 'mon', 'lic', 'vcsp', 'restore'];

  return {
    // --- State ---------------------------------------------------------------
    step: 1,
    maxReached: 1,
    stepError: '',
    saving: false,
    summary: {},
    _lastAutoPresetName: '', // tracks the last hostname we auto-filled into preset_name

    // Advanced step (step 5) two-phase model.
    advPhase: 'select',        // 'select' | 'config'
    advSel: { ntp: false, ha: false, mon: false, lic: false, vcsp: false, restore: false },

    // Minimal reactive config — only fields needed for conditional visibility.
    cfg: {
      ApplianceType: 'VSA',
      UseDHCP: false,
      VeeamAdminIsMfaEnabled: true,
      VeeamSoIsEnabled: true,
      VeeamSoIsMfaEnabled: true,
      NodeExporter: false,
    },

    // ISO list + selection (single source of truth, avoids duplicate name= bug)
    isoList:     initialIsos,
    isoSelected: '',

    // ISO upload state
    isoMode: 'select',   // 'select' | 'upload'
    isoProgress: 0,
    isoStatus: '',       // '' | 'uploading' | 'done' | 'error'
    isoError: '',

    // --- Computed ------------------------------------------------------------
    // currentIndex is 0-based; with a flat 6-step model it is simply step-1.
    get currentIndex() { return this.step - 1; },
    get totalSteps() { return TOTAL; },

    // Stepper bubbles: one per step, label from msgs.stepLabels.
    get wizSteps() {
      const labels = msgs.stepLabels || ['Source', 'System', 'Network', 'Accounts', 'Advanced', 'Review'];
      return labels.map((label, i) => ({ n: i + 1, label }));
    },

    // "Step X of 6" — with the "· Configure" suffix while in the advanced config phase.
    get stepLabel() {
      const base = (msgs.stepOf || 'Step {s} of 6').replace('{s}', this.step);
      if (this.step === 5 && this.advPhase === 'config') {
        return msgs.stepConfigSuffix ? (base + msgs.stepConfigSuffix) : base;
      }
      return base;
    },

    // Skip is only offered on the advanced step's select phase.
    get showSkip() { return this.step === 5 && this.advPhase === 'select'; },

    get advConfigHint() {
      const n = ADV_KEYS.filter(k => this.advSel[k]).length;
      return (msgs.advConfigHint || 'Only the {n} option(s) you enabled — set their values.').replace('{n}', n);
    },

    // Advanced toggle rows (used by both phase A list and phase B cards).
    get advToggles() {
      const meta = msgs.adv || {};
      return ADV_KEYS.map(key => ({
        key,
        icon:  (meta[key] && meta[key].icon)  || 'tune',
        label: (meta[key] && meta[key].label) || key,
        desc:  (meta[key] && meta[key].desc)  || '',
        sel:   !!this.advSel[key],
      }));
    },

    toggleAdv(key) { this.advSel[key] = !this.advSel[key]; },

    // Per-feature label/desc/icon lookup for the phase-B config cards.
    msgsAdv(key) {
      const meta = (msgs.adv && msgs.adv[key]) || {};
      return { icon: meta.icon || 'tune', label: meta.label || key, desc: meta.desc || '' };
    },

    // --- Stepper styling helpers --------------------------------------------
    bubbleStyle(n) {
      const base = 'width:30px;height:30px;border-radius:999px;display:inline-flex;align-items:center;justify-content:center;font-size:13px;font-weight:700;flex-shrink:0;transition:all 160ms;';
      const done = n < this.step, active = n === this.step;
      return (done || active)
        ? base + 'background:var(--vg);color:#fff;border:1.5px solid var(--vg);' + (active ? 'box-shadow:0 0 0 4px var(--vg-50);' : '')
        : base + 'background:#fff;color:var(--faint);border:1.5px solid #C9CCCF;';
    },
    labelStyle(n) {
      const done = n < this.step, active = n === this.step;
      return 'font-size:11.5px;margin-top:7px;text-align:center;' +
        (active ? 'color:var(--ink);font-weight:700;' : done ? 'color:var(--vg-deep);font-weight:600;' : 'color:var(--faint);font-weight:500;');
    },
    canJump(n) {
      // Steps up to and including maxReached are clickable.
      return n <= this.maxReached;
    },

    // --- Navigation ----------------------------------------------------------
    goTo(stepId) {
      if (stepId < 1 || stepId > TOTAL) return;
      if (stepId > this.maxReached) return; // can't jump ahead
      this.step = stepId;
      this.advPhase = 'select';
      this.stepError = '';
    },

    _goToStep(stepId) {
      this.step = stepId;
      if (stepId > this.maxReached) this.maxReached = stepId;
    },

    next() {
      const err = this._validate();
      if (err) { this.stepError = err; return; }
      this.stepError = '';

      // Advanced step: select → config (only if something is selected), else skip to Review.
      if (this.step === 5 && this.advPhase === 'select') {
        const anyAdv = ADV_KEYS.some(k => this.advSel[k]);
        if (anyAdv) { this.advPhase = 'config'; return; }
        this._goToStep(6);
        this.advPhase = 'select';
        return;
      }
      if (this.step === 5 && this.advPhase === 'config') {
        this._goToStep(6);
        this.advPhase = 'select';
        return;
      }

      if (this.step < TOTAL) {
        this._goToStep(this.step + 1);
        this.advPhase = 'select';
      }
    },

    prev() {
      this.stepError = '';
      // Within the advanced config phase, Back returns to the select phase.
      if (this.step === 5 && this.advPhase === 'config') { this.advPhase = 'select'; return; }
      if (this.step > 1) { this.step = this.step - 1; this.advPhase = 'select'; }
    },

    skip() {
      // Skip is only offered on the advanced step's select phase → straight to Review.
      if (!this.showSkip) return;
      this.stepError = '';
      this._goToStep(6);
      this.advPhase = 'select';
    },

    // --- ISO Upload ----------------------------------------------------------
    async uploadISO(file) {
      if (!file) return;
      this.isoProgress = 0;
      this.isoStatus = 'uploading';
      this.isoError = '';

      return new Promise((resolve, reject) => {
        const fd = new FormData();
        fd.append('file', file);
        const xhr = new XMLHttpRequest();
        xhr.open('POST', '/media/workspace/upload');
        xhr.upload.addEventListener('progress', e => {
          if (e.lengthComputable) this.isoProgress = Math.round((e.loaded / e.total) * 100);
        });
        xhr.addEventListener('load', async () => {
          if (xhr.status >= 200 && xhr.status < 400) {
            this.isoStatus = 'done';
            this.isoProgress = 100;
            // Refresh the ISO list from the server and switch to select mode
            try {
              const list = await fetch('/library/iso').then(r => r.json());
              if (Array.isArray(list)) this.isoList = list;
            } catch (_) {}
            this.isoSelected = file.name;
            this.isoMode = 'select';
            resolve(file.name);
          } else {
            this.isoStatus = 'error';
            this.isoError = xhr.responseText || 'Unknown error';
            reject(new Error(this.isoError));
          }
        });
        xhr.addEventListener('error', () => {
          this.isoStatus = 'error';
          this.isoError = 'Network error';
          reject(new Error('Network error'));
        });
        xhr.send(fd);
      });
    },

    // --- Generators ----------------------------------------------------------
    genPassword() { return genCompliantPassword(); },
    genBase32(n)  { return genBase32(n); },
    genGUID()     { return genGUID(); },

    fillPassword(fieldName) {
      const el = document.querySelector(`[name="${fieldName}"]`);
      if (el) el.value = genCompliantPassword();
    },
    fillBase32(fieldName) {
      const el = document.querySelector(`[name="${fieldName}"]`);
      if (el) el.value = genBase32(16);
    },
    fillGUID(fieldName) {
      const el = document.querySelector(`[name="${fieldName}"]`);
      if (el) el.value = genGUID();
    },

    // --- Save & Redirect -----------------------------------------------------
    async finish() {
      const err = this._validate();
      if (err) { this.stepError = err; return; }

      const nameInput = document.getElementById('preset_name');
      const name = (nameInput ? nameInput.value : '').trim();
      if (!name) { this.stepError = 'Preset name required.'; return; }
      if (!/^[A-Za-z0-9_.\- ]{1,64}$/.test(name)) {
        this.stepError = 'Invalid preset name (alphanumeric, space, dash, dot, underscore — up to 64 chars).';
        return;
      }

      this.saving = true;
      this.stepError = '';
      try {
        const form = document.getElementById('wizard-form');
        const cfg = formToConfigJSON(new FormData(form));
        const res = await fetch('/configs', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name, config: cfg }),
        });
        if (!res.ok) {
          this.stepError = (msgs.saveError || 'Save failed: ') + (await res.text());
          this.saving = false;
          return;
        }
        // Land on the expert build form, pre-loaded with the just-saved preset.
        window.location.href = '/new?preset=' + encodeURIComponent(name);
      } catch (e) {
        this.stepError = 'Error: ' + e.message;
        this.saving = false;
      }
    },

    // --- Summary builder (called when entering step 6 / Review) -------------
    buildSummary() {
      const get = name => {
        const el = document.querySelector(`[name="${CSS.escape(name)}"]`);
        return el ? el.value.trim() : '';
      };
      const isChecked = name => {
        const el = document.querySelector(`[name="${CSS.escape(name)}"]`);
        return el ? el.checked : false;
      };
      this.summary = {
        hostname:     get('Hostname'),
        keyboard:     get('KeyboardLayout'),
        timezone:     get('Timezone'),
        ip:           get('StaticIP'),
        ntp:          get('NtpServer').split(/[\n,]+/).filter(Boolean).slice(0, 2).join(', '),
        cfgOnly:      isChecked('CFGOnly'),
        inPlace:      isChecked('InPlace'),
        createBackup: isChecked('CreateBackup'),
        cleanupCfg:   isChecked('CleanupCFGFiles'),
        // These advanced features are driven by the phase-A toggles (advSel),
        // emitted via hidden inputs, so read them from advSel rather than .checked.
        nodeExporter: !!this.advSel.mon,
        licenseVbr:   !!this.advSel.lic,
        vcsp:         !!this.advSel.vcsp,
        ha:           !!this.advSel.ha && isChecked('HighAvailabilityEnabled'),
        debug:        isChecked('Debug'),
      };
      // Auto-fill preset_name with the current hostname, but only if the user
      // hasn't manually changed it from the last auto-filled value.
      const presetEl = document.getElementById('preset_name');
      if (presetEl && (presetEl.value === '' || presetEl.value === this._lastAutoPresetName)) {
        presetEl.value = this.summary.hostname || '';
        this._lastAutoPresetName = presetEl.value;
      }
    },

    // --- Per-step validation (light) -----------------------------------------
    _validate() {
      const get = name => {
        const el = document.querySelector(`[name="${CSS.escape(name)}"]`);
        return el ? el.value.trim() : '';
      };
      const checked = name => {
        const el = document.querySelector(`[name="${CSS.escape(name)}"]`);
        return el ? el.checked : false;
      };

      switch (this.step) {
        // --- Step 1: Source (appliance type + source ISO) ---
        case 1: {
          if (!get('SourceISO')) return 'Please select or upload a source ISO.';
          return '';
        }
        // --- Step 2: System (keyboard / timezone / hostname) ---
        case 2: {
          const h = get('Hostname');
          if (!h) return 'Hostname is required.';
          if (!/^[a-zA-Z0-9]([a-zA-Z0-9-]{0,13}[a-zA-Z0-9])?$/.test(h))
            return 'Hostname: 1–15 alphanumeric chars, hyphens allowed (not at start/end).';
          if (!get('KeyboardLayout')) return 'Keyboard layout is required.';
          if (!get('Timezone')) return 'Timezone is required.';
          return '';
        }
        // --- Step 3: Network (DHCP toggle + static fields) ---
        case 3: {
          if (!this.cfg.UseDHCP) {
            const ipRe = /^\d{1,3}(\.\d{1,3}){3}$/;
            if (!ipRe.test(get('StaticIP'))) return 'Valid static IP required.';
            if (!ipRe.test(get('Subnet'))) return 'Valid subnet mask required.';
            if (!ipRe.test(get('Gateway'))) return 'Valid gateway IP required.';
            const dns = get('DNSServers');
            if (!dns) return 'At least one DNS server required.';
          }
          return '';
        }
        // --- Step 4: Accounts & security (admin + Security Officer on one card) ---
        case 4: {
          const pw = get('VeeamAdminPassword');
          if (pw.length < 15) return 'Admin password must be at least 15 characters.';
          if (!/[A-Z]/.test(pw)) return 'Admin password must contain at least one uppercase letter.';
          if (!/[a-z]/.test(pw)) return 'Admin password must contain at least one lowercase letter.';
          if (!/[0-9]/.test(pw)) return 'Admin password must contain at least one digit.';
          if (!/[^A-Za-z0-9]/.test(pw)) return 'Admin password must contain at least one special character.';
          if (this.cfg.VeeamAdminIsMfaEnabled) {
            const key = get('VeeamAdminMfaSecretKey');
            if (!/^[A-Z2-7]{16,32}$/.test(key)) return 'Admin MFA key: 16–32 Base32 chars (A–Z, 2–7).';
          }
          if (this.cfg.VeeamSoIsEnabled) {
            const soPw = get('VeeamSoPassword');
            if (soPw.length < 15) return 'Security Officer password must be at least 15 characters.';
            if (soPw === pw) return 'Security Officer password must differ from admin password.';
            // SO MFA secret and recovery token are required only when SO MFA is enabled.
            if (this.cfg.VeeamSoIsMfaEnabled) {
              const mfaKey = get('VeeamSoMfaSecretKey');
              if (!/^[A-Z2-7]{16,32}$/.test(mfaKey)) return 'Security Officer MFA secret: 16–32 Base32 chars (A–Z, 2–7).';
              const token = get('VeeamSoRecoveryToken');
              if (!/^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/.test(token))
                return 'Security Officer recovery token is required (GUID).';
            }
          }
          return '';
        }
        // --- Step 5: Advanced (optional; validate only selected features' fields) ---
        case 5: {
          if (this.advSel.ha) {
            const emTimeout = parseInt(document.querySelector('[name="ExternalManagersInstallationTimeout"]')?.value || '3600', 10);
            const haTimeout = parseInt(document.querySelector('[name="HighAvailabilityTimeout"]')?.value || '3600', 10);
            if (emTimeout < 60 || emTimeout > 86400) return 'External managers timeout: 60–86400 seconds.';
            if (haTimeout < 60 || haTimeout > 86400) return 'HA timeout: 60–86400 seconds.';
          }
          // GRUB timeout lives in the always-present build options.
          const gt = parseInt(document.querySelector('[name="GrubTimeout"]')?.value || '10', 10);
          if (gt < 0 || gt > 300) return 'GRUB timeout: 0–300 seconds.';
          return '';
        }
        // --- Step 6: Review (save-as-preset + Generate) ---
        case 6: {
          const nameInput = document.getElementById('preset_name');
          const name = (nameInput ? nameInput.value : '').trim();
          if (!name) return 'Please enter a preset name.';
          if (!/^[A-Za-z0-9_.\- ]{1,64}$/.test(name)) return 'Invalid preset name.';
          return '';
        }
        default:
          return '';
      }
    },
  };
}
