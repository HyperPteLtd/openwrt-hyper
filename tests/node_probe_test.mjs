/** Real independent core against loopback HTTP proxies, inside router-test only. */
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createServer as createSecureServer } from 'node:https';
import { fileURLToPath } from 'node:url';
import { connect } from 'node:net';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { command, sleep, until } from './router_test_helpers.mjs';
assert.equal(process.env.HYPER_TEST_CONTAINER, '1', 'run only in disposable router-test');
const root = mkdtempSync(join(tmpdir(), 'hyper-probe-test-'));
const config = '/etc/config/hyper', init = '/etc/init.d/hyper', runtime = '/var/run/hyper/sing-box-c.json';
const originalConfig = readFileSync(config), originalInit = readFileSync(init);
const originalRuntime = existsSync(runtime) ? readFileSync(runtime) : undefined;
const servers = [], sockets = new Set();
let active = 0, maximum = 0, slow = false;
const certificate = fileURLToPath(new URL('./fixtures/probe-cert.pem', import.meta.url));
const fixture = createSecureServer({ cert: readFileSync(certificate), key: readFileSync(new URL('./fixtures/probe-key.pem', import.meta.url)) }, (req, res) => { res.writeHead(204, { Connection: 'close' }); res.end(); });
async function listen(server) {
  servers.push(server);
  server.on('connection', socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)); });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve)); return server.address().port;
}
const endpoint = await listen(fixture);
const env = { ...process.env, SSL_CERT_FILE: certificate, HYPER_PROBE_URL: `https://127.0.0.1:${endpoint}/generate_204` };
const rpc = (method, args = {}) => JSON.parse(command('ucode', ['-S', '-e', `const m=loadfile('/usr/share/rpcd/ucode/luci.hyper')()['luci.hyper']; printf('%J',m.${method}.call({args:${JSON.stringify(args)}}));`], { env }));
function state(id) {
  const result = rpc('latency_status', { id });
  if (result.state === 'failed') {
    const dir = `/var/run/hyper-latency/job-${id}`;
    throw new Error(JSON.stringify(result) + '\n' + ['core.log', 'generate.log'].map(name => existsSync(`${dir}/${name}`) ? readFileSync(`${dir}/${name}`, 'utf8') : '').join('\n'));
  }
  return result;
}
try {
  const nodes = [];
  for (let index = 0; index < 8; index++) {
    const proxy = createServer();
    proxy.on('connect', (req, socket, head) => {
      active++; maximum = Math.max(maximum, active);
      let pending = true;
      const settled = () => { if (pending) { pending = false; active--; } };
      socket.once('close', settled);
      if (index === 7) { socket.end('HTTP/1.1 502 Bad Gateway\r\n\r\n'); return; }
      setTimeout(() => {
        if (socket.destroyed) return;
        const upstream = connect(endpoint, '127.0.0.1');
        sockets.add(upstream); upstream.on('close', () => sockets.delete(upstream));
        upstream.on('error', () => socket.destroy()); socket.on('error', () => upstream.destroy());
        socket.on('close', () => upstream.destroy());
        upstream.on('connect', () => {
          // Count overlapping probe handshakes, not TCP sockets lingering after completion.
          settled();
          socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');
          if (head.length) upstream.write(head);
          socket.pipe(upstream); upstream.pipe(socket);
        });
      }, slow ? 1800 : index === 0 ? 40 : 350);
    });
    const port = await listen(proxy);
    nodes.push(`probe_${index}`);
    if (!index) writeFileSync(config, "config hyper 'config'\n option enabled '0'\n option main_node 'keep_current'\n option routing_mode 'bypass_mainland_china'\nconfig infra 'infra'\n option self_mark '100'\n");
    writeFileSync(config, readFileSync(config) + `\nconfig node 'probe_${index}'\n option type 'http'\n option address '127.0.0.1'\n option port '${port}'\n`);
  }
  mkdirSync('/var/run/hyper', { recursive: true }); writeFileSync(runtime, 'main configuration must stay unchanged\n');
  writeFileSync(init, `#!/bin/sh\nprintf '%s\\n' "$1" >> '${root}/service-calls'\n`);
  const begin = performance.now(); const job = rpc('latency', { nodes });
  assert(job.result && job.id); assert(performance.now() - begin < 1000, 'probe submission blocked rpcd');
  let first;
  await until(() => { first = state(job.id); return first.completed > 0; }, 'no incremental result', 10000);
  assert(first.completed < nodes.length, 'results were held until every node finished');
  let done; await until(() => { done = state(job.id); return done.state === 'done'; }, 'probe did not finish', 10000);
  for (const name of nodes.slice(0, 7)) assert(done.latency[name] > 0, `${name}: ${JSON.stringify(done)}`);
  assert.equal(done.latency.probe_7, null, 'one failed node must not fail its peers');
  assert(maximum >= 2 && maximum <= 6, `expected concurrent probes bounded by six, got ${maximum}`);
  assert.equal(command('uci', ['-q', 'get', 'hyper.config.main_node']).trim(), 'keep_current');
  assert.equal(command('uci', ['-q', 'get', 'hyper.config.enabled']).trim(), '0');
  assert.equal(readFileSync(runtime, 'utf8'), 'main configuration must stay unchanged\n');
  assert(!existsSync(join(root, 'service-calls')), 'independent probes must not start/stop the service or firewall');
  await until(() => !existsSync(`/var/run/hyper-latency/job-${job.id}/config.json`), 'probe credentials were not cleaned');
  console.log('ok   stopped service probes independently with incremental results, bounded concurrency and unchanged runtime/selection');

  slow = true; const old = rpc('latency', { nodes });
  await until(() => active > 0, 'slow probe did not start');
  const switching = performance.now(); assert(rpc('select_node', { node: 'probe_1' }).result);
  assert(performance.now() - switching < 1000, 'selection waited for latency');
  assert.equal(state(old.id).state, 'cancelled');
  assert.equal(command('uci', ['-q', 'get', 'hyper.config.main_node']).trim(), 'probe_1');
  slow = false; const replacement = rpc('latency', { nodes: ['probe_0'] });
  await until(() => state(replacement.id).state === 'done', 'replacement probe did not finish', 10000);
  assert.equal(state(old.id).state, 'cancelled');
  assert.deepEqual(Object.keys(state(replacement.id).latency), ['probe_0']);
  assert.equal(command('uci', ['-q', 'get', 'hyper.config.main_node']).trim(), 'probe_1');
  console.log('ok   manual selection immediately cancels old probes; replacement results cannot change the chosen node');

  slow = true; const stopping = rpc('latency', { nodes });
  await until(() => active > 0, 'stop fixture did not start');
  const stopBegin = performance.now(); assert(rpc('set_enabled', { enabled: false }).accepted);
  assert(performance.now() - stopBegin < 1000, 'stop waited for latency lock');
  assert.equal(state(stopping.id).state, 'cancelled');
  await until(() => existsSync(join(root, 'service-calls')), 'stop was not applied');
  assert.equal(readFileSync(join(root, 'service-calls'), 'utf8').trim(), 'stop');
  await until(() => !existsSync(`/var/run/hyper-latency/job-${stopping.id}/config.json`), 'cancelled probe core did not exit');
  console.log('ok   stop bypasses probe work and the independent worker releases its temporary core');
} finally {
  try { rpc('latency_cancel'); } catch {}
  await sleep(300);
  writeFileSync(config, originalConfig); writeFileSync(init, originalInit);
  if (originalRuntime) writeFileSync(runtime, originalRuntime); else rmSync(runtime, { force: true });
  for (const socket of sockets) socket.destroy();
  await Promise.all(servers.map(server => new Promise(resolve => server.close(resolve))));
  rmSync(root, { recursive: true, force: true });
}
