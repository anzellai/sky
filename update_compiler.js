import fs from "fs";

let content = fs.readFileSync("src/compiler.ts", "utf8");

content = content.replace(
  /import \{ emitModule \} from "\.\/codegen\/js-emitter\.js";/g,
  `import { lowerModule } from "./lower/lower-to-go.js";\nimport { emitGoPackage } from "./emit/go-emitter.js";\nimport * as CoreIR from "./core-ir/core-ir.js";`
);

content = content.replace(/const emitted = emitModule\(loaded\.moduleAst, \{[\s\S]*?\}\);/g, 
  `// Basic AST to CoreIR conversion
    const coreModule: CoreIR.Module = astToCore(loaded.moduleAst, typeCheck, foreignResult);
    
    // Lower to GoIR
    const goPkg = lowerModule(coreModule);
    
    // Emit Go code
    const goCode = emitGoPackage(goPkg);`
);

content = content.replace(/code: emitted\.code/g, `code: goCode`);
content = content.replace(/fs\.writeFileSync\(outputFile, emitted\.code, "utf8"\);/g, `fs.writeFileSync(outputFile, goCode, "utf8");`);
content = content.replace(/return path\.join\(outDir, \.\.\.moduleName\) \+ "\.js";/g, 
  `if (moduleName.length === 1 && moduleName[0] === "Main") {
    return path.join(outDir, "main.go"); // main package special case
  }
  return path.join(outDir, ...moduleName) + ".go";`
);

content += `

function convertExpr(expr: AST.Expression): CoreIR.Expr {
  switch (expr.kind) {
    case "IntegerLiteralExpression":
      return { kind: "Literal", value: expr.value, literalType: "Int", type: { kind: "TypeConstant", name: "Int" } };
    case "FloatLiteralExpression":
      return { kind: "Literal", value: expr.value, literalType: "Float", type: { kind: "TypeConstant", name: "Float" } };
    case "StringLiteralExpression":
      return { kind: "Literal", value: expr.value, literalType: "String", type: { kind: "TypeConstant", name: "String" } };
    case "IdentifierExpression":
      return { kind: "Variable", name: expr.name, type: { kind: "TypeConstant", name: "Any" } }; // Simplified type
    case "CallExpression":
      // A call like f(a, b) in Sky is parsed as f(a, b) or nested applications
      // The AST has it as \`callee\` and \`arguments\`
      let res = convertExpr(expr.callee);
      for (const arg of expr.arguments) {
        res = { kind: "Application", fn: res, args: [convertExpr(arg)], type: { kind: "TypeConstant", name: "Any" } };
      }
      return res;
    case "LetExpression": {
      let res = convertExpr(expr.body);
      // Let bindings in AST are usually represented as an array of declarations
      // We'll wrap the body in LetBinding nodes
      for (let i = expr.bindings.length - 1; i >= 0; i--) {
        const binding = expr.bindings[i];
        if (binding.pattern.kind === "VariablePattern") {
          res = {
            kind: "LetBinding",
            name: binding.pattern.name,
            value: convertExpr(binding.expression),
            body: res,
            type: { kind: "TypeConstant", name: "Any" }
          };
        }
      }
      return res;
    }
    default:
      return { kind: "Literal", value: \`/* unimplemented AST node: \${expr.kind} */\`, literalType: "String", type: { kind: "TypeConstant", name: "String" } };
  }
}

function astToCore(ast: AST.Module, typeCheck: TypeCheckResult, foreignResult: any): CoreIR.Module {
  const declarations: CoreIR.Declaration[] = [];
  const typeDeclarations: CoreIR.TypeDeclaration[] = [];

  for (const decl of ast.declarations) {
    if (decl.kind === "FunctionDeclaration") {
      const declInfo = typeCheck.declarations.find(d => d.name === decl.name);
      
      let bodyExpr = convertExpr(decl.body);
      
      // If it has parameters, wrap in lambdas
      for (let i = decl.parameters.length - 1; i >= 0; i--) {
        const paramPattern = decl.parameters[i].pattern;
        const paramName = paramPattern.kind === "VariablePattern" ? paramPattern.name : "_";
        bodyExpr = {
          kind: "Lambda",
          params: [paramName],
          body: bodyExpr,
          type: { kind: "TypeConstant", name: "Any" }
        };
      }

      declarations.push({
        name: decl.name,
        scheme: declInfo?.scheme || { type: { kind: "TypeConstant", name: "Any" }, bound: [] },
        body: bodyExpr
      });
    } else if (decl.kind === "TypeDeclaration") {
      typeDeclarations.push({
        name: decl.name,
        typeParams: Array.from(decl.typeParameters),
        constructors: (decl as any).constructors.map((c: any) => ({
          name: c.name,
          types: c.arguments.map(() => ({ kind: "TypeConstant", name: "Any" }))
        }))
      });
    }
  }

  // Inject foreign imports as declarations
  for (const ffi of foreignResult.bindings) {
    for (const val of ffi.values) {
      // Create a foreign mock function/var
      // Since it's mapped to a Go package, we'll create a ModuleRef or similar
      // Or in the lowerer, we'll map \`listenAndServe\` directly.
      // We will let the Go emitter handle foreign identifiers by preserving their Go names.
    }
  }

  return {
    name: Array.from(ast.name),
    declarations,
    typeDeclarations
  };
}
`;

fs.writeFileSync("src/compiler.ts", content);
