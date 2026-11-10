import { eq, done } from 'lib';
import { md5 } from 'md5';

/* RFC 1321 vectors, MD5 padding edges (55/56/63/64/65 bytes), UTF-8; expected values from Python hashlib */
const vectors = [
	[ "", 'd41d8cd98f00b204e9800998ecf8427e' ],
	[ "a", '0cc175b9c0f1b6a831c399e269772661' ],
	[ "abc", '900150983cd24fb0d6963f7d28e17f72' ],
	[ "message digest", 'f96b697d7cb7938d525a2f31aaf161d0' ],
	[ "The quick brown fox jumps over the lazy dog", '9e107d9d372bb6826bd81d3542a419d6' ],
	[ "astra@example.com", '2a02ef03ea339efb0d36e1a50f596b81' ],
	[ "日本 · 东京 01", '3c761f5a4c1963d764e07267764ce5c0' ],
	[ "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", '04364420e25c512fd958a70738aa8f72' ],
	[ "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", '6c830a5d23df58dbe0171eb5639bf3e4' ],
	[ "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", 'cac9376765157678c96c250aee8ffb87' ],
	[ "wwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwww", 'e03817e1560f2bda422e74827ecead8c' ],
	[ "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv", '8e628dd29d63f51c6e6ee2e7cce78883' ],
	[ "uuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu", '8482863a49174ab425658403e6069bef' ],
	[ "JP|anytls|jp1.example.com|443|日本 · 东京 01", '66979c93d9d6bb9ec986ed150d517e58' ],
	[ "\u0001\u0002\u0003\u0004\u0005\u0006\u0007\b\t\n\u000b\f\r\u000e\u000f\u0010\u0011\u0012\u0013\u0014\u0015\u0016\u0017\u0018\u0019\u001a\u001b\u001c\u001d\u001e\u001f !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~", 'ca927a2522f2e2d0c150c1e1a89139ce' ],
];

for (let v in vectors)
	eq(`md5 of ${length(v[0])} bytes`, md5(v[0]), v[1]);
eq('number input', md5(42), md5('42'));

done('md5');
