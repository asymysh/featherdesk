"use strict";

var HEADER_SIZE = 17;
var FRAME_TYPE_VIDEO_H264 = 1;
var FRAME_TYPE_AUDIO_PCM = 4;
var RECONNECT_DELAY = 2000;

var ws = null;
var decoder = null;
var canvas = null;
var ctx2d = null;
var connected = false;
var frameCount = 0;
var byteCount = 0;
var lastStatsTime = 0;
var fpsDisplay = 0;
var bpsDisplay = 0;
var audioCtx = null;
var audioWorklet = null;
var audioStarted = false;

function init() {
    canvas = document.getElementById("canvas");
    ctx2d = canvas.getContext("2d");

    if (typeof VideoDecoder === "undefined") {
        document.getElementById("info").textContent =
            "WebCodecs unavailable. Use HTTPS or localhost.";
        document.querySelector("#status .dot").className = "dot disconnected";
        return;
    }

    lastStatsTime = performance.now();
    connect();
    requestAnimationFrame(updateStats);
}

function connect() {
    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    var url = proto + "//" + location.host + "/ws?role=control";
    ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";

    ws.onopen = function() {
        connected = true;
        updateStatus();
        initDecoder();
    };

    ws.onmessage = function(ev) {
        if (!(ev.data instanceof ArrayBuffer)) return;
        var buf = new Uint8Array(ev.data);
        byteCount += buf.byteLength;
        if (buf.byteLength < HEADER_SIZE) return;

        var view = new DataView(ev.data);
        var type = view.getUint8(0);

        if (type === FRAME_TYPE_VIDEO_H264) {
            var width = view.getUint16(9, true);
            var height = view.getUint16(11, true);
            var payloadSize = view.getUint32(13, true);
            var payload = new Uint8Array(ev.data, HEADER_SIZE, payloadSize);

            if (canvas.width !== width || canvas.height !== height) {
                canvas.width = width;
                canvas.height = height;
            }
            decodeFrame(payload);
        } else if (type === FRAME_TYPE_AUDIO_PCM) {
            var payloadSize = view.getUint32(13, true);
            var payload = new Uint8Array(ev.data, HEADER_SIZE, payloadSize);
            playAudio(payload);
        }
    };

    ws.onclose = function() {
        connected = false;
        updateStatus();
        if (decoder) {
            decoder.close();
            decoder = null;
        }
        setTimeout(connect, RECONNECT_DELAY);
    };

    ws.onerror = function() {
        ws.close();
    };
}

function initDecoder() {
    if (decoder) {
        decoder.close();
    }
    decoder = new VideoDecoder({
        output: function(frame) {
            ctx2d.drawImage(frame, 0, 0);
            frame.close();
            frameCount++;
        },
        error: function(e) {
            console.error("decoder error:", e.message);
        }
    });
    decoder.configure({
        codec: "avc1.640033",
        optimizeForLatency: true
    });
}

function decodeFrame(nalData) {
    if (!decoder || decoder.state === "closed") return;

    var nalType = getNalType(nalData);
    var isKey = (nalType === 5 || nalType === 7);

    var chunk = new EncodedVideoChunk({
        type: isKey ? "key" : "delta",
        timestamp: performance.now() * 1000,
        data: nalData
    });

    try {
        decoder.decode(chunk);
    } catch (e) {
        initDecoder();
    }
}

function getNalType(nal) {
    if (nal.length < 5) return -1;
    var offset = 0;
    if (nal[0] === 0 && nal[1] === 0 && nal[2] === 0 && nal[3] === 1) {
        offset = 4;
    } else if (nal[0] === 0 && nal[1] === 0 && nal[2] === 1) {
        offset = 3;
    }
    return nal[offset] & 0x1F;
}

function detectKeyframe(nal) {
    var t = getNalType(nal);
    return t === 5 || t === 7 || t === 8;
}

function updateStats() {
    var now = performance.now();
    var elapsed = now - lastStatsTime;
    if (elapsed >= 1000) {
        fpsDisplay = Math.round(frameCount * 1000 / elapsed);
        bpsDisplay = Math.round(byteCount * 8000 / elapsed);
        frameCount = 0;
        byteCount = 0;
        lastStatsTime = now;
        updateStatus();
    }
    requestAnimationFrame(updateStats);
}

var WORKLET_CODE = 'class PCMProcessor extends AudioWorkletProcessor {\n' +
    '  constructor() { super(); this.buf = new Float32Array(0);\n' +
    '    this.port.onmessage = (e) => {\n' +
    '      var old = this.buf;\n' +
    '      var added = e.data;\n' +
    '      if (old.length > 48000) { old = old.slice(old.length - 24000); }\n' +
    '      var merged = new Float32Array(old.length + added.length);\n' +
    '      merged.set(old); merged.set(added, old.length);\n' +
    '      this.buf = merged;\n' +
    '    };\n' +
    '  }\n' +
    '  process(inputs, outputs) {\n' +
    '    var out = outputs[0];\n' +
    '    var needed = out[0].length;\n' +
    '    var channels = out.length;\n' +
    '    var samplesNeeded = needed * channels;\n' +
    '    if (this.buf.length < samplesNeeded) {\n' +
    '      for (var ch = 0; ch < channels; ch++) out[ch].fill(0);\n' +
    '      return true;\n' +
    '    }\n' +
    '    var consumed = this.buf.slice(0, samplesNeeded);\n' +
    '    this.buf = this.buf.slice(samplesNeeded);\n' +
    '    for (var i = 0; i < needed; i++) {\n' +
    '      for (var ch = 0; ch < channels; ch++) {\n' +
    '        out[ch][i] = consumed[i * channels + ch];\n' +
    '      }\n' +
    '    }\n' +
    '    return true;\n' +
    '  }\n' +
    '}\n' +
    'registerProcessor("pcm-processor", PCMProcessor);\n';

function initAudio() {
    if (audioStarted) return;
    audioStarted = true;
    audioCtx = new AudioContext({sampleRate: 48000});
    audioCtx.resume().then(function() {
        var blob = new Blob([WORKLET_CODE], {type: "application/javascript"});
        var url = URL.createObjectURL(blob);
        audioCtx.audioWorklet.addModule(url).then(function() {
            audioWorklet = new AudioWorkletNode(audioCtx, "pcm-processor", {
                outputChannelCount: [2]
            });
            audioWorklet.connect(audioCtx.destination);
            URL.revokeObjectURL(url);
        }).catch(function(e) {
            console.error("audioWorklet load failed:", e);
        });
    });
}

function playAudio(s16Data) {
    if (!audioWorklet) return;
    if (audioCtx.state === "suspended") {
        audioCtx.resume();
        return;
    }
    var samples = s16Data.length / 2;
    var floats = new Float32Array(samples);
    var view = new DataView(s16Data.buffer, s16Data.byteOffset, s16Data.byteLength);
    for (var i = 0; i < samples; i++) {
        floats[i] = view.getInt16(i * 2, true) / 32768.0;
    }
    audioWorklet.port.postMessage(floats, [floats.buffer]);
}

function updateStatus() {
    var dot = document.querySelector("#status .dot");
    var info = document.getElementById("info");
    if (connected) {
        dot.className = "dot connected";
        var bw = bpsDisplay > 1000000
            ? (bpsDisplay / 1000000).toFixed(1) + " Mbps"
            : Math.round(bpsDisplay / 1000) + " kbps";
        info.textContent = fpsDisplay + " fps | " + bw;
    } else {
        dot.className = "dot disconnected";
        info.textContent = "reconnecting...";
    }
}

function sendInput(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(msg));
    }
}

function initInput() {
    document.addEventListener("keydown", function(e) {
        if (!connected) return;
        e.preventDefault();
        initAudio();
        sendInput({type: "key", event: "down", code: e.code});
    });

    document.addEventListener("keyup", function(e) {
        if (!connected) return;
        e.preventDefault();
        sendInput({type: "key", event: "up", code: e.code});
    });

    canvas.addEventListener("pointermove", function(e) {
        if (!connected) return;
        var rect = canvas.getBoundingClientRect();
        var scaleX = canvas.width / rect.width;
        var scaleY = canvas.height / rect.height;
        var scale = Math.max(scaleX, scaleY);
        var renderedW = canvas.width / scale;
        var renderedH = canvas.height / scale;
        var offsetX = (rect.width - renderedW) / 2;
        var offsetY = (rect.height - renderedH) / 2;
        var x = Math.round((e.clientX - rect.left - offsetX) / renderedW * canvas.width);
        var y = Math.round((e.clientY - rect.top - offsetY) / renderedH * canvas.height);
        x = Math.max(0, Math.min(canvas.width - 1, x));
        y = Math.max(0, Math.min(canvas.height - 1, y));
        sendInput({type: "mousemove", x: x, y: y});
    });

    canvas.addEventListener("pointerdown", function(e) {
        if (!connected) return;
        e.preventDefault();
        canvas.setPointerCapture(e.pointerId);
        initAudio();
        sendInput({type: "mousedown", button: e.button});
    });

    canvas.addEventListener("pointerup", function(e) {
        if (!connected) return;
        e.preventDefault();
        canvas.releasePointerCapture(e.pointerId);
        sendInput({type: "mouseup", button: e.button});
    });

    canvas.addEventListener("wheel", function(e) {
        if (!connected) return;
        e.preventDefault();
        sendInput({type: "wheel", deltaY: e.deltaY});
    }, {passive: false});

    canvas.addEventListener("contextmenu", function(e) {
        e.preventDefault();
    });
}

function init() {
    canvas = document.getElementById("canvas");
    ctx2d = canvas.getContext("2d");
    lastStatsTime = performance.now();
    connect();
    initInput();
    requestAnimationFrame(updateStats);
}

document.addEventListener("DOMContentLoaded", init);
