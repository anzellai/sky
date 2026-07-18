//! Parser events + the tree-building sink (doc 04 §6). The parser emits a flat
//! `Vec<Event>`; the sink replays it into a rowan `GreenNodeBuilder`,
//! interleaving trivia from the raw lex vector so every byte lands in the tree
//! in source order (L8 losslessness). `forward_parent` implements Pratt
//! re-parenting (`CompletedMarker::precede`) without buffering.

use crate::kind::{SkyLang, SyntaxKind};
use crate::lexer::LexToken;
use rowan::{GreenNode, GreenNodeBuilder, Language};

#[derive(Debug)]
pub(crate) enum Event {
    /// Open a node. `forward_parent` is the *relative* distance (in event
    /// indices) to a `Start` that should become this node's parent — set by
    /// `precede`.
    Start {
        kind: SyntaxKind,
        forward_parent: Option<u32>,
    },
    /// Close the current node.
    Finish,
    /// Emit one significant token (its trivia is flushed just before it).
    Token {
        kind: SyntaxKind,
    },
    /// Emit a synthetic leaf whose text is a byte slice of the source (used for
    /// the multiline-interpolation interior split — doc 04 §9). `start..end` is
    /// an absolute byte range into the source.
    RawSlice {
        kind: SyntaxKind,
        start: u32,
        end: u32,
    },
    /// Flush leading trivia and consume one significant raw token *without*
    /// emitting it — its bytes are re-emitted by following `RawSlice`s (the
    /// multiline-interpolation split re-covers the `MultilineString` token).
    Skip,
}

impl Event {
    pub(crate) fn tombstone() -> Event {
        Event::Start {
            kind: SyntaxKind::Tombstone,
            forward_parent: None,
        }
    }
}

/// Replay `events` into a green tree, pulling trivia + significant token text
/// from `raw`/`src`. Every raw token is emitted exactly once, in order.
pub(crate) fn build_tree(src: &str, raw: &[LexToken], mut events: Vec<Event>) -> GreenNode {
    let mut builder = GreenNodeBuilder::new();
    let mut raw_pos = 0usize;

    // index of the final event (root Finish) so trailing trivia is flushed
    // into the root, not dropped.
    let last = events.len().saturating_sub(1);

    let mut forward_kinds: Vec<SyntaxKind> = Vec::new();

    let mut i = 0usize;
    while i < events.len() {
        match std::mem::replace(&mut events[i], Event::tombstone()) {
            Event::Start {
                kind: SyntaxKind::Tombstone,
                forward_parent: None,
            } => {
                // an abandoned / already-consumed marker
            }
            Event::Start {
                kind,
                forward_parent,
            } => {
                forward_kinds.clear();
                forward_kinds.push(kind);
                let mut idx = i;
                let mut fp = forward_parent;
                while let Some(fwd) = fp {
                    idx += fwd as usize;
                    match std::mem::replace(&mut events[idx], Event::tombstone()) {
                        Event::Start {
                            kind,
                            forward_parent,
                        } => {
                            if kind != SyntaxKind::Tombstone {
                                forward_kinds.push(kind);
                            }
                            fp = forward_parent;
                        }
                        _ => unreachable!("forward_parent must point at a Start"),
                    }
                }
                for &k in forward_kinds.iter().rev() {
                    if k != SyntaxKind::Tombstone {
                        builder.start_node(SkyLang::kind_to_raw(k));
                    }
                }
            }
            Event::Finish => {
                if i == last {
                    eat_trivia(&mut builder, src, raw, &mut raw_pos);
                }
                builder.finish_node();
            }
            Event::Token { kind } => {
                eat_trivia(&mut builder, src, raw, &mut raw_pos);
                if raw_pos < raw.len() {
                    let t = raw[raw_pos];
                    let text = &src[t.start as usize..t.end as usize];
                    builder.token(SkyLang::kind_to_raw(kind), text);
                    raw_pos += 1;
                }
            }
            Event::RawSlice { kind, start, end } => {
                let text = &src[start as usize..end as usize];
                builder.token(SkyLang::kind_to_raw(kind), text);
            }
            Event::Skip => {
                eat_trivia(&mut builder, src, raw, &mut raw_pos);
                if raw_pos < raw.len() {
                    raw_pos += 1;
                }
            }
        }
        i += 1;
    }

    // Any trivia the loop didn't reach (e.g. a totally empty event list) —
    // flush at the root so nothing is lost.
    eat_trivia(&mut builder, src, raw, &mut raw_pos);

    builder.finish()
}

fn eat_trivia(
    builder: &mut GreenNodeBuilder<'_>,
    src: &str,
    raw: &[LexToken],
    raw_pos: &mut usize,
) {
    while *raw_pos < raw.len() && raw[*raw_pos].kind.is_trivia() {
        let t = raw[*raw_pos];
        let text = &src[t.start as usize..t.end as usize];
        builder.token(SkyLang::kind_to_raw(t.kind), text);
        *raw_pos += 1;
    }
}
