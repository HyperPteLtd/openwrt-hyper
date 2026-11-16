#!/usr/bin/ucode
'use strict';

import { readfile, writefile, rename, unlink } from 'fs';
import { readSession } from 'hyper';
import { md5 } from 'md5';

const id = ARGV[0];
if (!match(id || '', /^[0-9a-f-]{36}$/)) exit(1);
const root = '/var/run/hyper-tasks';
const dir = `${root}/${id}`;
let req;
try { req = json(readfile(`${dir}/request.json`) || 'null'); } catch (e) { exit(1); }
if (!req || !(req.method in ['api', 'diag', 'health_check', 'check_update', 'install_update'])) exit(1);
/* Keep the request as task_status's fallback until metadata is fully visible. */
if (!writefile(`${dir}/meta.tmp`, sprintf('%J', { identity: req.identity, time: req.time, timeout: req.timeout })) ||
	!rename(`${dir}/meta.tmp`, `${dir}/meta.json`)) exit(1);
/* Sensitive request arguments are consumed once and never kept in task results. */
unlink(`${dir}/request.json`);

function sameIdentity() { return req.identity === md5(readSession()?.jwt || ''); }
let result;
try {
	if (!sameIdentity()) result = { state: 'failed', error: 'session_changed' };
	else {
		const methods = loadfile('/usr/share/rpcd/ucode/luci.hyper')()['luci.hyper'];
		const data = methods[req.method].call({ args: req.args });
		result = sameIdentity() ? { state: 'done', data } : { state: 'failed', error: 'session_changed' };
	}
} catch (e) { result = { state: 'failed', error: 'task_failed' }; }
writefile(`${dir}/result.tmp`, sprintf('%J', result));
rename(`${dir}/result.tmp`, `${dir}/result.json`);
/* Bound RAM retention without touching another worker's fresh task. */
system(`find ${root} -mindepth 1 -maxdepth 1 -type d -mmin +10 -exec rm -rf {} +`);
