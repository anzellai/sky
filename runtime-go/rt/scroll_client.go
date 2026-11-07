package rt

// Shared by Live and SPA. It deliberately does not rely on the relatively new
// scrollIntoView({container:"nearest"}) option: an engine ignoring that option
// scrolls outer ancestors too. All geometry below is physical vertical geometry.
// An explicit instant operation overrides CSS smooth scrolling/reduced motion.
const scrollClientJS = `(function(g){
  'use strict';
  function finite(n){return typeof n==='number'&&Number.isFinite(n);}
  function failure(){
    // A fixed class only: never the exception, target id, command or context.
    // A broken logger must not propagate into the application either.
    try{if(g.console&&typeof g.console.error==='function')g.console.error('[sky] ScrollFailure');}catch(_){ }
  }
  function usesContainment(style){
    // CSS Containment: any non-none USED containment disables body overflow
    // propagation. content-visibility:auto/hidden also imposes used containment.
    var contain=String(style.contain||'none').trim();
    return contain!=='none'||style.contentVisibility==='auto'||style.contentVisibility==='hidden';
  }
  function bodyOverflowToViewport(doc,bodyStyle){
    var root=doc.documentElement,rs=g.getComputedStyle(root);
    return doc.body.parentElement===root&&rs.display!=='none'&&bodyStyle.display!=='none'&&
      rs.overflowX==='visible'&&rs.overflowY==='visible'&&!usesContainment(rs)&&!usesContainment(bodyStyle);
  }
  function visibleViewport(height){
    var vv=g.visualViewport;
    if(vv&&finite(vv.offsetTop)&&finite(vv.height)&&vv.height>0&&finite(vv.offsetTop+vv.height))return {top:vv.offsetTop,height:vv.height,visual:true};
    return {top:0,height:height,visual:false};
  }
  function length(value,base,style,doc){
    value=String(value||'').trim();
    if(!value||value==='auto')return 0;
    var m=/^([+-]?(?:\d+(?:\.\d*)?|\.\d+))(px|%)?$/.exec(value);
    if(m){var n=Number(m[1]);return m[2]==='%'?n*base/100:n;}
    // Let the CSS engine resolve calc/min/max/clamp and any residual relative
    // units. A positioned child's top accepts negative lengths, unlike height.
    var probe,child;
    try{
      if(!doc.documentElement||!doc.createElement)return 0;
      probe=doc.createElement('div');child=doc.createElement('div');
      probe.style.cssText='all:initial;position:fixed;left:0;top:0;width:0;height:'+base+'px;border:0;padding:0;margin:0;visibility:hidden;pointer-events:none;contain:strict;overflow:hidden;';
      if(style&&style.font)probe.style.font=style.font;
      child.style.cssText='all:initial;position:absolute;left:0;width:0;height:0;border:0;padding:0;margin:0;';
      child.style.top=value;
      if(!child.style.top)return 0;
      probe.appendChild(child);doc.documentElement.appendChild(probe);
      var n=child.getBoundingClientRect().top-probe.getBoundingClientRect().top;
      return finite(n)?n:0;
    }finally{if(probe&&probe.parentNode)probe.parentNode.removeChild(probe);}
  }
  function instant(box,top,viewport,doc){
    var left=viewport?(finite(g.scrollX)?g.scrollX:doc.scrollingElement.scrollLeft):box.scrollLeft;
    if(!finite(left))left=0;
    // Native instant is independent of CSS scroll-behavior. The fallback is
    // only for old engines rejecting the option; it restores the exact inline
    // property/priority after making a synchronous, non-animated assignment.
    try{box.scrollTo({left:left,top:top,behavior:'instant'});return;}catch(_){ }
    var node=viewport?(doc.scrollingElement||doc.documentElement):box;
    if(!node)return;
    var style=node.style,old,priority;
    try{
      if(style){old=style.getPropertyValue('scroll-behavior');priority=style.getPropertyPriority('scroll-behavior');style.setProperty('scroll-behavior','auto','important');}
      if(viewport)g.scrollTo(left,top);else{box.scrollTop=top;box.scrollLeft=left;}
    }finally{if(style){if(old)style.setProperty('scroll-behavior',old,priority);else style.removeProperty('scroll-behavior');}}
  }
  g.__skyScrollApply=function(command){
    try{
      if(!command||typeof command!=='object')return;
      var doc=g.document;if(!doc)return;
      if(command.top===true){instant(g,0,true,doc);return;}
      if(typeof command.target!=='string'||!command.target)return;
      var target=doc.getElementById(command.target);
      if(!target||target.isConnected===false||!target.getClientRects||target.getClientRects().length===0)return;
      var ts=g.getComputedStyle(target);
      if(ts.visibility==='hidden'||ts.visibility==='collapse'||ts.display==='none')return;
      var box=null,node=target.parentElement;
      for(var hidden=target;hidden;hidden=hidden.parentElement)if(hidden.hidden)return;
      while(node){
        if(node===doc.scrollingElement||node===doc.documentElement)break;
        var cs=g.getComputedStyle(node);
        // A body's overflow can be propagated to the viewport. It is its own
        // box when either root axis/containment prevents CSS Overflow propagation.
        var bodyViewport=node===doc.body&&bodyOverflowToViewport(doc,cs);
        if(!bodyViewport&&/^(auto|scroll|overlay|hidden)$/.test(cs.overflowY)&&node.scrollHeight>node.clientHeight){box=node;break;}
        node=node.parentElement;
      }
      var viewport=box===null,style=viewport?g.getComputedStyle(doc.scrollingElement||doc.documentElement):g.getComputedStyle(box);
      var height=viewport?doc.documentElement.clientHeight:box.clientHeight;
      if(!finite(height)||height<=0)return;
      var portTop=0,scale=1;
      if(!viewport){
        var port=box.getBoundingClientRect();
        // offsetHeight rounds fractional CSS pixels. Prefer resolved border-box
        // height so an untransformed fractional-size port is not mistaken for
        // a scale transform (which accumulates error on long scrolls).
        var layout=parseFloat(style.height);
        if(finite(layout)&&style.boxSizing!=='border-box')layout+=(parseFloat(style.paddingTop)||0)+(parseFloat(style.paddingBottom)||0)+(parseFloat(style.borderTopWidth)||0)+(parseFloat(style.borderBottomWidth)||0);
        if(!finite(layout)||layout<=0)layout=box.offsetHeight;
        if(layout>0&&finite(port.height)&&port.height>0)scale=port.height/layout;
        portTop=port.top+(box.clientTop||0)*scale;
      }
      var rect=target.getBoundingClientRect();if(!finite(rect.top)||!finite(portTop)||!finite(scale)||scale<=0)return;
      var margin=length(ts.scrollMarginTop,height,ts,doc),padding=Math.max(0,length(style.scrollPaddingTop,height,style,doc));
      var targetTop=(rect.top-portTop)/scale-margin;
      var visible=viewport?visibleViewport(height):{top:0,height:height,visual:false};
      var paddingBottom=Math.max(0,length(style.scrollPaddingBottom,height,style,doc));
      if(command.ifNeeded===true&&targetTop>=visible.top+padding&&targetTop<visible.top+visible.height-paddingBottom)return;
      var current=viewport?(finite(g.scrollY)?g.scrollY:(doc.scrollingElement||doc.documentElement).scrollTop):box.scrollTop;
      if(!finite(current))return;
      // Native scrolling clamps the actual range, including reverse-flow boxes.
      // Never request horizontal movement, focus, a Msg, or an outer-box scroll.
      instant(viewport?g:box,current+targetTop-padding,viewport,doc);
      if(viewport&&visible.visual&&target.isConnected!==false){
        // CSSOM View explicitly records engine disagreement on whether
        // window.scrollTo coordinates the visual and layout viewports. Keep
        // the document-coordinate first scroll, then measure its INSTANT
        // result. Correct a remaining visual offset once, synchronously;
        // subtracting the old offset before the first scroll breaks engines
        // that already coordinate both viewports. No polling or animation.
        var after=visibleViewport(height),afterTop=target.getBoundingClientRect().top-margin;
        var afterCurrent=finite(g.scrollY)?g.scrollY:(doc.scrollingElement||doc.documentElement).scrollTop;
        var delta=afterTop-after.top-padding;
        if(finite(afterTop)&&finite(afterCurrent)&&finite(delta)&&delta!==0)instant(g,afterCurrent+delta,true,doc);
      }
    }catch(_){failure();}
  };
})(typeof window==='object'?window:globalThis);
`
