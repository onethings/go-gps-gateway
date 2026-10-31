// Command device-sim replays recorded device frames against a running gateway
// so the live lanes can drive the real TCP surface without hardware.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/devicescene"
)

// Write patterns. They exist because the frame decoder has to survive a frame
// arriving in two pieces and two frames arriving in one piece.
const (
	modeSingle = "single"
	modeSplit  = "split"
	modeBatch  = "batch"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// load accepts a scene file path or the name of a bundled scene.
func load(name string) (*devicescene.Scene, error) {
	if _, err := os.Stat(name); err == nil {
		return devicescene.Load(name)
	}

	scenes, err := devicescene.All()
	if err != nil {
		return nil, err
	}
	for i := range scenes {
		if scenes[i].Name == name {
			return &scenes[i], nil
		}
	}
	return nil, fmt.Errorf("no scene file or bundled scene named %q", name)
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("device-sim", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("addr", "127.0.0.1:5023", "gateway listen address")
	scenePath := flags.String("scene", "", "scene file to replay, or a bundled name such as gt06")
	mode := flags.String("mode", modeSingle, "write pattern, one of single, split or batch")
	repeat := flags.Int("repeat", 1, "times to replay the scene on one connection")
	connections := flags.Int("connections", 1, "parallel connections")
	readWait := flags.Duration("read-wait", 300*time.Millisecond, "idle wait for replies after the last write")

	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *scenePath == "" {
		fmt.Fprintln(stderr, "device-sim: -scene is required")
		return 2
	}
	if *mode != modeSingle && *mode != modeSplit && *mode != modeBatch {
		fmt.Fprintf(stderr, "device-sim: unknown mode %q\n", *mode)
		return 2
	}
	if *connections < 1 || *repeat < 1 {
		fmt.Fprintln(stderr, "device-sim: -connections and -repeat must be at least 1")
		return 2
	}

	scene, err := load(*scenePath)
	if err != nil {
		fmt.Fprintf(stderr, "device-sim: %v\n", err)
		return 1
	}
	frames, err := scene.Frames()
	if err != nil {
		fmt.Fprintf(stderr, "device-sim: %v\n", err)
		return 1
	}

	var (
		wait     sync.WaitGroup
		lock     sync.Mutex
		failures int
	)
	for i := 0; i < *connections; i++ {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()

			err := replay(*address, frames, *mode, *repeat, *readWait, func(format string, args ...any) {
				lock.Lock()
				defer lock.Unlock()
				fmt.Fprintf(stdout, "%s conn %d "+format+"\n", append([]any{scene.Name, id + 1}, args...)...)
			})
			if err != nil {
				lock.Lock()
				defer lock.Unlock()
				failures++
				fmt.Fprintf(stdout, "%s conn %d fail %v\n", scene.Name, id+1, err)
			}
		}(i)
	}
	wait.Wait()

	fmt.Fprintf(stdout, "sent %d frames on %d connections with mode %s\n",
		len(frames)**repeat**connections, *connections, *mode)
	if failures > 0 {
		fmt.Fprintf(stdout, "fail %s %d of %d connections\n", scene.Name, failures, *connections)
		return 1
	}
	fmt.Fprintf(stdout, "pass %s\n", scene.Name)
	return 0
}

func replay(
	address string, frames [][]byte, mode string, repeat int, readWait time.Duration,
	logf func(string, ...any),
) error {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	for i := 0; i < repeat; i++ {
		if err := writeFrames(conn, frames, mode); err != nil {
			return err
		}
	}

	replies, err := readReplies(conn, readWait)
	if err != nil {
		return err
	}
	logf("recv %d replies", len(replies))
	for _, reply := range replies {
		logf("recv %x", reply)
	}
	return nil
}

func writeFrames(conn net.Conn, frames [][]byte, mode string) error {
	switch mode {
	case modeBatch:
		joined := make([]byte, 0, 1024)
		for _, frame := range frames {
			joined = append(joined, frame...)
		}
		_, err := conn.Write(joined)
		return err

	case modeSplit:
		for _, frame := range frames {
			half := len(frame) / 2
			if _, err := conn.Write(frame[:half]); err != nil {
				return err
			}
			time.Sleep(20 * time.Millisecond)
			if _, err := conn.Write(frame[half:]); err != nil {
				return err
			}
		}
		return nil

	default:
		for _, frame := range frames {
			if _, err := conn.Write(frame); err != nil {
				return err
			}
		}
		return nil
	}
}

// readReplies collects whatever the gateway sends until it goes quiet. Some
// protocols answer every frame, others only the handshake, so an idle window
// is the only stop condition that fits all four.
func readReplies(conn net.Conn, idle time.Duration) ([][]byte, error) {
	var replies [][]byte

	buf := make([]byte, 1024)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return replies, err
		}

		n, err := conn.Read(buf)
		if n > 0 {
			replies = append(replies, append([]byte(nil), buf[:n]...))
		}
		if err == nil {
			continue
		}

		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return replies, nil
		}
		if errors.Is(err, io.EOF) {
			return replies, nil
		}
		return replies, err
	}
}
