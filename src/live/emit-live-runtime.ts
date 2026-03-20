// src/live/emit-live-runtime.ts
// Generates the Go runtime code for a Sky.Live application.
// This replaces the normal main() function with a Live server setup.

import * as AST from "../ast/ast.js";
import {
  ComponentModuleInfo,
  generateComponentUpdateCases,
  generateComponentMsgResolvers,
  generateComponentMsgDecoderCases,
  getComponentImports,
} from "./emit-component-wiring.js";

interface MsgVariant {
  name: string;
  fields: string[]; // parameter type hints (basic: "String", "Int", etc.)
  arity: number;
}

interface PageVariant {
  name: string;
  fields: string[];
  arity: number;
}

interface RouteMapping {
  pattern: string;
  pageConstructor: string;
}

/**
 * Extract Msg variants from a type declaration.
 */
function extractVariants(typeDecl: AST.TypeDeclaration): MsgVariant[] {
  if (!typeDecl.variants) return [];
  return typeDecl.variants.map((v: any) => ({
    name: v.name,
    fields: (v.fields || []).map((f: any) => {
      if (f.kind === "TypeName" || f.kind === "TypeConstructor") {
        return f.name || "Any";
      }
      return "Any";
    }),
    arity: (v.fields || []).length,
  }));
}

/**
 * Generate the decodeMsg function in Go.
 * Maps wire format { msg: "Increment", args: [...] } to Go Msg values.
 */
function generateMsgDecoder(variants: MsgVariant[], pageVariants: PageVariant[], componentInfos: ComponentModuleInfo[] = []): string {
  // Generate Page resolver helper (for Navigate-style msgs with Page args)
  let code = `func resolvePageArg(name string) any {\n`;
  code += `\tswitch name {\n`;
  for (let i = 0; i < pageVariants.length; i++) {
    code += `\tcase "${pageVariants[i].name}":\n`;
    code += `\t\treturn Page{Tag: ${i}}\n`;
  }
  code += `\t}\n\treturn nil\n}\n\n`;

  code += `func decodeMsg(name string, args []json.RawMessage) (any, error) {\n`;
  // Pre-process: if name contains a space, split into msg name + inline args
  // e.g., "Navigate CounterPage" → name="Navigate", inlineArgs=["CounterPage"]
  code += `\t// Handle compound msg strings like "Navigate CounterPage"\n`;
  code += `\tvar inlineResolvedArg any\n`;
  code += `\tparts := strings.SplitN(name, " ", 2)\n`;
  code += `\tif len(parts) > 1 {\n`;
  code += `\t\tname = parts[0]\n`;
  code += `\t\tinlineArg := parts[1]\n`;
  code += `\t\tpage := resolvePageArg(inlineArg)\n`;
  code += `\t\tif page != nil {\n`;
  code += `\t\t\tinlineResolvedArg = page\n`;
  code += `\t\t} else {\n`;
  code += `\t\t\tinlineResolvedArg = inlineArg\n`;
  code += `\t\t}\n`;
  code += `\t}\n`;
  // Build set of component-wired variant names (handled by component decoder)
  const componentVariantNames = new Set(componentInfos.map(c => c.binding.msgWrapperName));

  code += `\tswitch name {\n`;

  for (const v of variants) {
    // Skip component-wired variants — they're handled by the component decoder below
    if (componentVariantNames.has(v.name)) continue;
    code += `\tcase "${v.name}":\n`;
    if (v.arity === 0) {
      code += `\t\treturn Msg{Tag: ${tagIndex(v.name, variants)}}, nil\n`;
    } else {
      if (v.arity === 1) {
        // Single arg: use inline resolved arg if available, else decode from JSON
        const suffix = "";
        code += `\t\tif inlineResolvedArg != nil {\n`;
        code += `\t\t\treturn Msg{Tag: ${tagIndex(v.name, variants)}, ${v.name}Value${suffix}: inlineResolvedArg}, nil\n`;
        code += `\t\t}\n`;
        code += `\t\tif len(args) < 1 {\n`;
        code += `\t\t\treturn nil, fmt.Errorf("${v.name} expects 1 arg, got %d", len(args))\n`;
        code += `\t\t}\n`;
        code += generateArgDecoder(0, v.fields[0] || "Any");
        code += `\t\treturn Msg{Tag: ${tagIndex(v.name, variants)}, ${v.name}Value${suffix}: arg0}, nil\n`;
      } else {
        // Multiple args: decode from JSON
        code += `\t\tif len(args) < ${v.arity} {\n`;
        code += `\t\t\treturn nil, fmt.Errorf("${v.name} expects ${v.arity} args, got %d", len(args))\n`;
        code += `\t\t}\n`;
        for (let i = 0; i < v.arity; i++) {
          code += generateArgDecoder(i, v.fields[i] || "Any");
        }
        const fieldAssignments = [];
        for (let i = 0; i < v.arity; i++) {
          const suffix = i === 0 ? "" : String(i);
          fieldAssignments.push(`${v.name}Value${suffix}: arg${i}`);
        }
        code += `\t\treturn Msg{Tag: ${tagIndex(v.name, variants)}, ${fieldAssignments.join(", ")}}, nil\n`;
      }
    }
  }

  // Component message decoder cases
  if (componentInfos.length > 0) {
    code += generateComponentMsgDecoderCases(componentInfos);
  }
  code += `\tdefault:\n`;
  code += `\t\treturn nil, fmt.Errorf("unknown message: %s", name)\n`;
  code += `\t}\n`;
  code += `}\n\n`;

  // Helper to marshal inline args
  code += `func mustMarshal(v any) json.RawMessage {\n`;
  code += `\tb, _ := json.Marshal(v)\n`;
  code += `\treturn b\n`;
  code += `}\n`;

  return code;
}

function generateArgDecoder(index: number, fieldType: string): string {
  switch (fieldType) {
    case "String":
      return `\t\tvar arg${index} string\n\t\tjson.Unmarshal(args[${index}], &arg${index})\n`;
    case "Int":
      return `\t\tvar arg${index} int\n\t\tjson.Unmarshal(args[${index}], &arg${index})\n`;
    case "Bool":
      return `\t\tvar arg${index} bool\n\t\tjson.Unmarshal(args[${index}], &arg${index})\n`;
    case "Float":
      return `\t\tvar arg${index} float64\n\t\tjson.Unmarshal(args[${index}], &arg${index})\n`;
    default:
      // Complex types: decode as any via json
      return `\t\tvar arg${index} any\n\t\tjson.Unmarshal(args[${index}], &arg${index})\n`;
  }
}

function tagIndex(name: string, variants: MsgVariant[]): number {
  return variants.findIndex((v) => v.name === name);
}

function navigateTagIndex(variants: MsgVariant[]): number {
  // Find the Navigate variant (the one with a single Page-typed arg)
  const idx = variants.findIndex((v) => v.name === "Navigate");
  return idx >= 0 ? idx : 0;
}

/**
 * Generate the route table and URL mapping functions.
 */
function generateRouteTable(
  routes: RouteMapping[],
  pageVariants: PageVariant[],
  notFoundPage: string
): string {
  let code = `func getRoutes() []skylive_rt.PageDef {\n`;
  code += `\treturn []skylive_rt.PageDef{\n`;
  for (const r of routes) {
    const tagIdx = pageVariants.findIndex((p) => p.name === r.pageConstructor);
    code += `\t\t{Pattern: "${r.pattern}", Page: map[string]any{"Tag": ${tagIdx}, "name": "${r.pageConstructor}"}},\n`;
  }
  code += `\t}\n}\n\n`;

  // Helper to get page tag as int from either Page struct or map
  code += `func pageTag(page any) int {\n`;
  code += `\tswitch p := page.(type) {\n`;
  code += `\tcase Page:\n`;
  code += `\t\treturn p.Tag\n`;
  code += `\tcase map[string]any:\n`;
  code += `\t\tif t, ok := p["Tag"]; ok {\n`;
  code += `\t\t\tif n, ok := t.(int); ok { return n }\n`;
  code += `\t\t\tif n, ok := t.(float64); ok { return int(n) }\n`;
  code += `\t\t}\n`;
  code += `\t}\n`;
  code += `\treturn -1\n}\n\n`;

  // URL reverse mapping
  code += `func urlForPage(page any) string {\n`;
  code += `\tswitch pageTag(page) {\n`;
  for (let i = 0; i < pageVariants.length; i++) {
    const route = routes.find(r => r.pageConstructor === pageVariants[i].name);
    if (route) {
      code += `\tcase ${i}:\n`;
      code += `\t\treturn "${route.pattern}"\n`;
    }
  }
  code += `\t}\n\treturn "/"\n}\n\n`;

  // Title mapping
  code += `func titleForPage(page any) string {\n`;
  code += `\tswitch pageTag(page) {\n`;
  for (let i = 0; i < pageVariants.length; i++) {
    code += `\tcase ${i}:\n`;
    code += `\t\treturn "${pageVariants[i].name}"\n`;
  }
  code += `\t}\n\treturn "Sky.Live"\n}\n`;

  return code;
}

/**
 * Generate a fixModel function that reconstructs ADT structs from their
 * map[string]any representations (needed after JSON deserialization).
 */
function generateModelFixup(pageVariants: PageVariant[]): string {
  let code = `// fixModel reconstructs ADT structs from map representations after JSON deserialization.
func fixModel(model any) any {
\tm, ok := model.(map[string]any)
\tif !ok { return model }
\t// Fix the "page" field: convert map[string]any to Page struct
\tif pageVal, ok := m["page"]; ok {
\t\tif pageMap, ok := pageVal.(map[string]any); ok {
\t\t\tif tag, ok := pageMap["Tag"]; ok {
\t\t\t\tswitch t := tag.(type) {
\t\t\t\tcase int:
\t\t\t\t\tm["page"] = Page{Tag: t}
\t\t\t\tcase float64:
\t\t\t\t\tm["page"] = Page{Tag: int(t)}
\t\t\t\t}
\t\t\t}
\t\t}
\t}
\treturn m
}
`;
  return code;
}

/**
 * Generate the complete main.go for a Live app.
 * This replaces the normal main.go generated by the compiler.
 */
export function generateLiveMain(
  mainModule: AST.Module,
  msgTypeDecl: AST.TypeDeclaration | undefined,
  pageTypeDecl: AST.TypeDeclaration | undefined,
  routes: RouteMapping[],
  port: number = 4000,
  storeType: string = "memory",
  storePath: string = "",
  notFoundPage: string = "",
  componentInfos: ComponentModuleInfo[] = [],
  inputMode: string = "debounce",
  pollInterval: number = 0
): string {
  const msgVariants = msgTypeDecl ? extractVariants(msgTypeDecl) : [];
  const pageVariants = pageTypeDecl ? extractVariants(pageTypeDecl) : [];

  // Component imports
  const componentImports = getComponentImports(componentInfos);
  const componentImportsStr = componentImports.length > 0
    ? "\n" + componentImports.map(i => `\t${i}`).join("\n")
    : "";

  let code = `package main

import (
\t"encoding/json"
\t"fmt"
\t"strings"
\t"time"
\t"sky-out/skylive_rt"${componentImportsStr}
)

`;

  // Pre-compute Navigate tag index for BuildNavigateMsg
  const navTagIdx = navigateTagIndex(msgVariants);

  // Msg decoder
  code += generateMsgDecoder(msgVariants, pageVariants, componentInfos);
  code += "\n";

  // Route table
  code += generateRouteTable(routes, pageVariants, "NotFoundPage");
  code += "\n";

  // Model fixup: reconstruct ADT structs from map[string]any after JSON deserialization.
  code += generateModelFixup(pageVariants);
  code += "\n";

  // Component sub-message resolvers
  if (componentInfos.length > 0) {
    code += generateComponentMsgResolvers(componentInfos);
    code += "\n";
  }

  // Msg tag-to-name mapping for subscription runtime
  code += `func msgTagToName(tag int) string {\n`;
  code += `\tswitch tag {\n`;
  for (let i = 0; i < msgVariants.length; i++) {
    code += `\tcase ${i}:\n\t\treturn "${msgVariants[i].name}"\n`;
  }
  code += `\t}\n\treturn ""\n}\n\n`;

  // Check if module has a top-level subscriptions function
  const hasSubscriptions = mainModule.declarations.some(
    (d) => d.kind === "FunctionDeclaration" && d.name === "subscriptions"
  );

  // Main function — starts the Live server
  // Note: Sky compiles Update as Update(msg, model) (flattened from curried form)
  // and Init returns sky_wrappers.Tuple2{V0: model, V1: cmd}
  code += `func main() {
\tconfig := skylive_rt.LiveConfig{
\t\tPort:         ${port},
\t\tTTL:          30 * time.Minute,
\t\tStoreType:    "${storeType}",
\t\tStorePath:    "${storePath}",
\t\tInputMode:    "${inputMode}",
\t\tPollInterval: ${pollInterval},
\t}

\tapp := skylive_rt.LiveApp{
\t\tInit: func(req map[string]any, page any) (any, []any) {
\t\t\tresult := Init(req)
\t\t\t// Init returns sky_wrappers.Tuple2 struct
\t\t\tswitch t := result.(type) {
\t\t\tcase sky_wrappers.Tuple2:
\t\t\t\treturn t.V0, nil
\t\t\tdefault:
\t\t\t\treturn result, nil
\t\t\t}
\t\t},
\t\tUpdate: func(msg any, model any) (any, []any) {
\t\t\t// Fix model types after JSON deserialization (persistent stores)
\t\t\tmodel = fixModel(model)
${componentInfos.length > 0 ? generateComponentUpdateCases(componentInfos) : ""}\t\t\t// Update is compiled as Update(msg, model) returning Tuple2
\t\t\tresult := Update(msg, model)
\t\t\tswitch t := result.(type) {
\t\t\tcase sky_wrappers.Tuple2:
\t\t\t\treturn t.V0, nil
\t\t\tdefault:
\t\t\t\treturn result, nil
\t\t\t}
\t\t},
\t\tView: func(model any) *skylive_rt.VNode {
\t\t\tmodel = fixModel(model)
\t\t\tresult := View(model)
\t\t\treturn skylive_rt.MapToVNode(result)
\t\t},
\t\tDecodeMsg: decodeMsg,
\t\tURLForPage: urlForPage,
\t\tTitleForPage: titleForPage,
\t\tRoutes: getRoutes(),
\t\tNotFound: func() any { p := resolvePageArg("${notFoundPage}"); if p != nil { return p }; return Page{Tag: 0} }(),
\t\tBuildNavigateMsg: func(page any) any {
\t\t\treturn Msg{Tag: ${navTagIdx}, NavigateValue: page}
\t\t},
\t\tMsgTagToName: msgTagToName,${hasSubscriptions ? `
\t\tSubscriptions: func(model any) any {
\t\t\tmodel = fixModel(model)
\t\t\treturn Subscriptions(model)
\t\t},` : ""}
\t}

\tskylive_rt.StartServer(config, app)
}
`;

  return code;
}

/**
 * Parse route definitions from the main module AST.
 * Looks for the routes list in the `app { ... routes = [ ... ] }` call.
 */
export function extractRoutes(mainModule: AST.Module): RouteMapping[] {
  const routes: RouteMapping[] = [];

  // Find the main declaration
  const mainDecl = mainModule.declarations.find(
    (d) => d.kind === "FunctionDeclaration" && d.name === "main"
  );
  if (!mainDecl || mainDecl.kind !== "FunctionDeclaration") return routes;

  // Walk the AST to find the routes list
  extractRoutesFromExpr(mainDecl.body, routes);
  return routes;
}

function extractRoutesFromExpr(expr: AST.Expression, routes: RouteMapping[]): void {
  switch (expr.kind) {
    case "CallExpression":
      // Sky parses `route "/" CounterPage` as curried:
      // CallExpression(callee=CallExpression(callee=route, args=["/"]), args=[CounterPage])
      // Check for this curried form:
      if (
        expr.callee.kind === "CallExpression" &&
        expr.arguments.length === 1
      ) {
        const innerCall = expr.callee;
        const isRouteCall =
          (innerCall.callee.kind === "IdentifierExpression" && innerCall.callee.name === "route") ||
          (innerCall.callee.kind === "QualifiedIdentifierExpression" &&
           innerCall.callee.name.parts.join(".").endsWith("route"));

        if (isRouteCall && innerCall.arguments.length === 1) {
          const patternArg = innerCall.arguments[0];
          const pageArg = expr.arguments[0];
          if (patternArg.kind === "StringLiteralExpression") {
            let pageName = "Unknown";
            if (pageArg.kind === "IdentifierExpression") {
              pageName = pageArg.name;
            } else if (pageArg.kind === "QualifiedIdentifierExpression") {
              pageName = pageArg.name.parts[pageArg.name.parts.length - 1];
            }
            routes.push({ pattern: patternArg.value, pageConstructor: pageName });
          }
        }
      }
      // Also check non-curried form: route("/", CounterPage)
      if (
        expr.callee.kind === "IdentifierExpression" &&
        expr.callee.name === "route" &&
        expr.arguments.length >= 2
      ) {
        const patternArg = expr.arguments[0];
        const pageArg = expr.arguments[1];
        if (patternArg.kind === "StringLiteralExpression") {
          let pageName = "Unknown";
          if (pageArg.kind === "IdentifierExpression") {
            pageName = pageArg.name;
          } else if (pageArg.kind === "QualifiedIdentifierExpression") {
            pageName = pageArg.name.parts[pageArg.name.parts.length - 1];
          }
          routes.push({ pattern: patternArg.value, pageConstructor: pageName });
        }
      }
      // Recurse into arguments (but not the ones we already processed as routes)
      for (const arg of expr.arguments) {
        extractRoutesFromExpr(arg, routes);
      }
      extractRoutesFromExpr(expr.callee, routes);
      break;

    case "LetExpression":
      for (const binding of expr.bindings) {
        extractRoutesFromExpr(binding.value, routes);
      }
      extractRoutesFromExpr(expr.body, routes);
      break;

    case "ListExpression":
      for (const item of expr.items) {
        extractRoutesFromExpr(item, routes);
      }
      break;

    case "RecordExpression":
      for (const field of expr.fields) {
        extractRoutesFromExpr(field.value, routes);
      }
      break;

    case "ParenthesizedExpression":
      extractRoutesFromExpr(expr.expression, routes);
      break;

    case "LambdaExpression":
      extractRoutesFromExpr(expr.body, routes);
      break;
  }
}

/**
 * Find the Page type declaration in a module.
 */
export function findPageType(moduleAst: AST.Module): AST.TypeDeclaration | undefined {
  for (const decl of moduleAst.declarations) {
    if (decl.kind === "TypeDeclaration" && decl.name === "Page") {
      return decl;
    }
  }
  return undefined;
}

/**
 * Extract the notFound page constructor name from the app config.
 */
export function extractNotFound(mainModule: AST.Module): string | null {
  const mainDecl = mainModule.declarations.find(
    (d) => d.kind === "FunctionDeclaration" && d.name === "main"
  );
  if (!mainDecl || mainDecl.kind !== "FunctionDeclaration") return null;

  return findNotFoundInExpr(mainDecl.body);
}

function findNotFoundInExpr(expr: AST.Expression): string | null {
  switch (expr.kind) {
    case "CallExpression":
      for (const arg of expr.arguments) {
        const found = findNotFoundInExpr(arg);
        if (found) return found;
      }
      return findNotFoundInExpr(expr.callee);
    case "RecordExpression":
      for (const field of expr.fields) {
        if (field.name === "notFound") {
          if (field.value.kind === "IdentifierExpression") {
            return field.value.name;
          }
        }
      }
      return null;
    case "LetExpression":
      for (const binding of expr.bindings) {
        const found = findNotFoundInExpr(binding.value);
        if (found) return found;
      }
      return findNotFoundInExpr(expr.body);
    case "ParenthesizedExpression":
      return findNotFoundInExpr(expr.expression);
    default:
      return null;
  }
}
