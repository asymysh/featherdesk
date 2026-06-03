"use strict";

var HEADER_SIZE = 17;
var FRAME_TYPE_VIDEO_H264 = 1;
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

function init() {
    canvas = document.getElementById("canvas");
    ctx2d = canvas.getContext("2d");
    lastStatsTime = performance.now();
    connect();
    requestAnimationFrame(updateStats);
}

function connect() {
    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    var url = proto + "//" + location.host + "/ws";
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
        if (type !== FRAME_TYPE_VIDEO_H264) return;

        var width = view.getUint16(9, true);
        var height = view.getUint16(11, true);
        var payloadSize = view.getUint32(13, true);
        var payload = new Uint8Array(ev.data, HEADER_SIZE, payloadSize);

        if (canvas.width !== width || canvas.height !== height) {
            canvas.width = width;
            canvas.height = height;
        }

        decodeFrame(payload);
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
        codec: "avc1.42E01E",
        optimizeForLatency: true
    });
}

function decodeFrame(nalData) {
    if (!decoder || decoder.state === "closed") return;

    var isKey = detectKeyframe(nalData);
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

function detectKeyframe(nal) {
    if (nal.length < 5) return false;
    var offset = 0;
    if (nal[0] === 0 && nal[1] === 0 && nal[2] === 0 && nal[3] === 1) {
        offset = 4;
    } else if (nal[0] === 0 && nal[1] === 0 && nal[2] === 1) {
        offset = 3;
    }
    var nalType = nal[offset] & 0x1F;
    return nalType === 5 || nalType === 7 || nalType === 8;
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

document.addEventListener("DOMContentLoaded", init);
