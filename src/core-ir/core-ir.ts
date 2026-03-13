// src/core-ir/core-ir.ts
import type { Scheme, Type } from "../types/types.js";
import * as AST from "../ast/ast.js";

export type Expr =
  | Variable
  | Literal
  | Lambda
  | Application
  | LetBinding
  | IfExpr
  | Constructor
  | Match
  | RecordExpr
  | ListExpr
  | ModuleRef;

export interface BaseExpr {
  type: Type;
}

export interface Variable extends BaseExpr {
  kind: "Variable";
  name: string;
}

export interface Literal extends BaseExpr {
  kind: "Literal";
  value: string | number | boolean;
  literalType: "Int" | "Float" | "String" | "Bool" | "Unit";
}

export interface Lambda extends BaseExpr {
  kind: "Lambda";
  params: string[];
  body: Expr;
}

export interface Application extends BaseExpr {
  kind: "Application";
  fn: Expr;
  args: Expr[];
}

export interface LetBinding extends BaseExpr {
  kind: "LetBinding";
  name: string;
  value: Expr;
  body: Expr;
}

export interface IfExpr extends BaseExpr {
  kind: "IfExpr";
  condition: Expr;
  thenBranch: Expr;
  elseBranch: Expr;
}

export interface Constructor extends BaseExpr {
  kind: "Constructor";
  name: string;
  args: Expr[];
}

export interface Match extends BaseExpr {
  kind: "Match";
  expr: Expr;
  cases: MatchCase[];
}

export interface MatchCase {
  pattern: Pattern;
  body: Expr;
}

export type Pattern = 
  | ConstructorPattern
  | VariablePattern
  | LiteralPattern
  | WildcardPattern;

export interface ConstructorPattern {
  kind: "ConstructorPattern";
  name: string;
  args: Pattern[];
}

export interface VariablePattern {
  kind: "VariablePattern";
  name: string;
}

export interface LiteralPattern {
  kind: "LiteralPattern";
  value: string | number | boolean;
}

export interface WildcardPattern {
  kind: "WildcardPattern";
}

export interface RecordExpr extends BaseExpr {
  kind: "RecordExpr";
  fields: Record<string, Expr>;
}

export interface ListExpr extends BaseExpr {
  kind: "ListExpr";
  items: Expr[];
}

export interface ModuleRef extends BaseExpr {
  kind: "ModuleRef";
  module: string[];
  name: string;
}

export interface Declaration {
  name: string;
  scheme: Scheme;
  body: Expr;
}

export interface TypeDeclaration {
  name: string;
  typeParams: string[];
  constructors: { name: string; types: Type[] }[];
}

export interface Module {
  name: string[];
  declarations: Declaration[];
  typeDeclarations: TypeDeclaration[];
}