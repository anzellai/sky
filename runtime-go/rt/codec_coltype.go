package rt

import "strings"

// Codec_colTypeKind is the colspec kind of a Std.Codec ColType: `text`,
// `int`, `real`, `bool`, `blob`, `decimal`, `decimal|money=GBP` (CMoneyIn),
// with `?` before any `|flag` for CNull. Std.Codec.colTypeKind and
// Std.Db.Store's column spec read it here: ColType is a stdlib ADT, and a
// Sky `case` over its payloads (CNull inner, CMoneyIn code) paid a runtime
// narrowing per payload at every build (doc 14 §4).
func Codec_colTypeKind(ct any) any {
	return codecColTypeKind(ct)
}

func codecColTypeKind(ct any) string {
	var adt SkyADT
	switch v := ct.(type) {
	case SkyADT:
		adt = v
	case *SkyADT:
		if v == nil {
			return "blob"
		}
		adt = *v
	default:
		return "blob"
	}
	switch adt.SkyName {
	case "CText":
		return "text"
	case "CInt":
		return "int"
	case "CReal":
		return "real"
	case "CBool":
		return "bool"
	case "CDecimal":
		return "decimal"
	case "CMoneyIn":
		if len(adt.Fields) == 1 {
			if code, ok := adt.Fields[0].(string); ok {
				return "decimal|money=" + code
			}
		}
		return "decimal"
	case "CNull":
		if len(adt.Fields) == 1 {
			inner := codecColTypeKind(adt.Fields[0])
			if i := strings.IndexByte(inner, '|'); i >= 0 {
				return inner[:i] + "?" + inner[i:]
			}
			return inner + "?"
		}
	}
	return "blob"
}
