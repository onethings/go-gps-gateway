package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/hls"
)

// TestStreamServesJT1078Output is the join between the media listener and the
// REST layer: the listener writes the files, the existing stream route serves
// them without knowing where they came from.
func TestStreamServesJT1078Output(t *testing.T) {
	root := t.TempDir()
	manager := hls.NewManager(root)

	base := time.UnixMilli(1700000000000)
	for index, keyFrame := range []bool{true, false, true} {
		manager.WriteFrame(codec.MediaFrame{
			DeviceID:  8,
			Channel:   1,
			Codec:     codec.MediaCodecH264,
			Timestamp: base.Add(time.Duration(index) * 2 * time.Second),
			KeyFrame:  keyFrame,
			Payload:   []byte{0x00, 0x00, 0x00, 0x01, 0x65, byte(index)},
		})
	}

	server := NewServer(nil, WithStreams(root))
	handler := server.Handler()

	recorder := get(t, handler, "/api/stream/8")
	if recorder.Code != http.StatusOK {
		t.Fatalf("info status = %d, body %s", recorder.Code, recorder.Body)
	}
	var info map[string]string
	decodeExtensionBody(t, recorder, &info)
	if info["url"] != "/api/stream/8/index.m3u8" {
		t.Fatalf("url = %q", info["url"])
	}

	recorder = get(t, handler, "/api/stream/8/index.m3u8")
	if recorder.Code != http.StatusOK {
		t.Fatalf("playlist status = %d, body %s", recorder.Code, recorder.Body)
	}
	playlist := recorder.Body.String()
	if !strings.Contains(playlist, "1/0.ts") {
		t.Fatalf("playlist does not name the segment:\n%s", playlist)
	}

	recorder = get(t, handler, "/api/stream/8/1/0.ts")
	if recorder.Code != http.StatusOK {
		t.Fatalf("segment status = %d, body %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "video/mp2t" {
		t.Fatalf("content type = %q", got)
	}
	body := recorder.Body.Bytes()
	if len(body) == 0 || len(body)%188 != 0 {
		t.Fatalf("segment length = %d", len(body))
	}
	if body[0] != 0x47 {
		t.Fatalf("segment does not start with a transport stream sync byte")
	}
}
