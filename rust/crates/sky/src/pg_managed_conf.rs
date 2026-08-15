//! Splicing Sky's managed block into a `postgresql.conf` it does not own.
//!
//! # Why one module rather than three implementations
//!
//! Three places write a managed block into a PostgreSQL conf: the embedded
//! cluster (`runtime-go/rt/pg_embed_conf.go`), the shared production cluster
//! (`db_shared::apply_managed_block`) and the development cluster
//! (`db_cluster::ensure_sky_conf`). Two of them replaced the block; the third
//! returned early the moment the marker was present, so its block was written
//! ONCE — at `initdb` — and frozen at whatever machine created the data
//! directory, while the connection pools re-read the machine on every boot.
//!
//! Resize a host from 2 vCPU to 8 and `max_connections` stays sized for the
//! 2-vCPU machine: the app strangles itself on the upgrade. Restoring a data
//! directory onto a different host does the same with no warning at all.
//! Vertical scaling on one server is a first-class use of a local cluster, not
//! an edge case.
//!
//! Three implementations is what let one of them be wrong while the other two
//! were right, so the splice lives here and the callers pass their own markers.
//!
//! # Finding the end of a block written before end markers existed
//!
//! New blocks are delimited. Blocks written before the end marker existed are
//! not, and they ran to the end of the file — so for those the extent has to be
//! inferred, and the two callers infer it differently for a reason:
//!
//!   * a DEVELOPMENT conf's block is followed by whatever the operator appended,
//!     and the block's own header invites them to append there, so the extent
//!     stops at the first line that is not a comment, a blank, or an assignment
//!     to a key Sky manages ([`LegacyExtent::ManagedKeys`]);
//!   * a SHARED cluster's conf is generated whole by `sky db provision
//!     --shared`, and a begin marker with no end there means an interrupted
//!     write rather than an old format — merging with that wreckage would leave
//!     two `shared_buffers` and no way to tell which was meant, so the extent is
//!     the rest of the file ([`LegacyExtent::ToEndOfFile`]).

/// How to find the end of a managed block that has no end marker.
pub enum LegacyExtent<'a> {
    /// Consume comments, blanks and assignments to these keys; stop at the
    /// first line that is none of them. Preserves anything an operator wrote
    /// after an un-delimited block.
    ManagedKeys(&'a [String]),
    /// Treat the rest of the file as part of the block. For a conf Sky
    /// generates whole, where a missing end marker means a torn write.
    ToEndOfFile,
}

/// Replace the managed block with `block`, or append it when the file has none.
///
/// `block` starts at its begin marker (NO leading newline — the separating
/// blank line is this function's, so a replace cannot grow one per start) and
/// ends with its end marker and a newline. Everything outside the block is
/// preserved — that is the whole contract, since PostgreSQL takes the last
/// occurrence of a setting and an operator's own lines are expected to sit
/// after the block.
pub fn replace_managed_block(
    conf: &str,
    block: &str,
    begin: &str,
    end: &str,
    legacy: LegacyExtent<'_>,
) -> String {
    debug_assert!(
        block.starts_with(begin),
        "the block must start at its begin marker, without a leading newline"
    );
    let Some(start) = conf.find(begin) else {
        let mut out = conf.to_string();
        if !out.is_empty() {
            if !out.ends_with('\n') {
                out.push('\n');
            }
            out.push('\n');
        }
        out.push_str(block);
        return out;
    };
    let head = &conf[..start];
    let rest = &conf[start..];

    if let Some(i) = rest.find(end) {
        let tail = &rest[i + end.len()..];
        return format!("{head}{block}{}", tail.trim_start_matches('\n'));
    }

    match legacy {
        LegacyExtent::ToEndOfFile => {
            let mut out = format!("{head}{block}");
            if !out.ends_with('\n') {
                out.push('\n');
            }
            out
        }
        LegacyExtent::ManagedKeys(keys) => {
            let lines: Vec<&str> = rest.split('\n').collect();
            let mut consumed = 0usize;
            for (i, line) in lines.iter().enumerate() {
                let t = line.trim();
                if t.is_empty() || t.starts_with('#') {
                    consumed = i + 1;
                    continue;
                }
                let key = t.split('=').next().unwrap_or("").trim();
                if keys.iter().any(|k| k == key) {
                    consumed = i + 1;
                    continue;
                }
                break;
            }
            let tail = lines[consumed..].join("\n");
            format!("{head}{block}{}", tail.trim_start_matches('\n'))
        }
    }
}

/// The `key` of every `key = value` line in a rendered block.
///
/// Read from the block rather than hand-listed, so a setting added to the
/// renderer cannot quietly fall out of the legacy-extent inference and get
/// orphaned above the new block on the one conf that still needs inferring.
pub fn managed_keys(block: &str) -> Vec<String> {
    block
        .lines()
        .map(str::trim)
        .filter(|l| !l.is_empty() && !l.starts_with('#'))
        .filter_map(|l| l.split_once('='))
        .map(|(k, _)| k.trim().to_string())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    const BEGIN: &str = "# --- sky ---";
    const END: &str = "# --- end sky ---";

    fn block(n: u32) -> String {
        format!("{BEGIN}\n# sized for something\nmax_connections = {n}\nwork_mem = 4MB\n{END}\n")
    }

    #[test]
    fn a_delimited_block_is_replaced_and_what_follows_it_survives() {
        let base = format!("# stock\n\n{}mine = 1\n", block(50));
        let out = replace_managed_block(&base, &block(90), BEGIN, END, LegacyExtent::ToEndOfFile);
        assert!(out.contains("max_connections = 90"), "{out}");
        assert!(!out.contains("max_connections = 50"), "{out}");
        assert!(out.contains("# stock"), "{out}");
        assert!(out.contains("mine = 1"), "{out}");
        assert_eq!(out.matches(BEGIN).count(), 1, "{out}");
    }

    #[test]
    fn replacing_is_a_fixed_point() {
        let base = "# stock\n".to_string();
        let once = replace_managed_block(&base, &block(90), BEGIN, END, LegacyExtent::ToEndOfFile);
        let twice = replace_managed_block(&once, &block(90), BEGIN, END, LegacyExtent::ToEndOfFile);
        assert_eq!(once, twice);
    }

    /// The case that distinguishes the two policies: an un-delimited block with
    /// an operator's own setting after it.
    #[test]
    fn an_undelimited_block_gives_up_only_the_keys_it_manages() {
        let keys = managed_keys(&block(50));
        let legacy = format!(
            "# stock\n{BEGIN}\n# sized for a 1-core machine\nmax_connections = 50\nwork_mem = 4MB\n\
             log_min_duration_statement = 250  # mine\n"
        );
        let out = replace_managed_block(
            &legacy,
            &block(90),
            BEGIN,
            END,
            LegacyExtent::ManagedKeys(&keys),
        );
        assert!(out.contains("log_min_duration_statement = 250"), "the operator's setting was eaten:\n{out}");
        assert!(!out.contains("max_connections = 50"), "the stale value survived:\n{out}");
        assert!(out.contains("max_connections = 90"), "{out}");
        assert_eq!(out.matches(BEGIN).count(), 1, "{out}");

        // …and the other policy would have eaten it, which is why they differ.
        let eaten =
            replace_managed_block(&legacy, &block(90), BEGIN, END, LegacyExtent::ToEndOfFile);
        assert!(
            !eaten.contains("log_min_duration_statement"),
            "the two legacy policies have become the same thing, so one of the two callers is \
             now getting behaviour it was deliberately not given:\n{eaten}"
        );
    }

    #[test]
    fn a_file_with_no_block_gets_one_appended() {
        let out = replace_managed_block("port = 5432", &block(90), BEGIN, END, LegacyExtent::ToEndOfFile);
        assert!(out.starts_with("port = 5432\n\n"), "{out}");
        assert!(out.contains(BEGIN), "{out}");
    }

    /// The separating blank line is the function's, not the block's, precisely
    /// so a cluster restarted a hundred times does not accumulate a hundred
    /// blank lines above its tuning.
    #[test]
    fn repeated_replacement_does_not_grow_the_file() {
        let mut conf = "# stock\n".to_string();
        conf = replace_managed_block(&conf, &block(90), BEGIN, END, LegacyExtent::ToEndOfFile);
        let after_first = conf.clone();
        for _ in 0..5 {
            conf = replace_managed_block(&conf, &block(90), BEGIN, END, LegacyExtent::ToEndOfFile);
        }
        assert_eq!(conf, after_first, "the file grew across repeated retunes");
    }

    #[test]
    fn managed_keys_reads_the_block_rather_than_a_hand_list() {
        assert_eq!(managed_keys(&block(50)), vec!["max_connections", "work_mem"]);
    }
}
