import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { compileFunction } from 'node:vm';

// Execute the real script body. Only its imports are supplied by this fixture;
// production has no injection hooks, and no real OS or environment is queried.
const source = readFileSync(new URL('./playwright-cache-platform.mjs', import.meta.url), 'utf8');
const imports = [
  "import { createHash } from 'node:crypto';",
  "import { readFileSync } from 'node:fs';",
  "import { platform, arch, release } from 'node:os';",
];
let body = source.replace(/^#![^\n]*\n/, '');
for (const line of imports) {
  assert.equal(body.split(line).length, 2, `one exact import: ${line}`);
  body = body.replace(line, '');
}
const execute = compileFunction(body, ['createHash', 'readFileSync', 'platform', 'arch', 'release', 'console', 'process', 'Buffer']);
const ubuntu24 = 'ID=ubuntu\nVERSION_ID="24.04"\n';
const ubuntu26 = 'ID=ubuntu\nVERSION_ID="26.04"\n';
function run({ os = 'linux', cpu = 'x64', metadata = ubuntu24, kernel = 'fixture-kernel', missing = false, output = [] } = {}) {
  const lines = output;
  const reads = [];
  let releaseCalls = 0;
  const forbiddenProcess = new Proxy({}, { get() { throw new Error('environment access forbidden'); } });
  execute(createHash, (path) => {
    reads.push(path);
    assert.equal(path, '/etc/os-release');
    if (missing) throw Object.assign(new Error('fixture missing OS metadata'), { code: 'ENOENT' });
    return Buffer.from(metadata);
  }, () => os, () => cpu, () => { releaseCalls++; return kernel; }, { log: (line) => lines.push(line) }, forbiddenProcess, Buffer);
  assert.equal(lines.length, 1);
  assert.match(lines[0], /^platform=[0-9a-f]{64}$/);
  assert.deepEqual(reads, os === 'linux' ? ['/etc/os-release'] : []);
  assert.equal(releaseCalls, os === 'linux' ? 0 : 1);
  return lines[0];
}

test('same public platform inputs give the same cache digest', () => {
  assert.equal(run(), run());
});
test('Ubuntu 24 and 26 archives cannot share a cache key', () => {
  assert.notEqual(run(), run({ metadata: ubuntu26 }));
});
test('x64 and arm64 archives cannot share a cache key', () => {
  assert.notEqual(run(), run({ cpu: 'arm64' }));
});
test('non-Linux release and OS distinguish archive platforms', () => {
  const original = run({ os: 'darwin', kernel: 'fixture-1' });
  assert.notEqual(original, run({ os: 'darwin', kernel: 'fixture-2' }));
  assert.notEqual(original, run({ os: 'win32', kernel: 'fixture-1' }));
});
test('missing Linux OS metadata fails closed without emitting a key', () => {
  const output = [];
  assert.throws(() => run({ missing: true, output }), { code: 'ENOENT' });
  assert.deepEqual(output, []);
});
test('actual helper never reads process.env or any process property', () => {
  assert.match(run(), /^platform=/);
  assert.match(run({ os: 'darwin' }), /^platform=/);
});
