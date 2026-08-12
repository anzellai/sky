-- Neovim-headless LSP test driver.
--
-- Runs Sky's LSP under a real Neovim instance and exercises hover /
-- completion / definition / references end-to-end. Unlike the synthetic
-- JSON-RPC harness in test/Sky/Lsp/, this driver catches editor-behaviour
-- bugs:
--   - insertText vs label handling (the "double-prefix on accept" bug)
--   - hover popup markdown rendering
--   - go-to-def jumping to the wrong cursor position
--   - completion items not sorted / filtered as expected
--
-- Usage:
--   nvim --headless -u NONE -l scripts/lsp-test-nvim.lua <project-dir> <test-name>
--
-- Test names:
--   hover-task-run        — hover on Task.run, expect type signature
--   hover-field           — hover on model.count, expect Int
--   hover-type-name       — hover on Model, expect alias body
--   completion-qualified  — Std.<Tab>, expect insertText is local name
--   completion-field      — model.<Tab>, expect insertText is field name
--   completion-let        — let abc = 1 in ab<Tab>, expect abc
--   goto-def-type         — jump to Model alias decl
--
-- Output: prints "PASS: <test>" or "FAIL: <test>: <reason>" to stdout.
-- Exit code 0 if all pass, 1 if any fail.
--
-- This driver is currently a SCAFFOLD. The Lua API for headless LSP
-- testing requires careful sync with vim.wait() because LSP is async;
-- the actual test bodies are placeholders awaiting the synchronous
-- request shape we're building up next session.

-- Neovim's `-l <script.lua>` mode passes args via the global `arg`
-- table (Lua convention). vim.fn.argv() returns the editor's argv
-- which in -l mode is the script path itself, NOT the trailing args.
local args = arg or {}
if #args < 2 then
    io.stderr:write("usage: nvim --headless -u NONE -l scripts/lsp-test-nvim.lua <project-dir> <test-name>\n")
    io.stderr:write("got args: " .. vim.inspect(args) .. "\n")
    os.exit(1)
end

local project_dir = args[1]
local test_name = args[2]


-- ─── Fixture reset ───────────────────────────────────────────────────
-- Each test starts from a known fixture so a previous test's edits
-- (e.g. "x = Ui." appended for completion) don't poison the next run.

local FIXTURE = [[module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Std.Log exposing (println)
import Std.Ui as Ui

type alias Model = { count : Int, label : String }

stringify : Model -> String
stringify model =
    String.fromInt model.count

letDemo : Int
letDemo =
    let abcLocal = 1
    in abcLocal

type Msg = Increment | Decrement | SetCount Int

applyMsg : Msg -> Int -> Int
applyMsg msg current =
    case msg of
        Increment -> current + 1
        Decrement -> current - 1
        SetCount n -> n

doubleIt : Int -> Int
doubleIt = \x -> x * 2

main =
    Task.run (Task.succeed (applyMsg Increment 41))
]]

local function reset_fixture()
    local main_path = project_dir .. "/src/Main.sky"
    local f = io.open(main_path, "w")
    if not f then
        io.stderr:write("ERROR: cannot write fixture at " .. main_path .. "\n")
        os.exit(2)
    end
    f:write(FIXTURE)
    f:close()
end


-- ─── LSP setup ───────────────────────────────────────────────────────

local function find_sky_binary()
    local candidates = {
        vim.fn.getcwd() .. "/sky-out/sky",
        vim.fn.expand("~/.cabal/bin/sky"),
        "sky",
    }
    for _, c in ipairs(candidates) do
        if vim.fn.executable(c) == 1 then
            return c
        end
    end
    return nil
end


local function start_lsp(file_path)
    local sky = find_sky_binary()
    if not sky then
        io.stderr:write("ERROR: cannot find `sky` binary on $PATH\n")
        os.exit(2)
    end

    local client_id = vim.lsp.start({
        name = "sky-lsp",
        cmd = { sky, "lsp" },
        root_dir = project_dir,
        filetypes = { "sky" },
    })

    if not client_id then
        io.stderr:write("ERROR: vim.lsp.start failed\n")
        os.exit(2)
    end

    -- Open the test file
    vim.cmd("edit " .. file_path)

    -- Attach client to buffer
    local bufnr = vim.api.nvim_get_current_buf()
    vim.lsp.buf_attach_client(bufnr, client_id)

    -- Wait for the server to initialise (didOpen + index build).
    vim.wait(15000, function()
        return #vim.lsp.get_clients({ bufnr = bufnr }) > 0
    end, 100)

    -- Extra wait for index to populate.
    vim.wait(8000)

    return bufnr, client_id
end


-- ─── Test helpers ────────────────────────────────────────────────────

local function test_hover(bufnr, line, col, expected_substr)
    local result = nil
    vim.lsp.buf_request(bufnr,
        "textDocument/hover",
        {
            textDocument = vim.lsp.util.make_text_document_params(bufnr),
            position     = { line = line, character = col },
        },
        function(_, res, _, _)
            result = res
        end)

    vim.wait(5000, function() return result ~= nil end, 50)

    if not result or not result.contents then
        return false, "no hover content"
    end

    local body = result.contents.value or result.contents
    if type(body) ~= "string" then
        return false, "hover body not a string"
    end

    if not body:find(expected_substr, 1, true) then
        return false, string.format("hover body %q lacks %q", body, expected_substr)
    end

    return true, body
end


-- v0.13 G follow-up: shared helper for goto-def assertions. Accepts
-- an `expected_line` (0-based) the cursor should land on; matches
-- either `result.range.start.line` or `result.targetRange.start.line`
-- (LSP allows both shapes). Returns false with a debug message if
-- the resolved line differs or the response is empty.
--
-- `expected_col` (optional) additionally pins the COLUMN. It exists because a
-- line-only assertion is vacuous whenever the definition lives on the SAME line
-- as the reference: `goto-def-lambda-param` asks at 29:17 and expects line 29,
-- so it passed even against a stub that echoed the cursor's own position back
-- (verified by mutation — it was the only one of the six goto-def cases that
-- survived replacing the resolved line with the requested one). Callers whose
-- target is on a different line than the cursor are already falsifiable by the
-- line alone and pass no column, keeping their assertions exactly as they were.
local function test_goto_def(bufnr, line, col, expected_line, expected_col)
    local result = nil
    vim.lsp.buf_request(bufnr, "textDocument/definition",
        {
            textDocument = vim.lsp.util.make_text_document_params(bufnr),
            position     = { line = line, character = col },
        },
        function(_, res, _, _) result = res end)
    vim.wait(5000, function() return result ~= nil end, 50)
    if not result then return false, "no definition response" end
    local first = result[1] or result
    if not first then return false, "empty definition response" end
    local target_line, target_col = nil, nil
    if first.range and first.range.start then
        target_line = first.range.start.line
        target_col  = first.range.start.character
    elseif first.targetRange and first.targetRange.start then
        target_line = first.targetRange.start.line
        target_col  = first.targetRange.start.character
    end
    if target_line ~= expected_line then
        return false, string.format("definition went to line %s (expected %d)",
            tostring(target_line), expected_line)
    end
    if expected_col ~= nil and target_col ~= expected_col then
        return false, string.format(
            "definition landed on line %d col %s (expected col %d — the binder, "
            .. "not the reference the cursor was already on)",
            target_line, tostring(target_col), expected_col)
    end
    return true, string.format("jumped to line %d", target_line)
end


local function test_completion(bufnr, line, col, expected_label)
    local result = nil
    vim.lsp.buf_request(bufnr,
        "textDocument/completion",
        {
            textDocument = vim.lsp.util.make_text_document_params(bufnr),
            position     = { line = line, character = col },
        },
        function(_, res, _, _)
            result = res
        end)

    vim.wait(5000, function() return result ~= nil end, 50)

    if not result then
        return false, "no completion response"
    end

    local items = result.items or result
    if type(items) ~= "table" then
        return false, "completion result not a list"
    end

    for _, item in ipairs(items) do
        if item.label == expected_label then
            return true, vim.inspect(item)
        end
    end

    -- Show top-5 labels for debugging
    local labels = {}
    for i = 1, math.min(5, #items) do
        labels[#labels+1] = items[i].label or "?"
    end
    return false, string.format("expected %q in %d items; first 5: %s",
        expected_label, #items, table.concat(labels, ", "))
end


-- ─── Test runner ─────────────────────────────────────────────────────

local tests = {
    -- Hover on `run` in `Task.run` (line 32 0-based, col 9). Expect Task type.
    -- (Fixture extension shifted the `main` line from 20 → 32.)
    ["hover-task-run"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 32, 9, "Task")
    end,

    -- Hover on `count` in `model.count` (line 12 0-based, col 25).
    ["hover-field"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 12, 25, "Int")
    end,

    -- Hover on `Model` in `stringify : Model -> String` (line 10 0-based, col 13).
    ["hover-type-name"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 10, 13, "Model")
    end,

    -- Completion at `Ui.|` — verify `Ui.layout` appears AND insertText
    -- is just "layout" (no Ui. prefix), so accept doesn't double-up.
    ["completion-qualified-insert-text"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        -- Append `x = Ui.` line
        vim.cmd("$put ='x = Ui.'")
        vim.cmd("write")
        vim.wait(500)

        -- Re-read buffer to get correct cursor pos
        local last = vim.api.nvim_buf_line_count(bufnr) - 1
        local line_text = vim.api.nvim_buf_get_lines(bufnr, last, last+1, false)[1]
        local col = #line_text  -- right after the dot

        local result = nil
        vim.lsp.buf_request(bufnr, "textDocument/completion",
            {
                textDocument = vim.lsp.util.make_text_document_params(bufnr),
                position     = { line = last, character = col },
            },
            function(_, res, _, _) result = res end)
        vim.wait(5000, function() return result ~= nil end, 50)

        if not result then return false, "no completion response" end
        local items = result.items or result
        if type(items) ~= "table" then return false, "result not a list" end

        -- Find the `Ui.layout` item, check insertText
        for _, item in ipairs(items) do
            if item.label == "Ui.layout" then
                if item.insertText == "layout" then
                    return true, "Ui.layout has insertText=\"layout\" (no double-prefix)"
                else
                    return false, string.format(
                        "Ui.layout: insertText=%q (expected \"layout\")",
                        tostring(item.insertText))
                end
            end
        end
        return false, "Ui.layout not in completion items"
    end,

    -- Completion at `model.|` — expect a record-field completion offering
    -- `count` / `label` with insertText that's just the bare field name.
    ["completion-field"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        -- Append `y = model.` at end (model is in scope inside stringify
        -- only, but the LSP's record-field completion route just needs
        -- a valid "<expr>." chain — we'll wrap the test so it stays well
        -- formed by extending the stringify body via an injected helper).
        vim.cmd("$put =''")
        vim.cmd("$put ='describe : Model -> String'")
        vim.cmd("$put ='describe m ='")
        vim.cmd("$put ='    String.fromInt m.'")
        vim.cmd("write")
        vim.wait(500)

        local last = vim.api.nvim_buf_line_count(bufnr) - 1
        local line_text = vim.api.nvim_buf_get_lines(bufnr, last, last+1, false)[1]
        local col = #line_text  -- right after "m."

        local result = nil
        vim.lsp.buf_request(bufnr, "textDocument/completion",
            {
                textDocument = vim.lsp.util.make_text_document_params(bufnr),
                position     = { line = last, character = col },
            },
            function(_, res, _, _) result = res end)
        vim.wait(5000, function() return result ~= nil end, 50)

        if not result then return false, "no completion response" end
        local items = result.items or result
        if type(items) ~= "table" then return false, "result not a list" end

        local found_count = false
        local found_label = false
        for _, item in ipairs(items) do
            if item.label == "count" then found_count = true end
            if item.label == "label" then found_label = true end
        end
        if not found_count then
            local labels = {}
            for i = 1, math.min(8, #items) do
                labels[#labels+1] = items[i].label or "?"
            end
            return false, string.format("count not in field completion (%d items): %s",
                #items, table.concat(labels, ", "))
        end
        if not found_label then return false, "label field missing" end
        return true, "count + label both offered"
    end,

    -- Completion at the `abcLocal` use-site (line 17 in the fixture,
    -- after the `ab` chars on `in abcLocal`). Expect `abcLocal` to be
    -- offered as a local binding from idxLocals.
    ["completion-let-binding"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        -- Line 17 (0-based) is `    in abcLocal`
        --                       0123456789012345
        -- After `ab` → column 9 (in 0-based: after `    in ab` = 9)
        local result = nil
        vim.lsp.buf_request(bufnr, "textDocument/completion",
            {
                textDocument = vim.lsp.util.make_text_document_params(bufnr),
                position     = { line = 17, character = 9 },
            },
            function(_, res, _, _) result = res end)
        vim.wait(5000, function() return result ~= nil end, 50)

        if not result then return false, "no completion response" end
        local items = result.items or result
        if type(items) ~= "table" then return false, "result not a list" end

        for _, item in ipairs(items) do
            if item.label == "abcLocal" then
                return true, "let-binding abcLocal offered"
            end
        end
        local labels = {}
        for i = 1, math.min(12, #items) do
            labels[#labels+1] = items[i].label or "?"
        end
        return false, string.format("abcLocal not offered (%d items): %s",
            #items, table.concat(labels, ", "))
    end,

    -- Goto-definition on `Model` in a type annotation (line 10 0-based,
    -- col 13). Expect to land on the `type alias Model` line (line 8 0-based).
    ["goto-def-type-name"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        local result = nil
        vim.lsp.buf_request(bufnr, "textDocument/definition",
            {
                textDocument = vim.lsp.util.make_text_document_params(bufnr),
                position     = { line = 10, character = 13 },
            },
            function(_, res, _, _) result = res end)
        vim.wait(5000, function() return result ~= nil end, 50)

        if not result then return false, "no definition response" end
        local locs = result
        if type(locs) ~= "table" then return false, "result not a list" end
        if #locs == 0 and not locs.uri then return false, "empty definition list" end

        -- Result may be a Location, LocationLink, or list thereof
        local first = locs[1] or locs
        local target_line = nil
        if first.range and first.range.start then
            target_line = first.range.start.line
        elseif first.targetRange and first.targetRange.start then
            target_line = first.targetRange.start.line
        end
        if target_line ~= 8 then
            return false, string.format("definition went to line %s (expected 8)",
                tostring(target_line))
        end
        return true, "Model jumps to alias decl on line 8"
    end,

    -- v0.13 G — every USED symbol class gets hover + (where relevant) goto-def.

    -- Hover on `applyMsg` at its USE SITE in main (line 32 col 30).
    -- `    Task.run (Task.succeed (applyMsg Increment 41))`
    --                              ^ col 28-35 ── applyMsg ──
    -- Expect the function's annotation to surface (`Msg -> Int -> Int`).
    ["hover-function-use"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 32, 30, "Int")
    end,

    -- Goto-def on `applyMsg` at the use site (line 32 col 30). Expect
    -- to land on the def (line 22 — `applyMsg msg current =`) or its
    -- annotation (line 21). LSP servers typically prefer the def line;
    -- accept either.
    ["goto-def-function"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        local result = nil
        vim.lsp.buf_request(bufnr, "textDocument/definition",
            {
                textDocument = vim.lsp.util.make_text_document_params(bufnr),
                position     = { line = 32, character = 30 },
            },
            function(_, res, _, _) result = res end)
        vim.wait(5000, function() return result ~= nil end, 50)
        if not result then return false, "no definition response" end
        local first = result[1] or result
        if not first then return false, "empty definition response" end
        local target_line = nil
        if first.range and first.range.start then
            target_line = first.range.start.line
        elseif first.targetRange and first.targetRange.start then
            target_line = first.targetRange.start.line
        end
        -- Accept either the annotation (21) or the def (22).
        if target_line ~= 21 and target_line ~= 22 then
            return false, string.format("definition went to line %s (expected 21 or 22)",
                tostring(target_line))
        end
        return true, "applyMsg jumps to decl on line " .. tostring(target_line)
    end,

    -- Hover on `Increment` (ADT constructor use) at line 32 col 37.
    -- Expect the ADT type to appear in hover.
    ["hover-ctor-use"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 32, 37, "Msg")
    end,

    -- Hover on lambda parameter `x` at line 29 col 12 in `doubleIt = \x -> x * 2`.
    -- Expect Int (since the annotation says Int -> Int).
    ["hover-lambda-param"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 29, 12, "Int")
    end,

    -- Hover on `n` (case-pattern binding) at line 26 col 17 in
    -- `SetCount n -> n`. Expect Int (SetCount's single param type).
    ["hover-case-pattern"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 26, 17, "Int")
    end,

    -- Hover on `fromInt` (kernel call) at line 12 col 14 in
    -- `String.fromInt model.count`. Expect Int → String shape.
    ["hover-kernel-call"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 12, 14, "Int")
    end,

    -- v0.13 G follow-up — goto-def for the remaining USED symbol
    -- classes the LSP-100% contract called for.

    -- Goto-def on `Increment` (ADT ctor) at its USE SITE
    -- (line 32 col 37). Expect to land on the `type Msg = ...`
    -- declaration line (line 19).
    ["goto-def-ctor"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_goto_def(bufnr, 32, 37, 19)
    end,

    -- Goto-def on `abcLocal` at its USE site (line 17 col 7 in
    -- `    in abcLocal`). Expect to land on the let-binding
    -- decl line (line 16 — `    let abcLocal = 1`).
    ["goto-def-let-binding"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_goto_def(bufnr, 17, 8, 16)
    end,

    -- Goto-def on `x` (lambda param) at its USE site in
    -- `\x -> x * 2` (line 29 col 17 — the right-hand `x`).
    -- Expect to land on the binder (line 29 col 12). The COLUMN is
    -- load-bearing: binder and reference share a line, so line 29 alone
    -- is also true of "no jump happened at all".
    ["goto-def-lambda-param"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_goto_def(bufnr, 29, 17, 29, 12)
    end,

    -- Goto-def on a record-field access. `model.count` (line 12
    -- col 25). Expect to land on the alias decl (line 8 — the
    -- `type alias Model = { count : Int, ... }` line where `count`
    -- is declared).
    ["goto-def-field"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_goto_def(bufnr, 12, 25, 8)
    end,
}


local fn = tests[test_name]
if not fn then
    io.stderr:write("Unknown test: " .. test_name .. "\n")
    os.exit(1)
end

-- Reset fixture before EVERY test so prior writes don't leak in.
reset_fixture()

local ok, msg = fn()

-- Cleanup: stop every spawned LSP client BEFORE os.exit so the
-- child `sky lsp` subprocesses don't get reparented to launchd
-- as orphans. Without this each invocation leaks one sky lsp
-- proc (PPID=1) that survives nvim's exit — across the test
-- suite that accumulates into a process-table exhaustion class
-- (CLAUDE.md "Background-Task Hygiene"). `force=true` skips
-- the LSP graceful-shutdown handshake and sends SIGKILL to
-- the child immediately, which is what we want at script exit.
local clients = vim.lsp.get_clients and vim.lsp.get_clients()
                or vim.lsp.get_active_clients()
for _, client in ipairs(clients or {}) do
    pcall(vim.lsp.stop_client, client.id, true)
end
-- Brief wait so SIGKILL signal-delivery happens before nvim exits.
vim.wait(200)

if ok then
    io.stdout:write("PASS: " .. test_name .. "\n")
    os.exit(0)
else
    io.stdout:write("FAIL: " .. test_name .. ": " .. tostring(msg) .. "\n")
    os.exit(1)
end
