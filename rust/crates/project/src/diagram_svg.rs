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
/// An external system / egress sink accent (data that leaves the trust boundary).
pub const EXTERNAL: &str = "#7c3aed";
/// The trust-boundary accent for the UNTRUSTED side (browser / client).
pub const BOUNDARY_UNTRUSTED: &str = "#c2410c";
/// The trust-boundary accent for the TRUSTED side (server).
pub const BOUNDARY_TRUSTED: &str = "#2f855a";

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
    pub fn rect(
        &mut self,
        x: f64,
        y: f64,
        w: f64,
        h: f64,
        rx: f64,
        fill: &str,
        stroke: &str,
        sw: f64,
    ) {
        self.body.push_str(&format!(
            "  <rect x=\"{x:.1}\" y=\"{y:.1}\" width=\"{w:.1}\" height=\"{h:.1}\" rx=\"{rx:.1}\" \
             fill=\"{fill}\" stroke=\"{stroke}\" stroke-width=\"{sw:.1}\"/>\n"
        ));
        self.touch(x + w, y + h);
    }

    /// A single line of text. `anchor` is `start` / `middle` / `end`.
    pub fn text(
        &mut self,
        x: f64,
        y: f64,
        s: &str,
        anchor: &str,
        size: f64,
        weight: &str,
        fill: &str,
    ) {
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
    pub fn centred_lines(
        &mut self,
        cx: f64,
        y_top: f64,
        h: f64,
        lines: &[String],
        size: f64,
        weight: &str,
        fill: &str,
    ) {
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
    pub fn node(
        &mut self,
        x: f64,
        y: f64,
        w: f64,
        h: f64,
        fill: &str,
        stroke: &str,
        title: &str,
        subtitle: Option<&str>,
    ) {
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
    pub fn database(
        &mut self,
        x: f64,
        y: f64,
        w: f64,
        h: f64,
        fill: &str,
        stroke: &str,
        label: &str,
    ) {
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
        let pts: Vec<String> = points
            .iter()
            .map(|(x, y)| format!("{x:.1},{y:.1}"))
            .collect();
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
        self.text(
            bulge + 4.0,
            (y1 + y2) / 2.0 + 3.0,
            label,
            "start",
            10.5,
            "500",
            color,
        );
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

    /// A TRUST-BOUNDARY zone: a dashed bordered box with a header label, used to
    /// enclose the untrusted (browser) and trusted (server) sides of a system in
    /// a C4 / data-flow diagram. `accent` colours the border + header so the two
    /// sides read apart. Nothing is filled, so inner containers stay legible.
    pub fn zone(&mut self, x: f64, y: f64, w: f64, h: f64, label: &str, accent: &str) {
        self.body.push_str(&format!(
            "  <rect x=\"{x:.1}\" y=\"{y:.1}\" width=\"{w:.1}\" height=\"{h:.1}\" rx=\"10\" \
             fill=\"none\" stroke=\"{accent}\" stroke-width=\"1.3\" stroke-dasharray=\"6 4\"/>\n"
        ));
        self.text(x + 14.0, y + 19.0, label, "start", 11.5, "700", accent);
        self.touch(x + w, y + h);
    }

    /// A C4 CONTAINER box: a filled rounded rectangle with a bold title, a small
    /// «stereotype» line (e.g. «wasm client»), and an optional grey subtitle.
    /// Reads as a deployable unit, distinct from a capability shape.
    pub fn container(
        &mut self,
        x: f64,
        y: f64,
        w: f64,
        h: f64,
        fill: &str,
        title: &str,
        stereotype: &str,
        subtitle: Option<&str>,
    ) {
        self.rect(x, y, w, h, 8.0, fill, STROKE, 1.5);
        let cx = x + w / 2.0;
        self.text(cx, y + 24.0, title, "middle", 13.0, "700", TEXT);
        if !stereotype.is_empty() {
            self.text(
                cx,
                y + 40.0,
                &format!("«{stereotype}»"),
                "middle",
                10.5,
                "500",
                SUBTLE,
            );
        }
        if let Some(sub) = subtitle {
            if !sub.is_empty() {
                for (i, l) in wrap(sub, ((w - 20.0) / (10.5 * 0.6)).max(1.0) as usize)
                    .iter()
                    .enumerate()
                {
                    self.text(
                        cx,
                        y + 56.0 + i as f64 * 13.0,
                        l,
                        "middle",
                        10.0,
                        "400",
                        SUBTLE,
                    );
                }
            }
        }
    }

    /// An actor (person) glyph with a label beneath — the external user in a C4
    /// context/container diagram. Drawn centred on `cx`, head top at `y_top`.
    pub fn actor(&mut self, cx: f64, y_top: f64, label: &str) {
        let head_r = 10.0;
        let hy = y_top + head_r;
        self.body.push_str(&format!(
            "  <circle cx=\"{cx:.1}\" cy=\"{hy:.1}\" r=\"{head_r:.1}\" fill=\"{FILL}\" stroke=\"{STROKE}\" stroke-width=\"1.5\"/>\n"
        ));
        let by = hy + head_r + 2.0;
        // shoulders/body: a rounded trapezoid approximated by a path
        self.body.push_str(&format!(
            "  <path d=\"M {l:.1} {bb:.1} Q {cx:.1} {by:.1} {r:.1} {bb:.1}\" fill=\"none\" stroke=\"{STROKE}\" stroke-width=\"1.5\"/>\n",
            l = cx - 16.0, r = cx + 16.0, bb = by + 20.0, by = by,
        ));
        self.text(cx, by + 36.0, label, "middle", 12.0, "600", TEXT);
        self.touch(cx + 18.0, by + 40.0);
    }

    /// An orthogonal (right-angle) connector from `(x1,y1)` to `(x2,y2)`, routed
    /// as H then V (or a straight line when already axis-aligned), with an
    /// arrowhead on the end and an optional label on a white plate at the bend.
    /// Orthogonal routing is what keeps an architecture diagram from becoming
    /// diagonal spaghetti.
    pub fn ortho(&mut self, x1: f64, y1: f64, x2: f64, y2: f64, color: &str, label: Option<&str>) {
        self.markers.insert(color.to_string());
        let mid = Self::marker_id(color);
        if (y1 - y2).abs() < 0.5 || (x1 - x2).abs() < 0.5 {
            self.body.push_str(&format!(
                "  <line x1=\"{x1:.1}\" y1=\"{y1:.1}\" x2=\"{x2:.1}\" y2=\"{y2:.1}\" \
                 stroke=\"{color}\" stroke-width=\"1.5\" marker-end=\"url(#{mid})\"/>\n"
            ));
            if let Some(l) = label {
                self.edge_label((x1 + x2) / 2.0, (y1 + y2) / 2.0, l, color);
            }
        } else {
            // H to the mid-x, then V to the target row, then H into the target.
            let mx = (x1 + x2) / 2.0;
            let pts = [(x1, y1), (mx, y1), (mx, y2), (x2, y2)];
            let s: Vec<String> = pts.iter().map(|(x, y)| format!("{x:.1},{y:.1}")).collect();
            self.body.push_str(&format!(
                "  <polyline points=\"{}\" fill=\"none\" stroke=\"{color}\" stroke-width=\"1.5\" \
                 marker-end=\"url(#{mid})\"/>\n",
                s.join(" ")
            ));
            if let Some(l) = label {
                self.edge_label(mx, (y1 + y2) / 2.0, l, color);
            }
        }
        self.touch(x2, y2);
    }

    /// A legend box at `(x,y)`: a small bordered card listing `swatch → text`
    /// rows, so a reader can decode the shapes/colours without prose.
    pub fn legend(&mut self, x: f64, y: f64, rows: &[(String, String)]) {
        if rows.is_empty() {
            return;
        }
        let row_h = 18.0;
        let w = 12.0
            + rows
                .iter()
                .map(|(_, t)| 26.0 + text_width(t, 10.5))
                .fold(0.0_f64, f64::max)
            + 12.0;
        let h = 24.0 + rows.len() as f64 * row_h;
        self.rect(x, y, w, h, 6.0, "#ffffff", PKG_STROKE, 1.0);
        self.text(x + 10.0, y + 16.0, "Legend", "start", 10.5, "700", SUBTLE);
        for (i, (color, text)) in rows.iter().enumerate() {
            let ry = y + 24.0 + i as f64 * row_h;
            self.rect(x + 10.0, ry + 3.0, 14.0, 9.0, 2.0, "#ffffff", color, 1.5);
            self.text(x + 32.0, ry + 11.0, text, "start", 10.5, "500", TEXT);
        }
        self.touch(x + w, y + h);
    }

    /// A plain text caption (grey, small) — read/write hints, notes on a lane.
    pub fn caption(&mut self, x: f64, y: f64, s: &str, anchor: &str) {
        self.text(x, y, s, anchor, 10.5, "400", SUBTLE);
    }

    /// A plain straight line with NO arrowhead — a table rule, a boundary line, a
    /// separator. (Unlike [`Svg::edge`] / [`Svg::polyline`], which always carry a
    /// marker.) `dashed` draws it as a dashed rule.
    pub fn rule(&mut self, x1: f64, y1: f64, x2: f64, y2: f64, color: &str, dashed: bool) {
        let dash = if dashed {
            " stroke-dasharray=\"5 4\""
        } else {
            ""
        };
        self.body.push_str(&format!(
            "  <line x1=\"{x1:.1}\" y1=\"{y1:.1}\" x2=\"{x2:.1}\" y2=\"{y2:.1}\" \
             stroke=\"{color}\" stroke-width=\"1.0\"{dash}/>\n"
        ));
        self.touch(x1.max(x2), y1.max(y2));
    }

    /// A multi-line edge label: a white rounded plate carrying `lines` centred on
    /// `(cx, cy)`, coloured `color`. Used for a COLLAPSED parallel edge, whose
    /// label lists every Msg that shares the same source→target transition — one
    /// plate, stacked lines, never labels drawn on top of each other. The plate
    /// is opaque so it stays legible over a connector line.
    pub fn plate_lines(&mut self, cx: f64, cy: f64, lines: &[String], color: &str) {
        if lines.is_empty() {
            return;
        }
        let size = 10.5;
        let line_h = 14.0;
        let w = lines
            .iter()
            .map(|l| text_width(l, size))
            .fold(0.0_f64, f64::max)
            + 12.0;
        let h = lines.len() as f64 * line_h + 8.0;
        let x = cx - w / 2.0;
        let y = cy - h / 2.0;
        self.rect(x, y, w, h, 4.0, "#ffffff", "#e2e5ea", 1.0);
        let first = y + 4.0 + size * 0.85;
        for (i, l) in lines.iter().enumerate() {
            self.text(
                cx,
                first + i as f64 * line_h,
                l,
                "middle",
                size,
                "500",
                color,
            );
        }
        self.touch(x + w, y + h);
    }

    /// The pixel height a [`Svg::plate_lines`] plate needs for `n` lines — so a
    /// caller can reserve vertical space per row and guarantee no overlap.
    pub fn plate_height(n: usize) -> f64 {
        n.max(1) as f64 * 14.0 + 8.0
    }

    /// An orthogonal connector routed through an EXPLICIT bend coordinate, with an
    /// arrowhead on the end and NO label (the caller places a [`Svg::plate_lines`]
    /// where it wants it). `horizontal = true` routes H→V→H through the vertical
    /// line `x = mid`; `false` routes V→H→V through the horizontal line `y = mid`.
    /// An explicit bend lets a caller stagger many edges that share one source, so
    /// their trunks never overlap into one thick smear.
    pub fn ortho_via(
        &mut self,
        x1: f64,
        y1: f64,
        x2: f64,
        y2: f64,
        mid: f64,
        horizontal: bool,
        color: &str,
    ) {
        self.markers.insert(color.to_string());
        let mid_id = Self::marker_id(color);
        let pts = if horizontal {
            [(x1, y1), (mid, y1), (mid, y2), (x2, y2)]
        } else {
            [(x1, y1), (x1, mid), (x2, mid), (x2, y2)]
        };
        let s: Vec<String> = pts.iter().map(|(x, y)| format!("{x:.1},{y:.1}")).collect();
        self.body.push_str(&format!(
            "  <polyline points=\"{}\" fill=\"none\" stroke=\"{color}\" stroke-width=\"1.5\" \
             marker-end=\"url(#{mid_id})\"/>\n",
            s.join(" ")
        ));
        for (x, y) in pts {
            self.touch(x, y);
        }
    }

    /// A self-loop arc on the right side of a box, centred vertically on `cy`, with
    /// an arrowhead back into the box. Returns the anchor point where the caller
    /// should place the loop's [`Svg::plate_lines`] label. Drawing the arc and the
    /// label separately lets a self-loop carry a COLLAPSED multi-Msg label too.
    pub fn loop_arc(&mut self, x_right: f64, cy: f64, color: &str) -> (f64, f64) {
        self.markers.insert(color.to_string());
        let mid = Self::marker_id(color);
        let y1 = cy - 10.0;
        let y2 = cy + 10.0;
        let bulge = x_right + 34.0;
        self.body.push_str(&format!(
            "  <path d=\"M {x_right:.1} {y1:.1} C {bulge:.1} {y1:.1} {bulge:.1} {y2:.1} {x_right:.1} {y2:.1}\" \
             fill=\"none\" stroke=\"{color}\" stroke-width=\"1.5\" marker-end=\"url(#{mid})\"/>\n"
        ));
        self.touch(bulge, y2);
        (bulge + 6.0, cy)
    }

    /// A small pill / chip carrying `text` — a compact list item (an internal
    /// event, an "other" page) laid out in a wrapped grid. `border` outlines it,
    /// `text_color` fills the label. Returns the chip WIDTH so the caller can flow
    /// the next chip. Fixed height ([`Svg::CHIP_H`]).
    pub fn chip(&mut self, x: f64, y: f64, text: &str, border: &str, text_color: &str) -> f64 {
        let w = text_width(text, 10.5) + 16.0;
        self.rect(x, y, w, Self::CHIP_H, 9.0, "#ffffff", border, 1.0);
        self.text(
            x + w / 2.0,
            y + Self::CHIP_H / 2.0 + 3.5,
            text,
            "middle",
            10.5,
            "500",
            text_color,
        );
        w
    }

    /// The fixed height of a [`Svg::chip`].
    pub const CHIP_H: f64 = 22.0;

    /// A small padlock glyph centred on `(cx, cy)`, coloured `color` — the CONTROL
    /// marker stamped on a boundary crossing that authenticates (the backend
    /// authenticates every request). Reads as "this crossing is guarded".
    pub fn lock(&mut self, cx: f64, cy: f64, color: &str) {
        // body
        let bw = 11.0;
        let bh = 8.0;
        let bx = cx - bw / 2.0;
        let by = cy - 1.0;
        self.rect(bx, by, bw, bh, 1.5, "#ffffff", color, 1.5);
        // shackle
        self.body.push_str(&format!(
            "  <path d=\"M {l:.1} {by:.1} v -3 a {r:.1} {r:.1} 0 0 1 {d:.1} 0 v 3\" \
             fill=\"none\" stroke=\"{color}\" stroke-width=\"1.5\"/>\n",
            l = cx - 3.5,
            r = 3.5,
            d = 7.0,
            by = by,
        ));
        self.touch(cx + bw, cy + bh);
    }

    /// A datastore cylinder styled as a data store in a trust zone — an alias for
    /// [`Svg::database`] with the audit-diagram fill, kept named so the C4 code
    /// reads intentionally ("draw a data store", not "draw a database").
    pub fn datastore(&mut self, x: f64, y: f64, w: f64, h: f64, label: &str, accent: &str) {
        self.database(x, y, w, h, FILL_ALT, accent, label);
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
        o.push_str(&format!(
            "  <rect x=\"0\" y=\"0\" width=\"{w:.0}\" height=\"{h:.0}\" fill=\"{FILL}\"/>\n"
        ));
        if !self.title.is_empty() {
            o.push_str(&format!(
                "  <text x=\"{pad:.0}\" y=\"20\" text-anchor=\"start\" font-family=\"{FONT}\" \
                 font-size=\"14\" font-weight=\"700\" fill=\"{TEXT}\">{}</text>\n",
                escape(&self.title)
            ));
        }
        // Shift the body down below the title band.
        o.push_str(&format!(
            "  <g transform=\"translate({pad:.0},{ty:.0})\">\n",
            ty = title_h
        ));
        o.push_str(&self.body);
        o.push_str("  </g>\n");
        o.push_str("</svg>\n");
        o
    }
}
