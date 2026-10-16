//! The build-speed benchmark app generator (`xtask bench-gen`).
//!
//! ```text
//! cargo run -p xtask -- bench-gen --fields N --arms M --modules K --seed S --out DIR
//! ```
//!
//! The app is shaped like a medium production `Std.App` web app, so a build of
//! it costs what a real app costs, and it grows along the two axes that made
//! real apps slow to build: the number of Model fields and the number of
//! `update` arms. It is the input of `scripts/bench-build.sh` (wall time, CPU,
//! peak memory, against `docs/perf/build-budget.json`) and of the counted
//! growth guard `rust/crates/sky/tests/build_growth_flow.rs` (generated lines
//! and emitted Go must grow linearly).
//!
//! This file is pure (no dependency outside `std`): the growth guard includes
//! it with `#[path]`, so the generator stays out of the compiler's crates.
//!
//! What it holds, all with generic names:
//!
//! * a `Model` of N fields: `String`, `Int`, `Maybe String`, `List Item`,
//!   `Dict String Int`, a nested `Profile` record (with a nested `Address`)
//!   and `Bool`, the first seven fields one of each;
//! * M `Msg` arms: pure edits (`{ model | f = x }`), server reads and writes
//!   (`Persist`, a `Std.Db.Store` over `Codec.auto`), whole-model server arms
//!   (`stampK model`), arms with arguments, `Cmd.perform` chains over a server
//!   `Task`, plus a typed sign-in form (`Ui.form` + `Ui.onSubmit` into a
//!   record) that calls `Std.Auth`, and sign-out;
//! * `Std.Ui` views across K modules in a layered import DAG:
//!   `Domain` <- `Persist`, `State` <- `View.Common` <- `View.Section1` <-
//!   `View.Section2` <- … <- `Main`. Section `j` imports section `j - 1`.
//!
//! It must `sky check` and build for `--target web:app` and `--target web`.
//! The output is a pure function of the four numbers: the same arguments
//! always write the same bytes.

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::path::Path;

/// The generator's inputs. See the module docs.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Params {
    /// Model fields (at least [`MIN_FIELDS`]).
    pub fields: usize,
    /// Generated `Msg` constructors (at least [`MIN_ARMS`]). The sign-in and
    /// sign-out arms come on top.
    pub arms: usize,
    /// Modules in the project (at least [`MIN_MODULES`]); the ones beyond the
    /// five fixed modules are view sections.
    pub modules: usize,
    pub seed: u64,
}

pub const MIN_FIELDS: usize = 7;
pub const MIN_ARMS: usize = 12;
/// `Domain`, `Persist`, `State`, `View.Common`, `Main`, and one section.
pub const MIN_MODULES: usize = 6;
const FIXED_MODULES: usize = 5;

impl Params {
    pub fn validate(&self) -> Result<(), String> {
        if self.fields < MIN_FIELDS {
            return Err(format!("--fields must be at least {MIN_FIELDS}"));
        }
        if self.arms < MIN_ARMS {
            return Err(format!("--arms must be at least {MIN_ARMS}"));
        }
        if self.modules < MIN_MODULES {
            return Err(format!("--modules must be at least {MIN_MODULES}"));
        }
        Ok(())
    }

    fn sections(&self) -> usize {
        self.modules - FIXED_MODULES
    }
}

/// A deterministic 64-bit LCG (Knuth's MMIX constants). No dependency, the
/// same sequence on every platform.
struct Lcg(u64);

impl Lcg {
    fn new(seed: u64) -> Lcg {
        let mut l = Lcg(seed ^ 0x9E37_79B9_7F4A_7C15);
        l.next();
        l
    }
    fn next(&mut self) -> u64 {
        self.0 = self
            .0
            .wrapping_mul(6_364_136_223_846_793_005)
            .wrapping_add(1_442_695_040_888_963_407);
        self.0 >> 33
    }
    fn below(&mut self, n: usize) -> usize {
        (self.next() % n as u64) as usize
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum FieldKind {
    Str,
    Int,
    MaybeStr,
    Items,
    Tags,
    Profile,
    Flag,
}

const FIELD_KINDS: [FieldKind; 7] = [
    FieldKind::Str,
    FieldKind::Int,
    FieldKind::MaybeStr,
    FieldKind::Items,
    FieldKind::Tags,
    FieldKind::Profile,
    FieldKind::Flag,
];

impl FieldKind {
    fn sky_type(self) -> &'static str {
        match self {
            FieldKind::Str => "String",
            FieldKind::Int => "Int",
            FieldKind::MaybeStr => "Maybe String",
            FieldKind::Items => "List Item",
            FieldKind::Tags => "Dict String Int",
            FieldKind::Profile => "Profile",
            FieldKind::Flag => "Bool",
        }
    }
    fn initial(self) -> &'static str {
        match self {
            FieldKind::Str => "\"\"",
            FieldKind::Int => "0",
            FieldKind::MaybeStr => "Nothing",
            FieldKind::Items => "[]",
            FieldKind::Tags => "Dict.empty",
            FieldKind::Profile => "blankProfile",
            FieldKind::Flag => "False",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum ArmKind {
    /// Server read: `{ model | f = Persist.loadItems () }`.
    Load,
    /// Pure edit with an argument: `{ model | f = s }`.
    Edit,
    /// Whole-model server write: `Persist.createItem (stampK model)`.
    Save,
    /// Whole-model server write with an argument.
    Post,
    /// Server write with an argument.
    Add,
    /// Pure, `Int` argument.
    Inc,
    /// `Cmd.perform` over a server `Task`, answered by a second arm
    /// (`CountedK (Result Error Int)`). Two constructors.
    Refresh,
    Toggle,
    /// `Dict.insert` with an argument.
    Tag,
    /// Nested record update with an argument.
    Rename,
    /// `Maybe` with an argument.
    Pick,
}

/// The first eleven arms, one of each kind, `Load` first (`init` chains to
/// it). Later arms are drawn from [`LATER_ARMS`].
const ARM_KINDS: [ArmKind; 11] = [
    ArmKind::Load,
    ArmKind::Edit,
    ArmKind::Save,
    ArmKind::Post,
    ArmKind::Add,
    ArmKind::Inc,
    ArmKind::Refresh,
    ArmKind::Toggle,
    ArmKind::Tag,
    ArmKind::Rename,
    ArmKind::Pick,
];

/// The cycle for arms after the first eleven. A quarter are whole-model server
/// arms: real `update`s often hand the whole model to a helper (a payload
/// builder, a validator), and the split's cost of those arms is what made
/// medium apps slow to build.
const LATER_ARMS: [ArmKind; 12] = [
    ArmKind::Load,
    ArmKind::Edit,
    ArmKind::Save,
    ArmKind::Add,
    ArmKind::Inc,
    ArmKind::Post,
    ArmKind::Refresh,
    ArmKind::Toggle,
    ArmKind::Tag,
    ArmKind::Save,
    ArmKind::Rename,
    ArmKind::Pick,
];

impl ArmKind {
    fn field_kind(self) -> FieldKind {
        match self {
            ArmKind::Load | ArmKind::Save | ArmKind::Post | ArmKind::Add => FieldKind::Items,
            ArmKind::Edit => FieldKind::Str,
            ArmKind::Inc | ArmKind::Refresh => FieldKind::Int,
            ArmKind::Toggle => FieldKind::Flag,
            ArmKind::Tag => FieldKind::Tags,
            ArmKind::Rename => FieldKind::Profile,
            ArmKind::Pick => FieldKind::MaybeStr,
        }
    }
    fn name(self) -> &'static str {
        match self {
            ArmKind::Load => "Load",
            ArmKind::Edit => "Edit",
            ArmKind::Save => "Save",
            ArmKind::Post => "Post",
            ArmKind::Add => "Add",
            ArmKind::Inc => "Inc",
            ArmKind::Refresh => "Refresh",
            ArmKind::Toggle => "Toggle",
            ArmKind::Tag => "Tag",
            ArmKind::Rename => "Rename",
            ArmKind::Pick => "Pick",
        }
    }
}

struct Arm {
    /// 1-based arm number; the constructor is `<Kind><index>`.
    index: usize,
    kind: ArmKind,
    /// 1-based Model field the arm targets (`f<field>`).
    field: usize,
}

impl Arm {
    fn ctor(&self) -> String {
        format!("{}{}", self.kind.name(), self.index)
    }
}

struct Plan {
    params: Params,
    /// `kinds[k - 1]` is the kind of field `fk`.
    kinds: Vec<FieldKind>,
    arms: Vec<Arm>,
}

impl Plan {
    fn new(params: Params) -> Plan {
        let mut rng = Lcg::new(params.seed);
        // The first seven fields: one of each kind, in a seeded order.
        let mut first = FIELD_KINDS;
        for i in (1..first.len()).rev() {
            let j = rng.below(i + 1);
            first.swap(i, j);
        }
        let mut kinds: Vec<FieldKind> = first.to_vec();
        while kinds.len() < params.fields {
            kinds.push(FIELD_KINDS[rng.below(FIELD_KINDS.len())]);
        }
        let mut arms = Vec::new();
        let mut ctors = 0;
        let mut index = 0;
        while ctors < params.arms {
            index += 1;
            let mut kind = if index <= ARM_KINDS.len() {
                ARM_KINDS[index - 1]
            } else {
                // A fixed cycle, so each kind's share is the same at every
                // size (a random draw let the whole-model count drift).
                LATER_ARMS[(index + params.seed as usize) % LATER_ARMS.len()]
            };
            if kind == ArmKind::Refresh && params.arms - ctors < 2 {
                kind = ArmKind::Edit;
            }
            let candidates: Vec<usize> = kinds
                .iter()
                .enumerate()
                .filter(|(_, k)| **k == kind.field_kind())
                .map(|(i, _)| i + 1)
                .collect();
            let field = candidates[rng.below(candidates.len())];
            ctors += if kind == ArmKind::Refresh { 2 } else { 1 };
            arms.push(Arm { index, kind, field });
        }
        Plan {
            params,
            kinds,
            arms,
        }
    }

    fn kind(&self, field: usize) -> FieldKind {
        self.kinds[field - 1]
    }

    /// 1-based view section of a field or arm number.
    fn section_of(&self, n: usize) -> usize {
        (n - 1) % self.params.sections() + 1
    }
}

/// Generate the project: relative path -> file contents.
pub fn generate(params: Params) -> Result<BTreeMap<String, String>, String> {
    params.validate()?;
    let plan = Plan::new(params);
    let mut files = BTreeMap::new();
    files.insert("sky.toml".to_string(), SKY_TOML.to_string());
    files.insert("src/Domain.sky".to_string(), DOMAIN.to_string());
    files.insert("src/Persist.sky".to_string(), PERSIST.to_string());
    files.insert("src/State.sky".to_string(), state(&plan));
    files.insert("src/View/Common.sky".to_string(), COMMON.to_string());
    for j in 1..=params.sections() {
        files.insert(format!("src/View/Section{j}.sky"), section(&plan, j));
    }
    files.insert("src/Main.sky".to_string(), main_module(&plan));
    Ok(files)
}

/// Write the project under `out`, which must be absent or empty.
pub fn write(params: Params, out: &Path) -> Result<usize, String> {
    let files = generate(params)?;
    if out.exists() {
        let mut entries = std::fs::read_dir(out).map_err(|e| format!("{}: {e}", out.display()))?;
        if entries.next().is_some() {
            return Err(format!(
                "{} is not empty; bench-gen writes into an absent or empty directory",
                out.display()
            ));
        }
    }
    let mut lines = 0;
    for (rel, text) in &files {
        let path = out.join(rel);
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
        }
        std::fs::write(&path, text).map_err(|e| format!("{}: {e}", path.display()))?;
        lines += text.lines().count();
    }
    Ok(lines)
}

const SKY_TOML: &str = "name = \"bench-app\"
version = \"0.1.0\"
entry = \"src/Main.sky\"

[source]
root = \"src\"
";

const DOMAIN: &str = r#"module Domain exposing (Item, Address, Profile, Session, SignInForm, Page(..), pageCodec, blankItem, blankProfile, renameProfile)

-- Generated by `xtask bench-gen`: the pure records every other module shares.

import Sky.Core.Prelude exposing (..)
import Std.Codec as Codec exposing (Codec)


type alias Item =
    { id : Int
    , title : String
    , note : String
    , qty : Int
    , done : Bool
    }


type alias Address =
    { street : String
    , city : String
    }


type alias Profile =
    { name : String
    , email : String
    , address : Address
    }


type alias Session =
    { userId : Int
    , email : String
    }


type alias SignInForm =
    { email : String
    , password : String
    }


type Page
    = Home
    | Settings


-- The page rides the Sky.Spa wire with every whole-model arm.
pageCodec : Codec Page
pageCodec =
    Codec.enum [ ( Home, "home" ), ( Settings, "settings" ) ]


blankItem : Item
blankItem =
    { id = 0, title = "", note = "", qty = 0, done = False }


blankProfile : Profile
blankProfile =
    { name = "", email = "", address = { street = "", city = "" } }


renameProfile : String -> Profile -> Profile
renameProfile name p =
    let
        a =
            p.address
    in
    { p | name = name, address = { a | city = name } }
"#;

const PERSIST: &str = r#"module Persist exposing (loadItems, createItem, countItems, signIn)

-- Generated by `xtask bench-gen`: the server side. Every binding reaches a
-- database or auth kernel, so the Sky.Spa split keeps this module on the
-- backend.

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.System as System
import Sky.Core.Error as Error exposing (Error)
import Std.Log exposing (println)
import Std.Codec as Codec
import Std.Db as Db exposing (Db)
import Std.Db.Store as Store exposing (Store)
import Std.Auth as Auth
import Domain exposing (Item, Session, blankItem)


itemStore : Store Item
itemStore =
    Store.fromCodec "items" (Codec.auto blankItem)
        |> Store.serial "id"


dbConn : Db
dbConn =
    case Task.run (Db.connect ()) of
        Ok c ->
            let
                _ =
                    Task.run (Store.create c itemStore)
            in
            c

        Err e ->
            let
                _ =
                    println ("cannot open the database: " ++ Error.toString e)
            in
            System.exit 1


loadItems : () -> List Item
loadItems _ =
    case Task.run (Store.query itemStore |> Store.orderAsc "id" |> Store.toList dbConn) of
        Ok rows ->
            rows

        Err _ ->
            []


createItem : String -> Int
createItem title =
    case Task.run (Store.insert dbConn itemStore { blankItem | title = String.left 120 (String.trim title) }) of
        Ok newId ->
            newId

        Err _ ->
            0


countItems : () -> Task Error Int
countItems _ =
    Store.count dbConn (Store.query itemStore)


signIn : String -> String -> Maybe Session
signIn email password =
    case Task.run (Auth.login dbConn (String.toLower (String.trim email)) password) of
        Ok uid ->
            Just { userId = uid, email = email }

        Err _ ->
            Nothing
"#;

const COMMON: &str = r#"module View.Common exposing (label, field, panel, actionButton, itemRow, onOff)

-- Generated by `xtask bench-gen`: view helpers every section uses.

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Std.Ui as Ui exposing (Element)
import Std.Ui.Font as Font
import Std.Ui.Border as Border
import Std.Ui.Background as Background
import Domain exposing (Item)


label : String -> Element msg
label s =
    Ui.el [ Font.size 13, Font.color (Ui.rgb255 100 116 139) ] (Ui.text s)


field : String -> String -> String -> Element msg
field title name kind =
    Ui.column
        [ Ui.spacing 4 ]
        [ label title
        , Ui.input [ Ui.name name, Ui.htmlAttribute "type" kind, Border.width 1, Ui.paddingXY 8 6 ]
        ]


panel : String -> List (Element msg) -> Element msg
panel title body =
    Ui.column
        [ Ui.spacing 8, Ui.padding 12, Border.width 1, Border.rounded 8, Background.color (Ui.rgb255 255 255 255) ]
        (Ui.el [ Font.bold ] (Ui.text title) :: body)


actionButton : String -> msg -> Element msg
actionButton text msg =
    Ui.button [ Ui.paddingXY 12 6, Border.rounded 6 ] { onPress = Just msg, label = Ui.text text }


itemRow : Item -> Element msg
itemRow item =
    Ui.row [ Ui.spacing 8 ] [ Ui.text item.title, label (String.fromInt item.qty) ]


onOff : Bool -> String
onOff b =
    if b then
        "on"

    else
        "off"
"#;

fn state(plan: &Plan) -> String {
    let mut s = String::new();
    let saves: Vec<&Arm> = plan
        .arms
        .iter()
        .filter(|a| matches!(a.kind, ArmKind::Save | ArmKind::Post))
        .collect();
    let stamps: Vec<String> = saves.iter().map(|a| format!("stamp{}", a.index)).collect();
    let _ = writeln!(
        s,
        "module State exposing (Model, Msg(..), init, snapshot{})",
        stamps.iter().map(|n| format!(", {n}")).collect::<String>()
    );
    s.push_str(
        "\n-- Generated by `xtask bench-gen`: the Model, the Msg union and init.\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Dict as Dict exposing (Dict)\n\
         import Sky.Core.Error exposing (Error)\n\
         import Sky.Core.String as String\n\
         import Sky.Core.List as List\n\
         import Sky.Core.Task as Task\n\
         import Std.Cmd as Cmd\n\
         import Domain exposing (..)\n\n\n\
         type alias Model =\n    { page : Page\n    , session : Maybe Session\n    , status : String\n",
    );
    for (i, k) in plan.kinds.iter().enumerate() {
        let _ = writeln!(s, "    , f{} : {}", i + 1, k.sky_type());
    }
    s.push_str("    }\n\n\ntype Msg\n    = SubmitSignIn SignInForm\n    | SignOut\n");
    for a in &plan.arms {
        let ctor = a.ctor();
        match a.kind {
            ArmKind::Edit
            | ArmKind::Post
            | ArmKind::Add
            | ArmKind::Tag
            | ArmKind::Rename
            | ArmKind::Pick => {
                let _ = writeln!(s, "    | {ctor} String");
            }
            ArmKind::Inc => {
                let _ = writeln!(s, "    | {ctor} Int");
            }
            ArmKind::Refresh => {
                let _ = writeln!(
                    s,
                    "    | {ctor}\n    | Counted{} (Result Error Int)",
                    a.index
                );
            }
            ArmKind::Load | ArmKind::Save | ArmKind::Toggle => {
                let _ = writeln!(s, "    | {ctor}");
            }
        }
    }
    let first_load = plan
        .arms
        .iter()
        .find(|a| a.kind == ArmKind::Load)
        .map(Arm::ctor)
        .unwrap_or_else(|| "SignOut".to_string());
    s.push_str(
        "\n\ninit : () -> ( Model, Cmd Msg )\ninit _ =\n    ( { page = Home\n      , session = Nothing\n      , status = \"\"\n",
    );
    for (i, k) in plan.kinds.iter().enumerate() {
        let _ = writeln!(s, "      , f{} = {}", i + 1, k.initial());
    }
    let _ = writeln!(
        s,
        "      }}\n    , Cmd.perform (Task.succeed ()) (\\_ -> {first_load})\n    )"
    );
    // `snapshot` hands the whole model on, as a payload builder or an audit
    // log does, so an arm that calls a `stampK` reads the whole model.
    s.push_str("\n\nsnapshot : Model -> List Model\nsnapshot model =\n    [ model ]\n");
    for a in &saves {
        let _ = write!(
            s,
            "\n\nstamp{i} : Model -> String\nstamp{i} model =\n    String.fromInt (List.length (snapshot model)) ++ \"/{i}/\" ++ String.fromInt (List.length model.f{f})\n",
            i = a.index,
            f = a.field
        );
    }
    s
}

fn section(plan: &Plan, j: usize) -> String {
    let mut s = String::new();
    let _ = writeln!(
        s,
        "module View.Section{j} exposing (section{j}, badge{j})\n\n\
         -- Generated by `xtask bench-gen`: view section {j} of {}.\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Sky.Core.List as List\n\
         import Sky.Core.Dict as Dict\n\
         import Std.Ui as Ui exposing (Element)\n\
         import Std.Ui.Events as Ev\n\
         import Domain exposing (..)\n\
         import State exposing (..)\n\
         import View.Common as C",
        plan.params.sections()
    );
    if j > 1 {
        let _ = writeln!(s, "import View.Section{} as Prev", j - 1);
    }
    let badge_body = if j > 1 {
        format!("Prev.badge{} s", j - 1)
    } else {
        "C.label (\"[\" ++ s ++ \"]\")".to_string()
    };
    let _ = write!(
        s,
        "\n\nbadge{j} : String -> Element msg\nbadge{j} s =\n    {badge_body}\n\n\n\
         section{j} : Model -> Element Msg\nsection{j} model =\n    C.panel \"Section {j}\"\n"
    );
    let mut rows: Vec<String> = Vec::new();
    for k in (1..=plan.params.fields).filter(|k| plan.section_of(*k) == j) {
        let shown = match plan.kind(k) {
            FieldKind::Str => format!("C.label model.f{k}"),
            FieldKind::Int => format!("C.label (String.fromInt model.f{k})"),
            FieldKind::MaybeStr => format!("badge{j} (Maybe.withDefault \"none\" model.f{k})"),
            FieldKind::Items => {
                format!("Ui.column [ Ui.spacing 4 ] (List.map C.itemRow model.f{k})")
            }
            FieldKind::Tags => format!("C.label (String.join \", \" (Dict.keys model.f{k}))"),
            FieldKind::Profile => {
                format!("C.label (model.f{k}.name ++ \", \" ++ model.f{k}.address.city)")
            }
            FieldKind::Flag => format!("C.label (C.onOff model.f{k})"),
        };
        rows.push(format!(
            "Ui.row [ Ui.spacing 8 ] [ C.label \"f{k}\", {shown} ]"
        ));
    }
    for a in plan.arms.iter().filter(|a| plan.section_of(a.index) == j) {
        let (c, f) = (a.ctor(), a.field);
        rows.push(match a.kind {
            ArmKind::Load => format!("C.actionButton \"load {f}\" {c}"),
            ArmKind::Save => format!("C.actionButton \"save {f}\" {c}"),
            ArmKind::Post => format!(
                "Ui.input [ Ui.htmlAttribute \"placeholder\" \"{c}\", Ev.onEnter ({c} \"note\") ]"
            ),
            ArmKind::Refresh => format!("C.actionButton \"count {f}\" {c}"),
            ArmKind::Toggle => format!("C.actionButton (\"toggle \" ++ C.onOff model.f{f}) {c}"),
            ArmKind::Inc => format!("C.actionButton \"+1 f{f}\" ({c} 1)"),
            ArmKind::Edit => {
                format!("Ui.input [ Ui.htmlAttribute \"value\" model.f{f}, Ev.onInput {c} ]")
            }
            ArmKind::Rename => {
                format!("Ui.input [ Ui.htmlAttribute \"value\" model.f{f}.name, Ev.onInput {c} ]")
            }
            ArmKind::Add | ArmKind::Tag | ArmKind::Pick => {
                format!("Ui.input [ Ui.htmlAttribute \"placeholder\" \"{c}\", Ev.onInput {c} ]")
            }
        });
    }
    if rows.is_empty() {
        rows.push(format!("badge{j} \"empty\""));
    }
    for (i, r) in rows.iter().enumerate() {
        let lead = if i == 0 { "        [ " } else { "        , " };
        let _ = writeln!(s, "{lead}{r}");
    }
    s.push_str("        ]\n");
    s
}

fn main_module(plan: &Plan) -> String {
    let mut s = String::from(
        "module Main exposing (main)\n\n\
         -- Generated by `xtask bench-gen`: update, the sign-in form and the app.\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Dict as Dict\n\
         import Sky.Core.Error exposing (Error)\n\
         import Sky.Core.String as String\n\
         import Sky.Core.Task as Task\n\
         import Std.App as App\n\
         import Std.Cmd as Cmd\n\
         import Std.Sub as Sub\n\
         import Std.Ui as Ui exposing (Element)\n\
         import Domain exposing (..)\n\
         import State exposing (..)\n\
         import Persist\n\
         import View.Common as C\n",
    );
    for j in 1..=plan.params.sections() {
        let _ = writeln!(s, "import View.Section{j}");
    }
    s.push_str(
        "\n\nupdate : Msg -> Model -> ( Model, Cmd Msg )\nupdate msg model =\n    case msg of\n\
         \x20       SubmitSignIn form ->\n\
         \x20           ( { model | session = Persist.signIn form.email form.password }, Cmd.none )\n\n\
         \x20       SignOut ->\n\
         \x20           ( { model | session = Nothing }, Cmd.none )\n",
    );
    for a in &plan.arms {
        let (c, f, i) = (a.ctor(), a.field, a.index);
        let arm = match a.kind {
            ArmKind::Load => format!(
                "        {c} ->\n            ( {{ model | f{f} = Persist.loadItems () }}, Cmd.none )\n"
            ),
            ArmKind::Edit => format!(
                "        {c} s ->\n            ( {{ model | f{f} = s }}, Cmd.none )\n"
            ),
            ArmKind::Save => format!(
                "        {c} ->\n            let\n                newId =\n                    Persist.createItem (stamp{i} model)\n            in\n            ( {{ model | f{f} = Persist.loadItems (), status = \"saved \" ++ String.fromInt newId }}, Cmd.none )\n"
            ),
            ArmKind::Post => format!(
                "        {c} s ->\n            let\n                newId =\n                    Persist.createItem (stamp{i} model ++ s)\n            in\n            ( {{ model | f{f} = Persist.loadItems (), status = \"posted \" ++ String.fromInt newId }}, Cmd.none )\n"
            ),
            ArmKind::Add => format!(
                "        {c} s ->\n            let\n                newId =\n                    Persist.createItem s\n            in\n            ( {{ model | f{f} = Persist.loadItems (), status = \"added \" ++ String.fromInt newId }}, Cmd.none )\n"
            ),
            ArmKind::Inc => format!(
                "        {c} n ->\n            ( {{ model | f{f} = model.f{f} + n }}, Cmd.none )\n"
            ),
            ArmKind::Refresh => format!(
                "        {c} ->\n            ( {{ model | status = \"counting\" }}, Cmd.perform (Persist.countItems ()) Counted{i} )\n\n\
                 \x20       Counted{i} result ->\n            case result of\n                Ok n ->\n                    ( {{ model | f{f} = n, status = \"counted\" }}, Cmd.none )\n\n\
                 \x20               Err _ ->\n                    ( {{ model | status = \"count failed\" }}, Cmd.none )\n"
            ),
            ArmKind::Toggle => format!(
                "        {c} ->\n            ( {{ model | f{f} = not model.f{f} }}, Cmd.none )\n"
            ),
            ArmKind::Tag => format!(
                "        {c} s ->\n            ( {{ model | f{f} = Dict.insert s (Dict.size model.f{f} + 1) model.f{f} }}, Cmd.none )\n"
            ),
            ArmKind::Rename => format!(
                "        {c} s ->\n            ( {{ model | f{f} = renameProfile s model.f{f} }}, Cmd.none )\n"
            ),
            ArmKind::Pick => format!(
                "        {c} s ->\n            ( {{ model | f{f} = Just s }}, Cmd.none )\n"
            ),
        };
        s.push('\n');
        s.push_str(&arm);
    }
    s.push_str(
        "\n\nsignInView : Model -> Element Msg\nsignInView model =\n    case model.session of\n\
         \x20       Just s ->\n\
         \x20           Ui.row [ Ui.spacing 8 ] [ C.label s.email, C.actionButton \"sign out\" SignOut ]\n\n\
         \x20       Nothing ->\n\
         \x20           Ui.form\n\
         \x20               [ Ui.onSubmit SubmitSignIn ]\n\
         \x20               [ C.field \"Email\" \"email\" \"email\"\n\
         \x20               , C.field \"Password\" \"password\" \"password\"\n\
         \x20               , Ui.input [ Ui.htmlAttribute \"type\" \"submit\", Ui.htmlAttribute \"value\" \"Sign in\" ]\n\
         \x20               ]\n\n\n\
         view : Model -> Element Msg\nview model =\n    Ui.column\n        [ Ui.spacing 16, Ui.padding 16 ]\n\
         \x20       [ signInView model\n\
         \x20       , C.label model.status\n",
    );
    for j in 1..=plan.params.sections() {
        let _ = writeln!(s, "        , View.Section{j}.section{j} model");
    }
    s.push_str(
        "        ]\n\n\n\
         subscriptions : Model -> Sub Msg\nsubscriptions _ =\n    Sub.none\n\n\n\
         app =\n    App.app\n        { init = init\n        , update = update\n        , view = view\n        , subscriptions = subscriptions\n        }\n\
         \x20       |> App.withRoutes [ App.route \"/\" Home, App.route \"/settings\" Settings ]\n\
         \x20       |> App.withNotFound Home\n\n\n\
         main : Task Error ()\nmain =\n    App.run app\n",
    );
    s
}
