// Exercise the normal LuCI forms against a disposable local Hyper site.
// Give the browser credential files rather than passing their contents in argv.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
const { chromium } = await import(process.env.HYPER_PLAYWRIGHT || 'playwright');
const options = process.env;

function localURL(value) {
    const url = new URL(value);
    if (!['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)) throw Error('a loopback fixture is required');
    return url;
}

const site = localURL(options.HYPER_PLATFORM_ORIGIN);
const luci = localURL(options.HYPER_LUCI_URL);
const share = localURL((await readFile(options.HYPER_PLATFORM_SHARE_FILE, 'utf8')).trim());
if (site.protocol !== 'https:' || site.origin !== share.origin) throw Error('expected one local HTTPS fixture');
const account = JSON.parse(await readFile(options.HYPER_PLATFORM_MEMBER_FILE, 'utf8'));
const password = (await readFile(options.HYPER_LUCI_PASSWORD_FILE, 'utf8')).trim();
const browser = await chromium.launch({ executablePath: options.HYPER_CHROME || undefined });
let step = '';
async function phase(name) {
    step = name;
    if (options.HYPER_HTTP_AUDIT_PHASE_FILE) await writeFile(options.HYPER_HTTP_AUDIT_PHASE_FILE, name);
}

try {
    const page = await browser.newPage({ viewport: { width: 1366, height: 900 }, locale: 'zh-CN' });
    const errors = [];
    page.setDefaultTimeout(20000);
    await phase('luci-login');
    await page.goto(luci.href);
    await page.locator('[name=luci_username]').fill(options.HYPER_LUCI_USER || 'preview');
    await page.locator('[name=luci_password]').fill(password);
    await page.locator('[name=luci_password]').press('Enter');
    await page.locator('.ow nav').getByRole('link', { name: '订阅', exact: true }).click();
    page.on('pageerror', e => errors.push(e.name));
    const add = page.getByRole('button', { name: '＋ 添加订阅', exact: true });
    const modal = page.locator('dialog.ow-modal[open]');
    await phase('platform-import');
    await add.click();
    await modal.getByRole('button', { name: '服务商登录', exact: true }).click();
    await modal.getByLabel('服务商地址').fill(site.origin);
    await modal.getByRole('button', { name: '验证服务商' }).click();
    await modal.getByLabel('邮箱').fill(account.email);
    await modal.getByLabel('密码', { exact: true }).fill(account.password);
    const initiallySelected = await page.locator('.ow-source-card').filter({ hasText:'正在使用' }).count();
    await modal.getByRole('button', { name: '登录', exact: true }).click();
    if (initiallySelected) await modal.getByRole('button', { name: '使用', exact: true }).click();
    await modal.waitFor({ state: 'detached', timeout: 180000 });
    const platform = page.locator('.ow-source-card').filter({ hasText: '服务商账户' }).filter({ hasText: '正在使用' });
    const count = Number(await platform.locator('.ow-mono').innerText());
    assert.ok(count > 0);

    await phase('external-import');
    const name = '本机公开链接 · sing-box';
    await add.click();
    await modal.getByRole('button', { name: '订阅链接', exact: true }).click();
    await modal.getByRole('textbox', { name: '名称', exact: true }).fill(name);
    await modal.getByRole('textbox', { name: '订阅链接', exact: true }).fill(share.href);
    await modal.getByLabel('订阅请求 UA').selectOption('sing-box');
    await modal.getByRole('button', { name: '添加并导入' }).click();
    await modal.waitFor({ state: 'detached', timeout: 180000 });

    await phase('readback');
    await page.reload();
    const external = page.locator('.ow-source-card').filter({ has: page.getByText(name, { exact: true }) });
    assert.equal(Number(await platform.locator('.ow-mono').innerText()), count);
    assert.equal(Number(await external.locator('.ow-mono').innerText()), count);
    assert.equal(await external.locator('.ow-source-kind').innerText(), '链接订阅');
    assert.match(await external.locator('.ow-source-meta').innerText(), /sing-box/);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ platformNodes: count, externalNodes: count, persistedAfterReload: true, uncaughtErrors: 0 }));
} catch (error) {
    // Playwright can quote filled credentials in its call log.
    const position = String(error.stack || '').match(/platform\.mjs:(\d+):\d+/)?.[1] || '?';
    console.error('LuCI platform import failed at ' + step + ', line ' + position + ' (' + error.name + ')');
    process.exitCode = 1;
} finally {
    await browser.close();
}
