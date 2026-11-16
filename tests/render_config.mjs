/** JSON fixtures and semantic assertions used by render_test.sh (standard Node only). */
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
const [mode, input, output] = process.argv.slice(2);
const c = JSON.parse(mode === 'links-summary' ? input : readFileSync(input, 'utf8'));
const check = (name, actual, expected) => { assert.deepEqual(actual, expected, name); console.log(`ok   ${name}`); };
const outbound = tag => c.outbounds.find(o => o.tag === tag);
switch (mode) {
  case 'host': {
    function strip(value) {
      if (!value || typeof value !== 'object') return;
      delete value.routing_mark;
      for (const child of Object.values(value)) strip(child);
    }
    strip(c); c.inbounds = c.inbounds.filter(i => !['redirect', 'tproxy'].includes(i.type)); break;
  }
  case 'fixture-tun': c.hyper.config.proxy_mode = 'tun'; break;
  case 'fixture-dhcp':
    c.hyper.config.routing_mode = 'custom'; c.hyper.dns.default_server = 'wan_dhcp';
    c.hyper.wan_dhcp = { '.type': 'dns_server', enabled: '1', type: 'dhcp' }; break;
  case 'fixture-dnsonly': c.hyper.config.dns_only = '1'; break;
  case 'fixture-noself': c.hyper.control.proxy_router_self = '0'; break;
  case 'basic': {
    check('selector exists', outbound('main-out').type, 'selector');
    check('selector members', outbound('main-out').outbounds.slice().sort(), ['cfg-pp_anytls-out', 'cfg-pp_hk-out', 'cfg-pp_sg-out', 'cfg-pp_tokyo-out']);
    check('selector default', outbound('main-out').default, 'cfg-pp_tokyo-out');
    check('clash_api loopback', c.experimental.clash_api.external_controller, '127.0.0.1:5334');
    check('clash_api secret', c.experimental.clash_api.secret.length, 48);
    check('cache_file enabled', c.experimental.cache_file.enabled, true);
    check('reality pbk', outbound('cfg-pp_tokyo-out').tls.reality.public_key, 'jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0');
    const a = outbound('cfg-pp_anytls-out');
    check('anytls outbound', [a.type, a.server_port, a.tls.server_name, a.tls.insecure, a.idle_session_timeout, a.min_idle_session], ['anytls', 8443, 'cdn.example.com', true, '30s', 5]);
    check('trojan ws path', outbound('cfg-pp_sg-out').transport.path, '/ws');
    check('final is main-out', c.route.final, 'main-out');
    check('server rule sets', c.route.rule_set.filter(r => r.tag.startsWith('hyper-rule-')).map(r => r.tag).sort(), ['hyper-rule-cn-direct-domain', 'hyper-rule-streaming-domain', 'hyper-rule-streaming-ip']);
    check('disabled rule left out', c.route.rule_set.some(r => r.tag.includes('off')), false);
    check('direct rule + geoip', c.route.rules.find(r => (r.rule_set || []).includes('hyper-rule-cn-direct-domain')), { rule_set: ['hyper-rule-cn-direct-domain', 'geoip-cn'], action: 'route', outbound: 'direct-out' });
    check('proxy rule', c.route.rules.find(r => (r.rule_set || []).includes('hyper-rule-streaming-domain')).outbound, 'main-out');
    check('dns follows policy', c.dns.rules.filter(r => String(r.rule_set || '').startsWith('hyper-rule')).map(r => [r.rule_set, r.server]), [['hyper-rule-cn-direct-domain', 'china-dns'], ['hyper-rule-streaming-domain', 'main-dns']]);
    const tags = c.dns.rules.map(r => r.rule_set);
    assert(tags.includes('hyper-rule-cn-direct-domain') && tags.includes('geosite-cn'));
    check('rules before geosite', tags.indexOf('hyper-rule-cn-direct-domain') < tags.indexOf('geosite-cn'), true); break;
  }
  case 'tun':
    check("TUN uses the core's default stack", c.inbounds.filter(i => i.type === 'tun').map(i => 'stack' in i), [false]);
    check('TUN proxy connections bypass transparent interception', c.outbounds.filter(o => o.server).every(o => o.routing_mark === 100), true);
    check('TUN direct connections bypass transparent interception', outbound('direct-out').routing_mark, 100); break;
  case 'dhcp': check('custom DHCP DNS retained', c.dns.servers.filter(s => s.tag === 'cfg-wan_dhcp-dns').map(s => s.type), ['dhcp']); break;
  case 'links-summary':
    check('links parsed', c.nodes, 22);
    check('protocols', [...new Set(c.types)].sort(), ['anytls', 'http', 'hysteria', 'hysteria2', 'shadowsocks', 'snell', 'socks', 'trojan', 'tuic', 'vless', 'vmess', 'wireguard']);
    check('unsupported kept out', c.unsupported, ['kcp transport', 'ssr://']); break;
  case 'links':
    check('selector has all', outbound('main-out').outbounds.length, 22);
    check('wireguard endpoint', (c.endpoints || []).map(e => e.type), ['wireguard']);
    check('wireguard endpoint bypasses transparent interception', c.endpoints[0].routing_mark, 100);
    { const o = c.outbounds.find(o => o.type === 'vmess' && o.transport); check('vmess early data', [o.transport.path, o.transport.max_early_data], ['/vm', 2048]); }
    { const o = c.outbounds.find(o => o.type === 'snell' && o.version === 6);
      check('Snell v6 fields', [o.psk, o.mode, o.userkey, o.reuse], ['snell-fixture', 'unshaped', 'fixture-user', true]);
      check('Snell v6 excludes legacy fields', ['password', 'obfs_mode', 'obfs_host'].some(key => key in o), false); }
    { const o = c.outbounds.find(o => o.type === 'snell' && o.version === 4);
      check('Snell default v4 fields', [o.psk, o.obfs_mode, o.obfs_host], ['snell-fixture', 'tls', 'example.com']);
      check('Snell v4 excludes v6 fields', ['password', 'mode'].some(key => key in o), false); } break;
  case 'dnsonly':
    check('dns-only inbounds', c.inbounds.map(i => i.tag).sort(), ['dns-in', 'mixed-in']);
    check('dns-only: dns via proxy', c.dns.servers.find(s => s.tag === 'main-dns').detour, 'main-out');
    check('dns-only: cn split kept', c.dns.rules.some(r => r.server === 'china-dns'), true); break;
  default: throw new Error(`unknown render check: ${mode}`);
}
if (output) writeFileSync(output, JSON.stringify(c));
