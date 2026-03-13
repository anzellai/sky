// src/interop/go/type-mapper.ts

export function mapGoTypeToSky(goType: string): string {
    let t = goType.replace(/^\*+/, "").trim(); // Remove pointers

    // Handle variadic
    if (t.startsWith("...")) {
        t = "[]" + t.substring(3);
    }

    if (t === "string") return "String";
    if (t === "bool") return "Bool";
    if (t === "int" || t === "int8" || t === "int16" || t === "int32" || t === "int64" || 
        t === "uint" || t === "uint8" || t === "uint16" || t === "uint32" || t === "uint64" || 
        t === "uintptr" || t === "rune") return "Int";
    if (t === "float32" || t === "float64") return "Float";
    
    if (t === "[]byte") return "Bytes";
    if (t === "error") return "Error";
    if (t === "any" || t === "interface{}") return "Any";

    if (t.startsWith("[]")) {
        const inner = mapGoTypeToSky(t.substring(2));
        return inner.includes(" ") ? `(List (${inner}))` : `List ${inner}`;
    }

    if (t.startsWith("map[")) {
        const match = t.match(/map\[(.*?)\](.*)/);
        if (match) {
            const k = mapGoTypeToSky(match[1]);
            const v = mapGoTypeToSky(match[2]);
            return `Map ${k.includes(" ") ? `(${k})` : k} ${v.includes(" ") ? `(${v})` : v}`;
        }
    }

    if (t.startsWith("func(") || t.startsWith("chan") || t.startsWith("<-chan") || t.startsWith("chan<-")) {
        return "Any"; // Keep functions simple for now
    }

    // Strip package prefix
    const dotIdx = t.lastIndexOf(".");
    if (dotIdx !== -1) {
        t = t.substring(dotIdx + 1);
    }

    // Strip generics [T]
    const bracketIdx = t.indexOf("[");
    if (bracketIdx !== -1) {
        t = t.substring(0, bracketIdx);
    }

    // Ensure uppercase so it's a TypeReference, not a TypeVariable!
    return t.charAt(0).toUpperCase() + t.slice(1);
}

export function lowerCamelCase(s: string): string {
    if (s.length === 0) return s;
    return s.charAt(0).toLowerCase() + s.slice(1);
}
