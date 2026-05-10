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

main =
    Task.run (Task.succeed 42)
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
    -- Hover on `run` in `Task.run` (line 20 0-based, col 9). Expect Task type.
    ["hover-task-run"] = function()
        local bufnr = start_lsp(project_dir .. "/src/Main.sky")
        return test_hover(bufnr, 20, 9, "Task")
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
}


local fn = tests[test_name]
if not fn then
    io.stderr:write("Unknown test: " .. test_name .. "\n")
    os.exit(1)
end

-- Reset fixture before EVERY test so prior writes don't leak in.
reset_fixture()

local ok, msg = fn()
if ok then
    io.stdout:write("PASS: " .. test_name .. "\n")
    os.exit(0)
else
    io.stdout:write("FAIL: " .. test_name .. ": " .. tostring(msg) .. "\n")
    os.exit(1)
end
