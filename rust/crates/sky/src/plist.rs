//! Apple property lists as a tree, and a structured merge for the native shells.
//!
//! An iOS shell's `Info.plist` and `.entitlements` are built from several
//! sources: the keys Sky generates (identity, version, transport security), the
//! app's own `Std.Bundle` declarations (`withUsage`, `withEntitlement`), the
//! project's `native/ios/` fragments and every Sky dependency's fragments.
//!
//! The shell generator used to join those sources as TEXT. That produced two
//! kinds of invalid output (both pinned by the regression tests below):
//!
//! * two `app.entitlements` files that are each a whole plist document became
//!   two XML documents in one file, which `codesign` rejects;
//! * a key set by two sources (a library's `Info.plist.append` adding
//!   `NSCameraUsageDescription` next to `Bundle.withPermission Camera`) appeared
//!   twice in one `<dict>`, which is not a valid property list and leaves the
//!   value the OS reads undefined.
//!
//! [`merge`] reads every source into a [`Value`] tree first, then merges them
//! with a defined precedence:
//!
//! 1. Layers are given highest precedence first.
//! 2. A key present in one layer only is taken as it is.
//! 3. Two dictionaries merge key by key, recursively, under the same rules.
//! 4. Two arrays are unioned: the higher layer's items first, then each lower
//!    item not already present.
//! 5. Two equal values are one value.
//! 6. Two different values: the higher layer wins and the override is reported
//!    as a warning, EXCEPT when the higher layer is `locked` (a key Sky
//!    generates, such as `CFBundleIdentifier`), which is an error, and when both
//!    layers are dependencies of equal rank, which is an error too (their order
//!    is the directory order, not a decision anyone made); the fix names the
//!    project's own fragment, which outranks every dependency.

use crate::xmlmini::{self, Element, Node};

/// A property-list value. Dictionaries keep source order so the rendered file
/// is stable and reads in the order it was declared.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Value {
    Dict(Vec<(String, Value)>),
    Array(Vec<Value>),
    String(String),
    /// Kept as written (a plist integer may exceed i64 in principle).
    Integer(String),
    Real(String),
    Bool(bool),
    /// Base64 text, kept as written.
    Data(String),
    Date(String),
}

impl Value {
    fn kind(&self) -> &'static str {
        match self {
            Value::Dict(_) => "dict",
            Value::Array(_) => "array",
            Value::String(_) => "string",
            Value::Integer(_) => "integer",
            Value::Real(_) => "real",
            Value::Bool(_) => "bool",
            Value::Data(_) => "data",
            Value::Date(_) => "date",
        }
    }

    /// A short rendering for a diagnostic.
    pub fn brief(&self) -> String {
        match self {
            Value::String(s) => format!("{s:?}"),
            Value::Integer(s) | Value::Real(s) | Value::Date(s) => s.clone(),
            Value::Bool(b) => b.to_string(),
            Value::Data(_) => "<data>".to_string(),
            Value::Dict(_) | Value::Array(_) => format!("<{}>", self.kind()),
        }
    }
}

/// Read a property-list SOURCE into the entries of its top dictionary. Accepts
/// the three shapes the shells meet:
///
/// * a whole document (`<?xml …?><!DOCTYPE …><plist><dict>…</dict></plist>`);
/// * a bare `<dict>…</dict>`;
/// * a bare run of `<key>…</key><value/>` pairs (the historical
///   `Info.plist.append` shape).
///
/// A key repeated inside one dictionary is an error: it is not a valid plist.
pub fn parse_entries(src: &str) -> Result<Vec<(String, Value)>, String> {
    let nodes = xmlmini::parse_nodes(src)?;
    let elems: Vec<&Element> = nodes
        .iter()
        .map(|n| match n {
            Node::Element(e) => Ok(e),
            Node::Text(t) => Err(format!("unexpected text {:?} outside a key", t.trim())),
        })
        .collect::<Result<_, _>>()?;
    match elems.as_slice() {
        [] => Ok(Vec::new()),
        [root] if root.name == "plist" => {
            let inner: Vec<&Element> = root.elements().collect();
            match inner.as_slice() {
                [d] if d.name == "dict" => dict_entries(d.elements().collect()),
                [] => Ok(Vec::new()),
                _ => Err("a <plist> must hold exactly one <dict>".to_string()),
            }
        }
        [d] if d.name == "dict" => dict_entries(d.elements().collect()),
        many if many.iter().any(|e| e.name == "plist") => Err(format!(
            "{} top-level elements, one of them a whole <plist> document — two \
             documents joined as text are not one property list",
            many.len()
        )),
        pairs => dict_entries(pairs.to_vec()),
    }
}

fn dict_entries(children: Vec<&Element>) -> Result<Vec<(String, Value)>, String> {
    let mut out: Vec<(String, Value)> = Vec::new();
    let mut it = children.into_iter();
    while let Some(k) = it.next() {
        if k.name != "key" {
            return Err(format!("expected <key>, found <{}>", k.name));
        }
        let key = k.text();
        let Some(v) = it.next() else {
            return Err(format!("key `{key}` has no value"));
        };
        if out.iter().any(|(existing, _)| *existing == key) {
            return Err(format!(
                "key `{key}` appears twice in one <dict>, which is not a valid property list"
            ));
        }
        out.push((key, value_of(v)?));
    }
    Ok(out)
}

fn value_of(e: &Element) -> Result<Value, String> {
    Ok(match e.name.as_str() {
        "dict" => Value::Dict(dict_entries(e.elements().collect())?),
        "array" => Value::Array(e.elements().map(value_of).collect::<Result<_, _>>()?),
        "string" => Value::String(e.text()),
        "integer" => Value::Integer(e.text().trim().to_string()),
        "real" => Value::Real(e.text().trim().to_string()),
        "true" => Value::Bool(true),
        "false" => Value::Bool(false),
        "data" => Value::Data(e.text().split_whitespace().collect()),
        "date" => Value::Date(e.text().trim().to_string()),
        other => return Err(format!("<{other}> is not a property-list value")),
    })
}

/// Render a whole property-list document for the top dictionary `entries`.
pub fn render_document(entries: &[(String, Value)]) -> String {
    let mut out = String::from(
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n\
         <!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \
         \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n\
         <plist version=\"1.0\">\n",
    );
    render_value(&Value::Dict(entries.to_vec()), 0, &mut out);
    out.push_str("</plist>\n");
    out
}

fn indent(depth: usize, out: &mut String) {
    for _ in 0..depth {
        out.push_str("    ");
    }
}

fn render_value(v: &Value, depth: usize, out: &mut String) {
    indent(depth, out);
    match v {
        Value::Dict(entries) if entries.is_empty() => out.push_str("<dict/>\n"),
        Value::Dict(entries) => {
            out.push_str("<dict>\n");
            for (k, v) in entries {
                indent(depth + 1, out);
                out.push_str("<key>");
                out.push_str(&xmlmini::escape(k));
                out.push_str("</key>\n");
                render_value(v, depth + 1, out);
            }
            indent(depth, out);
            out.push_str("</dict>\n");
        }
        Value::Array(items) if items.is_empty() => out.push_str("<array/>\n"),
        Value::Array(items) => {
            out.push_str("<array>\n");
            for i in items {
                render_value(i, depth + 1, out);
            }
            indent(depth, out);
            out.push_str("</array>\n");
        }
        Value::String(s) => {
            out.push_str("<string>");
            out.push_str(&xmlmini::escape(s));
            out.push_str("</string>\n");
        }
        Value::Integer(s) => out.push_str(&format!("<integer>{}</integer>\n", xmlmini::escape(s))),
        Value::Real(s) => out.push_str(&format!("<real>{}</real>\n", xmlmini::escape(s))),
        Value::Bool(true) => out.push_str("<true/>\n"),
        Value::Bool(false) => out.push_str("<false/>\n"),
        Value::Data(s) => out.push_str(&format!("<data>{}</data>\n", xmlmini::escape(s))),
        Value::Date(s) => out.push_str(&format!("<date>{}</date>\n", xmlmini::escape(s))),
    }
}

/// How a layer ranks against the others.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Rank {
    /// Keys Sky generates. A different value for one of them is an error.
    Generated,
    /// The app's own `Std.Bundle` declarations.
    Declared,
    /// The project's own `native/<platform>/` fragment.
    Project,
    /// A Sky dependency's fragment. Dependencies share one rank.
    Dependency,
}

/// One source of property-list entries.
#[derive(Debug, Clone)]
pub struct Layer {
    /// Where the entries came from, for diagnostics (a path or a builder name).
    pub origin: String,
    pub rank: Rank,
    pub entries: Vec<(String, Value)>,
}

/// The merged dictionary and the overrides worth telling the user about.
#[derive(Debug)]
pub struct Merged {
    pub entries: Vec<(String, Value)>,
    pub warnings: Vec<String>,
}

/// Merge `layers` (highest precedence first; see the module docs). Returns every
/// conflict at once on failure, so one build names them all.
pub fn merge(layers: &[Layer]) -> Result<Merged, Vec<String>> {
    let mut acc: Vec<(String, (Value, usize))> = Vec::new();
    let mut warnings = Vec::new();
    let mut errors = Vec::new();
    for (li, layer) in layers.iter().enumerate() {
        for (k, v) in &layer.entries {
            match acc.iter_mut().find(|(ek, _)| ek == k) {
                None => acc.push((k.clone(), (v.clone(), li))),
                Some((_, (existing, owner))) => {
                    let merged = merge_value(
                        k,
                        existing,
                        &layers[*owner],
                        v,
                        layer,
                        &mut warnings,
                        &mut errors,
                    );
                    *existing = merged;
                }
            }
        }
    }
    if errors.is_empty() {
        Ok(Merged {
            entries: acc.into_iter().map(|(k, (v, _))| (k, v)).collect(),
            warnings,
        })
    } else {
        Err(errors)
    }
}

fn merge_value(
    path: &str,
    high: &Value,
    high_layer: &Layer,
    low: &Value,
    low_layer: &Layer,
    warnings: &mut Vec<String>,
    errors: &mut Vec<String>,
) -> Value {
    match (high, low) {
        (Value::Dict(h), Value::Dict(l)) => {
            let mut out = h.clone();
            for (k, lv) in l {
                let sub = format!("{path}.{k}");
                match out.iter_mut().find(|(ok, _)| ok == k) {
                    None => out.push((k.clone(), lv.clone())),
                    Some((_, hv)) => {
                        let m = merge_value(&sub, hv, high_layer, lv, low_layer, warnings, errors);
                        *hv = m;
                    }
                }
            }
            Value::Dict(out)
        }
        (Value::Array(h), Value::Array(l)) => {
            let mut out = h.clone();
            for item in l {
                if !out.contains(item) {
                    out.push(item.clone());
                }
            }
            Value::Array(out)
        }
        (h, l) if h == l => h.clone(),
        (h, l) => {
            let what = format!(
                "`{path}`: {} sets {}, {} sets {}",
                high_layer.origin,
                h.brief(),
                low_layer.origin,
                l.brief()
            );
            match (high_layer.rank, low_layer.rank) {
                (Rank::Generated, _) => errors.push(format!(
                    "{what}. Sky generates this key from the app's Std.Bundle \
                     declarations and build settings; remove it from {}.",
                    low_layer.origin
                )),
                (Rank::Dependency, Rank::Dependency) => errors.push(format!(
                    "{what}. Two dependencies disagree and neither outranks the \
                     other; set the key in the project's own native/ios fragment, \
                     which outranks every dependency."
                )),
                _ => warnings.push(format!("{what}; {} wins.", high_layer.origin)),
            }
            h.clone()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const OWN_ENT: &str = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n\
<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n\
<plist version=\"1.0\"><dict>\n\
<key>com.apple.security.application-groups</key><array><string>group.a</string></array>\n\
</dict></plist>\n";
    const DEP_ENT: &str = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n\
<plist version=\"1.0\"><dict>\n\
<key>com.apple.developer.in-app-payments</key><array><string>merchant.x</string></array>\n\
<key>com.apple.security.application-groups</key><array><string>group.b</string><string>group.a</string></array>\n\
</dict></plist>\n";

    fn layer(origin: &str, rank: Rank, src: &str) -> Layer {
        Layer {
            origin: origin.to_string(),
            rank,
            entries: parse_entries(src).expect(origin),
        }
    }

    /// The old corruption, case 1: the shell generator concatenated the
    /// project's and a dependency's `app.entitlements` as text. Each is a whole
    /// document, so the result is two documents in one file. It does not read
    /// as a property list. The structured merge yields one document with the
    /// arrays unioned.
    #[test]
    fn two_entitlement_documents_joined_as_text_were_invalid_and_now_merge() {
        let old_output = format!("{}\n{}\n", OWN_ENT.trim_end(), DEP_ENT.trim_end());
        assert!(
            parse_entries(&old_output).is_err(),
            "the old text join must read as invalid:\n{old_output}"
        );

        let merged = merge(&[
            layer("native/ios/app.entitlements", Rank::Project, OWN_ENT),
            layer("dep native/ios/app.entitlements", Rank::Dependency, DEP_ENT),
        ])
        .expect("merge");
        let doc = render_document(&merged.entries);
        let back = parse_entries(&doc).expect("the merged document reads back");
        assert_eq!(
            back,
            vec![
                (
                    "com.apple.security.application-groups".to_string(),
                    Value::Array(vec![
                        Value::String("group.a".into()),
                        Value::String("group.b".into())
                    ])
                ),
                (
                    "com.apple.developer.in-app-payments".to_string(),
                    Value::Array(vec![Value::String("merchant.x".into())])
                ),
            ]
        );
        assert_eq!(doc.matches("<?xml").count(), 1);
    }

    /// The old corruption, case 2: a library's `Info.plist.append` set
    /// `NSCameraUsageDescription` next to the key `Bundle.withPermission Camera`
    /// generates. Joined as text, the `<dict>` held the key twice. The merge
    /// keeps one, the app's own declaration wins, and the override is reported.
    #[test]
    fn a_usage_key_set_twice_was_duplicated_and_now_resolves() {
        let generated = "<key>NSCameraUsageDescription</key><string>Scans QR codes.</string>";
        let lib = "<key>NSCameraUsageDescription</key><string>Uses the camera.</string>\n\
                   <key>NSPhotoLibraryUsageDescription</key><string>Picks a photo.</string>";
        let old_dict = format!("<dict>{generated}{lib}</dict>");
        let err = parse_entries(&old_dict).expect_err("old duplicated dict must be invalid");
        assert!(err.contains("appears twice"), "{err}");

        let merged = merge(&[
            layer("Bundle.withUsage", Rank::Declared, generated),
            layer("lib Info.plist.append", Rank::Dependency, lib),
        ])
        .expect("merge");
        assert_eq!(merged.entries.len(), 2);
        assert_eq!(
            merged.entries[0].1,
            Value::String("Scans QR codes.".into()),
            "the app's own declaration wins"
        );
        assert_eq!(merged.warnings.len(), 1, "{:?}", merged.warnings);
        assert!(merged.warnings[0].contains("NSCameraUsageDescription"));
    }

    #[test]
    fn a_generated_key_cannot_be_overridden_by_a_fragment() {
        let errs = merge(&[
            layer(
                "sky",
                Rank::Generated,
                "<key>CFBundleIdentifier</key><string>com.acme.app</string>",
            ),
            layer(
                "native/ios/Info.plist.append",
                Rank::Project,
                "<key>CFBundleIdentifier</key><string>com.evil.app</string>",
            ),
        ])
        .expect_err("a generated key must be locked");
        assert!(errs[0].contains("CFBundleIdentifier") && errs[0].contains("remove it"));
    }

    #[test]
    fn two_dependencies_that_disagree_are_an_error_naming_the_fix() {
        let errs = merge(&[
            layer(
                "dep a",
                Rank::Dependency,
                "<key>aps-environment</key><string>development</string>",
            ),
            layer(
                "dep b",
                Rank::Dependency,
                "<key>aps-environment</key><string>production</string>",
            ),
        ])
        .expect_err("two deps disagreeing");
        assert!(errs[0].contains("project's own"), "{errs:?}");
    }

    #[test]
    fn nested_dicts_merge_key_by_key() {
        let merged = merge(&[
            layer(
                "sky",
                Rank::Generated,
                "<key>NSAppTransportSecurity</key><dict><key>NSAllowsArbitraryLoads</key><false/></dict>",
            ),
            layer(
                "proj",
                Rank::Project,
                "<key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>",
            ),
        ])
        .expect("merge");
        let Value::Dict(d) = &merged.entries[0].1 else {
            panic!()
        };
        assert_eq!(d.len(), 2);
    }

    #[test]
    fn every_value_kind_round_trips() {
        let src = "<dict><key>s</key><string>a &lt; b</string><key>i</key><integer>7</integer>\
                   <key>r</key><real>1.5</real><key>t</key><true/><key>f</key><false/>\
                   <key>d</key><data>AAEC</data><key>dt</key><date>2026-01-01T00:00:00Z</date>\
                   <key>a</key><array/><key>e</key><dict/></dict>";
        let entries = parse_entries(src).unwrap();
        assert_eq!(parse_entries(&render_document(&entries)).unwrap(), entries);
    }
}
