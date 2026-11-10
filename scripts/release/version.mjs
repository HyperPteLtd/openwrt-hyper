import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { analyzeCommits } from '@semantic-release/commit-analyzer';
import semver from 'semver';

function git(...args) {
  return execFileSync('git', args, { encoding: 'utf8' }).trimEnd();
}

const tags = git('tag', '--list', 'v*', '--sort=-version:refname').split('\n');
const previous = tags.find((tag) => /^v\d+\.\d+\.\d+$/.test(tag) && semver.valid(tag));
if (previous && !git('tag', '--merged', 'HEAD', '--list', previous)) {
  console.error(`Skipping automatic release: ${previous} is outside this commit's history.`);
  process.exit(0);
}

const range = previous ? `${previous}..HEAD` : 'HEAD';
const log = git('log', '--format=%B', '-z', range);
const commits = log.split('\0').filter(Boolean).map((message) => ({ message }));
const releaseType = await analyzeCommits(
  {
    preset: 'conventionalcommits',
    releaseRules: [
      { breaking: true, release: 'major' },
      { type: 'refactor', release: 'patch' },
    ],
  },
  {
    cwd: fileURLToPath(new URL('.', import.meta.url)),
    commits,
    logger: { log: (...args) => console.error(...args) },
  },
);

if (releaseType) {
  const initial = readFileSync('luci-app-hyper/Makefile', 'utf8').match(/^PKG_VERSION:=(\S+)$/m)?.[1];
  const version = previous ? semver.inc(previous, releaseType) : semver.valid(initial);
  if (!version) throw new Error('Could not determine a valid OpenWrt package version');
  console.log(`v${version}`);
}
