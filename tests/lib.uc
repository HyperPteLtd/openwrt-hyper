/* Minimal assertion helpers for ucode tests */

let failed = 0, passed = 0;

export function eq(name, got, want) {
	const g = sprintf('%J', got), w = sprintf('%J', want);
	if (g === w) {
		passed++;
	} else {
		failed++;
		warn(`FAIL ${name}\n  got:  ${g}\n  want: ${w}\n`);
	}
};

export function ok(name, cond) {
	eq(name, !!cond, true);
};

export function done(suite) {
	print(`${suite}: ${passed} passed, ${failed} failed\n`);
	exit(failed ? 1 : 0);
};
