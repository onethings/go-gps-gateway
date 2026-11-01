package gateway

import (
	"context"
	"errors"
	"net"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
)

const (
	defaultPollInterval = time.Minute
	defaultPollBackoff  = 5 * time.Second
	defaultPollTimeout  = 30 * time.Second
)

// PollConfig describes one device the gateway connects to instead of waiting for
// it to connect. This is the client-mode counterpart of Serve, the same split
// the Java server makes with its protocol pollers.
type PollConfig struct {
	// Protocol selects the decoder, exactly like a server protocol name.
	Protocol string
	// Address is the device endpoint the gateway dials.
	Address string
	// Interval is the pause between two poll requests.
	Interval time.Duration
	// Timeout caps one poll request and its answer.
	Timeout time.Duration
	// Command is encoded by the protocol and written every interval. Nil means
	// a plain position request.
	Command *model.Command
	// Dial defaults to net.Dialer.DialContext; tests inject their own.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// Poll dials the device, asks for a position, decodes the answer and repeats
// until ctx is cancelled. A failed dial or a closed connection is retried after
// a short backoff, so a device that reboots is picked up again.
func (s *Server) Poll(ctx context.Context, config PollConfig) error {
	if config.Address == "" {
		return errors.New("gateway: poll without an address")
	}
	if config.Interval <= 0 {
		config.Interval = defaultPollInterval
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultPollTimeout
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	command := config.Command
	if command == nil {
		command = &model.Command{Type: model.CommandPositionSingle}
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		conn, err := dial(ctx, "tcp", config.Address)
		if err != nil {
			s.logf("%s: poll %s: %v", s.protocolName, config.Address, err)
			if !sleepCtx(ctx, defaultPollBackoff) {
				return nil
			}
			continue
		}
		s.pollConnection(ctx, conn, config, command)
		_ = conn.Close()
		if !sleepCtx(ctx, defaultPollBackoff) {
			return nil
		}
	}
}

// pollConnection runs one dialled connection until it fails or ctx ends.
func (s *Server) pollConnection(
	ctx context.Context, conn net.Conn, config PollConfig, command *model.Command,
) {
	protocol := s.factory(s.base)
	frameDecoder := protocol.NewFrameDecoder()
	decoder := protocol.NewDecoder()

	var encoder codec.Encoder
	if protocol.NewEncoder != nil {
		encoder = protocol.NewEncoder()
	}

	dc := &deviceConn{conn: conn}
	var (
		session *codec.DeviceSession
		buffer  []byte
		readBuf = make([]byte, 4096)
	)

	for {
		if ctx.Err() != nil {
			return
		}

		if encoder != nil {
			payload, err := encoder.EncodeCommand(command)
			if err != nil {
				s.logf("%s: poll encode: %v", s.protocolName, err)
				return
			}
			if err := dc.write(payload); err != nil {
				return
			}
		}

		deadline := time.Now().Add(config.Timeout)
		if err := conn.SetReadDeadline(deadline); err != nil {
			return
		}

		answered := false
		for !answered && ctx.Err() == nil {
			read, err := conn.Read(readBuf)
			if read > 0 {
				buffer = append(buffer, readBuf[:read]...)
				for {
					frame, consumed := frameDecoder.DecodeFrame(buffer)
					if consumed > 0 {
						buffer = buffer[consumed:]
					}
					if frame == nil {
						break
					}
					answered = true
					if err := s.handleFrame(ctx, dc, decoder, frame, &session); err != nil {
						s.logf("%s: poll decode: %v", s.protocolName, err)
						return
					}
					if len(buffer) == 0 {
						break
					}
				}
			}
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					return
				}
				break
			}
		}

		if !sleepCtx(ctx, config.Interval) {
			return
		}
	}
}

// sleepCtx waits for the duration and reports whether ctx is still alive.
func sleepCtx(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
