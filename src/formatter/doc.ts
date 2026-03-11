export type Doc =
  | { kind: "text"; value: string }
  | { kind: "line" }
  | { kind: "softline" }
  | { kind: "concat"; parts: Doc[] }
  | { kind: "indent"; doc: Doc }
  | { kind: "group"; doc: Doc }

export function text(value: string): Doc {
  return { kind: "text", value }
}

export const line: Doc = { kind: "line" }

export const softline: Doc = { kind: "softline" }

export function concat(...parts: Doc[]): Doc {
  return { kind: "concat", parts }
}

export function indent(doc: Doc): Doc {
  return { kind: "indent", doc }
}

export function group(doc: Doc): Doc {
  return { kind: "group", doc }
}
