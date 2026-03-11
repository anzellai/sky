// src/lexer.ts
// Sky compiler: production-oriented lexer foundation
//
// Goals:
// - precise source spans for diagnostics
// - stable token model for parser + formatter + IDE tooling
// - indentation-aware token stream (Python/Elm style)
// - no external dependencies
//
// Notes:
// - This file is intentionally robust and reusable.
// - The parser can consume INDENT/DEDENT tokens directly.
// - Newlines are preserved as real tokens for better recovery.

export type SkyKeyword =
  | "module"
  | "exposing"
  | "import"
  | "as"
  | "type"
  | "alias"
  | "let"
  | "in"
  | "if"
  | "then"
  | "else"
  | "case"
  | "of"
  | "foreign"
  | "from"
  | "port";

export type TokenKind =
  | "Identifier"
  | "UpperIdentifier"
  | "Integer"
  | "Float"
  | "String"
  | "Char"
  | "Keyword"
  | "Operator"
  | "Equals"
  | "Colon"
  | "Comma"
  | "Dot"
  | "Pipe"
  | "Arrow"
  | "Backslash"
  | "LParen"
  | "RParen"
  | "LBracket"
  | "RBracket"
  | "LBrace"
  | "RBrace"
  | "Newline"
  | "Indent"
  | "Dedent"
  | "EOF";

export interface SourcePosition {
  readonly offset: number;
  readonly line: number;
  readonly column: number;
}

export interface SourceSpan {
  readonly start: SourcePosition;
  readonly end: SourcePosition;
}

export interface Token {
  readonly kind: TokenKind;
  readonly lexeme: string;
  readonly span: SourceSpan;
  readonly keyword?: SkyKeyword;
}

export interface Diagnostic {
  readonly severity: "error" | "warning";
  readonly message: string;
  readonly span: SourceSpan;
  readonly hint?: string;
}

export interface LexResult {
  readonly tokens: Token[];
  readonly diagnostics: Diagnostic[];
}

const KEYWORDS: ReadonlySet<string> = new Set([
  "module",
  "exposing",
  "import",
  "as",
  "type",
  "alias",
  "let",
  "in",
  "if",
  "then",
  "else",
  "case",
  "of",
  "foreign",
  "from",
  "port",
]);

const OPERATOR_CHARS = new Set(["+", "-", "*", "/", "%", "<", ">", "!", "?", "&", "|", "^", "~"]);

export class Lexer {
  private readonly source: string;
  private readonly fileName: string;
  private offset = 0;
  private line = 1;
  private column = 1;
  private readonly tokens: Token[] = [];
  private readonly diagnostics: Diagnostic[] = [];
  private readonly indentStack: number[] = [0];
  private atStartOfLine = true;

  constructor(source: string, fileName = "<memory>") {
    this.source = source.replace(/\r\n/g, "\n");
    this.fileName = fileName;
  }

  public lex(): LexResult {
    while (!this.isEOF()) {
      if (this.atStartOfLine) {
        this.lexIndentation();
        if (this.isEOF()) break;
      }

      const ch = this.peek();

      if (ch === " ") {
        this.advance();
        continue;
      }

      if (ch === "\t") {
        this.reportCurrent("Tabs are not allowed in Sky source files.", "Use spaces for indentation and alignment.");
        this.advance();
        continue;
      }

      if (ch === "\n") {
        this.lexNewline();
        continue;
      }

      if (ch === "-" && this.peek(1) === "-") {
        this.lexLineComment();
        continue;
      }

      if (ch === '{' && this.peek(1) === '-') {
        this.lexBlockComment();
        continue;
      }

      if (isIdentifierStart(ch)) {
        this.lexIdentifierOrKeyword();
        continue;
      }

      if (isDigit(ch)) {
        this.lexNumber();
        continue;
      }

      if (ch === '"') {
        this.lexString();
        continue;
      }

      if (ch === "'") {
        this.lexChar();
        continue;
      }

      if (ch === "|") {
        if (!OPERATOR_CHARS.has(this.peek(1))) {
          this.pushSimple("Pipe", 1);
          continue;
        }
      }

      if (ch === "-") {
        if (this.peek(1) === ">") {
          this.pushSimple("Arrow", 2);
        } else {
          this.lexOperator();
        }
        continue;
      }

      if (OPERATOR_CHARS.has(ch)) {
        this.lexOperator();
        continue;
      }

      switch (ch) {
        case "=":
          this.pushSimple("Equals", 1);
          break;
        case ":":
          this.pushSimple("Colon", 1);
          break;
        case ",":
          this.pushSimple("Comma", 1);
          break;
        case ".":
          this.pushSimple("Dot", 1);
          break;
        case "|":
          this.pushSimple("Pipe", 1);
          break;
        case "\\":
          this.pushSimple("Backslash", 1);
          break;
        case "(":
          this.pushSimple("LParen", 1);
          break;
        case ")":
          this.pushSimple("RParen", 1);
          break;
        case "[":
          this.pushSimple("LBracket", 1);
          break;
        case "]":
          this.pushSimple("RBracket", 1);
          break;
        case "{":
          this.pushSimple("LBrace", 1);
          break;
        case "}":
          this.pushSimple("RBrace", 1);
          break;
        default:
          this.reportCurrent(`Unexpected character ${JSON.stringify(ch)}.`, "Remove it or replace it with valid Sky syntax.");
          this.advance();
          break;
      }
    }

    this.closeIndentation();
    this.tokens.push(this.makeToken("EOF", "", this.currentPosition(), this.currentPosition()));

    return {
      tokens: this.tokens,
      diagnostics: this.diagnostics,
    };
  }

  private lexIndentation(): void {
    const start = this.currentPosition();
    let spaces = 0;

    while (!this.isEOF()) {
      const ch = this.peek();
      if (ch === " ") {
        spaces += 1;
        this.advance();
        continue;
      }
      if (ch === "\t") {
        this.reportCurrent("Tabs are not allowed in indentation.", "Indent with spaces only.");
        this.advance();
        continue;
      }
      break;
    }

    const next = this.peek();
    if (next === "\n") {
      this.atStartOfLine = true;
      return;
    }

    if ((next === "-" && this.peek(1) === "-") || (next === '{' && this.peek(1) === '-')) {
      this.atStartOfLine = false;
      return;
    }

    const current = this.indentStack[this.indentStack.length - 1];
    if (spaces > current) {
      this.indentStack.push(spaces);
      const end = this.currentPosition();
      this.tokens.push(this.makeToken("Indent", this.source.slice(start.offset, end.offset), start, end));
    } else if (spaces < current) {
      while (this.indentStack.length > 1 && spaces < this.indentStack[this.indentStack.length - 1]) {
        this.indentStack.pop();
        const end = this.currentPosition();
        this.tokens.push(this.makeToken("Dedent", "", start, end));
      }
      if (spaces !== this.indentStack[this.indentStack.length - 1]) {
        this.diagnostics.push({
          severity: "error",
          message: `Invalid indentation level ${spaces}.`,
          span: { start, end: this.currentPosition() },
          hint: `Expected indentation to match one of: ${this.indentStack.join(", ")}.`,
        });
      }
    }

    this.atStartOfLine = false;
  }

  private lexNewline(): void {
    const start = this.currentPosition();
    this.advance();
    const end = this.currentPosition();
    this.tokens.push(this.makeToken("Newline", "\n", start, end));
    this.atStartOfLine = true;
  }

  private lexLineComment(): void {
    while (!this.isEOF() && this.peek() !== "\n") {
      this.advance();
    }
  }

  private lexBlockComment(): void {
    const start = this.currentPosition();
    this.advance(); // {
    this.advance(); // -
    let depth = 1;

    while (!this.isEOF() && depth > 0) {
      if (this.peek() === '{' && this.peek(1) === '-') {
        depth += 1;
        this.advance();
        this.advance();
        continue;
      }
      if (this.peek() === '-' && this.peek(1) === '}') {
        depth -= 1;
        this.advance();
        this.advance();
        continue;
      }
      this.advance();
    }

    if (depth !== 0) {
      this.diagnostics.push({
        severity: "error",
        message: "Unterminated block comment.",
        span: { start, end: this.currentPosition() },
        hint: "Close the comment with -}.",
      });
    }
  }

  private lexIdentifierOrKeyword(): void {
    const start = this.currentPosition();
    let lexeme = "";

    while (!this.isEOF() && isIdentifierPart(this.peek())) {
      lexeme += this.advance();
    }

    const end = this.currentPosition();
    if (KEYWORDS.has(lexeme)) {
      this.tokens.push(this.makeToken("Keyword", lexeme, start, end, lexeme as SkyKeyword));
      return;
    }

    const kind: TokenKind = isUppercase(lexeme[0]) ? "UpperIdentifier" : "Identifier";
    this.tokens.push(this.makeToken(kind, lexeme, start, end));
  }

  private lexNumber(): void {
    const start = this.currentPosition();
    let lexeme = "";

    while (!this.isEOF() && isDigit(this.peek())) {
      lexeme += this.advance();
    }

    let kind: TokenKind = "Integer";
    if (this.peek() === "." && isDigit(this.peek(1))) {
      kind = "Float";
      lexeme += this.advance();
      while (!this.isEOF() && isDigit(this.peek())) {
        lexeme += this.advance();
      }
    }

    this.tokens.push(this.makeToken(kind, lexeme, start, this.currentPosition()));
  }

  private lexString(): void {
    const start = this.currentPosition();
    let lexeme = "";
    this.advance();

    while (!this.isEOF()) {
      const ch = this.peek();
      if (ch === '"') {
        this.advance();
        this.tokens.push(this.makeToken("String", lexeme, start, this.currentPosition()));
        return;
      }
      if (ch === "\\") {
        this.advance();
        if (this.isEOF()) break;
        lexeme += this.readEscapedCharacter();
        continue;
      }
      if (ch === "\n") {
        this.diagnostics.push({
          severity: "error",
          message: "Unterminated string literal.",
          span: { start, end: this.currentPosition() },
          hint: "Terminate the string before the end of the line.",
        });
        return;
      }
      lexeme += this.advance();
    }

    this.diagnostics.push({
      severity: "error",
      message: "Unterminated string literal.",
      span: { start, end: this.currentPosition() },
      hint: 'Close the string with a ".',
    });
  }

  private lexChar(): void {
    const start = this.currentPosition();
    this.advance();

    let value = "";
    if (this.isEOF()) {
      this.reportSpan(start, this.currentPosition(), "Unterminated character literal.", "Close the character with '.");
      return;
    }

    if (this.peek() === "\\") {
      this.advance();
      value = this.readEscapedCharacter();
    } else {
      value = this.advance();
    }

    if (this.peek() !== "'") {
      this.reportSpan(start, this.currentPosition(), "Character literal must contain exactly one character.", "Use a single quoted character like 'a'.");
      return;
    }

    this.advance();
    this.tokens.push(this.makeToken("Char", value, start, this.currentPosition()));
  }

  private lexOperator(): void {
    const start = this.currentPosition();
    let lexeme = "";

    while (!this.isEOF() && OPERATOR_CHARS.has(this.peek())) {
      lexeme += this.advance();
    }

    this.tokens.push(this.makeToken("Operator", lexeme, start, this.currentPosition()));
  }

  private readEscapedCharacter(): string {
    const ch = this.advance();
    switch (ch) {
      case "n":
        return "\n";
      case "r":
        return "\r";
      case "t":
        return "\t";
      case '"':
        return '"';
      case "'":
        return "'";
      case "\\":
        return "\\";
      default:
        this.reportCurrent(`Unknown escape sequence \\${ch}.`, "Use one of: \\n, \\r, \\t, \\\\, \", or \\'.");
        return ch;
    }
  }

  private closeIndentation(): void {
    const pos = this.currentPosition();
    while (this.indentStack.length > 1) {
      this.indentStack.pop();
      this.tokens.push(this.makeToken("Dedent", "", pos, pos));
    }
  }

  private pushSimple(kind: TokenKind, length: number): void {
    const start = this.currentPosition();
    let lexeme = "";
    for (let i = 0; i < length; i += 1) {
      lexeme += this.advance();
    }
    this.tokens.push(this.makeToken(kind, lexeme, start, this.currentPosition()));
  }

  private makeToken(kind: TokenKind, lexeme: string, start: SourcePosition, end: SourcePosition, keyword?: SkyKeyword): Token {
    return { kind, lexeme, span: { start, end }, keyword };
  }

  private reportCurrent(message: string, hint?: string): void {
    const pos = this.currentPosition();
    this.diagnostics.push({
      severity: "error",
      message,
      span: { start: pos, end: pos },
      hint,
    });
  }

  private reportSpan(start: SourcePosition, end: SourcePosition, message: string, hint?: string): void {
    this.diagnostics.push({
      severity: "error",
      message,
      span: { start, end },
      hint,
    });
  }

  private currentPosition(): SourcePosition {
    return {
      offset: this.offset,
      line: this.line,
      column: this.column,
    };
  }

  private peek(ahead = 0): string {
    return this.source[this.offset + ahead] ?? "\0";
  }

  private advance(): string {
    const ch = this.source[this.offset] ?? "\0";
    this.offset += 1;
    if (ch === "\n") {
      this.line += 1;
      this.column = 1;
    } else {
      this.column += 1;
    }
    return ch;
  }

  private isEOF(): boolean {
    return this.offset >= this.source.length;
  }
}

export function lex(source: string, fileName?: string): LexResult {
  return new Lexer(source, fileName).lex();
}

function isIdentifierStart(ch: string): boolean {
  return /[A-Za-z_]/.test(ch);
}

function isIdentifierPart(ch: string): boolean {
  return /[A-Za-z0-9_']/.test(ch);
}

function isDigit(ch: string): boolean {
  return /[0-9]/.test(ch);
}

function isUppercase(ch: string): boolean {
  return ch >= "A" && ch <= "Z";
}
