/** Start the pinned core with local rules/DNS and no deprecation overrides.
 * Syntax-only `check` misses startup failures. Keep real protocols, DNS rules,
 * remote download clients and Linux routing marks in root router containers.
 */
import assert from 'node:assert/strict';
import { createSocket } from 'node:dgram';
import { createServer, get } from 'node:http';
import { createServer as createNetServer } from 'node:net';
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { command, launch, sleep, stop, wait } from './router_test_helpers.mjs';

const config = JSON.parse(readFileSync(process.argv[2], 'utf8'));
const requests = [], dnsQueries = [], dnsServers = [], children = [];
const root = mkdtempSync(join(tmpdir(), 'hyper-core-'));
let server;
function clone(value) { return JSON.parse(JSON.stringify(value)); }
async function reservePort() {
  const socket = createNetServer();
  await new Promise((resolve, reject) => { socket.once('error', reject); socket.listen(0, '127.0.0.1', resolve); });
  const port = socket.address().port;
  await new Promise(resolve => socket.close(resolve));
  return port;
}
function versionReady(port) {
  return new Promise(resolve => {
    const req = get(`http://127.0.0.1:${port}/version`, { headers: { Authorization: 'Bearer test-only' } }, res => {
      res.resume(); resolve(res.statusCode === 200);
    });
    req.setTimeout(1000, () => req.destroy());
    req.on('error', () => resolve(false));
  });
}
function start(data, filename) {
  const path = join(root, filename), logPath = `${path}.log`;
  writeFileSync(path, JSON.stringify(data));
  const fd = openSync(logPath, 'w');
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('ENABLE_DEPRECATED_')));
  const child = launch('sing-box', ['run', '-c', path], { env, stdio: ['ignore', fd, fd] });
  closeSync(fd); children.push(child);
  return { child, output: () => readFileSync(logPath, 'utf8') };
}
async function healthy(instance, port, timeout) {
  const deadline = performance.now() + timeout;
  let ready = false;
  while (instance.child.exitCode === null && instance.child.signalCode === null && performance.now() < deadline) {
    ready = await versionReady(port);
    if (ready) break;
    await sleep(100);
  }
  assert(ready, instance.output());
  await sleep(1000);
  assert.equal(instance.child.exitCode, null, instance.output());
  assert.equal(instance.child.signalCode, null, instance.output());
  assert(!instance.output().toLowerCase().includes('deprecated'), instance.output());
}
async function queryDNS(name, port) {
  const header = Buffer.alloc(12);
  header.writeUInt16BE(17); header.writeUInt16BE(0x0100, 2); header.writeUInt16BE(1, 4);
  const question = Buffer.concat([...name.split('.').flatMap(part => [Buffer.from([Buffer.byteLength(part)]), Buffer.from(part)]), Buffer.from([0, 0, 1, 0, 1])]);
  const client = createSocket('udp4');
  try {
    return await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`DNS timed out: ${name}`)), 3000);
      client.once('message', reply => { clearTimeout(timer); resolve(reply); });
      client.once('error', error => { clearTimeout(timer); reject(error); });
      client.send(Buffer.concat([header, question]), port, '127.0.0.1');
    });
  } finally { client.close(); }
}
try {
  for (const kind of ['local', 'remote']) {
    const dns = createSocket('udp4');
    dns.on('message', (query, peer) => {
      let pos = 12;
      const labels = [];
      while (query[pos]) {
        const size = query[pos]; labels.push(query.subarray(pos + 1, pos + 1 + size).toString()); pos += size + 1;
      }
      const name = labels.join('.'); dnsQueries.push([kind, name]);
      const address = kind === 'local' && name !== 'foreign-answer.test' ? [223, 5, 5, 5] : [1, 0, 0, 1];
      const header = Buffer.alloc(12); query.copy(header, 0, 0, 2);
      header.writeUInt16BE(0x8180, 2); header.writeUInt16BE(1, 4); header.writeUInt16BE(1, 6);
      const answer = Buffer.alloc(16); answer.writeUInt16BE(0xc00c); answer.writeUInt16BE(1, 2);
      answer.writeUInt16BE(1, 4); answer.writeUInt32BE(60, 6); answer.writeUInt16BE(4, 10);
      Buffer.from(address).copy(answer, 12);
      dns.send(Buffer.concat([header, query.subarray(12, pos + 5), answer]), peer.port, peer.address);
    });
    await new Promise((resolve, reject) => { dns.once('error', reject); dns.bind(0, '127.0.0.1', resolve); });
    dnsServers.push(dns);
  }
  const rules = { '/geoip-cn.json': { ip_cidr: ['223.5.5.0/24'] }, '/geosite-cn.json': { domain: ['cn.test'] }, '/geosite-noncn.json': { domain: ['overseas.test'] } };
  server = createServer((req, res) => {
    requests.push(req.url);
    if (!rules[req.url]) { res.writeHead(404); res.end(); return; }
    const body = JSON.stringify({ version: 3, rules: [rules[req.url]] });
    res.writeHead(200, { 'Content-Length': Buffer.byteLength(body) }); res.end(body);
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  const dnsPort = await reservePort();
  config.inbounds = [{ type: 'direct', tag: 'dns-in', listen: '127.0.0.1', listen_port: dnsPort }];
  config.ntp = { enabled: false };
  config.dns.servers = [['default-dns', 0], ['system-dns', 0], ['china-dns', 0], ['main-dns', 1]].map(([tag, index]) => ({
    tag, type: 'udp', server: '127.0.0.1', server_port: dnsServers[index].address().port,
    ...(index ? { detour: 'main-out' } : {}),
  }));
  for (const outbound of config.outbounds) {
    if (process.platform !== 'linux' || process.geteuid() !== 0) delete outbound.routing_mark;
    if (outbound.tag === 'main-out') { outbound.outbounds.push('direct-out'); outbound.default = 'direct-out'; }
  }
  if (process.platform !== 'linux') for (const dns of config.dns.servers) if (dns.detour === 'direct-out') delete dns.detour;
  const remote = config.route.rule_set.filter(rule => rule.type === 'remote');
  assert(remote.length, 'fixture must exercise remote rule downloads');
  assert(remote.every(rule => rule.http_client?.detour === 'main-out'));
  assert(config.route.rule_set.every(rule => !('download_detour' in rule)));
  const bootstrap = Object.fromEntries(remote.map(rule => [rule.tag, rule.initial_path]));
  assert(Object.values(bootstrap).every(path => readFileSync(path).subarray(0, 3).equals(Buffer.from('SRS'))));
  for (const rule of remote) {
    delete rule.initial_path;
    Object.assign(rule, { format: 'source', url: `http://127.0.0.1:${server.address().port}/${rule.tag}.json` });
  }
  const port = await reservePort();
  config.experimental.clash_api = { external_controller: `127.0.0.1:${port}`, secret: 'test-only' };
  config.experimental.cache_file.path = join(root, 'cache.db');
  config.log = { level: 'info', timestamp: false };
  const legacy = clone(config);
  const legacyRule = legacy.route.rule_set.find(rule => rule.type === 'remote');
  legacyRule.download_detour = legacyRule.http_client.detour; delete legacyRule.http_client;
  const match = /^(\d+)\.(\d+)/.exec(command('sing-box', ['version', '-n']));
  assert(match, 'core version must contain major/minor');
  if (Number(match[1]) > 1 || (Number(match[1]) === 1 && Number(match[2]) >= 15)) {
    const instance = start(legacy, 'legacy.json');
    try {
      assert.notEqual((await wait(instance.child)).code, 0, 'legacy startup unexpectedly succeeded');
      assert(instance.output().includes('download_detour'), instance.output());
      console.log('ok   core startup: reported legacy download_detour failure reproduced');
    } finally { await stop(instance.child); }
  }
  const modern = start(config, 'modern.json');
  try {
    await healthy(modern, port, 15000);
    assert.equal(new Set(requests).size, remote.length, JSON.stringify({ requests, output: modern.output() }));
    console.log('ok   core startup: modern rules downloaded and API remains healthy');
    for (const [name, expected] of [['cn.test', ['local']], ['overseas.test', ['remote']], ['cn-answer.test', ['local']], ['foreign-answer.test', ['local', 'remote']]]) {
      const reply = await queryDNS(name, dnsPort);
      assert.equal(reply[3] & 15, 0, name); assert.equal(reply.readUInt16BE(6), 1, name);
      assert.deepEqual(dnsQueries.filter(([, queried]) => queried === name).map(([kind]) => kind), expected, `${name}: ${JSON.stringify(dnsQueries)}`);
    }
    console.log('ok   DNS: local/overseas rules and evaluated CN/non-CN answers preserve routing');
  } finally { await stop(modern.child); }
  const offline = clone(config);
  offline.experimental.cache_file.path = join(root, 'offline-cache.db');
  for (const rule of offline.route.rule_set) if (rule.type === 'remote') Object.assign(rule, {
    format: 'binary', initial_path: bootstrap[rule.tag], url: 'http://127.0.0.1:1/unreachable.srs',
  });
  const instance = start(offline, 'offline.json');
  try {
    await healthy(instance, port, 10000);
    console.log('ok   core startup: empty cache and unreachable updates use bundled rules');
  } finally { await stop(instance.child); }
} finally {
  await Promise.all(children.map(stop));
  if (server) { server.closeAllConnections?.(); await new Promise(resolve => server.close(resolve)); }
  for (const dns of dnsServers) await new Promise(resolve => dns.close(resolve));
  rmSync(root, { recursive: true, force: true });
}
