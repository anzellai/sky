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

use std::collections::BTreeSet;
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
];

/// The `Std.Native` functions in `CAPABILITY_NEEDS` that `src` calls: a
/// qualified use through the module's alias (`Native.authenticate`, or
/// `Std.Native.authenticate` with no alias), or a bare use of a name the import
/// exposes explicitly. `exposing (..)` counts every name.
pub fn native_capabilities_used(src: &str) -> BTreeSet<&'static str> {
    let mut out = BTreeSet::new();
    for at in call_sites(src, "import") {
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
            let q_used = call_sites(src, &qualified).next().is_some();
            let bare_used = (all || exposing.iter().any(|e| e == func))
                && call_sites(src, func).any(|after| {
                    let start = after - func.len();
                    start == 0 || src.as_bytes()[start - 1] != b'.'
                });
            if q_used || bare_used {
                out.insert(*func);
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
pub fn check_usage(
    used: &BTreeSet<&'static str>,
    declared: &[Declared],
    platform: Platform,
    release: bool,
) -> Result<(), String> {
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
            problems.push(format!(
                "the app calls `Native.{func}`, which needs {what} on {}. Add \
                 `|> Bundle.withUsage Bundle.{ctor} \"<why the app needs it>\"` to the \
                 `bundle` binding in the entry module.",
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
}

/// Read the declarations from the entry source (permissions, entitlements) and
/// the capabilities from every source file.
pub fn read_declarations(project_dir: &Path) -> Result<Declarations, String> {
    let entry = crate::read_entry_source(project_dir).unwrap_or_default();
    let permissions = scan_permissions(&entry).map_err(|e| format!("Std.Bundle: {e}"))?;
    let entitlements = scan_entitlements(&entry).map_err(|e| format!("Std.Bundle: {e}"))?;
    let mut capabilities = BTreeSet::new();
    for f in sky_sources(project_dir) {
        if let Ok(src) = std::fs::read_to_string(&f) {
            capabilities.extend(native_capabilities_used(&src));
        }
    }
    Ok(Declarations {
        permissions,
        entitlements,
        capabilities,
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

    /// Regression for the missing-purpose-string rule: `Native.authenticate`
    /// without Face ID declared fails the iOS and Android builds, naming the
    /// builder that fixes it. Declared, it passes; a release additionally needs
    /// the app's own text, not the generic default.
    #[test]
    fn a_capability_without_its_permission_is_refused_naming_the_fix() {
        let used = BTreeSet::from(["authenticate"]);
        for p in [Platform::Ios, Platform::Android] {
            let e = check_usage(&used, &[], p, false).expect_err("must refuse");
            assert!(
                e.contains("Native.authenticate") && e.contains("Bundle.withUsage Bundle.FaceId"),
                "{e}"
            );
        }
        assert!(e_ok(check_usage(&used, &[], Platform::Macos, false)));
        let bare = vec![Declared {
            ctor: "FaceId".into(),
            text: None,
        }];
        assert!(e_ok(check_usage(&used, &bare, Platform::Ios, false)));
        let e = check_usage(&used, &bare, Platform::Ios, true).expect_err("release");
        assert!(
            e.contains("App Review") && e.contains("NSFaceIDUsageDescription"),
            "{e}"
        );
        // Android has no purpose-string keys, so a release there only needs the
        // permission.
        assert!(e_ok(check_usage(&used, &bare, Platform::Android, true)));
        let own = vec![Declared {
            ctor: "FaceId".into(),
            text: Some("Unlocks your vault.".into()),
        }];
        assert!(e_ok(check_usage(&used, &own, Platform::Ios, true)));
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
}
