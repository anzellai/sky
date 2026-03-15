// src/type-system/env.ts
// Typing environment for Sky Hindley–Milner inference
//
// The environment maps identifiers → type schemes.
// It supports:
// - lexical scoping
// - extension for let bindings
// - lookup
// - computing free type variables across the environment

import type { Scheme } from "../types/types.js";
import { applySubstitutionToScheme, freeTypeVariablesInScheme, typeConstant, mono } from "../types/types.js";

export class TypeEnvironment {

  private readonly values: Map<string, Scheme>;

  constructor(parent?: TypeEnvironment) {

    if (parent) {
      this.values = new Map(parent.values);
    } else {
      this.values = new Map();
    }

  }

  clone(): TypeEnvironment {

    const env = new TypeEnvironment();

    for (const [k, v] of this.values.entries()) {
      env.values.set(k, v);
    }

    return env;

  }

  set(name: string, scheme: Scheme): void {

    this.values.set(name, scheme);

  }

  get(name: string): Scheme | undefined {

    return this.values.get(name);

  }

  has(name: string): boolean {

    return this.values.has(name);

  }

  extend(name: string, scheme: Scheme): TypeEnvironment {

    const env = this.clone();

    env.set(name, scheme);

    return env;

  }

  extendMany(bindings: Readonly<Record<string, Scheme>>): TypeEnvironment {

    const env = this.clone();

    for (const [name, scheme] of Object.entries(bindings)) {
      env.set(name, scheme);
    }

    return env;

  }

  applySubstitution(sub: { mappings: ReadonlyMap<number, any> }): TypeEnvironment {

    const env = new TypeEnvironment();

    for (const [name, scheme] of this.values.entries()) {

      env.values.set(name, applySubstitutionToScheme(scheme, sub));

    }

    return env;

  }

  freeTypeVariables(): ReadonlySet<number> {

    const vars = new Set<number>();

    for (const scheme of this.values.values()) {

      const schemeVars = freeTypeVariablesInScheme(scheme);

      for (const v of schemeVars) {
        vars.add(v);
      }

    }

    return vars;

  }

  entries(): IterableIterator<[string, Scheme]> {

    return this.values.entries();

  }

}


// ------------------------------------------------------------
// Default prelude environment
// ------------------------------------------------------------

export function createPreludeEnvironment(): TypeEnvironment {

  const env = new TypeEnvironment();

  // numeric operators

  env.set("+", mono(typeConstant("Int")));
  env.set("-", mono(typeConstant("Int")));
  env.set("*", mono(typeConstant("Int")));
  env.set("/", mono(typeConstant("Int")));

  // boolean values

  env.set("True", mono(typeConstant("Bool")));
  env.set("False", mono(typeConstant("Bool")));
  env.set("()", mono(typeConstant("Unit")));

  return env;

}
