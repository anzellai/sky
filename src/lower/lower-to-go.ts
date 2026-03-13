// src/lower/lower-to-go.ts
import * as CoreIR from "../core-ir/core-ir.js";
import * as GoIR from "../go-ir/go-ir.js";
import type { Type } from "../types/types.js";

export function lowerModule(module: CoreIR.Module): GoIR.GoPackage {
  const pkg: GoIR.GoPackage = {
    name: module.name[module.name.length - 1].toLowerCase(),
    imports: [],
    declarations: []
  };

  // Add net/http import if listenAndServe is present (hack for demo)
  if (module.declarations.some(d => JSON.stringify(d).includes("Http.get"))) {
    pkg.imports.push({ path: "net/http" });
  }
  if (module.declarations.some(d => JSON.stringify(d).includes("println"))) {
    pkg.imports.push({ path: "fmt" });
  }

  // Convert types
  for (const tDecl of module.typeDeclarations) {
    // Basic conversion of ADTs to struct with Tag
    // type Maybe a = Nothing | Just a
    // type Maybe[T any] struct { Tag int; JustValue T }
    
    const fields: { name: string; type: GoIR.GoType }[] = [
      { name: "Tag", type: { kind: "GoIdentType", name: "int" } }
    ];

    for (const ctor of tDecl.constructors) {
      if (ctor.types.length > 0) {
        // Simplified: Just take the first type for now as the value
        // In reality, we'd need to handle multiple constructor arguments
        fields.push({
          name: `${ctor.name}Value`,
          type: lowerType(ctor.types[0])
        });
      }
    }

    pkg.declarations.push({
      kind: "GoStructDecl",
      name: tDecl.name,
      typeParams: tDecl.typeParams,
      fields
    });
  }

  // Convert functions
  for (const decl of module.declarations) {
    if (decl.body.kind === "Lambda" || decl.name === "main") {
      let params: {name: string, type: GoIR.GoType}[] = [];
      let bodyExpr = decl.body;
      
      if (decl.body.kind === "Lambda") {
        const lambda = decl.body as CoreIR.Lambda;
        params = lambda.params.map(p => ({
          name: p,
          type: { kind: "GoIdentType", name: "any" }
        }));
        bodyExpr = lambda.body;
      }
      
      
      const stmts: GoIR.GoStmt[] = [];
      
      function flattenLet(expr: CoreIR.Expr) {
        if (expr.kind === "LetBinding") {
          stmts.push({
            kind: "GoAssignStmt",
            define: true,
            left: [{ kind: "GoIdent", name: expr.name }, { kind: "GoIdent", name: "_" }], // Add _ to ignore error for the demo
            right: lowerExpr(expr.value)
          });
          flattenLet(expr.body);
        } else {
          const loweredBodyExpr = lowerExpr(expr);
          if (decl.name === "main") {
            stmts.push({ kind: "GoExprStmt", expr: loweredBodyExpr });
          } else {
            stmts.push({ kind: "GoReturnStmt", expr: loweredBodyExpr });
          }
        }
      }

      flattenLet(bodyExpr);

      let retType: GoIR.GoType | undefined = undefined;
      if (decl.name !== "main") {
        retType = { kind: "GoIdentType", name: "any" };
      }

      pkg.declarations.push({
        kind: "GoFuncDecl",
        name: decl.name,
        typeParams: [],
        params: params,
        returnType: retType,
        body: stmts
      });
    } else {
      pkg.declarations.push({
        kind: "GoVarDecl",
        name: decl.name,
        type: lowerType(decl.scheme.type),
        value: lowerExpr(decl.body)
      });
    }
  }

  return pkg;
}

function lowerType(t: Type): GoIR.GoType {
  // Simplified type lowering
  if (t.kind === "TypeConstant") {
    if (t.name === "Int") return { kind: "GoIdentType", name: "int" };
    if (t.name === "Float") return { kind: "GoIdentType", name: "float64" };
    if (t.name === "Bool") return { kind: "GoIdentType", name: "bool" };
    if (t.name === "String") return { kind: "GoIdentType", name: "string" };
    if (t.name === "Unit") return { kind: "GoStructType", fields: [] };
    
    return {
      kind: "GoIdentType",
      name: t.name,
      typeArgs: (t as any).args?.map(lowerType)
    };
  }
  if (t.kind === "TypeVariable") {
    return { kind: "GoIdentType", name: "any" }; // type var
  }
  if (t.kind === "TypeFunction") {
    return {
      kind: "GoFuncType",
      params: [lowerType((t as any).domain)],
      results: [lowerType((t as any).codomain)]
    };
  }
  return { kind: "GoIdentType", name: "any" };
}

function lowerExpr(expr: CoreIR.Expr): GoIR.GoExpr {
  switch (expr.kind) {
    case "Literal": {
      if (expr.literalType === "String") {
        return { kind: "GoBasicLit", value: `"${expr.value}"` };
      }
      return { kind: "GoBasicLit", value: String(expr.value) };
    }
    case "Variable": {
      return { kind: "GoIdent", name: expr.name };
    }
    case "Application": {
      // Uncurry the application if it's a chain of calls
      const flattenApp = (app: CoreIR.Application): { fn: CoreIR.Expr, args: CoreIR.Expr[] } => {
        if (app.fn.kind === "Application") {
          const inner = flattenApp(app.fn);
          return { fn: inner.fn, args: [...inner.args, ...app.args] };
        }
        return { fn: app.fn, args: app.args };
      };

      const flat = flattenApp(expr);
      
      // Map listenAndServe to http.ListenAndServe and println to fmt.Println
      let fnExpr = lowerExpr(flat.fn);
      let args = flat.args.map(lowerExpr);

      if (fnExpr.kind === "GoIdent" && fnExpr.name === "listenAndServe") {
        fnExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "http" }, sel: "ListenAndServe" };
        if (args.length > 1 && args[1].kind === "GoBasicLit" && args[1].value === '"nil"') {
          args[1] = { kind: "GoIdent", name: "nil" };
        }
      } else if (fnExpr.kind === "GoIdent" && fnExpr.name === "Http.get") {
        fnExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "http" }, sel: "Get" };
      } else if (fnExpr.kind === "GoIdent" && fnExpr.name === "println") {
        fnExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "fmt" }, sel: "Println" };
      }
      
      return {
        kind: "GoCallExpr",
        fn: fnExpr,
        args: args
      };
    }
    case "LetBinding": {
      // In Go, this is an assignment statement if we are inside a function block
      // But GoExpr can only be expressions. We don't have block expressions in GoIR yet.
      // For now, we will cheat and represent it as an Immediately Invoked Function Expression (IIFE)
      return {
        kind: "GoCallExpr",
        fn: {
          kind: "GoFuncType",
          params: [],
          results: [{ kind: "GoIdentType", name: "any" }]
        } as any, // Not valid GoIR for an IIFE yet, let's fix below
        args: []
      };
    }
    // ... we need to expand this as we build out the full compiler ...
    default:
      return { kind: "GoBasicLit", value: "/* unimplemented */" };
  }
}