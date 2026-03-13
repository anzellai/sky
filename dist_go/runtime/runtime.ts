// src/runtime/runtime.ts
// Sky runtime support library

export type SkyADT = {
  readonly $: string
  readonly values: any[]
}

export function variant(tag: string, ...values: any[]): SkyADT {

  return {
    $: tag,
    values
  }

}

export function isVariant(v: any, tag: string): boolean {

  return v && typeof v === "object" && v.$ === tag

}

export function match(value: any, branches: Record<string, (...args: any[]) => any>): any {

  if (value && typeof value === "object" && "$" in value) {

    const handler = branches[value.$]

    if (!handler) {
      throw new Error(`Unhandled variant ${value.$}`)
    }

    return handler(...value.values)
  }

  if ("_" in branches) {
    return branches["_"](value)
  }

  throw new Error("Pattern match failed")

}

export function pipe(value: any, fn: (v: any) => any) {

  return fn(value)

}

export function updateRecord(record: any, updates: Record<string, any>) {

  return {
    ...record,
    ...updates
  }

}

export function tuple(...items: any[]) {

  return items

}

export function list(...items: any[]) {

  return items

}

export function equals(a: any, b: any): boolean {

  if (a === b) return true

  if (Array.isArray(a) && Array.isArray(b)) {

    if (a.length !== b.length) return false

    for (let i = 0; i < a.length; i++) {
      if (!equals(a[i], b[i])) return false
    }

    return true
  }

  if (typeof a === "object" && typeof b === "object") {

    const ak = Object.keys(a)
    const bk = Object.keys(b)

    if (ak.length !== bk.length) return false

    for (const k of ak) {
      if (!equals(a[k], b[k])) return false
    }

    return true
  }

  return false

}