import { Doc } from "./doc.js"

const MAX_WIDTH = 80

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
    output += " ".repeat(indentLevel * 4)
    column = indentLevel * 4
  }

  function fits(d: Doc, remaining: number): boolean {

    if (remaining < 0) return false

    switch (d.kind) {

      case "text":
        return d.value.length <= remaining

      case "line":
        return true

      case "softline":
        return true

      case "concat":
        for (const p of d.parts) {
          if (!fits(p, remaining)) return false
        }
        return true

      case "indent":
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

      case "softline":
        newline()
        break

      case "concat":
        for (const p of d.parts) walk(p)
        break

      case "indent":
        indentLevel++
        walk(d.doc)
        indentLevel--
        break

      case "group":

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

      case "softline":
        write(" ")
        break

      case "concat":
        for (const p of d.parts) flatten(p)
        break

      case "indent":
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
