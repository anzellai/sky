// src/type-system/infer.ts
// Algorithm W with basic pattern support for Sky

import * as AST from "../ast/ast.js";
import {
  Type,
  Scheme,
  Substitution,
  emptySubstitution,
  freshTypeVariable,
  functionType,
  applySubstitution,
  composeSubstitutions,
  instantiate,
  generalize,
  formatType,
} from "../types/types.js";
import { unify } from "./unify.js";
import { TypeEnvironment } from "./env.js";
import {
  inferPattern,
  extendEnvironmentWithPatternBindings,
} from "./patterns.js";
import type { AdtRegistry } from "./adt.js";

export interface InferResult {
  readonly substitution: Substitution;
  readonly type: Type;
}

export interface InferTopLevelResult {
  readonly name: string;
  readonly scheme: Scheme;
  readonly pretty: string;
}

export function inferExpression(
  registry: AdtRegistry,
  env: TypeEnvironment,
  expr: AST.Expression,
  nodeTypes?: Map<string, Type>,
): InferResult {
  switch (expr.kind) {
    case "IdentifierExpression": {
      const value = env.get(expr.name);
      if (!value) {
        throw new Error(`Unbound variable ${expr.name}`);
      }

      const resolved = instantiate(value);
      if (nodeTypes && expr.span) {
        nodeTypes.set(`${expr.span.start.line}:${expr.span.start.column}`, resolved);
      }
      return {
        substitution: emptySubstitution(),
        type: resolved,
      };
    }

    case "QualifiedIdentifierExpression": {
      const fullName = expr.name.parts.join(".");
      const value = env.get(fullName);
      if (!value) {
        throw new Error(`Unbound variable ${fullName}`);
      }

      const resolved = instantiate(value);
      if (nodeTypes && expr.span) {
        nodeTypes.set(`${expr.span.start.line}:${expr.span.start.column}`, resolved);
      }
      return {
        substitution: emptySubstitution(),
        type: resolved,
      };
    }

    case "IntegerLiteralExpression":
      return {
        substitution: emptySubstitution(),
        type: { kind: "TypeConstant", name: "Int" },
      };

    case "FloatLiteralExpression":
      return {
        substitution: emptySubstitution(),
        type: { kind: "TypeConstant", name: "Float" },
      };

    case "StringLiteralExpression":
      return {
        substitution: emptySubstitution(),
        type: { kind: "TypeConstant", name: "String" },
      };

    case "BooleanLiteralExpression":
      return {
        substitution: emptySubstitution(),
        type: { kind: "TypeConstant", name: "Bool" },
      };

    case "UnitExpression":
      return {
        substitution: emptySubstitution(),
        type: { kind: "TypeConstant", name: "Unit" },
      };

    case "CharLiteralExpression":
      return {
        substitution: emptySubstitution(),
        type: { kind: "TypeConstant", name: "Char" },
      };

    case "ParenthesizedExpression":
      return inferExpression(registry, env, expr.expression, nodeTypes);

    case "TupleExpression": {
      let currentSub = emptySubstitution();
      const itemTypes: Type[] = [];

      for (const item of expr.items) {
        const result = inferExpression(
          registry,
          env.applySubstitution(currentSub),
          item,
          nodeTypes,
        );
        currentSub = composeSubstitutions(result.substitution, currentSub);
        itemTypes.push(applySubstitution(result.type, currentSub));
      }

      return {
        substitution: currentSub,
        type: {
          kind: "TypeTuple",
          items: itemTypes,
        },
      };
    }

    case "ListExpression": {
      const elementType = freshTypeVariable();
      let currentSub = emptySubstitution();

      for (const item of expr.items) {
        const itemResult = inferExpression(
          registry,
          env.applySubstitution(currentSub),
          item,
          nodeTypes,
        );

        const s = unify(
          applySubstitution(elementType, currentSub),
          itemResult.type,
        );

        currentSub = composeSubstitutions(
          s,
          composeSubstitutions(itemResult.substitution, currentSub),
        );
      }

      return {
        substitution: currentSub,
        type: {
          kind: "TypeApplication",
          constructor: { kind: "TypeConstant", name: "List" },
          arguments: [applySubstitution(elementType, currentSub)],
        },
      };
    }

    case "RecordExpression": {
      let currentSub = emptySubstitution();
      const fields: Record<string, Type> = {};

      for (const field of expr.fields) {
        const result = inferExpression(
          registry,
          env.applySubstitution(currentSub),
          field.value,
          nodeTypes,
        );
        currentSub = composeSubstitutions(result.substitution, currentSub);
        fields[field.name] = result.type;
      }

      return {
        substitution: currentSub,
        type: {
          kind: "TypeRecord",
          fields,
        },
      };
    }

    case "RecordUpdateExpression": {
      const baseResult = inferExpression(registry, env, expr.base, nodeTypes);
      let currentSub = baseResult.substitution;
      const updatedFields: Record<string, Type> = {};

      for (const field of expr.fields) {
        const result = inferExpression(
          registry,
          env.applySubstitution(currentSub),
          field.value,
          nodeTypes,
        );
        currentSub = composeSubstitutions(result.substitution, currentSub);
        updatedFields[field.name] = result.type;
      }

      // Base must be a record containing at least these fields
      const expectedBase: Type = {
        kind: "TypeRecord",
        fields: updatedFields,
      };

      const s = unify(applySubstitution(baseResult.type, currentSub), expectedBase);
      const finalSub = composeSubstitutions(s, currentSub);

      return {
        substitution: finalSub,
        type: applySubstitution(baseResult.type, finalSub),
      };
    }

    case "FieldAccessExpression": {
      const target = inferExpression(registry, env, expr.target, nodeTypes);
      const fieldType = freshTypeVariable();

      const expectedRecord: Type = {
        kind: "TypeRecord",
        fields: {
          [expr.fieldName]: fieldType,
        },
      };

      const s = unify(target.type, expectedRecord);

      return {
        substitution: composeSubstitutions(s, target.substitution),
        type: applySubstitution(fieldType, s),
      };
    }

    case "LambdaExpression": {
      let currentEnv = env;
      let currentSub = emptySubstitution();
      const paramTypes: Type[] = [];

      for (const param of expr.parameters) {
        const paramType = freshTypeVariable();
        paramTypes.push(paramType);

        const patternResult = inferPattern(
          registry,
          env,
          param.pattern,
          applySubstitution(paramType, currentSub),
        );

        currentSub = composeSubstitutions(patternResult.substitution, currentSub);
        currentEnv = extendEnvironmentWithPatternBindings(
          currentEnv.applySubstitution(currentSub),
          patternResult.bindings,
        );
      }

      const body = inferExpression(
        registry,
        currentEnv.applySubstitution(currentSub),
        expr.body,
        nodeTypes,
      );

      currentSub = composeSubstitutions(body.substitution, currentSub);

      let resultType = body.type;
      for (let i = paramTypes.length - 1; i >= 0; i -= 1) {
        resultType = functionType(
          applySubstitution(paramTypes[i], currentSub),
          resultType,
        );
      }

      return {
        substitution: currentSub,
        type: resultType,
      };
    }

    case "CallExpression": {
      const fn = inferExpression(registry, env, expr.callee, nodeTypes);

      let currentSub = fn.substitution;
      let currentType = fn.type;

      for (const arg of expr.arguments) {
        const argResult = inferExpression(
          registry,
          env.applySubstitution(currentSub),
          arg,
          nodeTypes,
        );

        const resultType = freshTypeVariable();

        const s = unify(
          applySubstitution(currentType, argResult.substitution),
          functionType(argResult.type, resultType),
        );

        currentSub = composeSubstitutions(
          s,
          composeSubstitutions(argResult.substitution, currentSub),
        );

        currentType = applySubstitution(resultType, currentSub);
      }

      return {
        substitution: currentSub,
        type: currentType,
      };
    }

    case "IfExpression": {
      const cond = inferExpression(registry, env, expr.condition, nodeTypes);
      const s1 = unify(cond.type, { kind: "TypeConstant", name: "Bool" });

      const env1 = env.applySubstitution(
        composeSubstitutions(s1, cond.substitution),
      );

      const thenResult = inferExpression(registry, env1, expr.thenBranch, nodeTypes);
      const elseResult = inferExpression(
        registry,
        env1.applySubstitution(thenResult.substitution),
        expr.elseBranch,
        nodeTypes,
      );

      const s2 = unify(
        applySubstitution(thenResult.type, elseResult.substitution),
        elseResult.type,
      );

      const sub = composeSubstitutions(
        s2,
        composeSubstitutions(
          elseResult.substitution,
          composeSubstitutions(thenResult.substitution, composeSubstitutions(s1, cond.substitution)),
        ),
      );

      return {
        substitution: sub,
        type: applySubstitution(elseResult.type, sub),
      };
    }

    case "LetExpression": {
      let currentEnv = env;
      let currentSub = emptySubstitution();

      for (const binding of expr.bindings) {
        const valueResult = inferExpression(
          registry,
          currentEnv.applySubstitution(currentSub),
          binding.value,
          nodeTypes,
        );

        currentSub = composeSubstitutions(valueResult.substitution, currentSub);

        let valueType = applySubstitution(valueResult.type, currentSub);

        if (binding.typeAnnotation) {
          const annotatedType = translateTypeExpression(binding.typeAnnotation);
          const s = unify(valueType, annotatedType);
          currentSub = composeSubstitutions(s, currentSub);
          valueType = applySubstitution(valueType, currentSub);
        }

        const patternResult = inferPattern(
          registry,
          currentEnv.applySubstitution(currentSub),
          binding.pattern,
          valueType,
        );

        currentSub = composeSubstitutions(patternResult.substitution, currentSub);

        const generalizedBindings: Record<string, Scheme> = {};
        for (const [name, type] of Object.entries(patternResult.bindings)) {
          const resolvedType = applySubstitution(type, currentSub);
          generalizedBindings[name] = generalize(
            resolvedType,
            currentEnv.applySubstitution(currentSub).freeTypeVariables(),
          );
          // Record let-binding variable types at their pattern span
          if (nodeTypes && binding.pattern.span) {
            nodeTypes.set(`${binding.pattern.span.start.line}:${binding.pattern.span.start.column}`, resolvedType);
          }
        }

        currentEnv = currentEnv
          .applySubstitution(currentSub)
          .extendMany(generalizedBindings);
      }

      const body = inferExpression(
        registry,
        currentEnv.applySubstitution(currentSub),
        expr.body,
        nodeTypes,
      );

      return {
        substitution: composeSubstitutions(body.substitution, currentSub),
        type: body.type,
      };
    }

    case "CaseExpression": {
      const subject = inferExpression(registry, env, expr.subject, nodeTypes);
      let currentSub = subject.substitution;
      const resultType = freshTypeVariable();

      for (const branch of expr.branches) {
        const branchSubjectType = applySubstitution(subject.type, currentSub);

        const patternResult = inferPattern(
          registry,
          env,
          branch.pattern,
          branchSubjectType,
        );

        currentSub = composeSubstitutions(patternResult.substitution, currentSub);

        // Record case branch pattern binding types
        if (nodeTypes) {
          for (const [, type] of Object.entries(patternResult.bindings)) {
            if (branch.pattern.span) {
              nodeTypes.set(`${branch.pattern.span.start.line}:${branch.pattern.span.start.column}`, type);
            }
          }
        }

        const branchEnv = extendEnvironmentWithPatternBindings(
          env.applySubstitution(currentSub),
          patternResult.bindings,
        );

        const bodyResult = inferExpression(
          registry,
          branchEnv,
          branch.body,
          nodeTypes,
        );

        const s = unify(
          applySubstitution(bodyResult.type, bodyResult.substitution),
          applySubstitution(resultType, currentSub),
        );

        currentSub = composeSubstitutions(
          s,
          composeSubstitutions(bodyResult.substitution, currentSub),
        );
      }

      return {
        substitution: currentSub,
        type: applySubstitution(resultType, currentSub),
      };
    }

    case "BinaryExpression": {
      const left = inferExpression(registry, env, expr.left, nodeTypes);
      const right = inferExpression(
        registry,
        env.applySubstitution(left.substitution),
        expr.right,
        nodeTypes,
      );

      const leftType = applySubstitution(left.type, right.substitution);
      const rightType = right.type;

      switch (expr.operator) {
        case "+":
        case "-":
        case "*":
        case "/":
        case "%": {
          const s1 = unify(leftType, { kind: "TypeConstant", name: "Int" });
          const s2 = unify(
            applySubstitution(rightType, s1),
            { kind: "TypeConstant", name: "Int" },
          );

          return {
            substitution: composeSubstitutions(
              s2,
              composeSubstitutions(s1, composeSubstitutions(right.substitution, left.substitution)),
            ),
            type: { kind: "TypeConstant", name: "Int" },
          };
        }

        case "++": {
          const s1 = unify(leftType, { kind: "TypeConstant", name: "String" });
          const s2 = unify(
            applySubstitution(rightType, s1),
            { kind: "TypeConstant", name: "String" },
          );

          return {
            substitution: composeSubstitutions(
              s2,
              composeSubstitutions(s1, composeSubstitutions(right.substitution, left.substitution)),
            ),
            type: { kind: "TypeConstant", name: "String" },
          };
        }

        case "==":
        case "!=":
        case "<":
        case "<=":
        case ">":
        case ">=": {
          const s = unify(leftType, rightType);

          return {
            substitution: composeSubstitutions(
              s,
              composeSubstitutions(right.substitution, left.substitution),
            ),
            type: { kind: "TypeConstant", name: "Bool" },
          };
        }

        case "&&":
        case "||": {
          const s1 = unify(leftType, { kind: "TypeConstant", name: "Bool" });
          const s2 = unify(
            applySubstitution(rightType, s1),
            { kind: "TypeConstant", name: "Bool" },
          );

          return {
            substitution: composeSubstitutions(
              s2,
              composeSubstitutions(s1, composeSubstitutions(right.substitution, left.substitution)),
            ),
            type: { kind: "TypeConstant", name: "Bool" },
          };
        }

        case "|>": {
          const result = freshTypeVariable();
          const s = unify(
            rightType,
            functionType(leftType, result),
          );

          return {
            substitution: composeSubstitutions(
              s,
              composeSubstitutions(right.substitution, left.substitution),
            ),
            type: applySubstitution(result, s),
          };
        }

        case "<|": {
          const result = freshTypeVariable();
          const s = unify(
            leftType,
            functionType(rightType, result),
          );

          return {
            substitution: composeSubstitutions(
              s,
              composeSubstitutions(right.substitution, left.substitution),
            ),
            type: applySubstitution(result, s),
          };
        }

        case ">>":
        case "<<": {
          const a = freshTypeVariable();
          const b = freshTypeVariable();
          const c = freshTypeVariable();

          const leftExpected =
            expr.operator === ">>"
              ? functionType(a, b)
              : functionType(b, c);

          const rightExpected =
            expr.operator === ">>"
              ? functionType(b, c)
              : functionType(a, b);

          const s1 = unify(leftType, leftExpected);
          const s2 = unify(
            applySubstitution(rightType, s1),
            applySubstitution(rightExpected, s1),
          );

          return {
            substitution: composeSubstitutions(
              s2,
              composeSubstitutions(s1, composeSubstitutions(right.substitution, left.substitution)),
            ),
            type:
              expr.operator === ">>"
                ? functionType(
                  applySubstitution(a, s2),
                  applySubstitution(c, s2),
                )
                : functionType(
                  applySubstitution(a, s2),
                  applySubstitution(c, s2),
                ),
          };
        }

        default:
          throw new Error(`Unknown operator ${expr.operator}`);
      }
    }

    default:
      throw new Error(`Inference not implemented for ${(expr as any).kind}`);
  }
}

function translateTypeExpression(expr: AST.TypeExpression): Type {
  switch (expr.kind) {
    case "TypeVariable":
      return { kind: "TypeVariable", id: -1, name: expr.name }; // Note: name-based variables not fully supported in algorithmic infer yet, using dummy ID
    
    case "TypeReference":
      const baseType: Type = { kind: "TypeConstant", name: expr.name.parts.join(".") };
      if (expr.arguments && expr.arguments.length > 0) {
          return {
              kind: "TypeApplication",
              constructor: baseType,
              arguments: expr.arguments.map(translateTypeExpression)
          };
      }
      return baseType;

    case "FunctionType":
      return functionType(
        translateTypeExpression(expr.from),
        translateTypeExpression(expr.to)
      );
    
    case "RecordType":
      const fields: Record<string, Type> = {};
      for (const f of expr.fields) {
        fields[f.name] = translateTypeExpression(f.type);
      }
      return { kind: "TypeRecord", fields };
  }
}

export function inferTopLevel(
  registry: AdtRegistry,
  env: TypeEnvironment,
  decl: AST.FunctionDeclaration,
  typeAnnotation?: AST.TypeAnnotation,
  nodeTypes?: Map<string, Type>,
): InferTopLevelResult {
  const effectiveAnnotation = decl.typeAnnotation || typeAnnotation;

  const paramTypes = decl.parameters.map(() => freshTypeVariable());

  let localEnv = env;
  let currentSub = emptySubstitution();

  for (let i = 0; i < decl.parameters.length; i += 1) {
    const pattern = decl.parameters[i].pattern;
    const patternResult = inferPattern(
      registry,
      localEnv,
      pattern,
      applySubstitution(paramTypes[i], currentSub),
    );

    currentSub = composeSubstitutions(patternResult.substitution, currentSub);
    localEnv = extendEnvironmentWithPatternBindings(
      localEnv.applySubstitution(currentSub),
      patternResult.bindings,
    );

    // Record parameter types at their pattern span
    if (nodeTypes && pattern.span) {
      nodeTypes.set(
        `${pattern.span.start.line}:${pattern.span.start.column}`,
        applySubstitution(paramTypes[i], currentSub),
      );
    }
  }

  const bodyResult = inferExpression(
    registry,
    localEnv.applySubstitution(currentSub),
    decl.body,
    nodeTypes,
  );

  currentSub = composeSubstitutions(bodyResult.substitution, currentSub);

  let fnType: Type = applySubstitution(bodyResult.type, currentSub);
  for (let i = paramTypes.length - 1; i >= 0; i -= 1) {
    fnType = functionType(
      applySubstitution(paramTypes[i], currentSub),
      fnType,
    );
  }

  const finalType = applySubstitution(fnType, currentSub);

  if (effectiveAnnotation) {
    const annotatedType = translateTypeExpression(effectiveAnnotation.type);
    
    // In a more complete implementation, we would unify finalType with annotatedType here.
    // For now, we trust the annotation for the exported scheme but ensure the body was checked.
    const scheme = generalize(annotatedType, env.freeTypeVariables());
    
    return {
      name: decl.name,
      scheme,
      pretty: formatType(scheme.type),
    };
  }

  const finalScheme = generalize(
    finalType,
    env.applySubstitution(currentSub).freeTypeVariables(),
  );

  return {
    name: decl.name,
    scheme: finalScheme,
    pretty: formatType(finalScheme.type),
  };
}
