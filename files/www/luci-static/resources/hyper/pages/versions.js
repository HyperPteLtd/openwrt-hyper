import { t } from '../i18n.js';
import { h, diagnosticMessage } from '../dom.js';

// The core comes with the plugin (/usr/share/hyper/core): installing it reads the package's
// copy, checked against the package's SHA-256, and a plugin update brings the next version.
// Errors read in Chinese through diagnosticMessage, as in the log.
export function bootstrapState(status) {
	if (status.selected || !status.bootstrap) return '';
	switch (status.bootstrap.stage) {
	case 'running': return t('正在安装');
	case 'failed':
	case 'interrupted': return t('安装失败');
	default: return '';
	}
}

async function refresh(ui, updates) {
	const next = await ui.rpc('kernel_read', { action: 'status' });
	updates.dispatchEvent(new CustomEvent('change', { detail: next }));
	return next;
}

// Where the kernel lives: flash keeps it across reboots, memory saves flash and unpacks it from
// the plugin again after a reboot.
const storageModes = [['double', t('闪存')], ['tmp', t('内存')]];
const storageOf = (status) => (status?.mode === 'tmp' ? 'tmp' : 'double');

// Install the plugin's own kernel; returns the new status.
export async function installBundled(ui, updates, status, mode = 'double') {
	if (!status.bundled) throw Error(t('插件没有附带内核，请重新安装插件'));
	await ui.run('kernel_write', { action: 'install-json', args: { tag: status.bundled, mode } });
	return refresh(ui, updates);
}

// The storage row: switching reinstalls the selected version in the other location.
export function kernelStorage(ui, status, updates, segment) {
	const note = h('small', { class: 'ow-muted', role: 'status' });
	const describe = (mode) => {
		note.textContent = mode === 'tmp'
			? t('不占闪存，重启后从插件重新解压')
			: t('重启后保留');
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

export function kernelSummary(ui, status, updates, setup = false) {
	const label = h('span', { role: 'status' });
	const note = h('small', { class: 'ow-muted', role: 'status' });
	const action = ui.button('', () => {}, 'ow-link');
	let busy = '';
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
		if (!status.selected)
			return act(t('正在安装…'), async () => {
				status = await installBundled(ui, updates, status, storageOf(status));
			});
	});
	function update(next) {
		status = next;
		const state = bootstrapState(status);
		// Outside setup the row reads like the plugin row: a title, the version underneath.
		const current = status.selected || state || t('未安装');
		const text = setup ? current : t('内核');
		if (label.textContent !== text) label.textContent = text;
		if (note.dataset.version && (status.pending || state === t('安装失败'))) {
			note.textContent = '';
			delete note.dataset.version;
		}
		if (!note.textContent) delete note.dataset.tone;
		if (status.pending && !note.textContent) note.textContent = t('新内核正在确认运行正常');
		const failure = state === t('安装失败') && (status.bootstrap.result?.error || status.bootstrap.error);
		if (failure && !busy && !note.textContent) {
			note.textContent = diagnosticMessage(failure);
			note.dataset.tone = 'error';
		}
		if (!setup && (!note.textContent || note.dataset.version)) {
			note.textContent = status.selected ? t('当前 ') + status.selected + t(' · 随插件更新') : current;
			note.dataset.version = '1';
			delete note.dataset.tone;
		}
		let name = busy;
		if (!name) {
			if (status.staged && status.staged !== status.selected) name = t('使用 ') + status.staged;
			else if (!status.selected) name = state === t('正在安装') ? '' : state ? t('重新安装') : t('安装内核');
		}
		if (action.textContent !== name) action.textContent = name;
		action.hidden = !name;
		action.disabled = !!busy;
		action.classList.toggle('ow-primary', !busy && !status.selected);
		action.classList.toggle('ow-link', !(!busy && !status.selected));
	}
	update(status);
	updates.addEventListener('change', (e) => update(e.detail), { signal: ui.signal });
	return [label, action, note];
}
