// Synthetic browser acceptance. See web/AGENTS.md for the temporary driver setup.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const zlib = require('node:zlib');
const root = path.resolve(__dirname, '..');
const sha = 'a'.repeat(40);
const files = {
  '/': ['web/index.html', 'text/html'], '/app.js': ['web/app.js', 'text/javascript'],
  '/i18n.js': ['web/i18n.js', 'text/javascript'], '/styles.css': ['web/styles.css', 'text/css'],
  '/plugins/github/plugin.js': ['plugins/github/frontend/plugin.js', 'text/javascript'],
  '/plugins/github/plugin.css': ['plugins/github/frontend/plugin.css', 'text/css'],
};
const server = http.createServer((req, res) => {
  const file = files[req.url];
  if (!file) { res.writeHead(404); return res.end(); }
  res.setHeader('Content-Type', file[1]); res.end(fs.readFileSync(path.join(root, file[0])));
});
const maintenance = () => ({
  run_id: '20261026T010000.000000000Z-abcdef12', status: 'pushed', trigger: 'manual',
  started_at: '2026-10-26T01:00:00Z', finished_at: '2026-10-26T01:01:00Z',
  runner: 'codex', model: 'synthetic-model', stage: 'done', summary: 'Synthetic raw model summary.',
  usage: { input_tokens: 10000, cached_tokens: 0, output_tokens: 2345, total_tokens: 12345, source: 'reported' },
  cost: { estimated: true, currency: 'USD', total_cost: 0.0123, source: 'models.dev' },
  repository: { plugin: 'github', full_name: 'example/context', branch: 'main' },
  pull: { head: sha, status: 'pulled' }, commit: 'b'.repeat(40), remaining: 17, continuation_queued: true,
  needs_review: [{ source: 'inbox/observation.md', reason: 'candidate_requires_owner_review' }],
  edits: [{ target: 'core/overview.md', reason: 'Synthetic structural edit', diff: '+## Example\n', action: 'update' }],
});
const syntheticRunHistory = () => Array.from({ length: 25 }, (_, index) => {
  const day = String(20 - Math.floor(index / 4)).padStart(2, '0');
  const status = index % 5 === 1 ? 'failed' : index % 4 === 0 ? 'reported' : index % 3 === 0 ? 'no_changes' : 'pushed';
  return {
    run_id: `202610${day}T0100${String(index).padStart(2, '0')}.000000000Z-history${index}`,
    status,
    trigger: index % 2 ? 'scheduler' : 'manual',
    started_at: `2026-10-${day}T01:00:${String(index).padStart(2, '0')}Z`,
    finished_at: `2026-10-${day}T01:01:${String(index).padStart(2, '0')}Z`,
    runner: 'codex', model: 'synthetic-model', stage: 'done',
    summary: `Synthetic ${status} history run ${index + 1}`,
    files_changed: index % 4, remaining: 0, edits: [],
  };
});
const quotaSnapshot = () => {
  const now = Date.now();
  return {
    configured: true, source: 'magpie', fetched_at: new Date(now).toISOString(),
    stale: false, error: '', retry_after_seconds: 0,
    cards: [{
      provider: 'synthetic', provider_label: 'Synthetic Provider', account: 'owner@example',
      alias: 'Primary', kind: 'subscription', plan: 'Pro',
      windows: [{
        name: 'Weekly', used: 42, remaining: 58,
        resets_at: new Date(now + 86400000).toISOString(), display: '', unlimited: false,
        text: false, amount: 0, limit: 0, unit: '', balance: null,
      }],
      balance: '$12.50', read_at: new Date(now).toISOString(), error: '', stale: false,
    }],
  };
};
// A 1x1 PNG: the console only accepts a server-rendered data URL here.
const syntheticQR = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';
const crcTable = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n += 1) {
    let value = n;
    for (let bit = 0; bit < 8; bit += 1) value = value & 1 ? 0xedb88320 ^ (value >>> 1) : value >>> 1;
    table[n] = value >>> 0;
  }
  return table;
})();
function pngChunk(type, data) {
  const out = Buffer.alloc(12 + data.length);
  out.writeUInt32BE(data.length, 0);
  out.write(type, 4, 'ascii');
  data.copy(out, 8);
  out.writeUInt32BE(crc32(out.subarray(4, 8 + data.length)), 8 + data.length);
  return out;
}
function crc32(buffer) {
  let value = 0xffffffff;
  for (const byte of buffer) value = crcTable[(value ^ byte) & 0xff] ^ (value >>> 8);
  return (value ^ 0xffffffff) >>> 0;
}
function syntheticPNG(width = 64, height = 64) {
  const raw = Buffer.alloc((width * 4 + 1) * height);
  for (let y = 0; y < height; y += 1) {
    const row = y * (width * 4 + 1);
    raw[row] = 0;
    for (let x = 0; x < width; x += 1) {
      const at = row + 1 + x * 4;
      raw[at] = 28 + Math.round((x / Math.max(1, width - 1)) * 80);
      raw[at + 1] = 80 + Math.round((y / Math.max(1, height - 1)) * 80);
      raw[at + 2] = 120;
      raw[at + 3] = 255;
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; ihdr[9] = 6; ihdr[10] = 0; ihdr[11] = 0; ihdr[12] = 0;
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    pngChunk('IHDR', ihdr), pngChunk('IDAT', zlib.deflateSync(raw)), pngChunk('IEND', Buffer.alloc(0)),
  ]);
}
function parseMultipart(buffer, contentType) {
  const match = /boundary=(?:"([^"]+)"|([^;]+))/i.exec(contentType || '');
  if (!match) return { fields: {}, image: null, context: null };
  const boundary = Buffer.from('--' + (match[1] || match[2]));
  const fields = {};
  let image = null;
  let context = null;
  let index = buffer.indexOf(boundary);
  while (index >= 0) {
    const start = index + boundary.length;
    const next = buffer.indexOf(boundary, start);
    if (next < 0) break;
    let part = buffer.subarray(start, next);
    if (part.subarray(0, 2).toString() === '--') break;
    if (part.subarray(0, 2).toString() === '\r\n') part = part.subarray(2);
    const headerEnd = part.indexOf('\r\n\r\n');
    if (headerEnd < 0) { index = next; continue; }
    const headers = part.subarray(0, headerEnd).toString('utf8');
    const data = part.subarray(headerEnd + 4, Math.max(headerEnd + 4, part.length - 2));
    const name = /name="([^"]+)"/.exec(headers);
    if (name) {
      if (/filename="/.test(headers)) image = data;
      else {
        const value = data.toString('utf8');
        fields[name[1]] = value;
        if (name[1] === 'context') {
          try { context = JSON.parse(value); } catch (_) { context = null; }
        }
      }
    }
    index = next;
  }
  return { fields, image, context };
}
const syntheticCatalog = () => [
  {
    id: 'report', label: 'Create background report', label_key: 'catalog.report.label',
    description: 'Ask the report model to answer a specific owner request.', description_key: 'catalog.report.description',
    priority: 'P2', enabled: true, group: 'daily', order: 1,
    submit_label: 'Queue report', submit_label_key: 'catalog.report.submit',
    fields: [{
      name: 'request', label: 'What should the report answer?', type: 'textarea',
      required: true, max_length: 4000, placeholder: 'Describe the report request',
    }, {
      name: 'priority', label: 'Priority', type: 'select', required: false,
      options: [{ value: 'P0', label: 'P0' }, { value: 'P1', label: 'P1' }, { value: 'P2', label: 'P2' }, { value: 'P3', label: 'P3' }],
    }],
    requires: ['report'], legacy: false,
  },
  {
    id: 'maintenance', label: 'Organize context', label_key: 'catalog.maintenance.label',
    description: 'Run context maintenance using the configured maintenance binding.', description_key: 'catalog.maintenance.description',
    priority: 'P2', enabled: true, group: 'daily', order: 2,
    submit_label: 'Start maintenance', submit_label_key: 'catalog.maintenance.submit',
    fields: [
      { name: 'note', label: 'Note', type: 'text', required: false, max_length: 120, placeholder: 'Optional note' },
      { name: 'scope', label: 'Scope', type: 'select', required: false, placeholder: 'Choose scope', options: [{ value: 'all', label: 'All documents' }, { value: 'inbox', label: 'Inbox only' }] },
      { name: 'dry_run', label: 'Dry run', type: 'boolean', required: false },
    ],
    requires: ['maintenance'], legacy: false,
  },
  {
    id: 'workshop', label: 'Workshop action', priority: 'P2', enabled: true, group: 'workshop', order: 3,
    submit_label: 'Run workshop', fields: [], requires: ['workshop'], legacy: false,
  },
  {
    id: 'constructor', label: 'Constructor action', priority: 'P2', enabled: true, group: 'tools', order: 10,
    submit_label: 'Run constructor', fields: [], requires: ['report'], legacy: false,
  },
];
const weixinStatus = (overrides = {}) => ({
  deployment: { mode: 'auto', owner_pinned: false },
  configured: false, enabled: false, running: false, owner_id: '',
  state: 'idle', login: null, ...overrides,
});
const visible = async (page, selector) => page.locator(selector).waitFor({ state: 'visible' });
// Report text must stay readable in both themes. The drawer inherits the theme
// ink while the report panel owns its own surface.
const reportContrast = (page) => page.locator('#task-detail-body .run-report').evaluate((el) => {
  const rgb = (value) => {
    const parts = value.match(/[\d.]+/g);
    return parts && parts.length >= 3 ? parts.slice(0, 3).map(Number) : null;
  };
  const luminance = (color) => {
    const [r, g, b] = color.map((part) => {
      const value = part / 255;
      return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  let node = el;
  let background = getComputedStyle(node).backgroundColor;
  while (node && (background === 'rgba(0, 0, 0, 0)' || background === 'transparent')) {
    node = node.parentElement;
    if (node) background = getComputedStyle(node).backgroundColor;
  }
  const foreground = rgb(getComputedStyle(el).color);
  const surface = rgb(background);
  if (!foreground || !surface) return 0;
  const first = luminance(foreground);
  const second = luminance(surface);
  return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
});
async function main() {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ executablePath: process.env.FOLIO_BROWSER_EXECUTABLE, headless: true });
  try {
    for (const language of ['zh', 'en']) {
      const context = await browser.newContext({ locale: language === 'zh' ? 'zh-CN' : 'en-US', viewport: { width: 1440, height: 1000 } });
      const page = await context.newPage();
      // Deterministic display capture: one visible frame from a canvas-backed
      // MediaStream. The app must request the current tab, hide the panel while
      // the stream is open, and stop the track after taking its frame.
      await context.addInitScript(() => {
        window.__folioCaptureDenied = false;
        window.__folioCaptureOptions = null;
        window.__folioCaptureVisibility = null;
        let media = navigator.mediaDevices;
        if (!media) {
          media = {};
          try { Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: media }); } catch (_) {}
        }
        media.getDisplayMedia = async (options) => {
          window.__folioCaptureOptions = options;
          const panel = document.getElementById('feedback-view');
          window.__folioCaptureVisibility = panel ? getComputedStyle(panel).visibility : 'missing';
          if (window.__folioCaptureDenied) throw new DOMException('denied', 'NotAllowedError');
          const canvas = document.createElement('canvas');
          canvas.width = 800;
          canvas.height = 600;
          const drawing = canvas.getContext('2d');
          let tick = 0;
          const draw = () => {
            tick += 1;
            drawing.fillStyle = '#f3efe6';
            drawing.fillRect(0, 0, 800, 600);
            drawing.fillStyle = '#1d6b57';
            drawing.font = 'bold 44px sans-serif';
            drawing.fillText('Folio synthetic capture', 48, 96);
            drawing.fillStyle = '#39433a';
            drawing.font = '24px sans-serif';
            drawing.fillText('Current page frame ' + tick, 48, 144);
            drawing.fillStyle = '#e6f0ea';
            drawing.fillRect(48, 190, 700, 170);
            drawing.strokeStyle = '#145242';
            drawing.lineWidth = 2;
            drawing.strokeRect(48, 190, 700, 170);
            drawing.fillStyle = '#1f261f';
            drawing.font = '20px sans-serif';
            drawing.fillText('Synthetic controls and headings', 72, 238);
          };
          draw();
          if (typeof canvas.captureStream !== 'function') throw new DOMException('unsupported', 'NotSupportedError');
          const stream = canvas.captureStream(5);
          const timer = setInterval(draw, 100);
          const track = stream.getVideoTracks()[0];
          if (track && typeof track.addEventListener === 'function') track.addEventListener('ended', () => clearInterval(timer), { once: true });
          return stream;
        };
      });
      const errors = [];
      page.on('pageerror', e => errors.push(e.message));
      page.on('console', e => { if (e.type() === 'warning' && e.text().includes('missing elements')) errors.push(e.text()); });
      let authenticated = false, authorized = false, connected = false, paused = false;
      let usageError = false, stateError = false, expired = false, jobFailed = false;
      let run = maintenance(), reviewRun = null, runRequests = [], jobRequests = 0, quotaRefreshes = 0;
      let tasks = [], capabilities = null, chatRequests = [], catalogRequests = [], feedbackRequests = [], resultError = false, runnerInstalled = true, historyTasks = [], historicalResult = false;
      let catalogUnavailable = false, feedbackAccepted = true, feedbackReason = 'ambiguous', feedbackValidationError = false;
      let holdHistory = false, holdResult = false, releaseHistory, releaseResult;
      const reportID = "synthetic-report-run";
      let serviceConfig = { version: 1, connections: { local: { name: "Synthetic local", base_url: "http://127.0.0.1:8000/v1", key_present: true } }, defaults: {}, overrides: {} };
      const serviceRequests = [];
      let weixinLogin = null, weixinConfigured = false, weixinRunning = false, weixinOwner = '';
      let weixinDiagnostics = null;
      const taskListing = () => ({ tasks, paused, counts: {}, policy: { defer: { P2: true }, in_window: false, window_start: '00:30', window_end: '07:30' } });
      await page.route('**/api/**', async route => {
        const req = route.request(); const url = new URL(req.url()); const endpoint = url.pathname;
        let body = {}, status = 200;
        if (req.method() !== 'GET' && !['/api/login'].includes(endpoint)) {
          assert.equal(req.headers()['x-csrf-token'], 'synthetic-csrf');
          assert.equal(req.headers()['origin'], url.origin);
        }
        if (expired && endpoint !== '/api/session') { status = 401; body = { error: 'authentication_required' }; }
        else if (endpoint === '/api/session') body = { authenticated, csrf_token: 'synthetic-csrf' };
        else if (endpoint === '/api/login') { authenticated = true; body = { csrf_token: 'synthetic-csrf' }; }
        else if (endpoint === '/api/logout') { authenticated = false; }
        else if (endpoint === '/api/state') {
          if (stateError) { status = 503; body = { error: 'service_unavailable' }; }
          else body = { capabilities: capabilities === false ? undefined : capabilities || { assistant: { available: false, reason: "agent_unavailable" }, report: { available: connected && authorized, reason: "model_unavailable" }, maintenance: { available: connected && authorized, reason: "repository_unavailable" } }, storage: { plugin: 'github', auth: authorized ? 'gh' : 'missing' },
            repository: { connected, full_name: 'example/context', branch: 'main', head: sha },
            model: { available: true }, runners: { codex: { available: runnerInstalled }, dsh: { available: false } }, service: {} };
        }
        else if (endpoint === '/api/catalog') {
          if (catalogUnavailable) { status = 404; body = { error: 'not_found' }; }
          else body = { actions: syntheticCatalog() };
        }
        else if (endpoint === '/api/plugins') body = { plugins: [{ id: 'github' }] };
        else if (endpoint.endsWith('/auth/start')) body = { status: 'pending', user_code: 'SYNTHETIC', interval: 30 };
        else if (endpoint.endsWith('/auth/poll')) { authorized = true; body = { status: 'authenticated', mode: 'gh' }; }
        else if (endpoint.endsWith('/status')) body = { status: authorized ? 'authenticated' : 'signed_out', mode: 'gh' };
        else if (endpoint.endsWith('/repositories')) body = { repositories: [{ full_name: 'example/context', private: true }], count: 1 };
        else if (endpoint === '/api/repository/connect') { connected = true; body = { connected: true }; }
        else if (endpoint === '/api/task-kinds') body = { kinds: [{ id: 'maintenance', label: 'Maintenance' }] };
        else if (endpoint === '/api/automation') body = { latest: reviewRun ? run : null, review_run: reviewRun, runs: syntheticRunHistory(), models: { codex: [] } };
        else if (endpoint.endsWith('/result')) {
          const oldSession = holdResult;
          if (holdResult) { holdResult = false; await new Promise(resolve => { releaseResult = resolve; }); }
          if (oldSession) body = { run_id: reportID, format: 'markdown', text: 'OLD SESSION REPORT CONTENT' };
          else if (resultError) { status = 404; body = { error: 'report_result_unavailable' }; }
          else body = { run_id: reportID, format: 'markdown', text: '# Synthetic report\n<img src=x onerror=alert(1)>\nA readable result.', historical_snapshot: historicalResult, truncated: historicalResult };
        }
        else if (endpoint.startsWith('/api/automation/runs/')) {
          const id = decodeURIComponent(endpoint.split('/').pop());
          body = [run, ...syntheticRunHistory()].find(candidate => candidate.run_id === id) || run;
        }
        else if (endpoint === '/api/automation/run') { runRequests.push(req.postDataJSON()); body = { job_id: 'maintenance' }; }
        else if (endpoint.startsWith('/api/jobs/')) {
          jobRequests++;
          if (endpoint.endsWith('/maintenance')) {
            if (jobFailed) body = { status: 'failed', error: 'operation_failed' };
            else { run = maintenance(); run.needs_review = []; run.remaining = 0; run.status = 'no_changes'; run.edits = []; reviewRun = run; body = { status: 'succeeded', result: run }; }
          } else body = { status: 'succeeded', result: {} };
        }
        else if (endpoint === '/api/usage') {
          if (usageError) { status = 503; body = { error: 'usage_unavailable' }; }
          else body = { totals: { total_tokens: 12345, input_tokens: 10000, output_tokens: 2345, runs: 1, reported_runs: 1, estimated_cost_usd: 0.0123, priced_runs: 1, unpriced_cost_runs: 0 }, days: [], models: [] };
        }
        else if (endpoint === '/api/tasks/pause') { paused = req.postDataJSON().paused; body = taskListing(); }
        else if (/^\/api\/tasks\/[^/]+\/(update|run|cancel)$/.test(endpoint)) {
          const [, , , id, action] = endpoint.split('/');
          const task = tasks.find(t => t.id === id);
          if (action === 'update') task.enabled = req.postDataJSON().enabled;
          if (action === 'cancel') task.status = 'canceled';
          if (action === 'run') { task.not_before = ''; task.due = true; }
          body = taskListing();
        }
        else if (endpoint === '/api/tasks') {
          if (req.method() === 'POST') {
            const payload = req.postDataJSON();
            const task = { id: 'report-task', ...payload, status: 'queued', enabled: true, source: 'manual' };
            tasks.push(task); body = task;
          } else body = taskListing();
        }
        else if (endpoint === '/api/actions') body = { actions: [], http_hosts: [] };
        else if (endpoint === '/api/actions/versions') body = { versions: [] };
        else if (/^\/api\/actions\/[^/]+\/run$/.test(endpoint)) {
          const id = endpoint.split('/')[3];
          const payload = req.postDataJSON();
          catalogRequests.push({ id, fields: payload.fields });
          const task = {
            id: id === 'report' ? 'report-task' : id + '-task',
            title: id === 'report' ? 'Synthetic catalog report' : 'Synthetic catalog maintenance',
            kind: id, status: 'queued', enabled: true, priority: 'P2', source: 'manual',
          };
          tasks.push(task);
          body = { task, created: true };
        }
        else if (endpoint === '/api/feedback' && req.method() === 'POST') {
          const parsed = parseMultipart(req.postDataBuffer(), req.headers()['content-type']);
          feedbackRequests.push(parsed);
          if (feedbackValidationError) { status = 400; body = { error: 'feedback_invalid', reason: 'title_short' }; }
          else if (feedbackAccepted) {
            const task = { id: 'feedback-task', title: parsed.fields.title, kind: 'feedback', status: 'queued', enabled: true, priority: parsed.fields.priority, source: 'manual' };
            tasks.push(task);
            body = { accepted: true, feedback: { id: 'feedback-1', status: 'accepted' }, task };
          } else {
            body = { accepted: false, feedback: { id: 'feedback-1', status: 'rejected', reason: feedbackReason } };
          }
        }
        else if (endpoint.startsWith('/api/agent/sessions/')) {
          if (holdHistory) { holdHistory = false; await new Promise(resolve => { releaseHistory = resolve; }); body = { messages: [{ role: 'assistant', content: 'OLD SESSION CHAT CONTENT' }], tasks: [] }; }
          else body = { messages: [], tasks: historyTasks };
        }
        else if (endpoint === '/api/agent/turns') {
          chatRequests.push(req.postDataJSON());
          const task = { id: 'assistant-task', title: '<img src=x onerror=alert(1)>', kind: 'report', status: 'queued', enabled: true, priority: 'P2', source: 'agent:console-main' };
          tasks.push(task); historyTasks = [{ task_id: task.id, title: task.title, status: task.status }];
          return route.fulfill({ contentType: 'text/event-stream', body: [
            { type: 'turn.started' }, { type: 'task.created', task_id: task.id, title: task.title, status: task.status },
            { type: 'text.delta', text: 'Synthetic task accepted.' }, { type: 'turn.finished', status: 'completed' }
          ].map(event => 'data: ' + JSON.stringify(event) + '\n\n').join('') });
        }
        else if (endpoint === '/api/channels') body = { bark: { configured: false }, deliveries: [] };
        else if (endpoint === '/api/hooks') body = { hooks: [] };
        else if (endpoint === '/api/model-services') {
          if (req.method() === 'POST') {
            const payload = req.postDataJSON(); serviceRequests.push(payload);
            Object.values(payload.connections).forEach(connection => assert.equal('key_present' in connection, false));
            serviceConfig = { ...payload, connections: Object.fromEntries(Object.entries(payload.connections).map(([id, entry]) => [id, { name: entry.name, base_url: entry.base_url, key_present: Boolean(entry.api_key) || Boolean(serviceConfig.connections[id] && serviceConfig.connections[id].key_present) }])) };
          }
          body = serviceConfig;
        }
        else if (endpoint === '/api/model-services/test') body = { reachable: true, models: [{ id: 'synthetic-model' }], checked_at: new Date().toISOString(), inference_tested: false };
        else if (endpoint === '/api/agent/config') {
          if (req.method() === 'POST') body = { configured: true, available: true, kind: 'openai', provider: 'deepseek', base_url: 'https://api.deepseek.com/v1', model: 'synthetic-model', key_present: true, timeout_seconds: 120 };
          else body = { configured: false, available: false, kind: 'command', command: '/data/agent/runtime', key_present: false, timeout_seconds: 120 };
        }
        else if (endpoint === '/api/quotas') body = quotaSnapshot();
        else if (endpoint === '/api/quotas/refresh') { quotaRefreshes++; body = quotaSnapshot(); }
        else if (endpoint === '/api/weixin') body = weixinStatus({ configured: weixinConfigured, enabled: weixinRunning, running: weixinRunning, owner_id: weixinOwner, login: weixinLogin, diagnostics: weixinDiagnostics });
        else if (endpoint === '/api/weixin/login') {
          if (req.method() === 'POST') weixinLogin = { status: 'waiting', qr_image: syntheticQR };
          else if (weixinLogin && weixinLogin.status === 'waiting') weixinLogin = { status: 'need_code', qr_image: syntheticQR };
          body = { login: weixinLogin };
        }
        else if (endpoint === '/api/weixin/login/code') {
          weixinLogin = { status: 'awaiting_confirmation', scanner_id: 'synthetic-owner' };
          body = { login: weixinLogin };
        }
        else if (endpoint === '/api/weixin/login/cancel') { weixinLogin = null; body = weixinStatus({ configured: weixinConfigured, owner_id: weixinOwner }); }
        else if (endpoint === '/api/weixin/confirm') {
          weixinConfigured = true; weixinRunning = true; weixinOwner = 'synthetic-owner'; weixinLogin = null;
          weixinDiagnostics = { last_error: 'runner_auth', last_error_at: new Date().toISOString() };
          body = weixinStatus({ configured: true, enabled: true, running: true, owner_id: weixinOwner, diagnostics: weixinDiagnostics });
        }
        else if (endpoint === '/api/weixin/enabled') {
          weixinRunning = req.postDataJSON().enabled;
          body = weixinStatus({ configured: weixinConfigured, enabled: weixinRunning, running: weixinRunning, owner_id: weixinOwner });
        }
        else throw new Error('Unexpected synthetic endpoint: ' + endpoint);
        await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      });
      const origin = `http://127.0.0.1:${server.address().port}`;
      const activityTab = (tab) => page.locator(`[data-activity-tab="${tab}"]`);
      const settingsTab = (tab) => page.locator(`[data-settings-tab="${tab}"]`);
      const settleNavigation = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      const openActivity = async (tab = 'tasks') => {
        await page.locator('[data-view="activity"]').click();
        await visible(page, '#activity-view');
        await settleNavigation();
        await activityTab(tab).click();
        await page.waitForFunction((value) => {
          const button = document.querySelector(`[data-activity-tab="${value}"]`);
          const panel = document.getElementById(`activity-panel-${value}`);
          return button && button.getAttribute('aria-selected') === 'true' && panel && !panel.hidden;
        }, tab);
      };
      const openSettings = async (tab = 'connections') => {
        await page.locator('[data-view="settings"]').click();
        await visible(page, '#settings-view');
        await settleNavigation();
        await settingsTab(tab).click();
        await page.waitForFunction((value) => {
          const button = document.querySelector(`[data-settings-tab="${value}"]`);
          const panel = document.getElementById(`settings-panel-${value}`);
          return button && button.getAttribute('aria-selected') === 'true' && panel && !panel.hidden;
        }, tab);
      };
      const openCatalog = async (id) => {
        await page.locator(`button[data-catalog-launch="${id}"]`).click();
        await visible(page, '#catalog-dialog');
        await page.waitForFunction((actionId) => document.querySelector('#catalog-dialog [data-catalog-action]')?.dataset.catalogAction === actionId, id);
        return page.locator(`#catalog-dialog [data-catalog-action="${id}"]`);
      };
      const closeCatalog = async () => {
        if (!(await page.locator('#catalog-dialog').evaluate(dialog => dialog.open))) return;
        await page.locator('#catalog-dialog-close').click();
        await page.waitForFunction(() => !document.getElementById('catalog-dialog').open);
      };
      const focusedCatalogLauncher = () => page.evaluate(() => document.activeElement?.dataset?.catalogLaunch || '');
      await page.goto(origin);
      await visible(page, '#login-view');
      await page.locator('#login-username').fill('owner');
      await page.locator('#password').fill('synthetic-password');
      await page.locator('#login-submit').click();
      await visible(page, '#settings-view');
      assert.deepEqual(await page.locator('.section-nav a').evaluateAll(links => links.map(link => link.dataset.view)), ['dashboard', 'chat', 'activity', 'settings'], 'daily navigation has four destinations');
      assert.equal(await page.locator('[data-view="actions"], [data-view="runs"], [data-view="review"]').count(), 0, 'Actions, Run history and Owner review are not daily navigation entries');
      assert.equal(await page.locator('.page-view:visible').count(), 1, 'only one page section is visible');
      assert.equal(await page.locator('#settings-tab-connections').getAttribute('aria-selected'), 'true', 'settings defaults to Connections');
      assert.equal(await page.locator('.settings-panel:visible').count(), 1, 'only the active settings group is shown');
      assert.equal(await page.locator('#settings-panel-connections').isVisible(), true);
      assert.equal(await page.locator('#settings-panel-notifications').isVisible(), false);
      assert.equal(await page.locator('#settings-advanced-card').isVisible(), false, 'Advanced actions live only in the Advanced tab');
      await openSettings('advanced');
      await visible(page, '#settings-advanced-card');
      assert.equal(await page.locator('#settings-advanced-heading').textContent(), language === 'zh' ? '高级' : 'Advanced');
      assert.match(await page.locator('#settings-advanced-card .hint').textContent(), language === 'zh' ? /所有者配置/ : /owner configuration/i);
      assert.equal(await page.locator('#settings-advanced-actions').getAttribute('href'), '#actions');
      assert.equal(await page.locator('#settings-panel-connections').isVisible(), false, 'Connections hides when Advanced is active');
      await page.locator('#settings-advanced-actions').click();
      await visible(page, '#actions-view');
      assert.equal(await page.locator('#actions-heading').textContent(), language === 'zh' ? '动作' : 'Actions');
      await page.locator('[data-view="settings"]').click();
      await visible(page, '#settings-view');
      assert.equal(await page.locator('#settings-tab-connections').getAttribute('aria-selected'), 'true', 'returning to Settings defaults to Connections');
      assert.equal(await page.locator('#run-now-button').isDisabled(), true);
      await visible(page, '#repo-picker-slot button');
      await page.locator('#repo-picker-slot button').click();
      await visible(page, '.gh-code');
      assert.equal(await page.locator('.gh-code').textContent(), 'SYNTHETIC');
      assert.equal(await page.locator('#run-now-button').isDisabled(), true);
      await page.getByRole('button', { name: language === 'zh' ? '立即检查' : 'Check now', exact: true }).click();
      await visible(page, '#gh-repo-search');
      await page.locator('#gh-repo-search').fill('example');
      await page.locator('#gh-repo-search').press('ArrowDown');
      await page.locator('#gh-repo-search').press('Enter');
      await page.locator('#gh-connect').click();
      await page.waitForFunction(() => !document.getElementById('run-now-button').disabled);
      assert.equal(await page.locator('#settings-view').isVisible(), true, 'setup must not disappear after connecting');
      await page.locator('#model-services-save').click();
      await page.waitForFunction(() => document.getElementById('model-services-save').disabled === false);
      assert.deepEqual(serviceRequests.at(-1).defaults.assistant, {});
      await page.locator('#model-service-select').selectOption('');
      await page.locator('#model-services-form > details').first().evaluate(node => { node.open = true; });
      await page.locator('#model-service-id').fill('second');
      await page.locator('#model-service-name').fill('Second synthetic');
      await page.locator('#model-service-url').fill('http://127.0.0.1:8001/v1');
      await page.locator('#model-service-key').fill('synthetic-secret');
      await page.locator('#model-services-save').click();
      await page.waitForFunction(() => document.getElementById('model-services-save').disabled === false);
      assert.equal(await page.locator('#model-service-key').inputValue(), '');
      await page.locator('#model-service-select').selectOption('second');
      await page.locator('#model-task-assistant-connection').selectOption('second');
      await page.locator('#model-task-assistant').fill('group/assistant');
      await page.locator('.model-overrides > summary').click();
      await page.locator('#model-override-add').click();
      await page.locator('.model-override-id').fill('synthetic-report');
      await page.locator('.model-override-connection').selectOption('local');
      await page.locator('.model-override-model').fill('group/report');
      await page.locator('#model-services-save').click();
      await page.waitForFunction(() => document.getElementById('model-services-save').disabled === false);
      assert.deepEqual(serviceRequests.at(-1).defaults.assistant, { connection: 'second', model: 'group/assistant' });
      assert.deepEqual(serviceRequests.at(-1).overrides.report['synthetic-report'], { connection: 'local', model: 'group/report' });
      assert.equal('api_key' in serviceRequests.at(-1).connections.second, false);
      await page.locator('#model-services-test').click();
      await page.waitForFunction(() => document.getElementById('model-services-test').disabled === false);
      assert.match(await page.locator('#model-services-test-result').textContent(), language === 'zh' ? /未测试推理/ : /not tested/);
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-model-settings-${language}.png`, fullPage: true });
      // The main-agent runtime is configurable from the Advanced settings tab;
      // saving a key switches the assistant from unavailable to available.
      await openSettings('advanced');
      assert.equal(await page.locator('#agent-settings').isVisible(), false, 'legacy runtime stays collapsed');
      await page.locator('#advanced-settings > summary').click();
      await visible(page, '#agent-settings');
      assert.equal(await page.locator('#agent-badge').getAttribute('data-state'), 'disabled');
      await page.locator('#agent-kind').selectOption('openai');
      await page.locator('#agent-provider').selectOption('deepseek');
      await page.locator('#agent-model').fill('synthetic-model');
      await page.locator('#agent-api-key').fill('synthetic-key');
      await page.locator('#agent-save').click();
      await page.waitForFunction(() => document.getElementById('agent-badge').dataset.state === 'enabled');
      assert.equal(await page.locator('#agent-key-hint').textContent(), language === 'zh' ? '已保存 key。留空则保持不变。' : 'A key is stored. Leave the field blank to keep it.');
      // WeChat: the QR renders, the phone asks for digits, the owner decides, then
      // the channel starts without a restart.
      await openSettings('notifications');
      await visible(page, '#weixin-section');
      // Assert translated copy, not just state: a missing dictionary entry used
      // to pass because every assertion above read data attributes.
      assert.equal((await page.locator('#weixin-heading').innerText()).trim(), language === 'zh' ? '微信' : 'WeChat');
      assert.equal((await page.locator('#weixin-connect').innerText()).trim(), language === 'zh' ? '连接微信' : 'Connect WeChat');
      assert.equal(await page.locator('#weixin-connect').isDisabled(), false);
      await page.locator('#weixin-connect').click();
      await visible(page, '#weixin-qr');
      assert.match(await page.locator('#weixin-qr').getAttribute('src'), /^data:image\/png;base64,/);
      await visible(page, '#weixin-code-form');
      await page.locator('#weixin-code').fill('123456');
      await page.locator('#weixin-code-submit').click();
      await visible(page, '#weixin-confirm');
      assert.equal(await page.locator('#weixin-scanner').textContent(), 'synthetic-owner');
      await page.locator('#weixin-confirm-button').click();
      await visible(page, '#weixin-toggle');
      await page.waitForFunction(() => document.getElementById('weixin-badge').dataset.state === 'enabled');
      assert.equal(await page.locator('#weixin-qr-wrap').isVisible(), false);
      // A failed answer must reach the page as translated copy, not a raw class.
      await visible(page, '#weixin-health');
      const health = await page.locator('#weixin-health').innerText();
      assert.match(health, language === 'zh' ? /模型凭据失效/ : /model credentials rejected/);
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-weixin-${language}.png`, fullPage: true });
      await page.locator('#weixin-toggle').click();
      await page.waitForFunction(() => document.getElementById('weixin-badge').dataset.state === 'disabled');
      await page.locator('#weixin-toggle').click();
      await page.waitForFunction(() => document.getElementById('weixin-badge').dataset.state === 'enabled');
      await page.locator('#setup-finish').click();
      await visible(page, '#dashboard-view');
      await page.locator('#work-usage-details > summary').click();
      await visible(page, '#quotas-cards .quota-card');
      assert.equal(await page.locator('.quota-card-provider').textContent(), 'Synthetic Provider');
      assert.equal(await page.locator('.quota-window .quota-bar').count(), 1);
      await page.locator('#quotas-refresh').click();
      await page.waitForFunction(() => !document.getElementById('quotas-refresh').disabled);
      assert.equal(quotaRefreshes, 1);
      assert.equal(await page.locator('#repository-section').isVisible(), false);
      assert.equal(await page.locator('#channels-section').isVisible(), false);
      assert.equal(await page.locator('#usage-total').textContent(), '12,345');
      assert.match(await page.locator('#usage-cost').textContent(), /\$0\.0123/);
      assert.equal(await page.locator('#dashboard-view #tasks-section').count(), 0, 'the task board no longer lives on the dashboard');
      assert.equal(await page.locator('#dashboard-view .task-board').count(), 0);
      await visible(page, '#dashboard-current');
      assert.equal(await page.locator('#dashboard-current .current-count').count(), 3);
      assert.equal(await page.locator('#dashboard-current-list').isVisible(), true);
      const currentActivityLink = page.locator('#dashboard-current a[href="#activity"]');
      assert.equal(await currentActivityLink.count(), 1);
      assert.equal((await currentActivityLink.textContent()).trim(), language === 'zh' ? '查看全部动态' : 'View all activity');
      // Settings deep links switch to the owning tab and focus its heading.
      await page.goto(origin + '#channels-section');
      await visible(page, '#settings-view');
      assert.equal(await page.locator('#settings-tab-notifications').getAttribute('aria-selected'), 'true', 'channels deep link opens Notifications');
      assert.equal(await page.locator('#settings-panel-notifications').isVisible(), true);
      assert.equal(await page.evaluate(() => document.activeElement.id), 'channels-heading');
      await page.goto(origin + '#hooks-section');
      await visible(page, '#settings-view');
      assert.equal(await page.locator('#settings-tab-triggers').getAttribute('aria-selected'), 'true', 'hooks deep link opens Triggers');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'hooks-heading');
      await page.goto(origin + '#agent-settings');
      await visible(page, '#settings-view');
      assert.equal(await page.locator('#settings-tab-advanced').getAttribute('aria-selected'), 'true', 'agent deep link opens Advanced');
      assert.equal(await page.locator('#advanced-settings').evaluate(node => node.open), true, 'agent deep link opens the legacy runtime disclosure');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'agent-heading');
      await page.goto(origin + '#model-services-settings');
      await visible(page, '#settings-view');
      assert.equal(await page.locator('#settings-tab-connections').getAttribute('aria-selected'), 'true', 'model deep link opens Connections');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'model-services-heading');
      // Settings tabs share the roving keyboard behaviour.
      await settingsTab('connections').focus();
      await page.keyboard.press('ArrowRight');
      assert.equal(await page.locator('#settings-tab-notifications').getAttribute('aria-selected'), 'true');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'settings-tab-notifications');
      await page.keyboard.press('End');
      assert.equal(await page.locator('#settings-tab-advanced').getAttribute('aria-selected'), 'true');
      await page.keyboard.press('Home');
      assert.equal(await page.locator('#settings-tab-connections').getAttribute('aria-selected'), 'true');
      // Hash aliases open the Activity tab they name.
      await page.goto(origin + '#runs');
      await visible(page, '#activity-view');
      assert.equal(await page.locator('#activity-tab-runs').getAttribute('aria-selected'), 'true', '#runs opens Activity Runs');
      assert.equal(await page.locator('#activity-panel-runs').isVisible(), true);
      await page.goto(origin + '#review');
      await visible(page, '#activity-view');
      assert.equal(await page.locator('#activity-tab-review').getAttribute('aria-selected'), 'true', '#review opens Activity Review');
      assert.equal(await page.locator('#activity-panel-review').isVisible(), true);
      await page.goto(origin + '#activity');
      await visible(page, '#activity-view');
      assert.equal(await page.locator('#activity-tab-tasks').getAttribute('aria-selected'), 'true', '#activity opens Activity Tasks');
      // Browser back restores the previous Activity tab through the hash route.
      await activityTab('runs').click();
      await page.waitForFunction(() => location.hash === '#runs');
      await page.locator('[data-view="settings"]').click();
      await visible(page, '#settings-view');
      await page.goBack();
      await visible(page, '#activity-view');
      assert.equal(await page.locator('#activity-tab-runs').getAttribute('aria-selected'), 'true', 'back navigation restores the Runs tab');
      await page.locator('[data-view="dashboard"]').click();
      await visible(page, '#dashboard-view');
      // Activity owns the task board and refresh controls.
      await openActivity('tasks');
      await page.locator('#tasks-pause').click();
      assert.equal(paused, true);
      await page.locator('#tasks-pause').click();
      assert.equal(paused, false);
      // A restored configured session starts on the generic, server-driven quick actions.
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      await visible(page, '#catalog-list .catalog-launcher >> nth=0');
      assert.equal(await page.locator('#catalog-heading').textContent(), language === 'zh' ? '快捷操作' : 'Quick actions');
      assert.equal(await page.locator('#catalog-list .catalog-group h3').first().textContent(), language === 'zh' ? '日常' : 'Daily');
      assert.equal(await page.locator('#daily-ask').count(), 0, 'hardcoded daily controls are gone');
      assert.equal(await page.locator('#catalog-list .catalog-action').count(), 0, 'the dashboard does not render action forms');
      assert.equal(await page.locator('#catalog-list textarea').count(), 0, 'the dashboard starts without blank form fields');
      assert.equal(await page.locator('#catalog-dialog').isVisible(), false);
      const reportLauncher = page.locator('[data-catalog-launcher="report"]');
      assert.equal(await reportLauncher.locator('h4').textContent(), language === 'zh' ? '创建后台报告' : 'Create background report');
      assert.match(await reportLauncher.locator('.catalog-meta').textContent(), language === 'zh' ? /优先级 P2/ : /Priority P2/);
      assert.match(await reportLauncher.locator('.catalog-meta').textContent(), language === 'zh' ? /报告模型/ : /report model/);
      let reportAction = await openCatalog('report');
      assert.equal(await page.locator('#catalog-dialog-title').textContent(), language === 'zh' ? '创建后台报告' : 'Create background report');
      assert.equal(await page.evaluate(() => document.activeElement?.dataset?.catalogField), 'request', 'opening focuses the first action field');
      let reportInput = reportAction.locator('textarea');
      assert.equal(await reportInput.getAttribute('maxlength'), '4000');
      assert.equal(await reportAction.locator('select[data-catalog-field="priority"]').inputValue(), 'P2');
      await reportAction.locator('button[type="submit"]').click();
      assert.equal(catalogRequests.length, 0, 'catalog report requires an explicit owner request');
      assert.equal(await reportAction.locator('[data-catalog-message]').getAttribute('data-error'), 'true');
      await reportInput.fill('   ');
      await reportAction.locator('button[type="submit"]').click();
      assert.equal(catalogRequests.length, 0, 'whitespace does not count as a required field');
      await reportInput.fill('Summarize synthetic context <script>alert(1)</script>');
      await reportAction.locator('button[type="submit"]').click();
      await visible(page, '#catalog-dialog [data-catalog-action="report"] [data-chat-task]');
      assert.equal(catalogRequests[0].id, 'report');
      assert.equal(catalogRequests[0].fields.request, 'Summarize synthetic context <script>alert(1)</script>');
      assert.equal(catalogRequests[0].fields.priority, 'P2');
      assert.match(await reportAction.locator('[data-catalog-message]').textContent(), language === 'zh' ? /尚未完成/ : /not completed/);
      assert.equal(await page.locator('#catalog-dialog [data-catalog-action="report"] script').count(), 0, 'catalog copy is text, not HTML');
      await closeCatalog();
      assert.equal(await focusedCatalogLauncher(), 'report', 'closing the dialog returns focus to its launcher');
      // Escape and a backdrop click close the same native dialog and restore focus.
      reportAction = await openCatalog('report');
      await visible(page, '#catalog-dialog [data-catalog-action="report"] [data-chat-task]');
      await page.keyboard.press('Escape');
      await page.waitForFunction(() => !document.getElementById('catalog-dialog').open);
      assert.equal(await focusedCatalogLauncher(), 'report', 'Escape closes the dialog and restores launcher focus');
      reportAction = await openCatalog('report');
      await page.mouse.click(4, 4);
      await page.waitForFunction(() => !document.getElementById('catalog-dialog').open);
      assert.equal(await focusedCatalogLauncher(), 'report', 'clicking the backdrop closes the dialog and restores focus');
      // The configured priority survives form.reset() so a second submit keeps P2.
      reportAction = await openCatalog('report');
      reportInput = reportAction.locator('textarea');
      await page.waitForFunction(() => {
        const button = document.querySelector('#catalog-dialog [data-catalog-action="report"] button[type="submit"]');
        return button && !button.disabled;
      });
      assert.equal(await reportAction.locator('select[data-catalog-field="priority"]').inputValue(), 'P2');
      const secondReport = page.waitForResponse((response) => response.url().endsWith('/api/actions/report/run') && response.request().method() === 'POST');
      await reportInput.fill('Second synthetic report request');
      await reportAction.locator('button[type="submit"]').click();
      await secondReport;
      assert.equal(catalogRequests.length, 2);
      assert.equal(catalogRequests[1].id, 'report');
      assert.equal(catalogRequests[1].fields.priority, 'P2');
      await closeCatalog();
      // Optional select fields stay out of the request; booleans are explicit.
      let maintenanceAction = await openCatalog('maintenance');
      assert.equal(await maintenanceAction.locator('input[data-catalog-field="note"]').getAttribute('maxlength'), '120');
      await maintenanceAction.locator('input[data-catalog-field="note"]').fill('Synthetic maintenance note');
      await maintenanceAction.locator('button[type="submit"]').click();
      await visible(page, '#catalog-dialog [data-catalog-action="maintenance"] [data-chat-task]');
      assert.equal(catalogRequests[2].id, 'maintenance');
      assert.equal(catalogRequests[2].fields.note, 'Synthetic maintenance note');
      assert.equal(Object.prototype.hasOwnProperty.call(catalogRequests[2].fields, 'scope'), false);
      assert.equal(catalogRequests[2].fields.dry_run, false);
      await closeCatalog();
      // Object.prototype names are valid action ids and must not look busy.
      let constructorAction = await openCatalog('constructor');
      assert.equal(await constructorAction.locator('button[type="submit"]').isDisabled(), false, 'prototype property names do not look busy');
      const constructorResponse = page.waitForResponse((response) => response.url().endsWith('/api/actions/constructor/run') && response.request().method() === 'POST');
      await constructorAction.locator('button[type="submit"]').click();
      await constructorResponse;
      assert.equal(catalogRequests[3].id, 'constructor');
      await closeCatalog();
      tasks = tasks.filter(task => task.id !== 'maintenance-task' && task.id !== 'constructor-task');
      await page.evaluate(() => window.folio.refreshTasks());
      // Report result, fallback and follow-up still use the one task dialog.
      run = { run_id: reportID, status: 'reported', task_id: 'report-task', runner: 'synthetic', summary: 'Report complete', report_file: 'run-' + reportID + '.md', report_text: 'SHORT PREVIEW ONLY' };
      tasks.find(task => task.id === 'report-task').status = 'done';
      tasks.find(task => task.id === 'report-task').run_id = reportID;
      await page.evaluate(() => window.folio.refreshTasks());
      resultError = true;
      let reportDialog = await openCatalog('report');
      await reportDialog.locator('[data-chat-task]').click();
      await page.waitForFunction(() => (document.querySelector('#task-detail-body .run-report')?.textContent || '').includes(document.documentElement.lang.startsWith('zh') ? '报告正文暂不可用' : 'Report result unavailable'));
      await page.locator('#task-detail-close').click();
      resultError = false;
      reportDialog = await openCatalog('report');
      await reportDialog.locator('[data-chat-task]').click();
      await visible(page, '#task-detail-body .run-report');
      await page.waitForFunction(() => (document.querySelector('#task-detail-body .run-report')?.textContent || '').includes('A readable result'));
      for (const scheme of ['light', 'dark']) {
        await page.emulateMedia({ colorScheme: scheme });
        const ratio = await reportContrast(page);
        assert.ok(ratio >= 4.5, `report contrast in ${scheme} theme: ${ratio.toFixed(2)}`);
      }
      await page.emulateMedia({ colorScheme: 'light' });
      assert.equal(await page.locator('#task-detail-body img').count(), 0);
      await page.locator('#task-detail-body [data-report-followup]').click();
      await visible(page, '#chat-view');
      assert.match(await page.locator('#chat-input').inputValue(), /synthetic-report-run/);
      assert.match(await page.locator('#chat-input').inputValue(), /report.get/);
      historicalResult = true;
      run.run_id = reportID + '-historical';
      tasks.find(task => task.id === 'report-task').run_id = run.run_id;
      await page.locator('[data-view="dashboard"]').click();
      await page.evaluate(() => window.folio.refreshTasks());
      reportDialog = await openCatalog('report');
      await reportDialog.locator('[data-chat-task]').click();
      await visible(page, '#task-detail-body .report-provenance');
      assert.match(await page.locator('#task-detail-body .report-provenance').textContent(), language === 'zh' ? /部分历史快照/ : /Partial historical snapshot/);
      await page.locator('#task-detail-close').click();
      historicalResult = false;
      // Assistant readiness is independent of repository authorization and Codex;
      // an unavailable maintenance capability disables only that catalog action.
      const savedConnected = connected, savedAuthorized = authorized;
      connected = false; authorized = false; runnerInstalled = false;
      capabilities = { assistant: { available: true, reason: 'configured', verification: 'unknown' }, report: { available: true, reason: 'configured' }, maintenance: { available: false, reason: 'repository_unavailable' }, context: { available: false, reason: 'repository_unavailable' } };
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      await visible(page, '#catalog-list .catalog-launcher >> nth=0');
      assert.equal(await page.locator('button[data-catalog-launch="maintenance"]').isDisabled(), true);
      assert.equal(await page.locator('button[data-catalog-launch="report"]').isDisabled(), false);
      assert.match(await page.locator('[data-catalog-launcher="maintenance"] .catalog-meta').textContent(), language === 'zh' ? /连接并授权/ : /Connect and authorize/);
      assert.equal(await page.locator('button[data-catalog-launch="workshop"]').isDisabled(), true, 'workshop is unavailable in this phase');
      assert.match(await page.locator('[data-catalog-launcher="workshop"] .catalog-meta').textContent(), /workshop/);
      await page.locator('[data-view="chat"]').click();
      await visible(page, '#chat-view');
      assert.equal(await page.locator('#chat-heading').textContent(), language === 'zh' ? '助手' : 'Assistant');
      assert.equal(await page.locator('#chat-input').getAttribute('rows'), '2');
      assert.equal(await page.locator('#chat-input').getAttribute('maxlength'), '16000');
      assert.equal(await page.locator('#chat-input').inputValue(), '', 'chat starts without a hardcoded task prompt');
      assert.match(await page.locator('#chat-source-hint').textContent(), language === 'zh' ? /尚未连接/ : /No context/);
      assert.equal(await page.locator('#chat-empty').isVisible(), true);
      assert.equal(await page.locator('#chat-suggestions button').count(), 3);
      assert.equal((await page.locator('#chat-suggestions button').first().textContent()).trim(), language === 'zh' ? '总结最新报告' : 'Summarize the latest report');
      assert.equal(chatRequests.length, 0);
      await page.locator('#chat-suggestions button').first().click();
      assert.equal(chatRequests.length, 0, 'a suggestion fills the composer without sending');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'chat-input', 'a suggestion focuses the composer');
      assert.equal((await page.locator('#chat-input').inputValue()).trim(), language === 'zh' ? '总结最新报告' : 'Summarize the latest report');
      const initialChatHeight = await page.locator('#chat-input').evaluate(input => input.getBoundingClientRect().height);
      await page.locator('#chat-input').fill(Array.from({ length: 12 }, (_, index) => 'Synthetic line ' + index).join('\n'));
      const grownChatHeight = await page.locator('#chat-input').evaluate(input => input.getBoundingClientRect().height);
      assert.ok(grownChatHeight > initialChatHeight + 8, 'the composer auto-grows');
      assert.ok(grownChatHeight <= 161, `the composer stays within 160px: ${grownChatHeight}`);
      const chatInputBox = await page.locator('#chat-input').boundingBox();
      const chatSendBox = await page.locator('#chat-send').boundingBox();
      assert.ok(chatSendBox.x > chatInputBox.x + chatInputBox.width / 2, 'Send sits inline at the right');
      assert.ok(chatSendBox.width < chatInputBox.width / 2, 'Send is not a full-width bar');
      await page.locator('#chat-input').fill('Create a synthetic report task');
      await page.locator('#chat-send').click();
      await visible(page, '#chat-log .chat-task-card');
      assert.equal(await page.locator('#chat-empty').isVisible(), false, 'the empty state hides after conversation content');
      assert.equal(await page.locator('#chat-log img').count(), 0);
      await page.locator('[data-view="dashboard"]').click();
      await page.locator('[data-view="chat"]').click();
      await visible(page, '#chat-log .chat-task-card');
      assert.equal(await page.locator('#chat-empty').isVisible(), false, 'the empty state stays hidden with restored history');
      assert.equal(await page.locator('#chat-log img').count(), 0, 'restored task history stays safe');
      await page.locator('#chat-log [data-chat-task]').click();
      await visible(page, '#task-detail');
      await page.keyboard.press('Escape');
      // Older servers omit capability metadata; saved bindings still enable the catalog.
      connected = savedConnected; authorized = savedAuthorized; capabilities = false; runnerInstalled = true;
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      await visible(page, '#catalog-list .catalog-launcher >> nth=0');
      assert.equal(await page.locator('button[data-catalog-launch="report"]').isDisabled(), false);
      assert.equal(await page.locator('button[data-catalog-launch="maintenance"]').isDisabled(), false);
      // A missing or 404 catalog shows a generic hint and never resurrects daily cards.
      catalogUnavailable = true;
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      await page.waitForFunction(() => document.querySelectorAll('#catalog-list .catalog-launcher').length === 0);
      assert.match(await page.locator('#catalog-hint').textContent(), language === 'zh' ? /操作目录暂不可用/ : /action catalog is unavailable/i);
      assert.equal(await page.locator('#catalog-list .catalog-action').count(), 0);
      assert.equal(await page.locator('#daily-ask').count(), 0);
      catalogUnavailable = false;
      capabilities = null; tasks = []; historyTasks = []; runnerInstalled = true; connected = savedConnected; authorized = savedAuthorized;
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      // Feedback is screenshot-first: one capture primary path, no element
      // selection mode, two visible textareas and collapsed advanced fields.
      assert.equal(await page.locator('#feedback-view').isVisible(), false);
      await page.locator('#feedback-launcher').click();
      await visible(page, '#feedback-view');
      assert.equal(await page.locator('#feedback-launcher').getAttribute('aria-expanded'), 'true');
      const feedbackButtons = await page.locator('#feedback-view button').evaluateAll(buttons => buttons.map(button => (button.textContent || '').trim()));
      assert.equal(feedbackButtons.some(text => /element mode|select an element|select element|选择元素|元素模式/i.test(text)), false, 'no separate element-selection mode');
      assert.equal(await page.locator('#feedback-view [data-feedback-element-mode]').count(), 0);
      assert.match(await page.locator('#feedback-capture-page').getAttribute('class'), /primary/);
      assert.match(await page.locator('#feedback-choose-image').getAttribute('class'), /ghost/);
      assert.match(await page.locator('#feedback-dropzone .hint').textContent(), language === 'zh' ? /粘贴|拖入/ : /paste|drop/i);
      assert.equal(await page.locator('#feedback-form textarea:visible').count(), 2, 'only two primary textareas are visible');
      assert.equal(await page.locator('#feedback-more').evaluate(node => node.open), false, 'advanced fields start collapsed');
      assert.equal(await page.locator('#feedback-title').isVisible(), false);
      assert.equal(await page.locator('#feedback-priority').isVisible(), false);
      assert.equal(await page.locator('#feedback-acceptance').isVisible(), false);
      assert.equal((await page.locator('label[for="feedback-current"]').textContent()).trim(), language === 'zh' ? '发生了什么？' : 'What happened?');
      assert.equal((await page.locator('label[for="feedback-expected"]').textContent()).trim(), language === 'zh' ? '你希望怎样？' : 'What do you want?');
      assert.equal(await page.locator('#feedback-area').getAttribute('type'), 'hidden', 'area is derived from the active view');
      assert.equal(await page.locator('#feedback-area').inputValue(), 'dashboard');
      assert.equal(await page.locator('#feedback-dropzone').evaluate(node => node.getBoundingClientRect().height >= 90), true, 'screenshot dropzone is prominent');
      const panelBefore = await page.locator('#feedback-view').boundingBox();
      assert.ok(panelBefore.width >= 390 && panelBefore.width <= 440, `compact desktop width: ${panelBefore.width}`);
      assert.ok(panelBefore.height >= 560 && panelBefore.height <= 620, `compact desktop height: ${panelBefore.height}`);
      const submitVisible = async () => page.locator('#feedback-submit').evaluate((button) => {
        const panel = document.getElementById('feedback-view');
        const rect = button.getBoundingClientRect();
        const outer = panel.getBoundingClientRect();
        return rect.top >= outer.top && rect.bottom <= outer.bottom + 1 && rect.left >= outer.left && rect.right <= outer.right + 1;
      });
      assert.equal(await submitVisible(), true, 'submit is visible without scrolling');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'feedback-heading', 'opening focuses the heading');
      await page.keyboard.press('Escape');
      assert.equal(await page.locator('#feedback-view').isVisible(), false, 'Escape closes the panel');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'feedback-launcher', 'closing restores launcher focus');
      await page.locator('#feedback-launcher').click();
      await visible(page, '#feedback-view');
      const handle = await page.locator('#feedback-drag-handle').boundingBox();
      await page.mouse.move(handle.x + 48, handle.y + 18);
      await page.mouse.down();
      await page.mouse.move(handle.x + 8, handle.y - 22, { steps: 5 });
      await page.mouse.up();
      const panelAfter = await page.locator('#feedback-view').boundingBox();
      assert.ok(Math.abs(panelAfter.x - panelBefore.x) > 20 || Math.abs(panelAfter.y - panelBefore.y) > 20, 'feedback window is draggable');
      assert.equal(await submitVisible(), true, 'submit remains visible after dragging');
      // Denied screen capture is a translated, non-blocking fallback: the file
      // path stays available and the panel is restored.
      await page.evaluate(() => { window.__folioCaptureDenied = true; });
      await page.locator('#feedback-capture-page').click();
      await page.waitForFunction(() => document.getElementById('feedback-image-error').dataset.error === 'true');
      assert.match(await page.locator('#feedback-image-error').textContent(), language === 'zh' ? /取消截屏/ : /cancelled/i);
      assert.equal(await page.locator('#feedback-view').isVisible(), true);
      assert.equal(await page.locator('#feedback-choose-image').isVisible(), true);
      assert.equal(await page.locator('#feedback-dropzone').isVisible(), true);
      await page.evaluate(() => { window.__folioCaptureDenied = false; });
      // Successful capture hides the panel while getDisplayMedia runs, stores
      // one native-resolution frame, attaches element context and restores it.
      await page.locator('#feedback-capture-page').click();
      await visible(page, '#feedback-editor');
      await page.waitForFunction(() => document.getElementById('feedback-canvas').width > 1);
      await page.waitForFunction(() => {
        const text = document.getElementById('feedback-context-summary')?.textContent || '';
        return /(?:Attached|已附带)\s*\d+/.test(text);
      });
      assert.match(await page.locator('#feedback-context-summary').textContent(), language === 'zh' ? /已附带 \d+ 个页面元素 \/ 当前视图：/ : /Attached \d+ page elements \/ Current view:/);
      assert.match(await page.locator('#feedback-image-state').textContent(), /current-page\.png/);
      const captureOptions = await page.evaluate(() => window.__folioCaptureOptions);
      assert.equal(captureOptions.preferCurrentTab, true, 'current tab is preferred');
      assert.equal(captureOptions.audio, false);
      assert.equal(await page.evaluate(() => window.__folioCaptureVisibility), 'hidden', 'feedback panel is hidden during capture');
      assert.equal(await page.locator('#feedback-view').isVisible(), true, 'feedback panel is restored after capture');
      assert.equal(await page.locator('#feedback-context-details').isVisible(), true);
      await page.locator('#feedback-context-details > summary').click();
      const contextListCount = await page.locator('#feedback-context-list li').count();
      assert.ok(contextListCount > 0 && contextListCount <= 24, `context element list count: ${contextListCount}`);
      assert.ok((await page.locator('#feedback-context-list li code').first().textContent()).length > 0, 'context list shows selectors');
      assert.equal(await submitVisible(), true, 'submit stays visible with a captured preview');
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-feedback-${language}.png` });
      const canvasBox = await page.locator('#feedback-canvas').boundingBox();
      assert.ok(canvasBox && canvasBox.width > 8 && canvasBox.height > 8);
      await page.mouse.move(canvasBox.x + 4, canvasBox.y + 4);
      await page.mouse.down();
      await page.mouse.move(canvasBox.x + Math.min(44, canvasBox.width - 4), canvasBox.y + Math.min(28, canvasBox.height - 4), { steps: 4 });
      await page.mouse.up();
      await page.locator('#feedback-tool-arrow').click();
      await page.mouse.move(canvasBox.x + 6, canvasBox.y + canvasBox.height - 8);
      await page.mouse.down();
      await page.mouse.move(canvasBox.x + Math.min(52, canvasBox.width - 6), canvasBox.y + 8, { steps: 3 });
      await page.mouse.up();
      await page.locator('#feedback-undo').click();
      const exported = await page.locator('#feedback-canvas').evaluate(canvas => new Promise(resolve => {
        if (typeof canvas.toBlob !== 'function') { resolve(null); return; }
        canvas.toBlob(async blob => {
          const bytes = new Uint8Array(await blob.arrayBuffer());
          resolve({ type: blob.type, size: blob.size, signature: Array.from(bytes.slice(0, 8)) });
        }, 'image/png');
      }));
      assert.ok(exported, 'canvas supports PNG export');
      assert.equal(exported.type, 'image/png');
      assert.ok(exported.size > 0);
      assert.equal(exported.signature.join(','), '137,80,78,71,13,10,26,10');
      await page.locator('#feedback-current').fill('The synthetic current behavior is confusing.');
      await page.locator('#feedback-expected').fill('The synthetic expected behavior is clear.');
      const captureResponse = page.waitForResponse(response => response.url().endsWith('/api/feedback') && response.request().method() === 'POST');
      await page.locator('#feedback-submit').click();
      await captureResponse;
      await visible(page, '#feedback-created [data-chat-task]');
      assert.equal(feedbackRequests.length, 1);
      const captureRequest = feedbackRequests[0];
      assert.ok(Array.from(captureRequest.fields.title).length >= 4, 'title is derived on submit');
      assert.equal(captureRequest.fields.area, 'dashboard', 'area follows the active view');
      assert.equal(captureRequest.fields.priority, 'P2', 'priority defaults to P2');
      assert.ok(captureRequest.fields.acceptance.length >= 8, 'acceptance is derived when empty');
      assert.ok(captureRequest.image && captureRequest.image.length > 0, 'captured PNG is attached');
      assert.equal(captureRequest.image.subarray(0, 8).toString('hex'), '89504e470d0a1a0a');
      assert.equal(captureRequest.image.readUInt32BE(16), 800, 'capture keeps native width');
      assert.equal(captureRequest.image.readUInt32BE(20), 600, 'capture keeps native height');
      assert.ok(captureRequest.context, 'page capture attaches context JSON');
      assert.equal(captureRequest.context.view, 'dashboard');
      assert.match(captureRequest.context.route, /^#/);
      assert.equal(captureRequest.context.language, language);
      assert.ok(captureRequest.context.viewport.width > 1 && captureRequest.context.viewport.height > 1);
      assert.ok(captureRequest.context.elements.length > 0 && captureRequest.context.elements.length <= 24);
      assert.ok(Buffer.byteLength(captureRequest.fields.context, 'utf8') <= 24 * 1024, 'context stays within 24 KiB');
      for (const element of captureRequest.context.elements) {
        assert.equal(Object.prototype.hasOwnProperty.call(element, 'value'), false, 'no input values in context');
        assert.ok(element.rect.x >= 0 && element.rect.y >= 0 && element.rect.width >= 0 && element.rect.height >= 0);
        assert.equal(String(element.selector || '').includes('feedback-'), false, 'feedback UI is excluded from context');
      }
      assert.match(await page.locator('#feedback-message').textContent(), language === 'zh' ? /已接受/ : /accepted/i);
      assert.equal(await page.locator('#feedback-context').isVisible(), false, 'accepted submission clears context');
      // A pasted/uploaded file carries only view/route/theme/language/viewport.
      feedbackAccepted = false; feedbackReason = 'ambiguous';
      await page.locator('#feedback-image').setInputFiles({ name: 'synthetic.png', mimeType: 'image/png', buffer: syntheticPNG() });
      await visible(page, '#feedback-editor');
      await page.waitForFunction(() => document.getElementById('feedback-canvas').width > 1);
      await page.waitForFunction(() => {
        const text = document.getElementById('feedback-context-summary')?.textContent || '';
        return (document.documentElement.lang.startsWith('zh') ? text.includes('仅附带当前视图') : text.includes('Only the current view'));
      });
      assert.equal(await page.locator('#feedback-context-details').isVisible(), false, 'uploaded files do not expand to elements');
      await page.locator('#feedback-more > summary').click();
      await visible(page, '#feedback-title');
      await page.locator('#feedback-title').fill('Synthetic feedback title');
      await page.locator('#feedback-current').fill('The synthetic current behavior is confusing.');
      await page.locator('#feedback-expected').fill('The synthetic expected behavior is clear.');
      await page.locator('#feedback-acceptance').fill('The synthetic acceptance check passes.');
      await page.locator('#feedback-priority').selectOption('P1');
      const fileResponse = page.waitForResponse(response => response.url().endsWith('/api/feedback') && response.request().method() === 'POST');
      await page.locator('#feedback-submit').click();
      await fileResponse;
      assert.equal(feedbackRequests.length, 2);
      const fileRequest = feedbackRequests[1];
      assert.equal(fileRequest.fields.title, 'Synthetic feedback title');
      assert.equal(fileRequest.fields.area, 'dashboard');
      assert.equal(fileRequest.fields.priority, 'P1');
      assert.equal(fileRequest.fields.acceptance, 'The synthetic acceptance check passes.');
      assert.ok(fileRequest.image && fileRequest.image.length > 0);
      assert.ok(fileRequest.context, 'file upload attaches base context');
      assert.equal(Object.prototype.hasOwnProperty.call(fileRequest.context, 'elements'), false, 'file upload has no element context');
      assert.equal(fileRequest.context.view, 'dashboard');
      assert.match(fileRequest.context.route, /^#/);
      await page.waitForFunction(() => {
        const text = document.getElementById('feedback-message')?.textContent || '';
        return text.includes(document.documentElement.lang.startsWith('zh') ? '含义不明确' : 'ambiguous');
      });
      assert.equal(await page.locator('#feedback-title').inputValue(), 'Synthetic feedback title');
      assert.equal(await page.locator('#feedback-current').inputValue(), 'The synthetic current behavior is confusing.');
      assert.equal(await page.locator('#feedback-created [data-chat-task]').count(), 0);
      feedbackReason = 'synthetic_unknown_reason';
      await page.locator('#feedback-submit').click();
      await page.waitForFunction(() => (document.getElementById('feedback-message')?.textContent || '').includes('synthetic_unknown_reason'));
      assert.equal(await page.locator('#feedback-title').inputValue(), 'Synthetic feedback title');
      feedbackValidationError = true;
      await page.locator('#feedback-submit').click();
      await page.waitForFunction(() => {
        const text = document.getElementById('feedback-message')?.textContent || '';
        return text.includes(document.documentElement.lang.startsWith('zh') ? '标题太短' : 'title is too short');
      });
      assert.equal(await page.locator('#feedback-title').inputValue(), 'Synthetic feedback title');
      feedbackValidationError = false;
      await page.locator('#feedback-image').setInputFiles({ name: 'not-image.txt', mimeType: 'text/plain', buffer: Buffer.from('not an image') });
      await page.waitForFunction(() => document.getElementById('feedback-image-error').dataset.error === 'true');
      assert.match(await page.locator('#feedback-image-error').textContent(), /PNG|JPEG/);
      assert.equal(await page.locator('#feedback-context').isVisible(), false, 'invalid image clears context');
      feedbackAccepted = true;
      await page.locator('#feedback-close').click();
      assert.equal(await page.locator('#feedback-view').isVisible(), false);
      assert.equal(await page.locator('#feedback-launcher').getAttribute('aria-expanded'), 'false');
      await page.locator('#feedback-launcher').click();
      await visible(page, '#feedback-view');
      assert.equal(await page.locator('#feedback-title').inputValue(), 'Synthetic feedback title', 'closing the floating window preserves the draft');
      await page.locator('#feedback-more').evaluate(node => { node.open = false; });
      await page.locator('#feedback-close').click();
      // The board uses real stages, explains waiting, filters, and opens an attempt drawer.
      reviewRun = { ...maintenance(), needs_review: [] };
      run = { ...maintenance(), needs_review: [], task_id: 't-000000000002', status: 'running', stage: 'organize' };
      tasks = [
        { id: 't-000000000001', kind: 'maintenance', title: 'Queued example', status: 'queued', enabled: true, priority: 'P2', due: true, source: 'hook:example' },
        { id: 't-000000000002', kind: 'maintenance', title: 'Running example', status: 'running', enabled: true, priority: 'P1', source: 'manual' },
        { id: 't-000000000003', kind: 'maintenance', title: 'Failed example', status: 'failed', priority: 'P0', reason: 'runner_network', source: 'manual' },
        { id: 't-000000000004', kind: 'maintenance', title: 'Done example', status: 'done', priority: 'P2', source: 'manual' },
        { id: 't-000000000005', kind: 'feedback-plan', title: 'Feedback plan example', status: 'queued', enabled: true, priority: 'P2', source: 'manual' },
      ];
      await openActivity('review');
      assert.equal(await page.locator('#review-help').evaluate(node => node.open), false, 'review help starts collapsed');
      assert.equal(await page.locator('#review-list details').count(), 0, 'findings do not repeat the workflow help');
      await page.locator('#review-refresh').click();
      await openActivity('tasks');
      await page.waitForFunction(() => document.querySelectorAll('.task-board .task-item').length === 5);
      assert.equal(await page.locator('#activity-view').isVisible(), true);
      for (const column of ['current', 'attention', 'recent']) assert.equal(await page.locator(`[data-column="${column}"] .task-item`).count(), column === 'current' ? 3 : 1);
      const planMeta = page.locator('.task-item:has([data-task-inspect="t-000000000005"]) .task-meta');
      assert.match(await planMeta.textContent(), language === 'zh' ? /反馈计划/ : /Feedback plan/);
      assert.equal((await planMeta.textContent()).includes('feedback-plan'), false, 'reserved kind label is translated');
      assert.match(await page.locator('[data-column="current"] .task-progress').first().textContent(), language === 'zh' ? /空闲时段/ : /off-peak/);
      await page.locator('#task-search').fill('Failed');
      assert.equal(await page.locator('.task-board .task-item').count(), 1);
      await page.locator('#task-search').fill('');
      await page.locator('#task-priority-filter').selectOption('P0');
      assert.equal(await page.locator('.task-board .task-item').count(), 1);
      await page.locator('#task-priority-filter').selectOption('');
      // The dashboard keeps a compact current-work card with up to three rows.
      await page.locator('[data-view="dashboard"]').click();
      await visible(page, '#dashboard-current');
      await page.waitForFunction(() => document.querySelectorAll('#dashboard-current .current-task').length === 3);
      assert.equal(await page.locator('#dashboard-current-active').textContent(), '3');
      assert.equal(await page.locator('#dashboard-current-attention').textContent(), '1');
      assert.equal(await page.locator('#dashboard-current-done').textContent(), '1');
      assert.equal(await page.locator('#dashboard-view .task-board').count(), 0, 'the dashboard stays compact');
      const dashboardTask = page.locator('#dashboard-current [data-dashboard-task]').first();
      const dashboardTaskId = await dashboardTask.getAttribute('data-dashboard-task');
      await dashboardTask.click();
      await visible(page, '#task-detail');
      await page.locator('#task-detail-close').click();
      await page.waitForFunction((id) => document.activeElement.dataset.dashboardTask === id, dashboardTaskId);
      await openActivity('tasks');
      const taskCountBadge = await page.locator('#activity-count-tasks').textContent();
      assert.equal(taskCountBadge, '3', 'Activity Tasks count shows queued and running work');
      await page.locator('[data-task-inspect="t-000000000002"]').click();
      await visible(page, '#task-detail');
      await visible(page, '#task-detail-body .run-head');
      assert.match(await page.locator('#task-detail-body .run-meta').innerText(), /\$0\.0123/);
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-task-drawer-${language}.png`, fullPage: true });
      assert.equal(await page.locator('#activity-view').isVisible(), true);
      run = { ...run, status: 'no_changes', stage: 'done' };
      tasks[1].status = 'done'; tasks[1].run_id = run.run_id;
      await page.evaluate(() => window.folio.refreshState());
      await page.waitForFunction(() => document.querySelector('#task-detail-body .run-head .badge').dataset.state === 'no_changes');
      assert.equal(await page.locator('#task-detail').isVisible(), true, 'drawer follows completion without closing');
      await page.keyboard.press('Escape');
      assert.equal(await page.locator('#task-detail').isVisible(), false);
      await page.waitForFunction(() => document.activeElement.dataset.taskInspect === 't-000000000002');
      await page.locator('[data-task-inspect="t-000000000001"]').click();
      await visible(page, '#task-detail');
      await page.locator('#task-detail-close').click();
      await page.locator('[data-task-id="t-000000000001"][data-task-action="toggle"]').click();
      await page.waitForFunction(() => document.querySelector('[data-task-id="t-000000000001"][data-task-action="toggle"]').textContent.includes(document.documentElement.lang.startsWith('zh') ? '恢复' : 'Release'));
      assert.equal(tasks[0].enabled, false);
      tasks[3].status = 'canceled';
      await page.evaluate(() => window.folio.refreshTasks());
      assert.equal(await page.locator('[data-task-id="t-000000000004"][data-task-action="run"]').count(), 0);
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-board-${language}.png`, fullPage: true });
      tasks = [];
      // Report/failure details must not replace the last maintenance snapshot.
      reviewRun = maintenance(); run = { ...maintenance(), status: 'failed', needs_review: [], remaining: 0 };
      await openActivity('review');
      await page.locator('#review-refresh').click();
      await visible(page, '#review-list .review-finding');
      assert.equal(await page.locator('#review-help').evaluate(node => node.open), false, 'review help is collapsed by default');
      assert.equal(await page.locator('#review-list details').count(), 0, 'each finding does not repeat long help');
      assert.equal(await page.locator('#activity-count-review').textContent(), '1', 'Activity Review shows a finding count');
      await page.locator('#review-help > summary').click();
      await visible(page, '#review-help .review-steps');
      const links = page.locator('#review-list a');
      assert.equal(await links.count(), 2);
      assert.match(await links.nth(0).getAttribute('href'), new RegExp('/blob/' + sha + '/inbox/observation.md$'));
      assert.match(await links.nth(1).getAttribute('href'), /\/blob\/main\/inbox\/observation.md$/);
      assert.equal(await links.nth(0).getAttribute('rel'), 'noopener noreferrer');
      await page.locator('#review-help > summary').click();
      assert.equal(await page.locator('#review-help').evaluate(node => node.open), false, 'review help collapses again');
      // The review model link still lands on the run options inside Activity Tasks.
      await page.locator('#activity-panel-review a[href="#run-options"]').click();
      await visible(page, '#activity-panel-tasks');
      assert.equal(await page.locator('#activity-tab-tasks').getAttribute('aria-selected'), 'true');
      assert.equal(await page.locator('#run-options').evaluate(node => node.open), true, 'review model link opens runner options');
      await openActivity('review');
      // Unknown model text is inert, and an unsafe path never becomes a link.
      reviewRun.needs_review.push({ source: '../bad.md', reason: '<img src=x onerror=alert(1)>' });
      await page.locator('#review-refresh').click();
      await page.waitForFunction(() => document.querySelectorAll('#review-list .review-finding').length === 2);
      assert.equal(await page.locator('#review-list img').count(), 0);
      assert.equal(await page.locator('#review-list a').count(), 2);
      assert.equal(await page.locator('#activity-count-review').textContent(), '2');
      reviewRun = maintenance();
      // Run details distinguish automatic backlog from human review.
      run = maintenance();
      await openActivity('runs');
      await page.locator('#automation-refresh').click();
      await page.waitForFunction(() => document.getElementById('latest-run').textContent.includes('17'));
      assert.equal(await page.locator('#latest-run .review-finding').count(), 1);
      assert.equal(await page.locator('#latest-run .run-summary').first().isVisible(), false, 'raw model summary starts collapsed');
      // History is a bounded client window with an explicit status filter.
      await page.waitForFunction(() => document.querySelectorAll('#runs-list .run-history-item').length === 20);
      assert.equal(await page.locator('#runs-show-more').isVisible(), true);
      assert.match(await page.locator('#runs-window-status').textContent(), language === 'zh' ? /20 \/ 25/ : /20 of 25/);
      await page.locator('#runs-show-more').click();
      await page.waitForFunction(() => document.querySelectorAll('#runs-list .run-history-item').length === 25);
      assert.equal(await page.locator('#runs-show-more').isVisible(), false);
      await page.locator('#run-status-filter').selectOption('failed');
      await page.waitForFunction(() => document.querySelectorAll('#runs-list .run-history-item').length === 5);
      assert.equal(await page.locator('#runs-list .run-history-item .badge[data-state="failed"]').count(), 5);
      await page.locator('#run-status-filter').selectOption('reported');
      await page.waitForFunction(() => document.querySelectorAll('#runs-list .run-history-item').length > 0);
      const reportedBadges = await page.locator('#runs-list .run-history-item .badge').evaluateAll(nodes => nodes.map(node => node.dataset.state));
      assert.ok(reportedBadges.every(state => state === 'reported' || state === 'completed'), 'Reported filter only shows reported runs');
      await page.locator('#run-status-filter').selectOption('all');
      await page.waitForFunction(() => document.querySelectorAll('#runs-list .run-history-item').length === 20);
      assert.equal(await page.locator('#runs-show-more').isVisible(), true, 'changing a filter resets the bounded window');
      const selectedHistory = page.locator('#runs-list .run-history-item').nth(1);
      const selectedSummary = (await selectedHistory.locator('.run-history-summary').textContent()).trim();
      await selectedHistory.click();
      await page.waitForFunction((summary) => (document.getElementById('latest-run')?.textContent || '').includes(summary), selectedSummary);
      assert.equal(await page.locator('#runs-list .run-history-item[data-selected="true"]').count(), 1, 'selected run stays marked in history');
      // Keyboard tab behavior: arrows, Home/End and Escape back to the dashboard.
      await page.locator('#activity-tab-tasks').focus();
      await page.keyboard.press('ArrowRight');
      assert.equal(await page.locator('#activity-tab-runs').getAttribute('aria-selected'), 'true');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'activity-tab-runs');
      await page.keyboard.press('End');
      assert.equal(await page.locator('#activity-tab-review').getAttribute('aria-selected'), 'true');
      await page.keyboard.press('Home');
      assert.equal(await page.locator('#activity-tab-tasks').getAttribute('aria-selected'), 'true');
      await page.keyboard.press('Escape');
      await visible(page, '#dashboard-view');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'dashboard-heading', 'Escape returns to the dashboard heading');
      // Failed checks retain findings; successful checks refresh them via one run job.
      await openActivity('review');
      await page.locator('#review-refresh').click();
      await page.waitForFunction(() => document.querySelectorAll('#review-list .review-finding').length === 1);
      jobFailed = true;
      await page.locator('#review-recheck').click();
      await page.waitForFunction(() => document.getElementById('review-message').dataset.error === 'true');
      assert.equal(await page.locator('#review-list .review-finding').count(), 1);
      jobFailed = false;
      await page.locator('[data-view="dashboard"]').click();
      await openActivity('review');
      await page.locator('#review-recheck').click();
      await page.waitForFunction(() => document.getElementById('review-recheck').disabled === false && document.querySelectorAll('#review-list .review-finding').length === 0);
      assert.deepEqual(runRequests.at(-1), {});
      assert.equal(jobRequests, 2);
      // Error and recovery states, both translations, and mobile overflow.
      usageError = true;
      await page.locator('[data-view="dashboard"]').click();
      await page.locator('#work-usage-details').evaluate(node => { node.open = true; });
      await page.locator('#usage-window').selectOption('7');
      await visible(page, '#usage-message[data-error="true"]');
      usageError = false;
      await page.locator('#usage-window').selectOption('30');
      await page.waitForFunction(() => !document.getElementById('usage-message').textContent);
      const otherLanguage = language === 'zh' ? 'en' : 'zh';
      await page.locator(`[data-lang="${otherLanguage}"]`).click();
      assert.equal(await page.locator('[data-view="settings"]').textContent(), otherLanguage === 'zh' ? '设置' : 'Settings');
      await page.locator(`[data-lang="${language}"]`).click();
      await page.locator("#work-usage-details").evaluate(node => { node.open = false; });
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-dashboard-${language}.png`, fullPage: true });
      await page.setViewportSize({ width: 390, height: 844 });
      await page.locator('[data-view="dashboard"]').click();
      await visible(page, '#dashboard-view');
      // The sidebar becomes a fixed four-item bottom tab bar and content keeps
      // clear of it; the top controls stay on one compact row.
      assert.equal(await page.locator('.sidebar').evaluate(node => getComputedStyle(node).position), 'fixed');
      assert.equal(await page.locator('.section-nav').evaluate(node => getComputedStyle(node).display), 'flex');
      assert.equal(await page.locator('.section-nav a').count(), 4);
      const mobileNav = await page.locator('.sidebar').boundingBox();
      assert.ok(mobileNav && mobileNav.y + mobileNav.height >= 843, 'bottom tab bar reaches the bottom edge');
      const mainPadding = await page.locator('main.shell').evaluate(node => parseFloat(getComputedStyle(node).paddingBottom));
      assert.ok(mainPadding >= 80, `main clears the bottom bar: ${mainPadding}`);
      const launcherBefore = await page.locator('#feedback-launcher').boundingBox();
      assert.ok(launcherBefore && launcherBefore.y + launcherBefore.height <= mobileNav.y + 1, 'feedback launcher sits above the tab bar');
      assert.equal(await page.locator('.header-inner').evaluate(node => getComputedStyle(node).flexWrap), 'nowrap');
      const headerHeight = await page.locator('.header-inner').evaluate(node => node.getBoundingClientRect().height);
      assert.ok(headerHeight < 64, `compact mobile header: ${headerHeight}`);
      for (const control of ['#connection-state', '.lang-switch', '#logout-button']) assert.equal(await page.locator(control).isVisible(), true, `${control} stays reachable`);
      assert.equal(await page.locator('#dashboard-view .task-board').count(), 0, 'mobile dashboard has no task board');
      assert.equal(await page.locator('#dashboard-current').isVisible(), true);
      for (const view of ['dashboard', 'chat', 'activity', 'settings']) {
        await page.locator(`[data-view="${view}"]`).click();
        await visible(page, `#${view}-view`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `mobile overflow in ${view}`);
        if (view === 'dashboard') {
          await openCatalog('report');
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'mobile overflow in catalog dialog');
          await closeCatalog();
          assert.equal(await focusedCatalogLauncher(), 'report', 'mobile dialog close returns focus');
        }
        if (view === 'activity') {
          for (const tab of ['tasks', 'runs', 'review']) {
            await activityTab(tab).click();
            await page.waitForFunction((value) => !document.getElementById(`activity-panel-${value}`).hidden, tab);
            assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `mobile overflow in activity ${tab}`);
            if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-activity-${tab}-mobile-${language}.png`, fullPage: true });
          }
        }
        if (view === 'settings') {
          for (const tab of ['connections', 'notifications', 'triggers', 'advanced']) {
            await settingsTab(tab).click();
            await page.waitForFunction((value) => !document.getElementById(`settings-panel-${value}`).hidden, tab);
            assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `mobile overflow in settings ${tab}`);
            if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-settings-${tab}-mobile-${language}.png`, fullPage: true });
          }
        }
        if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-${view}-mobile-${language}.png`, fullPage: true });
      }
      await openActivity('runs');
      const navAfter = await page.locator('.sidebar').boundingBox();
      const launcherAfter = await page.locator('#feedback-launcher').boundingBox();
      assert.ok(launcherAfter.y + launcherAfter.height <= navAfter.y + 1, 'launcher stays above the fixed tab bar');
      await page.locator('#feedback-launcher').click();
      await visible(page, '#feedback-view');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'mobile overflow in feedback window');
      const mobilePanel = await page.locator('#feedback-view').boundingBox();
      assert.ok(mobilePanel.width >= 388 && mobilePanel.width <= 391, `full-width mobile sheet: ${mobilePanel.width}`);
      assert.equal(await page.locator('#feedback-area').inputValue(), 'runs', 'area follows the active Activity tab');
      assert.ok(mobilePanel.y + mobilePanel.height >= 843, 'mobile sheet reaches the bottom edge');
      assert.equal(await submitVisible(), true, 'submit stays visible in the mobile sheet');
      assert.equal(await page.locator('#feedback-form textarea:visible').count(), 2);
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-feedback-mobile-${language}.png` });
      await page.locator('#feedback-close').click();
      // Delayed history and report responses from a logged-out session must not
      // restore private data after a fresh login (even for the same owner).
      await page.setViewportSize({ width: 1440, height: 1000 });
      run = { run_id: reportID, status: 'reported', task_id: 'report-task', report_file: 'run-' + reportID + '.md', report_text: 'SHORT PREVIEW ONLY' };
      tasks = [{ id: 'report-task', title: 'Delayed synthetic report', kind: 'report', status: 'done', run_id: reportID }];
      holdHistory = true;
      await page.locator('[data-view="chat"]').click();
      await page.locator('[data-view="dashboard"]').click();
      await openActivity('tasks');
      await page.evaluate(() => window.folio.refreshTasks());
      holdResult = true;
      await page.locator('[data-task-inspect="report-task"]').click();
      await visible(page, '#task-detail-body .run-report');
      assert.equal(typeof releaseHistory, 'function');
      assert.equal(typeof releaseResult, 'function');
      await page.locator('#task-detail-close').click();
      await page.locator('[data-view="dashboard"]').click();
      await openCatalog('report');
      await page.locator('#logout-button').evaluate(button => button.click());
      await visible(page, '#login-view');
      assert.equal(await page.locator('#catalog-dialog').isVisible(), false, 'logout closes the private action dialog');
      await page.locator('#login-username').fill('owner');
      await page.locator('#password').fill('synthetic-password');
      await page.locator('#login-submit').click();
      await visible(page, '#dashboard-view');
      const oldHistoryResponse = page.waitForResponse(response => response.url().includes('/api/agent/sessions/console-main'));
      const oldReportResponse = page.waitForResponse(response => response.url().endsWith('/result'));
      releaseHistory(); releaseResult();
      await Promise.all([oldHistoryResponse, oldReportResponse]);
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
      await page.locator('[data-view="chat"]').click();
      await visible(page, '#chat-view');
      assert.equal(await page.locator('#chat-log').textContent().then(text => text.includes('OLD SESSION')), false);
      await page.locator('[data-view="dashboard"]').click();
      await openActivity('tasks');
      await page.locator('[data-task-inspect="report-task"]').click();
      await page.waitForFunction(() => (document.querySelector('#task-detail-body .run-report')?.textContent || '').includes('A readable result'));
      assert.equal(await page.locator('#task-detail-body').textContent().then(text => text.includes('OLD SESSION')), false);
      await page.locator('#task-detail-close').click();
      // Missing authorization preserves the selected repository and guides setup.
      authorized = false;
      await page.goto(origin);
      await visible(page, '#settings-view');
      assert.equal(await page.locator('#repository-summary').isVisible(), true);
      assert.equal(await page.locator('#repository-badge').getAttribute('data-state'), 'pending');
      // State-load failure still has navigation and a visible retryable error.
      stateError = true;
      await page.goto(origin);
      await visible(page, '#app-view');
      await visible(page, '#app-message[data-error="true"]');
      stateError = false;
      // Expiry hides private app data and returns to Folio sign-in.
      await page.locator("#chat-input").evaluate(input => { input.value = "Synthetic private draft"; });
      await page.locator('#feedback-launcher').click();
      await visible(page, '#feedback-view');
      await page.locator('#feedback-image').setInputFiles({ name: 'private.png', mimeType: 'image/png', buffer: syntheticPNG() });
      await visible(page, '#feedback-editor');
      await page.locator('#feedback-more').evaluate(node => { node.open = true; });
      await page.locator('#feedback-title').fill('Synthetic private feedback');
      await page.locator('#feedback-current').fill('Synthetic private current details.');
      expired = true;
      await page.locator('#review-refresh').evaluate(button => button.click());
      await visible(page, '#login-view');
      assert.equal(await page.locator('#app-view').isVisible(), false);
      assert.equal(await page.locator('#feedback-view').isVisible(), false);
      assert.equal(await page.locator('#chat-input').inputValue(), '');
      assert.equal(await page.locator('#chat-log').textContent(), '');
      assert.equal(await page.locator('#feedback-title').inputValue(), '');
      assert.equal(await page.locator('#feedback-current').inputValue(), '');
      assert.equal(await page.locator('#feedback-image').inputValue(), '');
      assert.equal(await page.locator('#feedback-canvas').isVisible(), false);
      assert.equal(await page.locator('#feedback-context').isVisible(), false);
      assert.equal(await page.locator('#feedback-context-summary').textContent(), '');
      assert.equal(await page.locator('#feedback-created').textContent(), '');
      assert.equal(await page.locator('#task-detail-body').textContent(), '');
      assert.deepEqual(errors, []);
      console.log(`${language}: onboarding, capability readiness, four-item nav, dashboard current card, Activity/Settings tabs with keyboard/hash/back/deep links, run window/status filter/selected detail, compact review help/recheck, quick-action launcher/dialog/close/focus/validation/disabled/fallback, assistant title/composer/suggestions, chat task events/history, report result/retry/follow-up, screenshot-first feedback capture/context/fallback/accept/reject, board/filter/drawer/actions, errors, both languages, mobile bottom nav/launcher/overflow, expiry passed`);
      await context.close();
    }
  } finally { await browser.close(); }
}
main().catch(e => { console.error(e); process.exitCode = 1; }).finally(() => server.close());
