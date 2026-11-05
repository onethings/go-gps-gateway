// Package hls turns device video into the playlist and segments a browser
// player reads. Segments go to <root>/<deviceId>/<channel>/, and the device
// level playlist <root>/<deviceId>/index.m3u8 follows the lowest channel, which
// is the main stream of a JT1078 terminal.
package hls

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/codec"
)

const (
	defaultMaxSegments = 5
	// maxSegmentBytes bounds one open segment. A terminal that never sends a
	// key frame must not grow the buffer without limit.
	maxSegmentBytes = 64 << 20
	segmentSuffix   = ".ts"
	playlistName    = "index.m3u8"
)

type streamKey struct {
	deviceID int64
	channel  int
}

// Manager is the codec.MediaSink of the gateway. It keeps one segmenter per
// device and channel and writes the playlists players poll.
type Manager struct {
	root        string
	logf        func(format string, args ...any)
	maxSegments int

	mu          sync.Mutex
	streams     map[streamKey]*stream
	mainChannel map[int64]int
}

// Option configures a manager.
type Option func(*Manager)

// WithLogger receives the disk errors the manager cannot return to the device
// connection.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(m *Manager) { m.logf = logf }
}

// WithMaxSegments sets how many finished segments a stream keeps on disk.
func WithMaxSegments(count int) Option {
	return func(m *Manager) {
		if count > 0 {
			m.maxSegments = count
		}
	}
}

// NewManager creates a manager whose streams live below root.
func NewManager(root string, options ...Option) *Manager {
	manager := &Manager{
		root:        root,
		logf:        func(string, ...any) {},
		maxSegments: defaultMaxSegments,
		streams:     make(map[streamKey]*stream),
		mainChannel: make(map[int64]int),
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

// WriteFrame implements codec.MediaSink.
func (m *Manager) WriteFrame(frame codec.MediaFrame) {
	if frame.DeviceID == 0 || len(frame.Payload) == 0 || frame.Codec == codec.MediaCodecUnknown {
		return
	}

	key := streamKey{deviceID: frame.DeviceID, channel: frame.Channel}
	m.mu.Lock()
	stream := m.streams[key]
	if stream == nil {
		stream = newStream(m, frame.DeviceID, frame.Channel)
		m.streams[key] = stream
		if current, ok := m.mainChannel[frame.DeviceID]; !ok || frame.Channel < current {
			m.mainChannel[frame.DeviceID] = frame.Channel
		}
	}
	m.mu.Unlock()

	if err := stream.write(frame); err != nil {
		m.logf("hls: device %d channel %d: %v", frame.DeviceID, frame.Channel, err)
	}
}

// isMain reports whether channel is the one the device level playlist follows.
func (m *Manager) isMain(deviceID int64, channel int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	main, ok := m.mainChannel[deviceID]
	return ok && main == channel
}

// stream is the segmenter of one device and channel.
type stream struct {
	manager   *Manager
	deviceID  int64
	channel   int
	deviceDir string
	dir       string

	mu      sync.Mutex
	writer  tsWriter
	segment []segmentInfo
	next    int

	started      bool
	overflowed   bool
	base         time.Time
	currentStart time.Time
	current      []byte
}

type segmentInfo struct {
	sequence int
	duration time.Duration
}

func newStream(manager *Manager, deviceID int64, channel int) *stream {
	deviceDir := filepath.Join(manager.root, strconv.FormatInt(deviceID, 10))
	return &stream{
		manager:   manager,
		deviceID:  deviceID,
		channel:   channel,
		deviceDir: deviceDir,
		dir:       filepath.Join(deviceDir, strconv.Itoa(channel)),
	}
}

// write appends one access unit and closes the open segment when a new key
// frame starts one.
func (s *stream) write(frame codec.MediaFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		if !frame.KeyFrame {
			return nil
		}
		s.started = true
		s.base = frame.Timestamp
		s.currentStart = frame.Timestamp
		if err := s.writePlaylists(); err != nil {
			return err
		}
	} else if frame.KeyFrame && len(s.current) > 0 && frame.Timestamp.After(s.currentStart) {
		if err := s.finalize(frame.Timestamp); err != nil {
			return err
		}
		s.currentStart = frame.Timestamp
	}

	if !frame.KeyFrame && len(s.current)+len(frame.Payload) > maxSegmentBytes {
		if !s.overflowed {
			s.manager.logf("hls: device %d channel %d: open segment passed %d bytes, dropping frames until the next key frame",
				s.deviceID, s.channel, maxSegmentBytes)
			s.overflowed = true
		}
		return nil
	}

	pts := frame.Timestamp.Sub(s.base).Milliseconds()
	if pts < 0 {
		pts = 0
	}
	s.current = s.writer.write(s.current, frame.Payload, pts, frame.KeyFrame, frame.Codec)
	return nil
}

func (s *stream) finalize(end time.Time) error {
	sequence := s.next
	s.next++

	path := filepath.Join(s.dir, strconv.Itoa(sequence)+segmentSuffix)
	if err := writeFileAtomic(path, s.current); err != nil {
		return err
	}

	s.segment = append(s.segment, segmentInfo{sequence: sequence, duration: end.Sub(s.currentStart)})
	s.current = nil
	s.overflowed = false
	s.trim()
	return s.writePlaylists()
}

// trim drops the oldest segments past the retention limit.
func (s *stream) trim() {
	for len(s.segment) > s.manager.maxSegments {
		oldest := s.segment[0]
		s.segment = s.segment[1:]
		_ = os.Remove(filepath.Join(s.dir, strconv.Itoa(oldest.sequence)+segmentSuffix))
	}
}

func (s *stream) writePlaylists() error {
	if err := writeFileAtomic(filepath.Join(s.dir, playlistName), playlist(s.segment, "")); err != nil {
		return err
	}
	if !s.manager.isMain(s.deviceID, s.channel) {
		return nil
	}
	prefix := strconv.Itoa(s.channel) + "/"
	return writeFileAtomic(filepath.Join(s.deviceDir, playlistName), playlist(s.segment, prefix))
}
