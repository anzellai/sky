// src/live/detect.ts
// Detects whether a Sky project is a Live app by checking if the main
// module imports Std.Live and calls `app`.

import * as AST from "../ast/ast.js";

export interface LiveDetection {
  isLive: boolean;
  // If live, the Msg type declaration from the main module
  msgType?: AST.TypeDeclaration;
  // Route definitions found in the app call
  routes?: any[];
}

/**
 * Detect if a module is a Sky.Live app.
 * Checks for: import Std.Live and a call to `app` in the main declaration.
 */
export function detectLiveApp(moduleAst: AST.Module): LiveDetection {
  // Check if Std.Live is imported
  const hasLiveImport = moduleAst.imports.some(
    (imp) => imp.moduleName.join(".") === "Std.Live"
  );

  if (!hasLiveImport) {
    return { isLive: false };
  }

  // Find the Msg type declaration
  let msgType: AST.TypeDeclaration | undefined;
  for (const decl of moduleAst.declarations) {
    if (decl.kind === "TypeDeclaration" && decl.name === "Msg") {
      msgType = decl;
      break;
    }
  }

  // Find Page type declaration
  let pageType: AST.TypeDeclaration | undefined;
  for (const decl of moduleAst.declarations) {
    if (decl.kind === "TypeDeclaration" && decl.name === "Page") {
      pageType = decl;
      break;
    }
  }

  // Check if main calls `app`
  const mainDecl = moduleAst.declarations.find(
    (d) => d.kind === "FunctionDeclaration" && d.name === "main"
  );

  if (!mainDecl || mainDecl.kind !== "FunctionDeclaration") {
    return { isLive: false };
  }

  // Look for `app { ... }` call in main body
  const hasAppCall = containsAppCall(mainDecl.body);

  return {
    isLive: hasAppCall,
    msgType,
  };
}

function containsAppCall(expr: AST.Expression): boolean {
  switch (expr.kind) {
    case "CallExpression":
      if (
        expr.callee.kind === "IdentifierExpression" &&
        expr.callee.name === "app"
      ) {
        return true;
      }
      if (
        expr.callee.kind === "QualifiedIdentifierExpression" &&
        expr.callee.name.parts.join(".") === "Std.Live.app"
      ) {
        return true;
      }
      // Also check callee and arguments
      if (containsAppCall(expr.callee)) return true;
      for (const arg of expr.arguments) {
        if (containsAppCall(arg)) return true;
      }
      return false;
    case "LetExpression":
      for (const binding of expr.bindings) {
        if (containsAppCall(binding.value)) return true;
      }
      return containsAppCall(expr.body);
    case "ParenthesizedExpression":
      return containsAppCall(expr.expression);
    default:
      return false;
  }
}
