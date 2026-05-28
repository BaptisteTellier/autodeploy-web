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
