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
      let tasks = [];
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
          else body = { storage: { plugin: 'github', auth: authorized ? 'gh' : 'missing' },
            repository: { connected, full_name: 'example/context', branch: 'main', head: sha },
            model: { available: true }, runners: { codex: { available: true }, dsh: { available: false } }, service: {} };
        }
        else if (endpoint === '/api/plugins') body = { plugins: [{ id: 'github' }] };
        else if (endpoint.endsWith('/auth/start')) body = { status: 'pending', user_code: 'SYNTHETIC', interval: 30 };
        else if (endpoint.endsWith('/auth/poll')) { authorized = true; body = { status: 'authenticated', mode: 'gh' }; }
        else if (endpoint.endsWith('/status')) body = { status: authorized ? 'authenticated' : 'signed_out', mode: 'gh' };
        else if (endpoint.endsWith('/repositories')) body = { repositories: [{ full_name: 'example/context', private: true }], count: 1 };
        else if (endpoint === '/api/repository/connect') { connected = true; body = { connected: true }; }
        else if (endpoint === '/api/task-kinds') body = { kinds: [{ id: 'maintenance', label: 'Maintenance' }] };
        else if (endpoint === '/api/automation') body = { latest: reviewRun ? run : null, review_run: reviewRun, runs: reviewRun ? [run] : [], models: { codex: [] } };
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
          else body = { totals: { total_tokens: 12345, input_tokens: 10000, output_tokens: 2345, runs: 1, reported_runs: 1 }, days: [], models: [] };
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
        else if (endpoint === '/api/tasks') body = taskListing();
        else if (endpoint === '/api/channels') body = { bark: { configured: false }, deliveries: [] };
        else if (endpoint === '/api/hooks') body = { hooks: [] };
        else if (endpoint === '/api/quotas') body = quotaSnapshot();
        else if (endpoint === '/api/quotas/refresh') { quotaRefreshes++; body = quotaSnapshot(); }
        else throw new Error('Unexpected synthetic endpoint: ' + endpoint);
        await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      });
      const origin = `http://127.0.0.1:${server.address().port}`;
      await page.goto(origin);
      await visible(page, '#login-view');
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
      await page.locator('#setup-finish').click();
      await visible(page, '#dashboard-view');
      await visible(page, '#quotas-cards .quota-card');
      assert.equal(await page.locator('.quota-card-provider').textContent(), 'Synthetic Provider');
      assert.equal(await page.locator('.quota-window .quota-bar').count(), 1);
      await page.locator('#quotas-refresh').click();
      await page.waitForFunction(() => !document.getElementById('quotas-refresh').disabled);
      assert.equal(quotaRefreshes, 1);
      assert.equal(await page.locator('#repository-section').isVisible(), false);
      assert.equal(await page.locator('#channels-section').isVisible(), false);
      assert.equal(await page.locator('#usage-total').textContent(), '12,345');
      await page.locator('#tasks-pause').click();
      assert.equal(paused, true);
      await page.locator('#tasks-pause').click();
      assert.equal(paused, false);
      // A restored configured session starts on the daily dashboard.
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
      for (const column of ['queued', 'running', 'attention', 'done']) assert.equal(await page.locator(`[data-column="${column}"] .task-item`).count(), 1);
      assert.match(await page.locator('[data-column="queued"] .task-progress').textContent(), language === 'zh' ? /空闲时段/ : /off-peak/);
      await page.locator('#task-search').fill('Failed');
      assert.equal(await page.locator('.task-board .task-item').count(), 1);
      await page.locator('#task-search').fill('');
      await page.locator('#task-priority-filter').selectOption('P0');
      assert.equal(await page.locator('.task-board .task-item').count(), 1);
      await page.locator('#task-priority-filter').selectOption('');
      await page.locator('[data-task-inspect="t-000000000002"]').click();
      await visible(page, '#task-detail');
      await visible(page, '#task-detail-body .run-head');
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
      await page.locator('#run-options-summary').click();
      await page.locator('#model-input').fill('synthetic-selected-model');
      await page.locator('#run-options-summary').click();
      await page.locator('[data-view="review"]').click();
      await page.locator('#review-recheck').click();
      await page.waitForFunction(() => document.getElementById('review-recheck').disabled === false && document.querySelectorAll('#review-list .review-finding').length === 0);
      assert.deepEqual(runRequests.at(-1), { runner: 'codex', model: 'synthetic-selected-model' });
      assert.equal(jobRequests, 2);
      // Error and recovery states, both translations, and mobile overflow.
      usageError = true;
      await page.locator('[data-view="dashboard"]').click();
      await page.locator('#usage-window').selectOption('7');
      await visible(page, '#usage-message[data-error="true"]');
      usageError = false;
      await page.locator('#usage-window').selectOption('30');
      await page.waitForFunction(() => !document.getElementById('usage-message').textContent);
      const otherLanguage = language === 'zh' ? 'en' : 'zh';
      await page.locator(`[data-lang="${otherLanguage}"]`).click();
      assert.equal(await page.locator('[data-view="settings"]').textContent(), otherLanguage === 'zh' ? '设置' : 'Settings');
      await page.locator(`[data-lang="${language}"]`).click();
      if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-dashboard-${language}.png`, fullPage: true });
      await page.setViewportSize({ width: 390, height: 844 });
      for (const view of ['dashboard', 'settings', 'review', 'runs']) {
        await page.locator(`[data-view="${view}"]`).click();
        await visible(page, `#${view}-view`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `mobile overflow in ${view}`);
        if (process.env.FOLIO_SCREENSHOTS) await page.screenshot({ path: `/tmp/folio-${view}-mobile-${language}.png`, fullPage: true });
      }
      // Missing authorization preserves the selected repository and guides setup.
      authorized = false;
      await page.goto(origin);
      await visible(page, '#settings-view');
      assert.equal(await page.locator('#repository-summary').isVisible(), true);
      assert.equal(await page.locator('#repository-badge').getAttribute('data-state'), 'pending');
      // State-load failure still has navigation and a visible retryable error.
      stateError = true;
      await page.goto(origin);
      await visible(page, '#settings-view');
      await visible(page, '#app-message[data-error="true"]');
      stateError = false;
      // Expiry hides private app data and returns to Folio sign-in.
      expired = true;
      await page.locator('#review-refresh').evaluate(button => button.click());
      await visible(page, '#login-view');
      assert.equal(await page.locator('#app-view').isVisible(), false);
      assert.deepEqual(errors, []);
      console.log(`${language}: onboarding, status board/filter/drawer/actions, review/recheck, errors, language, mobile, expiry passed`);
      await context.close();
    }
  } finally { await browser.close(); }
}
main().catch(e => { console.error(e); process.exitCode = 1; }).finally(() => server.close());
