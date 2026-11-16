import { eq, done } from 'lib';
import { mergeLogs } from 'logs';

const a = '2026-11-16 10:00:01 [DAEMON] Ready\n2026-11-16 10:00:03 [DAEMON] Stopped';
const b = '+0800 2026-11-16 10:00:02 WARN Slow\n  detail\n+0800 2026-11-16 10:00:04 INFO Closed';
eq('interleaves timestamps and keeps continuations', mergeLogs([a, b], 10),
	'2026-11-16 10:00:01 [DAEMON] Ready\n+0800 2026-11-16 10:00:02 WARN Slow\n  detail\n2026-11-16 10:00:03 [DAEMON] Stopped\n+0800 2026-11-16 10:00:04 INFO Closed');
eq('limit keeps newest records', mergeLogs([a, b], 1), '+0800 2026-11-16 10:00:04 INFO Closed');
eq('empty missing source', mergeLogs(['', null], 10), '');
eq('unknown timestamp remains visible', mergeLogs(['FATAL failed before logging initialized'], 10), 'FATAL failed before logging initialized');
done('logs');
