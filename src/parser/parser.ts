// src/parser.ts
// Sky parser
//
// Upgraded Pratt-style parser supporting:
// - Elm pipelines (|>, <|)
// - composition (>>, <<)
// - operator sections
// - whitespace function application
// - multiline pipeline chains

import type { Token } from "../lexer/lexer.js";
import * as AST from "../ast/ast.js";
import { getOperatorInfo } from "../parser/operator-table.js";
import { buildLeftSection, buildRightSection } from "../parser/sections.js";

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

    // Automatically inject the standard library prelude unless we are compiling it,
    // or if the user already imported it explicitly (which the formatter writes out).
    const isPrelude = name.join(".") === "Sky.Core.Prelude";
    const hasPreludeImport = imports.some(imp => imp.moduleName.join(".") === "Sky.Core.Prelude");
    
    if (!isPrelude && !hasPreludeImport) {
      imports.unshift({
        kind: "ImportDeclaration",
        moduleName: ["Sky", "Core", "Prelude"],
        exposing: {
          kind: "ExposingClause",
          items: [],
          open: true,
          span: {
            start: moduleToken.span.start,
            end: moduleToken.span.start,
          },
        },
        span: {
          start: moduleToken.span.start,
          end: moduleToken.span.start,
        },
      } as AST.ImportDeclaration);
    }

    const declarations: AST.Declaration[] = [];

    while (!this.match("EOF")) {

      if (this.match("Keyword", "foreign") && this.peek(1).kind === "Keyword" && this.peek(1).lexeme === "import") {
        declarations.push(...this.parseForeignImports());
        continue;
      }

      if (this.match("Keyword", "type") && this.peek(1).kind === "Keyword" && this.peek(1).lexeme === "alias") {
        declarations.push(this.parseTypeAliasDeclaration());
        continue;
      }

      if (this.match("Keyword", "type")) {
        declarations.push(this.parseTypeDeclaration());
        continue;
      }

      if (this.match("Identifier") || this.match("UpperIdentifier")) {
        if (this.peek(1).kind === "Colon") {
          declarations.push(this.parseTypeAnnotation());
        } else if (this.match("Identifier")) {
          declarations.push(this.parseFunction());
        } else {
          const t = this.peek();
          throw new Error(
            `Unexpected top-level token ${t.kind}:${t.lexeme} at ${t.span.start.line}:${t.span.start.column}`
          );
        }
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

  private parseForeignImports(): AST.ForeignImportDeclaration[] {
    const start = this.consume("Keyword", "foreign");
    this.consume("Keyword", "import");

    const pkgToken = this.consume("String");
    const pkgName = pkgToken.lexeme.replace(/^"|"$/g, "");

    const exposing = this.parseExposing();

    return exposing.items.map((item) => ({
      kind: "ForeignImportDeclaration",
      name: item.name,
      sourceModule: pkgName,
      isDefault: false,
      // Fake type annotation since parser doesn't support them yet
      typeAnnotation: {
        kind: "TypeAnnotation",
        name: item.name,
        type: { kind: "TypeVariable", name: "Foreign", span: item.span } as unknown as AST.TypeExpression,
        span: item.span,
      },
      span: {
        start: start.span.start,
        end: exposing.span.end,
      },
    } as AST.ForeignImportDeclaration));
  }

  private parseTypeAliasDeclaration(): AST.TypeAliasDeclaration {
    const start = this.consume("Keyword", "type");
    this.consume("Keyword", "alias");

    const name = this.consume("UpperIdentifier");
    const typeParameters: string[] = [];
    while (this.match("Identifier")) {
      typeParameters.push(this.consume("Identifier").lexeme);
    }

    this.consume("Equals");

    const aliasedType = this.parseTypeExpression();

    return {
      kind: "TypeAliasDeclaration",
      name: name.lexeme,
      typeParameters,
      aliasedType,
      span: {
        start: start.span.start,
        end: aliasedType.span.end,
      },
    };
  }

  private parseTypeDeclaration(): AST.TypeDeclaration {
    const start = this.consume("Keyword", "type");

    const name = this.consume("UpperIdentifier");
    const typeParameters: string[] = [];
    while (this.match("Identifier")) {
      typeParameters.push(this.consume("Identifier").lexeme);
    }

    this.consume("Equals");

    const variants: AST.TypeVariant[] = [];

    while (true) {
      const variantName = this.consume("UpperIdentifier");
      const fields: AST.TypeExpression[] = [];

      while (
        this.match("UpperIdentifier") || 
        this.match("Identifier") || 
        this.match("LParen") || 
        this.match("LBrace")
      ) {
        if (this.peek().span.start.column === 1 || this.peek().kind === "Pipe") {
          break;
        }
        fields.push(this.parseTypePrimary());
      }

      variants.push({
        kind: "TypeVariant",
        name: variantName.lexeme,
        fields,
        span: {
          start: variantName.span.start,
          end: fields.length > 0 ? fields[fields.length - 1].span.end : variantName.span.end,
        },
      });

      if (this.match("Pipe")) {
        this.consume("Pipe");
      } else {
        break;
      }
    }

    return {
      kind: "TypeDeclaration",
      name: name.lexeme,
      typeParameters,
      variants,
      span: {
        start: start.span.start,
        end: variants[variants.length - 1].span.end,
      },
    };
  }

  private parseTypeAnnotation(): AST.TypeAnnotation {
    const name = this.match("UpperIdentifier") ? this.consume("UpperIdentifier") : this.consume("Identifier");
    this.consume("Colon");
    const type = this.parseTypeExpression();
    return {
      kind: "TypeAnnotation",
      name: name.lexeme,
      type,
      span: {
        start: name.span.start,
        end: type.span.end,
      },
    };
  }

  private parseTypeExpression(): AST.TypeExpression {
    const left = this.parseTypeApplication();

    if (this.match("Arrow")) {
      this.consume("Arrow");
      const right = this.parseTypeExpression();
      return {
        kind: "FunctionType",
        from: left,
        to: right,
        span: {
          start: left.span.start,
          end: right.span.end,
        },
      } as AST.TypeExpression;
    }

    return left;
  }

  private parseTypeApplication(): AST.TypeExpression {
    const target = this.parseTypePrimary();

    if (target.kind === "TypeReference" || target.kind === "TypeVariable") {
      const args: AST.TypeExpression[] = [];
      while (
        this.match("UpperIdentifier") || 
        this.match("Identifier") || 
        this.match("LParen") || 
        this.match("LBrace")
      ) {
        // Stop if the next token starts a new declaration or variant
        if (this.peek().span.start.column === 1 || this.peek().kind === "Equals" || this.peek().kind === "Pipe") {
           break;
        }

        // Stop if this looks like the start of an assignment (for let bindings with annotations)
        if ((this.peek().kind === "Identifier" || this.peek().kind === "UpperIdentifier") && this.peek(1).kind === "Equals") {
           break;
        }

        args.push(this.parseTypePrimary());
      }

      if (args.length > 0) {
        if (target.kind !== "TypeReference") {
            throw new Error(`Type application must target a TypeReference. Got ${target.kind}`);
        }
        return {
          kind: "TypeReference",
          name: target.name,
          arguments: args,
          span: {
            start: target.span.start,
            end: args[args.length - 1].span.end,
          },
        } as AST.TypeReference;
      }
    }

    return target;
  }

  private parseTypePrimary(): AST.TypeExpression {
    if (this.match("UpperIdentifier")) {
      const id = this.consume("UpperIdentifier");
      const parts = [id.lexeme];
      while (this.match("Dot")) {
        this.consume("Dot");
        parts.push(this.consume("UpperIdentifier").lexeme);
      }
      return {
        kind: "TypeReference",
        name: {
          kind: "QualifiedIdentifier",
          parts,
          span: {
            start: id.span.start,
            end: this.previous().span.end,
          },
        },
        arguments: [],
        span: {
          start: id.span.start,
          end: this.previous().span.end,
        },
      } as AST.TypeReference;
    }

    if (this.match("Identifier")) {
      const id = this.consume("Identifier");
      return {
        kind: "TypeVariable",
        name: id.lexeme,
        span: id.span,
      } as AST.TypeExpression;
    }

    if (this.match("LParen")) {
      const start = this.consume("LParen");
      if (this.match("RParen")) {
        this.consume("RParen");
        return {
          kind: "TypeReference",
          name: { kind: "QualifiedIdentifier", parts: ["Unit"], span: start.span },
          arguments: [],
          span: { start: start.span.start, end: this.previous().span.end },
        } as AST.TypeExpression;
      }
      const first = this.parseTypeExpression();
      if (this.match("Comma")) {
        const items = [first];
        while (this.match("Comma")) {
          this.consume("Comma");
          items.push(this.parseTypeExpression());
        }
        const end = this.consume("RParen");
        return {
          kind: "TypeReference",
          name: { kind: "QualifiedIdentifier", parts: ["Tuple"], span: start.span },
          arguments: items,
          span: { start: start.span.start, end: end.span.end },
        } as AST.TypeExpression;
      }
      const end = this.consume("RParen");
      return {
        ...first,
        span: {
          start: start.span.start,
          end: end.span.end,
        },
      };
    }

    if (this.match("LBrace")) {
      const start = this.consume("LBrace");
      const fields: AST.RecordTypeField[] = [];
      while (!this.match("RBrace")) {
        const fieldName = this.consume("Identifier");
        this.consume("Colon");
        const fieldType = this.parseTypeExpression();
        fields.push({
          kind: "RecordTypeField",
          name: fieldName.lexeme,
          type: fieldType,
          span: {
            start: fieldName.span.start,
            end: fieldType.span.end,
          },
        });
        if (this.match("Comma")) {
          this.consume("Comma");
        }
      }
      const end = this.consume("RBrace");
      return {
        kind: "RecordType",
        fields,
        span: {
          start: start.span.start,
          end: end.span.end,
        },
      } as AST.TypeExpression;
    }

    const t = this.peek();
    throw new Error(`Unexpected token ${t.kind}:${t.lexeme} in type expression at ${t.span.start.line}:${t.span.start.column}`);
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

  private parseExpression(minPrecedence: number, minColumn: number = 0): AST.Expression {
    let left = this.parseApplication(minColumn);

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
  private parseApplication(minColumn: number = 0): AST.Expression {
    let expr = this.parsePrimary();
    const effectiveMinColumn = Math.max(minColumn, expr.span.start.column);

    while (true) {

      const save = this.pos;

      // Stop if next token is not a valid expression start
      if (!this.isStartOfPrimaryExpression()) {
        break;
      }

      const next = this.peek();
      const prev = this.previous();
      const isNewLine = next.span.start.line > prev.span.end.line;

      // Stop if this would start a new declaration
      if (this.peek(1)?.kind === "Equals" || (isNewLine && next.span.start.column <= Math.max(1, effectiveMinColumn))) {
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
      this.match("LParen") ||
      this.match("LBrace") ||
      this.match("LBracket") ||
      this.match("Backslash") ||
      (this.match("Keyword") && (this.peek().lexeme === "case" || this.peek().lexeme === "if" || this.peek().lexeme === "let"))
    );

  }

  
  private parsePattern(): AST.Pattern {
    const primary = this.parsePrimaryPattern();

    // Check for cons operator ::
    if (this.match("Operator") && this.peek().lexeme === "::") {
      this.consume("Operator", "::");
      const tail = this.parsePattern();
      return {
        kind: "ConsPattern",
        head: primary,
        tail,
        span: {
          start: primary.span.start,
          end: tail.span.end,
        },
      } as AST.Pattern;
    }

    // Check for as keyword
    if (this.match("Keyword", "as")) {
      this.consume("Keyword", "as");
      const nameToken = this.consume("Identifier");
      return {
        kind: "AsPattern",
        pattern: primary,
        name: nameToken.lexeme,
        span: {
          start: primary.span.start,
          end: nameToken.span.end,
        },
      } as AST.Pattern;
    }

    return primary;
  }

  private parsePrimaryPattern(): AST.Pattern {
    if (this.match("UpperIdentifier")) {
      const id = this.consume("UpperIdentifier");
      const parts = [id.lexeme];
      while (this.match("Dot")) {
        this.consume("Dot");
        parts.push(this.consume("UpperIdentifier").lexeme);
      }
      const args: AST.Pattern[] = [];
      while (this.match("Identifier") || this.match("UpperIdentifier") || this.match("LParen")) {
        // Stop if we are on a new line or at the start of a branch
        if (this.peek().span.start.column <= 1 || this.peek().kind === "Arrow") {
          break;
        }
        args.push(this.parsePattern());
      }
      return {
        kind: "ConstructorPattern",
        constructorName: { kind: "QualifiedIdentifier", parts, span: id.span },
        arguments: args,
        span: {
          start: id.span.start,
          end: args.length > 0 ? args[args.length - 1].span.end : id.span.end,
        }
      } as AST.Pattern;
    } else if (this.match("LParen")) {
      const start = this.consume("LParen");
      if (this.match("RParen")) {
          const end = this.consume("RParen");
          return {
              kind: "LiteralPattern",
              value: "()",
              span: { start: start.span.start, end: end.span.end }
          } as any;
      }
      const first = this.parsePattern();
      if (this.match("Comma")) {
          const items = [first];
          while (this.match("Comma")) {
              this.consume("Comma");
              items.push(this.parsePattern());
          }
          const end = this.consume("RParen");
          return {
              kind: "TuplePattern",
              items,
              span: { start: start.span.start, end: end.span.end }
          } as any;
      } else {
          this.consume("RParen");
          return first;
      }
    } else if (this.match("Identifier")) {
      const id = this.consume("Identifier");
      if (id.lexeme === "_") {
        return {
          kind: "WildcardPattern",
          span: id.span,
        } as AST.Pattern;
      }
      return {
        kind: "VariablePattern",
        name: id.lexeme,
        span: id.span,
      } as AST.Pattern;
    } else if (this.match("Integer")) {
      const t = this.consume("Integer");
      return {
        kind: "LiteralPattern",
        value: Number(t.lexeme),
        span: t.span,
      } as AST.Pattern;
    } else if (this.match("String")) {
      const t = this.consume("String");
      return {
        kind: "LiteralPattern",
        value: t.lexeme,
        span: t.span,
      } as AST.Pattern;
    } else if (this.match("Keyword", "True") || this.match("Keyword", "False")) {
       const t = this.consume("Keyword");
       return {
         kind: "LiteralPattern",
         value: t.lexeme === "True",
         span: t.span,
       } as AST.Pattern;
    } else if (this.match("LParen")) {
       const start = this.consume("LParen");
       if (this.match("RParen")) {
         const end = this.consume("RParen");
         return {
           kind: "LiteralPattern",
           value: "()",
           span: { start: start.span.start, end: end.span.end }
         } as AST.Pattern;
       }
       const first = this.parsePattern();
       if (this.match("Comma")) {
         const items = [first];
         while (this.match("Comma")) {
           this.consume("Comma");
           items.push(this.parsePattern());
         }
         const end = this.consume("RParen");
         return {
           kind: "TuplePattern",
           items,
           span: { start: start.span.start, end: end.span.end }
         } as AST.Pattern;
       } else {
         const end = this.consume("RParen");
         return {
           ...first,
           span: { start: start.span.start, end: end.span.end }
         } as AST.Pattern;
       }
    } else if (this.match("LBracket")) {
       const start = this.consume("LBracket");
       if (this.match("RBracket")) {
         const end = this.consume("RBracket");
         return {
           kind: "ListPattern",
           items: [],
           span: { start: start.span.start, end: end.span.end }
         } as AST.Pattern;
       }
       const items: AST.Pattern[] = [this.parsePattern()];
       while (this.match("Comma")) {
         this.consume("Comma");
         items.push(this.parsePattern());
       }
       const end = this.consume("RBracket");
       return {
         kind: "ListPattern",
         items,
         span: { start: start.span.start, end: end.span.end }
       } as AST.Pattern;
    }
    const t = this.peek();
    throw new Error(`Unexpected token ${t.kind}:${t.lexeme} in pattern`);
  }

private parsePrimary(): AST.Expression {
  let expr: AST.Expression;

  if (this.match("Keyword") && this.peek().lexeme === "case") {      const start = this.consume("Keyword", "case");
      const subject = this.parseExpression(0);
      this.consume("Keyword", "of");
      
      const branches: AST.CaseBranch[] = [];
      while (true) {
        if (this.match("EOF")) break;
        if (this.peek().span.start.column === 1 && this.peek().kind !== "Pipe") break;
        
        if (this.match("Pipe")) this.consume("Pipe");
        
        const pattern = this.parsePattern();
        this.consume("Arrow");
        const body = this.parseExpression(0, pattern.span.start.column);
        branches.push({
          kind: "CaseBranch",
          pattern,
          body,
          span: {
            start: pattern.span.start,
            end: body.span.end,
          },
        });
      }

      expr = {
        kind: "CaseExpression",
        subject,
        branches,
        span: {
          start: start.span.start,
          end: branches.length > 0 ? branches[branches.length - 1].span.end : subject.span.end,
        },
      };
    } else if (this.match("Keyword") && this.peek().lexeme === "if") {
      const start = this.consume("Keyword", "if");
      const condition = this.parseExpression(0);
      this.consume("Keyword", "then");
      const thenBranch = this.parseExpression(0);
      this.consume("Keyword", "else");
      const elseBranch = this.parseExpression(0);
      expr = {
        kind: "IfExpression",
        condition,
        thenBranch,
        elseBranch,
        span: {
          start: start.span.start,
          end: elseBranch.span.end,
        },
      };
    } else if (this.match("Keyword") && this.peek().lexeme === "let") {
      const start = this.consume("Keyword", "let");
      const bindings: AST.LetBinding[] = [];
      const minColumn = start.span.start.column;
      
      while (!this.match("Keyword", "in")) {
        const next = this.peek();
        const prev = this.previous();
        const isNewLine = next.span.start.line > prev.span.end.line;
        
        // If it's a new line and indentation is not greater than 'let', it's likely a missing 'in' or error
        if (isNewLine && next.span.start.column <= minColumn) {
            break;
        }

        let typeAnnotation: AST.TypeExpression | undefined;
        let pattern: AST.Pattern;
        
        // Check for annotated binding: x : Type
        if (this.peek().kind === "Identifier" && this.peek(1).kind === "Colon") {
           const idToken = this.consume("Identifier");
           this.consume("Colon");
           typeAnnotation = this.parseTypeExpression();
           
           pattern = {
             kind: "VariablePattern",
             name: idToken.lexeme,
             span: idToken.span
           };

           // Optional second name for the actual assignment line
           if (this.match("Identifier")) {
              const nextId = this.consume("Identifier");
              if (nextId.lexeme !== idToken.lexeme) {
                 throw new Error(`Type annotation name ${idToken.lexeme} must match binding name ${nextId.lexeme}`);
              }
           }
        } else {
           pattern = this.parsePattern();
        }

        this.consume("Equals");
        const value = this.parseExpression(0, pattern.span.start.column);
        bindings.push({
          kind: "LetBinding",
          pattern,
          typeAnnotation,
          value,
          span: {
            start: pattern.span.start,
            end: value.span.end,
          },
        });
      }
      
      this.consume("Keyword", "in");
      const body = this.parseExpression(0, start.span.start.column);
      expr = {
        kind: "LetExpression",
        bindings,
        body,
        span: {
          start: start.span.start,
          end: body.span.end,
        },
      };
    } else if (this.match("Backslash")) {
      const start = this.consume("Backslash");
      const parameters: AST.Parameter[] = [];
      
      while (!this.match("Arrow")) {
        const pattern = this.parsePattern();
        parameters.push({
          kind: "Parameter",
          pattern,
          span: pattern.span,
        });
      }
      
      this.consume("Arrow");
      const body = this.parseExpression(0, start.span.start.column);
      expr = {
        kind: "LambdaExpression",
        parameters,
        body,
        span: {
          start: start.span.start,
          end: body.span.end,
        },
      };
    } else if (this.match("LBrace")) {
      const start = this.consume("LBrace");
      
      // Check for record update: { model | ... }
      if (this.peek().kind === "Identifier" && this.peek(1).kind === "Pipe") {
          const base = this.consume("Identifier");
          const baseExpr: AST.Expression = {
              kind: "IdentifierExpression",
              name: base.lexeme,
              span: base.span
          };
          this.consume("Pipe");
          const fields: AST.RecordField[] = [];
          while (!this.match("RBrace")) {
              const name = this.consume("Identifier").lexeme;
              this.consume("Equals");
              const value = this.parseExpression(0);
              fields.push({
                  kind: "RecordField",
                  name,
                  value,
                  span: { start: value.span.start, end: value.span.end }
              });
              if (this.match("Comma")) this.consume("Comma");
          }
          const end = this.consume("RBrace");
          expr = {
              kind: "RecordUpdateExpression",
              base: baseExpr,
              fields,
              span: { start: start.span.start, end: end.span.end }
          } as any; // Cast until AST is updated
      } else {
          const fields: AST.RecordField[] = [];

          while (!this.match("RBrace")) {
            const name = this.consume("Identifier").lexeme;
            this.consume("Equals");
            const value = this.parseExpression(0);
            fields.push({
              kind: "RecordField",
              name,
              value,
              span: {
                start: value.span.start,
                end: value.span.end,
              },
            });
            if (this.match("Comma")) {
              this.consume("Comma");
            }
          }
          
          const end = this.consume("RBrace");
          expr = {
            kind: "RecordExpression",
            fields,
            span: {
              start: start.span.start,
              end: end.span.end,
            },
          };
      }
    } else if (this.match("LBracket")) {
      const start = this.consume("LBracket");
      const items: AST.Expression[] = [];

      while (!this.match("RBracket")) {
        items.push(this.parseExpression(0));
        if (this.match("Comma")) {
          this.consume("Comma");
        }
      }

      const end = this.consume("RBracket");
      expr = {
        kind: "ListExpression",
        items,
        span: {
          start: start.span.start,
          end: end.span.end,
        },
      };
    } else if (this.match("Identifier")) {
      const t = this.consume("Identifier");
      expr = {
        kind: "IdentifierExpression",
        name: t.lexeme,
        span: t.span,
      };
    } else if (this.match("UpperIdentifier")) {
      const id = this.consume("UpperIdentifier");
      const parts = [id.lexeme];
      while (this.match("Dot")) {
        this.consume("Dot");
        const next = this.match("UpperIdentifier") ? this.consume("UpperIdentifier") : this.consume("Identifier");
        parts.push(next.lexeme);
      }
      if (parts.length > 1) {
        expr = {
          kind: "QualifiedIdentifierExpression",
          name: { parts },
          span: { start: id.span.start, end: this.previous().span.end },
        };
      } else {
        expr = {
          kind: "IdentifierExpression",
          name: id.lexeme,
          span: id.span,
        };
      }
    } else if (this.match("Integer")) {
      const t = this.consume("Integer");
      expr = {
        kind: "IntegerLiteralExpression",
        value: Number(t.lexeme),
        raw: t.lexeme,
        span: t.span,
      };
    } else if (this.match("String")) {
      const t = this.consume("String");
      expr = {
        kind: "StringLiteralExpression",
        value: t.lexeme,
        span: t.span,
      };
      } else if (this.match("UpperIdentifier") && (this.peek().lexeme === "True" || this.peek().lexeme === "False")) {
      const t = this.consume("UpperIdentifier");
      expr = {
        kind: "BooleanLiteralExpression",
        value: t.lexeme === "True",
        span: t.span,
      };
    } else if (this.match("Identifier")) {
      const t = this.consume("Identifier");
      expr = {
        kind: "IdentifierExpression",
        name: t.lexeme,
        span: t.span,
      };
    } else if (this.match("LParen")) {
      const start = this.consume("LParen");
      if (this.match("RParen")) {
        const end = this.consume("RParen");
        expr = {
          kind: "UnitExpression",
          span: { start: start.span.start, end: end.span.end },
        };
      } else {
        if (this.match("Operator")) {
          const op = this.consume("Operator");
          this.consume("RParen");
          const right = this.parsePrimary();
          expr = buildLeftSection(op.lexeme, right, start.span);
        } else {
          const first = this.parseExpression(0);
          if (this.match("Operator")) {
            const op = this.consume("Operator");
            this.consume("RParen");
            expr = buildRightSection(first, op.lexeme, start.span);
          } else if (this.match("Comma")) {
            const items = [first];
            while (this.match("Comma")) {
              this.consume("Comma");
              items.push(this.parseExpression(0));
            }
            const end = this.consume("RParen");
            expr = {
              kind: "TupleExpression",
              items,
              span: { start: start.span.start, end: end.span.end },
            };
          } else {
            const end = this.consume("RParen");
            expr = {
              kind: "ParenthesizedExpression",
              expression: first,
              span: { start: start.span.start, end: end.span.end },
            };
          }
        }
      }
    } else {
      const t = this.peek();
      throw new Error(`Unexpected token ${t.kind}:${t.lexeme} at ${t.span.start.line}:${t.span.start.column}`);
    }

    // Parse field accesses (e.g. record.field)
    while (this.match("Dot") && this.peek(1).kind === "Identifier") {
      this.consume("Dot");
      const field = this.consume("Identifier");
      expr = {
        kind: "FieldAccessExpression",
        target: expr,
        fieldName: field.lexeme,
        span: {
          start: expr.span.start,
          end: field.span.end,
        }
      };
    }

    return expr;
  }
}
export function parse(tokens: Token[]): AST.Module {
  const parser = new Parser(tokens);
  return parser.parseModule();
}
