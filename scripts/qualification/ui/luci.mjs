// Run only against the disposable loopback router provisioned by the package lab.
import assert from 'node:assert/strict';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import fs from 'node:fs/promises';
import path from 'node:path';
const {chromium}=await import(process.env.HYPER_PLAYWRIGHT || 'playwright');
const base=new URL(process.env.HYPER_LUCI_URL);
if(!['127.0.0.1','[::1]','localhost'].includes(base.hostname))throw Error('this test requires a disposable local router');
const output=process.env.HYPER_UI_OUTPUT || 'build/ui-check';
await fs.mkdir(output,{recursive:true});
const pw=(await fs.readFile(process.env.HYPER_LUCI_PASSWORD_FILE,'utf8')).trim();
const browser=await chromium.launch({executablePath:process.env.HYPER_CHROME || undefined});
try {
	const page=await browser.newPage({viewport:{width:1280,height:900},locale:'zh-CN'}),errors=[];
	page.setDefaultTimeout(15000);
	let authenticated=false;
	page.on('pageerror',e=>{if(authenticated)errors.push(e.message);});
	await page.goto(base.href);await page.locator('[name=luci_username]').fill(process.env.HYPER_LUCI_USER || 'preview');
	await page.locator('[name=luci_password]').fill(pw);await page.locator('[name=luci_password]').press('Enter');
	await page.locator('.ow .ow-main > *').waitFor();authenticated=true;
	if(process.env.HYPER_LUCI_EXPECT_BOOTSTRAP_FAILURE) {
		// The package lab controls the upstream Release response for this case.
		const limited=process.env.HYPER_LUCI_EXPECT_BOOTSTRAP_FAILURE === 'rate-limit';
		const setup=page.getByRole('dialog',{name:'启用全屋代理'});
		const source=setup.getByLabel('粘贴订阅链接');
		if(await setup.isVisible()) await source.fill('https://example.invalid/draft');
		const owner=await setup.isVisible() ? setup : page.locator('.ow-main');
		const retry=owner.getByRole('button',{name:'重新下载'});
		await retry.waitFor({timeout:90000});
		if(await setup.isVisible()) assert.equal(await source.inputValue(),'https://example.invalid/draft');
		await owner.getByText(limited ? 'GitHub 查询次数已达上限，请稍后重试' : /无法|暂时|未通过/).first().waitFor();
		await page.keyboard.press('Escape');
		if(await setup.isVisible()) assert.equal(await source.inputValue(),'https://example.invalid/draft');
	}
	async function checkRecoveryPreference() {
		const setup=page.getByRole('dialog',{name:'启用全屋代理'});
		if(await setup.isVisible()) await page.keyboard.press('Escape');
		const container=process.env.HYPER_LUCI_TEST_CONTAINER;
		const docker=['--context',process.env.HYPER_DOCKER_CONTEXT || 'default','exec',container];
		const run=promisify(execFile);
		if(container) {
			await page.getByRole('button',{name:'检查运行环境'}).click();
			const dialog=page.getByRole('dialog',{name:'防火墙运行环境'});
			await dialog.waitFor();
			const env=JSON.parse((await run('docker',[...docker,'ubus','call','hyper','environment'])).stdout);
			const text=await dialog.innerText();
			assert.equal(text.includes('需要处理'),Boolean(env.network_requirements?.length));
			assert.equal(text.includes('可提供所缺功能的软件包'),Boolean(env.network_requirements?.length && env.firewall_packages?.length));
			for(const missing of env.network_requirements || []) assert(!text.includes(missing), 'untranslated network capability');
			for(const name of env.firewall_packages || []) assert(text.includes(name), 'missing package hint');
			await page.keyboard.press('Escape');
		}
		const label='代理无响应时回退直连';
		const control=page.getByRole('switch',{name:label});
		await control.waitFor();
		const initial=(await control.getAttribute('aria-checked'))==='true';
		for (const [state,width,height] of [[!initial,1280,900],[initial,390,844]]) {
			await page.setViewportSize({width,height});
			await control.click();
			const saved=page.getByRole('switch',{name:label,checked:state});
			await saved.waitFor();
			await page.reload();
			await saved.waitFor();
			if(container) {
				const [file, uci]=await Promise.all([
					run('docker',[...docker,'cat','/etc/config/hyper']),
					run('docker',[...docker,'uci','-q','get','hyper.main.fail_open']),
				]);
				const disk=/^\s*option\s+fail_open\s+['"]?([01])['"]?\s*$/m.exec(file.stdout)?.[1];
				assert.deepEqual([disk,uci.stdout.trim()], state ? ['1','1'] : ['0','0'], 'raw UCI bytes and readback');
			}
			assert.match(await saved.getAttribute('aria-describedby'),/^ow-fail-open-help$/);
			assert.equal(await page.locator('.ow').evaluate(el=>el.scrollWidth>el.clientWidth+1),false);
			await saved.scrollIntoViewIfNeeded();
			await page.screenshot({path:path.join(output,'luci-recovery-'+width+'.png')});
		}
	}
	if (!process.argv.includes('--recovery-only')) {
		const id='router-ui-test',name='LAN QA';
		const first=await page.getByRole('heading',{name:'启用全屋代理'}).count();
		if(first){
		await page.screenshot({path:path.join(output,'luci-onboarding.png'),fullPage:true});
		await page.locator('.ow-onboarding input[type=file]').setInputFiles({name:id+'.txt',mimeType:'text/plain',buffer:Buffer.from('http://127.0.0.1:3128#'+encodeURIComponent(name)+'\nhttp://127.0.0.1:3129#'+encodeURIComponent('LAN QA 2'))});
		}
		await page.locator('.ow .ow-plan-label').filter({hasText:id}).waitFor({timeout:30000});
		await page.locator('.ow').getByRole('button',{name:'全局',exact:true}).click();
		await page.locator('.ow').getByRole('link',{name:'代理',exact:true}).click();
		await page.locator('.ow').getByRole('button',{name:/LAN QA 2/}).click();
		await page.locator('.ow button.ow-node[aria-pressed=true]').filter({hasText:'LAN QA 2'}).waitFor();
		await page.reload();await page.locator('.ow button.ow-node[aria-pressed=true]').filter({hasText:'LAN QA 2'}).waitFor();
		for(const size of [{width:1280,height:900},{width:390,height:844}]) {
			await page.setViewportSize(size);
			for(const [key,name] of [['overview','概览'],['proxy','代理'],['subscriptions','订阅'],['rules','规则'],['logs','日志'],['connections','连接记录']]){
				await page.locator('.ow nav').getByRole('link',{name,exact:true}).click();
				await page.locator('.ow nav a[href="#'+key+'"][aria-current=page]').waitFor();
				await page.locator('.ow-main[aria-busy=false]').waitFor();
				const anchor=page.locator('.ow nav a[aria-current=page]');assert.equal(await anchor.textContent(),name);
				const text=await page.locator('.ow-main').innerText();assert(text.trim() && !text.includes('无法加载页面'),key+': '+text.slice(0,200));
				assert.equal(await page.locator('.ow').evaluate(el=>el.scrollWidth>el.clientWidth+1),false,key+' plugin overflow');
				await page.screenshot({path:path.join(output,'luci-'+key+'-'+size.width+'.png'),fullPage:false});
			}
		}
		await page.locator('.ow nav').getByRole('link',{name:'日志',exact:true}).focus();await page.keyboard.press('Enter');await page.getByRole('heading',{name:'日志',exact:true}).waitFor();
		await page.locator('.ow nav').getByRole('link',{name:'概览',exact:true}).click();
	}
	await checkRecoveryPreference();
	assert.deepEqual(errors,[]);
	if(process.argv.includes('--recovery-only')) console.log('Native LuCI fail-open toggle persisted across reloads on desktop and mobile');
	else console.log('Native LuCI login, onboarding file import, persistent stopped selection, six routes, keyboard and two screen sizes passed');
}finally{await browser.close();}
