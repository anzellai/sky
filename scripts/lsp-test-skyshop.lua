-- Headless LSP test driver targeting an existing project (no fixture
-- writes). Used to verify the Sky LSP behaves correctly on real
-- codebases — currently `examples/13-skyshop`, which exercises the
-- ~12MB Stripe SDK FFI surface.
--
-- Unlike scripts/lsp-test-nvim.lua (which rewrites
-- `<project>/src/Main.sky` to a known fixture), this driver leaves
-- the project untouched. It opens the requested file and probes
-- specific (line, col) positions against the LSP.
--
-- Usage:
--   nvim --headless -u NONE -l scripts/lsp-test-skyshop.lua \
--        <project-dir> <relative-file> <test-name>
--
-- Test names:
--   hover-stripe-newparams     — hover on `newCustomerListParams` in
--                                src/Lib/Stripe.sky:81 col 26
--   hover-stripe-setkey        — hover on `setKey` in
--                                src/Lib/Stripe.sky:48 col 21
--   completion-stripe-prefix   — completion at `Stripe.<probe>` in a
--                                temporary buffer line; checks that
--                                Stripe FFI symbols appear
--   completion-customer-prefix — same shape for `Customer.`
--
-- Output: prints "PASS: <test>" or "FAIL: <test>: <reason>" to stdout.
-- Exit code 0 on pass, 1 on fail.

local args = arg or {}
if #args < 3 then
    io.stderr:write("usage: lsp-test-skyshop.lua <project-dir> <relative-file> <test-name>\n")
    os.exit(1)
end

local project_dir = args[1]
local rel_file = args[2]
local test_name = args[3]
local target_path = project_dir .. "/" .. rel_file


local function find_sky_binary()
    local candidates = {
        vim.fn.expand("~/.local/bin/sky"),
        vim.fn.getcwd() .. "/sky-out/sky",
        "sky",
    }
    for _, c in ipairs(candidates) do
        if vim.fn.executable(c) == 1 then
            return c
        end
    end
    return nil
end


local function start_lsp()
    local sky = find_sky_binary()
    if not sky then
        io.stderr:write("ERROR: cannot find `sky` binary\n")
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

    vim.cmd("edit " .. target_path)
    local bufnr = vim.api.nvim_get_current_buf()
    vim.lsp.buf_attach_client(bufnr, client_id)

    -- Indexing a project of skyshop's size (43 modules + 18 FFI deps,
    -- ~12MB Stripe SDK) takes longer than the synthetic fixture.
    -- Generous wait for the workspace index to populate.
    vim.wait(30000, function()
        return #vim.lsp.get_clients({ bufnr = bufnr }) > 0
    end, 100)
    vim.wait(45000)

    return bufnr, client_id
end


local function test_hover(bufnr, line, col, expected_substr)
    local result = nil
    vim.lsp.buf_request(bufnr, "textDocument/hover",
        {
            textDocument = vim.lsp.util.make_text_document_params(bufnr),
            position     = { line = line, character = col },
        },
        function(_, res, _, _) result = res end)
    vim.wait(15000, function() return result ~= nil end, 100)

    if not result or not result.contents then
        return false, "no hover content"
    end

    local body = result.contents.value or result.contents
    if type(body) ~= "string" then
        return false, "hover body not a string"
    end

    if not body:find(expected_substr, 1, true) then
        return false, string.format("hover %q lacks %q", body, expected_substr)
    end
    return true, body
end


local function test_completion_at(bufnr, line, col, must_contain)
    local result = nil
    vim.lsp.buf_request(bufnr, "textDocument/completion",
        {
            textDocument = vim.lsp.util.make_text_document_params(bufnr),
            position     = { line = line, character = col },
        },
        function(_, res, _, _) result = res end)
    vim.wait(15000, function() return result ~= nil end, 100)

    if not result then return false, "no completion response" end
    local items = result.items or result
    if type(items) ~= "table" then return false, "result not a list" end

    for _, item in ipairs(items) do
        if item.label == must_contain then
            return true, string.format("found %q in %d items", must_contain, #items)
        end
    end

    local labels = {}
    for i = 1, math.min(8, #items) do
        labels[#labels+1] = items[i].label or "?"
    end
    return false, string.format("expected %q in %d items; first 8: %s",
        must_contain, #items, table.concat(labels, ", "))
end


local tests = {
    -- Hover on `newCustomerListParams` (line 80 0-based, col 19 = the
    -- 'n' of newCustomerListParams). Expect the FFI sig to surface.
    ["hover-stripe-newparams"] = function()
        local bufnr = start_lsp()
        return test_hover(bufnr, 80, 19, "CustomerListParams")
    end,

    -- Diagnostic: probe across multiple (line, col) pairs and dump
    -- each hover response so we can see what the LSP returns.
    -- Reads probes from env var SKY_HOVER_PROBES (format
    -- "line:col,line:col,..."), default probes line 80 cols 12..40.
    ["hover-debug"] = function()
        local bufnr = start_lsp()
        local probes_env = vim.fn.getenv("SKY_HOVER_PROBES")
        local probes = {}
        if probes_env ~= vim.NIL and probes_env ~= "" then
            for pair in string.gmatch(probes_env, "([^,]+)") do
                local l, c = pair:match("(%d+):(%d+)")
                if l and c then
                    probes[#probes+1] = { tonumber(l), tonumber(c) }
                end
            end
        else
            for _, c in ipairs({12, 13, 16, 18, 19, 20, 26, 35, 40}) do
                probes[#probes+1] = { 80, c }
            end
        end

        local out = ""
        for _, lc in ipairs(probes) do
            local line, col = lc[1], lc[2]
            local result = nil
            vim.lsp.buf_request(bufnr, "textDocument/hover",
                {
                    textDocument = vim.lsp.util.make_text_document_params(bufnr),
                    position     = { line = line, character = col },
                },
                function(_, res, _, _) result = res end)
            vim.wait(8000, function() return result ~= nil end, 50)

            local body = nil
            if result and result.contents then
                body = result.contents.value or result.contents
            end
            if type(body) == "string" then
                body = body:sub(1, 100):gsub("\n", " | ")
            else
                body = "<nil>"
            end
            out = out .. string.format("\n  %d:%d  -> %s", line, col, body)
        end
        return false, out
    end,

    -- Hover on `setKey` (line 47 0-based, col 18 = the 's' of setKey).
    ["hover-stripe-setkey"] = function()
        local bufnr = start_lsp()
        return test_hover(bufnr, 47, 18, "setKey")
    end,

    -- Completion at `Stripe.set` somewhere in the file. We probe the
    -- existing line with `Stripe.setKey key` (line 47 0-based);
    -- position cursor right after `Stripe.set` (col 21). Expect the
    -- LSP to surface `Stripe.setKey` (or similar) in completion items.
    -- Note: `setKey` is NOT a label "Stripe.setKey" but the qualified
    -- completion path renders it as that.
    ["completion-stripe-prefix"] = function()
        local bufnr = start_lsp()
        -- Line 47 source = `        _ = Stripe.setKey key`
        --                   012345678901234567890123456789
        -- col 21 = right after `Stripe.set` (between t and K).
        return test_completion_at(bufnr, 47, 21, "Stripe.setKey")
    end,
}


local fn = tests[test_name]
if not fn then
    io.stderr:write("Unknown test: " .. test_name .. "\n")
    os.exit(1)
end

local ok, msg = fn()
if ok then
    io.stdout:write("PASS: " .. test_name .. "\n")
    os.exit(0)
else
    io.stdout:write("FAIL: " .. test_name .. ": " .. tostring(msg) .. "\n")
    os.exit(1)
end
