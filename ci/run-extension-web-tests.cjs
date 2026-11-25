const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.resolve(__dirname, '..');
const web = path.join(root, 'build/ragflow/web');
const overlayWeb = path.join(root, 'extensions/model-platform/overlay/web');
const ts = require(path.join(web, 'node_modules/typescript'));

function collectOverlayFiles(dir, baseDir) {
  const results = [];
  if (!fs.existsSync(dir)) return results;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      results.push(...collectOverlayFiles(full, baseDir));
    } else if (/\.(ts|tsx)$/.test(entry.name)) {
      results.push(path.relative(baseDir, full).split(path.sep).join('/'));
    }
  }
  return results.sort();
}

const overlayFiles = collectOverlayFiles(overlayWeb, overlayWeb);
if (overlayFiles.length === 0) {
  console.error('No TypeScript overlay files found under extensions/model-platform/overlay/web.');
  process.exit(1);
}

const missing = overlayFiles.filter((rel) => !fs.existsSync(path.join(web, rel)));
if (missing.length > 0) {
  console.error('Missing assembled overlay files in build/ragflow/web:');
  for (const rel of missing) console.error(`  - ${rel}`);
  process.exit(1);
}

const tsconfigPath = path.join(web, 'tsconfig.json');
const readResult = ts.readConfigFile(tsconfigPath, ts.sys.readFile);
if (readResult.error) {
  console.error(ts.flattenDiagnosticMessageText(readResult.error.messageText, '\n'));
  process.exit(1);
}

const parsed = ts.parseJsonConfigFileContent(readResult.config, ts.sys, web);
const types = Array.from(new Set([...(parsed.options.types ?? []), 'jest']));
const options = { ...parsed.options, types, noEmit: true };

const host = ts.createCompilerHost(options);
host.getCurrentDirectory = () => web;

const targetFiles = new Set(overlayFiles.map((rel) => fs.realpathSync(path.join(web, rel))));
const program = ts.createProgram({ rootNames: parsed.fileNames, options, host });

const diagnostics = ts
  .getPreEmitDiagnostics(program)
  .filter((d) => d.file && targetFiles.has(fs.realpathSync(d.file.fileName)));

if (diagnostics.length > 0) {
  const formatted = ts.formatDiagnosticsWithColorAndContext(diagnostics, {
    getCurrentDirectory: () => web,
    getCanonicalFileName: (f) => f,
    getNewLine: () => '\n',
  });
  console.error(formatted);
  console.error(`TypeScript check failed with ${diagnostics.length} error(s) in frontend overlay files.`);
  process.exit(1);
}

console.log(`TypeScript check passed for ${overlayFiles.length} frontend overlay file(s).`);

const jestResult = spawnSync(
  path.join(web, 'node_modules/.bin/jest'),
  [
    '--no-cache',
    '--coverage=false',
    '--maxWorkers=2',
    '--testPathPattern=^.*/src/ee/',
  ],
  {
    cwd: web,
    stdio: 'inherit',
  },
);
if (jestResult.error) throw jestResult.error;
process.exit(jestResult.status ?? 1);
