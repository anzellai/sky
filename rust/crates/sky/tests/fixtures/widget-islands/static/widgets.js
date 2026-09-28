// Widget-island e2e fixture (scripts/islands-e2e.sh): two third-party-style
// widgets registered through window.Sky.island, loaded as a same-origin file
// with <script defer> so a strict Content-Security-Policy runs it.
(function () {
  window.__islandLog = [];
  window.__editorMounts = 0;
  window.__editorDestroyed = 0;

  // A contenteditable editor. Its text lives in the browser; it reports every
  // edit with send("changed", { text }) and takes the setText command.
  window.Sky.island("editor", {
    mount: function (el, props, send) {
      window.__editorMounts++;
      var label = document.createElement("div");
      label.className = "ed-label";
      var box = document.createElement("div");
      box.className = "ed-box";
      box.contentEditable = "true";
      box.textContent = props.initial || "";
      var mounts = document.createElement("div");
      mounts.className = "ed-mounts";
      mounts.textContent = "mounts=" + window.__editorMounts;
      var bad = document.createElement("button");
      bad.type = "button";
      bad.className = "ed-bad";
      bad.textContent = "bad";
      bad.addEventListener("click", function () { send("changed", { nope: 1 }); });
      el.appendChild(label);
      el.appendChild(box);
      el.appendChild(mounts);
      el.appendChild(bad);
      box.addEventListener("input", function () { send("changed", { text: box.textContent }); });
      this.label = label;
      this.box = box;
      this.update(props);
    },
    update: function (props) {
      this.label.textContent = "ticks=" + props.ticks;
    },
    command: function (name, payload) {
      window.__islandLog.push("command:" + name);
      if (name === "setText") {
        this.box.textContent = payload.text;
        this.send("changed", { text: payload.text });
      }
    },
    destroy: function () {
      window.__editorDestroyed++;
    }
  });

  // A click counter: send uses a mixed-case type on purpose (types are
  // matched case-insensitively).
  window.Sky.island("counter", {
    mount: function (el, props, send) {
      var n = props.start;
      var b = document.createElement("button");
      b.type = "button";
      b.className = "ctr";
      b.textContent = "count " + n;
      b.addEventListener("click", function () {
        n++;
        b.textContent = "count " + n;
        send("Inc", n);
      });
      el.appendChild(b);
    }
  });
})();
