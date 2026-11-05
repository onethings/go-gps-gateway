package hls

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
)

func TestCRC32MPEG2(t *testing.T) {
	if got := crc32MPEG2([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("crc = %#x, want 0x0376e6e7", got)
	}
}

func TestTransportStreamLayout(t *testing.T) {
	var writer tsWriter
	out := writer.write(nil, []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88}, 0, true, codec.MediaCodecH264)

	if len(out)%tsPacketSize != 0 {
		t.Fatalf("length = %d, not a multiple of %d", len(out), tsPacketSize)
	}
	for offset := 0; offset < len(out); offset += tsPacketSize {
		if out[offset] != 0x47 {
			t.Fatalf("packet at %d starts with %#x", offset, out[offset])
		}
	}

	pat := out[:tsPacketSize]
	if pid := int(pat[1]&0x1f)<<8 | int(pat[2]); pid != 0 {
		t.Fatalf("PAT pid = %d", pid)
	}
	if crc32MPEG2(pat[5:21]) != 0 {
		t.Fatal("PAT crc does not verify")
	}

	pmt := out[tsPacketSize : 2*tsPacketSize]
	if pid := int(pmt[1]&0x1f)<<8 | int(pmt[2]); pid != pmtPID {
		t.Fatalf("PMT pid = %d, want %d", pid, pmtPID)
	}
	if pmt[17] != streamTypeH264 {
		t.Fatalf("stream type = %#x, want H.264", pmt[17])
	}
	if crc32MPEG2(pmt[5:26]) != 0 {
		t.Fatal("PMT crc does not verify")
	}

	video := out[2*tsPacketSize:]
	if pid := int(video[1]&0x1f)<<8 | int(video[2]); pid != videoPID {
		t.Fatalf("video pid = %d, want %d", pid, videoPID)
	}
	if video[1]&0x40 == 0 {
		t.Fatal("first video packet does not start a payload unit")
	}
	if !bytes.Contains(video, []byte{0x00, 0x00, 0x01, 0xe0}) {
		t.Fatal("PES start code missing")
	}
	if !bytes.Contains(video, []byte{0x00, 0x00, 0x00, 0x01, 0x09}) {
		t.Fatal("access unit delimiter missing")
	}
	if !bytes.Contains(video, []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88}) {
		t.Fatal("payload missing")
	}
}

func TestTransportStreamH265(t *testing.T) {
	var writer tsWriter
	out := writer.write(nil, []byte{0x00, 0x00, 0x00, 0x01, 0x26, 0x01}, 0, true, codec.MediaCodecH265)

	pmt := out[tsPacketSize : 2*tsPacketSize]
	if pmt[17] != streamTypeH265 {
		t.Fatalf("stream type = %#x, want H.265", pmt[17])
	}
	if !bytes.Contains(out[2*tsPacketSize:], []byte{0x00, 0x00, 0x00, 0x01, 0x46}) {
		t.Fatal("H.265 access unit delimiter missing")
	}
}

func TestPlaylist(t *testing.T) {
	data := playlist([]segmentInfo{
		{sequence: 3, duration: 2 * time.Second},
		{sequence: 4, duration: 1500 * time.Millisecond},
	}, "1/")

	text := string(data)
	for _, want := range []string{
		"#EXTM3U\n",
		"#EXT-X-TARGETDURATION:2\n",
		"#EXT-X-MEDIA-SEQUENCE:3\n",
		"#EXTINF:2.000,\n1/3.ts\n",
		"#EXTINF:1.500,\n1/4.ts\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("playlist missing %q:\n%s", want, text)
		}
	}
}

func mediaFrame(deviceID int64, keyFrame bool, at time.Time, payload string) codec.MediaFrame {
	return codec.MediaFrame{
		DeviceID:  deviceID,
		Channel:   1,
		Codec:     codec.MediaCodecH264,
		Timestamp: at,
		KeyFrame:  keyFrame,
		Payload:   []byte(payload),
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestManagerWritesSegmentsAndPlaylists(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)

	base := time.UnixMilli(1700000000000)
	manager.WriteFrame(mediaFrame(7, true, base, "idr-a"))
	manager.WriteFrame(mediaFrame(7, false, base.Add(500*time.Millisecond), "p-a"))

	if _, err := os.Stat(filepath.Join(root, "7", "index.m3u8")); err != nil {
		t.Fatalf("device playlist: %v", err)
	}

	manager.WriteFrame(mediaFrame(7, true, base.Add(2*time.Second), "idr-b"))

	segment := readFile(t, filepath.Join(root, "7", "1", "0.ts"))
	if !strings.Contains(segment, "idr-a") || !strings.Contains(segment, "p-a") {
		t.Fatal("segment 0 does not hold the first access units")
	}
	if strings.Contains(segment, "idr-b") {
		t.Fatal("segment 0 holds the frame that closed it")
	}

	device := readFile(t, filepath.Join(root, "7", "index.m3u8"))
	if !strings.Contains(device, "#EXT-X-MEDIA-SEQUENCE:0") {
		t.Fatalf("media sequence missing:\n%s", device)
	}
	if !strings.Contains(device, "1/0.ts") {
		t.Fatalf("device playlist does not point at the channel segment:\n%s", device)
	}

	channel := readFile(t, filepath.Join(root, "7", "1", "index.m3u8"))
	if !strings.Contains(channel, "0.ts") || strings.Contains(channel, "1/0.ts") {
		t.Fatalf("channel playlist is wrong:\n%s", channel)
	}
}

func TestManagerDropsFramesBeforeTheFirstKeyFrame(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	base := time.UnixMilli(1700000000000)

	manager.WriteFrame(mediaFrame(7, false, base, "p-a"))
	if _, err := os.Stat(filepath.Join(root, "7", "1", "index.m3u8")); err == nil {
		t.Fatal("a stream started without a key frame")
	}
}

func TestManagerKeepsOnlyTheNewestSegments(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root, WithMaxSegments(2))
	base := time.UnixMilli(1700000000000)

	for index := 0; index < 4; index++ {
		manager.WriteFrame(mediaFrame(7, true, base.Add(time.Duration(index)*2*time.Second), "idr"))
	}

	if _, err := os.Stat(filepath.Join(root, "7", "1", "0.ts")); !os.IsNotExist(err) {
		t.Fatal("0.ts should have been removed")
	}
	for _, name := range []string{"1.ts", "2.ts"} {
		if _, err := os.Stat(filepath.Join(root, "7", "1", name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}

	device := readFile(t, filepath.Join(root, "7", "index.m3u8"))
	if !strings.Contains(device, "#EXT-X-MEDIA-SEQUENCE:1") {
		t.Fatalf("media sequence should follow the oldest kept segment:\n%s", device)
	}
}

func TestManagerFollowsTheLowestChannel(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	base := time.UnixMilli(1700000000000)

	secondary := mediaFrame(7, true, base, "idr-2")
	secondary.Channel = 2
	manager.WriteFrame(secondary)
	secondary = mediaFrame(7, true, base.Add(2*time.Second), "idr-2b")
	secondary.Channel = 2
	manager.WriteFrame(secondary)

	manager.WriteFrame(mediaFrame(7, true, base.Add(4*time.Second), "idr-1"))
	manager.WriteFrame(mediaFrame(7, true, base.Add(6*time.Second), "idr-1b"))

	device := readFile(t, filepath.Join(root, "7", "index.m3u8"))
	if !strings.Contains(device, "1/0.ts") {
		t.Fatalf("device playlist should follow channel 1:\n%s", device)
	}
	if _, err := os.Stat(filepath.Join(root, "7", "2", "0.ts")); err != nil {
		t.Fatalf("channel 2 segment missing: %v", err)
	}
}

func TestTransportStreamPacketBoundariesStayAligned(t *testing.T) {
	var writer tsWriter
	payload := bytes.Repeat([]byte{0x01, 0x02, 0x03}, 2000)
	out := writer.write(nil, payload, 1000, true, codec.MediaCodecH264)

	if len(out)%tsPacketSize != 0 {
		t.Fatalf("length = %d", len(out))
	}
	for offset := 0; offset < len(out); offset += tsPacketSize {
		if out[offset] != 0x47 {
			t.Fatalf("packet at %d starts with %#x", offset, out[offset])
		}
		pid := int(binary.BigEndian.Uint16(out[offset+1:offset+3]) & 0x1fff)
		switch pid {
		case 0, pmtPID, videoPID:
		default:
			t.Fatalf("unexpected pid %#x at %d", pid, offset)
		}
	}
}
