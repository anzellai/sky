//! A small, strict XML reader for the files the native shells merge: Apple
//! property lists (`Info.plist` fragments, `.entitlements`) and Android
//! manifest fragments (`native/android/permissions.xml`).
//!
//! It is not a general XML parser. It reads elements, attributes, text,
//! comments, the `<?xml …?>` declaration and a `<!DOCTYPE …>` line, and the five
//! predefined entities plus numeric character references. Anything else (a
//! CDATA section, an unterminated tag, a mismatched close tag) is an error that
//! names the byte offset, because a native build that ships a file it could not
//! read is worse than a build that stops.
//!
//! The old shell generator concatenated these fragments as text. Two full
//! plist documents joined that way are two XML documents in one file, and a key
//! set by two fragments appeared twice in one `<dict>`. Reading the fragments
//! into a tree first is what lets `plist::merge` refuse or resolve both.

/// One XML node.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Node {
    Element(Element),
    /// Character data between elements, entities already decoded.
    Text(String),
}

/// One XML element.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Element {
    pub name: String,
    /// Attributes in source order, values decoded.
    pub attrs: Vec<(String, String)>,
    pub children: Vec<Node>,
}

impl Element {
    /// The value of attribute `name`, if present.
    pub fn attr(&self, name: &str) -> Option<&str> {
        self.attrs
            .iter()
            .find(|(k, _)| k == name)
            .map(|(_, v)| v.as_str())
    }

    /// The child elements, ignoring text.
    pub fn elements(&self) -> impl Iterator<Item = &Element> {
        self.children.iter().filter_map(|n| match n {
            Node::Element(e) => Some(e),
            Node::Text(_) => None,
        })
    }

    /// The concatenated text content of the element (direct text children).
    pub fn text(&self) -> String {
        self.children
            .iter()
            .filter_map(|n| match n {
                Node::Text(t) => Some(t.as_str()),
                Node::Element(_) => None,
            })
            .collect()
    }
}

/// Parse a sequence of top-level nodes (a document has one root element; a
/// fragment may have many). The XML declaration, a DOCTYPE and comments are
/// skipped. Whitespace-only text between top-level elements is dropped.
pub fn parse_nodes(src: &str) -> Result<Vec<Node>, String> {
    let mut p = Parser { src, pos: 0 };
    let nodes = p.content(None)?;
    Ok(nodes
        .into_iter()
        .filter(|n| !matches!(n, Node::Text(t) if t.trim().is_empty()))
        .collect())
}

/// Escape text for element content or a double-quoted attribute value.
pub fn escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&apos;"),
            _ => out.push(c),
        }
    }
    out
}

/// Serialise an element compactly (no added whitespace), attributes in order.
pub fn render_element(e: &Element) -> String {
    let mut out = String::new();
    render_into(e, &mut out);
    out
}

fn render_into(e: &Element, out: &mut String) {
    out.push('<');
    out.push_str(&e.name);
    for (k, v) in &e.attrs {
        out.push(' ');
        out.push_str(k);
        out.push_str("=\"");
        out.push_str(&escape(v));
        out.push('"');
    }
    if e.children.is_empty() {
        out.push_str(" />");
        return;
    }
    out.push('>');
    for c in &e.children {
        match c {
            Node::Element(ch) => render_into(ch, out),
            Node::Text(t) => out.push_str(&escape(t)),
        }
    }
    out.push_str("</");
    out.push_str(&e.name);
    out.push('>');
}

struct Parser<'a> {
    src: &'a str,
    pos: usize,
}

impl<'a> Parser<'a> {
    fn rest(&self) -> &'a str {
        &self.src[self.pos..]
    }

    fn err<T>(&self, what: &str) -> Result<T, String> {
        let line = self.src[..self.pos].matches('\n').count() + 1;
        Err(format!("line {line}: {what}"))
    }

    /// Read nodes until the close tag `</close>` (consumed) or, when `close`
    /// is `None`, the end of input.
    fn content(&mut self, close: Option<&str>) -> Result<Vec<Node>, String> {
        let mut nodes = Vec::new();
        let mut text = String::new();
        loop {
            if self.pos >= self.src.len() {
                if let Some(name) = close {
                    return self.err(&format!("`<{name}>` is never closed"));
                }
                break;
            }
            let rest = self.rest();
            if rest.starts_with("<!--") {
                match rest.find("-->") {
                    Some(end) => self.pos += end + 3,
                    None => return self.err("an unterminated comment"),
                }
            } else if rest.starts_with("<?") {
                match rest.find("?>") {
                    Some(end) => self.pos += end + 2,
                    None => return self.err("an unterminated `<?…?>`"),
                }
            } else if rest.starts_with("<!DOCTYPE") {
                match rest.find('>') {
                    Some(end) => self.pos += end + 1,
                    None => return self.err("an unterminated DOCTYPE"),
                }
            } else if rest.starts_with("<![CDATA[") {
                return self.err("a CDATA section, which these files do not use");
            } else if rest.starts_with("</") {
                let end = match rest.find('>') {
                    Some(e) => e,
                    None => return self.err("an unterminated close tag"),
                };
                let name = rest[2..end].trim();
                match close {
                    Some(want) if want == name => {
                        self.pos += end + 1;
                        if !text.is_empty() {
                            nodes.push(Node::Text(std::mem::take(&mut text)));
                        }
                        return Ok(nodes);
                    }
                    Some(want) => {
                        return self.err(&format!("`</{name}>` closes `<{want}>`"));
                    }
                    None => return self.err(&format!("`</{name}>` has no open tag")),
                }
            } else if rest.starts_with('<') {
                if !text.is_empty() {
                    nodes.push(Node::Text(std::mem::take(&mut text)));
                }
                let el = self.element()?;
                nodes.push(Node::Element(el));
            } else {
                let end = rest.find('<').unwrap_or(rest.len());
                text.push_str(&decode_entities(&rest[..end]).or_else(|e| self.err(&e))?);
                self.pos += end;
            }
        }
        if !text.is_empty() {
            nodes.push(Node::Text(text));
        }
        Ok(nodes)
    }

    fn element(&mut self) -> Result<Element, String> {
        // at '<'
        self.pos += 1;
        let rest = self.rest();
        let name_len = rest
            .find(|c: char| c.is_whitespace() || c == '>' || c == '/')
            .unwrap_or(rest.len());
        let name = rest[..name_len].to_string();
        if name.is_empty() || !name.chars().all(is_name_char) {
            return self.err(&format!("`<{name}` is not an element name"));
        }
        self.pos += name_len;
        let mut attrs = Vec::new();
        loop {
            self.skip_ws();
            let rest = self.rest();
            if rest.starts_with("/>") {
                self.pos += 2;
                return Ok(Element {
                    name,
                    attrs,
                    children: Vec::new(),
                });
            }
            if rest.starts_with('>') {
                self.pos += 1;
                let children = self.content(Some(&name))?;
                return Ok(Element {
                    name,
                    attrs,
                    children,
                });
            }
            if rest.is_empty() {
                return self.err(&format!("`<{name}` is never closed"));
            }
            let klen = rest
                .find(|c: char| c == '=' || c.is_whitespace())
                .unwrap_or(rest.len());
            let key = rest[..klen].to_string();
            if key.is_empty() || !key.chars().all(is_name_char) {
                return self.err(&format!("a malformed attribute in `<{name}>`"));
            }
            self.pos += klen;
            self.skip_ws();
            if !self.rest().starts_with('=') {
                return self.err(&format!("attribute `{key}` in `<{name}>` has no value"));
            }
            self.pos += 1;
            self.skip_ws();
            let quote = match self.rest().chars().next() {
                Some(q @ ('"' | '\'')) => q,
                _ => return self.err(&format!("attribute `{key}` in `<{name}>` is not quoted")),
            };
            self.pos += 1;
            let rest = self.rest();
            let end = match rest.find(quote) {
                Some(e) => e,
                None => return self.err(&format!("attribute `{key}` is never closed")),
            };
            let value = decode_entities(&rest[..end]).or_else(|e| self.err(&e))?;
            self.pos += end + 1;
            if attrs.iter().any(|(k, _)| *k == key) {
                return self.err(&format!("attribute `{key}` appears twice in `<{name}>`"));
            }
            attrs.push((key, value));
        }
    }

    fn skip_ws(&mut self) {
        let rest = self.rest();
        let n = rest.len() - rest.trim_start().len();
        self.pos += n;
    }
}

fn is_name_char(c: char) -> bool {
    c.is_alphanumeric() || matches!(c, ':' | '_' | '-' | '.')
}

/// Decode the predefined entities and numeric character references.
fn decode_entities(s: &str) -> Result<String, String> {
    if !s.contains('&') {
        return Ok(s.to_string());
    }
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(i) = rest.find('&') {
        out.push_str(&rest[..i]);
        let after = &rest[i + 1..];
        let Some(semi) = after.find(';') else {
            return Err("a bare `&` (write `&amp;`)".to_string());
        };
        let ent = &after[..semi];
        let ch = match ent {
            "amp" => '&',
            "lt" => '<',
            "gt" => '>',
            "quot" => '"',
            "apos" => '\'',
            _ if ent.starts_with("#x") || ent.starts_with("#X") => {
                u32::from_str_radix(&ent[2..], 16)
                    .ok()
                    .and_then(char::from_u32)
                    .ok_or_else(|| format!("`&{ent};` is not a character"))?
            }
            _ if ent.starts_with('#') => ent[1..]
                .parse::<u32>()
                .ok()
                .and_then(char::from_u32)
                .ok_or_else(|| format!("`&{ent};` is not a character"))?,
            _ => return Err(format!("the entity `&{ent};` is not one XML defines")),
        };
        out.push(ch);
        rest = &after[semi + 1..];
    }
    out.push_str(rest);
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_a_document_with_prologue_doctype_and_comments() {
        let src = "<?xml version=\"1.0\"?>\n<!DOCTYPE plist PUBLIC \"x\" \"y\">\n<!-- c -->\n<plist version=\"1.0\"><dict><key>A &amp; B</key><true/></dict></plist>\n";
        let nodes = parse_nodes(src).unwrap();
        assert_eq!(nodes.len(), 1);
        let Node::Element(root) = &nodes[0] else {
            panic!("root")
        };
        assert_eq!(root.name, "plist");
        assert_eq!(root.attr("version"), Some("1.0"));
        let dict = root.elements().next().unwrap();
        let key = dict.elements().next().unwrap();
        assert_eq!(key.text(), "A & B");
    }

    #[test]
    fn a_fragment_may_hold_many_top_level_elements() {
        let nodes = parse_nodes(
            "<uses-permission android:name=\"a\" />\n<uses-permission android:name=\"b\"/>",
        )
        .unwrap();
        assert_eq!(nodes.len(), 2);
    }

    #[test]
    fn malformed_input_is_an_error_not_a_guess() {
        for bad in [
            "<dict><key>a</key>",
            "<a></b>",
            "</a>",
            "<a x=1/>",
            "<a>&nope;</a>",
            "<a>fish & chips</a>",
            "<a x=\"1\" x=\"2\"/>",
            "<![CDATA[x]]>",
        ] {
            assert!(parse_nodes(bad).is_err(), "must refuse {bad:?}");
        }
    }

    #[test]
    fn render_round_trips_escaping() {
        let nodes = parse_nodes("<a k=\"&quot;q&quot;\">x &lt; y</a>").unwrap();
        let Node::Element(e) = &nodes[0] else {
            panic!()
        };
        let out = render_element(e);
        assert_eq!(parse_nodes(&out).unwrap(), nodes);
    }
}
