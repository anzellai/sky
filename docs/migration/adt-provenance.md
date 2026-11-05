# ADT provenance and old saved snapshots

New compiled legacy ADTs carry their resolved parent identity. Codec.auto now
uses that identity for Decimal and Money formatting. Runtime values with an
explicit user parent retain their tagged payloads even when their constructor
is named Money or Decimal__Internal. The existing public opaque-type boundary
still rejects Codec.auto derivation for a custom __Internal constructor.
Only canonical stdlib
source identities receive builtin provenance. Public JSON does not expose or
accept the token. Ordinary builtin JSON/DB rows keep their existing format. Runtime Currency
boxes also carry their actual parent. A trusted Money currency field or an
exact declared Currency slot can recover an ownerless legacy Currency box;
explicit conflicting owners and sealed user lookalikes are rejected. The
existing CurrencyRaw writer/strict-reader distinction is preserved.

Gob preserves provenance in new snapshots. Older snapshots restore with an
empty token. Codec.auto can encode an old Decimal or Money when its exact
compiler-declared field/container type is backed by a canonical source grant.
Non-generic record aliases keep that metadata through lists, Maybe, Dict and
tuples. Anonymous records, generic declarations and erased payload slots may
lack it; there is no name/shape-based recovery in those slots.

Encoding an ambiguous old ownerless Money or Decimal__Internal raises an
explicit Codec.auto error, classified as JsonEncodeFailure. It does not write
zero, discard fields, or guess that a user value is a builtin. Applications
with those snapshots must export/restore through a supported declared type or
expire and recreate the affected sessions/snapshots before rollout. A session
can fail at its next encode until this is done. No automatic data-dropping
migration is provided.

Rebuild all generated code, including the bundled console, for the upgrade.
Rolling back to an older binary discards the new Gob field, so a mixed-version
round trip can recreate the old ambiguous condition. Plan snapshot migration
and rollback together; verify a real old-release session-store restore in
addition to structural Gob-schema tests before deployment.
