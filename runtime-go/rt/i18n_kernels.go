//go:build sky_i18n

package rt

// Std.I18n and Std.I18n.Format kernels. The engine is the package rt/i18n.
//
// This file has the build tag sky_i18n. The compiler sets the tag when a
// program imports Std.I18n or Std.I18n.Format, or when sky.toml has an
// [i18n] section, so a program that does not use the module links neither
// the engine nor golang.org/x/text/language. The CLDR data of each locale is
// behind its own tag (sky_cldr_<tag>), which the compiler sets for the
// locales of [i18n] locales.
//
// Every kernel is pure. Each takes and gives plain strings, ints, bools and
// lists, and Std.I18n wraps them in its opaque types.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"sky-app/rt/i18n"
)

const i18nDomain = "i18n"

// i18nErr turns an engine error into a Sky Error with the domain "i18n".
func i18nErr(where string, err error) any {
	code := "InvalidInput"
	var nc *i18n.NotCompiledError
	var ul *i18n.UnknownLocaleError
	var il *i18n.InvalidLocaleError
	var de *i18n.DateError
	var oe *i18n.OverrideError
	switch {
	case errors.As(err, &nc):
		code = "LocaleNotCompiled"
	case errors.As(err, &ul):
		code = "UnknownLocale"
	case errors.As(err, &il):
		code = "InvalidLocale"
	case errors.As(err, &de):
		code = "InvalidDate"
	case errors.As(err, &oe):
		code = "InvalidMessage"
	}
	f := DomainFailure{Domain: i18nDomain, Code: code, RetryAfterMs: -1}
	return Err[any, any](ErrDomain(7, where+": "+err.Error(), f))
}

// i18nLocale reads a canonical tag that Std.I18n made. A tag that no longer
// parses (impossible from Sky code) reads as the default locale.
func i18nLocale(v any) i18n.Locale {
	l, err := i18n.ParseLocale(AsString(v))
	if err != nil {
		return i18n.Installed().Default
	}
	return l
}

func i18nStrings(v any) []string {
	xs := AsList(v)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, AsString(x))
	}
	return out
}

func i18nTriples(v any) [][3]string {
	xs := AsList(v)
	out := make([][3]string, 0, len(xs))
	for _, x := range xs {
		t := AsTuple3(x)
		out = append(out, [3]string{AsString(t.V0), AsString(t.V1), AsString(t.V2)})
	}
	return out
}

func i18nTags(ls []i18n.Locale) []any {
	out := make([]any, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Tag)
	}
	return out
}

func i18nDecimal(where string, s any) (decimal.Decimal, any) {
	d, err := decimal.NewFromString(AsString(s))
	if err != nil {
		return d, Err[any, any](ErrInvalidInput(fmt.Sprintf("%s: %q is not a decimal", where, AsString(s))))
	}
	return d, nil
}

func init() {
	i18n.SetLogger(func(msg string) { logEmit(logLevelWarn, "warn", msg, nil) })

	RegisterPure("I18n_parse", func(args []any) any {
		l, err := i18n.ParseLocale(AsString(args[0]))
		if err != nil {
			return i18nErr("I18n.locale", err)
		}
		return Ok[any, any](l.Tag)
	})
	// I18n_decimalText : Decimal -> String, keeping its visible fraction
	// digits ("1.0" stays "1.0"; Decimal.toString gives "1"). CLDR plural
	// rules read them: in English "1" is one and "1.0" is other.
	RegisterPure("I18n_decimalText", func(args []any) any {
		d := decimalUnbox(args[0])
		if e := d.Exponent(); e < 0 {
			return d.StringFixed(-e)
		}
		return d.String()
	})
	RegisterPure("I18n_language", func(args []any) any { return i18nLocale(args[0]).Language })
	RegisterPure("I18n_script", func(args []any) any { return i18n.ScriptOf(i18nLocale(args[0])) })
	RegisterPure("I18n_region", func(args []any) any { return i18nLocale(args[0]).Region })
	RegisterPure("I18n_rtl", func(args []any) any { return i18n.RightToLeft(i18nLocale(args[0])) })
	RegisterPure("I18n_defaultLocale", func(args []any) any { return i18n.Installed().Default.Tag })
	RegisterPure("I18n_locales", func(args []any) any { return i18nTags(i18n.Installed().Locales) })
	RegisterPure("I18n_fallbacks", func(args []any) any {
		return i18nTags(i18n.Fallbacks(i18nLocale(args[0]), i18n.Installed().Default))
	})
	RegisterPure("I18n_negotiate", func(args []any) any {
		var sup []i18n.Locale
		for _, s := range i18nStrings(args[0]) {
			sup = append(sup, i18nLocale(s))
		}
		return i18n.Negotiate(sup, AsString(args[1]), i18n.Installed().Default).Tag
	})
	// I18n_plural : tag -> number text -> ordinal -> category index (0 zero … 5 other)
	RegisterPure("I18n_plural", func(args []any) any {
		l := i18nLocale(args[0])
		var c i18n.PluralCategory
		var err error
		if AsBool(args[2]) {
			c, err = i18n.Ordinal(l, AsString(args[1]))
		} else {
			c, err = i18n.Cardinal(l, AsString(args[1]))
		}
		if err != nil {
			return int(i18n.Other)
		}
		return int(c)
	})
	RegisterPure("I18n_checkOverrides", func(args []any) any {
		if _, err := i18n.NewOverrides(i18nTriples(args[0])); err != nil {
			return i18nErr("I18n.overrides", err)
		}
		return Ok[any, any](struct{}{})
	})
	// I18n_render : overrides -> tag -> key -> args (name, kind, payload) -> String
	RegisterPure("I18n_render", func(args []any) any {
		i18n.SetProduction(i18nProduction())
		var ov *i18n.Overrides
		if entries := i18nTriples(args[0]); len(entries) > 0 {
			// Overrides were checked when they were made (I18n.overrides).
			ov, _ = i18n.NewOverrides(entries)
		}
		a := i18n.Args{}
		for _, t := range i18nTriples(args[3]) {
			k := t[1]
			if len(k) != 1 {
				continue
			}
			a[t[0]] = i18n.Arg{Kind: i18n.ArgKind(k[0]), Payload: t[2]}
		}
		return i18n.Installed().Render(ov, i18nLocale(args[1]), AsString(args[2]), a)
	})

	// ── Std.I18n.Format ────────────────────────────────────────

	// I18n_formatNumber : kind -> tag -> payload -> Result Error String
	RegisterPure("I18n_formatNumber", func(args []any) any {
		l := i18nLocale(args[1])
		payload := AsString(args[2])
		var s string
		var err error
		switch AsString(args[0]) {
		case "int":
			n, perr := strconv.ParseInt(payload, 10, 64)
			if perr != nil {
				return Err[any, any](ErrInvalidInput("Format.int: " + perr.Error()))
			}
			s, err = i18n.FormatInt(l, n)
		case "decimal":
			d, bad := i18nDecimal("Format.decimal", payload)
			if bad != nil {
				return bad
			}
			s, err = i18n.FormatDecimal(l, d)
		case "percent":
			d, bad := i18nDecimal("Format.percent", payload)
			if bad != nil {
				return bad
			}
			s, err = i18n.FormatPercent(l, d)
		case "money":
			code, amt, _ := strings.Cut(payload, " ")
			d, bad := i18nDecimal("Format.money", amt)
			if bad != nil {
				return bad
			}
			s, err = i18n.FormatMoney(l, d, code)
		}
		if err != nil {
			return i18nErr("Format."+AsString(args[0]), err)
		}
		return Ok[any, any](s)
	})
	RegisterPure("I18n_formatDate", func(args []any) any {
		st, err := i18n.ParseStyle(AsString(args[1]))
		if err == nil {
			var s string
			s, err = i18n.FormatDate(i18nLocale(args[0]), st, AsString(args[2]))
			if err == nil {
				return Ok[any, any](s)
			}
		}
		return i18nErr("Format.date", err)
	})
	// I18n_formatTime : which ("time" | "dateTime") -> tag -> style -> zone -> ms
	RegisterPure("I18n_formatTime", func(args []any) any {
		st, err := i18n.ParseStyle(AsString(args[2]))
		if err == nil {
			l, zone, ms := i18nLocale(args[1]), AsString(args[3]), int64(AsInt(args[4]))
			var s string
			if AsString(args[0]) == "time" {
				s, err = i18n.FormatTime(l, st, zone, ms)
			} else {
				s, err = i18n.FormatDateTime(l, st, zone, ms)
			}
			if err == nil {
				return Ok[any, any](s)
			}
		}
		return i18nErr("Format."+AsString(args[0]), err)
	})
	RegisterPure("I18n_formatList", func(args []any) any {
		s, err := i18n.FormatList(i18nLocale(args[0]), i18n.ListStyle(AsString(args[1])), i18nStrings(args[2]))
		if err != nil {
			return i18nErr("Format.list", err)
		}
		return Ok[any, any](s)
	})
	// I18n_formatRelative : tag -> unit -> value -> auto
	RegisterPure("I18n_formatRelative", func(args []any) any {
		u, err := i18n.ParseRelUnit(AsString(args[1]))
		if err == nil {
			var s string
			s, err = i18n.FormatRelative(i18nLocale(args[0]), u, int64(AsInt(args[2])), AsBool(args[3]))
			if err == nil {
				return Ok[any, any](s)
			}
		}
		return i18nErr("Format.relative", err)
	})
}
