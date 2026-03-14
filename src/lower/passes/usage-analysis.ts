import * as CoreIR from "../../core-ir/core-ir.js";

export interface UsageGraph {
    usedVariables: Set<string>;
    usedFunctions: Set<string>;
    usedImports: Set<string>;
    usedConstructors: Set<string>;
}

export function analyzeUsage(module: CoreIR.Module): UsageGraph {
    const graph: UsageGraph = {
        usedVariables: new Set(),
        usedFunctions: new Set(),
        usedImports: new Set(),
        usedConstructors: new Set(),
    };

    // main is always an entry point
    graph.usedFunctions.add("main");

    const scanExpr = (expr: CoreIR.Expr | CoreIR.Pattern) => {
        if (!expr) return;

        switch (expr.kind) {
            case "Variable":
                graph.usedVariables.add(expr.name);
                break;
            case "VariablePattern":
                // VariablePattern introduces a variable, it doesn't use it.
                break;
            case "Application":
                scanExpr(expr.fn);
                for (const arg of expr.args) scanExpr(arg);
                break;
            case "LetBinding":
                scanExpr(expr.value);
                scanExpr(expr.body);
                break;
            case "Lambda":
                scanExpr(expr.body);
                break;
            case "IfExpr":
                scanExpr(expr.condition);
                scanExpr(expr.thenBranch);
                scanExpr(expr.elseBranch);
                break;
            case "Constructor":
                graph.usedConstructors.add(expr.name);
                for (const arg of expr.args) scanExpr(arg);
                break;
            case "Match":
                scanExpr(expr.expr);
                for (const c of expr.cases) {
                    scanExpr(c.pattern);
                    scanExpr(c.body);
                }
                break;
            case "RecordExpr":
                for (const k in expr.fields) {
                    scanExpr(expr.fields[k]);
                }
                break;
            case "ListExpr":
                for (const item of expr.items) scanExpr(item);
                break;
            case "ModuleRef":
                graph.usedImports.add(expr.module.join("."));
                graph.usedFunctions.add(`${expr.module.join(".")}.${expr.name}`);
                break;
        }
    };

    // Analyze starting from exposed/used functions
    // In a real system we'd build a call graph and trace from entry points.
    // Here we'll do a simple sweep of all declarations to be safe, but note their usage.
    for (const decl of module.declarations) {
        // If it's main or exported, scan it
        scanExpr(decl.body);
    }

    return graph;
}
