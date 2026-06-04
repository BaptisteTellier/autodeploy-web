// app.js — small Alpine.js component + form helpers for autodeploy-web.
//
// All helpers run client-side so password / MFA / GUID generation never round-
// trips to the server (and is never logged).

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
        alert('Preset saved: ' + name);
        location.reload();
      } else {
        alert('Save failed: ' + (await res.text()));
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
  }
}

// --- Wizard ------------------------------------------------------------------

function wizardApp() {
  // All 12 step definitions. Steps marked vsaOnly are hidden when ApplianceType !== 'VSA'.
  // Steps marked advanced can be skipped by the user.
  const STEPS = [
    { id: 1,  vsaOnly: false, advanced: false },
    { id: 2,  vsaOnly: false, advanced: false },
    { id: 3,  vsaOnly: false, advanced: false },
    { id: 4,  vsaOnly: false, advanced: false },
    { id: 5,  vsaOnly: false, advanced: false },
    { id: 6,  vsaOnly: false, advanced: false },
    { id: 7,  vsaOnly: true,  advanced: true  },
    { id: 8,  vsaOnly: true,  advanced: true  },
    { id: 9,  vsaOnly: true,  advanced: true  },
    { id: 10, vsaOnly: true,  advanced: true  },
    { id: 11, vsaOnly: false, advanced: true  },
    { id: 12, vsaOnly: false, advanced: false },
  ];

  return {
    // --- State ---------------------------------------------------------------
    step: 1,
    maxReached: 1,
    stepError: '',
    saving: false,

    // Minimal reactive config — only fields needed for conditional visibility.
    cfg: {
      ApplianceType: 'VSA',
      UseDHCP: false,
      VeeamAdminIsMfaEnabled: true,
      VeeamSoIsEnabled: true,
      VeeamSoIsMfaEnabled: true,
      NodeExporter: false,
    },

    // ISO upload state
    isoMode: 'select',   // 'select' | 'upload'
    isoProgress: 0,
    isoStatus: '',       // '' | 'uploading' | 'done' | 'error'
    isoError: '',

    // --- Computed ------------------------------------------------------------
    get visibleSteps() {
      return STEPS.filter(s => !s.vsaOnly || this.cfg.ApplianceType === 'VSA');
    },
    get totalSteps() { return this.visibleSteps.length; },
    get currentIndex() { return this.visibleSteps.findIndex(s => s.id === this.step); },
    get currentStepDef() { return STEPS.find(s => s.id === this.step) || STEPS[0]; },
    get progressPct() {
      const idx = this.visibleSteps.findIndex(s => s.id === this.step);
      return this.totalSteps > 1 ? Math.round((idx / (this.totalSteps - 1)) * 100) : 0;
    },
    get isAdvanced() { return this.currentStepDef.advanced; },

    // --- Navigation ----------------------------------------------------------
    goTo(stepId) {
      const target = this.visibleSteps.find(s => s.id === stepId);
      if (!target) return;
      const targetIdx = this.visibleSteps.findIndex(s => s.id === stepId);
      const maxIdx = this.visibleSteps.findIndex(s => s.id === this.maxReached);
      if (targetIdx > maxIdx) return; // can't jump ahead
      this.step = stepId;
      this.stepError = '';
    },

    next() {
      const err = this._validate();
      if (err) { this.stepError = err; return; }
      this.stepError = '';
      const idx = this.visibleSteps.findIndex(s => s.id === this.step);
      if (idx < this.visibleSteps.length - 1) {
        const nextId = this.visibleSteps[idx + 1].id;
        this.step = nextId;
        if (this.visibleSteps.findIndex(s => s.id === nextId) >
            this.visibleSteps.findIndex(s => s.id === this.maxReached)) {
          this.maxReached = nextId;
        }
      }
    },

    prev() {
      this.stepError = '';
      const idx = this.visibleSteps.findIndex(s => s.id === this.step);
      if (idx > 0) this.step = this.visibleSteps[idx - 1].id;
    },

    skip() {
      // Skip only allowed on advanced steps
      if (!this.isAdvanced) return;
      this.stepError = '';
      const idx = this.visibleSteps.findIndex(s => s.id === this.step);
      if (idx < this.visibleSteps.length - 1) {
        const nextId = this.visibleSteps[idx + 1].id;
        this.step = nextId;
        if (this.visibleSteps.findIndex(s => s.id === nextId) >
            this.visibleSteps.findIndex(s => s.id === this.maxReached)) {
          this.maxReached = nextId;
        }
      }
    },

    // --- ISO Upload ----------------------------------------------------------
    async uploadISO(file) {
      if (!file) return;
      this.isoMode = 'upload';
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
        xhr.addEventListener('load', () => {
          if (xhr.status >= 200 && xhr.status < 400) {
            this.isoStatus = 'done';
            this.isoProgress = 100;
            // Set the SourceISO hidden input to the uploaded filename
            const nameInput = document.querySelector('[name="SourceISO"]');
            if (nameInput) nameInput.value = file.name;
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
          this.stepError = 'Save failed: ' + (await res.text());
          this.saving = false;
          return;
        }
        // Mark user as having completed the wizard → expert mode so / doesn't redirect again
        try { await fetch('/mode/expert'); } catch (_) {}
        window.location.href = '/?preset=' + encodeURIComponent(name);
      } catch (e) {
        this.stepError = 'Error: ' + e.message;
        this.saving = false;
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
        case 1: {
          if (!get('SourceISO')) return 'Please select or upload a source ISO.';
          return '';
        }
        case 2: {
          const h = get('Hostname');
          if (!h) return 'Hostname is required.';
          if (!/^[a-zA-Z0-9]([a-zA-Z0-9-]{0,13}[a-zA-Z0-9])?$/.test(h))
            return 'Hostname: 1–15 alphanumeric chars, hyphens allowed (not at start/end).';
          if (!get('KeyboardLayout')) return 'Keyboard layout is required.';
          if (!get('Timezone')) return 'Timezone is required.';
          return '';
        }
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
          return '';
        }
        case 5: {
          if (this.cfg.VeeamSoIsEnabled) {
            const soPw = get('VeeamSoPassword');
            const adminPw = get('VeeamAdminPassword');
            if (soPw.length < 15) return 'Security Officer password must be at least 15 characters.';
            if (soPw === adminPw) return 'Security Officer password must differ from admin password.';
            if (this.cfg.VeeamSoIsMfaEnabled) {
              const key = get('VeeamSoMfaSecretKey');
              if (!/^[A-Z2-7]{16,32}$/.test(key)) return 'SO MFA key: 16–32 Base32 chars (A–Z, 2–7).';
            }
          }
          return '';
        }
        case 6: {
          if (!get('NtpServer')) return 'At least one NTP server is required.';
          return '';
        }
        case 7: {
          const emTimeout = parseInt(document.querySelector('[name="ExternalManagersInstallationTimeout"]')?.value || '3600', 10);
          const haTimeout = parseInt(document.querySelector('[name="HighAvailabilityTimeout"]')?.value || '3600', 10);
          if (emTimeout < 60 || emTimeout > 86400) return 'External managers timeout: 60–86400 seconds.';
          if (haTimeout < 60 || haTimeout > 86400) return 'HA timeout: 60–86400 seconds.';
          return '';
        }
        case 11: {
          const gt = parseInt(document.querySelector('[name="GrubTimeout"]')?.value || '10', 10);
          if (gt < 0 || gt > 300) return 'GRUB timeout: 0–300 seconds.';
          return '';
        }
        case 12: {
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
