#!/usr/bin/ucode
// SPDX-License-Identifier: GPL-2.0-only
'use strict';

import { mkdir, open, readfile, rename, unlink, writefile } from 'fs';
import { cursor } from 'uci';
import { matchWhole, shellQuote } from '/etc/hyper/scripts/hyper.uc';
import { updateDue, runAutoUpdate } from '/etc/hyper/scripts/software_update.uc';

const dir = '/var/run/hyper-software-update';
mkdir(dir, 0700);

function settings() {
	const uci = cursor();
	uci.load('hyper');
	return {
		auto_check: uci.get('hyper', 'update', 'auto_check') !== '0',
		auto_update: uci.get('hyper', 'update', 'auto_update') === '1'
	};
}

/* A separate ucode process keeps the same lock after rpcd returns, including
 * while the package replaces this script. No external flock utility is needed. */
function installPreparedUpdate() {
	const lock = open(dir + '/install.lock', 'we', 0600);
	if (!lock?.lock('x'))
		return;
	let job = null;
	try { job = json(readfile(dir + '/install.json') || 'null'); } catch (e) {}
	if (!job) {
		lock.close();
		return;
	}
	const state = { state: 'failed', version: job.version, time: time() };
	let attempted = false;
	try {
		const valid = (job.manager in ['opkg', 'apk']) && type(job.files) === 'array' && length(job.files) > 0
			&& !length(filter(job.files, (path) => type(path) !== 'string'
				|| substr(path, 0, length(dir + '/download/')) !== dir + '/download/'
				|| !matchWhole(substr(path, length(dir + '/download/')), /^[A-Za-z0-9._+-]+$/)));
		if (!valid)
			state.error = 'update_failed';
		else if (job.automatic && !settings().auto_update)
			state.error = 'automatic_update_disabled';
		else {
			const command = job.manager === 'apk' ? '/usr/bin/apk add --allow-untrusted' : '/bin/opkg install';
			attempted = true;
			const result = system(`${command} ${join(' ', map(job.files, shellQuote))} >${dir}/install.log 2>&1`);
			state.state = result === 0 ? 'done' : 'failed';
		}
	} catch (e) {
		state.error = 'update_failed';
	}
	// Remove staged files before publishing completion or releasing the lock.
	system(`rm -rf ${dir}/download`);
	unlink(dir + '/install.json');
	state.time = time();
	if (writefile(dir + '/state.tmp', sprintf('%J\n', state)))
		rename(dir + '/state.tmp', dir + '/state.json');
	lock.close();
	if (attempted)
		system('/etc/init.d/rpcd reload >/dev/null 2>&1');
}

if (ARGV[0] === 'install') {
	installPreparedUpdate();
	exit(0);
}

const lock = open(dir + '/scheduler.lock', 'we', 0600);
if (!lock?.lock('xn'))
	exit(0);

let previous = null;
try { previous = json(readfile(dir + '/check.json') || 'null'); } catch (e) {}
if (!updateDue(settings(), previous?.time, time(), ARGV[0] === 'force'))
	exit(0);

/* Record attempts, including failed requests, so a disconnected router does
 * not retry every hour. All downloads run here, outside the rpcd event loop. */
writefile(dir + '/check.json', sprintf('%J\n', { time: time(), latest: previous?.latest ?? null }));
const methods = loadfile('/usr/share/rpcd/ucode/luci.hyper')()['luci.hyper'];
runAutoUpdate(settings, methods.check_update.call, methods.install_update.call);
