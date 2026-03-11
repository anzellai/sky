// src/parser.ts
// Sky parser
//
// Upgraded Pratt-style parser supporting:
// - Elm pipelines (|>, <|)
// - composition (>>, <<)
// - operator sections
// - whitespace function application
// - multiline pipeline chains

import type { Token } from "./lexer.js";
import * as AST from "./ast.js";
import { getOperatorInfo } from "./parser/operator-table.js";
import { buildLeftSection, buildRightSection } from "./parser/sections.js";

export class Parser {
  private pos = 0;

  constructor(private readonly tokens: Token[]) { }

  private peek(offset = 0): Token {
    return this.tokens[this.pos + offset] ?? this.tokens[this.tokens.length - 1];
  }

  private previous(): Token {
    return this.tokens[this.pos - 1] ?? this.tokens[0];
  }

  private match(kind: string, lexeme?: string): boolean {
    const t = this.peek();
    if (t.kind !== kind) return false;
    if (lexeme !== undefined && t.lexeme !== lexeme) return false;
    return true;
  }

  private consume(kind: string, lexeme?: string): Token {
    const token = this.peek();

    if (!this.match(kind, lexeme)) {
      throw new Error(
        `Unexpected token ${token.kind}:${token.lexeme} at ${token.span.start.line}:${token.span.start.column}`
      );
    }

    this.pos++;
    return token;
  }

  parseModule(): AST.Module {

    const moduleToken = this.consume("Keyword", "module");

    const name = this.parseModuleName();

    let exposing: AST.ExposingClause | undefined;

    if (this.match("Keyword", "exposing")) {
      exposing = this.parseExposing();
    }

    const imports: AST.ImportDeclaration[] = [];

    while (this.match("Keyword", "import")) {
      imports.push(this.parseImport());
    }

    const declarations: AST.Declaration[] = [];

    while (!this.match("EOF")) {

      if (this.match("Identifier")) {
        declarations.push(this.parseFunction());
        continue;
      }

      const t = this.peek();

      throw new Error(
        `Unexpected token ${t.kind}:${t.lexeme} at ${t.span.start.line}:${t.span.start.column}`
      );
    }

    return {
      kind: "Module",
      name,
      exposing,
      imports,
      declarations,
      span: {
        start: moduleToken.span.start,
        end: this.previous().span.end,
      },
    };
  }

  private parseModuleName(): AST.ModuleName {

    const parts: string[] = [];

    parts.push(this.consume("UpperIdentifier").lexeme);

    while (this.match("Dot")) {
      this.consume("Dot");
      parts.push(this.consume("UpperIdentifier").lexeme);
    }

    return parts;
  }

  private parseExposing(): AST.ExposingClause {

    const start = this.consume("Keyword", "exposing");

    this.consume("LParen");

    if (this.peek().kind === "Dot" && this.peek(1).kind === "Dot") {
      this.consume("Dot");
      this.consume("Dot");
      const end = this.consume("RParen");
      return {
        kind: "ExposingClause",
        items: [],
        open: true,
        span: {
          start: start.span.start,
          end: end.span.end,
        },
      };
    }

    const items: AST.ExposedItem[] = [];

    while (!this.match("RParen")) {

      const nameToken = this.match("UpperIdentifier")
        ? this.consume("UpperIdentifier")
        : this.consume("Identifier");

      items.push({
        kind: nameToken.kind === "UpperIdentifier" ? "type" : "value",
        name: nameToken.lexeme,
        span: nameToken.span,
      } as AST.ExposedItem);

      if (this.match("Comma")) {
        this.consume("Comma");
      }
    }

    const end = this.consume("RParen");

    return {
      kind: "ExposingClause",
      items,
      open: false,
      span: {
        start: start.span.start,
        end: end.span.end,
      },
    };
  }

  private parseImport(): AST.ImportDeclaration {
    const start = this.consume("Keyword", "import");

    const moduleName = this.parseModuleName();

    let alias: AST.ImportAlias | undefined;
    if (this.match("Keyword", "as")) {
      this.consume("Keyword", "as");
      const aliasToken = this.consume("UpperIdentifier");
      alias = {
        kind: "ImportAlias",
        name: aliasToken.lexeme,
        span: aliasToken.span,
      };
    }

    let exposing: AST.ExposingClause | undefined;
    if (this.match("Keyword", "exposing")) {
      exposing = this.parseExposing();
    }

    return {
      kind: "ImportDeclaration",
      moduleName,
      alias,
      exposing,
      span: {
        start: start.span.start,
        end: this.previous().span.end,
      },
    } as AST.ImportDeclaration;
  }

  private parseFunction(): AST.FunctionDeclaration {

    const name = this.consume("Identifier")

    const params: AST.Parameter[] = []

    while (!this.match("Equals")) {

      const id = this.consume("Identifier")

      params.push({
        kind: "Parameter",
        pattern: {
          kind: "VariablePattern",
          name: id.lexeme,
          span: id.span
        },
        span: id.span
      })

    }

    this.consume("Equals")

    const body = this.parseExpression(0)

    return {
      kind: "FunctionDeclaration",
      name: name.lexeme,
      parameters: params,
      body,
      span: {
        start: name.span.start,
        end: body.span.end
      }
    }

  }

  private parseExpression(minPrecedence: number): AST.Expression {

    let left = this.parseApplication();

    while (true) {

      if (this.match("Equals")) break;

      if (!this.match("Operator")) break;

      const opToken = this.peek();

      const info = getOperatorInfo(opToken.lexeme);

      if (!info) break;

      if (info.precedence < minPrecedence) break;

      this.consume("Operator");

      const nextMin = info.associativity === "left"
        ? info.precedence + 1
        : info.precedence;

      const right = this.parseExpression(nextMin);

      left = {
        kind: "BinaryExpression",
        operator: opToken.lexeme,
        left,
        right,
        span: {
          start: left.span.start,
          end: right.span.end,
        },
      };

    }

    return left;
  }

  // Handles whitespace application like Elm
  private parseApplication(): AST.Expression {

    let expr = this.parsePrimary();

    while (true) {

      const save = this.pos;

      // Stop if next token is not a valid expression start
      if (!this.isStartOfPrimaryExpression()) {
        break;
      }

      // Stop if this would start a new declaration
      if (
        this.peek(1)?.kind === "Equals"
      ) {
        break;
      }

      const arg = this.parsePrimary();

      expr = {
        kind: "CallExpression",
        callee: expr,
        arguments: [arg],
        span: {
          start: expr.span.start,
          end: arg.span.end
        }
      };

    }

    return expr;

  }

  private isStartOfPrimaryExpression(): boolean {

    return (
      this.match("Identifier") ||
      this.match("UpperIdentifier") ||
      this.match("Integer") ||
      this.match("Float") ||
      this.match("String") ||
      this.match("LParen")
    );

  }

  private parsePrimary(): AST.Expression {

    if (this.match("Identifier")) {

      const t = this.consume("Identifier");

      return {
        kind: "IdentifierExpression",
        name: t.lexeme,
        span: t.span,
      };
    }

    if (this.match("Integer")) {

      const t = this.consume("Integer");

      return {
        kind: "IntegerLiteralExpression",
        value: Number(t.lexeme),
        raw: t.lexeme,
        span: t.span,
      };
    }

    if (this.match("String")) {

      const t = this.consume("String");

      return {
        kind: "StringLiteralExpression",
        value: t.lexeme,
        span: t.span,
      };
    }

    if (this.match("LParen")) {

      const start = this.consume("LParen");

      if (this.match("RParen")) {

        const end = this.consume("RParen");

        return {
          kind: "UnitExpression",
          span: {
            start: start.span.start,
            end: end.span.end,
          },
        };
      }

      if (this.match("Operator")) {

        const op = this.consume("Operator");

        this.consume("RParen");

        const right = this.parsePrimary();

        return buildLeftSection(op.lexeme, right, start.span);
      }

      const first = this.parseExpression(0);

      if (this.match("Operator")) {

        const op = this.consume("Operator");

        this.consume("RParen");

        return buildRightSection(first, op.lexeme, start.span);
      }

      const end = this.consume("RParen");

      return {
        kind: "ParenthesizedExpression",
        expression: first,
        span: {
          start: start.span.start,
          end: end.span.end,
        },
      };
    }

    const t = this.peek();

    throw new Error(
      `Unexpected token ${t.kind}:${t.lexeme} at ${t.span.start.line}:${t.span.start.column}`
    );
  }
}

export function parse(tokens: Token[]): AST.Module {
  const parser = new Parser(tokens);
  return parser.parseModule();
}
