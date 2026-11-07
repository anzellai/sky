//go:build !js

package rt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveScrollSnapshotIsOwnedByTheDispatchNotMirrors(t *testing.T) {
	sess := navSession()
	sess.viewID = "render"
	withLiveOriginTab("origin", func() {
		sess.recordScroll(scrollCmd{Top: true})
		sess.recordScroll(scrollCmd{Target: "part"})
	})
	if got := sess.prepareFrameSnapshot("body"); len(got.scrolls) != 0 || !sess.hasDispatchScrolls() {
		t.Fatal("a mirror/resync must not consume dispatch commands")
	}
	snapshot := sess.prepareDispatchFrameSnapshot("body")
	if len(snapshot.scrolls) != 2 || snapshot.scrolls[0].Tab != "origin" || !snapshot.scrolls[0].Command.Top || snapshot.scrolls[1].Command.Target != "part" {
		t.Fatal("capture must preserve origin and command order")
	}
	if sess.hasDispatchScrolls() || len(sess.prepareDispatchFrameSnapshot("body").scrolls) != 0 {
		t.Fatal("a dispatch's commands must be consumed exactly once")
	}
	var frame struct{ Scrolls []liveScrollDelivery }
	if err := json.Unmarshal([]byte(encodeSSEFrameFromSnapshot(snapshot)), &frame); err != nil || len(frame.Scrolls) != 2 {
		t.Fatal("the render envelope must carry its own immutable command data")
	}
	if strings.Contains(encodeSSEFrameFromSnapshot(sess.prepareFrameSnapshot("body")), "scrolls") {
		t.Fatal("reconnect/resync must not replay commands")
	}
}

func TestLiveScrollBackgroundChoosesOnlyTheOldestTab(t *testing.T) {
	sess := navSession()
	sess.registerSSEConn("oldest")
	sess.registerSSEConn("later")
	sess.recordScroll(scrollCmd{Target: "part"})
	commands := sess.takeDispatchScrolls()
	if len(commands) != 1 || commands[0].Tab != "oldest" {
		t.Fatal("background work must address exactly the oldest connected tab")
	}
	empty := navSession()
	empty.recordScroll(scrollCmd{Top: true})
	if empty.hasDispatchScrolls() {
		t.Fatal("background work without a browser must not retain commands for a future unrelated tab")
	}
}

func TestLiveScrollOnlyUpdateStillShipsItsRender(t *testing.T) {
	app, sess := newMirrorTestApp(), navSession()
	sess.cancelSub = make(chan struct{})
	sess.model = "model"
	sess.lastShippedBody = app.renderView(sess)
	app.update = func(_, model any) any { return SkyTuple2{V0: model, V1: Nav_scrollIntoView("part")} }
	withLiveOriginTab("origin", func() { app.deliverSubMsg(sess, "Next") })
	select {
	case frame := <-sess.sseCh:
		var value struct{ Scrolls []liveScrollDelivery }
		if err := json.Unmarshal([]byte(frame.data), &value); err != nil || len(value.Scrolls) != 1 || value.Scrolls[0].Tab != "origin" {
			t.Fatal("an unchanged body must not suppress its scroll command")
		}
	default:
		t.Fatal("scroll-only update was suppressed")
	}
}

func TestLiveScrollFailedDispatchDiscardsItsCommands(t *testing.T) {
	app, sess := newMirrorTestApp(), navSession()
	sess.cancelSub = make(chan struct{})
	sess.model = "model"
	app.renderView(sess)
	before := logNavRejected
	rejectedAfterScroll := false
	logNavRejected = func(navCmd, string) {
		rejectedAfterScroll = sess.hasDispatchScrolls()
		panic("rt.Coerce: synthetic dispatch failure")
	}
	defer func() { logNavRejected = before }()
	app.update = func(_, model any) any {
		return SkyTuple2{V0: model, V1: Cmd_batch([]any{Nav_scrollToTop(), Nav_pushUrl("https://off-site.test/")})}
	}
	withLiveOriginTab("origin", func() {
		if body := app.dispatch(sess, "Next"); body != "" {
			t.Fatal("failed dispatch must not publish a new view")
		}
	})
	if !rejectedAfterScroll {
		t.Fatal("the failure must occur after the dispatch queues its scroll")
	}
	if sess.hasDispatchScrolls() || len(sess.prepareDispatchFrameSnapshot(sess.lastComputedBody).scrolls) != 0 {
		t.Fatal("a recovered dispatch must not publish commands against its rolled-back view")
	}
}

func TestLiveScrollHTMLMetadataPreservesUnicodeAndLegacyBody(t *testing.T) {
	commands := []liveScrollDelivery{{Tab: "origin", Command: scrollCmd{Target: "snow-雪-</script>"}}}
	rr := httptest.NewRecorder()
	writeEventHTML(rr, 9, nil, "<div>view</div>", "render", commands)
	parts := strings.Split(rr.Body.String(), "<!--sky-scroll:")
	if len(parts) != 2 || parts[0] != "<div>view</div>" || !strings.HasSuffix(parts[1], "-->") {
		t.Fatal("full HTML must retain the legacy body plus inert bounded-framing metadata")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(parts[1], "-->"))
	var decoded []liveScrollDelivery
	if err != nil || json.Unmarshal(raw, &decoded) != nil || len(decoded) != 1 || decoded[0] != commands[0] {
		t.Fatal("Unicode and markup-like ids must round-trip as data")
	}
	legacy := httptest.NewRecorder()
	writeEventHTML(legacy, 9, nil, "<div>view</div>", "render")
	if legacy.Body.String() != "<div>view</div>" {
		t.Fatal("a response without commands must retain its exact old body")
	}
	boot := livePageScriptsWithScrolls("sid", liveBannerConfig{}, "csrf", "", "render", "origin", "", commands)
	end := strings.Index(boot, "</script>")
	if end < 0 {
		t.Fatal("missing boot block")
	}
	var cfg liveBootCfg
	if err := json.Unmarshal([]byte(boot[strings.Index(boot, ">")+1:end]), &cfg); err != nil || len(cfg.Scrolls) != 1 || cfg.Scrolls[0] != commands[0] {
		t.Fatal("initial page must deliver its own escaped command data")
	}
}

func TestLiveScrollPageGETCarriesMonotonicRenderSequence(t *testing.T) {
	app := newMirrorTestApp()
	first := httptest.NewRecorder()
	app.handleInitial(first, httptest.NewRequest("GET", "/", nil))
	initial, err := strconv.ParseInt(first.Header().Get("X-Sky-Seq"), 10, 64)
	if err != nil || initial <= 0 {
		t.Fatal("a fetched page must identify its committed render sequence")
	}
	second := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/", nil)
	for _, cookie := range first.Result().Cookies() {
		request.AddCookie(cookie)
	}
	request.Header.Set("X-Sky-Tab", "origin")
	app.handleInitial(second, request)
	next, err := strconv.ParseInt(second.Header().Get("X-Sky-Seq"), 10, 64)
	if err != nil || next <= initial {
		t.Fatal("a newer GET must let the client reject an older event's body and scroll")
	}
}

func TestLiveScrollClientCommitAndSupersession(t *testing.T) {
	node := requireNode(t)
	dir := t.TempDir()
	client, harness := filepath.Join(dir, "client.js"), filepath.Join(dir, "harness.cjs")
	if err := os.WriteFile(client, []byte(selectHoldJS+liveClientCoreJS), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte(liveScrollProtocolHarness), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, harness, client)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("scroll render-commit regression: %v\n%s", err, output)
	}
}

const liveScrollProtocolHarness = `const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const source = fs.readFileSync(process.argv[2], 'utf8');
let checks = 0;
const equal = (got, expected) => { checks++; assert.equal(JSON.stringify(got), JSON.stringify(expected)); };

function fixture() {
  const calls = [], errors = [], timers = [], listeners = {}, windowListeners = {}, moves = [], fragments = [];
  const state = { target: 'A', held: 'A' };
  let reply = {ok:true,status:200,headers:{get(){return null;}},text:async()=>'',json:async()=>({})};
  const document = {
    readyState: 'loading', body: null, activeElement: null, visibilityState: 'visible',
    getElementById(id) { return id === 'sky-live-cfg' ? {textContent: JSON.stringify({sid:'S',tab:'T',view:'A'})} : null; },
    addEventListener(name, fn) { (listeners[name] ||= []).push(fn); }, querySelector() { return null; }, querySelectorAll() { return []; }
  };
  const location = {href:'http://fixture.test/',origin:'http://fixture.test',pathname:'/',search:'',hash:'',reload(){},assign(){},replace(){}};
  const sandbox = {
    document, location, history:{state:null,pushState(_s,_t,url){move('push',url);},replaceState(_s,_t,url){move('replace',url);}}, navigator:{},
    console:{warn(){},log(){},error(name){errors.push(String(name));}},
    setTimeout(fn, delay) {timers.push({fn,delay});return timers.length;}, clearTimeout(){},setInterval(){return 0;},clearInterval(){},
    sessionStorage:{getItem(){return null;},setItem(){},removeItem(){}},
    EventSource:function(){this.addEventListener=function(){};this.close=function(){};},
    fetch:async()=>reply,
    URL, Uint8Array, TextDecoder, atob, JSON, Math, Date, Promise, Object, Array, String, Number, parseInt, encodeURIComponent,
    addEventListener(name,fn){(windowListeners[name] ||= []).push(fn);},scrollTo(){},crypto:{getRandomValues(x){return x;}}
  };
  function move(kind,url){
    moves.push([kind,url]);const u=new URL(url,location.href);
    for(const key of ['href','origin','pathname','search','hash'])location[key]=u[key];
  }
  sandbox.window = sandbox;
  sandbox.__skyScrollApply = command => calls.push([command.target || 'TOP',state.target,state.held]);
  vm.createContext(sandbox); vm.runInContext(source, sandbox);
  const get = expression => vm.runInContext(expression, sandbox);
  sandbox.__skyApplyPatches = patches => { for(const patch of patches) if(patch.text !== undefined) state.held = patch.text; };
  sandbox.__skyPatch = body => {const label=/data-test-page="([A-Z])"/.exec(body)?.[1]||body;state.target=label;state.held=label;sandbox.__skyHeld=null;};
  sandbox.__skySendFragment=()=>fragments.push(location.hash);
  function frame(seq, view, apply, commands, base, globalSeq) {
    get('__skyHandleResponse')(seq,null,apply,globalSeq,view,base || '',!!base,commands);
  }
  const own = target => [{tab:'T',command:{target,top:false,ifNeeded:false}}];
  const select = {tagName:'SELECT'};
  const hold = value => {get('__skyHold')({id:'select',text:value},select);state.target=value;};
  function flush() {
    document.activeElement=null;
    get('__skyFlushHeld')(select);
    const timer=timers.find(x=>x.delay===0 && String(x.fn).includes('__skyHeld !== h'));
    assert(timer,'select completion must use its existing flush task');timer.fn();
  }
  function setReply(view,seq,commands){
    const headers={'X-Sky-View':view,'X-Sky-Seq':seq===null?null:String(seq)};
    const cfg=JSON.stringify({scrolls:commands||null}).replace(/</g,'\\u003c');
    const text='<div data-test-page="'+view+'"></div><script type="application/json" id="sky-live-cfg">'+cfg+'</script>';
    reply={ok:true,status:200,headers:{get:k=>headers[k]||null},text:async()=>text,json:async()=>({})};
  }
  async function navigate(kind,url){
    if(kind==='command')await get('__skyNavApply')(url,false);
    else if(kind==='click'){
      assert(listeners.click.length>0);
      const anchor={tagName:'A',parentElement:null,hasAttribute:k=>k==='sky-nav',getAttribute:k=>k==='href'?url:null};
      for(const listener of listeners.click)listener({target:anchor,button:0,defaultPrevented:false,preventDefault(){}});
    }else{
      const u=new URL(url,location.href);for(const k of ['href','origin','pathname','search','hash'])location[k]=u[k];
      assert(windowListeners.popstate.length>0);for(const listener of windowListeners.popstate)listener();
    }
    await new Promise(resolve=>setImmediate(resolve));
  }
  return {sandbox,state,calls,errors,document,get,frame,own,select,hold,flush,moves,fragments,setReply,navigate};
}

{
  const f=fixture();
  f.frame(1,'B',()=>{f.state.target='B';f.state.held='B';},[...f.own('part'),{tab:'OTHER',command:{target:'wrong'}}],'A',7);
  equal(f.calls,[['part','B','B']]);
  f.frame(1,'C',()=>{throw Error('stale local frame applied');},f.own('stale'),'B',8);
  f.frame(2,'C',()=>{throw Error('stale global frame applied');},f.own('stale'),'B',7);
  equal(f.calls,[['part','B','B']]);
}
{
  const f=fixture();
  f.frame(2,'C',()=>{f.state.target='C';},f.own('C'),'B');
  equal(f.calls,[]);
  f.frame(1,'B',()=>{f.state.target='B';},f.own('B'),'A');
  equal(f.calls.map(x=>x.slice(0,2)),[['B','B'],['C','C']]);
}
{
  const f=fixture();
  f.frame(2,'C',()=>{throw Error('superseded held broadcast applied');},f.own('C'),'B',3);
  f.frame(1,'B',()=>{f.state.target='B';},f.own('B'),'A',4);
  equal(f.calls.map(x=>x.slice(0,2)),[['B','B']]);
}
// A scroll from B must not hit C's same-id node outside the held select.
{
  const f=fixture();f.document.activeElement=f.select;
  f.frame(1,'B',()=>f.hold('B'),f.own('part'),'A');equal(f.calls,[]);
  f.frame(2,'C',()=>f.hold('C'),undefined,'B');
  f.flush();equal(f.calls,[]);equal(f.state,{target:'C',held:'C'});
}
{
  const f=fixture();f.document.activeElement=f.select;
  f.frame(1,'B',()=>f.hold('B'),f.own('old'),'A');
  f.frame(2,'C',()=>f.hold('C'),f.own('new'),'B');equal(f.calls,[]);
  f.flush();equal(f.calls,[['new','C','C']]);
}
// Render lineage is a dispatch, even when its HTML content id is unchanged.
{
  const f=fixture();f.document.activeElement=f.select;
  f.frame(1,'B',()=>f.hold('B'),f.own('old'),'A');
  f.frame(2,'B',()=>{},undefined,'B');f.flush();equal(f.calls,[]);
}
{
  const f=fixture();f.document.activeElement=f.select;
  f.frame(1,'B',()=>f.hold('B'),f.own('old'),'A');
  f.frame(2,'C',()=>f.get('__skyPatchOrHold')('C'),f.own('new'));
  f.flush();equal(f.calls,[['new','C','C']]);
}
{
  const f=fixture();
  const deliveries=f.own('snow-\u96ea-</script>');
  const body='<div>view</div>';
  const reply=body+'<!--sky-scroll:'+Buffer.from(JSON.stringify(deliveries),'utf8').toString('base64')+'-->';
  equal(f.get('__skyScrollBodyReply')(reply),{body,scrolls:deliveries});
  equal(f.get('__skyScrollBodyReply')(body),{body,scrolls:null});
  const safeJson=JSON.stringify({scrolls:deliveries}).replace(/</g,'\\u003c');
  const page='<script type="application/json" id="sky-live-cfg">'+safeJson+'</script>';
  equal(f.get('__skyFetchedPageScrolls')(page),deliveries);
  f.sandbox.console.error=()=>{throw Error('broken logger');};
  f.get('__skyScrollProblem')('SyntheticScrollFailure');checks++;
}
async function fetchedPages(){
  for(const kind of ['click','popstate','command']){
    {
      const f=fixture();f.setReply('B',2,f.own('page-B'));await f.navigate(kind,'/B#b');
      equal(f.state.target,'B');equal(f.calls.map(x=>x.slice(0,2)),[['page-B','B']]);
      f.frame(1,'A',()=>{throw Error('old event after fetched page applied');},f.own('old-A'));
      equal(f.state.target,'B');equal(f.calls.length,1);
    }
    {
      const f=fixture();f.frame(2,'B',()=>{f.state.target='B';},undefined);
      f.setReply('A',1,f.own('old-A'));await f.navigate(kind,'/A#old');
      equal(f.state.target,'B');equal(f.calls,[]);equal(f.moves,[]);equal(f.fragments,[]);
    }
    {
      const f=fixture();f.frame(1,'A',()=>{f.state.target='A';},undefined);
      f.setReply('B',2,f.own('page-B'));await f.navigate(kind,'/B');
      equal(f.state.target,'B');equal(f.calls.map(x=>x.slice(0,2)),[['page-B','B']]);
    }
    {
      const f=fixture();f.frame(2,'B',()=>{f.state.target='B';},undefined);
      f.setReply('L',null,null);await f.navigate(kind,'/L');
      equal(f.state.target,'L');equal(f.calls,[]);
    }
  }
}
fetchedPages().then(()=>process.stdout.write(JSON.stringify({passed:true,checks})+'\n')).catch(error=>{process.stderr.write(String(error.stack)+'\n');process.exitCode=1;});
`
