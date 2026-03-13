import * as esbuild from 'esbuild';
import fs from 'fs';
import path from 'path';

async function bundle() {
  const commonOptions = {
    bundle: true,
    platform: 'node',
    format: 'esm',
    target: 'node20',
    minify: true,
    sourcemap: true,
    // We need to keep these as external if we want to support dynamic FFI extraction 
    // using the user's local typescript installation, but for a standalone binary
    // we bundle everything.
    external: ['fsevents'], 
  };

  console.log('Bundling sky...');
  await esbuild.build({
    ...commonOptions,
    entryPoints: ['src/bin/sky.ts'],
    outfile: 'dist/bin/sky',
    banner: {
      js: '#!/usr/bin/env node',
    },
  });

  console.log('Bundling sky-lsp...');
  await esbuild.build({
    ...commonOptions,
    entryPoints: ['src/bin/sky-lsp.ts'],
    outfile: 'dist/bin/sky-lsp',
    banner: {
      js: '#!/usr/bin/env node',
    },
  });

  // Ensure stdlib and runtime are available to the bundled compiler.
  // Since we are bundling everything into a single file, the compiler needs to know
  // where its internal assets are. 
  // We will continue to copy them to dist/ for now, but in a true single-binary
  // we might want to embed them as strings.
  
  console.log('Copying assets...');
  fs.mkdirSync('dist/stdlib', { recursive: true });
  fs.cpSync('src/stdlib', 'dist/stdlib', { recursive: true });
  fs.mkdirSync('dist/runtime', { recursive: true });
  fs.cpSync('src/runtime', 'dist/runtime', { recursive: true });

  console.log('Done!');
}

bundle().catch((err) => {
  console.error(err);
  process.exit(1);
});
