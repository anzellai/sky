-- Neovim-headless LSP CORPUS driver — the shapes real Sky code actually has.
--
-- `lsp-test-nvim.lua` (the original 17) asserts everything against ONE synthetic
-- single-module fixture: a record alias, one ADT, two functions, a `let`, a
-- lambda. That fixture cannot express the shapes where the bugs of this cycle
-- actually lived — cross-module resolution (#164), the four import forms, a
-- diagnostic's code+range as an editor renders it, or a real Sky.Live app.
--
-- This driver adds those. It differs from the original in one deliberate way:
-- a whole GROUP of cases runs against ONE LSP session, printing one
-- `PASS:`/`FAIL:` line per case. The original pays ~10 s of nvim + server
-- startup PER case, which is affordable for 17 and not for 47. Every case here
-- is read-only with respect to the positions the other cases use (the three
-- completion cases append lines at the END of the buffer, after every
-- position-based case in their group has already run), so sharing a session
-- cannot make one case mask another. A shared session is also the more faithful
-- editor simulation: a real editor answers many requests against one long-lived
-- server, which is exactly the state `sky-lsp`'s salsa db lives in.
--
-- Usage:
--   nvim --headless -u NONE -l scripts/lsp-corpus-nvim.lua <work-dir> <group> <repo-root>
--
-- Groups:
--   multimodule  — cross-module goto-def/hover/completion through every import
--                  shape, incl. the alias-is-not-the-last-segment form that
--                  #164 regressed on, and goto-def into `sky-stdlib`.
--   diagnostics  — [E1012] (value / constructor / type), [E2008], [E2007]
--                  reaching the editor with code AND range, plus the
--                  broken-file usability cases (an editor must stay useful when
--                  the code is wrong — that is when people need it most).
--   realapp      — the LSP driven over a REAL example project under examples/,
--                  a multi-module Sky.Live Model/Msg/update/view app.
--
-- Output: one "PASS: <case>" / "FAIL: <case>: <reason>" line per case.
-- Exit 0 if every case passed, 1 otherwise.

local args = arg or {}
if #args < 3 then
    io.stderr:write(
        "usage: nvim --headless -u NONE -l scripts/lsp-corpus-nvim.lua "
            .. "<work-dir> <group> <repo-root>\n"
    )
    os.exit(2)
end

local work_dir = args[1]
local group_name = args[2]
local repo_root = args[3]

-- ─── fixture materialisation ─────────────────────────────────────────────
--
-- Every fixture is rewritten from these literals on every run. A fixture left
-- over from an older revision is the single most effective way to make a gate
-- lie (this repo lost a day to a 10-day-old one), so nothing is reused.

local function write_file(path, body)
    local dir = path:match("^(.*)/[^/]+$")
    if dir then
        vim.fn.mkdir(dir, "p")
    end
    local f = io.open(path, "w")
    if not f then
        io.stderr:write("ERROR: cannot write " .. path .. "\n")
        os.exit(2)
    end
    f:write(body)
    f:close()
end

local function write_project(root, name, files)
    vim.fn.delete(root, "rf")
    write_file(
        root .. "/sky.toml",
        ("name = %q\nversion = \"0.0.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n"):format(
            name
        )
    )
    for rel, body in pairs(files) do
        write_file(root .. "/" .. rel, body)
    end
end

-- ─── LSP session ─────────────────────────────────────────────────────────

local function find_sky_binary()
    -- NB: deliberately NOT `<cwd>/sky-out/sky`. That path is the legacy Haskell
    -- oracle's output location; resolving it here would silently certify a
    -- different binary than the one under test.
    -- Built element-by-element, NOT as `{ vim.env.SKY_BIN, "sky" }`: an unset
    -- SKY_BIN puts a nil in slot 1 and `ipairs` stops dead there, which silently
    -- hides the `$PATH` fallback the gate actually relies on.
    local candidates = {}
    if vim.env.SKY_BIN and vim.env.SKY_BIN ~= "" then
        candidates[#candidates + 1] = vim.env.SKY_BIN
    end
    candidates[#candidates + 1] = "sky"
    for _, c in ipairs(candidates) do
        if vim.fn.executable(c) == 1 then
            return c
        end
    end
    return nil
end

local session_client = nil

local function start_session(project_dir)
    local sky = find_sky_binary()
    if not sky then
        io.stderr:write("ERROR: cannot find a `sky` binary (SKY_BIN or $PATH)\n")
        os.exit(2)
    end
    session_client = vim.lsp.start({
        name = "sky-lsp",
        cmd = { sky, "lsp" },
        root_dir = project_dir,
        filetypes = { "sky" },
    })
    if not session_client then
        io.stderr:write("ERROR: vim.lsp.start failed\n")
        os.exit(2)
    end
end

-- The FIRST open in a session pays for the server's initialise + stdlib/project
-- index build. Later opens in the same session only pay for the `didOpen`
-- round-trip, because the index is already warm — so they settle briefly and
-- let the per-assertion waits (`diagnostics`, `request`) do the real waiting.
-- Every assertion that expects NOTHING still waits the full diagnostics ceiling
-- (see `diagnostics`), so this cannot turn a slow publish into a false green.
local session_opened_any = false

local function open(file_path)
    vim.cmd("edit " .. vim.fn.fnameescape(file_path))
    local bufnr = vim.api.nvim_get_current_buf()
    vim.lsp.buf_attach_client(bufnr, session_client)
    vim.wait(15000, function()
        return #vim.lsp.get_clients({ bufnr = bufnr }) > 0
    end, 100)
    vim.wait(session_opened_any and 500 or 6000)
    session_opened_any = true
    return bufnr
end

-- ─── request helpers ─────────────────────────────────────────────────────

local function request(bufnr, method, line, ch)
    local result, done = nil, false
    vim.lsp.buf_request(bufnr, method, {
        textDocument = vim.lsp.util.make_text_document_params(bufnr),
        position = { line = line, character = ch },
    }, function(_, res, _, _)
        result = res
        done = true
    end)
    vim.wait(8000, function()
        return done
    end, 25)
    return result, done
end

local function hover_text(bufnr, line, ch)
    local r = request(bufnr, "textDocument/hover", line, ch)
    if not r or not r.contents then
        return nil
    end
    local body = r.contents.value or r.contents
    if type(body) ~= "string" then
        return nil
    end
    return body
end

--- Resolved definition as `{ uri = …, line = …, character = … }` (handles the
--- Location / LocationLink / list-of-either shapes an LSP may return).
local function definition(bufnr, line, ch)
    local r = request(bufnr, "textDocument/definition", line, ch)
    if not r then
        return nil
    end
    local first = r[1] or r
    if not first or (not first.range and not first.targetRange) then
        return nil
    end
    local rng = first.range or first.targetRange
    return {
        uri = first.uri or first.targetUri or "",
        line = rng.start.line,
        character = rng.start.character,
    }
end

local function completion_items(bufnr, line, ch)
    local r = request(bufnr, "textDocument/completion", line, ch)
    if not r then
        return nil
    end
    local items = r.items or r
    if type(items) ~= "table" then
        return nil
    end
    return items
end

local function item_named(items, label)
    for _, it in ipairs(items or {}) do
        if it.label == label then
            return it
        end
    end
    return nil
end

local function labels_of(items, n)
    local out = {}
    for i = 1, math.min(n or 10, #(items or {})) do
        out[#out + 1] = tostring(items[i].label)
    end
    return table.concat(out, ", ")
end

--- 0-based (line, col) of the `nth` (1-based) PLAIN occurrence of `needle`.
--- Positions are searched, never hardcoded, so a case survives an edit that
--- moves the line it targets — the `realapp` group drives a file this suite
--- does not own, and a hardcoded line there would rot into a false red.
local function find_pos(bufnr, needle, nth)
    nth = nth or 1
    local lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
    local seen = 0
    for i, text in ipairs(lines) do
        local from = 1
        while true do
            local s = text:find(needle, from, true)
            if not s then
                break
            end
            seen = seen + 1
            if seen == nth then
                return i - 1, s - 1
            end
            from = s + 1
        end
    end
    return nil, nil
end

--- Wait until the server has published diagnostics for `bufnr`, or `ceiling`
--- ms elapse. Returns the list. `want_any=true` returns as soon as one arrives
--- (fast path for the expect-an-error cases); otherwise it waits the FULL
--- ceiling before concluding "none", so a zero-diagnostics assertion can never
--- pass merely because the publish had not landed yet.
local function diagnostics(bufnr, want_any, ceiling)
    ceiling = ceiling or 8000
    if want_any then
        vim.wait(ceiling, function()
            return #vim.diagnostic.get(bufnr) > 0
        end, 50)
    else
        vim.wait(ceiling)
    end
    return vim.diagnostic.get(bufnr)
end

local function errors_only(ds)
    local out = {}
    for _, d in ipairs(ds or {}) do
        if d.severity == vim.diagnostic.severity.ERROR then
            out[#out + 1] = d
        end
    end
    return out
end

local function with_code(ds, code)
    local out = {}
    for _, d in ipairs(ds or {}) do
        if tostring(d.code) == code then
            out[#out + 1] = d
        end
    end
    return out
end

local function render(ds)
    local parts = {}
    for _, d in ipairs(ds or {}) do
        local msg = ((d.message or ""):gsub("\n", " ")):sub(1, 90)
        parts[#parts + 1] = string.format("%s@%d:%d %s", tostring(d.code), d.lnum, d.col, msg)
    end
    return "[" .. table.concat(parts, " | ") .. "]"
end

-- ─── case runner ─────────────────────────────────────────────────────────

local results = {}

local function case(name, fn)
    local ok, a, b = pcall(fn)
    if not ok then
        results[#results + 1] = { name = name, ok = false, msg = "lua error: " .. tostring(a) }
        return
    end
    results[#results + 1] = { name = name, ok = a and true or false, msg = tostring(b) }
end

--- goto-def assertion that checks WHERE the jump landed — the file as well as
--- the line. The original driver's helper compares the line only, which cannot
--- distinguish a cross-module jump from a same-file one (and, for a target on
--- the cursor's own line, cannot distinguish a jump from no jump at all).
local function expect_def(bufnr, line, ch, want_suffix, want_line)
    local d = definition(bufnr, line, ch)
    if not d then
        return false, "no definition response"
    end
    if not d.uri:find(want_suffix, 1, true) then
        return false, string.format("landed in %q, expected a file ending %q", d.uri, want_suffix)
    end
    if want_line and d.line ~= want_line then
        return false,
            string.format(
                "landed in the right file but on line %d (expected %d)",
                d.line,
                want_line
            )
    end
    return true, string.format("→ %s:%d", want_suffix, d.line)
end

local function expect_hover(bufnr, line, ch, needles)
    local body = hover_text(bufnr, line, ch)
    if not body then
        return false, "no hover content"
    end
    for _, n in ipairs(needles) do
        if not body:find(n, 1, true) then
            return false, string.format("hover %q lacks %q", body, n)
        end
    end
    return true, body:gsub("\n", " ")
end

-- ═════════════════════════════════════════════════════════════════════════
-- GROUP: multimodule
-- ═════════════════════════════════════════════════════════════════════════

local MULTI_FILES = {
    ["src/Domain/User.sky"] = [[module Domain.User exposing (User, Role(..), mkUser, describe, promote)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String


type alias User =
    { name : String, age : Int, role : Role }


type Role
    = Guest
    | Member
    | Admin Int


mkUser : String -> User
mkUser name =
    { name = name, age = 0, role = Guest }


describe : User -> String
describe user =
    user.name ++ ":" ++ String.fromInt user.age


promote : User -> User
promote user =
    { user | role = Admin 1 }
]],
    ["src/Util/Format.sky"] = [[module Util.Format exposing (pad, tag)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String


pad : Int -> String -> String
pad width text =
    String.fromInt width ++ ":" ++ text


tag : String -> String
tag text =
    "[" ++ text ++ "]"
]],
    ["src/Util/Text.sky"] = [[module Util.Text exposing (shout)

import Sky.Core.Prelude exposing (..)


shout : String -> String
shout text =
    text ++ "!"
]],
    -- Every import shape Sky has, in one file:
    --   line 2  `exposing (..)`                 — Prelude
    --   line 3  `as <last segment>`             — the ordinary alias
    --   line 5  `exposing (specific)`           — Std.Log
    --   line 6  `exposing (Type, Ctor(..))`     — a user module's type + ctors
    --   line 7  `as <NOT the last segment>`     — the #164 shape
    --   line 8  bare `import X.Y`               — qualifier is the LAST segment
    ["src/Main.sky"] = [[module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Core.Task as Task
import Std.Log exposing (println)
import Domain.User exposing (User, Role(..))
import Util.Format as Fmt
import Util.Text


seed : User
seed =
    User.mkUser "ada"


renamed : User
renamed =
    { seed | name = "grace" }


roleLabel : Role -> String
roleLabel role =
    case role of
        Guest -> "guest"
        Member -> "member"
        Admin level -> Fmt.pad level "admin"


summary : String
summary =
    seed
        |> User.describe
        |> Text.shout
        |> Fmt.tag


main =
    Task.run (println (summary ++ roleLabel Member ++ String.fromInt (String.length renamed.name)))
]],
}

-- 0-based positions in the Main.sky above. Columns are counted from the
-- literal, and `find_pos` re-derives them at run time where a literal column
-- would be fragile.
local function group_multimodule()
    local root = work_dir .. "/multimodule"
    write_project(root, "lsp-corpus-multimodule", MULTI_FILES)
    start_session(root)
    local b = open(root .. "/src/Main.sky")

    -- ── goto-def ACROSS a module boundary, once per import shape ──────────

    case("xmod-def-through-exposing-import", function()
        -- `User.mkUser` — qualifier from `import Domain.User exposing (...)`.
        local l, c = find_pos(b, "User.mkUser")
        return expect_def(b, l, c + 5, "src/Domain/User.sky", 17)
    end)

    case("xmod-def-through-alias-not-last-segment", function()
        -- THE #164 shape: `import Util.Format as Fmt` — the alias is not the
        -- module's last path segment, which is what the reverted qualifier
        -- heuristic got wrong.
        local l, c = find_pos(b, "Fmt.pad")
        return expect_def(b, l, c + 4, "src/Util/Format.sky", 7)
    end)

    case("xmod-def-through-bare-import", function()
        -- `import Util.Text` with no alias: the qualifier is `Text`, the LAST
        -- segment — NOT the full dotted path.
        local l, c = find_pos(b, "Text.shout")
        return expect_def(b, l, c + 5, "src/Util/Text.sky", 6)
    end)

    case("xmod-def-exposed-constructor", function()
        -- `Member`, imported bare via `exposing (Role(..))`, jumps to the
        -- variant's own line in the OTHER module.
        local l, c = find_pos(b, "roleLabel Member")
        return expect_def(b, l, c + 10, "src/Domain/User.sky", 12)
    end)

    case("xmod-def-exposed-type", function()
        -- `User` in the annotation `seed : User`, imported via `exposing (User)`.
        local l, c = find_pos(b, "seed : User")
        return expect_def(b, l, c + 7, "src/Domain/User.sky", 6)
    end)

    -- ── goto-def INTO the stdlib ─────────────────────────────────────────

    case("stdlib-def-value", function()
        local l, c = find_pos(b, "String.fromInt (String.length")
        return expect_def(b, l, c + 7, "sky-stdlib/Sky/Core/String.sky", nil)
    end)

    case("stdlib-def-kernel-qualifier", function()
        -- `String.length` — the kernel-qualifier shape the brief called out.
        local l, c = find_pos(b, "String.length")
        return expect_def(b, l, c + 7, "sky-stdlib/Sky/Core/String.sky", nil)
    end)

    -- ── hover through each import shape ──────────────────────────────────

    case("xmod-hover-through-alias-not-last-segment", function()
        local l, c = find_pos(b, "Fmt.pad")
        return expect_hover(b, l, c + 4, { "Int -> String -> String" })
    end)

    case("xmod-hover-through-bare-import", function()
        local l, c = find_pos(b, "Text.shout")
        return expect_hover(b, l, c + 5, { "String -> String" })
    end)

    case("xmod-hover-in-pipeline", function()
        -- A `|>` chain: the ref is an argument-less function value, not a call.
        local l, c = find_pos(b, "|> User.describe")
        return expect_hover(b, l, c + 8, { "User -> String" })
    end)

    case("xmod-hover-record-update-target", function()
        -- `{ seed | name = "grace" }` — the record-update shape.
        local l, c = find_pos(b, "{ seed | name")
        return expect_hover(b, l, c + 2, { "User" })
    end)

    case("xmod-hover-imported-ctor-in-case-pattern", function()
        -- `case` over an ADT that came from ANOTHER module.
        local l, c = find_pos(b, "Guest -> ")
        return expect_hover(b, l, c, { "Guest", "Role" })
    end)

    case("xmod-hover-binding-from-imported-ctor-payload", function()
        -- `Admin level ->` — `level`'s type is only knowable from the imported
        -- variant's payload.
        local l, c = find_pos(b, "Admin level ->")
        return expect_hover(b, l, c + 6, { "level", "Int" })
    end)

    -- ── completion through each qualifier shape ──────────────────────────
    -- These APPEND to the buffer, so they run after every position-based case
    -- above. Appending cannot move the lines those cases used.

    local function completion_case(name, trigger, expect_label, expect_insert)
        case(name, function()
            vim.cmd("silent $put ='" .. trigger .. "'")
            vim.cmd("silent write")
            vim.wait(600)
            local last = vim.api.nvim_buf_line_count(b) - 1
            local text = vim.api.nvim_buf_get_lines(b, last, last + 1, false)[1] or ""
            local items = completion_items(b, last, #text)
            if not items then
                return false, "no completion response"
            end
            local it = item_named(items, expect_label)
            if not it then
                return false,
                    string.format(
                        "%q absent from %d items: %s",
                        expect_label,
                        #items,
                        labels_of(items, 10)
                    )
            end
            -- insertText must be the BARE name: the editor already typed the
            -- qualifier, so echoing it back is the double-prefix bug.
            if it.insertText ~= expect_insert then
                return false,
                    string.format(
                        "%s: insertText=%q (expected %q)",
                        expect_label,
                        tostring(it.insertText),
                        expect_insert
                    )
            end
            return true, string.format("%s → insertText=%q", expect_label, expect_insert)
        end)
    end

    completion_case("xmod-comp-through-alias-not-last-segment", "cA = Fmt.", "Fmt.pad", "pad")
    completion_case("xmod-comp-through-bare-import", "cB = Text.", "Text.shout", "shout")
    completion_case("xmod-comp-through-exposing-import", "cC = User.", "User.describe", "describe")
end

-- ═════════════════════════════════════════════════════════════════════════
-- GROUP: diagnostics
-- ═════════════════════════════════════════════════════════════════════════

local DIAG_FILES = {
    ["src/Main.sky"] = [[module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)


main =
    println "ok"
]],
    ["src/Ambig/Alpha.sky"] = [[module Ambig.Alpha exposing (..)

import Sky.Core.Prelude exposing (..)


label : String
label =
    "ALPHA"


type Shape
    = Circle


type Colour
    = Same
]],
    ["src/Ambig/Beta.sky"] = [[module Ambig.Beta exposing (..)

import Sky.Core.Prelude exposing (..)


label : String
label =
    "BETA"


type Shape
    = Square


type Flavour
    = Same
]],
    ["src/AmbigValue.sky"] = [[module AmbigValue exposing (pick)

import Sky.Core.Prelude exposing (..)
import Ambig.Alpha exposing (..)
import Ambig.Beta exposing (..)


pick : String
pick =
    label
]],
    ["src/AmbigCtor.sky"] = [[module AmbigCtor exposing (pick)

import Sky.Core.Prelude exposing (..)
import Ambig.Alpha exposing (..)
import Ambig.Beta exposing (..)


pick =
    Same
]],
    ["src/AmbigType.sky"] = [[module AmbigType exposing (pick)

import Sky.Core.Prelude exposing (..)
import Ambig.Alpha exposing (..)
import Ambig.Beta exposing (..)


pick : Shape
pick =
    Circle
]],
    ["src/DictKey.sky"] = [[module DictKey exposing (grid)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Dict as Dict exposing (Dict)


grid : Dict ( Int, Int ) String
grid =
    Dict.insert ( 1, 2 ) "wall" Dict.empty
]],
    ["src/Arity.sky"] = [[module Arity exposing (bad)

import Sky.Core.Prelude exposing (..)


twice : Int -> Int
twice n =
    n * 2


bad : Int
bad =
    twice 1 2
]],
    -- A file that is BROKEN but still being edited — the state an editor has to
    -- stay useful in, because that is when the user needs it most.
    ["src/Broken.sky"] = [[module Broken exposing (good)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String


good : String
good =
    "ok"


alsoGood : Int -> Int
alsoGood n =
    n + 1


broken : Int
broken =
    String.fromInt "not an int"
]],
    -- The falsifiability twin for the whole group: valid code, same project,
    -- same session — publishes nothing.
    ["src/Clean.sky"] = [[module Clean exposing (fine)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String


fine : String
fine =
    String.fromInt 1
]],
}

local function group_diagnostics()
    local root = work_dir .. "/diagnostics"
    write_project(root, "lsp-corpus-diagnostics", DIAG_FILES)
    start_session(root)

    -- ── [E1012] in all three namespaces, as the EDITOR sees it ───────────

    local function ambiguity_case(name, file, needle, kind, alts)
        case(name, function()
            local b = open(root .. "/src/" .. file)
            local ds = with_code(diagnostics(b, true), "E1012")
            if #ds == 0 then
                return false, "no [E1012] published; got " .. render(diagnostics(b, false, 500))
            end
            -- Exactly one: the same defect reported twice is two squiggles and
            -- two quickfix rows for one mistake, and `sky check` prints one.
            if #ds ~= 1 then
                return false, string.format("[E1012] published %d times: %s", #ds, render(ds))
            end
            local d = ds[1]
            local l, c = find_pos(b, needle)
            if d.lnum ~= l or d.col ~= c then
                return false,
                    string.format(
                        "range %d:%d, expected the reference at %d:%d",
                        d.lnum,
                        d.col,
                        l,
                        c
                    )
            end
            if not d.message:find(kind, 1, true) then
                return false, string.format("message does not say %q: %s", kind, d.message)
            end
            for _, alt in ipairs(alts) do
                if not d.message:find(alt, 1, true) then
                    return false,
                        string.format(
                            "message must name the alternative %q so the user "
                                .. "knows which import to change: %s",
                            alt,
                            d.message
                        )
                end
            end
            return true, string.format("%s at %d:%d", kind, d.lnum, d.col)
        end)
    end

    ambiguity_case(
        "diag-e1012-value-in-editor",
        "AmbigValue.sky",
        "label",
        "Ambiguous name",
        { "Ambig.Alpha", "Ambig.Beta" }
    )
    ambiguity_case(
        "diag-e1012-constructor-in-editor",
        "AmbigCtor.sky",
        "Same",
        "Ambiguous constructor",
        { "Ambig.Alpha", "Ambig.Beta" }
    )
    ambiguity_case(
        "diag-e1012-type-in-editor",
        "AmbigType.sky",
        "Shape",
        "Ambiguous type",
        { "Ambig.Alpha", "Ambig.Beta" }
    )

    -- ── [E2008] unsupported Dict key ─────────────────────────────────────

    case("diag-e2008-dict-key-in-editor", function()
        local b = open(root .. "/src/DictKey.sky")
        local ds = with_code(diagnostics(b, true), "E2008")
        if #ds ~= 1 then
            return false,
                string.format(
                    "expected exactly one [E2008], got %d: %s",
                    #ds,
                    render(diagnostics(b, false, 500))
                )
        end
        local d = ds[1]
        -- Anchored on the ANNOTATION the user edits, not the 0:0 fallback a
        -- label-less diagnostic lands on.
        local l, c = find_pos(b, "Dict ( Int, Int ) String")
        if d.lnum ~= l or d.col ~= c then
            return false,
                string.format("range %d:%d, expected the annotation at %d:%d", d.lnum, d.col, l, c)
        end
        if not (d.message:find("( Int, Int )", 1, true) and d.message:find("`Int`", 1, true)) then
            return false, "message must name the offending key type AND the supported set: " .. d.message
        end
        return true, string.format("[E2008] at %d:%d", d.lnum, d.col)
    end)

    -- ── [E2007] kernel/user arity ────────────────────────────────────────

    case("diag-e2007-arity-in-editor", function()
        local b = open(root .. "/src/Arity.sky")
        local ds = with_code(diagnostics(b, true), "E2007")
        if #ds ~= 1 then
            return false,
                string.format(
                    "expected exactly one [E2007], got %d: %s",
                    #ds,
                    render(diagnostics(b, false, 500))
                )
        end
        local d = ds[1]
        local l = select(1, find_pos(b, "twice 1 2"))
        if d.lnum ~= l then
            return false, string.format("range on line %d, expected the call on %d", d.lnum, l)
        end
        if not (d.message:find("twice", 1, true) and d.message:find("1-arg", 1, true)) then
            return false, "message must name the callee and its declared arity: " .. d.message
        end
        return true, string.format("[E2007] at %d:%d", d.lnum, d.col)
    end)

    -- ── the twin: valid code in the same project publishes NOTHING ───────

    case("diag-clean-file-publishes-nothing", function()
        local b = open(root .. "/src/Clean.sky")
        local errs = errors_only(diagnostics(b, false))
        if #errs > 0 then
            return false, "valid module published errors: " .. render(errs)
        end
        return true, "0 errors"
    end)

    -- ── an editor must stay USEFUL while the file is broken ──────────────

    case("hover-still-works-in-a-file-with-a-type-error", function()
        local b = open(root .. "/src/Broken.sky")
        -- Prove the file really is broken first — otherwise this case would
        -- pass against a build that silently reports nothing.
        local errs = errors_only(diagnostics(b, true))
        if #errs == 0 then
            return false, "fixture is not actually broken — no error published"
        end
        local l, c = find_pos(b, "alsoGood : Int -> Int")
        return expect_hover(b, l, c, { "Int -> Int" })
    end)

    case("completion-still-works-in-a-file-with-a-type-error", function()
        local b = open(root .. "/src/Broken.sky")
        local errs = errors_only(diagnostics(b, true))
        if #errs == 0 then
            return false, "fixture is not actually broken — no error published"
        end
        vim.cmd("silent $put ='probe = String.'")
        vim.cmd("silent write")
        vim.wait(600)
        local last = vim.api.nvim_buf_line_count(b) - 1
        local text = vim.api.nvim_buf_get_lines(b, last, last + 1, false)[1] or ""
        local items = completion_items(b, last, #text)
        if not items or #items == 0 then
            return false, "a type error must not empty the completion list"
        end
        if not item_named(items, "String.fromInt") then
            return false,
                string.format(
                    "String.fromInt absent from %d items: %s",
                    #items,
                    labels_of(items, 10)
                )
        end
        return true, string.format("%d items offered despite the type error", #items)
    end)

    case("hover-on-an-unresolvable-name-degrades-quietly", function()
        local b = open(root .. "/src/Broken.sky")
        vim.cmd("silent $put ='mystery = noSuchNameAnywhere'")
        vim.cmd("silent write")
        vim.wait(600)
        local l, c = find_pos(b, "noSuchNameAnywhere")
        local body = hover_text(b, l, c)
        -- Either no content or a content that does NOT invent a type. What must
        -- never happen is a crash, a hang, or a confidently wrong signature.
        if body and body:find("noSuchNameAnywhere :", 1, true) and not body:find("?", 1, true) then
            return false, "hover invented a signature for an undefined name: " .. body
        end
        -- The session must still be alive: a later request has to succeed, or a
        -- server that died on the unresolvable name would "pass" the above.
        local gl, gc = find_pos(b, "alsoGood : Int -> Int")
        local ok = expect_hover(b, gl, gc, { "Int -> Int" })
        if not ok then
            return false, "the server stopped answering after the unresolvable hover"
        end
        return true, "no invented signature; session still answering"
    end)
end

-- ═════════════════════════════════════════════════════════════════════════
-- GROUP: realapp — the LSP over a REAL example, not a fixture
-- ═════════════════════════════════════════════════════════════════════════

local function group_realapp()
    -- `19-skyforum` is the canonical multi-module Sky.Live app: State / Update /
    -- View.* with a Model/Msg/update/view TEA loop. Driven IN PLACE and
    -- read-only — the point is that this is code the project ships, not a
    -- fixture written to make the LSP look good.
    local root = repo_root .. "/examples/19-skyforum"
    start_session(root)

    local main = open(root .. "/src/Main.sky")

    case("realapp-def-into-sibling-update-module", function()
        local l, c = find_pos(main, "update = update")
        return expect_def(main, l, c + 9, "src/Update.sky", nil)
    end)

    case("realapp-def-into-sibling-view-module", function()
        local l, c = find_pos(main, "postsListView model")
        return expect_def(main, l, c, "src/View/Posts.sky", nil)
    end)

    case("realapp-hover-imported-view-function", function()
        -- The full real signature is `postsListView : Maybe Session -> List
        -- Post -> Element Msg`. Asserting only "non-empty" would pass against a
        -- hover that returned any string at all, so this pins the SUBSTANCE:
        -- the symbol name, an arrow, and the `Element` return type — none of
        -- which the LSP can produce without resolving types that live in three
        -- other modules (`State`, `Std.Ui`) of a real Sky.Live app.
        local l, c = find_pos(main, "postsListView model")
        local ok, body = expect_hover(main, l, c, { "postsListView :", "->", "Element" })
        if not ok then
            return false, body
        end
        if body:find("?", 1, true) then
            return false, "hover left a type unresolved: " .. body
        end
        return true, body
    end)

    -- Ordered LAST for Main.sky: the two goto-defs above already proved the
    -- project index is fully built, so a zero here cannot be "the publish had
    -- not landed yet".
    case("realapp-main-publishes-no-false-diagnostics", function()
        local errs = errors_only(diagnostics(main, false))
        if #errs > 0 then
            return false, "the editor red-squiggles a shipping example: " .. render(errs)
        end
        return true, "0 errors on a real Sky.Live Main"
    end)

    local upd = open(root .. "/src/Update.sky")

    case("realapp-hover-model-type-through-exposing-all", function()
        -- `Model` in `update : Msg -> Model -> ( Model, Cmd Msg )` reaches
        -- `State` through `import State exposing (..)`.
        local l, c = find_pos(upd, "Msg -> Model ->")
        return expect_hover(upd, l, c + 7, { "Model" })
    end)

    case("realapp-def-type-through-exposing-all", function()
        local l, c = find_pos(upd, "Msg -> Model ->")
        return expect_def(upd, l, c + 7, "src/State.sky", nil)
    end)

    case("realapp-update-module-publishes-no-false-diagnostics", function()
        local errs = errors_only(diagnostics(upd, false))
        if #errs > 0 then
            return false, "the editor red-squiggles a shipping example: " .. render(errs)
        end
        return true, "0 errors on a real TEA update module"
    end)
end

-- ═════════════════════════════════════════════════════════════════════════

local groups = {
    multimodule = group_multimodule,
    diagnostics = group_diagnostics,
    realapp = group_realapp,
}

local g = groups[group_name]
if not g then
    io.stderr:write("Unknown group: " .. group_name .. "\n")
    os.exit(2)
end

g()

-- Stop every spawned client BEFORE exit so no `sky lsp` child is reparented to
-- launchd (process-table exhaustion class — CLAUDE.md background-task hygiene).
for _, client in ipairs(vim.lsp.get_clients and vim.lsp.get_clients() or {}) do
    pcall(vim.lsp.stop_client, client.id, true)
end
vim.wait(200)

-- Leading newline: nvim writes its own messages (file-write notices, warnings)
-- to the same stream WITHOUT a trailing newline, which would otherwise glue
-- itself to the first result line and hide it from the driver's line-oriented
-- scan — a case that reports nothing is a case that cannot fail.
io.stdout:write("\n")

local failed = 0
for _, r in ipairs(results) do
    if r.ok then
        io.stdout:write("PASS: " .. r.name .. "\n")
    else
        failed = failed + 1
        io.stdout:write("FAIL: " .. r.name .. ": " .. r.msg .. "\n")
    end
end

if #results == 0 then
    io.stdout:write("FAIL: " .. group_name .. ": the group ran ZERO cases\n")
    os.exit(1)
end

-- The driver states how many cases it ran; the shell cross-checks that against
-- how many result lines it actually parsed. Without this a swallowed line is
-- indistinguishable from a case that was never written.
io.stdout:write(string.format("CASES: %s %d\n", group_name, #results))

os.exit(failed == 0 and 0 or 1)
