package rt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Executes the actual shared client in a deterministic DOM geometry fixture.
// Real engine layout/calc/native scrolling is additionally covered by the
// browser scroll gate; this does not claim to emulate a layout engine.
func TestScrollClientGeometryAndFallback(t *testing.T) {
	node := requireNode(t)
	dir := t.TempDir()
	client := filepath.Join(dir, "scroll.js")
	harness := filepath.Join(dir, "harness.cjs")
	if err := os.WriteFile(client, []byte(scrollClientJS), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte(scrollClientHarness), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, harness, client).CombinedOutput(); err != nil {
		t.Fatalf("scroll client regression: %v\n%s", err, out)
	}
}

const scrollClientHarness = `
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function style(extra={}) {
 const props=new Map([['scroll-behavior',['smooth','important']]]);
 return Object.assign({overflowX:'visible',overflowY:'visible',contain:'none',contentVisibility:'visible',visibility:'visible',display:'block',height:'200px',boxSizing:'border-box',scrollPaddingTop:'0px',scrollMarginTop:'0px',
 getPropertyValue(k){return props.get(k)?.[0]||''},getPropertyPriority(k){return props.get(k)?.[1]||''},
 setProperty(k,v,p){props.set(k,[v,p||''])},removeProperty(k){props.delete(k)}},extra);
}
function fixture() {
 let calls=[],lookup=[],errors=[];
 const html={clientHeight:600,scrollLeft:9,scrollTop:70,style:style(),parentElement:null};
 const outer={parentElement:html,style:style({overflowY:'auto',height:'400px'}),clientHeight:400,scrollHeight:1200,scrollTop:90,scrollLeft:31};
 const inner={parentElement:outer,style:style({overflowY:'auto'}),clientHeight:200,scrollHeight:1000,scrollTop:50,scrollLeft:23,clientTop:5,offsetHeight:200,getBoundingClientRect(){return {top:100,height:200}},scrollTo(v){calls.push(['inner',v]);this.scrollTop=v.top}};
 const target={parentElement:inner,style:style(),getClientRects(){return [{}]},getBoundingClientRect(){return {top:405,height:500}}};
 const doc={documentElement:html,scrollingElement:html,body:null,getElementById(id){lookup.push(id);return id==='missing'?null:target},querySelector(){throw Error('CSS selector forbidden')}};
 const g={document:doc,console:{error(...args){errors.push(args)}},scrollX:9,scrollY:70,getComputedStyle(n){return n.style},scrollTo(v,y){if(typeof v==='number'){calls.push(['window',{left:v,top:y}]);return}calls.push(['window',v])}};
 const ctx={window:g,globalThis:g};vm.runInNewContext(fs.readFileSync(process.argv[2],'utf8'),ctx);
 return {g,doc,html,outer,inner,target,calls,lookup,errors,apply:g.__skyScrollApply};
}
{
 const f=fixture();f.target.style.scrollMarginTop='20px';f.inner.style.scrollPaddingTop='10px';
 f.apply({target:'odd[x]#"'});assert.deepEqual(f.lookup,['odd[x]#"']);
 assert.equal(f.calls.length,1);assert.equal(f.calls[0][1].top,320);assert.equal(f.calls[0][1].left,23);assert.equal(f.calls[0][1].behavior,'instant');
 assert.equal(f.outer.scrollTop,90);assert.equal(f.g.scrollY,70);
}
{
 const f=fixture();f.target.getBoundingClientRect=()=>({top:125,height:900});
 f.apply({target:'id',ifNeeded:true});assert.equal(f.calls.length,0,'visible top of tall target must not move');
 f.target.getBoundingClientRect=()=>({top:95,height:10});f.apply({target:'id',ifNeeded:true});assert.equal(f.calls[0][1].top,40);
}
{
 const f=fixture();f.apply({target:'missing'});f.target.hidden=true;f.apply({target:'id'});f.target.hidden=false;
 f.target.style.visibility='hidden';f.apply({target:'id'});f.target.style.visibility='visible';
 f.target.getClientRects=()=>[];f.apply({target:'id'});assert.equal(f.calls.length,0);assert.equal(f.errors.length,0);
}
{
 const f=fixture();f.inner.style.scrollPaddingTop='10%';f.target.style.scrollMarginTop='15px';
 f.apply({target:'id'});assert.equal(f.calls[0][1].top,315);
}
{
 const f=fixture();let removed=0;
 f.doc.createElement=()=>({style:{},appendChild(n){n.parentNode=this;this.child=n},getBoundingClientRect(){if(this.parentNode===f.html)return {top:0};assert.equal(this.style.top,'calc(10% + 5px)');return {top:25}}});
 f.html.appendChild=n=>{n.parentNode=f.html};f.html.removeChild=n=>{assert.equal(n.parentNode,f.html);n.parentNode=null;removed++};
 f.inner.style.scrollPaddingTop='calc(10% + 5px)';f.apply({target:'id'});assert.equal(f.calls[0][1].top,325);assert.equal(removed,1);
}
{
 const f=fixture();f.inner.getBoundingClientRect=()=>({top:100,height:400});f.target.getBoundingClientRect=()=>({top:710,height:20});
 f.apply({target:'id'});assert.equal(f.calls[0][1].top,350,'scaled inner geometry');
 const x=fixture();x.inner.style.height='200.5px';x.inner.getBoundingClientRect=()=>({top:100,height:200.5});x.apply({target:'id'});assert.equal(x.calls[0][1].top,350,'fractional height is not transform');
}
{
 const f=fixture();f.target.parentElement=f.html;f.target.getBoundingClientRect=()=>({top:200,height:20});
 f.apply({target:'id'});assert.equal(f.calls[0][0],'window');assert.equal(f.calls[0][1].top,270);assert.equal(f.calls[0][1].left,9);
 f.apply({top:true});assert.equal(f.calls[1][1].top,0);assert.equal(f.calls[1][1].left,9);
}
{
 const f=fixture();f.inner.scrollTo=()=>{throw Error('old options')};f.apply({target:'id'});
 assert.equal(f.inner.scrollTop,350);assert.equal(f.inner.scrollLeft,23);assert.equal(f.inner.style.getPropertyValue('scroll-behavior'),'smooth');assert.equal(f.inner.style.getPropertyPriority('scroll-behavior'),'important');assert.equal(f.outer.scrollTop,90);
}
{
 // A layout-only native window scroll keeps the visual viewport pan. The
 // bounded second measurement corrects that offset, never assuming it reset.
 const f=fixture();f.target.parentElement=f.html;f.g.visualViewport={offsetTop:100,height:200};
 f.html.style.scrollPaddingTop='10px';f.html.style.scrollPaddingBottom='20px';
 const documentTop=370;
 f.target.getBoundingClientRect=()=>({top:documentTop-f.g.scrollY,height:10});
 f.g.scrollTo=v=>{f.calls.push(['window',v]);f.g.scrollY=v.top};
 f.apply({target:'id',ifNeeded:true});
 assert.equal(f.calls.length,2);assert.equal(f.calls[0][1].top,360);assert.equal(f.calls[1][1].top,260);
 assert.equal(f.target.getBoundingClientRect().top,110);assert.equal(f.calls[1][1].left,9);
}
{
 // An engine coordinating both viewports already aligns the visible top;
 // do not subtract the old visual offset before that native operation.
 const f=fixture();f.target.parentElement=f.html;f.g.visualViewport={offsetTop:100,height:200};
 f.html.style.scrollPaddingTop='10px';
 f.target.getBoundingClientRect=()=>({top:370-f.g.scrollY,height:10});
 f.g.scrollTo=v=>{f.calls.push(['window',v]);f.g.scrollY=v.top-f.g.visualViewport.offsetTop};
 f.apply({target:'id'});assert.equal(f.calls.length,1);assert.equal(f.calls[0][1].top,360);assert.equal(f.target.getBoundingClientRect().top,110);
}
{
 const f=fixture();f.target.parentElement=f.html;f.g.visualViewport={offsetTop:100,height:200};
 f.target.getBoundingClientRect=()=>({top:150,height:900});f.apply({target:'id',ifNeeded:true});assert.equal(f.calls.length,0);
 f.g.visualViewport.height=0;f.target.getBoundingClientRect=()=>({top:300,height:10});f.apply({target:'id',ifNeeded:true});assert.equal(f.calls.length,0,'invalid visual viewport falls back to layout interval');
}
{
 const f=fixture();f.target.parentElement=f.html;f.g.visualViewport={offsetTop:100,height:200};f.html.style.scrollPaddingBottom='50px';
 f.target.getBoundingClientRect=()=>({top:340-f.g.scrollY,height:10});
 f.g.scrollTo=v=>{f.calls.push(['window',v]);f.g.scrollY=v.top};
 f.apply({target:'id',ifNeeded:true});assert.ok(f.calls.length>0,'bottom padding excludes an otherwise visible top');
}
{
 const f=fixture();f.target.isConnected=false;f.apply({target:'id'});assert.equal(f.calls.length,0);assert.equal(f.errors.length,0);
 f.target.isConnected=true;f.g.getComputedStyle=()=>{throw Error('secret target or context')};
 assert.doesNotThrow(()=>f.apply({target:'private-id'}));assert.equal(f.errors.length,1);assert.deepEqual(f.errors[0],['[sky] ScrollFailure']);
 f.g.console.error=()=>{throw Error('broken logger')};assert.doesNotThrow(()=>f.apply({target:'id'}));
}
{
 // CSS Overflow3: clip on EITHER root axis is not body propagation. Any
 // root/body used containment disables the special body propagation too.
 const cases=[['clip','clip','none','none','visible','visible'],['clip','visible','none','none','visible','visible'],
 ['visible','clip','none','none','visible','visible'],['visible','visible','layout','none','visible','visible'],
 ['visible','visible','none','paint','visible','visible'],['visible','visible','none','none','auto','visible'],
 ['visible','visible','none','none','visible','auto']];
 for(const [x,y,rootContain,bodyContain,rootCV,bodyCV] of cases){
  const f=fixture();f.doc.body=f.inner;f.inner.parentElement=f.html;
  Object.assign(f.html.style,{overflowX:x,overflowY:y,contain:rootContain,contentVisibility:rootCV});
  Object.assign(f.inner.style,{contain:bodyContain,contentVisibility:bodyCV});
  f.apply({target:'id'});assert.equal(f.calls.length,1);assert.equal(f.calls[0][0],'inner');assert.equal(f.g.scrollY,70);
 }
 const f=fixture();f.doc.body=f.inner;f.inner.parentElement=f.html;
 f.apply({target:'id'});assert.equal(f.calls[0][0],'window','visible both axes and no containment propagate body overflow');
}
{
 // Proven available forward movement cannot silently disappear in native code.
 const f=fixture();f.doc.body=f.inner;f.inner.parentElement=f.html;f.html.style.overflowX='clip';f.html.style.overflowY='clip';
 f.inner.scrollTo=v=>{f.calls.push(['native-noop',v])};
 f.apply({target:'id'});assert.equal(f.inner.scrollTop,50);assert.equal(f.g.scrollY,70);assert.equal(f.outer.scrollTop,90);
 assert.equal(f.errors.length,1,'available native movement must report fixed ScrollFailure');assert.deepEqual(f.errors[0],['[sky] ScrollFailure']);
}
{
 // At either clamped boundary, and within one pixel of it, a native no-op is
 // harmless. Reverse-flow boxes have their zero boundary at the OTHER end.
 for(const [current,targetTop,reverse] of [[800,405,false],[0,95,false],[799.6,106,false],[50,106,false],[900,405,false],[0,405,true],[-800,5,true],[-900,5,true]]){
  const f=fixture();f.inner.scrollTop=current;
  if(reverse)Object.assign(f.inner.style,{display:'flex',flexDirection:'column-reverse'});
  f.target.getBoundingClientRect=()=>({top:targetTop,height:10});
  f.inner.scrollTo=v=>{f.calls.push(['boundary-noop',v])};
  f.apply({target:'id'});assert.equal(f.errors.length,0,'clamp/rounding/reverse boundary must remain harmless');
 }
 const f=fixture();f.inner.scrollTop=-100;Object.assign(f.inner.style,{display:'flex',flexDirection:'column-reverse'});
 f.target.getBoundingClientRect=()=>({top:5,height:10});f.inner.scrollTo=()=>{};f.apply({target:'id'});
 assert.equal(f.errors.length,1,'available reverse movement must not silently no-op');
}
{
 // A successful visual-viewport-only pan has unchanged layout scrollY. It is
 // fulfillment, not a native no-op; check the completed visual position.
 const f=fixture();f.target.parentElement=f.html;f.html.scrollHeight=1000;f.g.visualViewport={offsetTop:100,height:200};
 f.target.getBoundingClientRect=()=>({top:370-f.g.scrollY,height:10});
 f.g.scrollTo=v=>{f.calls.push(['visual-pan',v]);f.g.visualViewport.offsetTop=v.top-f.g.scrollY};
 f.apply({target:'id'});assert.equal(f.g.scrollY,70);assert.equal(f.g.visualViewport.offsetTop,300);assert.equal(f.errors.length,0);
 const x=fixture();x.target.parentElement=x.html;x.html.scrollHeight=1000;x.g.visualViewport={offsetTop:100,height:200};
 x.target.getBoundingClientRect=()=>({top:370-x.g.scrollY,height:10});x.g.scrollTo=v=>x.calls.push(['native-noop',v]);
 x.apply({target:'id'});assert.equal(x.errors.length,1,'one failure after the COMPLETE viewport correction, not one per attempt');
}
{
 const f=fixture();f.html.scrollHeight=2000;f.g.scrollY=500;f.g.scrollTo=()=>{};
 f.apply({top:true});assert.equal(f.errors.length,1,'available viewport top request cannot silently no-op');
 const x=fixture();x.html.scrollHeight=2000;x.g.scrollY=0;x.g.scrollTo=()=>{};x.apply({top:true});assert.equal(x.errors.length,0);
}
`
