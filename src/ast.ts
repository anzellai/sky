// src/ast.ts
// Sky compiler AST and shared syntax model
//
// Design goals:
// - Preserve source spans on every significant node.
// - Keep the CST out of the main compiler pipeline.
// - Model syntax in a way that is friendly to:
//   - parsing
//   - name resolution
//   - type checking
//   - code generation
//   - diagnostics
//
// This file intentionally focuses on the surface AST.
// Later phases can layer symbol IDs / type IDs on top rather than mutate it.

import type { SourceSpan } from "./lexer.js";
export type { SourceSpan };

export interface SourcePosition {
  readonly line: number
  readonly column: number
}

export interface NodeBase {
  readonly kind: SyntaxKind;
  readonly span: SourceSpan;
}

export type SyntaxKind =
  | "Module"
  | "ExposingClause"
  | "ImportDeclaration"
  | "ImportAlias"
  | "ForeignImportDeclaration"
  | "TypeAliasDeclaration"
  | "TypeDeclaration"
  | "TypeVariant"
  | "FunctionDeclaration"
  | "TypeAnnotation"
  | "QualifiedIdentifier"
  | "TypeReference"
  | "TypeVariable"
  | "FunctionType"
  | "RecordType"
  | "RecordTypeField"
  | "Parameter"
  | "Pattern"
  | "WildcardPattern"
  | "VariablePattern"
  | "ConstructorPattern"
  | "LiteralPattern"
  | "TuplePattern"
  | "ListPattern"
  | "Expression"
  | "IdentifierExpression"
  | "QualifiedIdentifierExpression"
  | "IntegerLiteralExpression"
  | "FloatLiteralExpression"
  | "StringLiteralExpression"
  | "CharLiteralExpression"
  | "BooleanLiteralExpression"
  | "UnitExpression"
  | "TupleExpression"
  | "ListExpression"
  | "RecordExpression"
  | "RecordField"
  | "FieldAccessExpression"
  | "CallExpression"
  | "LambdaExpression"
  | "IfExpression"
  | "LetExpression"
  | "LetBinding"
  | "CaseExpression"
  | "CaseBranch"
  | "BinaryExpression"
  | "ParenthesizedExpression";

export interface Module extends NodeBase {
  readonly kind: "Module";
  readonly name: ModuleName;
  readonly exposing?: ExposingClause;
  readonly imports: ImportDeclaration[];
  readonly declarations: Declaration[];
}

export type Declaration =
  | FunctionDeclaration
  | TypeAliasDeclaration
  | TypeDeclaration
  | ForeignImportDeclaration;

export type ModuleName = readonly string[];

export interface ExposingClause extends NodeBase {
  readonly kind: "ExposingClause";
  readonly items: ExposedItem[];
  readonly open: boolean;
}

export type ExposedItem =
  | {
    readonly kind: "value";
    readonly name: string;
    readonly span: SourceSpan;
  }
  | {
    readonly kind: "type";
    readonly name: string;
    readonly exposeConstructors: boolean;
    readonly span: SourceSpan;
  };

export interface ImportDeclaration extends NodeBase {
  readonly kind: "ImportDeclaration";
  readonly moduleName: ModuleName;
  readonly alias?: ImportAlias;
  readonly exposing?: ExposingClause;
}

export interface ImportAlias extends NodeBase {
  readonly kind: "ImportAlias";
  readonly name: string;
}

export interface ForeignImportDeclaration extends NodeBase {
  readonly kind: "ForeignImportDeclaration";
  readonly name: string;
  readonly typeAnnotation: TypeAnnotation;
  readonly sourceModule: string;
  readonly importName?: string;
  readonly isDefault: boolean;
}

export interface TypeAliasDeclaration extends NodeBase {
  readonly kind: "TypeAliasDeclaration";
  readonly name: string;
  readonly typeParameters: readonly string[];
  readonly aliasedType: TypeExpression;
}

export interface TypeDeclaration extends NodeBase {
  readonly kind: "TypeDeclaration";
  readonly name: string;
  readonly typeParameters: readonly string[];
  readonly variants: readonly TypeVariant[];
}

export interface TypeVariant extends NodeBase {
  readonly kind: "TypeVariant";
  readonly name: string;
  readonly fields: readonly TypeExpression[];
}

export interface FunctionDeclaration extends NodeBase {
  readonly kind: "FunctionDeclaration";
  readonly name: string;
  readonly typeAnnotation?: TypeAnnotation;
  readonly parameters: readonly Parameter[];
  readonly body: Expression;
}

export interface TypeAnnotation extends NodeBase {
  readonly kind: "TypeAnnotation";
  readonly name: string;
  readonly type: TypeExpression;
}

export type TypeExpression =
  | TypeReference
  | TypeVariable
  | FunctionType
  | RecordType;

export interface QualifiedIdentifier extends NodeBase {
  readonly kind: "QualifiedIdentifier";
  readonly parts: readonly string[];
}

export interface TypeReference extends NodeBase {
  readonly kind: "TypeReference";
  readonly name: QualifiedIdentifier;
  readonly arguments: readonly TypeExpression[];
}

export interface TypeVariable extends NodeBase {
  readonly kind: "TypeVariable";
  readonly name: string;
}

export interface FunctionType extends NodeBase {
  readonly kind: "FunctionType";
  readonly from: TypeExpression;
  readonly to: TypeExpression;
}

export interface RecordType extends NodeBase {
  readonly kind: "RecordType";
  readonly fields: readonly RecordTypeField[];
}

export interface RecordTypeField extends NodeBase {
  readonly kind: "RecordTypeField";
  readonly name: string;
  readonly type: TypeExpression;
}

export interface Parameter extends NodeBase {
  readonly kind: "Parameter";
  readonly pattern: Pattern;
}

export type Pattern =
  | WildcardPattern
  | VariablePattern
  | ConstructorPattern
  | LiteralPattern
  | TuplePattern
  | ListPattern;

export interface WildcardPattern extends NodeBase {
  readonly kind: "WildcardPattern";
}

export interface VariablePattern extends NodeBase {
  readonly kind: "VariablePattern";
  readonly name: string;
}

export interface ConstructorPattern extends NodeBase {
  readonly kind: "ConstructorPattern";
  readonly constructorName: QualifiedIdentifier;
  readonly arguments: readonly Pattern[];
}

export type LiteralValue = number | string | boolean;

export interface LiteralPattern extends NodeBase {
  readonly kind: "LiteralPattern";
  readonly value: LiteralValue;
}

export interface TuplePattern extends NodeBase {
  readonly kind: "TuplePattern";
  readonly items: readonly Pattern[];
}

export interface ListPattern extends NodeBase {
  readonly kind: "ListPattern";
  readonly items: readonly Pattern[];
}

export type Expression =
  | IdentifierExpression
  | QualifiedIdentifierExpression
  | IntegerLiteralExpression
  | FloatLiteralExpression
  | StringLiteralExpression
  | CharLiteralExpression
  | BooleanLiteralExpression
  | UnitExpression
  | TupleExpression
  | ListExpression
  | RecordExpression
  | FieldAccessExpression
  | CallExpression
  | LambdaExpression
  | IfExpression
  | LetExpression
  | CaseExpression
  | BinaryExpression
  | ParenthesizedExpression;

export interface IdentifierExpression {
  readonly kind: "IdentifierExpression"
  readonly name: string
  readonly span: SourceSpan
}

export interface QualifiedIdentifierExpression {
  readonly kind: "QualifiedIdentifierExpression"
  readonly name: {
    readonly parts: readonly string[]
  }
  readonly span: SourceSpan
}

export interface IntegerLiteralExpression extends NodeBase {
  readonly kind: "IntegerLiteralExpression";
  readonly value: number;
  readonly raw: string;
}

export interface FloatLiteralExpression extends NodeBase {
  readonly kind: "FloatLiteralExpression";
  readonly value: number;
  readonly raw: string;
}

export interface StringLiteralExpression extends NodeBase {
  readonly kind: "StringLiteralExpression";
  readonly value: string;
}

export interface CharLiteralExpression extends NodeBase {
  readonly kind: "CharLiteralExpression";
  readonly value: string;
}

export interface BooleanLiteralExpression extends NodeBase {
  readonly kind: "BooleanLiteralExpression";
  readonly value: boolean;
}

export interface UnitExpression extends NodeBase {
  readonly kind: "UnitExpression";
}

export interface TupleExpression extends NodeBase {
  readonly kind: "TupleExpression";
  readonly items: readonly Expression[];
}

export interface ListExpression extends NodeBase {
  readonly kind: "ListExpression";
  readonly items: readonly Expression[];
}

export interface RecordExpression extends NodeBase {
  readonly kind: "RecordExpression";
  readonly fields: readonly RecordField[];
}

export interface RecordField extends NodeBase {
  readonly kind: "RecordField";
  readonly name: string;
  readonly value: Expression;
}

export interface FieldAccessExpression extends NodeBase {
  readonly kind: "FieldAccessExpression";
  readonly target: Expression;
  readonly fieldName: string;
}

export interface CallExpression extends NodeBase {
  readonly kind: "CallExpression";
  readonly callee: Expression;
  readonly arguments: readonly Expression[];
}

export interface LambdaExpression extends NodeBase {
  readonly kind: "LambdaExpression";
  readonly parameters: readonly Parameter[];
  readonly body: Expression;
}

export interface IfExpression extends NodeBase {
  readonly kind: "IfExpression";
  readonly condition: Expression;
  readonly thenBranch: Expression;
  readonly elseBranch: Expression;
}

export interface LetExpression extends NodeBase {
  readonly kind: "LetExpression";
  readonly bindings: readonly LetBinding[];
  readonly body: Expression;
}

export interface LetBinding extends NodeBase {
  readonly kind: "LetBinding";
  readonly pattern: Pattern;
  readonly typeAnnotation?: TypeExpression;
  readonly value: Expression;
}

export interface CaseExpression extends NodeBase {
  readonly kind: "CaseExpression";
  readonly subject: Expression;
  readonly branches: readonly CaseBranch[];
}

export interface CaseBranch extends NodeBase {
  readonly kind: "CaseBranch";
  readonly pattern: Pattern;
  readonly body: Expression;
}

export interface BinaryExpression extends NodeBase {
  readonly kind: "BinaryExpression";
  readonly operator: string;
  readonly left: Expression;
  readonly right: Expression;
}

export interface ParenthesizedExpression extends NodeBase {
  readonly kind: "ParenthesizedExpression";
  readonly expression: Expression;
}

export function makeQualifiedIdentifier(parts: readonly string[], span: SourceSpan): QualifiedIdentifier {
  return {
    kind: "QualifiedIdentifier",
    parts: [...parts],
    span,
  };
}

export function isValueDeclaration(node: Declaration): node is FunctionDeclaration | ForeignImportDeclaration {
  return node.kind === "FunctionDeclaration" || node.kind === "ForeignImportDeclaration";
}

export function isTypeDeclaration(node: Declaration): node is TypeAliasDeclaration | TypeDeclaration {
  return node.kind === "TypeAliasDeclaration" || node.kind === "TypeDeclaration";
}

export function getDeclarationName(node: Declaration): string {
  switch (node.kind) {
    case "FunctionDeclaration":
    case "ForeignImportDeclaration":
    case "TypeAliasDeclaration":
    case "TypeDeclaration":
      return node.name;
  }
}

export function getQualifiedIdentifierText(name: QualifiedIdentifier): string {
  return name.parts.join(".");
}

export function isLiteralExpression(
  node: Expression,
): node is
  | IntegerLiteralExpression
  | FloatLiteralExpression
  | StringLiteralExpression
  | CharLiteralExpression
  | BooleanLiteralExpression {
  return (
    node.kind === "IntegerLiteralExpression"
    || node.kind === "FloatLiteralExpression"
    || node.kind === "StringLiteralExpression"
    || node.kind === "CharLiteralExpression"
    || node.kind === "BooleanLiteralExpression"
  );
}
