// src/interop/go/type-mapper.ts

const GO_PRIMITIVE_TYPES = new Set([
    "string", "bool", "byte", "rune",
    "int", "int8", "int16", "int32", "int64",
    "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
    "float32", "float64",
]);

/** True when the Go type is a pointer to a scalar primitive (e.g. *string, *int). */
export function isGoPointerToPrimitive(goType: string): boolean {
    if (!goType.startsWith("*")) return false;
    const inner = goType.replace(/^\*+/, "").trim();
    return GO_PRIMITIVE_TYPES.has(inner);
}

export function mapGoTypeToSky(goType: string, currentPackage?: string): string {
    // Pointer-to-primitive → Maybe T  (e.g. *string → Maybe String)
    if (isGoPointerToPrimitive(goType)) {
        const inner = goType.replace(/^\*+/, "").trim();
        const mapped = mapGoTypeToSkyInner(inner, currentPackage);
        return mapped.includes(" ") ? `Maybe (${mapped})` : `Maybe ${mapped}`;
    }

    return mapGoTypeToSkyInner(goType, currentPackage);
}

function mapGoTypeToSkyInner(goType: string, currentPackage?: string): string {
    let t = goType.replace(/^\*+/, "").trim(); // Remove pointers (opaque struct pointers)

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

    // Go generic type parameters: single uppercase letters (T, K, V, E)
    // or constrained types (~string, ~int). Map to the constraint's base type
    // or Any if unconstrained.
    if (/^[A-Z]$/.test(t)) return "Any";
    if (t.startsWith("~")) {
        const underlying = t.substring(1);
        if (GO_PRIMITIVE_TYPES.has(underlying)) return mapGoTypeToSkyInner(underlying, currentPackage);
        return "Any";
    }

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
        return "Any";
    }

    // Inline Go struct types (struct{Field Type "tag"; ...}) can't be
    // represented in Sky — map to opaque Foreign type.
    if (t.startsWith("struct{") || t.startsWith("struct {")) {
        return "Foreign";
    }

    // Strip generics [T] early — Go parameterized types (e.g. iter.Seq[string])
    // can't be represented in Sky's type system, so map them to Foreign.
    const bracketIdx = t.indexOf("[");
    if (bracketIdx !== -1) {
        return "Foreign";
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
            const skyPkg = parts.map(p =>
                p.split("-").map(w => w.charAt(0).toUpperCase() + w.slice(1)).join("")
            ).join(".");
            return skyPkg + "." + name.charAt(0).toUpperCase() + name.slice(1);
        }
    }

    // Single uppercase letter = Go generic type parameter (T, K, V, E, etc.)
    // Map to Foreign since we can't resolve the concrete type
    if (/^[A-Z]$/.test(t)) {
        return "Foreign";
    }

    // Ensure uppercase so it's a TypeReference, not a TypeVariable!
    return t.charAt(0).toUpperCase() + t.slice(1);
}

// Sky reserved words that cannot be used as identifiers
const SKY_RESERVED_WORDS = new Set([
    "type", "module", "import", "exposing", "as", "if", "then", "else",
    "case", "of", "let", "in", "where", "port", "foreign",
]);

export function lowerCamelCase(s: string): string {
    if (s.length === 0) return s;
    const result = s.charAt(0).toLowerCase() + s.slice(1);
    // Append underscore if the name clashes with a Sky keyword
    if (SKY_RESERVED_WORDS.has(result)) return result + "_";
    return result;
}
