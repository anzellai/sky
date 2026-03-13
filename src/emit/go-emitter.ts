// src/emit/go-emitter.ts
import * as GoIR from "../go-ir/go-ir.js";

export function emitGoPackage(pkg: GoIR.GoPackage): string {
  let out = `package ${pkg.name}\n\n`;

  if (pkg.imports.length > 0) {
    out += `import (\n`;
    for (const imp of pkg.imports) {
      if (imp.alias) {
        out += `\t${imp.alias} "${imp.path}"\n`;
      } else {
        out += `\t"${imp.path}"\n`;
      }
    }
    out += `)\n\n`;
  }

  for (const decl of pkg.declarations) {
    out += emitGoDeclaration(decl) + "\n\n";
  }

  return out;
}

function emitGoDeclaration(decl: GoIR.GoDeclaration): string {
  switch (decl.kind) {
    case "GoStructDecl": {
      let out = `type ${decl.name}`;
      if (decl.typeParams.length > 0) {
        out += `[${decl.typeParams.map(p => `${p} any`).join(", ")}]`;
      }
      out += ` struct {\n`;
      for (const field of decl.fields) {
        out += `\t${field.name} ${emitGoType(field.type)}`;
        if (field.tag) {
          out += ` \`${field.tag}\``;
        }
        out += `\n`;
      }
      out += `}`;
      return out;
    }
    case "GoTypeDecl": {
      let out = `type ${decl.name}`;
      if (decl.typeParams.length > 0) {
        out += `[${decl.typeParams.map(p => `${p} any`).join(", ")}]`;
      }
      out += ` ${emitGoType(decl.underlyingType)}`;
      return out;
    }
    case "GoFuncDecl": {
      let out = `func ${decl.name}`;
      if (decl.typeParams.length > 0) {
        out += `[${decl.typeParams.map(p => `${p} any`).join(", ")}]`;
      }
      out += `(`;
      out += decl.params.map(p => `${p.name} ${emitGoType(p.type)}`).join(", ");
      out += `)`;
      if (decl.returnType) {
        out += ` ${emitGoType(decl.returnType)}`;
      }
      out += ` {\n`;
      for (const stmt of decl.body) {
        out += emitGoStmt(stmt, 1) + "\n";
      }
      out += `}`;
      return out;
    }
    case "GoVarDecl": {
      let out = `var ${decl.name} ${emitGoType(decl.type)}`;
      if (decl.value) {
        out += ` = ${emitGoExpr(decl.value)}`;
      }
      return out;
    }
  }
}

function emitGoStmt(stmt: GoIR.GoStmt, indent: number): string {
  const tabs = "\t".repeat(indent);
  switch (stmt.kind) {
    case "GoIfStmt": {
      let out = `${tabs}if ${emitGoExpr(stmt.condition)} {\n`;
      for (const s of stmt.thenBranch) {
        out += emitGoStmt(s, indent + 1) + "\n";
      }
      if (stmt.elseBranch.length > 0) {
        out += `${tabs}} else {\n`;
        for (const s of stmt.elseBranch) {
          out += emitGoStmt(s, indent + 1) + "\n";
        }
      }
      out += `${tabs}}`;
      return out;
    }
    case "GoSwitchStmt": {
      let out = `${tabs}switch ${emitGoExpr(stmt.expr)} {\n`;
      for (const clause of stmt.cases) {
        if (clause.exprs.length === 0) {
          out += `${tabs}default:\n`;
        } else {
          out += `${tabs}case ${clause.exprs.map(emitGoExpr).join(", ")}:\n`;
        }
        for (const s of clause.body) {
          out += emitGoStmt(s, indent + 1) + "\n";
        }
      }
      out += `${tabs}}`;
      return out;
    }
    case "GoAssignStmt": {
      const op = stmt.define ? ":=" : "=";
      return `${tabs}${emitGoExpr(stmt.left)} ${op} ${emitGoExpr(stmt.right)}`;
    }
    case "GoReturnStmt": {
      if (stmt.expr) {
        return `${tabs}return ${emitGoExpr(stmt.expr)}`;
      }
      return `${tabs}return`;
    }
    case "GoExprStmt": {
      return `${tabs}${emitGoExpr(stmt.expr)}`;
    }
    case "GoVarDeclStmt": {
      let out = `${tabs}var ${stmt.name}`;
      if (stmt.type) {
        out += ` ${emitGoType(stmt.type)}`;
      }
      out += ` = ${emitGoExpr(stmt.value)}`;
      return out;
    }
  }
}

function emitGoExpr(expr: GoIR.GoExpr): string {
  switch (expr.kind) {
    case "GoIdent":
      return expr.name;
    case "GoBasicLit":
      return expr.value;
    case "GoCallExpr":
      return `${emitGoExpr(expr.fn)}(${expr.args.map(emitGoExpr).join(", ")})`;
    case "GoSelectorExpr":
      return `${emitGoExpr(expr.expr)}.${expr.sel}`;
    case "GoSliceLit":
      return `${emitGoType(expr.type)}{${expr.elements.map(emitGoExpr).join(", ")}}`;
    case "GoMapLit":
      return `${emitGoType(expr.type)}{${expr.entries.map(e => `${emitGoExpr(e.key)}: ${emitGoExpr(e.value)}`).join(", ")}}`;
    case "GoCompositeLit":
      return `${emitGoType(expr.type)}{${expr.elements.map(emitGoExpr).join(", ")}}`;
    case "GoUnaryExpr":
      return `${expr.op}${emitGoExpr(expr.expr)}`;
    case "GoBinaryExpr":
      return `${emitGoExpr(expr.left)} ${expr.op} ${emitGoExpr(expr.right)}`;
  }
}

function emitGoType(type: GoIR.GoType): string {
  switch (type.kind) {
    case "GoIdentType": {
      if (type.typeArgs && type.typeArgs.length > 0) {
        return `${type.name}[${type.typeArgs.map(emitGoType).join(", ")}]`;
      }
      return type.name;
    }
    case "GoSelectorType":
      return `${type.pkg}.${type.name}`;
    case "GoPointerType":
      return `*${emitGoType(type.elem)}`;
    case "GoSliceType":
      return `[]${emitGoType(type.elem)}`;
    case "GoMapType":
      return `map[${emitGoType(type.key)}]${emitGoType(type.value)}`;
    case "GoFuncType":
      return `func(${type.params.map(emitGoType).join(", ")}) ${type.results.length === 1 ? emitGoType(type.results[0]) : `(${type.results.map(emitGoType).join(", ")})`}`;
    case "GoStructType": {
      let out = `struct { `;
      out += type.fields.map(f => {
        let fOut = `${f.name} ${emitGoType(f.type)}`;
        if (f.tag) fOut += ` \`${f.tag}\``;
        return fOut;
      }).join("; ");
      out += ` }`;
      return out;
    }
    case "GoInterfaceType":
      return `interface{}`;
  }
}