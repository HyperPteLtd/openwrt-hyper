/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * MD5 (RFC 1321) in plain ucode. OpenWrt 23.05 has no ucode-mod-digest, and
 * Hyper only hashes short strings (check-in account keys, node section names),
 * so this keeps the package installable there with the same output.
 *
 * Copyright (C) 2026 Hyper Pte. Ltd.
 */

'use strict';

const MASK = 0xffffffff;

const SHIFT = [
	7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22,
	5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20,
	4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23,
	6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21
];

/* floor(abs(sin(i + 1)) * 2^32) */
const K = [
	0xd76aa478, 0xe8c7b756, 0x242070db, 0xc1bdceee,
	0xf57c0faf, 0x4787c62a, 0xa8304613, 0xfd469501,
	0x698098d8, 0x8b44f7af, 0xffff5bb1, 0x895cd7be,
	0x6b901122, 0xfd987193, 0xa679438e, 0x49b40821,
	0xf61e2562, 0xc040b340, 0x265e5a51, 0xe9b6c7aa,
	0xd62f105d, 0x02441453, 0xd8a1e681, 0xe7d3fbc8,
	0x21e1cde6, 0xc33707d6, 0xf4d50d87, 0x455a14ed,
	0xa9e3e905, 0xfcefa3f8, 0x676f02d9, 0x8d2a4c8a,
	0xfffa3942, 0x8771f681, 0x6d9d6122, 0xfde5380c,
	0xa4beea44, 0x4bdecfa9, 0xf6bb4b60, 0xbebfbc70,
	0x289b7ec6, 0xeaa127fa, 0xd4ef3085, 0x04881d05,
	0xd9d4d039, 0xe6db99e5, 0x1fa27cf8, 0xc4ac5665,
	0xf4292244, 0x432aff97, 0xab9423a7, 0xfc93a039,
	0x655b59c3, 0x8f0ccc92, 0xffeff47d, 0x85845dd1,
	0x6fa87e4f, 0xfe2ce6e0, 0xa3014314, 0x4e0811a1,
	0xf7537e82, 0xbd3af235, 0x2ad7d2bb, 0xeb86d391
];

function rotl(x, c) {
	return ((x << c) | (x >> (32 - c))) & MASK;
}

/* Hex MD5 of a byte string, the same as digest's md5() and `md5sum` */
export function md5(str) {
	str = `${str}`;
	const len = length(str);

	/* bytes + 0x80 + zero padding to 56 mod 64 + 64-bit little-endian bit length */
	const bytes = [];
	for (let i = 0; i < len; i++)
		push(bytes, ord(str, i));
	push(bytes, 0x80);
	while (length(bytes) % 64 !== 56)
		push(bytes, 0);
	const bits = len * 8;
	for (let i = 0; i < 8; i++)
		push(bytes, (bits >> (8 * i)) & 0xff);

	let a0 = 0x67452301, b0 = 0xefcdab89, c0 = 0x98badcfe, d0 = 0x10325476;
	const m = [];

	for (let off = 0; off < length(bytes); off += 64) {
		for (let j = 0; j < 16; j++) {
			const p = off + j * 4;
			m[j] = bytes[p] | (bytes[p + 1] << 8) | (bytes[p + 2] << 16) | (bytes[p + 3] << 24);
		}

		let a = a0, b = b0, c = c0, d = d0;
		for (let i = 0; i < 64; i++) {
			let f, g;
			if (i < 16) {
				f = (b & c) | ((b ^ MASK) & d);
				g = i;
			} else if (i < 32) {
				f = (d & b) | ((d ^ MASK) & c);
				g = (5 * i + 1) % 16;
			} else if (i < 48) {
				f = b ^ c ^ d;
				g = (3 * i + 5) % 16;
			} else {
				f = c ^ (b | (d ^ MASK));
				g = (7 * i) % 16;
			}
			f = (f + a + K[i] + m[g]) & MASK;
			a = d;
			d = c;
			c = b;
			b = (b + rotl(f, SHIFT[i])) & MASK;
		}

		a0 = (a0 + a) & MASK;
		b0 = (b0 + b) & MASK;
		c0 = (c0 + c) & MASK;
		d0 = (d0 + d) & MASK;
	}

	let hex = '';
	for (let word in [ a0, b0, c0, d0 ])
		for (let i = 0; i < 4; i++)
			hex += sprintf('%02x', (word >> (8 * i)) & 0xff);
	return hex;
};
