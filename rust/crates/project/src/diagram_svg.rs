//! Self-contained SVG primitives for `sky doc --diagram --format svg`.
//!
//! We draw the SVG XML ourselves — there is NO external tool, no headless
//! browser, and no runtime dependency. Every diagram kind builds its own layout
//! (in [`crate::diagram`]) out of the primitives here: rounded rectangles with
//! centred, wrapped labels; text; arrow edges with a real `<marker>` arrowhead;
//! and two distinct capability shapes (a database cylinder and a queue). Output
//! is deterministic: the same report always produces byte-identical SVG.

use std::collections::BTreeSet;

/// Shared monochrome palette. Kept small so every kind reads as one system.
pub const STROKE: &str = "#333333";
pub const FILL: &str = "#ffffff";
pub const FILL_ALT: &str = "#f4f4f5";
pub const TEXT: &str = "#1a1a1a";
pub const SUBTLE: &str = "#6b7280";
pub const PKG_STROKE: &str = "#c4c7cc";
pub const PKG_FILL: &str = "#fbfbfc";
/// A server round-trip edge (Sky.Spa `/_rpc`, or a server action).
pub const SERVER_EDGE: &str = "#d9822b";
/// A client (in-browser) edge.
pub const CLIENT_EDGE: &str = "#2b6cb0";

/// The font stack. A real fallback chain so the SVG renders without web fonts.
pub const FONT: &str = "-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif";

/// XML-escape a string for use in an SVG text node or an attribute value.
pub fn escape(s: &str) -> String {
    let mut o = String::with_capacity(s.len());
    for ch in s.chars() {
        match ch {
            '&' => o.push_str("&amp;"),
            '<' => o.push_str("&lt;"),
            '>' => o.push_str("&gt;"),
            '"' => o.push_str("&quot;"),
            '\'' => o.push_str("&#39;"),
            other => o.push(other),
        }
    }
    o
}

/// Estimate the rendered width of `s` at `font_size`. An average glyph advance
/// of ~0.6em keeps boxes comfortably wide for the label they hold.
pub fn text_width(s: &str, font_size: f64) -> f64 {
    s.chars().count() as f64 * font_size * 0.6
}

/// Wrap `s` into lines no wider than `max_chars` characters, breaking on spaces.
/// A single token longer than `max_chars` is kept whole (never split mid-word).
pub fn wrap(s: &str, max_chars: usize) -> Vec<String> {
    let max = max_chars.max(1);
    let mut lines: Vec<String> = Vec::new();
    let mut cur = String::new();
    for word in s.split_whitespace() {
        if cur.is_empty() {
            cur.push_str(word);
        } else if cur.chars().count() + 1 + word.chars().count() <= max {
            cur.push(' ');
            cur.push_str(word);
        } else {
            lines.push(std::mem::take(&mut cur));
            cur.push_str(word);
        }
    }
    if !cur.is_empty() {
        lines.push(cur);
    }
    if lines.is_empty() {
        lines.push(String::new());
    }
    lines
}

/// A growing SVG document. Elements append to `body`; the drawn extent is
/// tracked so [`Svg::render`] can size the canvas and its `viewBox`.
pub struct Svg {
    body: String,
    max_x: f64,
    max_y: f64,
    /// Distinct arrow colours used — one `<marker>` per colour in `<defs>`.
    markers: BTreeSet<String>,
    title: String,
}

impl Svg {
    pub fn new(title: &str) -> Self {
        Svg {
            body: String::new(),
            max_x: 0.0,
            max_y: 0.0,
            markers: BTreeSet::new(),
            title: title.to_string(),
        }
    }

    fn touch(&mut self, x: f64, y: f64) {
        if x > self.max_x {
            self.max_x = x;
        }
        if y > self.max_y {
            self.max_y = y;
        }
    }

    /// A raw rounded rectangle.
    pub fn rect(&mut self, x: f64, y: f64, w: f64, h: f64, rx: f64, fill: &str, stroke: &str, sw: f64) {
        self.body.push_str(&format!(
            "  <rect x=\"{x:.1}\" y=\"{y:.1}\" width=\"{w:.1}\" height=\"{h:.1}\" rx=\"{rx:.1}\" \
             fill=\"{fill}\" stroke=\"{stroke}\" stroke-width=\"{sw:.1}\"/>\n"
        ));
        self.touch(x + w, y + h);
    }

    /// A single line of text. `anchor` is `start` / `middle` / `end`.
    pub fn text(&mut self, x: f64, y: f64, s: &str, anchor: &str, size: f64, weight: &str, fill: &str) {
        self.body.push_str(&format!(
            "  <text x=\"{x:.1}\" y=\"{y:.1}\" text-anchor=\"{anchor}\" \
             font-family=\"{FONT}\" font-size=\"{size:.1}\" font-weight=\"{weight}\" \
             fill=\"{fill}\">{}</text>\n",
            escape(s)
        ));
        self.touch(x, y);
    }

    /// A box label: `lines` centred horizontally on `cx`, vertically centred in
    /// the band `[y_top, y_top+h]`, at `size`px with `weight`.
    pub fn centred_lines(&mut self, cx: f64, y_top: f64, h: f64, lines: &[String], size: f64, weight: &str, fill: &str) {
        let n = lines.len() as f64;
        let line_h = size * 1.25;
        let block = n * line_h;
        let first_baseline = y_top + (h - block) / 2.0 + size * 0.9;
        for (i, l) in lines.iter().enumerate() {
            let y = first_baseline + i as f64 * line_h;
            self.text(cx, y, l, "middle", size, weight, fill);
        }
    }

    /// A standard module / page node: a rounded rectangle with a centred title
    /// and an optional smaller subtitle beneath it.
    pub fn node(&mut self, x: f64, y: f64, w: f64, h: f64, fill: &str, stroke: &str, title: &str, subtitle: Option<&str>) {
        self.rect(x, y, w, h, 8.0, fill, stroke, 1.5);
        let cx = x + w / 2.0;
        match subtitle {
            Some(sub) if !sub.is_empty() => {
                self.text(cx, y + h / 2.0 - 1.0, title, "middle", 13.0, "600", TEXT);
                self.text(cx, y + h / 2.0 + 15.0, sub, "middle", 11.0, "400", SUBTLE);
            }
            _ => {
                let lines = wrap(title, ((w - 16.0) / (13.0 * 0.6)).max(1.0) as usize);
                self.centred_lines(cx, y, h, &lines, 13.0, "600", TEXT);
            }
        }
    }

    /// A database cylinder: a rounded rectangle with an ellipse arc across the
    /// top, so it reads as a datastore, not a plain box.
    pub fn database(&mut self, x: f64, y: f64, w: f64, h: f64, fill: &str, stroke: &str, label: &str) {
        let ry = 6.0;
        self.rect(x, y, w, h, 6.0, fill, stroke, 1.5);
        // top ellipse line
        self.body.push_str(&format!(
            "  <path d=\"M {x:.1} {ytop:.1} a {rx:.1} {ry:.1} 0 0 0 {w:.1} 0\" \
             fill=\"none\" stroke=\"{stroke}\" stroke-width=\"1.5\"/>\n",
            ytop = y + ry,
            rx = w / 2.0,
        ));
        let cx = x + w / 2.0;
        self.text(cx, y + h / 2.0 + 6.0, label, "middle", 12.0, "500", TEXT);
        self.touch(x + w, y + h);
    }

    /// A queue node: a rectangle with a doubled right edge (a second vertical
    /// line just inside the right border), so it reads as a channel / queue.
    pub fn queue(&mut self, x: f64, y: f64, w: f64, h: f64, fill: &str, stroke: &str, label: &str) {
        self.rect(x, y, w, h, 4.0, fill, stroke, 1.5);
        let dx = x + w - 6.0;
        self.body.push_str(&format!(
            "  <line x1=\"{dx:.1}\" y1=\"{y:.1}\" x2=\"{dx:.1}\" y2=\"{y2:.1}\" \
             stroke=\"{stroke}\" stroke-width=\"1.5\"/>\n",
            y2 = y + h,
        ));
        let cx = x + (w - 6.0) / 2.0;
        self.text(cx, y + h / 2.0 + 4.0, label, "middle", 12.0, "500", TEXT);
    }

    fn marker_id(color: &str) -> String {
        format!("arrow_{}", color.trim_start_matches('#'))
    }

    /// A straight arrow edge with an optional mid-point label (drawn on a small
    /// white plate so it stays legible over a line).
    pub fn edge(&mut self, x1: f64, y1: f64, x2: f64, y2: f64, color: &str, label: Option<&str>) {
        self.markers.insert(color.to_string());
        let mid = Self::marker_id(color);
        self.body.push_str(&format!(
            "  <line x1=\"{x1:.1}\" y1=\"{y1:.1}\" x2=\"{x2:.1}\" y2=\"{y2:.1}\" \
             stroke=\"{color}\" stroke-width=\"1.5\" marker-end=\"url(#{mid})\"/>\n"
        ));
        if let Some(l) = label {
            self.edge_label((x1 + x2) / 2.0, (y1 + y2) / 2.0, l, color);
        }
        self.touch(x2, y2);
    }

    /// An elbow (multi-segment) arrow edge through `points`, arrowhead on the
    /// last segment, optional label at the first vertex.
    pub fn polyline(&mut self, points: &[(f64, f64)], color: &str, label: Option<&str>) {
        if points.len() < 2 {
            return;
        }
        self.markers.insert(color.to_string());
        let mid = Self::marker_id(color);
        let pts: Vec<String> = points.iter().map(|(x, y)| format!("{x:.1},{y:.1}")).collect();
        self.body.push_str(&format!(
            "  <polyline points=\"{}\" fill=\"none\" stroke=\"{color}\" stroke-width=\"1.5\" \
             marker-end=\"url(#{mid})\"/>\n",
            pts.join(" ")
        ));
        if let Some(l) = label {
            let (lx, ly) = points[0];
            self.edge_label(lx, ly - 6.0, l, color);
        }
        for (x, y) in points {
            self.touch(*x, *y);
        }
    }

    /// A self-loop arc on the right side of a box, labelled.
    pub fn self_loop(&mut self, x_right: f64, y_top: f64, color: &str, label: &str) {
        self.markers.insert(color.to_string());
        let mid = Self::marker_id(color);
        let y1 = y_top + 8.0;
        let y2 = y_top + 26.0;
        let bulge = x_right + 26.0;
        self.body.push_str(&format!(
            "  <path d=\"M {x_right:.1} {y1:.1} C {bulge:.1} {y1:.1} {bulge:.1} {y2:.1} {x_right:.1} {y2:.1}\" \
             fill=\"none\" stroke=\"{color}\" stroke-width=\"1.5\" marker-end=\"url(#{mid})\"/>\n"
        ));
        self.text(bulge + 4.0, (y1 + y2) / 2.0 + 3.0, label, "start", 10.5, "500", color);
        self.touch(bulge + text_width(label, 10.5) + 8.0, y2);
    }

    /// A small labelled plate centred on a point — used for edge mid-labels.
    fn edge_label(&mut self, cx: f64, cy: f64, label: &str, color: &str) {
        let w = text_width(label, 10.5) + 8.0;
        let h = 15.0;
        self.body.push_str(&format!(
            "  <rect x=\"{x:.1}\" y=\"{y:.1}\" width=\"{w:.1}\" height=\"{h:.1}\" rx=\"3\" \
             fill=\"#ffffff\" stroke=\"none\" opacity=\"0.9\"/>\n",
            x = cx - w / 2.0,
            y = cy - h / 2.0,
        ));
        self.text(cx, cy + 3.5, label, "middle", 10.5, "500", color);
    }

    /// A package (grouping) rectangle with a header label — a light bordered box
    /// around a lane, e.g. the Sky.Spa Client / Server lanes.
    pub fn package(&mut self, x: f64, y: f64, w: f64, h: f64, label: &str) {
        self.rect(x, y, w, h, 6.0, PKG_FILL, PKG_STROKE, 1.0);
        self.text(x + 12.0, y + 18.0, label, "start", 12.0, "700", SUBTLE);
    }

    /// A plain text caption (grey, small) — read/write hints, notes on a lane.
    pub fn caption(&mut self, x: f64, y: f64, s: &str, anchor: &str) {
        self.text(x, y, s, anchor, 10.5, "400", SUBTLE);
    }

    /// Finish the document: header (width/height + viewBox), `<defs>` with one
    /// arrowhead marker per colour used, the title, the body, and the footer.
    pub fn render(&self) -> String {
        let pad = 24.0;
        let w = self.max_x + pad * 2.0;
        let title_h = if self.title.is_empty() { 0.0 } else { 34.0 };
        let h = self.max_y + pad + title_h;
        let mut o = String::new();
        o.push_str(&format!(
            "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"{w:.0}\" height=\"{h:.0}\" \
             viewBox=\"0 0 {w:.0} {h:.0}\" font-family=\"{FONT}\">\n"
        ));
        o.push_str("  <defs>\n");
        for color in &self.markers {
            let id = Self::marker_id(color);
            o.push_str(&format!(
                "    <marker id=\"{id}\" viewBox=\"0 0 10 10\" refX=\"9\" refY=\"5\" \
                 markerWidth=\"7\" markerHeight=\"7\" orient=\"auto-start-reverse\">\n\
                 \x20     <path d=\"M 0 0 L 10 5 L 0 10 z\" fill=\"{color}\"/>\n    </marker>\n"
            ));
        }
        o.push_str("  </defs>\n");
        o.push_str(&format!("  <rect x=\"0\" y=\"0\" width=\"{w:.0}\" height=\"{h:.0}\" fill=\"{FILL}\"/>\n"));
        if !self.title.is_empty() {
            o.push_str(&format!(
                "  <text x=\"{pad:.0}\" y=\"20\" text-anchor=\"start\" font-family=\"{FONT}\" \
                 font-size=\"14\" font-weight=\"700\" fill=\"{TEXT}\">{}</text>\n",
                escape(&self.title)
            ));
        }
        // Shift the body down below the title band.
        o.push_str(&format!("  <g transform=\"translate({pad:.0},{ty:.0})\">\n", ty = title_h));
        o.push_str(&self.body);
        o.push_str("  </g>\n");
        o.push_str("</svg>\n");
        o
    }
}
