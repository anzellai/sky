/* src/ffi/convert-types.ts
 *
 * Convert TypeScript types extracted from .d.ts into Sky types.
 *
 * This is intentionally conservative:
 * - only well-known safe conversions are performed
 * - unknown types fall back to `Foreign`
 *
 * The goal is to generate usable bindings without risking incorrect typing.
 */

export interface ConvertedFunctionType {
  readonly name: string
  readonly skyType: string
}

export interface ConvertSignatureResult {
  readonly converted?: ConvertedFunctionType
  readonly diagnostics: readonly string[]
}

export function convertFunctionSignature(
  name: string,
  signatureText: string
): ConvertSignatureResult {

  const diagnostics: string[] = []

  const arrowIndex = signatureText.lastIndexOf("=>")

  if (arrowIndex === -1) {
    diagnostics.push(`Unsupported signature format: ${signatureText}`)
    return { diagnostics }
  }

  const paramsPart = signatureText.slice(0, arrowIndex).trim()
  const returnPart = signatureText.slice(arrowIndex + 2).trim()

  const params = parseParameterTypes(paramsPart)

  const convertedParams = params.map(convertType)
  const convertedReturn = convertType(returnPart)

  const skyType =
    convertedParams.length === 0
      ? convertedReturn
      : `${convertedParams.join(" -> ")} -> ${convertedReturn}`

  return {
    converted: {
      name,
      skyType
    },
    diagnostics
  }
}

function parseParameterTypes(paramText: string): string[] {

  const trimmed = paramText
    .replace(/^\(/, "")
    .replace(/\)$/, "")
    .trim()

  if (trimmed.length === 0) {
    return []
  }

  return trimmed
    .split(",")
    .map(p => p.trim())
    .map(p => {
      const colon = p.indexOf(":")
      if (colon !== -1) {
        return p.slice(colon + 1).trim()
      }
      return p
    })
}

export function convertType(tsType: string): string {

  const t = tsType.trim()

  // primitive mappings
  if (t === "string") return "String"
  if (t === "number") return "Float"
  if (t === "boolean") return "Bool"
  if (t === "void") return "Unit"
  if (t === "undefined") return "Unit"
  if (t === "null") return "Unit"

  // Array<T>
  const arrayMatch = /^Array<(.*)>$/.exec(t)
  if (arrayMatch) {
    return `List ${convertType(arrayMatch[1])}`
  }

  // T[]
  if (t.endsWith("[]")) {
    return `List ${convertType(t.slice(0, -2))}`
  }

  // Promise<T>
  const promiseMatch = /^Promise<(.*)>$/.exec(t)
  if (promiseMatch) {
    return `Task ${convertType(promiseMatch[1])}`
  }

  // function type
  const arrowIndex = t.indexOf("=>")
  if (arrowIndex !== -1) {
    const left = t.slice(0, arrowIndex).trim()
    const right = t.slice(arrowIndex + 2).trim()

    const args = parseParameterTypes(left).map(convertType)
    const ret = convertType(right)

    if (args.length === 0) {
      return ret
    }

    return `${args.join(" -> ")} -> ${ret}`
  }

  // union types fallback
  if (t.includes("|")) {
    return "Foreign"
  }

  // object literal fallback
  if (t.startsWith("{") && t.endsWith("}")) {
    return "Foreign"
  }

  // generic fallback
  if (/^[A-Z]/.test(t)) {
    return t
  }

  return "Foreign"
}