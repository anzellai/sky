import * as esbuild from "esbuild";
import fs from "fs";

esbuild.buildSync({
  entryPoints: ["src/bin/sky.ts"],
  bundle: true,
  platform: "node",
  outfile: "dist/bin/sky.cjs",
  format: "esm",
  banner: { js: `import { createRequire } from 'module'; const require = createRequire(import.meta.url);` }
});

esbuild.buildSync({
  entryPoints: ["src/bin/sky-lsp.ts"],
  bundle: true,
  platform: "node",
  outfile: "dist/bin/sky-lsp.cjs",
  format: "esm",
  banner: { js: `import { createRequire } from 'module'; const require = createRequire(import.meta.url);` }
});

// Since node supports single executable applications natively from 20.0
// but we just want an executable, we can just make it executable and add shebang
const skySource = fs.readFileSync("dist/bin/sky.cjs", "utf8");
fs.writeFileSync("bin/sky", "#!/usr/bin/env node\n" + skySource);
fs.chmodSync("bin/sky", "755");

const lspSource = fs.readFileSync("dist/bin/sky-lsp.cjs", "utf8");
fs.writeFileSync("bin/sky-lsp", "#!/usr/bin/env node\n" + lspSource);
fs.chmodSync("bin/sky-lsp", "755");

console.log("Built dist/sky and dist/sky-lsp successfully.");
