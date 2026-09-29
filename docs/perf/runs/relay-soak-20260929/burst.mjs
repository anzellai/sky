// Accelerated phase: N requests through the relay's HTTP stack (4 at a time),
// and after every STEP the live heap after a forced GC, goroutines and RSS.
import { execSync } from 'node:child_process';
const [BASE, PP, PID, N, STEP] = [process.argv[2], process.argv[3], process.argv[4], +process.argv[5], +process.argv[6]];
const paths = ['/', '/t/recv?ch=nochannel&from=0&wait=0', '/status?m=nomachine'];
const stat = async (done) => {
  const h = await (await fetch(`${PP}/heap?gc=1&debug=1`)).text();
  const f = k => (h.match(new RegExp(`# ${k} = (\\d+)`)) || [])[1];
  const g = (await (await fetch(`${PP}/goroutine?debug=1`)).text()).split('\n')[0].split(' ').pop();
  const rss = execSync(`ps -o rss= -p ${PID}`).toString().trim();
  console.log([done, f('HeapAlloc'), f('HeapInuse'), f('HeapObjects'), f('NextGC'), g, rss].join(' '));
};
console.log('requests live_heap_after_gc heap_inuse heap_objects next_gc goroutines rss_kb');
await stat(0);
let done = 0;
while (done < N) {
  const target = done + STEP;
  const worker = async () => { while (done < target) { const i = done++; await (await fetch(BASE + paths[i % 3])).arrayBuffer(); } };
  await Promise.all([worker(), worker(), worker(), worker()]);
  await stat(done);
}
