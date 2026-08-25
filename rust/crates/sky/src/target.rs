//! The `--target family[:variant]` axis — one extendible target model where a
//! *variant* exists only under its *family*, so invalid combinations cannot be
//! constructed. See `docs/design/unified-app-builder.md`.
//!
//! # The model
//!
//! A target is `family[:variant]`, where `variant` is the single irreducible
//! choice that family can't infer for you:
//!
//! | family     | variant means | values                     | bare family      |
//! |------------|---------------|----------------------------|------------------|
//! | `web`      | execution     | `app`                      | server-driven    |
//! | `desktop`  | OS            | `mac` · `windows` · `linux`| host OS          |
//! | `tablet`   | OS            | `ipad` · `android` · `win` | responsive       |
//! | `mobile`   | OS            | `ios` · `android`          | (platform req'd) |
//! | `terminal` | renderer      | `tui` · `cli`              | `tui`            |
//!
//! Because a variant is spelled only under its family, `web:ios` and
//! `terminal:mac` are impossible — [`Target::parse`] rejects them at parse time
//! with a did-you-mean pointing at the family the variant *does* belong to.
//!
//! # Phase status
//!
//! This module is the grammar + validation + errors, and a mapping onto today's
//! spa **frontend-shell** strings ([`Target::frontend_shell`]) so the existing
//! `sky build` / `sky spa-split` `--target` sites can adopt it without changing
//! shipped behaviour. The `web` server/client *semantic flip* (design: bare
//! `web` = server-driven Sky.Live, `web:app` = client Sky.Spa) lands later with
//! `Std.App`; here both map to today's web frontend shell.

/// A build/run target: a family plus its one irreducible variant.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Target {
    /// `web` — server-driven HTML + SSE (the website). = Sky.Live.
    Web,
    /// `web:app` — client wasm, auto-split, backend derived. = Sky.Spa.
    WebApp,
    /// `desktop[:mac|windows|linux]` — native window. = Sky.Webview.
    Desktop(DesktopOs),
    /// `tablet[:ipad|android|windows]` — on-device wasm shell.
    Tablet(TabletOs),
    /// `mobile:ios|android` — on-device wasm shell.
    Mobile(MobileOs),
    /// `terminal[:tui|cli]` — local process. = Sky.Tui / Sky.Cli.
    Terminal(TermRenderer),
}

/// Desktop platform. Bare `desktop` = [`DesktopOs::Host`] (build for the host).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DesktopOs {
    Host,
    Mac,
    Windows,
    Linux,
}

/// Tablet platform. Bare `tablet` = [`TabletOs::Any`] (responsive web shell,
/// matching today's `tablet` == responsive web).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TabletOs {
    Any,
    Ipad,
    Android,
    Windows,
}

/// Mobile platform. There is no sensible bare default — a phone build must name
/// its store — so bare `mobile` is a parse error.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MobileOs {
    Ios,
    Android,
}

/// Terminal renderer. Bare `terminal` = [`TermRenderer::Tui`] (the interactive
/// full-screen default).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TermRenderer {
    Tui,
    Cli,
}

/// Every family name, for error messages + did-you-mean.
const FAMILIES: &[&str] = &["web", "desktop", "tablet", "mobile", "terminal"];

impl Target {
    /// Parse a `--target` value. On error, the `String` is a complete,
    /// user-facing, possibly multi-line message (same register as the existing
    /// CLI diagnostics) — print it and exit.
    ///
    /// Accepts the `family[:variant]` grammar plus the **legacy flat spellings**
    /// (`ios`, `android`) that shipped before this model, mapped to their
    /// `mobile:*` equivalents, so existing scripts keep working.
    pub fn parse(raw: &str) -> Result<Target, String> {
        let s = raw.trim().to_ascii_lowercase();
        if s.is_empty() {
            return Err(unknown_family(""));
        }

        // Legacy flat spellings (pre-family model): `ios` / `android` were
        // top-level targets; today they mean `mobile:ios` / `mobile:android`.
        match s.as_str() {
            "ios" => return Ok(Target::Mobile(MobileOs::Ios)),
            "android" => return Ok(Target::Mobile(MobileOs::Android)),
            _ => {}
        }

        let (family, variant) = match s.split_once(':') {
            Some((f, v)) => (f, Some(v)),
            None => (s.as_str(), None),
        };

        match family {
            "web" => match variant {
                None => Ok(Target::Web),
                Some("app") => Ok(Target::WebApp),
                Some(v) => Err(bad_variant("web", v, &["app"])),
            },
            "desktop" => match variant {
                None => Ok(Target::Desktop(DesktopOs::Host)),
                Some("mac") => Ok(Target::Desktop(DesktopOs::Mac)),
                Some("windows") => Ok(Target::Desktop(DesktopOs::Windows)),
                Some("linux") => Ok(Target::Desktop(DesktopOs::Linux)),
                Some(v) => Err(bad_variant("desktop", v, &["mac", "windows", "linux"])),
            },
            "tablet" => match variant {
                None => Ok(Target::Tablet(TabletOs::Any)),
                Some("ipad") => Ok(Target::Tablet(TabletOs::Ipad)),
                Some("android") => Ok(Target::Tablet(TabletOs::Android)),
                Some("windows") => Ok(Target::Tablet(TabletOs::Windows)),
                Some(v) => Err(bad_variant("tablet", v, &["ipad", "android", "windows"])),
            },
            "mobile" => match variant {
                None => Err(format!(
                    "sky --target: `mobile` needs a platform\n  \
                     use one of: mobile:ios · mobile:android"
                )),
                Some("ios") => Ok(Target::Mobile(MobileOs::Ios)),
                Some("android") => Ok(Target::Mobile(MobileOs::Android)),
                Some(v) => Err(bad_variant("mobile", v, &["ios", "android"])),
            },
            "terminal" => match variant {
                None => Ok(Target::Terminal(TermRenderer::Tui)),
                Some("tui") => Ok(Target::Terminal(TermRenderer::Tui)),
                Some("cli") => Ok(Target::Terminal(TermRenderer::Cli)),
                Some(v) => Err(bad_variant("terminal", v, &["tui", "cli"])),
            },
            other => Err(unknown_family(other)),
        }
    }

    /// The legacy frontend-shell string this target maps to for the spa
    /// auto-split (`web` / `desktop` / `ios` / `android` / `tablet`), or `None`
    /// for a target that is **not a frontend shell** (`terminal`) — the caller
    /// gives a context-appropriate error for `None`.
    ///
    /// `Web` and `WebApp` both map to `"web"` for now (the server/client flip is
    /// a later phase); the spa path is inherently the client build.
    pub fn frontend_shell(self) -> Option<&'static str> {
        match self {
            Target::Web | Target::WebApp => Some("web"),
            Target::Desktop(_) => Some("desktop"),
            Target::Tablet(_) => Some("tablet"),
            Target::Mobile(MobileOs::Ios) => Some("ios"),
            Target::Mobile(MobileOs::Android) => Some("android"),
            Target::Terminal(_) => None,
        }
    }

    /// The canonical `family[:variant]` spelling, for echoing back in messages.
    pub fn canonical(self) -> String {
        match self {
            Target::Web => "web".into(),
            Target::WebApp => "web:app".into(),
            Target::Desktop(DesktopOs::Host) => "desktop".into(),
            Target::Desktop(DesktopOs::Mac) => "desktop:mac".into(),
            Target::Desktop(DesktopOs::Windows) => "desktop:windows".into(),
            Target::Desktop(DesktopOs::Linux) => "desktop:linux".into(),
            Target::Tablet(TabletOs::Any) => "tablet".into(),
            Target::Tablet(TabletOs::Ipad) => "tablet:ipad".into(),
            Target::Tablet(TabletOs::Android) => "tablet:android".into(),
            Target::Tablet(TabletOs::Windows) => "tablet:windows".into(),
            Target::Mobile(MobileOs::Ios) => "mobile:ios".into(),
            Target::Mobile(MobileOs::Android) => "mobile:android".into(),
            Target::Terminal(TermRenderer::Tui) => "terminal:tui".into(),
            Target::Terminal(TermRenderer::Cli) => "terminal:cli".into(),
        }
    }
}

/// Which family, if any, claims `variant` as one of its values — powers the
/// cross-family did-you-mean ("`ios` is a platform of `mobile`").
fn family_owning_variant(variant: &str) -> Option<&'static str> {
    match variant {
        "app" => Some("web"),
        "mac" | "linux" => Some("desktop"),
        "ipad" => Some("tablet"),
        "ios" => Some("mobile"),
        "tui" | "cli" => Some("terminal"),
        // `android` (mobile+tablet) and `windows` (desktop+tablet) are shared —
        // no single owner to suggest.
        _ => None,
    }
}

fn bad_variant(family: &str, variant: &str, valid: &[&str]) -> String {
    let mut msg = format!(
        "sky --target: `{variant}` is not a variant of `{family}`\n  \
         `{family}` accepts: {}",
        valid.join(" · ")
    );
    if let Some(owner) = family_owning_variant(variant) {
        if owner != family {
            msg.push_str(&format!("\n  did you mean `{owner}:{variant}`?"));
        }
    }
    msg
}

fn unknown_family(family: &str) -> String {
    let mut msg = format!("sky --target: unknown target `{family}`");
    // If the whole token is actually a known variant, point at its family.
    if let Some(owner) = family_owning_variant(family) {
        msg.push_str(&format!("\n  did you mean `{owner}:{family}`?"));
    }
    msg.push_str(&format!("\n  supported families: {}", FAMILIES.join(" · ")));
    msg
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_valid_target_parses_to_the_expected_variant() {
        let cases = [
            ("web", Target::Web),
            ("web:app", Target::WebApp),
            ("desktop", Target::Desktop(DesktopOs::Host)),
            ("desktop:mac", Target::Desktop(DesktopOs::Mac)),
            ("desktop:windows", Target::Desktop(DesktopOs::Windows)),
            ("desktop:linux", Target::Desktop(DesktopOs::Linux)),
            ("tablet", Target::Tablet(TabletOs::Any)),
            ("tablet:ipad", Target::Tablet(TabletOs::Ipad)),
            ("tablet:android", Target::Tablet(TabletOs::Android)),
            ("tablet:windows", Target::Tablet(TabletOs::Windows)),
            ("mobile:ios", Target::Mobile(MobileOs::Ios)),
            ("mobile:android", Target::Mobile(MobileOs::Android)),
            ("terminal", Target::Terminal(TermRenderer::Tui)),
            ("terminal:tui", Target::Terminal(TermRenderer::Tui)),
            ("terminal:cli", Target::Terminal(TermRenderer::Cli)),
        ];
        for (raw, want) in cases {
            assert_eq!(Target::parse(raw), Ok(want), "parsing {raw:?}");
        }
    }

    #[test]
    fn canonical_roundtrips_every_target() {
        for (raw, want) in [
            ("web", Target::Web),
            ("web:app", Target::WebApp),
            ("desktop", Target::Desktop(DesktopOs::Host)),
            ("desktop:mac", Target::Desktop(DesktopOs::Mac)),
            ("mobile:ios", Target::Mobile(MobileOs::Ios)),
            ("terminal:cli", Target::Terminal(TermRenderer::Cli)),
        ] {
            assert_eq!(want.canonical(), raw);
            assert_eq!(Target::parse(&want.canonical()), Ok(want));
        }
    }

    #[test]
    fn legacy_flat_ios_android_map_to_mobile() {
        assert_eq!(Target::parse("ios"), Ok(Target::Mobile(MobileOs::Ios)));
        assert_eq!(
            Target::parse("android"),
            Ok(Target::Mobile(MobileOs::Android))
        );
    }

    #[test]
    fn input_is_trimmed_and_case_folded() {
        assert_eq!(Target::parse("  WEB:APP  "), Ok(Target::WebApp));
        assert_eq!(
            Target::parse("Mobile:iOS"),
            Ok(Target::Mobile(MobileOs::Ios))
        );
    }

    #[test]
    fn a_variant_under_the_wrong_family_is_rejected_with_a_cross_family_hint() {
        // `ios` belongs to `mobile`, not `web`.
        let err = Target::parse("web:ios").unwrap_err();
        assert!(err.contains("not a variant of `web`"), "{err}");
        assert!(err.contains("did you mean `mobile:ios`?"), "{err}");

        // `mac` belongs to `desktop`, not `terminal`.
        let err = Target::parse("terminal:mac").unwrap_err();
        assert!(err.contains("did you mean `desktop:mac`?"), "{err}");
    }

    #[test]
    fn a_bare_variant_typed_as_a_family_points_at_its_family() {
        // A user types `app` meaning `web:app`.
        let err = Target::parse("app").unwrap_err();
        assert!(err.contains("did you mean `web:app`?"), "{err}");
        // `tui` alone → `terminal:tui`.
        let err = Target::parse("tui").unwrap_err();
        assert!(err.contains("did you mean `terminal:tui`?"), "{err}");
    }

    #[test]
    fn bare_mobile_demands_a_platform() {
        let err = Target::parse("mobile").unwrap_err();
        assert!(err.contains("needs a platform"), "{err}");
        assert!(err.contains("mobile:ios"), "{err}");
        assert!(err.contains("mobile:android"), "{err}");
    }

    #[test]
    fn unknown_family_lists_the_families() {
        let err = Target::parse("wat").unwrap_err();
        assert!(err.contains("unknown target `wat`"), "{err}");
        for fam in FAMILIES {
            assert!(err.contains(fam), "families list missing {fam}: {err}");
        }
    }

    #[test]
    fn an_unknown_variant_lists_the_valid_ones() {
        let err = Target::parse("desktop:solaris").unwrap_err();
        assert!(err.contains("not a variant of `desktop`"), "{err}");
        assert!(
            err.contains("mac") && err.contains("windows") && err.contains("linux"),
            "{err}"
        );
    }

    #[test]
    fn frontend_shell_maps_families_and_excludes_terminal() {
        assert_eq!(Target::Web.frontend_shell(), Some("web"));
        assert_eq!(Target::WebApp.frontend_shell(), Some("web"));
        assert_eq!(
            Target::Desktop(DesktopOs::Mac).frontend_shell(),
            Some("desktop")
        );
        assert_eq!(Target::Mobile(MobileOs::Ios).frontend_shell(), Some("ios"));
        assert_eq!(
            Target::Mobile(MobileOs::Android).frontend_shell(),
            Some("android")
        );
        assert_eq!(
            Target::Tablet(TabletOs::Any).frontend_shell(),
            Some("tablet")
        );
        assert_eq!(Target::Terminal(TermRenderer::Tui).frontend_shell(), None);
        assert_eq!(Target::Terminal(TermRenderer::Cli).frontend_shell(), None);
    }

    #[test]
    fn every_pre_model_flat_target_still_parses() {
        // The exact set the old `matches!` allowlist accepted must remain valid,
        // so no shipped invocation breaks.
        for legacy in ["web", "desktop", "ios", "android", "tablet"] {
            assert!(
                Target::parse(legacy).is_ok(),
                "legacy target {legacy} must parse"
            );
        }
    }
}
