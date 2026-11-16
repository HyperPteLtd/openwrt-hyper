/** Task dispatch and worker identity boundaries on the firmware's actual ucode. */
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { available, command } from './router_test_helpers.mjs';

const ucode = process.env.UCODE || 'ucode';
if (!available(ucode) && !existsSync(ucode)) {
  console.log('skip RPC tasks (requires ucode; covered by router tests)');
  process.exit(0);
}
const repo = fileURLToPath(new URL('../', import.meta.url));
const scripts = join(repo, 'luci-app-hyper/root/etc/hyper/scripts');
const root = mkdtempSync(join(tmpdir(), 'hyper-rpc-task-'));
const backendFile = join(root, 'backend.uc');
const backendSource = readFileSync(join(repo, 'luci-app-hyper/root/usr/share/rpcd/ucode/luci.hyper'), 'utf8');
const env = { ...process.env, HYPER_RPC_TASK: '', HYPER_TASK_TEST_SESSION: join(root, 'session.json') };
const flags = ['-S', '-L', join(repo, 'tests/stubs'), '-L', root, '-L', scripts, '-L', process.env.UCODE_LIB || '/usr/lib/ucode'];
const run = (source, extra = {}) => command(ucode, [...flags, '-e', source], { env: { ...env, ...extra } });
const writeJSON = (path, data) => writeFileSync(path, JSON.stringify(data));
const digest = value => createHash('md5').update(value).digest('hex');
try {
  // Keep the production method table and wrapper factory. Stub only enqueue I/O
  // so a dispatch regression cannot execute an installation during this test.
  const source = backendSource.replace(
    /function queueTask\(method, args\) \{[\s\S]*?\n\}/,
    'function queueTask(method, args) { return { queued: method, args }; }',
  ).replaceAll('/etc/hyper/scripts/', scripts + '/');
  assert.notEqual(source, backendSource, 'enqueue fixture must match the production function');
  writeFileSync(backendFile, source);
  const names = ['api', 'diag', 'health_check', 'check_update', 'install_update'];
  const dispatched = JSON.parse(run(
    'const m=loadfile(' + JSON.stringify(backendFile) + ')()["luci.hyper"]; ' +
    'let result=[]; for(let name in ' + JSON.stringify(names) + ') push(result,m[name].call({args:{marker:name}})); printf("%J",result);',
  ));
  assert.deepEqual(dispatched, names.map(name => ({ queued: name, args: { marker: name } })));
  console.log('ok   every deferred method retains its own dispatch identity');

  const direct = JSON.parse(run(
    'const m=loadfile(' + JSON.stringify(backendFile) + ')()["luci.hyper"]; printf("%J",m.api.call({args:{path:"/not-allowed"}}));',
    { HYPER_RPC_TASK: '1' },
  ));
  assert.equal(direct.error, 'path not allowed');
  assert.equal(direct.queued, undefined);
  console.log('ok   background task mode invokes the method instead of enqueueing recursively');

  const queueRoot = join(root, 'queue'); mkdirSync(queueRoot);
  const queueBackend = join(root, 'queue-backend.uc');
  writeFileSync(queueBackend, backendSource.replaceAll('/etc/hyper/scripts/', scripts + '/')
    .replace("const TASK_DIR = '/var/run/hyper-tasks';", 'const TASK_DIR = ' + JSON.stringify(queueRoot) + ';')
    + '\nfunction system(command) { return 0; }\n');
  const queued = JSON.parse(run(
    "import {readfile,writefile} from 'fs'; " +
    'const m=loadfile(' + JSON.stringify(queueBackend) + ')()["luci.hyper"]; let out=[]; ' +
    'for(let name in ["check_update","install_update"]) { const ack=m[name].call({args:{}}); ' +
    'const path=' + JSON.stringify(queueRoot + '/') + '+ack._hyper_task+"/request.json"; const req=json(readfile(path)); ' +
    'req.time=time()-200; writefile(path,sprintf("%J",req)); push(out,{ack,status:m.task_status.call({args:{id:ack._hyper_task}}),timeout:req.timeout}); } printf("%J",out);',
  ));
  assert.equal(queued[0].ack._hyper_timeout, 180);
  assert.equal(queued[0].timeout, 180);
  assert.deepEqual(queued[0].status, { state: 'failed', error: 'timeout' });
  assert.equal(queued[1].ack._hyper_timeout, 480);
  assert.equal(queued[1].timeout, 480);
  assert.deepEqual(queued[1].status, { state: 'running' });
  console.log('ok   queue acknowledgment and status allow the installation download deadline');

  const tasks = join(root, 'tasks'); mkdirSync(tasks);
  const called = join(root, 'called');
  writeFileSync(join(root, 'task_session.uc'),
    "import { readfile } from 'fs'; export function readSession(){return json(readfile(getenv('HYPER_TASK_TEST_SESSION')) || 'null');};\n");
  const workerFile = join(root, 'worker.uc');
  const methodFile = join(root, 'methods.uc');
  const worker = readFileSync(join(scripts, 'rpc_task.uc'), 'utf8')
    .replace("from 'hyper'", "from 'task_session'")
    .replace("const root = '/var/run/hyper-tasks';", 'const root = ' + JSON.stringify(tasks) + ';')
    .replace("loadfile('/usr/share/rpcd/ucode/luci.hyper')", 'loadfile(' + JSON.stringify(methodFile) + ')');
  writeFileSync(workerFile, worker);
  let sequence = 0;
  function request(identity = 'account-a', extra = {}) {
    const id = '12345678-1234-1234-1234-' + String(++sequence).padStart(12, '0');
    const dir = join(tasks, id); mkdirSync(dir);
    writeJSON(join(dir, 'request.json'), { method: 'api', args: { password: 'test-secret' },
      identity: digest(identity), time: Math.floor(Date.now() / 1000), timeout: 180, ...extra });
    return { id, dir };
  }
  const execute = task => {
    command(ucode, [...flags, workerFile, task.id], { env: { ...env, HYPER_RPC_TASK: '1' } });
    return JSON.parse(readFileSync(join(task.dir, 'result.json'), 'utf8'));
  };
  writeJSON(env.HYPER_TASK_TEST_SESSION, { jwt: 'account-a' });
  writeFileSync(methodFile, "import { writefile } from 'fs'; return {'luci.hyper':{api:{call:function(req){" +
    'writefile(' + JSON.stringify(called) + ',"called"); return {ok:true,value:"ready"};}}}};\n');
  const good = request();
  assert.deepEqual(execute(good), { state: 'done', data: { ok: true, value: 'ready' } });
  assert.equal(existsSync(join(good.dir, 'request.json')), false);
  assert.equal(existsSync(join(good.dir, 'meta.tmp')), false);
  const metadata = readFileSync(join(good.dir, 'meta.json'), 'utf8');
  assert.equal(JSON.parse(metadata).timeout, 180);
  assert.equal(metadata.includes('test-secret'), false);
  console.log('ok   worker consumes private arguments and publishes complete metadata/result');

  rmSync(called);
  const obsolete = request('old-account');
  assert.deepEqual(execute(obsolete), { state: 'failed', error: 'session_changed' });
  assert.equal(existsSync(called), false);
  console.log('ok   obsolete account tasks never reach the method');

  writeFileSync(methodFile, "import { writefile } from 'fs'; return {'luci.hyper':{api:{call:function(req){" +
    'writefile(' + JSON.stringify(env.HYPER_TASK_TEST_SESSION) + ',\'{"jwt":"account-b"}\'); return {private:"old-account-data"};}}}};\n');
  const changed = request();
  assert.deepEqual(execute(changed), { state: 'failed', error: 'session_changed' });
  assert.equal(readFileSync(join(changed.dir, 'result.json'), 'utf8').includes('old-account-data'), false);
  console.log('ok   identity changes during work discard the previous account result');

  writeJSON(env.HYPER_TASK_TEST_SESSION, { jwt: 'account-a' });
  writeFileSync(methodFile, "return {'luci.hyper':{api:{call:function(req){die('private internal failure');}}}};\n");
  const failed = request();
  assert.deepEqual(execute(failed), { state: 'failed', error: 'task_failed' });
  console.log('ok   method failures publish a terminal result without leaking internal errors');
} finally {
  rmSync(root, { recursive: true, force: true });
}
