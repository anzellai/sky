import fs from "fs";
import process from "process";
import { formatModule } from "../../lsp/formatter/formatter.js";
import { lex } from "../../lexer/lexer.js";
import { filterLayout } from "../../parser/filter-layout.js";
import { parse } from "../../parser/parser.js";

export async function handleFmt(fileOrDir: string) {
  if (fileOrDir === "-") {
    const source = fs.readFileSync(0, "utf8");
    try {
      const { tokens } = lex(source, "stdin");
      const filtered = filterLayout(tokens);
      const ast = parse(filtered);
      const formatted = formatModule(ast, source);
      process.stdout.write(formatted);
    } catch (e: any) {
      console.error(`Failed to format stdin: ${e.message}`);
      process.exit(1);
    }
    return;
  }

  if (!fileOrDir) {
    console.error("Usage: sky fmt <file-or-dir>");
    process.exit(1);
  }

  function formatFile(filePath: string) {
    if (!filePath.endsWith(".sky") && !filePath.endsWith(".skyi")) return;
    try {
      const source = fs.readFileSync(filePath, "utf8");
      const { tokens } = lex(source, filePath);
      const filtered = filterLayout(tokens);
      const ast = parse(filtered);
      const formatted = formatModule(ast, source);

      if (source !== formatted) {
        // Roundtrip safety: verify formatted output re-parses
        try {
          const { tokens: t2 } = lex(formatted, filePath);
          const f2 = filterLayout(t2);
          parse(f2);
        } catch (e2: any) {
          console.error(`Skipping ${filePath}: formatted output would not re-parse (${e2.message})`);
          return;
        }
        fs.writeFileSync(filePath, formatted, "utf8");
        console.log(`Formatted ${filePath}`);
      }
    } catch (e: any) {
      console.error(`Failed to format ${filePath}: ${e.message}`);
    }
  }

  function walk(dir: string) {
    const stat = fs.statSync(dir);
    if (stat.isFile()) {
      formatFile(dir);
    } else if (stat.isDirectory()) {
      for (const item of fs.readdirSync(dir)) {
        walk(dir + "/" + item);
      }
    }
  }

  walk(fileOrDir);
}
