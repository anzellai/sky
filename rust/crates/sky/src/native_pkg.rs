//! Native shell packaging: permission purpose strings, typed entitlements, the
//! structured fragment merges, the missing-purpose-string check, and the
//! release policy `sky package --release` enforces.
//!
//! Everything here is read from the app's own source with the same bounded,
//! comment-aware scan the rest of the `Std.Bundle` identity uses (see
//! `scan_bundle_calls_all` in `main.rs`): the packaging lives in code, next to
//! the app, and never becomes a `sky.toml` key. A declaration the scan cannot
//! read (a computed purpose string, a computed entitlement) is an ERROR, never
//! a silent drop, because a store build that loses a purpose string is
//! rejected by App Review, and one that loses an entitlement fails at run time.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use crate::plist::{self, Layer, Rank, Value};
use crate::xmlmini::{self, Element, Node};

// ─────────────────────────────────────────────────────────────────────────────
// Permissions and purpose strings
// ─────────────────────────────────────────────────────────────────────────────

/// How one `Std.Bundle.Permission` constructor maps onto each platform.
#[derive(Debug)]
pub struct PermSpec {
    /// The Sky constructor name (`Camera`, `FaceId`, …).
    pub ctor: &'static str,
    /// iOS `Info.plist` purpose-string keys. Empty: iOS needs no key.
    pub ios_keys: &'static [&'static str],
    /// macOS `Info.plist` purpose-string keys (the desktop `.app`).
    pub macos_keys: &'static [&'static str],
    /// Android `<uses-permission>` names.
    pub android_perms: &'static [&'static str],
    /// Android permissions that are "dangerous" (need a run-time request).
    pub android_runtime: &'static [&'static str],
    /// The text used when the app declares the permission with
    /// `withPermission` (no `withUsage` text). A release build refuses it for
    /// a key the OS shows, because App Review rejects a generic purpose string.
    pub default_text: &'static str,
    /// Needs location plumbing (CLLocationManager / setGeolocationEnabled).
    pub location: bool,
    /// Needs media-capture plumbing (WKUIDelegate / onPermissionRequest).
    pub media: bool,
}

/// Every `Std.Bundle.Permission` constructor. Keep in step with
/// `sky-stdlib/Std/Bundle.sky` (`permission_table_matches_the_stdlib` checks).
pub const PERMISSIONS: &[PermSpec] = &[
    PermSpec {
        ctor: "Location",
        ios_keys: &["NSLocationWhenInUseUsageDescription"],
        macos_keys: &["NSLocationUsageDescription"],
        android_perms: &[
            "android.permission.ACCESS_FINE_LOCATION",
            "android.permission.ACCESS_COARSE_LOCATION",
        ],
        android_runtime: &[
            "android.permission.ACCESS_FINE_LOCATION",
            "android.permission.ACCESS_COARSE_LOCATION",
        ],
        default_text: "Uses your location.",
        location: true,
        media: false,
    },
    PermSpec {
        ctor: "LocationAlways",
        ios_keys: &[
            "NSLocationWhenInUseUsageDescription",
            "NSLocationAlwaysAndWhenInUseUsageDescription",
        ],
        macos_keys: &["NSLocationUsageDescription"],
        android_perms: &[
            "android.permission.ACCESS_FINE_LOCATION",
            "android.permission.ACCESS_COARSE_LOCATION",
            "android.permission.ACCESS_BACKGROUND_LOCATION",
        ],
        // Background location is requested on its own, after the foreground
        // grant (Android 11+ ignores it in a combined request).
        android_runtime: &[
            "android.permission.ACCESS_FINE_LOCATION",
            "android.permission.ACCESS_COARSE_LOCATION",
        ],
        default_text: "Uses your location, also when the app is in the background.",
        location: true,
        media: false,
    },
    PermSpec {
        ctor: "Camera",
        ios_keys: &["NSCameraUsageDescription"],
        macos_keys: &["NSCameraUsageDescription"],
        android_perms: &["android.permission.CAMERA"],
        android_runtime: &["android.permission.CAMERA"],
        default_text: "Uses the camera.",
        location: false,
        media: true,
    },
    PermSpec {
        ctor: "Microphone",
        ios_keys: &["NSMicrophoneUsageDescription"],
        macos_keys: &["NSMicrophoneUsageDescription"],
        android_perms: &["android.permission.RECORD_AUDIO"],
        android_runtime: &["android.permission.RECORD_AUDIO"],
        default_text: "Uses the microphone.",
        location: false,
        media: true,
    },
    PermSpec {
        ctor: "Notifications",
        ios_keys: &[],
        macos_keys: &[],
        android_perms: &["android.permission.POST_NOTIFICATIONS"],
        android_runtime: &["android.permission.POST_NOTIFICATIONS"],
        default_text: "",
        location: false,
        media: false,
    },
    PermSpec {
        ctor: "PhotoLibrary",
        ios_keys: &[
            "NSPhotoLibraryUsageDescription",
            "NSPhotoLibraryAddUsageDescription",
        ],
        macos_keys: &["NSPhotoLibraryUsageDescription"],
        android_perms: &["android.permission.READ_MEDIA_IMAGES"],
        android_runtime: &["android.permission.READ_MEDIA_IMAGES"],
        default_text: "Uses your photo library.",
        location: false,
        media: false,
    },
    PermSpec {
        ctor: "Contacts",
        ios_keys: &["NSContactsUsageDescription"],
        macos_keys: &["NSContactsUsageDescription"],
        android_perms: &["android.permission.READ_CONTACTS"],
        android_runtime: &["android.permission.READ_CONTACTS"],
        default_text: "Uses your contacts.",
        location: false,
        media: false,
    },
    PermSpec {
        ctor: "FaceId",
        ios_keys: &["NSFaceIDUsageDescription"],
        // Touch ID on a Mac has no purpose-string key.
        macos_keys: &[],
        android_perms: &["android.permission.USE_BIOMETRIC"],
        android_runtime: &[],
        default_text: "Uses Face ID to confirm it is you.",
        location: false,
        media: false,
    },
    PermSpec {
        ctor: "LocalNetwork",
        ios_keys: &["NSLocalNetworkUsageDescription"],
        macos_keys: &["NSLocalNetworkUsageDescription"],
        android_perms: &[],
        android_runtime: &[],
        default_text: "Finds devices on your local network.",
        location: false,
        media: false,
    },
    PermSpec {
        ctor: "Bluetooth",
        ios_keys: &["NSBluetoothAlwaysUsageDescription"],
        macos_keys: &["NSBluetoothAlwaysUsageDescription"],
        android_perms: &[
            "android.permission.BLUETOOTH_CONNECT",
            "android.permission.BLUETOOTH_SCAN",
        ],
        android_runtime: &[
            "android.permission.BLUETOOTH_CONNECT",
            "android.permission.BLUETOOTH_SCAN",
        ],
        default_text: "Connects to Bluetooth devices.",
        location: false,
        media: false,
    },
];

pub fn perm_spec(ctor: &str) -> Option<&'static PermSpec> {
    PERMISSIONS.iter().find(|p| p.ctor == ctor)
}

/// One declared permission: the constructor and the app's own purpose string
/// (`withUsage`), or `None` for a bare `withPermission`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Declared {
    pub ctor: String,
    pub text: Option<String>,
}

impl Declared {
    pub fn spec(&self) -> &'static PermSpec {
        perm_spec(&self.ctor).expect("scan_permissions only yields known constructors")
    }

    /// The purpose string the OS shows.
    pub fn text(&self) -> &str {
        self.text.as_deref().unwrap_or(self.spec().default_text)
    }
}

fn is_word(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

/// Each word-bounded, uncommented occurrence of `func` in `src`, as the byte
/// offset just past the name.
fn call_sites<'a>(src: &'a str, func: &'a str) -> impl Iterator<Item = usize> + 'a {
    let bytes = src.as_bytes();
    let mut from = 0;
    std::iter::from_fn(move || loop {
        let rel = src[from..].find(func)?;
        let at = from + rel;
        from = at + func.len();
        let after = at + func.len();
        let before_ok = at == 0 || !is_word(bytes[at - 1]);
        let after_ok = bytes.get(after).map(|b| !is_word(*b)).unwrap_or(true);
        if before_ok && after_ok && !crate::in_line_comment(src, at) {
            return Some(after);
        }
    })
}

/// Skip whitespace (newlines included) and `(`.
fn skip_ws_parens(s: &str) -> &str {
    s.trim_start_matches(|c: char| c.is_whitespace() || c == '(')
}

/// A possibly-qualified constructor at the start of `s`, returned unqualified
/// with the rest of the input.
fn ctor_prefix(s: &str) -> (String, &str) {
    let len = s
        .find(|c: char| !(c.is_alphanumeric() || c == '_' || c == '.'))
        .unwrap_or(s.len());
    let ident = &s[..len];
    let name = ident.rsplit('.').next().unwrap_or(ident).to_string();
    (name, &s[len..])
}

/// The line number of byte offset `at`, for a diagnostic.
fn line_of(src: &str, at: usize) -> usize {
    src[..at].matches('\n').count() + 1
}

/// The permissions the entry source declares, deduped by constructor in
/// declaration order. `withUsage P "text"` sets the purpose string and wins
/// over a bare `withPermission P` for the same constructor. An unknown
/// constructor or a purpose string that is not a literal is an error.
pub fn scan_permissions(src: &str) -> Result<Vec<Declared>, String> {
    let mut out: Vec<Declared> = Vec::new();
    let mut put = |d: Declared| match out.iter_mut().find(|e| e.ctor == d.ctor) {
        Some(e) => {
            if d.text.is_some() {
                e.text = d.text;
            }
        }
        None => out.push(d),
    };
    // Both builders, in source order, so the result is declaration order.
    let mut sites: Vec<(usize, bool)> = call_sites(src, "withPermission")
        .map(|a| (a, false))
        .chain(call_sites(src, "withUsage").map(|a| (a, true)))
        .collect();
    sites.sort();
    for (after, usage) in sites {
        if !usage {
            let (ctor, _) = ctor_prefix(skip_ws_parens(&src[after..]));
            // A `withPermission` whose argument is not a known constructor is
            // left alone here: it may be the definition in Std.Bundle itself, or
            // a local helper; the type checker owns the argument's validity.
            if perm_spec(&ctor).is_some() {
                put(Declared { ctor, text: None });
            }
            continue;
        }
        let (ctor, rest) = ctor_prefix(skip_ws_parens(&src[after..]));
        if ctor.is_empty() || ctor.chars().next().is_some_and(|c| c.is_lowercase()) {
            // `withUsage : Permission -> …` (a signature) or a computed
            // constructor. A signature line has `:` next; anything else is a
            // value the build cannot read.
            if skip_ws_parens(&src[after..]).starts_with(':') || ctor.is_empty() {
                continue;
            }
            return Err(format!(
                "line {}: `Bundle.withUsage {ctor} …` — the build reads the permission \
                 statically; write the constructor itself (e.g. `Bundle.withUsage \
                 Bundle.Camera \"Scans QR codes.\"`).",
                line_of(src, after)
            ));
        }
        if perm_spec(&ctor).is_none() {
            return Err(format!(
                "line {}: `Bundle.withUsage {ctor}` — `{ctor}` is not a Std.Bundle \
                 permission (one of: {}).",
                line_of(src, after),
                PERMISSIONS
                    .iter()
                    .map(|p| p.ctor)
                    .collect::<Vec<_>>()
                    .join(", ")
            ));
        }
        let rest = rest.trim_start_matches(|c: char| c.is_whitespace() || c == ')');
        match crate::string_literal_prefix(rest) {
            Some(text) if !text.trim().is_empty() => put(Declared {
                ctor,
                text: Some(text),
            }),
            Some(_) => {
                return Err(format!(
                    "line {}: `Bundle.withUsage {ctor} \"\"` — a purpose string must say \
                     why the app needs it; the OS shows it in the permission prompt.",
                    line_of(src, after)
                ))
            }
            None => {
                return Err(format!(
                    "line {}: `Bundle.withUsage {ctor} …` — the purpose string must be a \
                     string literal: the build writes it into Info.plist, so it cannot \
                     be computed at run time.",
                    line_of(src, after)
                ))
            }
        }
    }
    Ok(out)
}

/// The `Info.plist` purpose-string entries for `declared` on iOS (`macos =
/// false`) or macOS (`macos = true`).
pub fn usage_entries(declared: &[Declared], macos: bool) -> Vec<(String, Value)> {
    let mut out: Vec<(String, Value)> = Vec::new();
    for d in declared {
        let spec = d.spec();
        let keys = if macos {
            spec.macos_keys
        } else {
            spec.ios_keys
        };
        for k in keys {
            if !out.iter().any(|(e, _)| e == k) {
                out.push((k.to_string(), Value::String(d.text().to_string())));
            }
        }
    }
    out
}

/// Android `<uses-permission>` names for `declared`, deduped in order.
pub fn android_permissions(declared: &[Declared]) -> Vec<&'static str> {
    let mut out: Vec<&'static str> = Vec::new();
    for d in declared {
        for p in d.spec().android_perms {
            if !out.contains(p) {
                out.push(p);
            }
        }
    }
    out
}

/// Android permissions that need a run-time request, deduped in order.
pub fn android_runtime_permissions(declared: &[Declared]) -> Vec<&'static str> {
    let mut out: Vec<&'static str> = Vec::new();
    for d in declared {
        for p in d.spec().android_runtime {
            if !out.contains(p) {
                out.push(p);
            }
        }
    }
    out
}

// ─────────────────────────────────────────────────────────────────────────────
// Typed entitlements
// ─────────────────────────────────────────────────────────────────────────────

/// One `Std.Bundle.Entitlement`, as the build reads it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Entitlement {
    KeychainAccessGroup(String),
    AppGroup(String),
    AssociatedDomain(String),
    /// `true` for production, `false` for development.
    PushNotifications(bool),
    ICloudContainer(String),
}

const ENTITLEMENT_CTORS: &[&str] = &[
    "KeychainAccessGroup",
    "AppGroup",
    "AssociatedDomain",
    "PushNotifications",
    "ICloudContainer",
];

/// The entitlements the entry source declares with `withEntitlement`, in
/// declaration order, validated. A computed argument is an error.
pub fn scan_entitlements(src: &str) -> Result<Vec<Entitlement>, String> {
    let mut out = Vec::new();
    for after in call_sites(src, "withEntitlement") {
        let arg = skip_ws_parens(&src[after..]);
        if arg.starts_with([':', ',', ')']) {
            continue; // the signature in Std.Bundle, or an import's exposing list
        }
        let line = line_of(src, after);
        let (ctor, rest) = ctor_prefix(arg);
        if !ENTITLEMENT_CTORS.contains(&ctor.as_str()) {
            if ctor.chars().next().is_some_and(|c| c.is_lowercase()) {
                return Err(format!(
                    "line {line}: `Bundle.withEntitlement {ctor}` — the build reads \
                     entitlements statically; write the constructor and a string literal \
                     (e.g. `Bundle.withEntitlement (Bundle.AppGroup \"group.com.acme.app\")`)."
                ));
            }
            return Err(format!(
                "line {line}: `{ctor}` is not a Std.Bundle entitlement (one of: {}).",
                ENTITLEMENT_CTORS.join(", ")
            ));
        }
        let rest = rest.trim_start();
        let e = if ctor == "PushNotifications" {
            let (env, _) = ctor_prefix(rest.trim_start_matches('('));
            match env.as_str() {
                "PushProduction" => Entitlement::PushNotifications(true),
                "PushDevelopment" => Entitlement::PushNotifications(false),
                _ => {
                    return Err(format!(
                        "line {line}: `Bundle.PushNotifications` takes \
                         `Bundle.PushDevelopment` or `Bundle.PushProduction`."
                    ))
                }
            }
        } else {
            let Some(v) = crate::string_literal_prefix(rest) else {
                return Err(format!(
                    "line {line}: `Bundle.{ctor}` needs a string literal: the build writes it \
                     into the entitlements file, so it cannot be computed at run time."
                ));
            };
            let v = v.trim().to_string();
            let bad = |why: &str| Err(format!("line {line}: `Bundle.{ctor} {v:?}` — {why}"));
            match ctor.as_str() {
                "KeychainAccessGroup" => {
                    if v.is_empty() || v.contains(char::is_whitespace) {
                        return bad("a keychain access group is a non-empty identifier");
                    }
                    Entitlement::KeychainAccessGroup(v)
                }
                "AppGroup" => {
                    if !v.starts_with("group.") || v.len() <= "group.".len() {
                        return bad(
                            "an app group starts with `group.` (e.g. `group.com.acme.app`)",
                        );
                    }
                    Entitlement::AppGroup(v)
                }
                "AssociatedDomain" => {
                    let ok = [
                        "applinks:",
                        "webcredentials:",
                        "activitycontinuation:",
                        "appclips:",
                    ]
                    .iter()
                    .any(|p| v.starts_with(p) && v.len() > p.len());
                    if !ok {
                        return bad("an associated domain names its service, e.g. \
                             `applinks:example.com` or `webcredentials:example.com`");
                    }
                    if parse_associated_domain(&v).is_none() {
                        return bad("an associated domain is `<service>:<host>`, where the \
                             host is a domain name, optionally starting `*.`, with an \
                             optional `:<port>` and `?mode=developer` (or `managed`)");
                    }
                    Entitlement::AssociatedDomain(v)
                }
                _ => {
                    if !v.starts_with("iCloud.") || v.len() <= "iCloud.".len() {
                        return bad(
                            "an iCloud container starts with `iCloud.` (e.g. `iCloud.com.acme.app`)",
                        );
                    }
                    Entitlement::ICloudContainer(v)
                }
            }
        };
        if !out.contains(&e) {
            out.push(e);
        }
    }
    Ok(out)
}

/// One associated domain, parsed: Apple's `<service>:<host>[:<port>][?mode=…]`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AssociatedDomain {
    /// `applinks`, `webcredentials`, `activitycontinuation` or `appclips`.
    pub service: String,
    /// The domain name, possibly `*.`-prefixed (a wildcard subdomain).
    pub host: String,
    pub port: Option<u16>,
}

/// Parse an `AssociatedDomain` value (`applinks:example.com`,
/// `applinks:*.example.com:8443?mode=developer`). `None` when the host is not
/// a domain name.
pub fn parse_associated_domain(v: &str) -> Option<AssociatedDomain> {
    let (service, rest) = v.split_once(':')?;
    let rest = match rest.split_once('?') {
        Some((before, mode)) => {
            let m = mode.strip_prefix("mode=")?;
            if !m.split('+').all(|x| x == "developer" || x == "managed") {
                return None;
            }
            before
        }
        None => rest,
    };
    let (host, port) = match rest.rsplit_once(':') {
        Some((h, p)) => (h, Some(p.parse::<u16>().ok().filter(|p| *p > 0)?)),
        None => (rest, None),
    };
    let bare = host.strip_prefix("*.").unwrap_or(host);
    let label_ok = |l: &str| {
        !l.is_empty()
            && l.len() <= 63
            && !l.starts_with('-')
            && !l.ends_with('-')
            && l.chars().all(|c| c.is_ascii_alphanumeric() || c == '-')
    };
    if bare.is_empty() || !bare.split('.').all(label_ok) {
        return None;
    }
    Some(AssociatedDomain {
        service: service.to_string(),
        host: host.to_ascii_lowercase(),
        port,
    })
}

/// The declared associated domains, as the native shells use them: the iOS
/// and Android shells route a link to an `applinks:` host into the app, and
/// the Android build maps each domain onto its Android equivalent.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct LinkDomains {
    /// `applinks:` domains: an `autoVerify` App Links intent filter each.
    pub app_links: Vec<AssociatedDomain>,
    /// `webcredentials:` hosts: the site shares its sign-in credentials with
    /// the app (Digital Asset Links `get_login_creds`, and the app's
    /// `asset_statements`).
    pub login_hosts: Vec<String>,
    /// Domains with no Android equivalent (`activitycontinuation:`,
    /// `appclips:`), as written: the build says it leaves them out.
    pub unmapped: Vec<String>,
}

impl LinkDomains {
    pub fn is_empty(&self) -> bool {
        self.app_links.is_empty() && self.login_hosts.is_empty()
    }

    /// Every host the site-association file must be served on.
    pub fn hosts(&self) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        for h in self
            .app_links
            .iter()
            .map(|d| d.host.trim_start_matches("*.").to_string())
            .chain(self.login_hosts.iter().cloned())
        {
            if !out.contains(&h) {
                out.push(h);
            }
        }
        out
    }
}

/// Read the associated domains out of the declared entitlements.
pub fn link_domains(ents: &[Entitlement]) -> LinkDomains {
    let mut out = LinkDomains::default();
    for e in ents {
        let Entitlement::AssociatedDomain(v) = e else {
            continue;
        };
        let Some(d) = parse_associated_domain(v) else {
            continue; // refused by scan_entitlements
        };
        match d.service.as_str() {
            "applinks" => {
                if !out
                    .app_links
                    .iter()
                    .any(|x| x.host == d.host && x.port == d.port)
                {
                    out.app_links.push(d);
                }
            }
            "webcredentials" => {
                let h = d.host.trim_start_matches("*.").to_string();
                if !out.login_hosts.contains(&h) {
                    out.login_hosts.push(h);
                }
            }
            _ => {
                if !out.unmapped.contains(v) {
                    out.unmapped.push(v.clone());
                }
            }
        }
    }
    out
}

/// The App Links intent filters for the shell's activity: one
/// `android:autoVerify` filter per `applinks:` domain, for `https` and that
/// host (and port). Empty when there is none.
pub fn android_link_filters(links: &LinkDomains) -> String {
    links
        .app_links
        .iter()
        .map(|d| {
            let port = d
                .port
                .map(|p| format!(" android:port=\"{p}\""))
                .unwrap_or_default();
            format!(
                "\n            <intent-filter android:autoVerify=\"true\">\n\
                 \x20               <action android:name=\"android.intent.action.VIEW\" />\n\
                 \x20               <category android:name=\"android.intent.category.DEFAULT\" />\n\
                 \x20               <category android:name=\"android.intent.category.BROWSABLE\" />\n\
                 \x20               <data android:scheme=\"https\" android:host=\"{}\"{port} />\n\
                 \x20           </intent-filter>",
                d.host
            )
        })
        .collect()
}

/// The `asset_statements` string resource (a JSON array, as Android reads it)
/// that tells Android which sites the app trusts for sign-in credentials, or
/// `None` without a `webcredentials:` domain.
pub fn android_asset_statements(links: &LinkDomains) -> Option<String> {
    if links.login_hosts.is_empty() {
        return None;
    }
    let items: Vec<String> = links
        .login_hosts
        .iter()
        .map(|h| format!("{{\"include\": \"https://{h}/.well-known/assetlinks.json\"}}"))
        .collect();
    Some(format!("[{}]", items.join(", ")))
}

/// The hosts the shell accepts an incoming link for (the `applinks:` hosts,
/// `*.`-prefixed for a wildcard), as a Java / Swift string-array body.
pub fn link_host_literals(links: &LinkDomains) -> String {
    links
        .app_links
        .iter()
        .map(|d| format!("\"{}\"", d.host))
        .collect::<Vec<_>>()
        .join(", ")
}

/// Format a SHA-256 certificate digest the way Digital Asset Links writes it:
/// upper-case hex pairs joined by `:`. `None` unless `hex` is 64 hex digits
/// (colons and spaces are ignored).
pub fn sha256_fingerprint(hex: &str) -> Option<String> {
    let clean: String = hex
        .chars()
        .filter(|c| !matches!(c, ':' | ' '))
        .collect::<String>()
        .to_ascii_uppercase();
    if clean.len() != 64 || !clean.chars().all(|c| c.is_ascii_hexdigit()) {
        return None;
    }
    Some(
        clean
            .as_bytes()
            .chunks(2)
            .map(|p| std::str::from_utf8(p).unwrap_or(""))
            .collect::<Vec<_>>()
            .join(":"),
    )
}

/// The first signer's SHA-256 certificate digest from `apksigner verify
/// --print-certs` output, as a Digital Asset Links fingerprint.
pub fn apksigner_sha256(output: &str) -> Option<String> {
    output.lines().find_map(|l| {
        let (_, rest) = l.split_once("certificate SHA-256 digest:")?;
        sha256_fingerprint(rest.trim())
    })
}

/// The `/.well-known/assetlinks.json` the site must serve on each host of
/// `links` for the app `package` signed with the certificate `fingerprint`:
/// `handle_all_urls` for App Links, `get_login_creds` for shared sign-in.
pub fn assetlinks_json(package: &str, fingerprint: &str, links: &LinkDomains) -> String {
    let mut relations = Vec::new();
    if !links.app_links.is_empty() {
        relations.push("\"delegate_permission/common.handle_all_urls\"");
    }
    if !links.login_hosts.is_empty() {
        relations.push("\"delegate_permission/common.get_login_creds\"");
    }
    format!(
        "[\n  {{\n    \"relation\": [{}],\n    \"target\": {{\n      \"namespace\": \"android_app\",\n      \"package_name\": \"{package}\",\n      \"sha256_cert_fingerprints\": [\"{fingerprint}\"]\n    }}\n  }}\n]\n",
        relations.join(", ")
    )
}

fn push_array(out: &mut Vec<(String, Value)>, key: &str, item: Value) {
    match out.iter_mut().find(|(k, _)| k == key) {
        Some((_, Value::Array(items))) => {
            if !items.contains(&item) {
                items.push(item)
            }
        }
        _ => out.push((key.to_string(), Value::Array(vec![item]))),
    }
}

/// The entitlements-file entries for `ents` (iOS and macOS use the same keys).
pub fn entitlement_entries(ents: &[Entitlement]) -> Vec<(String, Value)> {
    let mut out: Vec<(String, Value)> = Vec::new();
    for e in ents {
        match e {
            Entitlement::KeychainAccessGroup(g) => {
                push_array(&mut out, "keychain-access-groups", Value::String(g.clone()))
            }
            Entitlement::AppGroup(g) => push_array(
                &mut out,
                "com.apple.security.application-groups",
                Value::String(g.clone()),
            ),
            Entitlement::AssociatedDomain(d) => push_array(
                &mut out,
                "com.apple.developer.associated-domains",
                Value::String(d.clone()),
            ),
            Entitlement::PushNotifications(prod) => {
                let v = Value::String(if *prod { "production" } else { "development" }.into());
                if !out.iter().any(|(k, _)| k == "aps-environment") {
                    out.push(("aps-environment".to_string(), v));
                }
            }
            Entitlement::ICloudContainer(c) => {
                push_array(
                    &mut out,
                    "com.apple.developer.icloud-container-identifiers",
                    Value::String(c.clone()),
                );
                push_array(
                    &mut out,
                    "com.apple.developer.icloud-services",
                    Value::String("CloudKit".into()),
                );
            }
        }
    }
    out
}

// ─────────────────────────────────────────────────────────────────────────────
// Restricted entitlements, ad hoc signatures and the simulator
// ─────────────────────────────────────────────────────────────────────────────

/// Whether the kernel honours `key` only in a signature a provisioning profile
/// backs. AppleMobileFileIntegrity refuses to run a binary that is signed ad
/// hoc and carries one of these (`amfid`: "The file is adhoc signed but
/// contains restricted entitlements", error -424); the process never starts,
/// so `simctl launch` reports "No such process" and a macOS `.app` dies with
/// SIGKILL. Measured on macOS 27 / iOS 27 with an ad hoc signature: each
/// `com.apple.developer.*` key, `keychain-access-groups` and `aps-environment`
/// kill the launch; `com.apple.security.application-groups` and
/// `com.apple.security.get-task-allow` do not.
pub fn is_restricted_entitlement(key: &str) -> bool {
    matches!(
        key,
        "keychain-access-groups"
            | "application-identifier"
            | "com.apple.application-identifier"
            | "aps-environment"
    ) || key.starts_with("com.apple.developer.")
}

/// Split entitlements into the ones an ad hoc signature may carry and the
/// restricted ones (see [`is_restricted_entitlement`]).
pub fn split_restricted(entries: &[(String, Value)]) -> (Vec<(String, Value)>, Vec<String>) {
    let mut signable = Vec::new();
    let mut restricted = Vec::new();
    for (k, v) in entries {
        if is_restricted_entitlement(k) {
            restricted.push(k.clone());
        } else {
            signable.push((k.clone(), v.clone()));
        }
    }
    (signable, restricted)
}

/// The team-id prefix the simulator build uses in its application identifier
/// when the app declares no keychain access group to take one from. The
/// simulator does not check it against a team; it only has to be present, as
/// Xcode's `$(AppIdentifierPrefix)` is.
pub const SIMULATOR_TEAM_PREFIX: &str = "SKYSIMTEAM";

/// Whether `s` has the shape of an Apple team id (ten upper-case letters and
/// digits), the prefix of an application identifier.
fn is_team_id(s: &str) -> bool {
    s.len() == 10
        && s.bytes()
            .all(|b| b.is_ascii_uppercase() || b.is_ascii_digit())
}

/// The entitlements the iOS SIMULATOR build embeds, as Xcode does for a
/// simulator build: every requested entitlement, plus the
/// `application-identifier` (`<team prefix>.<bundle id>`) and, when the app
/// declares none, `keychain-access-groups = [<application-identifier>]`.
///
/// They are not put in the code signature. Xcode writes them into the
/// `__TEXT,__entitlements` (XML) and `__TEXT,__ents_der` (DER) sections of the
/// executable (`ld -sectcreate`, from `<App>.app-Simulated.xcent`) and signs
/// the simulator build ad hoc with an EMPTY entitlements dictionary. The
/// simulator reads the sections, so the Keychain gets its access group (without
/// one every `SecItem*` call fails with -34018, errSecMissingEntitlement), and
/// the kernel never sees a restricted entitlement in an ad hoc signature, so
/// the launch is not refused. The team prefix is the one the app's first
/// declared keychain access group carries (so the default access group and the
/// application identifier agree, as on a device), else
/// [`SIMULATOR_TEAM_PREFIX`].
pub fn simulated_entitlements(
    bundle_id: &str,
    requested: &[(String, Value)],
) -> Vec<(String, Value)> {
    let declared_groups: Vec<String> = requested
        .iter()
        .find(|(k, _)| k == "keychain-access-groups")
        .map(|(_, v)| match v {
            Value::Array(items) => items
                .iter()
                .filter_map(|i| match i {
                    Value::String(s) => Some(s.clone()),
                    _ => None,
                })
                .collect(),
            _ => Vec::new(),
        })
        .unwrap_or_default();
    let prefix = declared_groups
        .first()
        .and_then(|g| g.split('.').next())
        .filter(|p| is_team_id(p))
        .unwrap_or(SIMULATOR_TEAM_PREFIX);
    let app_id = format!("{prefix}.{bundle_id}");
    let mut out: Vec<(String, Value)> = Vec::new();
    if !requested.iter().any(|(k, _)| k == "application-identifier") {
        out.push((
            "application-identifier".to_string(),
            Value::String(app_id.clone()),
        ));
    }
    if declared_groups.is_empty() {
        out.push((
            "keychain-access-groups".to_string(),
            Value::Array(vec![Value::String(app_id)]),
        ));
    }
    out.extend(
        requested
            .iter()
            .filter(|(k, _)| !(k == "keychain-access-groups" && declared_groups.is_empty()))
            .cloned(),
    );
    out
}

// ─────────────────────────────────────────────────────────────────────────────
// Native fragments: discovery and structured merge
// ─────────────────────────────────────────────────────────────────────────────

/// A fragment file found under a `native/<platform>/` dir.
#[derive(Debug, Clone)]
pub struct Fragment {
    pub path: PathBuf,
    /// The project's own fragment (outranks every dependency's).
    pub own: bool,
}

/// Every `native/<platform>/<file>` across the project and its `.skydeps`
/// packages, the project's first.
pub fn fragments(dirs: &[PathBuf], project_dir: &Path, file: &str) -> Vec<Fragment> {
    let own_dir = project_dir.join("native");
    dirs.iter()
        .map(|d| d.join(file))
        .filter(|p| p.is_file())
        .map(|p| Fragment {
            own: p.starts_with(&own_dir),
            path: p,
        })
        .collect()
}

/// Read each fragment as a property-list layer.
pub fn fragment_layers(frags: &[Fragment], project_dir: &Path) -> Result<Vec<Layer>, String> {
    frags
        .iter()
        .map(|f| {
            let origin = f
                .path
                .strip_prefix(project_dir)
                .unwrap_or(&f.path)
                .display()
                .to_string();
            let src =
                std::fs::read_to_string(&f.path).map_err(|e| format!("read {origin}: {e}"))?;
            let entries = plist::parse_entries(&src)
                .map_err(|e| format!("{origin} is not a valid property list: {e}"))?;
            Ok(Layer {
                origin,
                rank: if f.own {
                    Rank::Project
                } else {
                    Rank::Dependency
                },
                entries,
            })
        })
        .collect()
}

/// Merge `layers` into one document, printing each override warning, or
/// return every conflict as one error.
pub fn merge_document(what: &str, layers: &[Layer]) -> Result<String, String> {
    match plist::merge(layers) {
        Ok(m) => {
            for w in &m.warnings {
                eprintln!("  note: {what}: {w}");
            }
            Ok(plist::render_document(&m.entries))
        }
        Err(errs) => Err(format!(
            "{what}: the sources disagree:\n  {}",
            errs.join("\n  ")
        )),
    }
}

/// The merged manifest-root elements from the `native/android/permissions.xml`
/// fragments (project first), deduplicated structurally. The generated
/// `generated` permission names are dropped from the fragments (they are
/// already in the manifest). Two `<uses-permission>` elements for the same
/// name with different attributes (say, one with `android:maxSdkVersion`) are a
/// conflict: the project's own wins over a dependency, two dependencies are an
/// error. The result is the XML text to splice under `<manifest>`.
pub fn merge_android_fragments(
    frags: &[Fragment],
    project_dir: &Path,
    generated: &[&str],
) -> Result<String, String> {
    let mut kept: Vec<(Element, bool, String)> = Vec::new(); // (element, own, origin)
    let mut errors = Vec::new();
    for f in frags {
        let origin = f
            .path
            .strip_prefix(project_dir)
            .unwrap_or(&f.path)
            .display()
            .to_string();
        let src = std::fs::read_to_string(&f.path).map_err(|e| format!("read {origin}: {e}"))?;
        let nodes =
            xmlmini::parse_nodes(&src).map_err(|e| format!("{origin} is not valid XML: {e}"))?;
        for n in nodes {
            let Node::Element(el) = n else {
                return Err(format!(
                    "{origin}: text outside an element; a manifest fragment holds \
                     elements such as <uses-permission android:name=\"…\" />"
                ));
            };
            let name_attr = el.attr("android:name").map(str::to_string);
            let keyed = matches!(
                el.name.as_str(),
                "uses-permission" | "uses-permission-sdk-23" | "uses-feature"
            );
            if keyed {
                if let Some(n) = &name_attr {
                    if el.name == "uses-permission" && generated.contains(&n.as_str()) {
                        // Already declared by Std.Bundle; identical attributes
                        // are a duplicate, anything more is a conflict.
                        if el.attrs.len() == 1 {
                            continue;
                        }
                        errors.push(format!(
                            "{origin}: <uses-permission android:name=\"{n}\"> adds attributes to \
                             a permission Sky already declares from Std.Bundle; remove it from \
                             the fragment or drop the Bundle declaration."
                        ));
                        continue;
                    }
                }
            }
            let same_key = |k: &Element| {
                keyed && k.name == el.name && k.attr("android:name") == name_attr.as_deref()
            };
            match kept.iter().position(|(k, _, _)| *k == el || same_key(k)) {
                Some(i) if kept[i].0 == el => {} // identical: one copy
                Some(i) => {
                    let (_, own_first, first_origin) = &kept[i];
                    if *own_first && !f.own {
                        eprintln!(
                            "  note: {origin} and {first_origin} both declare <{} \
                             android:name=\"{}\"> differently; {first_origin} wins.",
                            el.name,
                            name_attr.as_deref().unwrap_or("")
                        );
                    } else {
                        errors.push(format!(
                            "{origin} and {first_origin} both declare <{} android:name=\"{}\"> \
                             with different attributes; set it once in the project's own \
                             native/android/permissions.xml, which outranks every dependency.",
                            el.name,
                            name_attr.as_deref().unwrap_or("")
                        ));
                    }
                }
                None => kept.push((el, f.own, origin.clone())),
            }
        }
    }
    if !errors.is_empty() {
        return Err(format!(
            "native/android/permissions.xml: the fragments disagree:\n  {}",
            errors.join("\n  ")
        ));
    }
    Ok(kept
        .iter()
        .map(|(e, _, _)| format!("\n    {}", xmlmini::render_element(e)))
        .collect())
}

// ─────────────────────────────────────────────────────────────────────────────
// Capabilities the app uses → the purpose strings they need
// ─────────────────────────────────────────────────────────────────────────────

/// `Std.Native` functions whose OS API refuses to run without a declared
/// permission, and the constructors that satisfy each (any one of them).
pub const CAPABILITY_NEEDS: &[(&str, &[&str])] = &[
    ("authenticate", &["FaceId"]),
    ("capturePhoto", &["Camera"]),
    ("geolocation", &["Location", "LocationAlways"]),
    // Android 13 and later drops a notification without POST_NOTIFICATIONS,
    // and an undeclared permission is refused without a prompt. iOS asks
    // with no Info.plist key, so the check applies to Android only.
    ("notify", &["Notifications"]),
    ("scanCode", &["Camera"]),
];

/// The `Std.Native` functions in `CAPABILITY_NEEDS` that `src` calls: a
/// qualified use through the module's alias (`Native.authenticate`, or
/// `Std.Native.authenticate` with no alias), or a bare use of a name the import
/// exposes explicitly. `exposing (..)` counts every name.
#[cfg(test)]
pub fn native_capabilities_used(src: &str) -> BTreeSet<&'static str> {
    native_capability_lines(src).into_keys().collect()
}

/// [`native_capabilities_used`] with the line of each function's first use, so
/// the missing-purpose-string error can name where the app calls it.
pub fn native_capability_lines(src: &str) -> BTreeMap<&'static str, usize> {
    let mut out = BTreeMap::new();
    // Each import declaration's byte range. A name in an import's exposing
    // list is not a use of it.
    let imports: Vec<(usize, usize)> = call_sites(src, "import")
        .map(|at| {
            let line_end = src[at..].find('\n').map(|n| at + n).unwrap_or(src.len());
            // An import may continue on indented lines (a long exposing list).
            let mut end = line_end;
            while end < src.len() {
                let next_end = src[end + 1..]
                    .find('\n')
                    .map(|n| end + 1 + n)
                    .unwrap_or(src.len());
                let next = &src[end + 1..next_end];
                if next.starts_with(' ') || next.starts_with('\t') {
                    end = next_end;
                } else {
                    break;
                }
            }
            (at, end)
        })
        .collect();
    let in_import = |at: usize| imports.iter().any(|(s, e)| *s <= at && at <= *e);
    for &(at, end) in &imports {
        let decl = src[at..end].trim();
        let Some(rest) = decl.strip_prefix("Std.Native") else {
            continue;
        };
        if rest.starts_with(|c: char| c.is_alphanumeric() || c == '.' || c == '_') {
            continue; // Std.NativeX
        }
        let alias = rest
            .split_whitespace()
            .collect::<Vec<_>>()
            .windows(2)
            .find(|w| w[0] == "as")
            .map(|w| w[1].to_string())
            .unwrap_or_else(|| "Std.Native".to_string());
        let exposing: Vec<String> = rest
            .find("exposing")
            .map(|i| {
                rest[i + "exposing".len()..]
                    .trim()
                    .trim_start_matches('(')
                    .trim_end_matches(')')
                    .split(',')
                    .map(|s| s.trim().to_string())
                    .collect()
            })
            .unwrap_or_default();
        let all = exposing.iter().any(|e| e == "..");
        for (func, _) in CAPABILITY_NEEDS {
            let qualified = format!("{alias}.{func}");
            let q_used = call_sites(src, &qualified).find(|a| !in_import(*a));
            let bare_used = if all || exposing.iter().any(|e| e == func) {
                call_sites(src, func).find(|after| {
                    let start = after - func.len();
                    (start == 0 || src.as_bytes()[start - 1] != b'.') && !in_import(*after)
                })
            } else {
                None
            };
            if let Some(at) = [q_used, bare_used].into_iter().flatten().min() {
                let line = line_of(src, at);
                let e = out.entry(*func).or_insert(line);
                *e = (*e).min(line);
            }
        }
    }
    out
}

/// Every `.sky` file under the project's source root.
fn sky_sources(project_dir: &Path) -> Vec<PathBuf> {
    let root = project_dir.join(project::configured_source_root(project_dir));
    let mut out = Vec::new();
    let mut stack = vec![root];
    while let Some(d) = stack.pop() {
        let Ok(rd) = std::fs::read_dir(&d) else {
            continue;
        };
        for e in rd.flatten() {
            let p = e.path();
            if p.file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| n.starts_with('.'))
            {
                continue;
            }
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().and_then(|x| x.to_str()) == Some("sky") {
                out.push(p);
            }
        }
    }
    out.sort();
    out
}

/// The shells the purpose-string check applies to. The desktop shell asks for
/// none of these capabilities through an OS prompt that needs a key.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Platform {
    Ios,
    Android,
    Macos,
}

impl Platform {
    fn label(self) -> &'static str {
        match self {
            Platform::Ios => "iOS",
            Platform::Android => "Android",
            Platform::Macos => "macOS",
        }
    }
}

/// Check that every capability the app calls has its permission declared, and,
/// for a release, that every declared permission the OS shows a prompt for
/// carries the app's own purpose string. Returns every problem in one error.
/// Where the app declares its bundle and calls its capabilities, so a
/// missing-purpose-string error names the user's own file and line, never a
/// file the build derived from it.
#[derive(Debug, Default, Clone)]
pub struct Sites {
    /// The entry file, relative to the project (`src/Main.sky`).
    pub entry: String,
    /// The line of the entry's `bundle =` binding, when it has one.
    pub bundle_line: Option<usize>,
    /// Each capability's first use: the file relative to the project, and the
    /// line.
    pub calls: BTreeMap<&'static str, (String, usize)>,
}

/// The line of the top-level `bundle =` binding in `src`, if any.
pub fn bundle_binding_line(src: &str) -> Option<usize> {
    src.lines().enumerate().find_map(|(i, l)| {
        let rest = l.strip_prefix("bundle")?;
        let rest = rest.trim_start();
        (rest.starts_with('=') && !rest.starts_with("==")).then_some(i + 1)
    })
}

pub fn check_usage(
    used: &BTreeSet<&'static str>,
    declared: &[Declared],
    platform: Platform,
    release: bool,
    sites: &Sites,
) -> Result<(), String> {
    let entry = if sites.entry.is_empty() {
        "the entry module".to_string()
    } else {
        sites.entry.clone()
    };
    let mut problems = Vec::new();
    if platform != Platform::Macos {
        for (func, needs) in CAPABILITY_NEEDS {
            if !used.contains(func) {
                continue;
            }
            if declared.iter().any(|d| needs.contains(&d.ctor.as_str())) {
                continue;
            }
            let ctor = needs[0];
            let spec = perm_spec(ctor).expect("table ctor");
            let what = match platform {
                Platform::Ios => spec.ios_keys.join(" / "),
                _ => spec.android_perms.join(" / "),
            };
            if what.is_empty() {
                // The platform asks for this capability with no declaration.
                continue;
            }
            let at = sites
                .calls
                .get(func)
                .map(|(f, l)| format!(" at {f}:{l}"))
                .unwrap_or_default();
            // A permission with no purpose string (Notifications) is declared
            // with `withPermission`.
            let fix = if spec.default_text.is_empty() {
                format!("`|> Bundle.withPermission Bundle.{ctor}`")
            } else {
                format!("`|> Bundle.withUsage Bundle.{ctor} \"<why the app needs it>\"`")
            };
            let place = match sites.bundle_line {
                Some(l) => format!("to the `bundle` binding at {entry}:{l}"),
                None => format!(
                    "to a `bundle` binding in {entry}: `bundle = Bundle.default {}`, with \
                     `import Std.Bundle as Bundle exposing (Bundle)` and `bundle` in the \
                     module's exposing list",
                    fix.trim_matches('`')
                ),
            };
            problems.push(format!(
                "the app calls `Native.{func}`{at}, which needs {what} on {}. Add {fix} {place}.",
                platform.label()
            ));
        }
    }
    if release {
        for d in declared {
            let spec = d.spec();
            let keys = match platform {
                Platform::Ios => spec.ios_keys,
                Platform::Macos => spec.macos_keys,
                Platform::Android => &[],
            };
            if d.text.is_none() && !keys.is_empty() {
                problems.push(format!(
                    "`Bundle.withPermission Bundle.{c}` uses the generic purpose string \
                     {t:?} for {k}. App Review rejects a generic purpose string; a release \
                     states the reason: `|> Bundle.withUsage Bundle.{c} \"<why the app \
                     needs it>\"`.",
                    c = d.ctor,
                    t = spec.default_text,
                    k = keys.join(" / ")
                ));
            }
        }
    }
    if problems.is_empty() {
        Ok(())
    } else {
        Err(format!(
            "missing permission purpose string{}:\n  - {}",
            if problems.len() == 1 { "" } else { "s" },
            problems.join("\n  - ")
        ))
    }
}

/// Everything the shells read from the app's declarations, resolved once.
#[derive(Debug, Default)]
pub struct Declarations {
    pub permissions: Vec<Declared>,
    pub entitlements: Vec<Entitlement>,
    pub capabilities: BTreeSet<&'static str>,
    pub sites: Sites,
}

/// Read the declarations from the entry source (permissions, entitlements) and
/// the capabilities from every source file.
pub fn read_declarations(project_dir: &Path) -> Result<Declarations, String> {
    let entry_rel = crate::entry_rel_path(project_dir);
    let entry = crate::read_entry_source(project_dir).unwrap_or_default();
    let permissions = scan_permissions(&entry).map_err(|e| format!("Std.Bundle: {e}"))?;
    let entitlements = scan_entitlements(&entry).map_err(|e| format!("Std.Bundle: {e}"))?;
    let mut capabilities = BTreeSet::new();
    let mut sites = Sites {
        entry: entry_rel,
        bundle_line: bundle_binding_line(&entry),
        calls: BTreeMap::new(),
    };
    for f in sky_sources(project_dir) {
        if let Ok(src) = std::fs::read_to_string(&f) {
            let rel = f
                .strip_prefix(project_dir)
                .unwrap_or(&f)
                .to_string_lossy()
                .replace('\\', "/");
            for (func, line) in native_capability_lines(&src) {
                capabilities.insert(func);
                sites.calls.entry(func).or_insert((rel.clone(), line));
            }
        }
    }
    Ok(Declarations {
        permissions,
        entitlements,
        capabilities,
        sites,
    })
}

/// `Bundle.withBuild <n>` — the store build number (CFBundleVersion /
/// `android:versionCode`). `Ok(None)` when absent; an error when present but
/// not a positive integer literal.
pub fn scan_build_number(src: &str) -> Result<Option<u32>, String> {
    let mut found = None;
    for after in call_sites(src, "withBuild") {
        let arg = skip_ws_parens(&src[after..]);
        if arg.starts_with([':', ',', ')']) {
            continue; // a signature, or an import's exposing list
        }
        let digits: String = arg.chars().take_while(|c| c.is_ascii_digit()).collect();
        match digits.parse::<u32>() {
            Ok(n) if n > 0 => found = Some(n),
            _ => {
                return Err(format!(
                    "line {}: `Bundle.withBuild` needs a positive integer literal (the store \
                     build number, e.g. `Bundle.withBuild 7`).",
                    line_of(src, after)
                ))
            }
        }
    }
    Ok(found)
}

// ─────────────────────────────────────────────────────────────────────────────
// Release packaging (`sky package --release`)
// ─────────────────────────────────────────────────────────────────────────────

/// Set by `sky package --release` to the directory the release artefacts land
/// in. Its presence is what switches the native shell builders from a
/// development build (simulator, debug-signed APK, inspectable web view) to a
/// release build; the child `sky build` legs of a Sky.Spa or Std.App build
/// inherit it with the rest of the environment.
pub const RELEASE_ENV: &str = "SKY_PACKAGE_RELEASE";

/// Signing configuration, read from the environment only: a keystore password
/// never belongs in a tracked file.
pub const IOS_SIGN_IDENTITY: &str = "SKY_IOS_SIGN_IDENTITY";
pub const IOS_PROVISIONING_PROFILE: &str = "SKY_IOS_PROVISIONING_PROFILE";
pub const ANDROID_KEYSTORE: &str = "SKY_ANDROID_KEYSTORE";
pub const ANDROID_KEYSTORE_PASSWORD: &str = "SKY_ANDROID_KEYSTORE_PASSWORD";
pub const ANDROID_KEY_ALIAS: &str = "SKY_ANDROID_KEY_ALIAS";
pub const ANDROID_KEY_PASSWORD: &str = "SKY_ANDROID_KEY_PASSWORD";
pub const MACOS_SIGN_IDENTITY: &str = "SKY_MACOS_SIGN_IDENTITY";
pub const MACOS_PROVISIONING_PROFILE: &str = "SKY_MACOS_PROVISIONING_PROFILE";

fn env_nonempty(name: &str) -> Option<String> {
    std::env::var(name).ok().filter(|v| !v.trim().is_empty())
}

/// The release artefact directory, when this build is a release.
pub fn release_dir() -> Option<PathBuf> {
    env_nonempty(RELEASE_ENV).map(PathBuf::from)
}

/// A release build must load a real backend: the development defaults
/// (`localhost`, the emulator alias, loopback) point at the build machine, which
/// a user's device cannot reach, and plain `http` sends the session in clear
/// text. Both are refused, naming the fix.
pub fn check_release_url(url: &crate::app_url::AppUrl) -> Result<(), String> {
    use crate::app_url::{Source, BUILDER, ENV_VAR};
    let fix = format!(
        "Set the deployed backend's address: `|> {BUILDER} \"https://app.example.com/\"` on \
         the App value, or {ENV_VAR}=https://app.example.com/ when you package."
    );
    if matches!(url.source, Source::Default { .. }) {
        return Err(format!(
            "a release build has no backend address: the shell would load the development \
             default {}, which a user's device cannot reach. {fix}",
            url.url
        ));
    }
    if url.is_local() {
        return Err(format!(
            "a release build loads {} ({}), a local development address a user's device \
             cannot reach. {fix}",
            url.url,
            url.source_label()
        ));
    }
    if url.scheme != "https" {
        return Err(format!(
            "a release build loads {} ({}) over plain http, which sends the session cookie \
             in clear text. Serve the backend over https and point the app at it. {fix}",
            url.url,
            url.source_label()
        ));
    }
    Ok(())
}

/// Android release signing, from the environment. Every value is required:
/// a release is never signed with the debug keystore.
#[derive(Debug, Clone)]
pub struct AndroidSigning {
    pub keystore: PathBuf,
}

pub fn android_signing() -> Result<AndroidSigning, String> {
    let missing: Vec<&str> = [
        ANDROID_KEYSTORE,
        ANDROID_KEYSTORE_PASSWORD,
        ANDROID_KEY_ALIAS,
    ]
    .into_iter()
    .filter(|n| env_nonempty(n).is_none())
    .collect();
    if !missing.is_empty() {
        return Err(format!(
            "an Android release is signed with your upload key, never the debug key. Set {} \
             (and {ANDROID_KEY_PASSWORD} when the key's password differs from the \
             keystore's). Create a key once with:\n    keytool -genkeypair -v -keystore \
             upload.jks -alias upload -keyalg RSA -keysize 2048 -validity 10000",
            missing.join(", ")
        ));
    }
    let keystore = PathBuf::from(env_nonempty(ANDROID_KEYSTORE).unwrap_or_default());
    if !keystore.is_file() {
        return Err(format!(
            "{ANDROID_KEYSTORE}={} is not a file.",
            keystore.display()
        ));
    }
    Ok(AndroidSigning { keystore })
}

/// iOS release signing: `None` builds an unsigned archive (with a note);
/// an identity without a provisioning profile (or the reverse) is an error,
/// because a device build signed without a profile does not install.
#[derive(Debug, Clone)]
pub struct IosSigning {
    pub identity: String,
    pub profile: PathBuf,
}

pub fn ios_signing() -> Result<Option<IosSigning>, String> {
    match (
        env_nonempty(IOS_SIGN_IDENTITY),
        env_nonempty(IOS_PROVISIONING_PROFILE),
    ) {
        (None, None) => Ok(None),
        (Some(identity), Some(profile)) => {
            let profile = PathBuf::from(profile);
            if !profile.is_file() {
                return Err(format!(
                    "{IOS_PROVISIONING_PROFILE}={} is not a file.",
                    profile.display()
                ));
            }
            Ok(Some(IosSigning { identity, profile }))
        }
        (Some(_), None) => Err(format!(
            "{IOS_SIGN_IDENTITY} is set but {IOS_PROVISIONING_PROFILE} is not: a signed iOS \
             build embeds the provisioning profile (a .mobileprovision file from the Apple \
             Developer portal)."
        )),
        (None, Some(_)) => Err(format!(
            "{IOS_PROVISIONING_PROFILE} is set but {IOS_SIGN_IDENTITY} is not: name the \
             signing identity, e.g. \"Apple Distribution: Acme Ltd (TEAMID)\" (see \
             `security find-identity -v -p codesigning`)."
        )),
    }
}

pub fn macos_identity() -> Option<String> {
    env_nonempty(MACOS_SIGN_IDENTITY)
}

/// The macOS provisioning profile (`.provisionprofile`) a Developer ID build
/// embeds when it asks for a restricted entitlement, or `None` when unset.
pub fn macos_profile() -> Result<Option<PathBuf>, String> {
    match env_nonempty(MACOS_PROVISIONING_PROFILE) {
        None => Ok(None),
        Some(p) => {
            let p = PathBuf::from(p);
            if p.is_file() {
                Ok(Some(p))
            } else {
                Err(format!(
                    "{MACOS_PROVISIONING_PROFILE}={} is not a file.",
                    p.display()
                ))
            }
        }
    }
}

/// How a macOS `.app` is signed, given the requested entitlements.
#[derive(Debug, PartialEq)]
pub enum MacSigning {
    /// Ad hoc: the signature carries only `entitlements`, and `dropped` are
    /// the restricted ones the ad hoc signature cannot carry (the build says
    /// so in a note).
    AdHoc {
        entitlements: Vec<(String, Value)>,
        dropped: Vec<String>,
    },
    /// A Developer ID identity, with the profile to embed when a restricted
    /// entitlement needs one.
    Identity {
        identity: String,
        profile: Option<PathBuf>,
    },
}

/// Decide the macOS signing. An ad hoc signature never carries a restricted
/// entitlement: the kernel refuses to launch such a binary (see
/// [`is_restricted_entitlement`]), so the app would die on start. Without an
/// identity those entitlements are dropped and named; the app still runs on
/// this Mac (a non-sandboxed app reaches the login Keychain without an access
/// group). With an identity, a restricted entitlement needs a provisioning
/// profile that grants it, embedded in the `.app`.
pub fn macos_signing(
    identity: Option<String>,
    profile: Option<PathBuf>,
    requested: &[(String, Value)],
) -> Result<MacSigning, String> {
    let (signable, restricted) = split_restricted(requested);
    match identity {
        None => {
            if profile.is_some() {
                return Err(format!(
                    "{MACOS_PROVISIONING_PROFILE} is set but {MACOS_SIGN_IDENTITY} is not: a \
                     provisioning profile is embedded in a build signed with a Developer ID \
                     identity."
                ));
            }
            Ok(MacSigning::AdHoc {
                entitlements: signable,
                dropped: restricted,
            })
        }
        Some(identity) => {
            if !restricted.is_empty() && profile.is_none() {
                return Err(format!(
                    "the app asks for {} on macOS, which a Developer ID signature carries \
                     only with a provisioning profile that grants it. Set \
                     {MACOS_PROVISIONING_PROFILE} to the app's `.provisionprofile` from the \
                     Apple Developer portal.",
                    restricted.join(", ")
                ));
            }
            Ok(MacSigning::Identity { identity, profile })
        }
    }
}

/// The entitlements a provisioning profile grants, read with
/// `security cms -D -i <profile>` (macOS). Returns the profile's
/// `Entitlements` dictionary.
pub fn profile_entitlements(profile: &Path) -> Result<Vec<(String, Value)>, String> {
    let out = std::process::Command::new("security")
        .args(["cms", "-D", "-i"])
        .arg(profile)
        .output()
        .map_err(|e| format!("run `security cms` on the provisioning profile: {e}"))?;
    if !out.status.success() {
        return Err(format!(
            "`security cms -D -i {}` could not decode the provisioning profile: {}",
            profile.display(),
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    let entries = plist::parse_entries(&String::from_utf8_lossy(&out.stdout))
        .map_err(|e| format!("the provisioning profile is not a property list: {e}"))?;
    match entries.into_iter().find(|(k, _)| k == "Entitlements") {
        Some((_, Value::Dict(d))) => Ok(d),
        _ => Err("the provisioning profile has no Entitlements dictionary".to_string()),
    }
}

/// Split a profile's entitlements into the identity keys a signed build must
/// carry verbatim (generated rank) and check that every key the app asks for is
/// one the profile grants. Returns the generated layer entries.
pub fn signed_entitlements(
    profile: &[(String, Value)],
    requested: &[(String, Value)],
) -> Result<Vec<(String, Value)>, String> {
    let identity_keys = [
        "application-identifier",
        "com.apple.developer.team-identifier",
        "get-task-allow",
    ];
    let generated: Vec<(String, Value)> = profile
        .iter()
        .filter(|(k, _)| identity_keys.contains(&k.as_str()))
        .cloned()
        .collect();
    let not_granted: Vec<&str> = requested
        .iter()
        .map(|(k, _)| k.as_str())
        .filter(|k| !identity_keys.contains(k) && !profile.iter().any(|(pk, _)| pk == k))
        .collect();
    if !not_granted.is_empty() {
        return Err(format!(
            "the provisioning profile does not grant {}. Enable the capability for the \
             app id in the Apple Developer portal and download the profile again.",
            not_granted.join(", ")
        ));
    }
    Ok(generated)
}

// ─────────────────────────────────────────────────────────────────────────────
// The Android code scanner's decoder (ZXing)
// ─────────────────────────────────────────────────────────────────────────────

/// ZXing core, the barcode decoder the Android shell's `Native.scanCode` uses:
/// a plain Java jar (Apache License 2.0) with no Android resources, native
/// libraries or Play services, so the SDK-tools pipeline (`javac` + `d8`)
/// compiles it in as it is. Pinned by version and SHA-256 (the Maven Central
/// artefact; its published SHA-1 is ca1349214a356cd7958651b2d5a0e1f3811a9c4b).
pub const ZXING_VERSION: &str = "3.5.3";
pub const ZXING_JAR_NAME: &str = "zxing-core-3.5.3.jar";
pub const ZXING_URL: &str =
    "https://repo1.maven.org/maven2/com/google/zxing/core/3.5.3/core-3.5.3.jar";
pub const ZXING_SHA256: &str = "8d8064c1636fdaef7189dd9055c7d59950a8940a12f2293956446ec3c109fd82";

/// The ZXing jar, from the cache (`~/.cache/sky/android/`) or fetched once from
/// Maven Central. A download whose SHA-256 is not the pinned one is refused
/// and removed. Only an app that calls `Native.scanCode` needs it.
pub fn zxing_jar() -> Result<PathBuf, String> {
    let dir = crate::bundled::cache_root().join("android");
    let jar = dir.join(ZXING_JAR_NAME);
    let ok = |p: &Path| {
        std::fs::read(p)
            .map(|b| crate::db_provision::sha256_hex(&b) == ZXING_SHA256)
            .unwrap_or(false)
    };
    if ok(&jar) {
        return Ok(jar);
    }
    std::fs::create_dir_all(&dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    let part = dir.join(format!("{ZXING_JAR_NAME}.part"));
    let _ = std::fs::remove_file(&part);
    eprintln!("  Native.scanCode: fetching the ZXing {ZXING_VERSION} decoder (Apache-2.0) from Maven Central");
    let st = std::process::Command::new("curl")
        .args(["-fsSL", "--retry", "2", "--max-time", "300", "-o"])
        .arg(&part)
        .arg(ZXING_URL)
        .status()
        .map_err(|e| format!("could not run curl ({e}); is curl installed?"))?;
    if !st.success() {
        let _ = std::fs::remove_file(&part);
        return Err(format!(
            "Native.scanCode: could not download the ZXing decoder from {ZXING_URL} (curl \
             exit {}). The Android scanner needs it. Without network, put the jar (SHA-256 \
             {ZXING_SHA256}) at {} and build again.",
            st.code()
                .map(|c| c.to_string())
                .unwrap_or_else(|| "?".into()),
            jar.display()
        ));
    }
    if !ok(&part) {
        let _ = std::fs::remove_file(&part);
        return Err(format!(
            "Native.scanCode: the file downloaded from {ZXING_URL} does not have the pinned \
             SHA-256 {ZXING_SHA256}; it was removed and nothing was built with it."
        ));
    }
    std::fs::rename(&part, &jar).map_err(|e| format!("move {}: {e}", jar.display()))?;
    Ok(jar)
}

/// Copy a finished artefact (file or bundle directory) into the release dir.
pub fn publish_artefact(from: &Path, release: &Path) -> Result<PathBuf, String> {
    std::fs::create_dir_all(release).map_err(|e| format!("create {}: {e}", release.display()))?;
    let name = from
        .file_name()
        .ok_or_else(|| format!("{} has no file name", from.display()))?;
    let to = release.join(name);
    if to.is_dir() {
        std::fs::remove_dir_all(&to).map_err(|e| format!("remove {}: {e}", to.display()))?;
    } else {
        let _ = std::fs::remove_file(&to);
    }
    if from.is_dir() {
        // `ditto` keeps a bundle's symlinks, modes and code signature intact.
        let st = std::process::Command::new("ditto")
            .arg(from)
            .arg(&to)
            .status()
            .map_err(|e| format!("run ditto: {e}"))?;
        if !st.success() {
            return Err(format!("ditto {} {} failed", from.display(), to.display()));
        }
    } else {
        std::fs::copy(from, &to).map_err(|e| format!("copy {}: {e}", from.display()))?;
    }
    Ok(to)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn with_usage_sets_the_purpose_string_and_wins_over_with_permission() {
        let src = "import Std.Bundle as Bundle exposing (Bundle, withUsage, withPermission)\n\
                   bundle =\n    Bundle.default\n        |> Bundle.withPermission Bundle.Camera\n\
                   \x20       |> Bundle.withUsage Bundle.Camera \"Scans QR codes to pair.\"\n\
                   \x20       |> Bundle.withUsage FaceId\n            \"Confirms it is you.\"\n\
                   \x20       |> Bundle.withPermission Bundle.Notifications\n\
                   -- |> Bundle.withUsage Bundle.Contacts \"commented out\"\n";
        let d = scan_permissions(src).expect("scan");
        assert_eq!(
            d,
            vec![
                Declared {
                    ctor: "Camera".into(),
                    text: Some("Scans QR codes to pair.".into())
                },
                Declared {
                    ctor: "FaceId".into(),
                    text: Some("Confirms it is you.".into())
                },
                Declared {
                    ctor: "Notifications".into(),
                    text: None
                },
            ]
        );
        let ios = usage_entries(&d, false);
        assert_eq!(
            ios,
            vec![
                (
                    "NSCameraUsageDescription".to_string(),
                    Value::String("Scans QR codes to pair.".into())
                ),
                (
                    "NSFaceIDUsageDescription".to_string(),
                    Value::String("Confirms it is you.".into())
                ),
            ]
        );
        // macOS has a camera key but no Face ID key.
        assert_eq!(usage_entries(&d, true).len(), 1);
        assert_eq!(
            android_permissions(&d),
            vec![
                "android.permission.CAMERA",
                "android.permission.USE_BIOMETRIC",
                "android.permission.POST_NOTIFICATIONS"
            ]
        );
        // USE_BIOMETRIC is a normal permission: no run-time request.
        assert!(!android_runtime_permissions(&d).contains(&"android.permission.USE_BIOMETRIC"));
    }

    #[test]
    fn a_computed_or_empty_purpose_string_is_an_error() {
        let e = scan_permissions("b = Bundle.withUsage Bundle.Camera why\n").unwrap_err();
        assert!(e.contains("string literal"), "{e}");
        let e = scan_permissions("b = Bundle.withUsage Bundle.Camera \"  \"\n").unwrap_err();
        assert!(e.contains("must say"), "{e}");
        let e = scan_permissions("b = Bundle.withUsage Bundle.Teleport \"x\"\n").unwrap_err();
        assert!(e.contains("not a Std.Bundle permission"), "{e}");
        let e = scan_permissions("b = Bundle.withUsage perm \"x\"\n").unwrap_err();
        assert!(e.contains("statically"), "{e}");
        // The signature in Std.Bundle itself is not a use.
        assert!(
            scan_permissions("withUsage : Permission -> String -> Bundle -> Bundle\n")
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn typed_entitlements_render_to_the_apple_keys() {
        let src = "import Std.Bundle as Bundle exposing (withEntitlement)\n\
                   bundle =\n    Bundle.default\n\
                   \x20       |> Bundle.withEntitlement (Bundle.AppGroup \"group.com.acme.app\")\n\
                   \x20       |> Bundle.withEntitlement (Bundle.AssociatedDomain \"applinks:acme.com\")\n\
                   \x20       |> Bundle.withEntitlement (Bundle.AssociatedDomain \"webcredentials:acme.com\")\n\
                   \x20       |> Bundle.withEntitlement (Bundle.PushNotifications Bundle.PushProduction)\n\
                   \x20       |> Bundle.withEntitlement (Bundle.KeychainAccessGroup \"ABCDE12345.com.acme.shared\")\n\
                   \x20       |> Bundle.withEntitlement (Bundle.ICloudContainer \"iCloud.com.acme.app\")\n";
        let ents = scan_entitlements(src).expect("scan");
        assert_eq!(ents.len(), 6);
        let doc = plist::render_document(&entitlement_entries(&ents));
        let back = plist::parse_entries(&doc).expect("valid plist");
        let get = |k: &str| back.iter().find(|(e, _)| e == k).map(|(_, v)| v.clone());
        assert_eq!(
            get("com.apple.developer.associated-domains"),
            Some(Value::Array(vec![
                Value::String("applinks:acme.com".into()),
                Value::String("webcredentials:acme.com".into())
            ]))
        );
        assert_eq!(
            get("aps-environment"),
            Some(Value::String("production".into()))
        );
        assert!(get("com.apple.security.application-groups").is_some());
        assert!(get("keychain-access-groups").is_some());
        assert_eq!(
            get("com.apple.developer.icloud-services"),
            Some(Value::Array(vec![Value::String("CloudKit".into())]))
        );
    }

    #[test]
    fn malformed_entitlements_are_refused_with_the_fix() {
        for (src, needle) in [
            (
                "b = Bundle.withEntitlement (Bundle.AppGroup \"com.acme\")",
                "group.",
            ),
            (
                "b = Bundle.withEntitlement (Bundle.AssociatedDomain \"acme.com\")",
                "applinks:",
            ),
            (
                "b = Bundle.withEntitlement (Bundle.ICloudContainer \"com.acme\")",
                "iCloud.",
            ),
            ("b = Bundle.withEntitlement myEnt", "statically"),
            (
                "b = Bundle.withEntitlement (Bundle.AppGroup groupName)",
                "string literal",
            ),
            (
                "b = Bundle.withEntitlement (Bundle.PushNotifications env)",
                "PushProduction",
            ),
        ] {
            let e = scan_entitlements(src).expect_err(src);
            assert!(e.contains(needle), "{src}: {e}");
        }
    }

    #[test]
    fn capability_detection_follows_the_import_alias_and_exposing_list() {
        let qualified = "import Std.Native as Native\n\nx = Native.authenticate \"Unlock\"\n";
        assert_eq!(
            native_capabilities_used(qualified),
            BTreeSet::from(["authenticate"])
        );
        let exposed = "import Std.Native exposing (capturePhoto, share)\n\ny = capturePhoto ()\n";
        assert_eq!(
            native_capabilities_used(exposed),
            BTreeSet::from(["capturePhoto"])
        );
        let unaliased = "import Std.Native\n\nz = Std.Native.geolocation ()\n";
        assert_eq!(
            native_capabilities_used(unaliased),
            BTreeSet::from(["geolocation"])
        );
        // Not imported, commented out, or a different module's function.
        for src in [
            "x = Native.authenticate \"u\"\n",
            "import Std.Native as Native\n-- x = Native.authenticate \"u\"\n",
            "import Std.Native as Native\nimport My.Auth as Auth\nx = Auth.authenticate 1\n",
            "import Std.Native as N\nx = authenticate 1\n",
        ] {
            assert!(native_capabilities_used(src).is_empty(), "{src}");
        }
    }

    /// `Native.notify` without `Bundle.Notifications` fails the Android build:
    /// Android 13 and later refuses an undeclared POST_NOTIFICATIONS without a
    /// prompt, so every notification was lost. The fix names `withPermission`
    /// (the permission has no purpose string). iOS needs no declaration.
    #[test]
    fn native_notify_needs_the_notifications_permission_on_android_only() {
        let used = BTreeSet::from(["notify"]);
        let e = check_usage(&used, &[], Platform::Android, false, &Sites::default())
            .expect_err("Android must refuse Native.notify without POST_NOTIFICATIONS");
        assert!(
            e.contains("Native.notify")
                && e.contains("android.permission.POST_NOTIFICATIONS")
                && e.contains("`|> Bundle.withPermission Bundle.Notifications`"),
            "{e}"
        );
        // iOS and macOS ask with no declaration.
        for p in [Platform::Ios, Platform::Macos] {
            assert!(e_ok(check_usage(&used, &[], p, true, &Sites::default())));
        }
        let declared = vec![Declared {
            ctor: "Notifications".into(),
            text: None,
        }];
        for release in [false, true] {
            assert!(e_ok(check_usage(
                &used,
                &declared,
                Platform::Android,
                release,
                &Sites::default()
            )));
        }
    }

    /// Regression for the missing-purpose-string rule: `Native.authenticate`
    /// without Face ID declared fails the iOS and Android builds, naming the
    /// builder that fixes it. Declared, it passes; a release additionally needs
    /// the app's own text, not the generic default.
    #[test]
    fn a_capability_without_its_permission_is_refused_naming_the_fix() {
        let used = BTreeSet::from(["authenticate"]);
        for p in [Platform::Ios, Platform::Android] {
            let e = check_usage(&used, &[], p, false, &Sites::default()).expect_err("must refuse");
            assert!(
                e.contains("Native.authenticate") && e.contains("Bundle.withUsage Bundle.FaceId"),
                "{e}"
            );
        }
        assert!(e_ok(check_usage(
            &used,
            &[],
            Platform::Macos,
            false,
            &Sites::default()
        )));
        let bare = vec![Declared {
            ctor: "FaceId".into(),
            text: None,
        }];
        assert!(e_ok(check_usage(
            &used,
            &bare,
            Platform::Ios,
            false,
            &Sites::default()
        )));
        let e =
            check_usage(&used, &bare, Platform::Ios, true, &Sites::default()).expect_err("release");
        assert!(
            e.contains("App Review") && e.contains("NSFaceIDUsageDescription"),
            "{e}"
        );
        // Android has no purpose-string keys, so a release there only needs the
        // permission.
        assert!(e_ok(check_usage(
            &used,
            &bare,
            Platform::Android,
            true,
            &Sites::default()
        )));
        let own = vec![Declared {
            ctor: "FaceId".into(),
            text: Some("Unlocks your vault.".into()),
        }];
        assert!(e_ok(check_usage(
            &used,
            &own,
            Platform::Ios,
            true,
            &Sites::default()
        )));
    }

    /// The missing-purpose-string error points at the USER's files: the line
    /// that calls the capability and the `bundle` binding to fix, and names
    /// the builder. Before v0.27.0 an `App.app` entry reported it from the
    /// derived client project, ending with "the failure above is in the
    /// SYNTHESISED client entry".
    #[test]
    fn a_missing_purpose_string_names_the_call_and_the_bundle_binding() {
        let used = BTreeSet::from(["authenticate", "scanCode"]);
        let sites = Sites {
            entry: "src/Main.sky".into(),
            bundle_line: Some(14),
            calls: BTreeMap::from([
                ("authenticate", ("src/Main.sky".to_string(), 40)),
                ("scanCode", ("src/Page/Pair.sky".to_string(), 7)),
            ]),
        };
        let e = check_usage(&used, &[], Platform::Ios, false, &sites).expect_err("refuse");
        assert!(
            e.contains("`Native.authenticate` at src/Main.sky:40")
                && e.contains("Bundle.withUsage Bundle.FaceId")
                && e.contains("`bundle` binding at src/Main.sky:14"),
            "{e}"
        );
        assert!(
            e.contains("`Native.scanCode` at src/Page/Pair.sky:7")
                && e.contains("Bundle.withUsage Bundle.Camera"),
            "{e}"
        );
        assert!(!e.contains("SYNTHESISED"), "{e}");
        // No `bundle` binding yet: say how to add one.
        let none = Sites {
            bundle_line: None,
            ..sites
        };
        let e = check_usage(&used, &[], Platform::Android, false, &none).expect_err("refuse");
        assert!(
            e.contains("to a `bundle` binding in src/Main.sky")
                && e.contains("bundle = Bundle.default |> Bundle.withUsage Bundle.FaceId"),
            "{e}"
        );
        let camera = vec![Declared {
            ctor: "Camera".into(),
            text: Some("Scans the pairing code.".into()),
        }];
        let e = check_usage(&used, &camera, Platform::Ios, false, &Sites::default())
            .expect_err("FaceId still missing");
        assert!(!e.contains("scanCode"), "{e}");
    }

    #[test]
    fn capability_lines_skip_the_import_exposing_list() {
        let src = "import Std.Native as Native exposing (scanCode)\n\n\
                   a = 1\n\nb = scanCode opts\n\nc = Native.scanCode opts\n";
        assert_eq!(
            native_capability_lines(src),
            BTreeMap::from([("scanCode", 5)])
        );
        // Exposed but never called is not a use.
        let src = "import Std.Native exposing (authenticate)\n\nx = 1\n";
        assert!(native_capability_lines(src).is_empty());
    }

    #[test]
    fn the_bundle_binding_line_is_found() {
        let src = "module Main exposing (main, bundle)\n\nbundle : Bundle\nbundle =\n    Bundle.default\n";
        assert_eq!(bundle_binding_line(src), Some(4));
        assert_eq!(bundle_binding_line("bundles = 1\nmain = 0\n"), None);
    }

    /// The restricted entitlements are exactly the ones measured to stop an ad
    /// hoc signed launch (AMFI -424); app groups and get-task-allow are not.
    #[test]
    fn restricted_entitlements_are_the_profile_backed_keys() {
        for k in [
            "keychain-access-groups",
            "application-identifier",
            "aps-environment",
            "com.apple.developer.associated-domains",
            "com.apple.developer.icloud-container-identifiers",
            "com.apple.developer.icloud-services",
        ] {
            assert!(is_restricted_entitlement(k), "{k}");
        }
        for k in [
            "com.apple.security.application-groups",
            "com.apple.security.get-task-allow",
        ] {
            assert!(!is_restricted_entitlement(k), "{k}");
        }
    }

    /// The simulator build embeds what Xcode's `-Simulated.xcent` holds: the
    /// declared entitlements, the application identifier, and a keychain
    /// access group for the app's own id when none is declared.
    #[test]
    fn simulated_entitlements_add_the_app_identifier_and_keychain_group() {
        let s = |v: &str| Value::String(v.into());
        // Nothing declared: the Keychain still gets an access group.
        let sim = simulated_entitlements("com.example.probe", &[]);
        assert_eq!(
            sim,
            vec![
                (
                    "application-identifier".to_string(),
                    s("SKYSIMTEAM.com.example.probe")
                ),
                (
                    "keychain-access-groups".to_string(),
                    Value::Array(vec![s("SKYSIMTEAM.com.example.probe")])
                ),
            ]
        );
        // A declared group keeps its place (it is the default access group, as
        // on a device), and its team prefix becomes the application
        // identifier's.
        let declared = entitlement_entries(&[
            Entitlement::KeychainAccessGroup("ABCDE12345.com.example.probe".into()),
            Entitlement::AssociatedDomain("applinks:example.com".into()),
        ]);
        let sim = simulated_entitlements("com.example.probe", &declared);
        assert_eq!(
            sim[0],
            (
                "application-identifier".to_string(),
                s("ABCDE12345.com.example.probe")
            )
        );
        assert_eq!(&sim[1..], &declared[..]);
    }

    #[test]
    fn an_ad_hoc_mac_signature_drops_restricted_entitlements() {
        let requested = entitlement_entries(&[
            Entitlement::KeychainAccessGroup("ABCDE12345.com.acme.vault".into()),
            Entitlement::AppGroup("group.com.acme.vault".into()),
        ]);
        match macos_signing(None, None, &requested).unwrap() {
            MacSigning::AdHoc {
                entitlements,
                dropped,
            } => {
                assert_eq!(dropped, vec!["keychain-access-groups".to_string()]);
                assert_eq!(
                    entitlements
                        .iter()
                        .map(|(k, _)| k.as_str())
                        .collect::<Vec<_>>(),
                    vec!["com.apple.security.application-groups"]
                );
            }
            other => panic!("{other:?}"),
        }
        // A Developer ID signature with a restricted entitlement needs the
        // profile; without one the build refuses, naming the variable.
        let e = macos_signing(
            Some("Developer ID Application: Acme".into()),
            None,
            &requested,
        )
        .expect_err("needs a profile");
        assert!(
            e.contains(MACOS_PROVISIONING_PROFILE) && e.contains("keychain-access-groups"),
            "{e}"
        );
        let p = PathBuf::from("/tmp/x.provisionprofile");
        assert!(matches!(
            macos_signing(
                Some("Developer ID Application: Acme".into()),
                Some(p.clone()),
                &requested
            ),
            Ok(MacSigning::Identity {
                profile: Some(_),
                ..
            })
        ));
        assert!(macos_signing(None, Some(p), &requested).is_err());
    }

    fn e_ok(r: Result<(), String>) -> bool {
        r.is_ok()
    }

    #[test]
    fn build_number_reads_a_positive_literal_only() {
        assert_eq!(
            scan_build_number("b = Bundle.withBuild 42\n").unwrap(),
            Some(42)
        );
        assert_eq!(scan_build_number("b = Bundle.default\n").unwrap(), None);
        assert_eq!(
            scan_build_number("import Std.Bundle exposing (withBuild)\n").unwrap(),
            None
        );
        assert!(scan_build_number("b = Bundle.withBuild n\n").is_err());
        assert!(scan_build_number("b = Bundle.withBuild 0\n").is_err());
    }

    /// Regression: the old generator appended `native/android/permissions.xml`
    /// fragments as text, so a permission two sources declared appeared twice
    /// in the manifest. The structured merge keeps one, drops the ones Sky
    /// already declares, and refuses two dependencies that disagree.
    #[test]
    fn android_fragments_merge_structurally() {
        let dir = std::env::temp_dir().join(format!(
            "sky-native-frag-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let own = dir.join("native/android");
        let dep_a = dir.join(".skydeps/a/native/android");
        let dep_b = dir.join(".skydeps/b/native/android");
        for d in [&own, &dep_a, &dep_b] {
            std::fs::create_dir_all(d).unwrap();
        }
        std::fs::write(
            own.join("permissions.xml"),
            "<uses-permission android:name=\"android.permission.NFC\" />\n\
             <uses-permission android:name=\"android.permission.CAMERA\" />",
        )
        .unwrap();
        std::fs::write(
            dep_a.join("permissions.xml"),
            "<!-- a payments lib -->\n<uses-permission android:name=\"android.permission.NFC\"/>\n\
             <uses-feature android:name=\"android.hardware.nfc\" android:required=\"false\"/>",
        )
        .unwrap();
        std::fs::write(
            dep_b.join("permissions.xml"),
            "<uses-feature android:name=\"android.hardware.nfc\" android:required=\"false\" />",
        )
        .unwrap();
        let dirs = vec![own.clone(), dep_a.clone(), dep_b.clone()];
        let frags = fragments(
            &dirs.iter().map(|d| d.to_path_buf()).collect::<Vec<_>>(),
            &dir,
            "permissions.xml",
        );
        let old_text: String = frags
            .iter()
            .map(|f| std::fs::read_to_string(&f.path).unwrap())
            .collect();
        assert_eq!(
            old_text.matches("android.permission.NFC").count(),
            2,
            "the old text join duplicated the permission"
        );
        let merged =
            merge_android_fragments(&frags, &dir, &["android.permission.CAMERA"]).expect("merge");
        assert_eq!(
            merged.matches("android.permission.NFC").count(),
            1,
            "{merged}"
        );
        assert_eq!(
            merged.matches("android.hardware.nfc").count(),
            1,
            "{merged}"
        );
        assert!(
            !merged.contains("CAMERA"),
            "Sky already declares CAMERA:\n{merged}"
        );

        // Two dependencies that disagree on the same feature are an error.
        std::fs::write(
            dep_b.join("permissions.xml"),
            "<uses-feature android:name=\"android.hardware.nfc\" android:required=\"true\" />",
        )
        .unwrap();
        let e = merge_android_fragments(&frags, &dir, &[]).expect_err("conflict");
        assert!(
            e.contains("android.hardware.nfc") && e.contains("project's own"),
            "{e}"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn release_url_refuses_local_default_and_plain_http() {
        use crate::app_url::{resolve, Shell};
        let ok = resolve(Shell::Ios, None, Some("https://app.example.com/"), None).unwrap();
        assert!(check_release_url(&ok).is_ok());
        let dflt = resolve(Shell::Ios, None, None, None).unwrap();
        let e = check_release_url(&dflt).unwrap_err();
        assert!(
            e.contains("App.withAppUrl") && e.contains("SKY_APP_URL"),
            "{e}"
        );
        let local = resolve(Shell::Android, Some("https://localhost:8443/"), None, None).unwrap();
        assert!(check_release_url(&local).unwrap_err().contains("local"));
        let http = resolve(Shell::Desktop, None, Some("http://app.example.com/"), None).unwrap();
        assert!(check_release_url(&http).unwrap_err().contains("plain http"));
    }

    #[test]
    fn signed_entitlements_take_identity_from_the_profile_and_check_grants() {
        let profile = plist::parse_entries(
            "<key>application-identifier</key><string>TEAM.com.acme.app</string>\
             <key>com.apple.developer.team-identifier</key><string>TEAM</string>\
             <key>get-task-allow</key><false/>\
             <key>aps-environment</key><string>production</string>",
        )
        .unwrap();
        let requested = vec![(
            "aps-environment".to_string(),
            Value::String("production".into()),
        )];
        let gen = signed_entitlements(&profile, &requested).expect("granted");
        assert_eq!(gen.len(), 3);
        let requested = vec![(
            "com.apple.security.application-groups".to_string(),
            Value::Array(vec![]),
        )];
        let e = signed_entitlements(&profile, &requested).unwrap_err();
        assert!(e.contains("application-groups"), "{e}");
    }

    /// The Rust table and the Sky `Permission` type must list the same
    /// constructors, or a declared permission silently maps to nothing.
    #[test]
    fn permission_table_matches_the_stdlib() {
        let src = include_str!("../../../../sky-stdlib/Std/Bundle.sky");
        let start = src.find("type Permission").expect("type Permission");
        let body = &src[start..];
        let end = body[1..].find("\n\n").map(|n| n + 1).unwrap_or(body.len());
        let ctors: Vec<&str> = body[..end]
            .split(['=', '|'])
            .skip(1)
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .collect();
        let table: Vec<&str> = PERMISSIONS.iter().map(|p| p.ctor).collect();
        assert_eq!(ctors, table);
    }

    #[test]
    fn associated_domains_parse_host_port_mode_and_wildcard() {
        let d = parse_associated_domain("applinks:Example.com").unwrap();
        assert_eq!(
            (d.service.as_str(), d.host.as_str(), d.port),
            ("applinks", "example.com", None)
        );
        let d = parse_associated_domain("applinks:*.example.com:8443?mode=developer").unwrap();
        assert_eq!((d.host.as_str(), d.port), ("*.example.com", Some(8443)));
        assert!(
            parse_associated_domain("webcredentials:a.example.com?mode=developer+managed")
                .is_some()
        );
        for bad in [
            "applinks:",
            "applinks:exa mple.com",
            "applinks:-x.com",
            "applinks:x.com:0",
            "applinks:x.com:port",
            "applinks:x.com?mode=other",
            "applinks:x.com/path",
            "applinks:x.com\"><evil",
        ] {
            assert!(parse_associated_domain(bad).is_none(), "{bad}");
        }
        // The build refuses one before any platform sees it.
        let e = scan_entitlements(
            "b = Bundle.withEntitlement (Bundle.AssociatedDomain \"applinks:x.com/path\")",
        )
        .unwrap_err();
        assert!(e.contains("<service>:<host>"), "{e}");
    }

    #[test]
    fn associated_domains_map_onto_android_with_nothing_dropped_silently() {
        let links = link_domains(&[
            Entitlement::AssociatedDomain("applinks:example.com".into()),
            Entitlement::AssociatedDomain("applinks:example.com?mode=developer".into()),
            Entitlement::AssociatedDomain("applinks:*.shop.example".into()),
            Entitlement::AssociatedDomain("webcredentials:example.com".into()),
            Entitlement::AssociatedDomain("activitycontinuation:example.com".into()),
            Entitlement::AssociatedDomain("appclips:example.com".into()),
            Entitlement::AppGroup("group.com.example".into()),
        ]);
        let hosts: Vec<&str> = links.app_links.iter().map(|d| d.host.as_str()).collect();
        assert_eq!(
            hosts,
            vec!["example.com", "*.shop.example"],
            "one filter per host"
        );
        assert_eq!(links.login_hosts, vec!["example.com".to_string()]);
        assert_eq!(
            links.unmapped,
            vec![
                "activitycontinuation:example.com".to_string(),
                "appclips:example.com".to_string()
            ]
        );
        assert_eq!(links.hosts(), vec!["example.com", "shop.example"]);
        assert_eq!(
            link_host_literals(&links),
            "\"example.com\", \"*.shop.example\""
        );
        assert_eq!(
            android_asset_statements(&links).as_deref(),
            Some("[{\"include\": \"https://example.com/.well-known/assetlinks.json\"}]")
        );
    }

    #[test]
    fn the_app_links_filter_is_a_verified_https_view_filter() {
        let links = link_domains(&[
            Entitlement::AssociatedDomain("applinks:example.com".into()),
            Entitlement::AssociatedDomain("applinks:example.org:8443".into()),
        ]);
        let xml = android_link_filters(&links);
        let nodes = xmlmini::parse_nodes(&xml).expect("valid XML");
        assert_eq!(nodes.len(), 2);
        for (n, (host, port)) in nodes
            .iter()
            .zip([("example.com", None), ("example.org", Some("8443"))])
        {
            let Node::Element(f) = n else { panic!("{xml}") };
            assert_eq!(f.name, "intent-filter");
            assert_eq!(f.attr("android:autoVerify"), Some("true"));
            let kids: Vec<(&str, Option<&str>)> = f
                .elements()
                .map(|e| (e.name.as_str(), e.attr("android:name")))
                .collect();
            assert_eq!(
                kids,
                vec![
                    ("action", Some("android.intent.action.VIEW")),
                    ("category", Some("android.intent.category.DEFAULT")),
                    ("category", Some("android.intent.category.BROWSABLE")),
                    ("data", None),
                ]
            );
            let data = f.elements().last().unwrap();
            assert_eq!(data.attr("android:scheme"), Some("https"));
            assert_eq!(data.attr("android:host"), Some(host));
            assert_eq!(data.attr("android:port"), port);
        }
        assert_eq!(android_link_filters(&LinkDomains::default()), "");
    }

    #[test]
    fn assetlinks_json_names_the_package_and_the_signing_certificate() {
        let out = "Signer #1 certificate DN: CN=Flow Test, O=Sky, C=GB\n\
                   Signer #1 certificate SHA-256 digest: 0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9\n\
                   Signer #1 certificate SHA-1 digest: 00\n";
        let fp = apksigner_sha256(out).expect("digest");
        assert_eq!(
            fp,
            "0A:1B:2C:3D:4E:5F:60:71:82:93:A4:B5:C6:D7:E8:F9:0A:1B:2C:3D:4E:5F:60:71:82:93:A4:B5:C6:D7:E8:F9"
        );
        assert!(sha256_fingerprint("abc").is_none());
        let links = link_domains(&[
            Entitlement::AssociatedDomain("applinks:example.com".into()),
            Entitlement::AssociatedDomain("webcredentials:example.com".into()),
        ]);
        let json: serde_json::Value =
            serde_json::from_str(&assetlinks_json("com.example.probe", &fp, &links)).expect("JSON");
        assert_eq!(
            json,
            serde_json::json!([{
                "relation": [
                    "delegate_permission/common.handle_all_urls",
                    "delegate_permission/common.get_login_creds"
                ],
                "target": {
                    "namespace": "android_app",
                    "package_name": "com.example.probe",
                    "sha256_cert_fingerprints": [fp]
                }
            }])
        );
    }
}
