// src/parser/sections.ts
// Elm-style operator sections
//
// (+) 1  ->  \x -> x + 1
// (1 +)  ->  \x -> 1 + x

import * as AST from "../ast.js";

const SECTION_ARG = "__section_arg";

/**
 * (+) 1  =>  \x -> x + 1
 */
export function buildLeftSection(
  operator: string,
  right: AST.Expression,
  span: AST.NodeBase["span"]
): AST.Expression {

  const param: AST.Parameter = {
    kind: "Parameter",
    pattern: {
      kind: "VariablePattern",
      name: SECTION_ARG,
      span
    },
    span
  };

  const leftExpr: AST.Expression = {
    kind: "IdentifierExpression",
    name: SECTION_ARG,
    span
  };

  const body: AST.Expression = {
    kind: "BinaryExpression",
    operator,
    left: leftExpr,
    right,
    span
  };

  return {
    kind: "LambdaExpression",
    parameters: [param],
    body,
    span
  };
}

/**
 * (1 +)  =>  \x -> 1 + x
 */
export function buildRightSection(
  left: AST.Expression,
  operator: string,
  span: AST.NodeBase["span"]
): AST.Expression {

  const param: AST.Parameter = {
    kind: "Parameter",
    pattern: {
      kind: "VariablePattern",
      name: SECTION_ARG,
      span
    },
    span
  };

  const rightExpr: AST.Expression = {
    kind: "IdentifierExpression",
    name: SECTION_ARG,
    span
  };

  const body: AST.Expression = {
    kind: "BinaryExpression",
    operator,
    left,
    right: rightExpr,
    span
  };

  return {
    kind: "LambdaExpression",
    parameters: [param],
    body,
    span
  };
}
