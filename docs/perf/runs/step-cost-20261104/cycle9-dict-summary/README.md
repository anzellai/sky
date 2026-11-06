# Oversized primitive Dict changes

Six browser/mode reports preserve the original eight checkpoints and add three
exact NoWrite/reload checkpoints. `conditions.json` binds the compiler and source
inputs and records the scope. All reports use public synthetic data.

The changed-entry measurement follows a completed cold load. Cold-load work
remains in every report. The previous snapshot remains authoritative above the
unchanged size cap; this is not a chunk-storage or under-cap scaling result.
