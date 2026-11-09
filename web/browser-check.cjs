// Synthetic browser acceptance. See web/AGENTS.md for the temporary driver setup.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
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
const weixinStatus = (overrides = {}) => ({
  deployment: { mode: 'auto', owner_pinned: false },
  configured: false, enabled: false, running: false, owner_id: '',
  state: 'idle', login: null, ...overrides,
});
const visible = async (page, selector) => page.locator(selector).waitFor({ state: 'visible' });
async function main() {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ executablePath: process.env.FOLIO_BROWSER_EXECUTABLE, headless: true });
  try {
    for (const language of ['zh', 'en']) {
      const context = await browser.newContext({ locale: language === 'zh' ? 'zh-CN' : 'en-US', viewport: { width: 1440, height: 1000 } });
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', e => errors.push(e.message));
      page.on('console', e => { if (e.type() === 'warning' && e.text().includes('missing elements')) errors.push(e.text()); });
      let authenticated = false, authorized = false, connected = false, paused = false;
      let usageError = false, stateError = false, expired = false, jobFailed = false;
      let run = maintenance(), reviewRun = null, runRequests = [], jobRequests = 0, quotaRefreshes = 0;
      let tasks = [], capabilities = null, chatRequests = [], reportRequests = [], resultError = false, runnerInstalled = true, historyTasks = [], historicalResult = false;
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
        else if (endpoint === '/api/plugins') body = { plugins: [{ id: 'github' }] };
        else if (endpoint.endsWith('/auth/start')) body = { status: 'pending', user_code: 'SYNTHETIC', interval: 30 };
        else if (endpoint.endsWith('/auth/poll')) { authorized = true; body = { status: 'authenticated', mode: 'gh' }; }
        else if (endpoint.endsWith('/status')) body = { status: authorized ? 'authenticated' : 'signed_out', mode: 'gh' };
        else if (endpoint.endsWith('/repositories')) body = { repositories: [{ full_name: 'example/context', private: true }], count: 1 };
        else if (endpoint === '/api/repository/connect') { connected = true; body = { connected: true }; }
        else if (endpoint === '/api/task-kinds') body = { kinds: [{ id: 'maintenance', label: 'Maintenance' }] };
        else if (endpoint === '/api/automation') body = { latest: reviewRun ? run : null, review_run: reviewRun, runs: reviewRun ? [run] : [], models: { codex: [] } };
        else if (endpoint.endsWith('/result')) {
          const oldSession = holdResult;
          if (holdResult) { holdResult = false; await new Promise(resolve => { releaseResult = resolve; }); }
          if (oldSession) body = { run_id: reportID, format: 'markdown', text: 'OLD SESSION REPORT CONTENT' };
          else if (resultError) { status = 404; body = { error: 'report_result_unavailable' }; }
          else body = { run_id: reportID, format: 'markdown', text: '# Synthetic report\n<img src=x onerror=alert(1)>\nA readable result.', historical_snapshot: historicalResult, truncated: historicalResult };
        }
        else if (endpoint.startsWith('/api/automation/runs/')) body = run;
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
            const payload = req.postDataJSON(); reportRequests.push(payload);
            const task = { id: 'report-task', ...payload, status: 'queued', enabled: true, source: 'manual' };
            tasks.push(task); body = task;
          } else body = taskListing();
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
      await page.goto(origin);
      await visible(page, '#login-view');
      await page.locator('#login-username').fill('owner');
      await page.locator('#password').fill('synthetic-password');
      await page.locator('#login-submit').click();
      await visible(page, '#settings-view');
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
      // The main-agent runtime is configurable from Settings; saving a key
      // switches the assistant from unavailable to available without a restart.
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
      await page.locator('#tasks-pause').click();
      assert.equal(paused, true);
      await page.locator('#tasks-pause').click();
      assert.equal(paused, false);
      // A restored configured session starts on the daily dashboard.
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      // Assistant readiness is independent of repository authorization and Codex.
      const savedConnected = connected, savedAuthorized = authorized;
      connected = false; authorized = false; runnerInstalled = false;
      capabilities = { assistant: { available: true, reason: 'configured', verification: 'unknown' }, report: { available: true, reason: 'configured' }, maintenance: { available: false, reason: 'repository_unavailable' }, context: { available: false, reason: 'repository_unavailable' } };
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      assert.equal(await page.locator('#daily-ask').isDisabled(), false);
      assert.equal(await page.locator('#daily-organize').isDisabled(), true);
      await page.locator('#daily-ask').click();
      await visible(page, '#chat-view');
      await page.waitForFunction(() => document.activeElement.id === 'chat-input');
      assert.match(await page.locator('#chat-input').inputValue(), language === 'zh' ? /来源/ : /source paths/);
      assert.match(await page.locator('#chat-source-hint').textContent(), language === 'zh' ? /尚未连接/ : /No context/);
      await page.locator('#chat-input').fill('Create a synthetic report task');
      await page.locator('#chat-send').click();
      await visible(page, '#chat-log .chat-task-card');
      assert.equal(await page.locator('#chat-log img').count(), 0);
      await page.locator('[data-view="dashboard"]').click();
      await page.locator('[data-view="chat"]').click();
      await visible(page, '#chat-log .chat-task-card');
      assert.equal(await page.locator('#chat-log img').count(), 0, 'restored task history stays safe');
      await page.locator('#chat-log [data-chat-task]').click();
      await visible(page, '#task-detail');
      await page.keyboard.press('Escape');
      await page.locator('[data-view="dashboard"]').click();
      await page.locator('#daily-report').click();
      await page.locator('#report-submit').click();
      assert.equal(reportRequests.length, 0, 'report requires an explicit owner request');
      await page.locator('#report-request').fill('   ');
      await page.locator('#report-submit').click();
      assert.equal(reportRequests.length, 0, 'whitespace does not count as a request');
      assert.equal(await page.locator('#report-message').getAttribute('data-error'), 'true');
      assert.equal(await page.locator('#report-request').getAttribute('maxlength'), '4000');
      await page.locator('#report-request').fill('Summarize synthetic context <script>alert(1)</script>');
      await page.locator('#report-priority').selectOption('P2');
      await page.locator('#report-submit').click();
      await visible(page, '#report-created [data-chat-task]');
      assert.equal(reportRequests[0].payload.request, 'Summarize synthetic context <script>alert(1)</script>');
      assert.equal(reportRequests[0].priority, 'P2');
      assert.match(await page.locator('#report-message').textContent(), language === 'zh' ? /尚未完成/ : /not completed/);
      assert.match(await page.locator('#report-created .hint').textContent(), language === 'zh' ? /低峰|等待/ : /off-peak|waiting/);
      assert.equal(await page.locator('#report-created script').count(), 0);
      run = { run_id: reportID, status: 'reported', task_id: 'report-task', runner: 'synthetic', summary: 'Report complete', report_file: 'run-' + reportID + '.md', report_text: 'SHORT PREVIEW ONLY' };
      tasks.find(task => task.id === 'report-task').status = 'done';
      tasks.find(task => task.id === 'report-task').run_id = reportID;
      await page.evaluate(() => window.folio.refreshTasks());
      resultError = true;
      await page.locator('#report-created [data-chat-task]').click();
      await page.waitForFunction(() => (document.querySelector('#task-detail-body .run-report')?.textContent || '').includes(document.documentElement.lang.startsWith('zh') ? '报告正文暂不可用' : 'Report result unavailable'));
      await page.locator('#task-detail-close').click();
      resultError = false;
      await page.locator('#report-created [data-chat-task]').click();
      await visible(page, '#task-detail-body .run-report');
      await page.waitForFunction(() => (document.querySelector('#task-detail-body .run-report')?.textContent || '').includes('A readable result'));
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
      await page.locator('#report-created [data-chat-task]').click();
      await visible(page, '#task-detail-body .report-provenance');
      assert.match(await page.locator('#task-detail-body .report-provenance').textContent(), language === 'zh' ? /部分历史快照/ : /Partial historical snapshot/);
      await page.locator('#task-detail-close').click();
      historicalResult = false;
      // Older servers omit capability metadata; saved assistant bindings still
      // lead into the daily dashboard without repository or an installed runner.
      capabilities = false;
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      assert.equal(await page.locator('#daily-ask').isDisabled(), false);
      assert.equal(await page.locator('#daily-organize').isDisabled(), true);
      connected = savedConnected; authorized = savedAuthorized; capabilities = null; tasks = []; runnerInstalled = true; historyTasks = [];
      await page.goto(origin);
      await visible(page, '#dashboard-view');
      // The board uses real stages, explains waiting, filters, and opens an attempt drawer.
      reviewRun = { ...maintenance(), needs_review: [] };
      run = { ...maintenance(), needs_review: [], task_id: 't-000000000002', status: 'running', stage: 'organize' };
      tasks = [
        { id: 't-000000000001', kind: 'maintenance', title: 'Queued example', status: 'queued', enabled: true, priority: 'P2', due: true, source: 'hook:example' },
        { id: 't-000000000002', kind: 'maintenance', title: 'Running example', status: 'running', enabled: true, priority: 'P1', source: 'manual' },
        { id: 't-000000000003', kind: 'maintenance', title: 'Failed example', status: 'failed', priority: 'P0', reason: 'runner_network', source: 'manual' },
        { id: 't-000000000004', kind: 'maintenance', title: 'Done example', status: 'done', priority: 'P2', source: 'manual' },
      ];
      await page.locator('[data-view="review"]').click();
      await page.locator('#review-refresh').click();
      await page.locator('[data-view="dashboard"]').click();
      await page.waitForFunction(() => document.querySelectorAll('.task-board .task-item').length === 4);
      for (const column of ['current', 'attention', 'recent']) assert.equal(await page.locator(`[data-column="${column}"] .task-item`).count(), column === 'current' ? 2 : 1);
      assert.match(await page.locator('[data-column="current"] .task-progress').first().textContent(), language === 'zh' ? /空闲时段/ : /off-peak/);
      await page.locator('#task-search').fill('Failed');
      assert.equal(await page.locator('.task-board .task-item').count(), 1);
      await page.locator('#task-search').fill('');
      await page.locator('#task-priority-filter').selectOption('P0');
      assert.equal(await page.locator('.task-board .task-item').count(), 1);
      await page.locator('#task-priority-filter').selectOption('');
      await page.locator('[data-task-inspect="t-000000000002"]').click();
      await visible(page, '#task-detail');
      await visible(page, '#task-detail-body .run-head');
      assert.match(await page.locator('#task-detail-body .run-meta').innerText(), /\$0\.0123/);
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-task-drawer-${language}.png`, fullPage: true });
      assert.equal(await page.locator('#dashboard-view').isVisible(), true);
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
      await page.locator('[data-view="review"]').click();
      await page.locator('#review-refresh').click();
      await visible(page, '#review-list .review-finding');
      const links = page.locator('#review-list a');
      assert.equal(await links.count(), 2);
      assert.match(await links.nth(0).getAttribute('href'), new RegExp('/blob/' + sha + '/inbox/observation.md$'));
      assert.match(await links.nth(1).getAttribute('href'), /\/blob\/main\/inbox\/observation.md$/);
      assert.equal(await links.nth(0).getAttribute('rel'), 'noopener noreferrer');
      // Unknown model text is inert, and an unsafe path never becomes a link.
      reviewRun.needs_review.push({ source: '../bad.md', reason: '<img src=x onerror=alert(1)>' });
      await page.locator('#review-refresh').click();
      await page.waitForFunction(() => document.querySelectorAll('#review-list .review-finding').length === 2);
      assert.equal(await page.locator('#review-list img').count(), 0);
      assert.equal(await page.locator('#review-list a').count(), 2);
      reviewRun = maintenance();
      // Run details distinguish automatic backlog from human review.
      run = maintenance();
      await page.locator('[data-view="runs"]').click();
      await page.locator('#automation-refresh').click();
      await page.waitForFunction(() => document.getElementById('latest-run').textContent.includes('17'));
      assert.equal(await page.locator('#latest-run .review-finding').count(), 1);
      assert.equal(await page.locator('#latest-run .run-summary').first().isVisible(), false, 'raw model summary starts collapsed');
      // Keyboard navigation reaches the page heading, not a hidden setup control.
      await page.locator('[data-view="review"]').focus();
      await page.keyboard.press('Enter');
      await visible(page, '#review-view');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'review-heading');
      // Failed checks retain findings; successful checks refresh them via one run job.
      jobFailed = true;
      await page.locator('#review-recheck').click();
      await page.waitForFunction(() => document.getElementById('review-message').dataset.error === 'true');
      assert.equal(await page.locator('#review-list .review-finding').count(), 1);
      jobFailed = false;
      await page.locator('[data-view="dashboard"]').click();
      await page.locator('[data-view="review"]').click();
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
      for (const view of ['dashboard', 'chat', 'settings', 'review', 'runs']) {
        await page.locator(`[data-view="${view}"]`).click();
        await visible(page, `#${view}-view`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `mobile overflow in ${view}`);
        if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-${view}-mobile-${language}.png`, fullPage: true });
      }
      // Delayed history and report responses from a logged-out session must not
      // restore private data after a fresh login (even for the same owner).
      await page.setViewportSize({ width: 1440, height: 1000 });
      run = { run_id: reportID, status: 'reported', task_id: 'report-task', report_file: 'run-' + reportID + '.md', report_text: 'SHORT PREVIEW ONLY' };
      tasks = [{ id: 'report-task', title: 'Delayed synthetic report', kind: 'report', status: 'done', run_id: reportID }];
      holdHistory = true;
      await page.locator('[data-view="chat"]').click();
      await page.locator('[data-view="dashboard"]').click();
      await page.evaluate(() => window.folio.refreshTasks());
      holdResult = true;
      await page.locator('[data-task-inspect="report-task"]').click();
      await visible(page, '#task-detail-body .run-report');
      assert.equal(typeof releaseHistory, 'function');
      assert.equal(typeof releaseResult, 'function');
      await page.locator('#task-detail-close').click();
      await page.locator('#logout-button').click();
      await visible(page, '#login-view');
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
      expired = true;
      await page.locator('#review-refresh').evaluate(button => button.click());
      await visible(page, '#login-view');
      assert.equal(await page.locator('#app-view').isVisible(), false);
      assert.equal(await page.locator('#chat-input').inputValue(), '');
      assert.equal(await page.locator('#chat-log').textContent(), '');
      assert.equal(await page.locator('#report-request').inputValue(), '');
      assert.equal(await page.locator('#task-detail-body').textContent(), '');
      assert.deepEqual(errors, []);
      console.log(`${language}: onboarding, capability readiness, context prefill, task events/history, required report request/result/retry/follow-up, board/filter/drawer/actions, review/recheck, errors, language, mobile, expiry passed`);
      await context.close();
    }
  } finally { await browser.close(); }
}
main().catch(e => { console.error(e); process.exitCode = 1; }).finally(() => server.close());
