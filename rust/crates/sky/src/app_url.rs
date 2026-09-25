//! The backend address a native client shell loads.
//!
//! A Sky.Spa client built for `mobile:ios`, `mobile:android` or a `desktop`
//! shell is a thin native web view over the wasm client, which the backend
//! serves over HTTP. The shell must know that backend's address, and for the
//! phone shells it must know it at BUILD time: a phone cannot read the build
//! machine's environment, and it cannot reach the build machine's `localhost`.
//!
//! Resolution order (first wins):
//!
//! 1. `SKY_APP_URL` in the build environment;
//! 2. `App.withAppUrl "<url>"` on the entry's `App` value, read statically by
//!    the build (`project::app_entry::builder_string_arg`);
//! 3. the default: `http://localhost:<PORT>/` (iOS simulator, which shares the
//!    host network), `http://10.0.2.2:<PORT>/` (Android emulator's alias for the
//!    host), `http://127.0.0.1:<PORT>/` (desktop), with `PORT` read at build
//!    time and 8951 when it is unset or not a port.
//!
//! A set value must be an absolute `http://` or `https://` URL with a host. It
//! is normalised (scheme and host lower-cased, a trailing `/` added to a bare
//! path) and baked into the shell source. The desktop shell can also read
//! `SKY_APP_URL` at run time, because it runs on the machine that sets it.

pub const ENV_VAR: &str = "SKY_APP_URL";
pub const BUILDER: &str = "App.withAppUrl";
/// Internal flag the Std.App build passes to its generated frontend leg, which
/// no longer has the user's entry to read the builder from.
pub const BUILDER_FLAG: &str = "--builder-app-url=";
pub const DEFAULT_PORT: u16 = 8951;

/// The native shell that embeds the backend address.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Shell {
    Ios,
    Android,
    Desktop,
}

impl Shell {
    /// The shell for a frontend-shell name (`target::Target::frontend_shell`).
    /// `web` and `tablet` stage a web bundle only and embed no address.
    pub fn from_frontend_shell(s: &str) -> Option<Shell> {
        match s {
            "ios" => Some(Shell::Ios),
            "android" => Some(Shell::Android),
            "desktop" => Some(Shell::Desktop),
            _ => None,
        }
    }

    fn default_host(self) -> &'static str {
        match self {
            Shell::Ios => "localhost",
            Shell::Android => "10.0.2.2",
            Shell::Desktop => "127.0.0.1",
        }
    }

    fn label(self) -> &'static str {
        match self {
            Shell::Ios => "iOS",
            Shell::Android => "Android",
            Shell::Desktop => "desktop",
        }
    }
}

/// Where the resolved address came from.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Source {
    Env,
    Builder,
    Default { port: u16 },
}

/// A resolved, validated backend address.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AppUrl {
    /// The normalised URL the shell loads.
    pub url: String,
    /// `http` or `https`.
    pub scheme: String,
    /// The host, lower-cased (an IPv6 literal keeps its brackets).
    pub host: String,
    pub source: Source,
    pub shell: Shell,
}

impl AppUrl {
    /// The label of the source, as the build summary prints it.
    pub fn source_label(&self) -> String {
        match &self.source {
            Source::Env => ENV_VAR.to_string(),
            Source::Builder => BUILDER.to_string(),
            Source::Default { port } => format!("default, PORT={port}"),
        }
    }

    /// `loads <url> (<source>)` for the build summary.
    pub fn summary(&self) -> String {
        format!("loads {} ({})", self.url, self.source_label())
    }

    /// True for a host the development defaults already cover.
    pub fn is_local(&self) -> bool {
        matches!(
            self.host.as_str(),
            "localhost" | "127.0.0.1" | "10.0.2.2" | "[::1]"
        )
    }

    /// The host that needs a cleartext exception: plain `http` to a host that
    /// is not local. `None` for `https` and for the local development hosts.
    pub fn cleartext_host(&self) -> Option<&str> {
        if self.scheme == "http" && !self.is_local() {
            Some(self.host.trim_start_matches('[').trim_end_matches(']'))
        } else {
            None
        }
    }

    /// The warning a plain-http remote address earns.
    pub fn cleartext_warning(&self) -> Option<String> {
        let host = self.cleartext_host()?;
        Some(format!(
            "warning: the {} shell loads plain http from `{host}` ({}). The build permits \
             cleartext for exactly that host so the app can load it, but traffic is not \
             encrypted. A production device build should use an https:// address.",
            self.shell.label(),
            self.source_label()
        ))
    }
}

/// `PORT` as the generated backend reads it: a port number, else 8951.
pub fn parse_port(port: Option<&str>) -> u16 {
    port.and_then(|p| p.trim().parse::<u16>().ok())
        .filter(|p| *p != 0)
        .unwrap_or(DEFAULT_PORT)
}

/// Resolve the address for `shell` from the build environment's
/// `SKY_APP_URL` (`env`), the statically read builder value (`builder`) and
/// the build environment's `PORT` (`port`).
pub fn resolve(
    shell: Shell,
    env: Option<&str>,
    builder: Option<&str>,
    port: Option<&str>,
) -> Result<AppUrl, String> {
    if let Some(v) = env {
        return validate(v, Source::Env, shell);
    }
    if let Some(v) = builder {
        return validate(v, Source::Builder, shell);
    }
    let port = parse_port(port);
    let host = shell.default_host();
    Ok(AppUrl {
        url: format!("http://{host}:{port}/"),
        scheme: "http".to_string(),
        host: host.to_string(),
        source: Source::Default { port },
        shell,
    })
}

/// [`resolve`] against this process's environment.
pub fn resolve_from_process(shell: Shell, builder: Option<&str>) -> Result<AppUrl, String> {
    let env = match std::env::var(ENV_VAR) {
        Ok(v) => Some(v),
        Err(std::env::VarError::NotPresent) => None,
        Err(std::env::VarError::NotUnicode(_)) => {
            return Err(format!("{ENV_VAR} is not valid UTF-8"))
        }
    };
    let port = std::env::var("PORT").ok();
    resolve(shell, env.as_deref(), builder, port.as_deref())
}

/// Characters a URL must percent-encode, refused rather than guessed at.
fn is_refused_char(c: char) -> bool {
    c.is_whitespace()
        || c.is_control()
        || matches!(c, '"' | '<' | '>' | '\\' | '^' | '`' | '{' | '}' | '|')
}

fn validate(raw: &str, source: Source, shell: Shell) -> Result<AppUrl, String> {
    let origin = match source {
        Source::Env => ENV_VAR,
        _ => BUILDER,
    };
    let refuse = |why: String| {
        Err(format!(
            "{origin} = {raw:?} is not a backend address the native shell can load: {why}. \
             Give an absolute http:// or https:// URL with a host, for example \
             \"https://example.test/\"."
        ))
    };
    let t = raw.trim();
    if t.is_empty() {
        return refuse("it is empty".to_string());
    }
    if let Some(c) = t.chars().find(|c| is_refused_char(*c)) {
        return refuse(format!(
            "it contains {c:?}, which a URL must percent-encode"
        ));
    }
    let Some((scheme, rest)) = t.split_once("://") else {
        return refuse("it has no http:// or https:// scheme".to_string());
    };
    let scheme = scheme.to_ascii_lowercase();
    if scheme != "http" && scheme != "https" {
        return refuse(format!("the scheme `{scheme}` is not http or https"));
    }
    let auth_end = rest.find(['/', '?', '#']).unwrap_or(rest.len());
    let (authority, tail) = rest.split_at(auth_end);
    if authority.is_empty() {
        return refuse("it has no host".to_string());
    }
    if authority.contains('@') {
        return refuse(
            "it carries user credentials, which do not belong in an address baked into an app"
                .to_string(),
        );
    }
    let (host, port) = if let Some(after) = authority.strip_prefix('[') {
        let Some((inner, after_bracket)) = after.split_once(']') else {
            return refuse("its IPv6 host has no closing `]`".to_string());
        };
        if inner.is_empty()
            || !inner
                .chars()
                .all(|c| c.is_ascii_hexdigit() || c == ':' || c == '.')
        {
            return refuse(format!("`[{inner}]` is not an IPv6 address"));
        }
        let port = match after_bracket {
            "" => None,
            p => match p.strip_prefix(':') {
                Some(p) => Some(p),
                None => return refuse(format!("unexpected `{p}` after the host")),
            },
        };
        (format!("[{}]", inner.to_ascii_lowercase()), port)
    } else {
        let (h, p) = match authority.rsplit_once(':') {
            Some((h, p)) => (h, Some(p)),
            None => (authority, None),
        };
        if h.is_empty() {
            return refuse("it has no host".to_string());
        }
        if !h
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '.' || c == '_')
            || h.starts_with('.')
            || h.starts_with('-')
        {
            return refuse(format!("`{h}` is not a host name"));
        }
        (h.to_ascii_lowercase(), p)
    };
    let port_part = match port {
        None => String::new(),
        Some(p) => match p.parse::<u16>() {
            Ok(n) if n != 0 && p.chars().all(|c| c.is_ascii_digit()) => format!(":{n}"),
            _ => return refuse(format!("`{p}` is not a port number (1-65535)")),
        },
    };
    // Normalise the path: a bare origin gets `/`, and a path with no query or
    // fragment gets a trailing `/`, so the shell always loads a directory URL
    // the backend's relative asset paths resolve against.
    let tail = if tail.is_empty() {
        "/".to_string()
    } else if tail.starts_with('?') || tail.starts_with('#') {
        format!("/{tail}")
    } else if !tail.contains(['?', '#']) && !tail.ends_with('/') {
        format!("{tail}/")
    } else {
        tail.to_string()
    };
    Ok(AppUrl {
        url: format!("{scheme}://{host}{port_part}{tail}"),
        scheme,
        host,
        source,
        shell,
    })
}

/// A Swift string literal (quotes included).
pub fn swift_string_literal(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '\\' => out.push_str("\\\\"),
            '"' => out.push_str("\\\""),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c.is_control() => out.push_str(&format!("\\u{{{:x}}}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// A Java string literal (quotes included).
pub fn java_string_literal(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '\\' => out.push_str("\\\\"),
            '"' => out.push_str("\\\""),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c.is_control() => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// A Sky string literal (quotes included).
pub fn sky_string_literal(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '\\' => out.push_str("\\\\"),
            '"' => out.push_str("\\\""),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

fn xml_escape(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&apos;")
}

/// The `NSAppTransportSecurity` entry of the iOS `Info.plist`. Local
/// networking stays allowed (the simulator loads a host backend); a plain-http
/// remote host gets an `NSExceptionDomains` entry for exactly that host.
pub fn ios_ats_plist(u: &AppUrl) -> String {
    let mut out = String::from(
        "\n    <!-- Cleartext to local development hosts (the simulator loads a backend\n         \
         on the host). A production backend uses https. -->\n    \
         <key>NSAppTransportSecurity</key>\n    <dict>\n        \
         <key>NSAllowsLocalNetworking</key><true/>",
    );
    if let Some(host) = u.cleartext_host() {
        out.push_str(&format!(
            "\n        <!-- The backend address is plain http (App.withAppUrl / SKY_APP_URL):\n             \
             allow cleartext for exactly that host. -->\n        \
             <key>NSExceptionDomains</key>\n        <dict>\n            \
             <key>{h}</key>\n            <dict>\n                \
             <key>NSExceptionAllowsInsecureHTTPLoads</key><true/>\n                \
             <key>NSIncludesSubdomains</key><false/>\n            </dict>\n        </dict>",
            h = xml_escape(host)
        ));
    }
    out.push_str("\n    </dict>");
    out
}

/// The Android `<application>` cleartext attribute and, for a plain-http
/// remote host, the `res/xml/network_security_config.xml` that permits
/// cleartext for exactly that host. The development default (the emulator's
/// host alias, or another local host over http) keeps the global
/// `usesCleartextTraffic` flag; an https address permits no cleartext.
pub fn android_cleartext(u: &AppUrl) -> (String, Option<String>) {
    if let Some(host) = u.cleartext_host() {
        let cfg = format!(
            "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n\
             <!-- Generated by `sky build`: the backend address is plain http\n     \
             (App.withAppUrl / SKY_APP_URL), so cleartext is permitted for exactly\n     \
             that host. Every other host needs https. -->\n\
             <network-security-config>\n    \
             <domain-config cleartextTrafficPermitted=\"true\">\n        \
             <domain includeSubdomains=\"false\">{}</domain>\n    \
             </domain-config>\n\
             </network-security-config>\n",
            xml_escape(host)
        );
        (
            "\n        android:networkSecurityConfig=\"@xml/network_security_config\"".to_string(),
            Some(cfg),
        )
    } else if u.scheme == "http" {
        (
            "\n        android:usesCleartextTraffic=\"true\"".to_string(),
            None,
        )
    } else {
        (String::new(), None)
    }
}

/// The Sky expression the desktop shell uses as its built-in address: the
/// set URL as a literal, or — for the default — the loopback address on the
/// `PORT` read at run time (the build-time `PORT`, else 8951, as its default).
pub fn desktop_url_expr(u: &AppUrl) -> String {
    match &u.source {
        Source::Default { port } => format!(
            "\"http://{}:\" ++ System.getenvOr \"PORT\" \"{port}\" ++ \"/\"",
            Shell::Desktop.default_host()
        ),
        _ => sky_string_literal(&u.url),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ok(shell: Shell, env: Option<&str>, builder: Option<&str>, port: Option<&str>) -> AppUrl {
        resolve(shell, env, builder, port).unwrap_or_else(|e| panic!("resolve failed: {e}"))
    }

    // ---- resolution order: SKY_APP_URL > App.withAppUrl > default ----

    #[test]
    fn env_overrides_the_builder() {
        let u = ok(
            Shell::Ios,
            Some("https://env.example.test/"),
            Some("https://builder.example.test/"),
            None,
        );
        assert_eq!(u.url, "https://env.example.test/");
        assert_eq!(u.source, Source::Env);
        assert!(u.summary().contains("(SKY_APP_URL)"), "{}", u.summary());
    }

    #[test]
    fn the_builder_is_used_when_env_is_unset() {
        let u = ok(
            Shell::Android,
            None,
            Some("https://example.test/"),
            Some("8000"),
        );
        assert_eq!(u.url, "https://example.test/");
        assert_eq!(u.source, Source::Builder);
        assert_eq!(u.summary(), "loads https://example.test/ (App.withAppUrl)");
    }

    #[test]
    fn the_default_follows_port_per_shell() {
        let ios = ok(Shell::Ios, None, None, Some("8000"));
        assert_eq!(ios.url, "http://localhost:8000/");
        assert_eq!(ios.source, Source::Default { port: 8000 });
        assert!(
            ios.summary().contains("(default, PORT=8000)"),
            "{}",
            ios.summary()
        );
        let android = ok(Shell::Android, None, None, Some("8000"));
        assert_eq!(android.url, "http://10.0.2.2:8000/");
        let desktop = ok(Shell::Desktop, None, None, Some("8000"));
        assert_eq!(desktop.url, "http://127.0.0.1:8000/");
    }

    #[test]
    fn a_missing_or_bad_port_falls_back_to_8951() {
        assert_eq!(parse_port(None), 8951);
        assert_eq!(parse_port(Some("")), 8951);
        assert_eq!(parse_port(Some("abc")), 8951);
        assert_eq!(parse_port(Some("0")), 8951);
        assert_eq!(parse_port(Some(" 9000 ")), 9000);
        assert_eq!(
            ok(Shell::Ios, None, None, None).url,
            "http://localhost:8951/"
        );
        assert_eq!(
            ok(Shell::Android, None, None, None).url,
            "http://10.0.2.2:8951/"
        );
    }

    // ---- validation ----

    #[test]
    fn non_http_urls_are_refused_naming_the_source() {
        for bad in [
            "ftp://x",
            "not a url",
            "",
            "   ",
            "http://",
            "https:///path",
            "example.test",
        ] {
            let e = resolve(Shell::Ios, None, Some(bad), None).unwrap_err();
            assert!(e.contains("App.withAppUrl"), "builder {bad:?}: {e}");
            let e = resolve(Shell::Ios, Some(bad), None, None).unwrap_err();
            assert!(e.contains("SKY_APP_URL"), "env {bad:?}: {e}");
        }
    }

    #[test]
    fn malformed_authorities_are_refused() {
        for bad in [
            "https://user:pw@example.test/",
            "https://example.test:99999/",
            "https://example.test:abc/",
            "https://exa mple.test/",
            "https://example.test/\"quote",
            "https://example.test/a\\b",
            "https://[::1/",
        ] {
            assert!(
                resolve(Shell::Android, None, Some(bad), None).is_err(),
                "{bad:?} must be refused"
            );
        }
    }

    #[test]
    fn a_missing_trailing_slash_is_normalised() {
        let cases = [
            ("https://example.test", "https://example.test/"),
            ("https://example.test/app", "https://example.test/app/"),
            ("https://example.test/app/", "https://example.test/app/"),
            ("HTTPS://Example.TEST:8443", "https://example.test:8443/"),
            ("https://example.test?x=1", "https://example.test/?x=1"),
            ("https://example.test/p?x=1", "https://example.test/p?x=1"),
            ("  https://example.test/  ", "https://example.test/"),
        ];
        for (raw, want) in cases {
            assert_eq!(ok(Shell::Ios, None, Some(raw), None).url, want, "{raw}");
        }
    }

    // ---- cleartext exception host ----

    #[test]
    fn plain_http_to_a_remote_host_needs_an_exception_for_exactly_that_host() {
        let u = ok(Shell::Ios, None, Some("http://Example.test:8000/app"), None);
        assert_eq!(u.host, "example.test");
        assert_eq!(u.cleartext_host(), Some("example.test"));
        assert!(u.cleartext_warning().unwrap().contains("https"));
    }

    #[test]
    fn https_and_local_hosts_need_no_exception() {
        for raw in [
            "https://example.test/",
            "http://localhost:8000/",
            "http://127.0.0.1:8000/",
            "http://10.0.2.2:8000/",
            "http://[::1]:8000/",
        ] {
            let u = ok(Shell::Android, None, Some(raw), None);
            assert_eq!(u.cleartext_host(), None, "{raw}");
            assert_eq!(u.cleartext_warning(), None, "{raw}");
        }
        assert_eq!(ok(Shell::Android, None, None, None).cleartext_host(), None);
    }

    // ---- escaping ----

    #[test]
    fn string_literals_escape_for_swift_and_java() {
        assert_eq!(swift_string_literal("a\"b\\c\n"), "\"a\\\"b\\\\c\\n\"");
        assert_eq!(swift_string_literal("\\(x)"), "\"\\\\(x)\"");
        assert_eq!(java_string_literal("a\"b\\c\n"), "\"a\\\"b\\\\c\\n\"");
        assert_eq!(java_string_literal("\u{1}"), "\"\\u0001\"");
    }

    // ---- platform configuration ----

    #[test]
    fn ios_plist_adds_an_exception_domain_only_for_a_cleartext_host() {
        let remote = ok(Shell::Ios, None, Some("http://example.test/"), None);
        let plist = ios_ats_plist(&remote);
        assert!(plist.contains("<key>NSExceptionDomains</key>"), "{plist}");
        assert!(plist.contains("<key>example.test</key>"), "{plist}");
        assert!(
            plist.contains("<key>NSExceptionAllowsInsecureHTTPLoads</key><true/>"),
            "{plist}"
        );
        assert!(!plist.contains("NSAllowsArbitraryLoads"), "{plist}");
        let local = ios_ats_plist(&ok(Shell::Ios, None, None, None));
        assert!(local.contains("NSAllowsLocalNetworking"), "{local}");
        assert!(!local.contains("NSExceptionDomains"), "{local}");
    }

    #[test]
    fn android_scopes_cleartext_to_the_one_host() {
        let remote = ok(
            Shell::Android,
            None,
            Some("http://example.test:8000/"),
            None,
        );
        let (attr, cfg) = android_cleartext(&remote);
        assert!(
            attr.contains("android:networkSecurityConfig=\"@xml/network_security_config\""),
            "{attr}"
        );
        assert!(!attr.contains("usesCleartextTraffic"), "{attr}");
        let cfg = cfg.expect("a network security config");
        assert!(cfg.contains("cleartextTrafficPermitted=\"true\""), "{cfg}");
        assert!(cfg.contains(">example.test</domain>"), "{cfg}");

        // Default (emulator host): today's global cleartext flag, no config file.
        let (attr, cfg) = android_cleartext(&ok(Shell::Android, None, None, None));
        assert!(
            attr.contains("android:usesCleartextTraffic=\"true\""),
            "{attr}"
        );
        assert!(cfg.is_none());

        // https: no cleartext at all.
        let (attr, cfg) = android_cleartext(&ok(
            Shell::Android,
            None,
            Some("https://example.test/"),
            None,
        ));
        assert!(
            !attr.contains("Cleartext") && !attr.contains("networkSecurityConfig"),
            "{attr}"
        );
        assert!(cfg.is_none());
    }

    #[test]
    fn desktop_bakes_a_set_url_and_keeps_the_run_time_port_default() {
        let set = desktop_url_expr(&ok(
            Shell::Desktop,
            None,
            Some("https://example.test/"),
            None,
        ));
        assert_eq!(set, "\"https://example.test/\"");
        let default = desktop_url_expr(&ok(Shell::Desktop, None, None, Some("8000")));
        assert!(
            default.contains("System.getenvOr \"PORT\" \"8000\""),
            "{default}"
        );
        assert!(default.contains("127.0.0.1"), "{default}");
    }

    #[test]
    fn frontend_shell_names_map_to_shells() {
        assert_eq!(Shell::from_frontend_shell("ios"), Some(Shell::Ios));
        assert_eq!(Shell::from_frontend_shell("android"), Some(Shell::Android));
        assert_eq!(Shell::from_frontend_shell("desktop"), Some(Shell::Desktop));
        assert_eq!(Shell::from_frontend_shell("web"), None);
        assert_eq!(Shell::from_frontend_shell("tablet"), None);
    }
}
