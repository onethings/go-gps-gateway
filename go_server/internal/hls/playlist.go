package hls

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// playlist renders one media playlist. prefix is empty for a channel playlist
// and "<channel>/" for the device level one, so the segment URIs stay relative
// to the playlist that names them.
func playlist(segments []segmentInfo, prefix string) []byte {
	var out strings.Builder
	out.WriteString("#EXTM3U\n")
	out.WriteString("#EXT-X-VERSION:3\n")
	out.WriteString("#EXT-X-TARGETDURATION:" + strconv.Itoa(targetDuration(segments)) + "\n")
	sequence := 0
	if len(segments) > 0 {
		sequence = segments[0].sequence
	}
	out.WriteString("#EXT-X-MEDIA-SEQUENCE:" + strconv.Itoa(sequence) + "\n")

	for _, segment := range segments {
		out.WriteString("#EXTINF:")
		out.WriteString(strconv.FormatFloat(segment.duration.Seconds(), 'f', 3, 64))
		out.WriteString(",\n")
		out.WriteString(prefix + strconv.Itoa(segment.sequence) + segmentSuffix + "\n")
	}
	return []byte(out.String())
}

// targetDuration is the longest segment rounded up, as the HLS spec requires.
func targetDuration(segments []segmentInfo) int {
	target := 1
	for _, segment := range segments {
		if value := int(math.Ceil(segment.duration.Seconds())); value > target {
			target = value
		}
	}
	return target
}

// writeFileAtomic writes through a temporary file and renames it, so a player
// polling the playlist never reads a half written one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := temp.Name()

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(name)
		return err
	}
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		os.Remove(name)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
