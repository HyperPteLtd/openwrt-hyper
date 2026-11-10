import { ok, done } from 'lib';
import { isAllowedPath, isValidIdempotencyKey, SUBSCRIBE_PATH } from 'ppanel';
import { matchWhole } from 'hyper';

/* allowed: auth, public user/order/subscribe pages */
for (let p in [
	'/v1/auth/login', '/v1/auth/register', '/v1/auth/check', '/v1/auth/reset',
	'/v1/common/site/config', '/v1/common/send_code',
	'/v1/public/user/info', '/v1/public/user/devices', '/v1/public/user/logout',
	'/v1/public/user/affiliate/count', '/v1/public/subscribe/list',
	'/v1/public/order/pre', '/v1/public/order/purchase', '/v1/public/order/detail',
	'/v1/public/payment/methods', '/v1/public/announcement/list', '/v1/public/coupon/redeem',
	'/v2/subscribe/locations',
	'/v2/orders/purchase', '/v2/orders/202611091200001/payment',
	'/v2/orders/202611091200001/payment/selection', '/v2/orders/AB_c-1.2/cancel'
])
	ok(`allow ${p}`, isAllowedPath(p));

/* refused: admin, growth, internal, account deletion, traversal, query smuggling */
for (let p in [
	'/v1/admin/user/list', '/v1/growth/api/v1/me', '/v1/internal/subscription-duration-grants',
	'/v1/public/user/account-deletion', '/v1/public/user/subscribe_token',
	'/v1/public/user/../../admin/user/list', '/v1/public/order/../../../admin/x',
	'/v1/public/user/info?x=1', '/v1/public/user/info#a', '/v1/public/user/info ',
	'v1/public/user/info', '', null, 42,
	'/v2/subscribe/config',
	'/v2/orders/x/apple-iap/confirm', '/v2/orders/apple-iap/recover', '/v2/orders/a%2Fb/payment',
	'/v2/orders//payment', '/v2/orders/x/refund'
])
	ok(`refuse ${p}`, !isAllowedPath(p));

for (let k in [ 'checkout:v2:6f1c2b9e-1d2c-4b8a-9a33-0c1e2f3a4b5c', 'abcdefgh' ])
	ok(`idempotency key ${k}`, isValidIdempotencyKey(k));
for (let k in [ 'short', 'has space key', 'semi;colon-key', null, 12345678, sprintf('%0129d', 1) ])
	ok(`bad idempotency key ${k}`, !isValidIdempotencyKey(k));

/* ucode regexes use REG_NEWLINE: one good line must not validate the whole value */
ok('key with injected curl directive', !isValidIdempotencyKey('checkout:v2:abcdefgh\nurl = "http://evil.example/'));
ok('key with trailing newline', !isValidIdempotencyKey('checkout:v2:abcdefgh\n'));
ok('key with leading line', !isValidIdempotencyKey('x\ncheckout:v2:abcdefgh'));
ok('key hiding a line break behind NUL', !isValidIdempotencyKey('checkout:v2:abcdefgh\u0000\nurl = "http://evil.example/'));
ok('path with control char', !isAllowedPath('/v1/public/user/info\u0001'));
ok('path with second line', !isAllowedPath('/v1/public/user/info\n/v1/admin/user/list'));
ok('matchWhole plain', matchWhole('abc', /^[a-z]+$/));
ok('matchWhole second line', !matchWhole('abc\n;rm', /^[a-z]+$/));
ok('matchWhole first line', !matchWhole(';rm\nabc', /^[a-z]+$/));
ok('matchWhole tab', !matchWhole('a\tb', /^[a-z\t]+$/));
ok('matchWhole not a string', !matchWhole(12, /^[0-9]+$/));

ok('subscribe path is fixed', SUBSCRIBE_PATH === '/api/subscribe');

done('ppanel');
