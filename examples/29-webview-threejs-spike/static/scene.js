/*
 * Sky.Webview spike — Three.js animated scene
 *
 * Renders a rotating torus knot + a small flock of orbiting cubes
 * against a starfield, with color cycling on the lighting. Updates
 * the HUD's FPS / renderer string / WebGL version so we can compare
 * what the webview reports versus a regular browser tab.
 *
 * This file is intentionally vanilla JS (no modules, no build step)
 * so vendored Three.js 0.158 (last UMD build) loads it directly.
 * When Sky.Webview ships, the same file works unchanged inside the
 * native webview.
 */

(function () {
    "use strict";

    if (typeof THREE === "undefined") {
        document.body.insertAdjacentHTML(
            "beforeend",
            "<div style='position:fixed;top:50%;left:50%;transform:translate(-50%,-50%);" +
                "background:#2a0a0a;padding:20px;border:1px solid #ff5a5a;border-radius:8px;" +
                "color:#ffb0b0;font-family:monospace'>" +
                "ERROR: THREE.js failed to load.<br>" +
                "Check /static/three.min.js is served correctly." +
                "</div>",
        );
        return;
    }

    var wrap = document.getElementById("canvas-wrap");
    if (!wrap) {
        console.error("[spike] #canvas-wrap not in DOM");
        return;
    }

    // ── renderer + scene setup ────────────────────────────────────
    var renderer = new THREE.WebGLRenderer({
        antialias: true,
        alpha: false,
    });
    renderer.setPixelRatio(window.devicePixelRatio || 1);
    renderer.setSize(wrap.clientWidth, wrap.clientHeight);
    wrap.appendChild(renderer.domElement);

    var scene = new THREE.Scene();
    scene.background = new THREE.Color(0x0a0e27);
    scene.fog = new THREE.FogExp2(0x0a0e27, 0.04);

    var camera = new THREE.PerspectiveCamera(
        50,
        wrap.clientWidth / wrap.clientHeight,
        0.1,
        1000,
    );
    camera.position.set(0, 0, 6);
    camera.lookAt(0, 0, 0);

    // ── lighting ──────────────────────────────────────────────────
    var ambient = new THREE.AmbientLight(0x404060, 0.6);
    scene.add(ambient);

    var keyLight = new THREE.PointLight(0x7fb8ff, 1.5, 30);
    keyLight.position.set(5, 5, 5);
    scene.add(keyLight);

    var fillLight = new THREE.PointLight(0xff7fb8, 1.0, 30);
    fillLight.position.set(-5, -2, 4);
    scene.add(fillLight);

    // ── hero mesh: torus knot, the classic "is webgl actually fast" test ──
    var knotGeometry = new THREE.TorusKnotGeometry(1.2, 0.4, 200, 24);
    var knotMaterial = new THREE.MeshStandardMaterial({
        color: 0x8fb8ff,
        metalness: 0.7,
        roughness: 0.25,
        wireframe: false,
    });
    var knot = new THREE.Mesh(knotGeometry, knotMaterial);
    scene.add(knot);

    // ── orbiting cubes: per-frame transforms validate the loop runs ──
    var cubeGroup = new THREE.Group();
    var cubeCount = 12;
    var cubes = [];
    for (var i = 0; i < cubeCount; i++) {
        var cube = new THREE.Mesh(
            new THREE.BoxGeometry(0.18, 0.18, 0.18),
            new THREE.MeshStandardMaterial({
                color: new THREE.Color().setHSL(i / cubeCount, 0.7, 0.55),
                metalness: 0.5,
                roughness: 0.4,
            }),
        );
        var angle = (i / cubeCount) * Math.PI * 2;
        cube.userData = { angle: angle, baseRadius: 2.6 };
        cube.position.set(
            Math.cos(angle) * 2.6,
            Math.sin(angle * 1.7) * 0.4,
            Math.sin(angle) * 2.6,
        );
        cubeGroup.add(cube);
        cubes.push(cube);
    }
    scene.add(cubeGroup);

    // ── starfield: points sprite, also a portability canary ──────
    var starGeometry = new THREE.BufferGeometry();
    var starCount = 800;
    var positions = new Float32Array(starCount * 3);
    for (var s = 0; s < starCount; s++) {
        positions[s * 3 + 0] = (Math.random() - 0.5) * 60;
        positions[s * 3 + 1] = (Math.random() - 0.5) * 60;
        positions[s * 3 + 2] = (Math.random() - 0.5) * 60;
    }
    starGeometry.setAttribute(
        "position",
        new THREE.BufferAttribute(positions, 3),
    );
    var starMaterial = new THREE.PointsMaterial({
        color: 0xb8c8ff,
        size: 0.08,
        sizeAttenuation: true,
    });
    var starfield = new THREE.Points(starGeometry, starMaterial);
    scene.add(starfield);

    // ── controls ──────────────────────────────────────────────────
    var rotating = true;
    var wireframe = false;

    document.getElementById("toggle-rotate").addEventListener("click", function () {
        rotating = !rotating;
        this.textContent = rotating ? "pause rotation" : "resume rotation";
    });

    document.getElementById("toggle-wireframe").addEventListener("click", function () {
        wireframe = !wireframe;
        knotMaterial.wireframe = wireframe;
        this.textContent = wireframe ? "shaded mode" : "toggle wireframe";
    });

    // ── HUD probes: renderer + WebGL version + FPS ───────────────
    var gl = renderer.getContext();
    try {
        var dbg = gl.getExtension("WEBGL_debug_renderer_info");
        var rstr = dbg
            ? gl.getParameter(dbg.UNMASKED_RENDERER_WEBGL)
            : gl.getParameter(gl.RENDERER);
        document.getElementById("renderer-value").textContent = String(rstr).slice(0, 60);
    } catch (e) {
        document.getElementById("renderer-value").textContent = "(unknown)";
    }

    var wgl2 = renderer.capabilities && renderer.capabilities.isWebGL2;
    document.getElementById("webgl-version-value").textContent = wgl2 ? "WebGL 2" : "WebGL 1";

    var fpsValueEl = document.getElementById("fps-value");
    var fpsFrames = 0;
    var fpsLastTime = performance.now();

    // ── viewport resize ──────────────────────────────────────────
    window.addEventListener("resize", function () {
        var w = wrap.clientWidth;
        var h = wrap.clientHeight;
        renderer.setSize(w, h);
        camera.aspect = w / h;
        camera.updateProjectionMatrix();
    });

    // ── animation loop ───────────────────────────────────────────
    function tick(now) {
        requestAnimationFrame(tick);

        // FPS
        fpsFrames++;
        if (now - fpsLastTime >= 500) {
            var fps = Math.round((fpsFrames * 1000) / (now - fpsLastTime));
            fpsValueEl.textContent = String(fps);
            fpsFrames = 0;
            fpsLastTime = now;
        }

        if (rotating) {
            knot.rotation.x = now * 0.0006;
            knot.rotation.y = now * 0.0009;

            // Color cycling on the key light to prove uniforms update.
            var hue = (now * 0.0001) % 1;
            keyLight.color.setHSL(hue, 0.7, 0.55);
            fillLight.color.setHSL((hue + 0.5) % 1, 0.7, 0.55);

            // Orbiting cubes.
            for (var i = 0; i < cubes.length; i++) {
                var c = cubes[i];
                var t = now * 0.0007 + c.userData.angle;
                var r = c.userData.baseRadius + Math.sin(now * 0.001 + c.userData.angle) * 0.3;
                c.position.set(
                    Math.cos(t) * r,
                    Math.sin(t * 1.7) * 0.5,
                    Math.sin(t) * r,
                );
                c.rotation.x = t * 1.3;
                c.rotation.y = t * 0.9;
            }

            // Slow starfield rotation.
            starfield.rotation.y = now * 0.00005;
        }

        renderer.render(scene, camera);
    }

    requestAnimationFrame(tick);

    console.log("[sky-webview-spike] scene initialised; tick loop running.");
})();
