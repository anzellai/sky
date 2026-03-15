import { parse } from "./src/parser/parser.js";
import { lex } from "./src/lexer/lexer.js";
import fs from "fs";

const code = fs.readFileSync("src/stdlib/Sky/Core/String.sky", "utf8");
const tokens = lex(code);
const ast = parse(tokens);
console.log(JSON.stringify(ast, null, 2));
