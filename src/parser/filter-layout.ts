import type { Token } from "../lexer.js";

export function filterLayout(tokens: readonly Token[]): Token[] {
  const result: Token[] = [];

  for (const token of tokens) {
    switch (token.kind) {
      case "Newline":
      case "Indent":
      case "Dedent":
        continue;

      default:
        result.push(token);
    }
  }

  return result;
}
