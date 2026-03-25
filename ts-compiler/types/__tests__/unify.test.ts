import { describe, it, expect } from "vitest";
import { unify, UnificationError } from "../unify.js";
import {
  Type,
  freshTypeVariable,
  emptySubstitution,
  applySubstitution,
} from "../types.js";

function tc(name: string): Type {
  return { kind: "TypeConstant", name };
}

function tv(name?: string, constraints?: readonly string[]): Type {
  const v = freshTypeVariable();
  return { ...v, name, constraints } as Type;
}

function tfn(from: Type, to: Type): Type {
  return { kind: "TypeFunction", from, to };
}

function tapp(ctor: Type, args: Type[]): Type {
  return { kind: "TypeApplication", constructor: ctor, arguments: args };
}

describe("unify", () => {
  it("unifies identical constants", () => {
    const s = unify(tc("Int"), tc("Int"));
    expect(s).toBeDefined();
  });

  it("unifies type variable with constant", () => {
    const a = tv("a");
    const s = unify(a, tc("Int"));
    const resolved = applySubstitution(a, s);
    expect(resolved).toEqual(tc("Int"));
  });

  it("rejects mismatched constants", () => {
    expect(() => unify(tc("Int"), tc("String"))).toThrow(UnificationError);
  });

  it("allows Int and Float to unify", () => {
    const s = unify(tc("Int"), tc("Float"));
    expect(s).toBeDefined();
  });

  it("allows Char and String to unify", () => {
    const s = unify(tc("Char"), tc("String"));
    expect(s).toBeDefined();
  });

  it("unifies function types", () => {
    const a = tv("a");
    const s = unify(
      tfn(tc("Int"), a),
      tfn(tc("Int"), tc("String"))
    );
    const resolved = applySubstitution(a, s);
    expect(resolved).toEqual(tc("String"));
  });

  it("rejects occurs check violation", () => {
    const a = tv("a") as any;
    const listA = tapp(tc("List"), [a]);
    expect(() => unify(a, listA)).toThrow("Occurs check");
  });

  it("unifies JsValue with anything", () => {
    const s = unify(tc("JsValue"), tc("Int"));
    expect(s).toBeDefined();
  });

  it("allows Foreign Go types to unify (interface satisfaction)", () => {
    const s = unify(tc("ResponseWriter"), tc("Writer"));
    expect(s).toBeDefined();
  });

  it("rejects Sky native type mismatches", () => {
    expect(() => unify(tc("Int"), tc("String"))).toThrow(UnificationError);
    expect(() => unify(tc("Bool"), tc("List"))).toThrow(UnificationError);
  });
});

describe("type constraints", () => {
  it("allows Int for comparable", () => {
    const a = tv("comparable", ["comparable"]);
    const s = unify(a, tc("Int"));
    expect(s).toBeDefined();
  });

  it("allows String for comparable", () => {
    const a = tv("comparable", ["comparable"]);
    const s = unify(a, tc("String"));
    expect(s).toBeDefined();
  });

  it("rejects function type for comparable", () => {
    const a = tv("comparable", ["comparable"]);
    expect(() => unify(a, tfn(tc("Int"), tc("Int")))).toThrow("not comparable");
  });

  it("rejects record type for comparable", () => {
    const a = tv("comparable", ["comparable"]);
    const record: Type = { kind: "TypeRecord", fields: { x: tc("Int") } };
    expect(() => unify(a, record)).toThrow("not comparable");
  });

  it("allows Int for number", () => {
    const a = tv("n", ["number"]);
    const s = unify(a, tc("Int"));
    expect(s).toBeDefined();
  });

  it("allows Float for number", () => {
    const a = tv("n", ["number"]);
    const s = unify(a, tc("Float"));
    expect(s).toBeDefined();
  });

  it("rejects String for number", () => {
    const a = tv("n", ["number"]);
    expect(() => unify(a, tc("String"))).toThrow("not a number");
  });

  it("allows String for appendable", () => {
    const a = tv("a", ["appendable"]);
    const s = unify(a, tc("String"));
    expect(s).toBeDefined();
  });

  it("allows List for appendable", () => {
    const a = tv("a", ["appendable"]);
    const s = unify(a, tapp(tc("List"), [tc("Int")]));
    expect(s).toBeDefined();
  });

  it("rejects Int for appendable", () => {
    const a = tv("a", ["appendable"]);
    expect(() => unify(a, tc("Int"))).toThrow("not appendable");
  });
});
