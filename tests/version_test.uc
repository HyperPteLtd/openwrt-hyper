import { eq, done } from 'lib';
import { versionCompare } from 'hyper';

/* [a, b, expected versionCompare(a, b)] */
const cases = [
	/* installed package (opkg / apk add -r<PKG_RELEASE>) vs release tag */
	[ 'v0.2.0', '0.2.0-r1', 0 ],
	[ '0.2.0-r1', 'v0.2.0', 0 ],
	[ '0.2.0-r3', '0.2.0-r1', 0 ],
	[ 'v0.2.1', '0.2.0-r1', 1 ],
	[ 'v0.2.0', '0.2.1-r1', -1 ],
	/* numeric, not string, order */
	[ '0.10.0', '0.9.9', 1 ],
	[ '1.0.0', '0.99.99', 1 ],
	[ '2', '10', -1 ],
	/* missing parts are 0 */
	[ '1.2', '1.2.0', 0 ],
	[ '1.2.1', '1.2', 1 ],
	/* pre-releases sort before their release */
	[ '0.2.0-beta.1', '0.2.0', -1 ],
	[ '0.2.0', '0.2.0-beta.1', 1 ],
	[ '0.2.0-beta.2', '0.2.0-beta.10', -1 ],
	[ '0.2.0-alpha', '0.2.0-beta', -1 ],
	[ '0.2.0-beta', '0.2.0-beta.1', -1 ],
	[ '1.0.0-1', '1.0.0-beta', -1 ],
	[ 'v0.2.0-beta.1', '0.2.0-beta.1-r1', 0 ],
	/* unknown installed version */
	[ 'v0.1.0', null, 1 ],
	[ '', '', 0 ],
];

for (let c in cases)
	eq(`versionCompare(${c[0]}, ${c[1]})`, versionCompare(c[0], c[1]), c[2]);

done('version');
