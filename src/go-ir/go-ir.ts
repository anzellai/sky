// src/go-ir/go-ir.ts

export interface GoPackage {
  name: string;
  imports: GoImport[];
  declarations: GoDeclaration[];
}

export interface GoImport {
  path: string;
  alias?: string;
}

export type GoDeclaration = 
  | GoStructDecl
  | GoTypeDecl
  | GoFuncDecl
  | GoVarDecl;

export interface GoStructDecl {
  kind: "GoStructDecl";
  name: string;
  typeParams: string[];
  fields: { name: string; type: GoType; tag?: string }[];
}

export interface GoTypeDecl {
  kind: "GoTypeDecl";
  name: string;
  typeParams: string[];
  underlyingType: GoType;
}

export interface GoFuncDecl {
  kind: "GoFuncDecl";
  name: string;
  typeParams: string[];
  params: { name: string; type: GoType }[];
  returnType?: GoType;
  body: GoStmt[];
}

export interface GoVarDecl {
  kind: "GoVarDecl";
  name: string;
  type: GoType;
  value?: GoExpr;
}

export type GoStmt =
  | GoIfStmt
  | GoSwitchStmt
  | GoAssignStmt
  | GoReturnStmt
  | GoExprStmt
  | GoVarDeclStmt;

export interface GoIfStmt {
  kind: "GoIfStmt";
  condition: GoExpr;
  thenBranch: GoStmt[];
  elseBranch: GoStmt[];
}

export interface GoSwitchStmt {
  kind: "GoSwitchStmt";
  expr: GoExpr;
  cases: GoCaseClause[];
}

export interface GoCaseClause {
  kind: "GoCaseClause";
  exprs: GoExpr[]; // Empty for default
  body: GoStmt[];
}

export interface GoAssignStmt {
  kind: "GoAssignStmt";
  left: GoExpr[];
  right: GoExpr;
  define: boolean; // true for :=, false for =
}

export interface GoReturnStmt {
  kind: "GoReturnStmt";
  expr?: GoExpr;
}

export interface GoExprStmt {
  kind: "GoExprStmt";
  expr: GoExpr;
}

export interface GoVarDeclStmt {
  kind: "GoVarDeclStmt";
  name: string;
  type?: GoType;
  value: GoExpr;
}

export type GoExpr =
  | GoIdent
  | GoBasicLit
  | GoCallExpr
  | GoSelectorExpr
  | GoSliceLit
  | GoMapLit
  | GoCompositeLit
  | GoUnaryExpr
  | GoBinaryExpr
  | GoFuncLit;

export interface GoFuncLit {
  kind: "GoFuncLit";
  type: GoFuncType;
  body: GoStmt[];
}

export interface GoIdent {
  kind: "GoIdent";
  name: string;
}

export interface GoBasicLit {
  kind: "GoBasicLit";
  value: string; // The literal text, e.g., "1", "true", `"hello"`
}

export interface GoCallExpr {
  kind: "GoCallExpr";
  fn: GoExpr;
  args: GoExpr[];
}

export interface GoSelectorExpr {
  kind: "GoSelectorExpr";
  expr: GoExpr;
  sel: string;
}

export interface GoSliceLit {
  kind: "GoSliceLit";
  type: GoType;
  elements: GoExpr[];
}

export interface GoMapLit {
  kind: "GoMapLit";
  type: GoType;
  entries: { key: GoExpr; value: GoExpr }[];
}

export interface GoCompositeLit {
  kind: "GoCompositeLit";
  type: GoType;
  elements: GoExpr[]; // Can be key-value pairs or positional
}

export interface GoUnaryExpr {
  kind: "GoUnaryExpr";
  op: string; // e.g., "&", "*"
  expr: GoExpr;
}

export interface GoBinaryExpr {
  kind: "GoBinaryExpr";
  left: GoExpr;
  op: string; // e.g., "+", "==", "&&"
  right: GoExpr;
}

export type GoType =
  | GoIdentType
  | GoSelectorType
  | GoPointerType
  | GoSliceType
  | GoMapType
  | GoFuncType
  | GoStructType
  | GoInterfaceType;

export interface GoIdentType {
  kind: "GoIdentType";
  name: string;
  typeArgs?: GoType[];
}

export interface GoSelectorType {
  kind: "GoSelectorType";
  pkg: string;
  name: string;
}

export interface GoPointerType {
  kind: "GoPointerType";
  elem: GoType;
}

export interface GoSliceType {
  kind: "GoSliceType";
  elem: GoType;
}

export interface GoMapType {
  kind: "GoMapType";
  key: GoType;
  value: GoType;
}

export interface GoFuncType {
  kind: "GoFuncType";
  params: GoType[];
  results: GoType[];
}

export interface GoStructType {
  kind: "GoStructType";
  fields: { name: string; type: GoType; tag?: string }[];
}

export interface GoInterfaceType {
  kind: "GoInterfaceType";
  methods: string[]; // Simplification
}