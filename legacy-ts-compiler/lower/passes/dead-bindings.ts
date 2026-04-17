import * as CoreIR from "../../core-ir/core-ir.js";
import { UsageGraph } from "./usage-analysis.js";

export function eliminateDeadBindings(module: CoreIR.Module, usage: UsageGraph): CoreIR.Module {
    
    const newDecls: CoreIR.Declaration[] = [];

    const optimizeExpr = (expr: CoreIR.Expr): CoreIR.Expr => {
        switch (expr.kind) {
            case "LetBinding":
                if (expr.name !== "_" && !usage.usedVariables.has(expr.name)) {
                    // Eliminate unused let bindings that are NOT side-effects
                    return optimizeExpr(expr.body);
                }

                return {
                    ...expr,
                    value: optimizeExpr(expr.value),
                    body: optimizeExpr(expr.body)
                };

            case "Application":
                return {
                    ...expr,
                    fn: optimizeExpr(expr.fn),
                    args: expr.args.map(optimizeExpr)
                };

            case "Lambda":
                return {
                    ...expr,
                    params: expr.params.map(p => usage.usedVariables.has(p) ? p : "_"),
                    body: optimizeExpr(expr.body)
                };

            case "IfExpr":
                return {
                    ...expr,
                    condition: optimizeExpr(expr.condition),
                    thenBranch: optimizeExpr(expr.thenBranch),
                    elseBranch: optimizeExpr(expr.elseBranch)
                };

            case "Match":
                return {
                    ...expr,
                    expr: optimizeExpr(expr.expr),
                    cases: expr.cases.map(c => ({
                        pattern: optimizePattern(c.pattern),
                        body: optimizeExpr(c.body)
                    }))
                };
            
            case "Constructor":
                return {
                    ...expr,
                    args: expr.args.map(optimizeExpr)
                };

            case "RecordExpr":
                const newFields: Record<string, CoreIR.Expr> = {};
                for (const k in expr.fields) {
                    newFields[k] = optimizeExpr(expr.fields[k]);
                }
                return { ...expr, fields: newFields };

            case "ListExpr":
                return { ...expr, items: expr.items.map(optimizeExpr) };

            default:
                return expr;
        }
    };

    const optimizePattern = (pat: CoreIR.Pattern): CoreIR.Pattern => {
        if (pat.kind === "VariablePattern") {
            if (!usage.usedVariables.has(pat.name)) {
                return { kind: "VariablePattern", name: "_" };
            }
        } else if (pat.kind === "ConstructorPattern") {
            return {
                ...pat,
                args: pat.args.map(optimizePattern)
            };
        } else if (pat.kind === "ConsPattern") {
            return {
                ...pat,
                head: optimizePattern(pat.head),
                tail: optimizePattern(pat.tail)
            };
        } else if (pat.kind === "AsPattern") {
            return {
                ...pat,
                pattern: optimizePattern(pat.pattern),
                name: usage.usedVariables.has(pat.name) ? pat.name : "_"
            };
        }
        return pat;
    };

    for (const decl of module.declarations) {
        newDecls.push({
            ...decl,
            body: optimizeExpr(decl.body)
        });
    }

    return {
        ...module,
        declarations: newDecls
    };
}
