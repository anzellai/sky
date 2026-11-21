package i18n

import "strings"

// pseudoMap gives each ASCII letter an accented form that stays readable.
var pseudoMap = map[rune]rune{
	'a': 'á', 'b': 'ƀ', 'c': 'ç', 'd': 'ð', 'e': 'é', 'f': 'ƒ', 'g': 'ĝ', 'h': 'ĥ', 'i': 'í', 'j': 'ĵ',
	'k': 'ķ', 'l': 'ļ', 'm': 'ɱ', 'n': 'ñ', 'o': 'ö', 'p': 'þ', 'q': 'ǫ', 'r': 'ŕ', 's': 'š', 't': 'ţ',
	'u': 'û', 'v': 'ṽ', 'w': 'ŵ', 'x': 'ẋ', 'y': 'ý', 'z': 'ž',
	'A': 'Å', 'B': 'Ɓ', 'C': 'Ç', 'D': 'Ð', 'E': 'É', 'F': 'Ƒ', 'G': 'Ĝ', 'H': 'Ĥ', 'I': 'Î', 'J': 'Ĵ',
	'K': 'Ķ', 'L': 'Ļ', 'M': 'Ṁ', 'N': 'Ñ', 'O': 'Ö', 'P': 'Þ', 'Q': 'Ǫ', 'R': 'Ŕ', 'S': 'Š', 'T': 'Ţ',
	'U': 'Û', 'V': 'Ṽ', 'W': 'Ŵ', 'X': 'Ẋ', 'Y': 'Ý', 'Z': 'Ž',
}

// Pseudo gives the en-XA form of a message: each pattern's text accented,
// made about 40 percent longer, and put in brackets, so a layout test shows
// clipped text, hard-coded strings and concatenation. Placeholders and the
// message's structure do not change.
func Pseudo(m *Message) *Message {
	out := *m
	out.Pattern = pseudoPattern(m.Pattern)
	out.Variants = make([]Variant, len(m.Variants))
	for i, v := range m.Variants {
		out.Variants[i] = Variant{Keys: v.Keys, Pattern: pseudoPattern(v.Pattern)}
	}
	return &out
}

func pseudoPattern(p Pattern) Pattern {
	letters := 0
	var res Pattern
	res = append(res, Part{Text: "["})
	for _, part := range p {
		if part.Expr != nil {
			res = append(res, part)
			continue
		}
		var b strings.Builder
		for _, r := range part.Text {
			if a, ok := pseudoMap[r]; ok {
				b.WriteRune(a)
				letters++
			} else {
				b.WriteRune(r)
			}
		}
		res = append(res, Part{Text: b.String()})
	}
	pad := (letters*2 + 4) / 5
	if pad > 0 {
		res = append(res, Part{Text: " " + strings.Repeat("·", pad)})
	}
	res = append(res, Part{Text: "]"})
	return res
}
