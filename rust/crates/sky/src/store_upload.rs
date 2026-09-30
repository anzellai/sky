//! `sky package --release --target mobile:ios --upload testflight`: send the
//! signed `.ipa` to App Store Connect, where it becomes a TestFlight build.
//!
//! The upload is Apple's own tool, `xcrun altool` (Xcode). Sky adds what the
//! tool does not do for you:
//!
//! * **Refusals before any network call**, each naming its fix: the API key
//!   variables and the `.p8` file, a bundle id and a build number set in the
//!   `bundle` binding, and a build signed for App Store distribution (an
//!   unsigned `-unsigned.ipa`, a development or ad hoc profile, and an
//!   enterprise profile are all refused here, not by Apple after an upload).
//! * **The key never reaches a command line.** altool finds the key file as
//!   `AuthKey_<KEY_ID>.p8` in the directory `$API_PRIVATE_KEYS_DIR` names
//!   (altool's own documented search path). Only the key id and the issuer id,
//!   which are identifiers and not secrets, are arguments. The key's contents
//!   are never read into an error message or a log line.
//! * **Validate, then upload**, and Apple's error text reported plainly, with
//!   the Sky fix for the errors a Sky app can meet.
//!
//! The tool invocation is injectable for tests only: `SKY_XCRUN` names an
//! executable run in place of `xcrun`, so a test proves the argument
//! construction, the refusals, the success path and the error parsing with a
//! fake, without a network or real credentials.

use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::process::Command;

use crate::plist::{self, Value};

/// App Store Connect API key id (the 10-character id shown next to the key).
pub const ASC_KEY_ID: &str = "SKY_ASC_KEY_ID";
/// App Store Connect issuer id (a UUID, at the top of the API keys page).
pub const ASC_ISSUER_ID: &str = "SKY_ASC_ISSUER_ID";
/// Path to the downloaded `AuthKey_<KEY_ID>.p8` private key.
pub const ASC_KEY_PATH: &str = "SKY_ASC_KEY_PATH";
/// TEST-ONLY: an executable run in place of `xcrun`. The flow tests point it
/// at a fake that records its arguments; a real upload leaves it unset.
pub const XCRUN_OVERRIDE: &str = "SKY_XCRUN";

/// Where `--upload` sends the build.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Destination {
    TestFlight,
}

pub fn parse_destination(s: &str) -> Result<Destination, String> {
    match s {
        "testflight" => Ok(Destination::TestFlight),
        other => Err(format!(
            "`--upload {other}` is not a destination Sky uploads to. The one destination \
             is `--upload testflight` (App Store Connect, for an iOS or iPadOS build). An \
             Android build uploads its .aab in the Play Console."
        )),
    }
}

fn env_nonempty(name: &str) -> Option<String> {
    std::env::var(name).ok().filter(|v| !v.trim().is_empty())
}

// ─────────────────────────────────────────────────────────────────────────────
// Preconditions
// ─────────────────────────────────────────────────────────────────────────────

/// The App Store Connect API key, from the environment.
#[derive(Debug, Clone)]
pub struct AscKey {
    pub key_id: String,
    pub issuer_id: String,
    pub path: PathBuf,
}

const KEY_SETUP: &str = "Create a key once in App Store Connect: Users and Access → \
     Integrations → App Store Connect API → Team Keys → +, with the App Manager role. \
     Download the AuthKey_<KEY_ID>.p8 file (Apple lets you download it once) and keep \
     it out of the repository.";

/// Read and check the key variables. Nothing here prints the key's contents.
pub fn asc_key() -> Result<AscKey, String> {
    let missing: Vec<&str> = [ASC_KEY_ID, ASC_ISSUER_ID, ASC_KEY_PATH]
        .into_iter()
        .filter(|n| env_nonempty(n).is_none())
        .collect();
    if !missing.is_empty() {
        return Err(format!(
            "an upload to TestFlight signs in with an App Store Connect API key. Set {}. \
             {KEY_SETUP}",
            missing.join(", ")
        ));
    }
    let key_id = env_nonempty(ASC_KEY_ID)
        .unwrap_or_default()
        .trim()
        .to_string();
    let issuer_id = env_nonempty(ASC_ISSUER_ID)
        .unwrap_or_default()
        .trim()
        .to_string();
    let path = PathBuf::from(env_nonempty(ASC_KEY_PATH).unwrap_or_default());
    // The key id becomes part of a file name (`AuthKey_<id>.p8`), so it must be
    // a plain identifier: Apple's key ids are 10 upper-case letters and digits.
    if !key_id.chars().all(|c| c.is_ascii_alphanumeric()) {
        return Err(format!(
            "{ASC_KEY_ID}={key_id:?} is not an App Store Connect key id. It is the \
             10-character id (letters and digits) shown next to the key in Users and \
             Access → Integrations."
        ));
    }
    if !issuer_id.chars().all(|c| c.is_ascii_hexdigit() || c == '-') {
        return Err(format!(
            "{ASC_ISSUER_ID}={issuer_id:?} is not an App Store Connect issuer id. It is \
             the UUID shown as \"Issuer ID\" at the top of the API keys page."
        ));
    }
    if !path.is_file() {
        return Err(format!(
            "{ASC_KEY_PATH}={} is not a file. Point it at the AuthKey_<KEY_ID>.p8 you \
             downloaded from App Store Connect.",
            path.display()
        ));
    }
    let text = std::fs::read(&path)
        .map_err(|e| format!("{ASC_KEY_PATH}={}: cannot read it: {e}", path.display()))?;
    let looks_like_key = String::from_utf8_lossy(&text).contains("-----BEGIN PRIVATE KEY-----");
    if !looks_like_key {
        return Err(format!(
            "{ASC_KEY_PATH}={} is not an App Store Connect API key: a .p8 key file starts \
             with -----BEGIN PRIVATE KEY-----.",
            path.display()
        ));
    }
    Ok(AscKey {
        key_id,
        issuer_id,
        path,
    })
}

/// The signing identity must be a distribution identity: TestFlight refuses a
/// build signed with a development certificate.
pub fn check_distribution_identity(identity: &str) -> Result<(), String> {
    let dev = ["Apple Development", "iPhone Developer", "iOS Development"];
    if dev.iter().any(|d| identity.contains(d)) {
        return Err(format!(
            "SKY_IOS_SIGN_IDENTITY is {identity:?}, a development identity. TestFlight \
             takes a build signed for distribution: use your \"Apple Distribution: …\" \
             identity (`security find-identity -v -p codesigning`)."
        ));
    }
    Ok(())
}

/// The profile's plist, cut out of the CMS envelope of a `.mobileprovision`.
/// The envelope carries its content as plain bytes, so no decoder is needed.
fn profile_plist(bytes: &[u8], origin: &str) -> Result<Vec<(String, Value)>, String> {
    let text = String::from_utf8_lossy(bytes);
    let start = text.find("<?xml").or_else(|| text.find("<plist"));
    let end = text.find("</plist>").map(|e| e + "</plist>".len());
    match (start, end) {
        (Some(s), Some(e)) if s < e => plist::parse_entries(&text[s..e])
            .map_err(|err| format!("{origin} is not a readable provisioning profile: {err}")),
        _ => Err(format!(
            "{origin} is not a provisioning profile (no property list inside it)."
        )),
    }
}

/// An App Store distribution profile lists no devices, is not an enterprise
/// (in-house) profile, and does not allow a debugger to attach. A development
/// or ad hoc profile would be rejected by App Store Connect after the upload;
/// this says so before one.
pub fn check_app_store_profile(bytes: &[u8], origin: &str) -> Result<(), String> {
    let entries = profile_plist(bytes, origin)?;
    let get = |k: &str| entries.iter().find(|(ek, _)| ek == k).map(|(_, v)| v);
    let fix = "Create an \"App Store Connect\" distribution profile for the bundle id in \
               the Apple Developer portal (Certificates, Identifiers & Profiles → Profiles → \
               + → Distribution → App Store Connect), set SKY_IOS_PROVISIONING_PROFILE to \
               it, and package again.";
    if get("ProvisionedDevices").is_some() {
        return Err(format!(
            "{origin} lists devices: it is a development or ad hoc profile, which \
             TestFlight does not accept. {fix}"
        ));
    }
    if matches!(get("ProvisionsAllDevices"), Some(Value::Bool(true))) {
        return Err(format!(
            "{origin} is an enterprise (in-house) profile, which App Store Connect does not \
             accept. {fix}"
        ));
    }
    if let Some(Value::Dict(ents)) = get("Entitlements") {
        if ents
            .iter()
            .any(|(k, v)| k == "get-task-allow" && *v == Value::Bool(true))
        {
            return Err(format!(
                "{origin} allows a debugger to attach (get-task-allow): it is a development \
                 profile, which TestFlight does not accept. {fix}"
            ));
        }
    }
    Ok(())
}

// ─────────────────────────────────────────────────────────────────────────────
// The .ipa
// ─────────────────────────────────────────────────────────────────────────────

/// One entry of a zip archive's central directory.
#[derive(Debug, Clone)]
struct ZipEntry {
    name: String,
    method: u16,
    compressed: u64,
    local_offset: u64,
}

fn u16_at(b: &[u8], i: usize) -> Option<u16> {
    b.get(i..i + 2).map(|s| u16::from_le_bytes([s[0], s[1]]))
}

fn u32_at(b: &[u8], i: usize) -> Option<u32> {
    b.get(i..i + 4)
        .map(|s| u32::from_le_bytes([s[0], s[1], s[2], s[3]]))
}

/// Read the central directory of a zip archive (an `.ipa` is one).
fn zip_entries(bytes: &[u8]) -> Result<Vec<ZipEntry>, String> {
    // The end-of-central-directory record is in the last 64 KiB + 22 bytes.
    let floor = bytes.len().saturating_sub(65_536 + 22);
    let eocd = (floor..bytes.len().saturating_sub(21))
        .rev()
        .find(|&i| u32_at(bytes, i) == Some(0x0605_4b50))
        .ok_or("it is not a zip archive (no end-of-central-directory record)")?;
    let count = u16_at(bytes, eocd + 10).ok_or("truncated zip")? as usize;
    let cd_offset = u32_at(bytes, eocd + 16).ok_or("truncated zip")?;
    if cd_offset == u32::MAX || count == usize::from(u16::MAX) {
        return Err("it is a zip64 archive, which Sky does not read".to_string());
    }
    let mut at = cd_offset as usize;
    let mut out = Vec::with_capacity(count);
    for _ in 0..count {
        if u32_at(bytes, at) != Some(0x0201_4b50) {
            return Err("its central directory is damaged".to_string());
        }
        let method = u16_at(bytes, at + 10).ok_or("truncated zip")?;
        let compressed = u64::from(u32_at(bytes, at + 20).ok_or("truncated zip")?);
        let name_len = u16_at(bytes, at + 28).ok_or("truncated zip")? as usize;
        let extra_len = u16_at(bytes, at + 30).ok_or("truncated zip")? as usize;
        let comment_len = u16_at(bytes, at + 32).ok_or("truncated zip")? as usize;
        let local_offset = u64::from(u32_at(bytes, at + 42).ok_or("truncated zip")?);
        let name = bytes
            .get(at + 46..at + 46 + name_len)
            .ok_or("truncated zip")?;
        out.push(ZipEntry {
            name: String::from_utf8_lossy(name).into_owned(),
            method,
            compressed,
            local_offset,
        });
        at += 46 + name_len + extra_len + comment_len;
    }
    Ok(out)
}

/// The bytes of one entry: read directly when stored, through `unzip -p`
/// (present on every Mac, where the upload runs) when compressed.
fn zip_read(ipa: &Path, bytes: &[u8], e: &ZipEntry) -> Result<Vec<u8>, String> {
    if e.method == 0 {
        let at = e.local_offset as usize;
        if u32_at(bytes, at) != Some(0x0403_4b50) {
            return Err(format!("{}: damaged local header", e.name));
        }
        let name_len = u16_at(bytes, at + 26).ok_or("truncated zip")? as usize;
        let extra_len = u16_at(bytes, at + 28).ok_or("truncated zip")? as usize;
        let start = at + 30 + name_len + extra_len;
        return bytes
            .get(start..start + e.compressed as usize)
            .map(<[u8]>::to_vec)
            .ok_or_else(|| format!("{}: truncated entry", e.name));
    }
    let out = Command::new("unzip")
        .arg("-p")
        .arg(ipa)
        .arg(&e.name)
        .output()
        .map_err(|err| format!("run unzip to read {}: {err}", e.name))?;
    if !out.status.success() {
        return Err(format!("unzip could not read {} from the .ipa", e.name));
    }
    Ok(out.stdout)
}

/// Check that `ipa` is a SIGNED app archive with an App Store profile inside.
/// Returns the app bundle's name inside `Payload/`.
pub fn check_signed_ipa(ipa: &Path) -> Result<String, String> {
    let shown = ipa.display();
    let fix = "Set SKY_IOS_SIGN_IDENTITY (your \"Apple Distribution: …\" identity) and \
               SKY_IOS_PROVISIONING_PROFILE (an App Store distribution profile), then run \
               `sky package --release --target mobile:ios --upload testflight`.";
    let file_name = ipa
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    if file_name.ends_with("-unsigned.ipa") {
        return Err(format!(
            "{shown} is the UNSIGNED archive `sky package` makes without signing \
             configured. App Store Connect refuses an unsigned build. {fix}"
        ));
    }
    if !file_name.ends_with(".ipa") {
        return Err(format!("{shown} is not an .ipa file."));
    }
    let bytes = std::fs::read(ipa).map_err(|e| format!("read {shown}: {e}"))?;
    let entries = zip_entries(&bytes).map_err(|e| format!("{shown}: {e}"))?;
    let app = entries
        .iter()
        .find_map(|e| {
            let rest = e.name.strip_prefix("Payload/")?;
            let (dir, _) = rest.split_once(".app/")?;
            (!dir.contains('/')).then(|| format!("{dir}.app"))
        })
        .ok_or_else(|| {
            format!("{shown} has no Payload/<App>.app inside: it is not an iOS app archive.")
        })?;
    let has = |suffix: &str| {
        let want = format!("Payload/{app}/{suffix}");
        entries.iter().find(|e| e.name == want)
    };
    let (Some(_), Some(profile)) = (
        has("_CodeSignature/CodeResources"),
        has("embedded.mobileprovision"),
    ) else {
        return Err(format!(
            "{shown} is not signed: Payload/{app} has no code signature or no embedded \
             provisioning profile. App Store Connect refuses an unsigned build. {fix}"
        ));
    };
    let profile_bytes = zip_read(ipa, &bytes, profile)?;
    check_app_store_profile(
        &profile_bytes,
        &format!("the provisioning profile inside {shown}"),
    )?;
    Ok(app)
}

// ─────────────────────────────────────────────────────────────────────────────
// The tool
// ─────────────────────────────────────────────────────────────────────────────

/// The `xcrun` to run: `SKY_XCRUN` (tests only) or `xcrun` on PATH.
pub fn xcrun() -> OsString {
    std::env::var_os(XCRUN_OVERRIDE)
        .filter(|v| !v.is_empty())
        .unwrap_or_else(|| OsString::from("xcrun"))
}

/// `xcrun --find altool` must succeed: altool ships with Xcode on macOS.
pub fn find_altool(xcrun: &OsString) -> Result<(), String> {
    let ok = Command::new(xcrun)
        .args(["--find", "altool"])
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false);
    if ok {
        Ok(())
    } else {
        Err(
            "the upload runs Apple's `xcrun altool`, which ships with Xcode on macOS, and \
             `xcrun --find altool` did not find it. Install Xcode (the Command Line Tools \
             alone do not have it) and select it with `sudo xcode-select -s \
             /Applications/Xcode.app`, or upload the .ipa with Apple's Transporter app."
                .to_string(),
        )
    }
}

/// The two altool steps.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Step {
    Validate,
    Upload,
}

impl Step {
    fn label(self) -> &'static str {
        match self {
            Step::Validate => "validation",
            Step::Upload => "upload",
        }
    }
}

/// The arguments after `xcrun`, in the form `xcrun altool --help` documents
/// for the current Xcode. The key file is NOT among them: altool reads it
/// from `$API_PRIVATE_KEYS_DIR` (see [`KeyDir`]).
pub fn altool_args(step: Step, ipa: &Path, key: &AscKey) -> Vec<OsString> {
    let verb = match step {
        Step::Validate => "--validate-app",
        Step::Upload => "--upload-package",
    };
    vec![
        "altool".into(),
        verb.into(),
        ipa.as_os_str().to_owned(),
        "--api-key".into(),
        key.key_id.clone().into(),
        "--api-issuer".into(),
        key.issuer_id.clone().into(),
        "--output-format".into(),
        "json".into(),
    ]
}

/// The directory altool finds `AuthKey_<KEY_ID>.p8` in. When the key file
/// already has that name its own directory is used; otherwise the key is copied
/// into a private temporary directory (0700, the file 0600) that is removed
/// when this value is dropped.
pub struct KeyDir {
    pub dir: PathBuf,
    temporary: bool,
}

impl Drop for KeyDir {
    fn drop(&mut self) {
        if self.temporary {
            let _ = std::fs::remove_dir_all(&self.dir);
        }
    }
}

pub fn key_dir(key: &AscKey) -> Result<KeyDir, String> {
    let want = format!("AuthKey_{}.p8", key.key_id);
    if key.path.file_name().and_then(|n| n.to_str()) == Some(want.as_str()) {
        let dir = key
            .path
            .parent()
            .filter(|p| !p.as_os_str().is_empty())
            .map(Path::to_path_buf)
            .unwrap_or_else(|| PathBuf::from("."));
        return Ok(KeyDir {
            dir,
            temporary: false,
        });
    }
    let dir = std::env::temp_dir().join(format!(
        "sky-asc-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or(0)
    ));
    std::fs::create_dir_all(&dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    let guard = KeyDir {
        dir,
        temporary: true,
    };
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&guard.dir, std::fs::Permissions::from_mode(0o700))
            .map_err(|e| format!("restrict {}: {e}", guard.dir.display()))?;
    }
    let to = guard.dir.join(&want);
    std::fs::copy(&key.path, &to).map_err(|e| format!("copy the API key: {e}"))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&to, std::fs::Permissions::from_mode(0o600))
            .map_err(|e| format!("restrict the API key copy: {e}"))?;
    }
    Ok(guard)
}

/// What one altool step reported.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Report {
    pub ok: bool,
    /// Apple's error messages, as written.
    pub errors: Vec<String>,
    /// The success message, when one was printed.
    pub message: Option<String>,
    /// The delivery UUID of an upload, when one was printed.
    pub delivery: Option<String>,
}

fn json_str(v: &serde_json::Value, keys: &[&str]) -> Option<String> {
    keys.iter()
        .find_map(|k| v.get(*k).and_then(|x| x.as_str()))
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

/// Read altool's `--output-format json` output. Apple lists errors under
/// `product-errors` (each with a `message` and a `userInfo` that may carry the
/// failure reason); success carries `success-message` and, for an upload,
/// `details.delivery-uuid`. When the output is not JSON (an older altool, a
/// crash), the error lines are taken from the text. The exit status decides
/// success; an error list with a zero exit is still a failure.
pub fn parse_report(exit_ok: bool, stdout: &str, stderr: &str) -> Report {
    let mut r = Report::default();
    let json = stdout
        .find('{')
        .and_then(|s| serde_json::from_str::<serde_json::Value>(stdout[s..].trim()).ok());
    if let Some(v) = &json {
        if let Some(errs) = v.get("product-errors").and_then(|e| e.as_array()) {
            for e in errs {
                let mut line = json_str(e, &["message"]).unwrap_or_default();
                if let Some(info) = e.get("userInfo") {
                    for k in ["NSLocalizedFailureReason", "NSLocalizedRecoverySuggestion"] {
                        if let Some(extra) = json_str(info, &[k]) {
                            if !line.contains(&extra) {
                                if !line.is_empty() {
                                    line.push(' ');
                                }
                                line.push_str(&extra);
                            }
                        }
                    }
                }
                if !line.is_empty() {
                    r.errors.push(line);
                }
            }
        }
        r.message = json_str(v, &["success-message"]);
        r.delivery = v
            .get("details")
            .and_then(|d| json_str(d, &["delivery-uuid"]))
            .or_else(|| json_str(v, &["delivery-uuid"]));
    } else {
        for line in stdout.lines().chain(stderr.lines()) {
            let t = line.trim();
            let lower = t.to_ascii_lowercase();
            if lower.contains("error") && !t.is_empty() {
                r.errors.push(t.trim_start_matches("*** ").to_string());
            }
        }
    }
    if !exit_ok && r.errors.is_empty() {
        let tail: Vec<&str> = stdout
            .lines()
            .chain(stderr.lines())
            .map(str::trim)
            .filter(|l| !l.is_empty())
            .collect();
        let from = tail.len().saturating_sub(5);
        r.errors.push(if tail.is_empty() {
            "altool failed and printed nothing.".to_string()
        } else {
            tail[from..].join(" | ")
        });
    }
    r.ok = exit_ok && r.errors.is_empty();
    r
}

/// The Sky-side fix for the Apple errors a Sky app can meet.
pub fn hints(errors: &[String], bundle_id: &str, build: &str) -> Vec<String> {
    let all = errors.join("\n").to_ascii_lowercase();
    let has = |needles: &[&str]| needles.iter().any(|n| all.contains(n));
    let mut out = Vec::new();
    if has(&[
        "bundle version must be higher",
        "redundant binary upload",
        "has already been uploaded",
        "invalid.duplicate",
        "cfbundleversion",
    ]) {
        out.push(format!(
            "every upload needs a build number higher than the last one App Store Connect \
             has for this version. This build is {build}: raise `Bundle.withBuild` and package \
             again."
        ));
    }
    if has(&["cfbundleshortversionstring", "itms-90062", "train version"]) {
        out.push(
            "the version is closed for new builds: raise `Bundle.withVersion` (the marketing \
             version) and package again."
                .to_string(),
        );
    }
    if has(&[
        "no suitable application records",
        "cannot determine the apple id",
        "no app record",
        "could not find the app",
    ]) {
        out.push(format!(
            "App Store Connect has no app with bundle id {bundle_id}. Create it once: My Apps \
             → + → New App, and choose {bundle_id} (register the id under Identifiers in the \
             Apple Developer portal first if it is not listed)."
        ));
    }
    if has(&[
        "not_authorized",
        "unable to authenticate",
        "authentication credentials",
        "invalid issuer",
        "forbidden",
    ]) {
        out.push(format!(
            "Apple did not accept the API key. Check {ASC_KEY_ID} and {ASC_ISSUER_ID} against \
             Users and Access → Integrations, that the key is not revoked, and that it has the \
             App Manager (or Admin) role."
        ));
    }
    if has(&["authkey_", "private key", "api key file"]) {
        out.push(format!(
            "altool could not load the key file. Check that {ASC_KEY_PATH} is the .p8 \
             downloaded for {ASC_KEY_ID}."
        ));
    }
    if has(&["itms-90161", "invalid provisioning profile", "itms-90164"]) {
        out.push(
            "sign with an App Store distribution profile for this bundle id \
             (SKY_IOS_PROVISIONING_PROFILE) and an \"Apple Distribution\" identity \
             (SKY_IOS_SIGN_IDENTITY)."
                .to_string(),
        );
    }
    if has(&["itms-90683", "purpose string", "usagedescription"]) {
        out.push(
            "a permission has no purpose string: declare it with `Bundle.withUsage` in the \
             `bundle` binding."
                .to_string(),
        );
    }
    if has(&[
        "itms-90022",
        "itms-90704",
        "itms-90713",
        "missing required icon",
        "cfbundleiconname",
    ]) {
        out.push(
            "the app has no App Store icon: set `Bundle.withIcon \"assets/icon.png\"` (a \
             1024×1024 PNG with no transparency) and package again."
                .to_string(),
        );
    }
    out
}

/// Run one altool step and read its report. `key_dir` is passed to altool as
/// `API_PRIVATE_KEYS_DIR`.
pub fn run_step(
    xcrun: &OsString,
    step: Step,
    ipa: &Path,
    key: &AscKey,
    key_dir: &Path,
) -> Result<Report, String> {
    let out = Command::new(xcrun)
        .args(altool_args(step, ipa, key))
        .env("API_PRIVATE_KEYS_DIR", key_dir)
        .stdin(std::process::Stdio::null())
        .output()
        .map_err(|e| format!("run `xcrun altool` for the {}: {e}", step.label()))?;
    Ok(parse_report(
        out.status.success(),
        &String::from_utf8_lossy(&out.stdout),
        &String::from_utf8_lossy(&out.stderr),
    ))
}

/// Validate, then upload. Prints progress and Apple's messages; the error names
/// the step that failed with Apple's text and the Sky fixes.
pub fn upload_testflight(
    xcrun: &OsString,
    ipa: &Path,
    key: &AscKey,
    bundle_id: &str,
    build: &str,
) -> Result<Report, String> {
    let dir = key_dir(key)?;
    for step in [Step::Validate, Step::Upload] {
        eprintln!(
            "  TestFlight: {} of {} (bundle id {bundle_id}, build {build})…",
            step.label(),
            ipa.display()
        );
        let report = run_step(xcrun, step, ipa, key, &dir.dir)?;
        if !report.ok {
            let mut msg = format!(
                "App Store Connect refused the {} of {}:\n",
                step.label(),
                ipa.display()
            );
            for e in &report.errors {
                msg.push_str(&format!("    Apple: {e}\n"));
            }
            for h in hints(&report.errors, bundle_id, build) {
                msg.push_str(&format!("    fix: {h}\n"));
            }
            if step == Step::Validate {
                msg.push_str("    Nothing was uploaded.");
            }
            return Err(msg.trim_end().to_string());
        }
        if step == Step::Upload {
            return Ok(report);
        }
    }
    unreachable!("the loop returns on the upload step")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn key() -> AscKey {
        AscKey {
            key_id: "ABC123DEF4".into(),
            issuer_id: "57246542-96fe-1a63-e053-0824d011072a".into(),
            path: PathBuf::from("/keys/AuthKey_ABC123DEF4.p8"),
        }
    }

    #[test]
    fn the_arguments_follow_altool_and_never_carry_the_key_file() {
        let v = altool_args(Step::Validate, Path::new("/r/App.ipa"), &key());
        let v: Vec<String> = v.iter().map(|s| s.to_string_lossy().into_owned()).collect();
        assert_eq!(
            v,
            [
                "altool",
                "--validate-app",
                "/r/App.ipa",
                "--api-key",
                "ABC123DEF4",
                "--api-issuer",
                "57246542-96fe-1a63-e053-0824d011072a",
                "--output-format",
                "json"
            ]
        );
        let u = altool_args(Step::Upload, Path::new("/r/App.ipa"), &key());
        assert_eq!(u[1], "--upload-package");
        for a in v.iter() {
            assert!(!a.contains(".p8"), "the key file is never an argument: {a}");
        }
    }

    #[test]
    fn a_named_key_file_is_used_in_place() {
        let d = key_dir(&key()).unwrap();
        assert_eq!(d.dir, PathBuf::from("/keys"));
        assert!(!d.temporary);
    }

    #[test]
    fn apple_json_errors_are_read_with_their_reason() {
        let out = r#"{"tool-version":"27.0.5","product-errors":[{"code":-19232,"message":"The provided entity includes an attribute with a value that has already been used","userInfo":{"NSLocalizedFailureReason":"The bundle version must be higher than the previously uploaded version: '3'."}}]}"#;
        let r = parse_report(false, out, "");
        assert!(!r.ok);
        assert_eq!(r.errors.len(), 1);
        assert!(r.errors[0].contains("already been used"));
        assert!(r.errors[0].contains("must be higher"));
        let h = hints(&r.errors, "com.example.vault", "3");
        assert!(h.iter().any(|h| h.contains("Bundle.withBuild")), "{h:?}");
    }

    #[test]
    fn a_success_report_carries_the_delivery_uuid() {
        let out = r#"{"success-message":"No errors uploading 'App.ipa'","details":{"delivery-uuid":"1f0c-42"}}"#;
        let r = parse_report(true, out, "");
        assert!(r.ok);
        assert_eq!(r.delivery.as_deref(), Some("1f0c-42"));
        assert_eq!(r.message.as_deref(), Some("No errors uploading 'App.ipa'"));
    }

    #[test]
    fn errors_with_a_zero_exit_are_still_a_failure_and_text_output_is_read() {
        let out = r#"{"product-errors":[{"message":"Unable to authenticate."}]}"#;
        assert!(!parse_report(true, out, "").ok);
        let r = parse_report(false, "", "*** Error: Unable to upload archive. (-19000)\n");
        assert_eq!(r.errors, ["Error: Unable to upload archive. (-19000)"]);
        let r = parse_report(false, "", "");
        assert_eq!(r.errors, ["altool failed and printed nothing."]);
    }

    fn profile(extra: &str) -> Vec<u8> {
        format!(
            "0\u{80}\u{6}DER-prefix<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist \
             PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"x\">\n<plist version=\"1.0\"><dict>\
             <key>Name</key><string>p</string>{extra}</dict></plist>DER-suffix"
        )
        .into_bytes()
    }

    #[test]
    fn only_an_app_store_profile_passes() {
        let ok = profile("<key>Entitlements</key><dict><key>get-task-allow</key><false/></dict>");
        assert!(check_app_store_profile(&ok, "p").is_ok());
        let dev = profile("<key>ProvisionedDevices</key><array><string>00008</string></array>");
        assert!(check_app_store_profile(&dev, "p")
            .unwrap_err()
            .contains("lists devices"));
        let ent = profile("<key>ProvisionsAllDevices</key><true/>");
        assert!(check_app_store_profile(&ent, "p")
            .unwrap_err()
            .contains("enterprise"));
        let dbg = profile("<key>Entitlements</key><dict><key>get-task-allow</key><true/></dict>");
        assert!(check_app_store_profile(&dbg, "p")
            .unwrap_err()
            .contains("get-task-allow"));
        assert!(check_app_store_profile(b"not a profile", "p").is_err());
    }

    #[test]
    fn a_development_identity_is_refused() {
        assert!(check_distribution_identity("Apple Development: A (T)").is_err());
        assert!(check_distribution_identity("iPhone Developer: A (T)").is_err());
        assert!(check_distribution_identity("Apple Distribution: A (T)").is_ok());
    }

    #[test]
    fn an_unknown_destination_is_refused() {
        assert_eq!(parse_destination("testflight"), Ok(Destination::TestFlight));
        assert!(parse_destination("play")
            .unwrap_err()
            .contains("Play Console"));
    }
}
