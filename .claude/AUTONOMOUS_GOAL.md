# Autonomous mandate — Sky.Spa native capabilities (exp/spa)

Captured 2026-08-24. User (attended, perms + continuity granted upfront):

> keep going, fully unattended + autonomous, don't ask me permissions or
> continuity, until fully e2e implemented/tested/validated (if possible).

## Interpretation (the durable anchor)
Extend the `Std.Native` client-capability surface with the most commonly used
native/device APIs, each e2e IMPLEMENTED + TESTED + VALIDATED (run the generated
wasm in a real browser, not just built). Complete the dedicated example
(examples/64-spa-native) as the manual+e2e harness, incl. multi-platform
screenshots to match its siblings (web done; iOS, and Android if the emulator is
stable). Keep the auto-split correct (native = ClientEffect). Update docs +
memory. Commit at phase boundaries on exp/spa. NO merge to main, NO release/tag.

## Done = 
- A comprehensive common-native-API set, each with a Native_<cap> js kernel +
  Err !js stub + rt test + e2e browser validation.
- example 64 exercises them all, web+iOS screenshots present (Android if stable).
- All gates green (rt native, spa_split_flow incl. client-side regression,
  clean-slate example build, census).
- "(if possible)" carve-outs honestly reported: Android emulator instability,
  Phase 3 signing/store submission (blocked on the user's Apple/Play accounts).

## Stop only on a genuine blocker (external auth, irreversible action needing
## sign-off). Otherwise continue.
