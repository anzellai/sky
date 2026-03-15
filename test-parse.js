import { parse } from "./dist/parser/parser.js";
import { lex } from "./dist/lexer/lexer.js";
import { filterLayout } from "./dist/parser/filter-layout.js";
import fs from "fs";

const code = fs.readFileSync("src/stdlib/Sky/Core/String.sky", "utf8");
const { tokens } = lex(code);
const layoutTokens = filterLayout(tokens);
const ast = parse(layoutTokens);
console.log(JSON.stringify(ast, null, 2));
