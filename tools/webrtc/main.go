// Integration-only executable, not a shipped client SDK.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type client struct {
	base, token, user, device string
	http                      *http.Client
	engine                    *cryptoenc.Engine
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return b
}
func (c client) request(method, path string, v, out any) error {
	data, e := json.Marshal(v)
	if e != nil {
		return e
	}
	r, e := http.NewRequest(method, c.base+path, bytes.NewReader(data))
	if e != nil {
		return e
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	res, e := c.http.Do(r)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if e != nil {
		return e
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s HTTP %d", method, path, res.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "WebRTC acceptance failed:", e)
		os.Exit(1)
	}
}
func run() error {
	tmp, e := os.MkdirTemp("", "qgramm-webrtc-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	pub, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	management := base64.StdEncoding.EncodeToString(random(32))
	if os.Getenv("QGRAMM_TEST_TURN") == "" {
		return errors.New("isolated TURN secret missing")
	}
	cfg := fmt.Sprintf("[server]\nlisten=\"127.0.0.1:8080\"\nallow_insecure_loopback=true\n[storage]\npath=%q\nfiles=%q\n[features]\ncalls=true\n[calls]\nturn_urls=[\"turn:qgturn:3478?transport=udp\"]\nturn_secret_env=\"QGRAMM_TEST_TURN\"\n", filepath.Join(tmp, "qg.db"), filepath.Join(tmp, "files"))
	path := filepath.Join(tmp, "calls.toml")
	if e = os.WriteFile(path, []byte(cfg), 0600); e != nil {
		return e
	}
	cmd := exec.Command("/usr/local/bin/qgramm-calls", "-config", path)
	cmd.Env = append(os.Environ(), "QGRAMM_TOKEN_PUBLIC_KEY="+base64.StdEncoding.EncodeToString(pub), "QGRAMM_MASTER_KEY="+base64.StdEncoding.EncodeToString(random(32)), "QGRAMM_HPKE_KEY="+base64.StdEncoding.EncodeToString(random(32)), "QGRAMM_MANAGEMENT_SECRET="+management)
	if e = cmd.Start(); e != nil {
		return e
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	hc := &http.Client{Timeout: 10 * time.Second}
	base := "http://127.0.0.1:8080"
	ready := false
	for i := 0; i < 100; i++ {
		r, e := hc.Get(base + "/readyz")
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		return errors.New("calls binary not ready")
	}
	admin := client{base: base, token: management, http: hc}
	peers := make([]client, 2)
	for i, user := range []string{"alice", "bob"} {
		engine, e := cryptoenc.New(random(32), random(32))
		if e != nil {
			return e
		}
		device := user + "-phone"
		token, e := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"sub": user, "device_id": device, "iss": "qgramm", "aud": "qgramm", "iat": time.Now().Unix(), "exp": time.Now().Add(10 * time.Minute).Unix()}).SignedString(private)
		if e != nil {
			return e
		}
		peers[i] = client{base: base, token: token, user: user, device: device, http: hc, engine: engine}
		if e = admin.request("PUT", "/management/v1/users/"+user, map[string]any{}, nil); e != nil {
			return e
		}
		if e = admin.request("PUT", "/management/v1/users/"+user+"/devices/"+device, map[string]string{"public_key": engine.PublicKey()}, nil); e != nil {
			return e
		}
	}
	var caps struct {
		Key string `json:"server_key"`
	}
	if e = peers[0].request("GET", "/v1/capabilities", nil, &caps); e != nil {
		return e
	}
	serverKey, e := base64.StdEncoding.DecodeString(caps.Key)
	if e != nil {
		return e
	}
	video, audio, e := frames()
	if e != nil {
		return e
	}
	fmt.Printf("runtime=%s/%s go=%s encoded_vp8_frames=%d encoded_opus_frames=%d\n", runtime.GOOS, runtime.GOARCH, runtime.Version(), len(video), len(audio))
	for _, relay := range []bool{false, true} {
		for _, mode := range []string{"audio", "video"} {
			if e = pair(admin, peers, serverKey, relay, mode, video, audio); e != nil {
				return e
			}
		}
	}
	return nil
}
func frames() (video, audio [][]byte, err error) {
	v, e := os.Open("/tmp/video.ivf")
	if e != nil {
		return nil, nil, e
	}
	defer v.Close()
	ivf, _, e := ivfreader.NewWith(v)
	if e != nil {
		return nil, nil, e
	}
	for {
		b, _, e := ivf.ParseNextFrame()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, nil, e
		}
		video = append(video, b)
	}
	a, e := os.Open("/tmp/audio.ogg")
	if e != nil {
		return nil, nil, e
	}
	defer a.Close()
	ogg, _, e := oggreader.NewWith(a)
	if e != nil {
		return nil, nil, e
	}
	for {
		b, _, e := ogg.ParseNextPage()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, nil, e
		}
		if !bytes.HasPrefix(b, []byte("OpusTags")) && !bytes.HasPrefix(b, []byte("OpusHead")) {
			audio = append(audio, b)
		}
	}
	if len(video) == 0 || len(audio) == 0 {
		return nil, nil, errors.New("encoded media missing")
	}
	return video, audio, nil
}
func pair(admin client, peers []client, key []byte, relay bool, mode string, video, audio [][]byte) error {
	label := mode + "-direct"
	if relay {
		label = mode + "-relay"
	}
	chat := "media-" + label
	if e := admin.request("POST", "/management/v1/chats/direct", map[string]any{"id": chat, "mode": "basic", "members": []string{"alice", "bob"}}, nil); e != nil {
		return e
	}
	var call struct {
		ID string `json:"id"`
	}
	if e := peers[0].request("POST", "/v1/chats/"+chat+"/calls", map[string]string{"mode": mode}, &call); e != nil {
		return e
	}
	configs := make([]webrtc.Configuration, 2)
	if relay {
		for i := range peers {
			var turn struct {
				URLs       []string `json:"urls"`
				Username   string   `json:"username"`
				Credential string   `json:"credential"`
			}
			if e := peers[i].request("GET", "/v1/calls/turn", nil, &turn); e != nil {
				return e
			}
			configs[i].ICETransportPolicy = webrtc.ICETransportPolicyRelay
			configs[i].ICEServers = []webrtc.ICEServer{{URLs: turn.URLs, Username: turn.Username, Credential: turn.Credential}}
		}
	}
	pcs := make([]*webrtc.PeerConnection, 2)
	var mu sync.Mutex
	counts := map[string]int{}
	var readErr error
	for i := range pcs {
		pc, e := webrtc.NewPeerConnection(configs[i])
		if e != nil {
			return e
		}
		pcs[i] = pc
		defer pc.Close()
		index := i
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			var dep rtp.Depacketizer = &codecs.OpusPacket{}
			source := audio
			name := fmt.Sprintf("peer%d/audio", index)
			clock := uint32(48000)
			if track.Kind() == webrtc.RTPCodecTypeVideo {
				dep = &codecs.VP8Packet{}
				source = video
				name = fmt.Sprintf("peer%d/video", index)
				clock = 90000
			}
			builder := samplebuilder.New(32, dep, clock)
			for {
				p, _, e := track.ReadRTP()
				if e != nil {
					return
				}
				builder.Push(p)
				for {
					s := builder.Pop()
					if s == nil {
						break
					}
					match := false
					for _, f := range source {
						if bytes.Equal(f, s.Data) {
							match = true
							break
						}
					}
					mu.Lock()
					if match {
						counts[name]++
					} else {
						readErr = fmt.Errorf("%s decoded RTP payload differs from source", name)
					}
					mu.Unlock()
				}
			}
		})
	}
	type tracks struct {
		a, v *webrtc.TrackLocalStaticSample
	}
	locals := make([]tracks, 2)
	kinds := []string{"audio"}
	if mode == "video" {
		kinds = append(kinds, "video")
	}
	for i, pc := range pcs {
		for _, kind := range kinds {
			mime := webrtc.MimeTypeOpus
			if kind == "video" {
				mime = webrtc.MimeTypeVP8
			}
			track, e := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: mime}, kind, "qg-acceptance")
			if e != nil {
				return e
			}
			sender, e := pc.AddTrack(track)
			if e != nil {
				return e
			}
			go func() {
				b := make([]byte, 1500)
				for {
					if _, _, e := sender.Read(b); e != nil {
						return
					}
				}
			}()
			if kind == "audio" {
				locals[i].a = track
			} else {
				locals[i].v = track
			}
		}
	}
	gatherSDP := func(pc *webrtc.PeerConnection, offer bool) (webrtc.SessionDescription, error) {
		var d webrtc.SessionDescription
		var e error
		if offer {
			d, e = pc.CreateOffer(nil)
		} else {
			d, e = pc.CreateAnswer(nil)
		}
		if e != nil {
			return d, e
		}
		done := webrtc.GatheringCompletePromise(pc)
		if e = pc.SetLocalDescription(d); e != nil {
			return d, e
		}
		select {
		case <-done:
			return *pc.LocalDescription(), nil
		case <-time.After(15 * time.Second):
			return d, errors.New("ICE gathering timeout")
		}
	}
	offer, e := gatherSDP(pcs[0], true)
	if e != nil {
		return e
	}
	got, e := signal(peers[0], peers[1], key, chat, call.ID, "offer", label+"-offer", map[string]string{"sdp": offer.SDP})
	if e != nil {
		return e
	}
	if got != offer.SDP || fingerprint(got) == "" {
		return errors.New("offer fingerprint not bound")
	}
	if e = pcs[1].SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: got}); e != nil {
		return e
	}
	if e = peers[1].request("POST", "/v1/calls/"+call.ID+"/signals", map[string]string{"type": "accept", "operation_id": label + "-accept"}, nil); e != nil {
		return e
	}
	answer, e := gatherSDP(pcs[1], false)
	if e != nil {
		return e
	}
	got, e = signal(peers[1], peers[0], key, chat, call.ID, "answer", label+"-answer", map[string]string{"sdp": answer.SDP})
	if e != nil {
		return e
	}
	if got != answer.SDP || fingerprint(got) == "" {
		return errors.New("answer fingerprint not bound")
	}
	if e = pcs[0].SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: got}); e != nil {
		return e
	}
	for i, pc := range pcs {
		for _, line := range strings.Split(pc.LocalDescription().SDP, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "a=candidate:") {
				candidate := strings.TrimPrefix(line, "a=")
				got, e := signal(peers[i], peers[1-i], key, chat, call.ID, "ice", fmt.Sprintf("%s-ice%d", label, i), map[string]string{"candidate": candidate, "sdp_mid": "0"})
				if e != nil {
					return e
				}
				mid := "0"
				if e = pcs[1-i].AddICECandidate(webrtc.ICECandidateInit{Candidate: got, SDPMid: &mid}); e != nil {
					return e
				}
				break
			}
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		connected := pcs[0].ConnectionState() == webrtc.PeerConnectionStateConnected && pcs[1].ConnectionState() == webrtc.PeerConnectionStateConnected
		if connected {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s DTLS connection timeout: %s/%s", label, pcs[0].ConnectionState(), pcs[1].ConnectionState())
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i, pc := range pcs {
		selected, e := pc.GetTransceivers()[0].Sender().Transport().ICETransport().GetSelectedCandidatePair()
		if e != nil || selected == nil {
			return errors.New("selected ICE pair absent")
		}
		if relay && (selected.Local.Typ != webrtc.ICECandidateTypeRelay || selected.Remote.Typ != webrtc.ICECandidateTypeRelay) {
			return errors.New("force relay selected nonrelay candidate")
		}
		fmt.Printf("%s peer%d selected_pair=%s/%s dtls_srtp=connected fingerprint_binding=verified\n", label, i, selected.Local.Typ, selected.Remote.Typ)
	}
	for frame := 0; frame < 100; frame++ {
		for _, tracks := range locals {
			if e = tracks.a.WriteSample(media.Sample{Data: audio[frame%len(audio)], Duration: 20 * time.Millisecond}); e != nil {
				return e
			}
			if frame%5 == 0 && tracks.v != nil {
				if e = tracks.v.WriteSample(media.Sample{Data: video[(frame/5)%len(video)], Duration: 100 * time.Millisecond}); e != nil {
					return e
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if readErr != nil {
		return readErr
	}
	for _, name := range func() []string {
		keys := []string{"peer0/audio", "peer1/audio"}
		if mode == "video" {
			keys = append(keys, "peer0/video", "peer1/video")
		}
		return keys
	}() {
		if counts[name] < 5 {
			return fmt.Errorf("%s insufficient %s frames: %d", label, name, counts[name])
		}
		fmt.Printf("%s %s reconstructed_source_frames=%d\n", label, name, counts[name])
	}
	return peers[0].request("POST", "/v1/calls/"+call.ID+"/signals", map[string]string{"type": "end", "operation_id": label + "-end"}, nil)
}
func fingerprint(sdp string) string {
	for _, line := range strings.Split(sdp, "\n") {
		if strings.HasPrefix(line, "a=fingerprint:") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
func signal(from, to client, key []byte, chat, call, kind, op string, payload map[string]string) (string, error) {
	plain, e := json.Marshal(payload)
	if e != nil {
		return "", e
	}
	env, e := cryptoenc.SealEnvelope(key, plain, cryptoenc.Binding(chat, from.user, from.device, op))
	if e != nil {
		return "", e
	}
	wire := map[string]any{"type": kind, "operation_id": op, "to_device": to.device, "envelope": env}
	encoded, _ := json.Marshal(wire)
	if bytes.Contains(encoded, []byte(`"sdp":`)) || bytes.Contains(encoded, []byte(`"candidate":`)) {
		return "", errors.New("signal HTTP body contains plaintext media fields")
	}
	for field, value := range payload {
		if field == "sdp_mid" {
			continue
		}
		if bytes.Contains(encoded, []byte(value)) {
			return "", errors.New("signal HTTP body exposes media content")
		}
	}
	if e = from.request("POST", "/v1/calls/"+call+"/signals", wire, nil); e != nil {
		return "", e
	}
	var events []struct {
		Type string `json:"type"`
		Data struct {
			Operation string             `json:"operation_id"`
			Envelope  cryptoenc.Envelope `json:"envelope"`
		} `json:"data"`
	}
	var eventWire json.RawMessage
	if e = to.request("GET", "/v1/chats/"+chat+"/events?after=0", nil, &eventWire); e != nil {
		return "", e
	}
	if bytes.Contains(eventWire, []byte(`"sdp":`)) || bytes.Contains(eventWire, []byte(`"candidate":`)) {
		return "", errors.New("event HTTP body exposes plaintext media fields")
	}
	if e = json.Unmarshal(eventWire, &events); e != nil {
		return "", e
	}
	for _, event := range events {
		if event.Type != "call."+kind || event.Data.Operation != op {
			continue
		}
		raw, e := to.engine.OpenEnvelope(event.Data.Envelope, cryptoenc.Binding(chat, to.user, to.device, op))
		if e != nil {
			return "", e
		}
		var recovered map[string]string
		if e = json.Unmarshal(raw, &recovered); e != nil {
			return "", e
		}
		if kind == "ice" {
			return recovered["candidate"], nil
		}
		return recovered["sdp"], nil
	}
	return "", errors.New("durable signal missing")
}
