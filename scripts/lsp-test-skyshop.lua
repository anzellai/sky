-- Headless LSP driver for a REAL FFI-heavy project: `examples/13-skyshop`,
-- whose generated Go FFI surface (the Stripe SDK, ~76k symbols) is the largest
-- in the repository. It leaves the project untouched: it opens one file and
-- probes positions it FINDS in that file, so an edit that moves a line does not
-- turn the cases into probes of whitespace.
--
-- Run by `scripts/lsp-test-nvim.sh` as the `realffi` group of the `lsp` gate:
--
--   nvim --headless -u NONE -l scripts/lsp-test-skyshop.lua <project-dir>
--
-- Cases (one LSP session for all of them):
--   ffi-hover-constructor   hover on `Stripe.newCustomerListParams` shows a
--                           signature that names the FFI type it returns
--   ffi-hover-function      hover on `Stripe.setKey` shows its signature
--                           (`String`), not only the token
--   ffi-completion-member   completion after `Stripe.set` offers `setKey`
--
-- Output: one `PASS: <case>` / `FAIL: <case>: <reason>` line per case, then
-- `CASES: realffi <n>`, the same protocol as scripts/lsp-corpus-nvim.lua.
-- Exit code 0 when every case passes, 1 otherwise, 2 on a setup failure.
--
-- The compiler under test is the `sky` on `$PATH` (the gate puts the binary it
-- just built first), exactly as for the other two drivers.

local args = arg or {}
if #args < 1 then
    io.stderr:write("usage: lsp-test-skyshop.lua <project-dir>\n")
    os.exit(2)
end

local group_name = "realffi"
local project_dir = vim.fn.fnamemodify(args[1], ":p"):gsub("/$", "")
local rel_file = "src/Lib/Stripe.sky"
local target_path = project_dir .. "/" .. rel_file

if vim.fn.filereadable(target_path) ~= 1 then
    io.stderr:write("ERROR: " .. target_path .. " is not readable\n")
    os.exit(2)
end

-- The FFI surface is generated, not committed (`sky-ffi/` is gitignored). The
-- shell driver runs `sky install` when it is missing; if it is still missing
-- here, every case would measure an LSP with no Stripe bindings at all.
if vim.fn.filereadable(project_dir .. "/sky-ffi/stripe.skyi") ~= 1 then
    io.stderr:write("ERROR: " .. project_dir .. "/sky-ffi/stripe.skyi is missing; "
        .. "run `sky install` in the project first\n")
    os.exit(2)
end

if vim.fn.executable("sky") ~= 1 then
    io.stderr:write("ERROR: cannot find `sky` on $PATH\n")
    os.exit(2)
end

local client_id = vim.lsp.start({
    name = "sky-lsp",
    cmd = { "sky", "lsp" },
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

-- The first position of `needle` in the buffer, 0-based (line, col).
local function locate(needle)
    local lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
    for i, l in ipairs(lines) do
        local c = l:find(needle, 1, true)
        if c then
            return i - 1, c - 1
        end
    end
    return nil, nil
end

local function request(method, line, col, timeout_ms)
    local result, done = nil, false
    vim.lsp.buf_request(bufnr, method, {
        textDocument = vim.lsp.util.make_text_document_params(bufnr),
        position = { line = line, character = col },
    }, function(_, res, _, _)
        result = res
        done = true
    end)
    vim.wait(timeout_ms, function() return done end, 100)
    return result
end

local function hover_text(line, col)
    local res = request("textDocument/hover", line, col, 15000)
    if not res or not res.contents then
        return nil
    end
    local body = res.contents.value or res.contents
    if type(body) ~= "string" then
        return nil
    end
    return body
end

-- Wait until the server answers a hover on a Stripe binding: indexing a
-- project of this size takes far longer than the fixture suites. Bounded, so a
-- server that never answers turns into failed cases, not a hung gate.
do
    local l, c = locate("Stripe.setKey")
    if l then
        local deadline = vim.loop.now() + 120000
        while vim.loop.now() < deadline do
            if hover_text(l, c + #"Stripe.") then
                break
            end
            vim.wait(2000)
        end
    end
end

local results = {}
local function case(name, fn)
    local ok, msg = fn()
    results[#results + 1] = { name = name, ok = ok, msg = msg or "" }
end

case("ffi-hover-constructor", function()
    local needle = "Stripe.newCustomerListParams"
    local l, c = locate(needle)
    if not l then
        return false, "the file no longer calls " .. needle
    end
    local body = hover_text(l, c + #"Stripe.")
    if not body then
        return false, "no hover content"
    end
    -- The token itself contains `CustomerListParams`, so the FFI type must
    -- appear in a SIGNATURE: after an arrow.
    local arrow = body:find("->", 1, true)
    if not arrow or not body:find("CustomerListParams", arrow, true) then
        return false, string.format("hover %q shows no signature returning CustomerListParams", body)
    end
    return true
end)

case("ffi-hover-function", function()
    local needle = "Stripe.setKey"
    local l, c = locate(needle)
    if not l then
        return false, "the file no longer calls " .. needle
    end
    local body = hover_text(l, c + #"Stripe.")
    if not body then
        return false, "no hover content"
    end
    if not (body:find("setKey", 1, true) and body:find("String", 1, true)) then
        return false, string.format("hover %q does not show setKey's String signature", body)
    end
    return true
end)

case("ffi-completion-member", function()
    local needle = "Stripe.setKey"
    local l, c = locate(needle)
    if not l then
        return false, "the file no longer calls " .. needle
    end
    local res = request("textDocument/completion", l, c + #"Stripe.set", 15000)
    if not res then
        return false, "no completion response"
    end
    local items = res.items or res
    if type(items) ~= "table" then
        return false, "completion result is not a list"
    end
    for _, item in ipairs(items) do
        local label = item.label or ""
        if label == "setKey" or label == "Stripe.setKey" then
            return true
        end
    end
    local labels = {}
    for i = 1, math.min(8, #items) do
        labels[#labels + 1] = items[i].label or "?"
    end
    return false, string.format("setKey not among %d items; first 8: %s",
        #items, table.concat(labels, ", "))
end)

-- Stop every spawned client BEFORE exit so no `sky lsp` child is reparented.
for _, client in ipairs(vim.lsp.get_clients and vim.lsp.get_clients() or {}) do
    pcall(vim.lsp.stop_client, client.id, true)
end
vim.wait(200)

-- Leading newline: nvim's own messages carry no trailing newline (see
-- lsp-corpus-nvim.lua).
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
io.stdout:write(string.format("CASES: %s %d\n", group_name, #results))
os.exit(failed == 0 and 0 or 1)
