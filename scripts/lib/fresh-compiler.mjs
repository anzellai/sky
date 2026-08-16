// scripts/lib/fresh-compiler.mjs — the Node face of scripts/lib/fresh-compiler.sh.
//
// It does NOT reimplement the check. It runs the shell library, which is the
// single definition of what "the compiler is current with this tree" means.
// `scripts/lib/with-timeout.sh` was written after six scripts had each grown
// their own `timeout` fallback in four subtly different variants; a second
// notion of freshness written in JavaScript would be the same mistake with a
// different noun.
//
// Usage:
//     import { requireFreshCompiler } from './lib/fresh-compiler.mjs';
//     requireFreshCompiler(skyBin, repoRoot);   // exits non-zero if stale

import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const LIB = path.join(path.dirname(fileURLToPath(import.meta.url)), 'fresh-compiler.sh');

/**
 * Exit the process unless `bin` is an executable at least as new as every
 * source input that determines what a `sky` binary does.
 *
 * The shell library owns the message and the exit status; this passes both
 * through unchanged so a Node caller and a shell caller are indistinguishable
 * from the outside.
 */
export function requireFreshCompiler(bin, repoRoot) {
    const r = spawnSync('/bin/bash', [LIB, bin, repoRoot], {
        stdio: ['ignore', 'pipe', 'inherit'],
        encoding: 'utf8',
    });
    // ENOENT on bash itself. Refusing to run is the only honest answer: this
    // function exists so the caller cannot proceed on an unverified compiler,
    // and "the checker would not start" is not verification.
    if (r.error) {
        console.error(`FAIL: could not run ${LIB}: ${r.error.message}`);
        console.error('  A gate cannot pass on a freshness check that did not run.');
        process.exit(2);
    }
    if (r.status !== 0) {
        process.exit(r.status === null ? 2 : r.status);
    }
}
