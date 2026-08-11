#!/usr/bin/env node
// Drive sky lsp against every example's entry file. Assert 0 diagnostics.
const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');

const ROOT = "/Users/anzel/works/playground/sky/examples";
const examples = fs.readdirSync(ROOT).filter(d => /^\d+-/.test(d)).sort();
const results = [];

async function probeExample(dir) {
    return new Promise(resolve => {
        const projRoot = path.join(ROOT, dir);
        const entry = path.join(projRoot, "src", "Main.sky");
        if (!fs.existsSync(entry)) return resolve({dir, status: "no-entry"});
        const uri = "file://" + entry;
        const content = fs.readFileSync(entry, 'utf8');
        const proc = spawn("/Users/anzel/.local/bin/sky", ["lsp"], { cwd: projRoot });
        let diagCount = -1;
        let alive = true;
        let firstDiag = null;
        proc.on('exit', () => { alive = false; });
        proc.stdin.on('error', () => {});

        function send(msg) {
            if (!alive) return;
            const body = Buffer.from(JSON.stringify(msg));
            const hdr = Buffer.from(`Content-Length: ${body.length}\r\n\r\n`);
            try { proc.stdin.write(Buffer.concat([hdr, body])); } catch {}
        }
        let buf = Buffer.alloc(0);
        proc.stdout.on('data', chunk => {
            buf = Buffer.concat([buf, chunk]);
            while (true) {
                const sep = buf.indexOf('\r\n\r\n');
                if (sep < 0) return;
                const m = buf.slice(0, sep).toString().match(/Content-Length: (\d+)/);
                if (!m) { buf = buf.slice(sep + 4); continue; }
                const n = parseInt(m[1]);
                if (buf.length < sep + 4 + n) return;
                const body = buf.slice(sep + 4, sep + 4 + n);
                buf = buf.slice(sep + 4 + n);
                try {
                    const msg = JSON.parse(body.toString());
                    if (msg.method === 'textDocument/publishDiagnostics') {
                        const diags = msg.params.diagnostics;
                        diagCount = diags.length;
                        if (diags[0]) firstDiag = diags[0].message.split('\n')[0].slice(0, 80);
                    }
                } catch {}
            }
        });

        send({jsonrpc:"2.0",id:1,method:"initialize",
              params:{processId:process.pid, rootUri:"file://"+projRoot,
                      capabilities:{textDocument:{publishDiagnostics:{}}}}});
        setTimeout(() => send({jsonrpc:"2.0",method:"initialized",params:{}}), 300);
        setTimeout(() => send({jsonrpc:"2.0",method:"textDocument/didOpen",
              params:{textDocument:{uri, languageId:"sky",version:1,text:content}}}), 800);
        setTimeout(() => send({jsonrpc:"2.0",method:"textDocument/didSave",
              params:{textDocument:{uri}, text: content}}), 2500);
        setTimeout(() => {
            if (alive) proc.kill();
            resolve({dir, diagCount, firstDiag});
        }, 8000);
    });
}

(async () => {
    for (const dir of examples) {
        const r = await probeExample(dir);
        results.push(r);
        const badge = r.diagCount === 0 ? "OK " : r.diagCount === -1 ? "?? " : "! ";
        console.log(`${badge}${dir.padEnd(30)} diag=${r.diagCount}${r.firstDiag ? " → " + r.firstDiag : ""}`);
    }
    // `diagCount === -1` means the LSP never answered — a dead, missing or
    // wedged `sky lsp`. It was badged "??" and then filtered OUT of `bad`, and
    // the script had no `process.exit` at all, so a totally broken LSP printed
    // "?? " for every example and the sweep still exited 0. Both a diagnostic
    // and a non-answer are failures, and the exit status now says so.
    const bad = results.filter(r => r.diagCount > 0);
    const dead = results.filter(r => r.diagCount === -1);
    console.log(
        `---\n${results.length} examples · ${bad.length} with diagnostics · ` +
        `${dead.length} with no LSP response`);
    if (results.length === 0) {
        console.error("FAIL: no examples probed — the sweep verified nothing");
        process.exit(2);
    }
    if (dead.length > 0) {
        console.error(`FAIL: LSP never responded for: ${dead.map(r => r.dir).join(", ")}`);
    }
    if (bad.length > 0) {
        console.error(`FAIL: diagnostics reported for: ${bad.map(r => r.dir).join(", ")}`);
    }
    process.exit(bad.length + dead.length > 0 ? 1 : 0);
})();
