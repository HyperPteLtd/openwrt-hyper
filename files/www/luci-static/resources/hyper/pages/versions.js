import { t } from '../i18n.js';
import { h, diagnosticMessage } from '../dom.js';

// The kernel row: 检查更新, then 更新到 vX only when a newer stable release exists. The
// router downloads the core on its own, apart from the plugin: it picks a reachable download
// address and checks every download against the original release's SHA-256. A release reads
// as its version alone. Errors read in Chinese through diagnosticMessage, as in the log.
const noStable = 'publisher has no stable release';

export function bootstrapState(status) {
	if (status.selected || !status.bootstrap) return '';
	switch (status.bootstrap.stage) {
	case 'running': return t('正在下载');
	case 'failed':
	case 'interrupted': return t('下载失败');
	default: return '';
	}
}

async function latestStable(ui) {
	const versions = (await ui.run('kernel_read', { action: 'versions' })) || [];
	return versions.find((v) => v.installable && !v.prerelease);
}

async function refresh(ui, updates) {
	const next = await ui.rpc('kernel_read', { action: 'status' });
	updates.dispatchEvent(new CustomEvent('change', { detail: next }));
	return next;
}

// Where the kernel lives: flash keeps the previous version for rollback, memory saves
// flash and downloads again after a reboot.
const storageModes = [['double', t('闪存')], ['tmp', t('内存')]];
const storageOf = (status) => (status?.mode === 'tmp' ? 'tmp' : 'double');

// Download and install the newest stable kernel; returns the new status.
export async function installLatest(ui, updates, mode = 'double') {
	const latest = await latestStable(ui);
	if (!latest) throw Error(noStable);
	await ui.run('kernel_write', { action: 'install-json', args: { tag: latest.tag, mode } });
	return refresh(ui, updates);
}

// The storage row: switching reinstalls the selected version in the other location.
export function kernelStorage(ui, status, updates, segment) {
	const note = h('small', { class: 'ow-muted', role: 'status' });
	const describe = (mode) => {
		note.textContent = mode === 'tmp'
			? t('不占闪存，重启后重新下载')
			: t('重启保留，可回退上一版');
		delete note.dataset.tone;
	};
	describe(storageOf(status));
	const control = segment(ui, t('内核存储位置'), storageModes, storageOf(status), async (mode) => {
		if (!status.selected) return describe(mode);
		note.textContent = t('正在切换…');
		delete note.dataset.tone;
		try {
			await ui.run('kernel_write', { action: 'install-json', args: { tag: status.selected, mode } });
			status = await refresh(ui, updates);
			describe(storageOf(status));
		} catch (error) {
			note.textContent = diagnosticMessage(error.message);
			note.dataset.tone = 'error';
		}
	});
	updates.addEventListener('change', (e) => { status = e.detail; }, { signal: ui.signal });
	return [control, note];
}

export function kernelSummary(ui, status, updates) {
	const label = h('span', { role: 'status' }, t('内核'));
	const note = h('small', { class: 'ow-muted', role: 'status' });
	const action = ui.button('', () => {}, 'ow-link');
	let latest = null, checked = false, busy = '';
	async function act(text, work) {
		busy = text;
		note.textContent = '';
		delete note.dataset.version;
		update(status);
		try {
			await work();
		} catch (error) {
			if (error.name !== 'AbortError') {
				note.textContent = diagnosticMessage(error.message);
				note.dataset.tone = 'error';
			}
		} finally {
			busy = '';
			update(status);
		}
	}
	action.addEventListener('click', () => {
		if (status.staged && status.staged !== status.selected)
			return act(t('正在切换…'), async () => {
				await ui.run('kernel_write', { action: 'select', args: { tag: status.staged } });
				status = await refresh(ui, updates);
			});
		if (!status.selected || latest)
			return act(status.selected ? t('正在更新…') : t('正在下载…'), async () => {
				status = await installLatest(ui, updates, storageOf(status));
				latest = null;
				checked = true;
			});
		return act(t('正在检查…'), async () => {
			const found = await latestStable(ui);
			checked = true;
			latest = found && found.tag !== status.selected ? found : null;
		});
	});
	function update(next) {
		status = next;
		const state = bootstrapState(status);
		// The row reads like the plugin row: a title, the version underneath.
		const current = status.selected || state || t('未安装');
		if (note.dataset.version && (status.pending || state === t('下载失败'))) {
			note.textContent = '';
			delete note.dataset.version;
		}
		if (!note.textContent) delete note.dataset.tone;
		if (status.pending && !note.textContent) note.textContent = t('新内核正在确认运行正常');
		const failure = state === t('下载失败') && (status.bootstrap.result?.error || status.bootstrap.error);
		if (failure && !busy && !note.textContent) {
			note.textContent = diagnosticMessage(failure);
			note.dataset.tone = 'error';
		}
		if (!note.textContent || note.dataset.version) {
			note.textContent = status.selected ? t('当前 {0}', status.selected) : current;
			note.dataset.version = '1';
			delete note.dataset.tone;
		}
		let name = busy;
		if (!name) {
			if (status.staged && status.staged !== status.selected) name = t('使用 {0}', status.staged);
			else if (!status.selected) name = state === t('正在下载') ? '' : state ? t('重新下载') : t('下载内核');
			else if (latest) name = t('更新到 {0}', latest.tag);
			else name = checked ? t('已是最新') : t('检查更新');
		}
		if (action.textContent !== name) action.textContent = name;
		action.hidden = !name;
		action.disabled = !!busy || name === t('已是最新');
		action.classList.toggle('ow-primary', !busy && (!!latest || !status.selected));
		action.classList.toggle('ow-link', !(!busy && (!!latest || !status.selected)));
	}
	update(status);
	updates.addEventListener('change', (e) => update(e.detail), { signal: ui.signal });
	return [label, action, note];
}
