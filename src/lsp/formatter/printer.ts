import { Doc } from "./layout.js"

const MAX_WIDTH = 80
const INDENT_WIDTH = 4

export function render(doc: Doc): string {

  let output = ""
  let indentLevel = 0
  let column = 0

  function write(str: string) {
    output += str
    column += str.length
  }

  function newline() {
    output += "\n"
    output += " ".repeat(indentLevel * INDENT_WIDTH)
    column = indentLevel * INDENT_WIDTH
  }

  // Estimate the flattened width of a doc (line → space, hardline → infinity)
  function flatWidth(d: Doc): number {
    switch (d.kind) {
      case "text":
        return d.value.length
      case "line":
        return 1  // becomes a space when flattened
      case "softline":
        return 0  // becomes empty when flattened (or 1 for space)
      case "hardline":
        return 9999  // can't flatten — forces break
      case "concat": {
        let w = 0
        for (const p of d.parts) {
          w += flatWidth(p)
          if (w > MAX_WIDTH) return w  // early exit
        }
        return w
      }
      case "indent":
        return flatWidth(d.doc)
      case "group":
        return flatWidth(d.doc)
    }
  }

  // Check if a doc fits within remaining columns when flattened
  function fits(d: Doc, remaining: number): boolean {
    if (remaining < 0) return false

    switch (d.kind) {
      case "text":
        return d.value.length <= remaining
      case "line":
        return true  // becomes space (1 char)
      case "softline":
        return true
      case "hardline":
        return false  // hardline always forces a break
      case "concat": {
        let rem = remaining
        for (const p of d.parts) {
          const w = flatWidth(p)
          if (w > rem) return false
          rem -= w
        }
        return true
      }
      case "indent":
        // When flattened, indent doesn't add visible width
        // But when broken, it adds INDENT_WIDTH — accounted in walk()
        return fits(d.doc, remaining)
      case "group":
        return fits(d.doc, remaining)
    }
  }

  function walk(d: Doc) {
    switch (d.kind) {
      case "text":
        write(d.value)
        break

      case "line":
        newline()
        break

      case "hardline":
        newline()
        break

      case "softline":
        newline()
        break

      case "concat":
        for (const p of d.parts) {
          walk(p)
        }
        break

      case "indent":
        indentLevel += 1
        walk(d.doc)
        indentLevel -= 1
        break

      case "group":
        // Try to fit the entire group on one line
        if (fits(d.doc, MAX_WIDTH - column)) {
          flatten(d.doc)
        } else {
          walk(d.doc)
        }
        break
    }
  }

  function flatten(d: Doc) {
    switch (d.kind) {
      case "text":
        write(d.value)
        break

      case "line":
        write(" ")
        break

      case "hardline":
        // hardline is never flattened — always breaks
        newline()
        break

      case "softline":
        // softline becomes empty in flatten mode
        break

      case "concat":
        for (const p of d.parts) flatten(p)
        break

      case "indent":
        // When flattened, indent has no effect (everything is on one line)
        flatten(d.doc)
        break

      case "group":
        flatten(d.doc)
        break
    }
  }

  walk(doc)

  return output
}
