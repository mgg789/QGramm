import { CipherSuite, DhkemX25519HkdfSha256, HkdfSha256, Aes128Gcm } from "@hpke/core";
const encoder = new TextEncoder(), decoder = new TextDecoder();
const suite = new CipherSuite({ kem: new DhkemX25519HkdfSha256(), kdf: new HkdfSha256(), aead: new Aes128Gcm() });
const from64 = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const to64 = (b) => btoa(String.fromCharCode(...new Uint8Array(b)));
const hex = (b) => Array.from(new Uint8Array(b), (x) => x.toString(16).padStart(2, "0")).join("");
const binding = (chat, user, device, operation) => encoder.encode(JSON.stringify({ version: 1, chat, user, device, operation }));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function http(method, url, token, body) {
  const r = await fetch(url, { method, headers: { ...token ? { Authorization: "Bearer " + token } : {}, "Content-Type": "application/json" }, body: body === void 0 ? void 0 : JSON.stringify(body) });
  if (!r.ok) throw new Error(method + " " + new URL(url, location.href).pathname + " HTTP " + r.status);
  return r.json();
}
function fingerprint(sdp) {
  return sdp.split("\n").find((l) => l.startsWith("a=fingerprint:"))?.trim() || "";
}
async function signal(boot, keys, serverKey, chat, call, kind, operation, from, payload) {
  const sender = await suite.createSenderContext({ recipientPublicKey: serverKey, info: encoder.encode("qgramm/basic/v1") });
  const ciphertext = await sender.seal(encoder.encode(JSON.stringify(payload)), binding(chat, boot.peers[from].user, boot.peers[from].device, operation));
  const publicBytes = await suite.kem.serializePublicKey(serverKey);
  const envelope = { key_id: hex(await crypto.subtle.digest("SHA-256", publicBytes)), enc: to64(sender.enc), ciphertext: to64(ciphertext) };
  const wire = { type: kind, operation_id: operation, to_device: boot.peers[1 - from].device, envelope };
  if ("sdp" in wire || "candidate" in wire) throw new Error("plaintext signal fields");
  await http("POST", boot.base + "/v1/calls/" + call + "/signals", boot.peers[from].token, wire);
  const events = await http("GET", boot.base + "/v1/chats/" + chat + "/events?after=0", boot.peers[1 - from].token);
  for (const event of events) {
    if (event.type !== "call." + kind || event.data.operation_id !== operation) continue;
    if ("sdp" in event.data || "candidate" in event.data) throw new Error("plaintext signal event");
    const recipient = await suite.createRecipientContext({ recipientKey: keys[1 - from].privateKey, enc: from64(event.data.envelope.enc), info: encoder.encode("qgramm/basic/v1") });
    return JSON.parse(decoder.decode(await recipient.open(from64(event.data.envelope.ciphertext), binding(chat, boot.peers[1 - from].user, boot.peers[1 - from].device, operation))));
  }
  ;
  throw new Error("durable browser signaling event missing");
}
async function gather(pc, offer) {
  const sdp = offer ? await pc.createOffer() : await pc.createAnswer();
  await pc.setLocalDescription(sdp);
  if (pc.iceGatheringState !== "complete") {
    await Promise.race([new Promise((resolve) => {
      const f = () => {
        if (pc.iceGatheringState === "complete") {
          pc.removeEventListener("icegatheringstatechange", f);
          resolve();
        }
      };
      pc.addEventListener("icegatheringstatechange", f);
    }), sleep(1e4).then(() => {
      throw new Error("native ICE gathering timeout");
    })]);
  }
  ;
  return pc.localDescription.sdp;
}
async function check(boot, keys, serverKey, mode, mismatch = false, relay = false) {
  const label = mismatch ? "mismatch" : mode + (relay ? "-relay" : ""), chat = "browser-" + label;
  await http("POST", "/chat", null, { id: chat });
  const call = await http("POST", boot.base + "/v1/chats/" + chat + "/calls", boot.peers[0].token, { mode });
  const configs = await Promise.all(boot.peers.map(async peer => {
    if (!relay) return {};
    const turn = await http("GET", boot.base + "/v1/calls/turn", peer.token);
    return {iceTransportPolicy:"relay",iceServers:[{urls:turn.urls,username:turn.username,credential:turn.credential}]};
  }));
  const pcs = configs.map(config => new RTCPeerConnection(config)), streams = [], contexts = [], oscillators = [], timers = [], videos = [], rendered = [0, 0],audioMetrics=[{samples:0,energy:0},{samples:0,energy:0}];
  try {
    for (let i = 0; i < 2; i++) {
      const context = new AudioContext({sinkId:{type:"none"}});
      contexts.push(context);
      await context.resume();await context.audioWorklet.addModule("/pcm.js");
      const oscillator = context.createOscillator();
      oscillators.push(oscillator);
      oscillator.frequency.value = 440 + i * 220;
      const gain = context.createGain();
      gain.gain.value = 0.2;
      const destination = context.createMediaStreamDestination();
      oscillator.connect(gain).connect(destination);
      gain.connect(context.destination);
      oscillator.start();
      const stream = destination.stream;
      streams.push(stream);
      if (mode === "video") {
        const canvas = document.createElement("canvas");
        canvas.width = 160;
        canvas.height = 120;
        document.querySelector("#media").append(canvas);
        let n = 0;
        const paint = () => {
          const c = canvas.getContext("2d");
          c.fillStyle = n++ % 2 ? "#e14030" : "#206ce0";
          c.fillRect(0, 0, 160, 120);
          c.fillStyle = "#fff";
          c.fillRect(n % 140, 15, 20, 80);
        };
        paint();
        timers.push(setInterval(paint, 50));
        for (const track of canvas.captureStream(20).getVideoTracks()) stream.addTrack(track);
      }
      ;
      for (const track of stream.getTracks()) pcs[i].addTrack(track, stream);
      pcs[i].ontrack = (e) => {
        if(e.track.kind==="audio"){
          const audio=document.createElement("audio");audio.autoplay=true;audio.srcObject=new MediaStream([e.track]);document.querySelector("#media").append(audio);audio.play().catch(()=>{});
          const source=context.createMediaStreamSource(new MediaStream([e.track]));
          const receiver=new AudioWorkletNode(context,"receiver-pcm");receiver.port.onmessage=event=>{audioMetrics[i]=event.data};
          source.connect(receiver).connect(context.destination);
        }
        if (e.track.kind === "video") {
          const video = document.createElement("video");
          video.autoplay = true;
          video.muted = true;
          video.playsInline = true;
          video.srcObject = new MediaStream([e.track]);
          document.querySelector("#media").append(video);
          videos[i] = video;
          const count = () => {
            rendered[i]++;
            video.requestVideoFrameCallback(count);
          };
          video.requestVideoFrameCallback(count);
          video.play().catch(() => {
          });
        }
      };
    }
    const offer = await gather(pcs[0], true);
    const receivedOffer = await signal(boot, keys, serverKey, chat, call.id, "offer", label + "-offer", 0, { sdp: offer });
    if (receivedOffer.sdp !== offer || !fingerprint(offer)) throw new Error("browser offer fingerprint differs after HPKE");
    await pcs[1].setRemoteDescription({ type: "offer", sdp: receivedOffer.sdp });
    await http("POST", boot.base + "/v1/calls/" + call.id + "/signals", boot.peers[1].token, { type: "accept", operation_id: label + "-accept" });
    const answer = await gather(pcs[1], false);
    const sendAnswer = mismatch ? answer.replace(/a=fingerprint:sha-256 [^\r\n]+/g, "a=fingerprint:sha-256 " + Array(32).fill("00").join(":")) : answer;
    const receivedAnswer = await signal(boot, keys, serverKey, chat, call.id, "answer", label + "-answer", 1, { sdp: sendAnswer });
    if (receivedAnswer.sdp !== sendAnswer) throw new Error("browser answer differs after HPKE");
    await pcs[0].setRemoteDescription({ type: "answer", sdp: receivedAnswer.sdp });
    for (let i = 0; i < 2; i++) {
      const candidate = pcs[i].localDescription.sdp.split("\n").map((x) => x.trim()).find((x) => x.startsWith("a=candidate:"));
      if (!candidate) throw new Error("browser ICE candidate absent");
      const got = await signal(boot, keys, serverKey, chat, call.id, "ice", label + "-ice" + i, i, { candidate: candidate.slice(2), sdp_mid: "0" });
      await pcs[1 - i].addIceCandidate({ candidate: got.candidate, sdpMid: "0" });
    }
    if (mismatch) {
      const deadline2 = Date.now() + 15e3;
      while (Date.now() < deadline2 && pcs[0].connectionState !== "failed") await sleep(100);
      if (pcs[0].connectionState !== "failed") throw new Error("tampered DTLS fingerprint did not produce failed transport");
      const stats = await pcs[0].getStats();
      let received = 0;
      stats.forEach((x) => {
        if (x.type === "inbound-rtp") received += x.bytesReceived || 0;
      });
      if (received !== 0) throw new Error("tampered fingerprint allowed media");
      return { mode: "video", case: "fingerprint-mismatch", accepted:true,fingerprintMismatchRejected: true, connectionState: pcs[0].connectionState, mediaBytesReceived: received };
    }
    const deadline = Date.now() + 12e3;
    let summaries;
    do {
      await sleep(200);
      summaries = [];
      for (let i = 0; i < 2; i++) {
        const stats = await pcs[i].getStats();
        const summary2 = { connectionState: pcs[i].connectionState, iceConnectionState: pcs[i].iceConnectionState, mdnsCandidates: pcs[i].localDescription.sdp.includes(".local"), candidates: pcs[i].localDescription.sdp.split("\n").filter((x) => x.startsWith("a=candidate:")).map((x) => ({ protocol: x.split(" ")[2], loopback: x.split(" ")[4].startsWith("127.") })), audioContextTime:contexts[i].currentTime,audioContextState:contexts[i].state,audioSamples: audioMetrics[i].samples, audioEnergy: audioMetrics[i].energy, videoFramesDecoded: 0, videoFramesRendered: rendered[i], videoWidth: videos[i]?.videoWidth || 0 };
        stats.forEach((x) => {
          if(x.type==="media-source"&&x.kind==="audio"){summary2.senderSourceAudioLevel=x.audioLevel;summary2.senderSourceAudioEnergy=x.totalAudioEnergy}
          if(x.type==="outbound-rtp"){summary2.sentPackets=(summary2.sentPackets||0)+(x.packetsSent||0)}
          if(x.type==="inbound-rtp"){
            (summary2.inbound ||= []).push({kind:x.kind,mediaType:x.mediaType,packets:x.packetsReceived,bytes:x.bytesReceived,samples:x.totalSamplesReceived,energy:x.totalAudioEnergy,framesDecoded:x.framesDecoded});
          }
          if (x.type === "candidate-pair") {
            summary2.candidatePair = { state: x.state, requestsSent: x.requestsSent, requestsReceived: x.requestsReceived, responsesReceived: x.responsesReceived };
          }
          ;
          if (x.type === "inbound-rtp" && x.kind === "audio") {
            summary2.inboundAudioStatsSamples = x.totalSamplesReceived || 0;summary2.audioRTPBytes=x.bytesReceived||0;
            summary2.inboundAudioStatsEnergy = x.totalAudioEnergy || 0;
          }
          ;
          if (x.type === "inbound-rtp" && x.kind === "video") summary2.videoFramesDecoded = x.framesDecoded || 0;
        });
        const transport = [...stats.values()].find(x => x.type === "transport" && x.selectedCandidatePairId);
        const selected = transport && stats.get(transport.selectedCandidatePairId);
        if(selected)summary2.selectedCandidatePair={state:selected.state,localCandidateType:stats.get(selected.localCandidateId)?.candidateType,remoteCandidateType:stats.get(selected.remoteCandidateId)?.candidateType};
        summaries.push(summary2);
      }
      ;
      if (summaries.every((s) => s.connectionState === "connected" && s.audioSamples > 1e3 && s.audioEnergy > 0 && s.audioRTPBytes>1000 && (mode !== "video" || s.videoFramesDecoded >= 5 && s.videoFramesRendered >= 5 && s.videoWidth === 160))) break;
    } while (Date.now() < deadline);
    const audioDecodedVerified=summaries.every(s=>s.connectionState==="connected"&&s.audioSamples>1000&&s.audioEnergy>0&&s.audioRTPBytes>1000);
    const videoDecodedVerified=mode==="video"&&summaries.every(s=>s.connectionState==="connected"&&s.videoFramesDecoded>=5&&s.videoFramesRendered>=5&&s.videoWidth===160);
    const relaySelectedVerified=relay&&summaries.every(s=>s.selectedCandidatePair?.state==="succeeded"&&s.selectedCandidatePair.localCandidateType==="relay"&&s.selectedCandidatePair.remoteCandidateType==="relay");
    return { mode, case: relay ? "turn-relay" : "direct",accepted:audioDecodedVerified&&(mode!=="video"||videoDecodedVerified)&&(!relay||relaySelectedVerified),relaySelectedVerified,audioDecodedVerified,videoDecodedVerified,fingerprintBindingVerified: fingerprint(receivedOffer.sdp) === fingerprint(offer) && fingerprint(receivedAnswer.sdp) === fingerprint(answer), peers: summaries };
  } finally {
    for (const t of timers) clearInterval(t);
    for (const o of oscillators) o.stop();
    for (const s of streams) for (const track of s.getTracks()) track.stop();
    for (const pc of pcs) pc.close();
    for (const context of contexts) await context.close();
    await http("POST", boot.base + "/v1/calls/" + call.id + "/signals", boot.peers[0].token, { type: "end", operation_id: label + "-end" });
  }
}
document.querySelector("#run").addEventListener("click", async () => {
  document.querySelector("#run").disabled = true;
  window.results = [];
  try {
    const boot = await http("GET", "/bootstrap");
    const keys = [await suite.kem.generateKeyPair(), await suite.kem.generateKeyPair()];
    await http("POST", "/register", null, await Promise.all(keys.map(async (k) => to64(await suite.kem.serializePublicKey(k.publicKey)))));
    const caps = await http("GET", boot.base + "/v1/capabilities", boot.peers[0].token);
    const serverKey = await suite.kem.deserializePublicKey(from64(caps.server_key));
    for (const mode of ["audio", "video"]){try{window.results.push(await check(boot,keys,serverKey,mode))}catch(error){window.results.push({mode,accepted:false,error:String(error)})}}
    if(boot.turnEnabled)for(const mode of ["audio","video"]){try{window.results.push(await check(boot,keys,serverKey,mode,false,true))}catch(error){window.results.push({mode,case:"turn-relay",accepted:false,error:String(error)})}}
    try{window.results.push(await check(boot,keys,serverKey,"video",true))}catch(error){window.results.push({case:"fingerprint-mismatch",accepted:false,error:String(error)})}
    document.querySelector("#results").textContent = JSON.stringify(window.results, null, 2);
  } catch (e) {
    window.testError = String(e);
    document.querySelector("#results").textContent = window.testError;
  } finally {
    window.finished = true;
  }
});
