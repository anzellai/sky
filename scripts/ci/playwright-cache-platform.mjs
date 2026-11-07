#!/usr/bin/env node
// Browser archives are platform-specific even when their revision is identical.
// Read public OS metadata only; never inspect the process environment.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { platform, arch, release } from 'node:os';
const os = platform();
const metadata = os === 'linux' ? readFileSync('/etc/os-release') : Buffer.from(release());
const digest = createHash('sha256').update(os).update('\0').update(arch()).update('\0').update(metadata).digest('hex');
console.log(`platform=${digest}`);
