// src/interop/go/type-mapper.ts

export function mapGoTypeToSky(goType: string, currentPackage?: string): string {
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

    // Arrays: [N]Type
    if (t.startsWith("[")) {
        const match = t.match(/^\[.*?\](.*)/);
        if (match) {
            const inner = match[1];
            if (inner === "byte") return "Bytes";
            const mappedInner = mapGoTypeToSky(inner, currentPackage);
            return mappedInner.includes(" ") ? `(List (${mappedInner}))` : `List ${mappedInner}`;
        }
    }

    if (t.startsWith("[]")) {
        const inner = mapGoTypeToSky(t.substring(2), currentPackage);
        return inner.includes(" ") ? `(List (${inner}))` : `List ${inner}`;
    }

    if (t.startsWith("map[")) {
        const match = t.match(/map\[(.*?)\](.*)/);
        if (match) {
            const k = mapGoTypeToSky(match[1], currentPackage);
            const v = mapGoTypeToSky(match[2], currentPackage);
            return `Map ${k.includes(" ") ? `(${k})` : k} ${v.includes(" ") ? `(${v})` : v}`;
        }
    }

    if (t.startsWith("chan ") || t.startsWith("<-chan ") || t.startsWith("chan<- ")) {
        let inner = t.replace(/^(?:<-)?chan(?:<-)?\s+/, "");
        const mappedInner = mapGoTypeToSky(inner, currentPackage);
        return mappedInner.includes(" ") ? `(Channel (${mappedInner}))` : `Channel ${mappedInner}`;
    }

    if (t.startsWith("func(")) {
        // Advanced: map func(A, B) C to A -> B -> C
        // Keep it simple for now, maybe map to Any
        // Or we can try to extract it if simple
        return "Any";
    }

    // Map package prefix to PascalCase
    const dotIdx = t.lastIndexOf(".");
    if (dotIdx !== -1) {
        const pkg = t.substring(0, dotIdx);
        const name = t.substring(dotIdx + 1);
        
        if (currentPackage && (pkg === currentPackage || pkg === currentPackage.split("/").pop())) {
            t = name;
        } else {
            const parts = pkg.split(/[\/\.]/);
            const skyPkg = parts.map(p => p.charAt(0).toUpperCase() + p.slice(1)).join(".");
            return skyPkg + "." + name.charAt(0).toUpperCase() + name.slice(1);
        }
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
