// Soak driver for a copy of a downstream relay app: one fake daemon (control
// WebSocket, Ed25519 hello, channel WebSocket that echoes each client message
// back) and one client on the HTTP tunnel: a long poll always waiting
// (wait=20000) and one send at a time, every SEND_MS.
import crypto from 'node:crypto';
const BASE = process.argv[2] || 'http://127.0.0.1:8781';
const MINUTES = +(process.argv[3] || 45);
const SEND_MS = +(process.argv[4] || 5000);
const WS = BASE.replace('http', 'ws');
const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
const pub = Buffer.from(publicKey.export({ format: 'jwk' }).x, 'base64url');
function base32(buf) {
  const a = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = 0, v = 0, out = '';
  for (const b of buf) { v = (v << 8) | b; bits += 8; while (bits >= 5) { out += a[(v >>> (bits - 5)) & 31]; bits -= 5; } }
  if (bits > 0) out += a[(v << (5 - bits)) & 31];
  return out;
}
const machineId = base32(crypto.createHash('sha256').update(pub).digest()).toLowerCase().slice(0, 16);
const stats = { sends: 0, recvs: 0, echoes: 0, errors: 0, dials: 0 };
const log = (...a) => console.log(new Date().toISOString(), ...a);

// Daemon.
await new Promise((resolve, reject) => {
  const ctl = new WebSocket(`${WS}/machine`);
  ctl.onerror = e => reject(new Error('ctl ' + e.message));
  ctl.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.t === 'challenge') {
      const sig = crypto.sign(null, Buffer.concat([Buffer.from((process.env.RELAY_AUTH_LABEL || 'relay-auth-v1')), Buffer.from(m.n, 'base64')]), privateKey);
      ctl.send(JSON.stringify({ t: 'hello', pub: pub.toString('base64'), sig: sig.toString('base64'), name: 'Soak' }));
    } else if (m.t === 'welcome') { log('registered', m.id, m.id === machineId ? '(id ok)' : '(ID MISMATCH)'); resolve(); }
    else if (m.t === 'dial') {
      stats.dials++;
      const chws = new WebSocket(`${WS}/accept?ch=${m.ch}`);
      chws.binaryType = 'arraybuffer';
      chws.onmessage = e2 => { chws.send(e2.data); stats.echoes++; };
      chws.onerror = () => stats.errors++;
    } else log('ctl', ev.data);
  };
});

// Client.
const post = async (path, body) => (await fetch(BASE + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : '{}' })).json();
const open = await post(`/t/open?m=${machineId}`);
if (!open.ok) { log('open failed', open); process.exit(1); }
const ch = open.ch; log('channel open');
const end = Date.now() + MINUTES * 60000;
let from = 0, ready = false, seq = 0, got = 0;
const recvLoop = (async () => {
  while (Date.now() < end) {
    try {
      const r = await (await fetch(`${BASE}/t/recv?ch=${ch}&from=${from}&wait=20000&ready=${ready ? 1 : 0}`)).json();
      stats.recvs++;
      if (r.ready) ready = true;
      got += (r.msgs || []).length; from = r.next;
      if (r.closed) { log('closed by relay'); break; }
    } catch (e) { stats.errors++; await new Promise(r => setTimeout(r, 500)); }
  }
})();
const sendLoop = (async () => {
  while (!ready && Date.now() < end) await new Promise(r => setTimeout(r, 100));
  while (Date.now() < end) {
    seq++;
    const payload = crypto.randomBytes(64 + (seq % 512)).toString('hex');
    try { const r = await post(`/t/send?ch=${ch}`, { seq, msgs: [payload] }); if (!r.ok) stats.errors++; else stats.sends++; }
    catch (e) { stats.errors++; }
    await new Promise(r => setTimeout(r, SEND_MS));
  }
})();
const tick = setInterval(() => log('stats', JSON.stringify({ ...stats, received: got, from })), 60000);
await Promise.all([recvLoop, sendLoop]);
clearInterval(tick);
await post(`/t/close?ch=${ch}`);
log('done', JSON.stringify({ ...stats, received: got }));
process.exit(0);
