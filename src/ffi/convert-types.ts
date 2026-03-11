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

  let cleanSignatureText = signatureText.trim()
  if (cleanSignatureText.startsWith("<")) {
    let depth = 0;
    let i = 0;
    for (; i < cleanSignatureText.length; i++) {
      if (cleanSignatureText[i] === "<") depth++;
      else if (cleanSignatureText[i] === ">") depth--;
      if (depth === 0) break;
    }
    cleanSignatureText = cleanSignatureText.slice(i + 1).trim();
  }

  const arrowIndex = cleanSignatureText.lastIndexOf("=>")

  if (arrowIndex === -1) {
    diagnostics.push(`Unsupported signature format: ${cleanSignatureText}`)
    return { diagnostics }
  }

  const paramsText = cleanSignatureText.slice(0, arrowIndex).trim()
  const returnText = cleanSignatureText.slice(arrowIndex + 2).trim()

  const params = parseParameterTypes(paramsText)

  const convertedParams = params.map(convertType)
  const convertedReturn = convertType(returnText)

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

  const parts: string[] = [];
  let current = "";
  let depth = 0;

  for (let i = 0; i < trimmed.length; i++) {
    const char = trimmed[i];
    if (char === "<" || char === "(" || char === "{") depth++;
    else if (char === ">" || char === ")" || char === "}") depth--;

    if (char === "," && depth === 0) {
      parts.push(current.trim());
      current = "";
    } else {
      current += char;
    }
  }
  
  if (current.trim()) {
    parts.push(current.trim());
  }

  return parts
    .map(p => {
      let isVariadic = false;
      if (p.startsWith("...")) {
        isVariadic = true;
      }

      // Only split by colon if it's not inside an object literal or generic
      let cDepth = 0;
      for (let i = 0; i < p.length; i++) {
        if (p[i] === "<" || p[i] === "(" || p[i] === "{") cDepth++;
        else if (p[i] === ">" || p[i] === ")" || p[i] === "}") cDepth--;
        if (p[i] === ":" && cDepth === 0) {
          let typeStr = p.slice(i + 1).trim();
          if (isVariadic && typeStr.endsWith("[]")) {
            // Flatten variadics: `...args: T[]` becomes just `T`
            typeStr = typeStr.slice(0, -2).trim();
          }
          return typeStr;
        }
      }
      return p.trim();
    })
}

export function convertType(tsType: string): string {

  const t = tsType.trim()

  // primitive mappings
  if (t === "string") return "String"
  if (t === "number") return "Int"
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
      return `Unit -> ${ret}`
    }

    return `(${args.join(" -> ")} -> ${ret})`
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
  return "Foreign"
}