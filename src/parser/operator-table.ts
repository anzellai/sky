// src/parser/operator-table.ts
// Sky operator table
//
// Implements Elm-compatible precedence and associativity for:
// - |>  pipeline left
// - <|  application right
// - >>  composition right
// - <<  composition right
//
// This table is intentionally small and explicit so the parser can evolve
// without hard-coded precedence scattered through the codebase.

export type Associativity = "left" | "right" | "non";

export interface OperatorInfo {
  readonly precedence: number;
  readonly associativity: Associativity;
}

const TABLE: Readonly<Record<string, OperatorInfo>> = Object.freeze({
  // Elm-style application / pipeline family
  "|>": { precedence: 0, associativity: "left" },
  "<|": { precedence: 0, associativity: "right" },

  // Elm-style function composition
  ">>": { precedence: 9, associativity: "right" },
  "<<": { precedence: 9, associativity: "right" },

  // Common operators already supported by Sky
  "||": { precedence: 2, associativity: "right" },
  "&&": { precedence: 3, associativity: "right" },

  "==": { precedence: 4, associativity: "non" },
  "!=": { precedence: 4, associativity: "non" },
  "<": { precedence: 4, associativity: "non" },
  "<=": { precedence: 4, associativity: "non" },
  ">": { precedence: 4, associativity: "non" },
  ">=": { precedence: 4, associativity: "non" },

  "++": { precedence: 5, associativity: "right" },
  "::": { precedence: 5, associativity: "right" },
  "+": { precedence: 6, associativity: "left" },
  "-": { precedence: 6, associativity: "left" },

  "*": { precedence: 7, associativity: "left" },
  "/": { precedence: 7, associativity: "left" },
  "%": { precedence: 7, associativity: "left" },
});

export function getOperatorInfo(operator: string): OperatorInfo | undefined {
  return TABLE[operator];
}

export function isKnownOperator(operator: string): boolean {
  return operator in TABLE;
}
