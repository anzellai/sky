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

  // Convert types
  for (const tDecl of module.typeDeclarations) {
    const fields: { name: string; type: GoIR.GoType }[] = [
      { name: "Tag", type: { kind: "GoIdentType", name: "int" } }
    ];

    for (const ctor of tDecl.constructors) {
      if (ctor.types.length > 0) {
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
            left: [{ kind: "GoIdent", name: expr.name }],
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

  const foreignModules = new Set<string>();
  const scanGoNode = (node: any) => {
      if (!node) return;
      if (typeof node !== "object") return;
      if (node.kind === "GoSelectorExpr" && node.expr && node.expr.kind === "GoIdent") {
         foreignModules.add(node.expr.name);
      }
      for (const k of Object.keys(node)) {
          scanGoNode(node[k]);
      }
  };

  for (const decl of pkg.declarations) {
      scanGoNode(decl);
  }

  if (foreignModules.has("sky_wrappers")) {
      pkg.imports.push({ path: "sky-out/sky_wrappers", alias: "sky_wrappers" });
  }
  if (foreignModules.has("http")) {
      pkg.imports.push({ path: "net/http" });
  }
  if (foreignModules.has("fmt")) {
      pkg.imports.push({ path: "fmt" });
  }

  return pkg;
}

function lowerType(t: Type): GoIR.GoType {
  if (!t || !t.kind) {
      return { kind: "GoIdentType", name: "any" };
  }
  // Simplified type lowering
  if (t.kind === "TypeConstant") {
    if (t.name === "Int") return { kind: "GoIdentType", name: "int" };
    if (t.name === "Float") return { kind: "GoIdentType", name: "float64" };
    if (t.name === "Bool") return { kind: "GoIdentType", name: "bool" };
    if (t.name === "String") return { kind: "GoIdentType", name: "string" };
    if (t.name === "Unit") return { kind: "GoStructType", fields: [] };
    if (t.name === "Any") return { kind: "GoIdentType", name: "any" };
    
    return {
      kind: "GoIdentType",
      name: t.name
    };
  }
  if (t.kind === "TypeApplication") {
    const base = lowerType(t.constructor);
    if (base.kind === "GoIdentType") {
       return {
         ...base,
         typeArgs: t.arguments?.map(lowerType)
       };
    }
    return base;
  }
  if (t.kind === "TypeVariable") {
    return { kind: "GoIdentType", name: "any" }; // type var
  }
  if (t.kind === "TypeFunction") {
    return {
      kind: "GoFuncType",
      params: [lowerType(t.from)],
      results: [lowerType(t.to)]
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
      if (expr.name.startsWith("Http.")) {
         const selName = "Sky_net_http_" + expr.name.substring(5);
         return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: selName };
      }
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
      } else if (fnExpr.kind === "GoIdent" && fnExpr.name.startsWith("Http.")) {
        const selName = "Sky_net_http_" + fnExpr.name.substring(5);
        fnExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: selName };
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
      // Create an IIFE for local let bindings inside expressions
      const stmts: GoIR.GoStmt[] = [];
      const flattenLet = (e: CoreIR.Expr) => {
        if (e.kind === "LetBinding") {
          stmts.push({
            kind: "GoAssignStmt",
            define: true,
            left: [{ kind: "GoIdent", name: e.name }],
            right: lowerExpr(e.value)
          });
          flattenLet(e.body);
        } else {
          stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(e) });
        }
      };
      flattenLet(expr);

      return {
        kind: "GoCallExpr",
        fn: {
          kind: "GoFuncLit",
          type: { kind: "GoFuncType", params: [], results: [lowerType(expr.type)] },
          body: stmts
        },
        args: []
      };
    }
    case "Match": {
      const cases: GoIR.GoCaseClause[] = expr.cases.map((c, i) => {
        const stmts: GoIR.GoStmt[] = [];
        
        // Very basic matching mapped to constructor tags
        // Assuming ADTs are structs with Tag int, and ConstructorValue fields
        if (c.pattern.kind === "ConstructorPattern") {
           // We extract the variables from the struct
           for (let j = 0; j < c.pattern.args.length; j++) {
              const argPat = c.pattern.args[j];
              if (argPat.kind === "VariablePattern" && argPat.name !== "_") {
                  stmts.push({
                      kind: "GoAssignStmt",
                      define: true,
                      left: [{ kind: "GoIdent", name: argPat.name }],
                      right: { kind: "GoSelectorExpr", expr: lowerExpr(expr.expr), sel: `${c.pattern.name}Value` }
                  });
              }
           }
           stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body) });
           
           return {
               kind: "GoCaseClause",
               exprs: [{ kind: "GoBasicLit", value: String(i) }], // Naive: assuming index is tag
               body: stmts
           };
        }
        
        // Fallback catch-all
        if (c.pattern.kind === "WildcardPattern" || c.pattern.kind === "VariablePattern") {
           if (c.pattern.kind === "VariablePattern" && c.pattern.name !== "_") {
               stmts.push({
                   kind: "GoAssignStmt",
                   define: true,
                   left: [{ kind: "GoIdent", name: c.pattern.name }],
                   right: lowerExpr(expr.expr)
               });
           }
           stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body) });
           return { kind: "GoCaseClause", exprs: [], body: stmts };
        }

        return { kind: "GoCaseClause", exprs: [], body: [{ kind: "GoReturnStmt", expr: lowerExpr(c.body) }] };
      });

      return {
        kind: "GoCallExpr",
        fn: {
          kind: "GoFuncLit",
          type: { kind: "GoFuncType", params: [], results: [lowerType(expr.type)] },
          body: [
            {
              kind: "GoSwitchStmt",
              expr: { kind: "GoSelectorExpr", expr: lowerExpr(expr.expr), sel: "Tag" },
              cases: cases
            },
            {
               // Unreachable panic for exhaustiveness fallback
               kind: "GoExprStmt",
               expr: { kind: "GoCallExpr", fn: { kind: "GoIdent", name: "panic" }, args: [{ kind: "GoBasicLit", value: `"unmatched case"` }] }
            }
          ]
        },
        args: []
      };
    }
    // ... we need to expand this as we build out the full compiler ...
    default:
      return { kind: "GoBasicLit", value: "/* unimplemented */" };
  }
}