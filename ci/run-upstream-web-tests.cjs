const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.resolve(__dirname, '..');
const web = path.join(root, 'build/ragflow/web');
const manifest = require('./upstream-web-known-failures.json');
const revision = spawnSync('git', ['-C', path.join(root, 'upstream/ragflow'), 'rev-parse', 'HEAD'], { encoding: 'utf8' });
if (revision.status !== 0 || revision.stdout.trim() !== manifest.upstreamCommit) {
  throw new Error('Upstream revision differs from the web test exclusions; review the list before updating it.');
}
const seen = new Set();
const patterns = ['/node_modules/', '/dist/'];
for (const entry of manifest.excludedTests) {
  if (!/^src\/[\w./-]+\.test\.(ts|tsx|js|jsx)$/.test(entry.path) || entry.path.includes('..') || !entry.reason || seen.has(entry.path)) {
    throw new Error(`Invalid or duplicate excluded test: ${entry.path}`);
  }
  if (!fs.existsSync(path.join(web, entry.path))) {
    throw new Error(`Excluded upstream test no longer exists: ${entry.path}`);
  }
  seen.add(entry.path);
  patterns.push(`/${entry.path.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);
}
const summary = [
  `### Upstream web tests: ${manifest.upstreamVersion}`,
  '',
  `Excluded ${seen.size} known failing test files; all other discovered upstream tests must pass.`,
  '',
  ...manifest.excludedTests.map(({ path: testPath, reason }) => `- \`${testPath}\`: ${reason}`),
  '',
].join('\n');
console.log(summary);
if (process.env.GITHUB_STEP_SUMMARY) fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${summary}\n`);
const result = spawnSync('npm', ['test', '--', '--runInBand', `--testPathIgnorePatterns=${patterns.join('|')}`], {
  cwd: web,
  stdio: 'inherit',
});
if (result.error) throw result.error;
process.exit(result.status ?? 1);
