import { eq, ok, done } from 'lib';
import { decodeBody, parseAnyTls, parseFeatures, parseLocations, parseNode, parseStatus, parseSubscription, isValidHost, isValidPort } from 'snapshot';

/* full link, as the app parses it */
let n = parseAnyTls('anytls://p%40ss%3Aword@jp1.example.com:8443/?sni=cdn.example.com&alpn=h2,http/1.1&insecure=0&udp=1#%E6%97%A5%E6%9C%AC%20%C2%B7%20%E4%B8%9C%E4%BA%AC%2001', { country: 'JP' });
eq('anytls type', n?.type, 'anytls');
eq('anytls host/port', [n?.address, n?.port], ['jp1.example.com', '8443']);
eq('password decoded', n?.password, 'p@ss:word');
eq('label decoded', n?.label, '日本 · 东京 01');
eq('sni', n?.tls_sni, 'cdn.example.com');
eq('alpn list', n?.tls_alpn, ['h2', 'http/1.1']);
eq('insecure=0 honoured', n?.tls_insecure, '0');
eq('tls forced', n?.tls, '1');
eq('app idle defaults', [n?.anytls_idle_session_check_interval, n?.anytls_idle_session_timeout, n?.anytls_min_idle_session], ['30', '30', '5']);
eq('country + group', [n?.country, n?.grouphash], ['JP', 'ppanel']);

/* defaults: port 443, sni = host, insecure from opts (app default true) */
n = parseAnyTls('anytls://secret@203.0.113.7', {});
eq('default port', n?.port, '443');
eq('default sni = host', n?.tls_sni, '203.0.113.7');
eq('default insecure (app default)', n?.tls_insecure, '1');
eq('label falls back to host', n?.label, '203.0.113.7');
eq('insecure default can be turned off', parseAnyTls('anytls://s@h.example', { insecure_default: false })?.tls_insecure, '0');
eq('bare insecure flag means on', parseAnyTls('anytls://s@h.example?allowInsecure', { insecure_default: false })?.tls_insecure, '1');
eq('alternate sni key', parseAnyTls('anytls://s@h.example?peer=p.example')?.tls_sni, 'p.example');

/* IPv6 */
n = parseAnyTls('anytls://s@[2001:db8::1]:443#v6');
eq('ipv6', [n?.address, n?.port, n?.label], ['2001:db8::1', '443', 'v6']);

/* literal % in a display name survives */
eq('literal percent in name', parseAnyTls('anytls://s@h.example#100%off')?.label, '100%off');

/* rejects */
eq('no password', parseAnyTls('anytls://h.example:443'), null);
eq('empty password', parseAnyTls('anytls://@h.example:443'), null);
eq('bad port', parseAnyTls('anytls://s@h.example:70000'), null);
eq('shell chars in host', parseAnyTls('anytls://s@h.example;reboot:443'), null);
eq('space in authority', parseAnyTls('anytls://s@h .example:443'), null);
eq('other scheme', parseAnyTls('vless://u@h.example:443'), null);

/* whole body: plain and base64, REMARKS / STATUS, every protocol, unknown schemes counted */
const body = 'REMARKS=Hyper\nSTATUS=Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09 12:00:00 +0800\nanytls://a@a.example:443#A\nvless://11111111-2222-3333-4444-555555555555@v.example:443?security=tls#V\nanytls://broken\nssr://abc\nanytls://b@b.example:443#B\n';
let s = parseSubscription(body, { country: 'SG' });
eq('nodes parsed', map(s.nodes, (x) => x.label), ['A', 'V', 'B']);
eq('invalid links counted', s.invalid, 1);
eq('unsupported schemes listed', s.unsupported, ['ssr://']);
eq('remarks/status', [s.remarks, s.status], ['Hyper', 'Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09 12:00:00 +0800']);
eq('country tag', s.nodes[1].country, 'SG');
eq('base64 body', map(parseSubscription(b64enc(body)).nodes, (x) => x.label), ['A', 'V', 'B']);
eq('base64url body', map(parseSubscription(replace(replace(replace(b64enc(body), /\+/g, '-'), /\//g, '_'), /=+$/, '')).nodes, (x) => x.label), ['A', 'V', 'B']);
eq('plain passthrough', decodeBody('anytls://x@y.example'), 'anytls://x@y.example');
eq('base64 of other schemes', decodeBody(b64enc('trojan://p@t.example:443')), 'trojan://p@t.example:443');
eq('empty body', length(parseSubscription('').nodes), 0);

/* ---------- every protocol ---------- */
const UUID = '11111111-2222-3333-4444-555555555555';
const PBK = 'jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0';

/* VLESS + REALITY + vision */
n = parseNode(`vless://${UUID}@r.example:443?encryption=none&security=reality&sni=www.apple.com&fp=chrome&pbk=${PBK}&sid=6ba85179e30d4fc2&type=tcp&flow=xtls-rprx-vision#%E6%97%A5%E6%9C%AC%20R`, { country: 'JP' });
eq('vless reality', [n?.type, n?.address, n?.port, n?.uuid, n?.label], ['vless', 'r.example', '443', UUID, '日本 R']);
eq('vless reality tls', [n?.tls, n?.tls_reality, n?.tls_reality_public_key, n?.tls_reality_short_id, n?.tls_sni, n?.tls_utls, n?.vless_flow],
	['1', '1', PBK, '6ba85179e30d4fc2', 'www.apple.com', 'chrome', 'xtls-rprx-vision']);
eq('vless tcp has no transport', n?.transport, null);
eq('reality forces uTLS', parseNode(`vless://${UUID}@r.example:443?security=reality&pbk=${PBK}`)?.tls_utls, 'chrome');
eq('reality without key rejected', parseNode(`vless://${UUID}@r.example:443?security=reality`), null);
eq('reality bad short id rejected', parseNode(`vless://${UUID}@r.example:443?security=reality&pbk=${PBK}&sid=xyz`), null);

/* VLESS ws + tls, early data */
n = parseNode(`vless://${UUID}@w.example:8443?security=tls&type=ws&host=cdn.example&path=%2Fws%3Fed%3D2048&alpn=h2%2Chttp%2F1.1&allowInsecure=1#W`);
eq('vless ws', [n?.transport, n?.ws_host, n?.ws_path, n?.websocket_early_data, n?.websocket_early_data_header],
	['ws', 'cdn.example', '/ws', '2048', 'Sec-WebSocket-Protocol']);
eq('vless tls options', [n?.tls, n?.tls_alpn, n?.tls_insecure, n?.tls_sni], ['1', ['h2', 'http/1.1'], '1', null]);
eq('vless grpc', [parseNode(`vless://${UUID}@g.example:443?security=tls&type=grpc&serviceName=svc`)?.grpc_servicename], ['svc']);
eq('vless httpupgrade', [parseNode(`vless://${UUID}@h.example:443?type=httpupgrade&host=a.example&path=%2Fup`)?.transport,
	parseNode(`vless://${UUID}@h.example:443?type=httpupgrade&host=a.example&path=%2Fup`)?.http_path], ['httpupgrade', '/up']);
eq('vless h2 host list', parseNode(`vless://${UUID}@h.example:443?security=tls&type=http&host=a.example,b.example`)?.http_host, ['a.example', 'b.example']);
eq('vless plain has no tls', parseNode(`vless://${UUID}@p.example:80`)?.tls, null);
eq('vless kcp unsupported', parseNode(`vless://${UUID}@k.example:443?type=kcp`)?.unsupported, 'kcp transport');
eq('vless xhttp unsupported', parseNode(`vless://${UUID}@k.example:443?type=xhttp`)?.unsupported, 'xhttp transport');
eq('vless tcp http header unsupported', parseNode(`vless://${UUID}@k.example:443?type=tcp&headerType=http`)?.unsupported, 'tcp/http header');
eq('vless old xtls flow unsupported', parseNode(`vless://${UUID}@k.example:443?security=tls&flow=xtls-rprx-direct`)?.unsupported, 'vless flow xtls-rprx-direct');
eq('vless without uuid', parseNode('vless://@k.example:443'), null);
eq('vless unknown fp dropped', parseNode(`vless://${UUID}@k.example:443?security=tls&fp=weird`)?.tls_utls, null);

/* Trojan */
n = parseNode('trojan://p%40ss@t.example:443?sni=s.example&type=ws&path=%2Ftr#T');
eq('trojan', [n?.type, n?.password, n?.tls, n?.tls_sni, n?.transport, n?.ws_path, n?.tls_insecure], ['trojan', 'p@ss', '1', 's.example', 'ws', '/tr', '0']);
eq('trojan default port', parseNode('trojan://p@t.example')?.port, '443');
eq('trojan allowInsecure', parseNode('trojan://p@t.example:443?allowInsecure=1')?.tls_insecure, '1');
eq('trojan without password', parseNode('trojan://t.example:443'), null);
eq('trojan label fallback', parseNode('trojan://p@t.example:8443')?.label, 't.example:8443');

/* VMess (v2rayN JSON) */
const vm = { v: '2', ps: '香港 VM', add: 'vm.example', port: 443, id: UUID, aid: '0', scy: 'auto', net: 'ws', type: 'none', host: 'cdn.example', path: '/vm', tls: 'tls', sni: 'sni.example', alpn: 'h2', fp: 'firefox' };
n = parseNode('vmess://' + b64enc(sprintf('%J', vm)));
eq('vmess', [n?.type, n?.label, n?.address, n?.port, n?.uuid, n?.vmess_alterid, n?.vmess_encrypt], ['vmess', '香港 VM', 'vm.example', '443', UUID, '0', 'auto']);
eq('vmess ws + tls', [n?.transport, n?.ws_host, n?.ws_path, n?.tls, n?.tls_sni, n?.tls_alpn, n?.tls_utls], ['ws', 'cdn.example', '/vm', '1', 'sni.example', ['h2'], 'firefox']);
eq('vmess grpc service from path', parseNode('vmess://' + b64enc(sprintf('%J', { ...vm, net: 'grpc', path: 'gsvc' })))?.grpc_servicename, 'gsvc');
eq('vmess h2', parseNode('vmess://' + b64enc(sprintf('%J', { ...vm, net: 'h2', host: 'a.example' })))?.transport, 'http');
eq('vmess tcp/http header unsupported', parseNode('vmess://' + b64enc(sprintf('%J', { ...vm, net: 'tcp', type: 'http', tls: '' })))?.unsupported, 'tcp/http header');
eq('vmess no tls', parseNode('vmess://' + b64enc(sprintf('%J', { ...vm, net: 'tcp', tls: '' })))?.tls, null);
eq('vmess bad security', parseNode('vmess://' + b64enc(sprintf('%J', { ...vm, scy: 'rot13' })))?.unsupported, 'vmess security rot13');
eq('vmess shadowrocket format', parseNode('vmess://YXV0bzp1dWlkQGhvc3Q6NDQz?remarks=x')?.unsupported, 'vmess shadowrocket format');
eq('vmess garbage', parseNode('vmess://not-json'), null);
eq('vmess label fallback', parseNode('vmess://' + b64enc(sprintf('%J', { ...vm, ps: '' })))?.label, 'vm.example:443');

/* Shadowsocks: SIP002 plain, SIP002 base64 userinfo, whole-link base64, plugin, 2022 */
n = parseNode('ss://YWVzLTI1Ni1nY206cGFzcw@ss.example:8388#SS1');
eq('ss base64 userinfo', [n?.type, n?.shadowsocks_encrypt_method, n?.password, n?.port, n?.label], ['shadowsocks', 'aes-256-gcm', 'pass', '8388', 'SS1']);
n = parseNode('ss://2022-blake3-aes-256-gcm:YctPZ6U7xPPcU%2Bgp3u%2B0tx%2FtRizJN9K8y%2BuKlW2qjlI%3D@ss.example:443#SS2');
eq('ss 2022 percent-encoded', [n?.shadowsocks_encrypt_method, n?.password], ['2022-blake3-aes-256-gcm', 'YctPZ6U7xPPcU+gp3u+0tx/tRizJN9K8y+uKlW2qjlI=']);
eq('ss 2022 key size checked', parseNode('ss://2022-blake3-aes-128-gcm:YctPZ6U7xPPcU%2Bgp3u%2B0tx%2FtRizJN9K8y%2BuKlW2qjlI%3D@ss.example:443'), null);
eq('ss 2022 multi-user keys', parseNode('ss://2022-blake3-aes-128-gcm:AAECAwQFBgcICQoLDA0ODw%3D%3D%3AEBESExQVFhcYGRobHB0eHw%3D%3D@ss.example:443')?.password,
	'AAECAwQFBgcICQoLDA0ODw==:EBESExQVFhcYGRobHB0eHw==');
n = parseNode('ss://' + b64enc('chacha20-ietf-poly1305:p@ss/w?rd#1@[2001:db8::2]:8388') + '#Whole');
eq('ss whole-link base64', [n?.shadowsocks_encrypt_method, n?.password, n?.address, n?.port, n?.label],
	['chacha20-ietf-poly1305', 'p@ss/w?rd#1', '2001:db8::2', '8388', 'Whole']);
n = parseNode('ss://YWVzLTEyOC1nY206dGVzdA@ss.example:8388/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dexample.com#P');
eq('ss obfs plugin', [n?.shadowsocks_plugin, n?.shadowsocks_plugin_opts], ['obfs-local', 'obfs=http;obfs-host=example.com']);
eq('ss simple-obfs renamed', parseNode('ss://YWVzLTEyOC1nY206dGVzdA@ss.example:8388/?plugin=simple-obfs%3Bobfs%3Dtls')?.shadowsocks_plugin, 'obfs-local');
eq('ss unknown plugin', parseNode('ss://YWVzLTEyOC1nY206dGVzdA@ss.example:8388/?plugin=kcptun')?.unsupported, 'shadowsocks plugin kcptun');
eq('ss unknown method', parseNode('ss://' + b64enc('rot13:pw') + '@ss.example:8388')?.unsupported, 'shadowsocks method rot13');
eq('ss without port', parseNode('ss://YWVzLTEyOC1nY206dGVzdA@ss.example'), null);

/* SIP008 JSON */
s = parseSubscription(sprintf('%J', { version: 1, servers: [
	{ server: 's1.example', server_port: 8388, password: 'pw', method: 'aes-256-gcm', remarks: 'S1' },
	{ server: 's2.example', server_port: 8389, password: 'pw', method: 'aes-128-gcm', plugin: 'v2ray-plugin', plugin_opts: 'tls;host=a.example' },
	{ server: 's3.example', server_port: 0, password: 'pw', method: 'aes-128-gcm' },
	{ server: 's4.example', server_port: 1, password: 'pw', method: 'bad' }
] }), { country: 'US' });
eq('sip008 nodes', map(s.nodes, (x) => [x.label, x.shadowsocks_plugin, x.shadowsocks_plugin_opts, x.country]),
	[['S1', null, null, 'US'], ['s2.example:8389', 'v2ray-plugin', 'tls;host=a.example', 'US']]);
eq('sip008 invalid / unsupported', [s.invalid, s.unsupported], [1, ['shadowsocks method bad']]);

/* Hysteria2 */
n = parseNode('hysteria2://pa%2Bss@hy.example:443/?sni=s.example&obfs=salamander&obfs-password=ob&insecure=1#HY2');
eq('hy2', [n?.type, n?.password, n?.tls, n?.tls_sni, n?.tls_insecure, n?.hysteria_obfs_type, n?.hysteria_obfs_password],
	['hysteria2', 'pa+ss', '1', 's.example', '1', 'salamander', 'ob']);
eq('hy2 alias + port hopping', [parseNode('hy2://p@hy.example:443,20000-30000')?.type, parseNode('hy2://p@hy.example:20000-30000')?.port], ['hysteria2', '20000']);
eq('hy2 default port', parseNode('hy2://p@hy.example')?.port, '443');
eq('hy2 bad obfs', parseNode('hy2://p@hy.example?obfs=xor')?.unsupported, 'hysteria2 obfs xor');
eq('hy2 without quic', parseNode('hy2://p@hy.example', { features: { with_utls: true } })?.unsupported, 'hysteria2 (sing-box built without QUIC)');
eq('hy2 with quic', parseNode('hy2://p@hy.example', { features: { with_quic: true } })?.type, 'hysteria2');

/* Hysteria 1 */
n = parseNode('hysteria://h1.example:36712?protocol=udp&auth=secret&peer=p.example&insecure=1&upmbps=50&downmbps=100&obfsParam=ob&alpn=hysteria#H1');
eq('hysteria', [n?.type, n?.hysteria_auth_type, n?.hysteria_auth_payload, n?.hysteria_up_mbps, n?.hysteria_down_mbps, n?.hysteria_obfs_password, n?.tls_sni, n?.tls_alpn],
	['hysteria', 'string', 'secret', '50', '100', 'ob', 'p.example', ['hysteria']]);
eq('hysteria needs speeds', parseNode('hysteria://h1.example:36712?auth=x'), null);
eq('hysteria faketcp unsupported', parseNode('hysteria://h1.example:36712?protocol=faketcp&upmbps=1&downmbps=1')?.unsupported, 'hysteria protocol faketcp');

/* TUIC */
n = parseNode(`tuic://${UUID}:p%40w@tu.example:443?congestion_control=bbr&udp_relay_mode=quic&alpn=h3&sni=s.example&allow_insecure=1#TU`);
eq('tuic', [n?.type, n?.uuid, n?.password, n?.tuic_congestion_control, n?.tuic_udp_relay_mode, n?.tls_alpn, n?.tls_sni, n?.tls_insecure],
	['tuic', UUID, 'p@w', 'bbr', 'quic', ['h3'], 's.example', '1']);
eq('tuic needs password', parseNode(`tuic://${UUID}@tu.example:443`), null);
eq('tuic odd congestion dropped', parseNode(`tuic://${UUID}:p@tu.example:443?congestion_control=reno`)?.tuic_congestion_control, null);

/* WireGuard */
const PRIV = 'yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=', PUB = 'xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=';
n = parseNode(`wireguard://${replace(PRIV, /\+/g, '%2B')}@wg.example:51820?publickey=${PUB}&address=10.0.0.2,fd00::2/64&mtu=1280&reserved=1,2,3#WG`);
eq('wireguard', [n?.type, n?.wireguard_private_key, n?.wireguard_peer_public_key, n?.wireguard_local_address, n?.wireguard_mtu, n?.wireguard_reserved],
	['wireguard', PRIV, PUB, ['10.0.0.2/32', 'fd00::2/64'], '1280', ['1', '2', '3']]);
eq('wg alias, unencoded + in query', parseNode(`wg://${replace(PRIV, /\+/g, '%2B')}@wg.example?publickey=${PUB}&address=10.0.0.2/32`)?.wireguard_peer_public_key, PUB);
eq('wireguard without address', parseNode(`wireguard://${replace(PRIV, /\+/g, '%2B')}@wg.example:51820?publickey=${PUB}`), null);
eq('wireguard bad reserved', parseNode(`wireguard://${replace(PRIV, /\+/g, '%2B')}@wg.example:51820?publickey=${PUB}&address=10.0.0.2&reserved=1,2`), null);
eq('wireguard without feature', parseNode(`wireguard://${replace(PRIV, /\+/g, '%2B')}@wg.example:51820?publickey=${PUB}&address=10.0.0.2`, { features: { with_quic: true } })?.unsupported,
	'wireguard (sing-box built without WireGuard)');

/* SOCKS / HTTP */
n = parseNode('socks5://u%3Ax:p%40w@so.example:1080#S5');
eq('socks5', [n?.type, n?.username, n?.password, n?.socks_version], ['socks', 'u:x', 'p@w', '5']);
eq('socks4a', parseNode('socks4a://so.example:1080')?.socks_version, '4a');
n = parseNode('socks://' + b64enc('user:p%w') + '@so.example:1080');
eq('v2rayN socks base64 userinfo', [n?.username, n?.password], ['user', 'p%w']);
n = parseNode('https://u:p@hp.example?sni=s.example#H');
eq('https proxy', [n?.type, n?.port, n?.tls, n?.tls_sni, n?.username, n?.password], ['http', '443', '1', 's.example', 'u', 'p']);
eq('http proxy', [parseNode('http://hp.example:3128')?.tls, parseNode('http://hp.example')?.port], [null, '80']);

/* shared rejects */
eq('ipv6 vless', parseNode(`vless://${UUID}@[2001:db8::9]:443`)?.address, '2001:db8::9');
eq('unknown scheme', parseNode('naive+https://u:p@n.example')?.unsupported, 'naive+https://');
eq('not a link', parseNode('hello'), null);
eq('bad host', parseNode('trojan://p@bad_host!:443'), null);
eq('bracket junk', parseNode('trojan://p@h]x:443'), null);
eq('null options dropped', 'tls_alpn' in parseNode('trojan://p@t.example:443'), false);

/* sing-box build tags */
eq('features', parseFeatures('sing-box version 1.12.22\n\nEnvironment: go1.24.11 linux/arm64\nTags: with_gvisor,with_quic,with_wireguard,with_utls\nRevision: x\n'),
	{ with_gvisor: true, with_quic: true, with_wireguard: true, with_utls: true });
eq('no tags line', parseFeatures('sing-box version 1.12.22'), null);

eq('status parse', parseStatus('Traffic: 1.5 GB / 100 GB | Expires: 2027-11-09 12:00:00 +0800'),
	{ traffic_used: '1.5 GB', traffic_total: '100 GB', expires: '2027-11-09 12:00:00 +0800' });

/* locations: wrapped or not, sorted by sort desc, junk codes dropped */
eq('locations', map(parseLocations({ data: { locations: { JP: { sort: 5 }, SG: { sort: '9' }, 'X Y': { sort: 1 }, US: {} } } }), (l) => l.code), ['SG', 'JP', 'US']);
eq('locations unwrapped', length(parseLocations({ locations: { HK: { sort: 1 } } })), 1);
eq('locations garbage', parseLocations('x'), []);

ok('host ipv4', isValidHost('192.168.1.1'));
ok('host shell chars', !isValidHost('a;b'));
ok('port ok', isValidPort('443'));
ok('port text', !isValidPort('443abc'));

ok('host with second line', !isValidHost('a.example\nb.example'));
ok('host with control char', !isValidHost('a.example\u0000'));
ok('port with second line', !isValidPort('443\n1'));
eq('control char in sni drops the node', parseNode('trojan://p@t.example:443?sni=a.example%0Ab.example#T'), null);
eq('control char in password drops the node', parseNode('trojan://p%0Ax@t.example:443#T'), null);
eq('NUL in sni drops the node', parseNode('trojan://p@t.example:443?sni=a.example%00%0Ab#T'), null);
eq('control char in vmess json host drops the node',
	parseNode('vmess://' + b64enc(sprintf('%J', { v: '2', add: 'v.example\nx', port: '443', id: '11111111-2222-3333-4444-555555555555', net: 'tcp' }))), null);
eq('control chars in the name are cleaned', parseNode('trojan://p@t.example:443#Line%0A%09Two')?.label, 'Line Two');
eq('name of only control chars falls back', parseNode('trojan://p@t.example:443#%0A')?.label, 't.example:443');

done('snapshot');
