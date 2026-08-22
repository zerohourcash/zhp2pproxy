package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	p2pHeaderBytes    = 24
	maxVersionPayload = 4096
	copyBufferBytes   = 32 << 10
)

type deadlineReadWriter interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type p2pVersionMessage struct {
	raw             []byte
	protocolVersion int32
}

func readP2PVersion(r deadlineReadWriter, magicText string, timeout time.Duration) (p2pVersionMessage, error) {
	magic, err := decodeP2PMagic(magicText)
	if err != nil {
		return p2pVersionMessage{}, err
	}
	if timeout <= 0 {
		return p2pVersionMessage{}, errors.New("invalid p2p handshake timeout")
	}
	if err := r.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return p2pVersionMessage{}, err
	}
	defer r.SetReadDeadline(time.Time{}) //nolint:errcheck -- clearing a deadline is best effort during teardown

	header := make([]byte, p2pHeaderBytes)
	if _, err := io.ReadFull(r, header); err != nil {
		return p2pVersionMessage{}, fmt.Errorf("read p2p header: %w", err)
	}
	if string(header[:4]) != string(magic) {
		return p2pVersionMessage{}, fmt.Errorf("unexpected p2p network magic %x", header[:4])
	}
	command, err := p2pCommand(header[4:16])
	if err != nil {
		return p2pVersionMessage{}, err
	}
	if command != "version" {
		return p2pVersionMessage{}, fmt.Errorf("first p2p command is %q, want version", command)
	}
	payloadBytes := binary.LittleEndian.Uint32(header[16:20])
	if payloadBytes < 4 || payloadBytes > maxVersionPayload {
		return p2pVersionMessage{}, fmt.Errorf("invalid p2p version payload size %d", payloadBytes)
	}
	payload := make([]byte, payloadBytes)
	if _, err := io.ReadFull(r, payload); err != nil {
		return p2pVersionMessage{}, fmt.Errorf("read p2p version payload: %w", err)
	}
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	if string(header[20:24]) != string(second[:4]) {
		return p2pVersionMessage{}, errors.New("invalid p2p version checksum")
	}
	version := binary.LittleEndian.Uint32(payload[:4])
	if version > math.MaxInt32 {
		return p2pVersionMessage{}, fmt.Errorf("invalid p2p protocol version %d", version)
	}
	raw := make([]byte, 0, len(header)+len(payload))
	raw = append(raw, header...)
	raw = append(raw, payload...)
	return p2pVersionMessage{raw: raw, protocolVersion: int32(version)}, nil
}

func decodeP2PMagic(text string) ([]byte, error) {
	text = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(text), "0x"))
	raw, err := hex.DecodeString(text)
	if err != nil || len(raw) != 4 {
		return nil, errors.New("node_p2p_magic must contain exactly 4 hexadecimal bytes")
	}
	return raw, nil
}

func p2pCommand(raw []byte) (string, error) {
	end := len(raw)
	for i, b := range raw {
		if b == 0 {
			end = i
			for _, rest := range raw[i:] {
				if rest != 0 {
					return "", errors.New("invalid p2p command padding")
				}
			}
			break
		}
		if b < 0x20 || b > 0x7e {
			return "", errors.New("invalid p2p command character")
		}
	}
	if end == 0 {
		return "", errors.New("empty p2p command")
	}
	return string(raw[:end]), nil
}

func writeP2PVersion(w deadlineReadWriter, message p2pVersionMessage, timeout time.Duration) error {
	if err := w.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	defer w.SetWriteDeadline(time.Time{}) //nolint:errcheck -- clearing a deadline is best effort during teardown
	for remaining := message.raw; len(remaining) > 0; {
		n, err := w.Write(remaining)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
		remaining = remaining[n:]
	}
	return nil
}

type rateLimitedReader struct {
	ctx     context.Context
	reader  io.Reader
	limiter *rate.Limiter
	maxRead int
}

func (r *rateLimitedReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		p = p[:r.maxRead]
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		if waitErr := r.limiter.WaitN(r.ctx, n); waitErr != nil {
			return 0, waitErr
		}
	}
	return n, err
}

func pipeBothLimited(ctx context.Context, a, b deadlineReadWriter, bytesPerSecond int64) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if bytesPerSecond <= 0 {
		bytesPerSecond = 8 << 20
	}
	burst := copyBufferBytes
	if bytesPerSecond < int64(burst) {
		burst = int(bytesPerSecond)
		if burst < 1024 {
			burst = 1024
		}
	}
	newReader := func(reader io.Reader) io.Reader {
		return &rateLimitedReader{
			ctx: ctx, reader: reader,
			limiter: rate.NewLimiter(rate.Limit(bytesPerSecond), burst),
			maxRead: burst,
		}
	}
	done := make(chan struct{}, 2)
	copyOne := func(dst io.Writer, src io.Reader) {
		buffer := make([]byte, copyBufferBytes)
		_, _ = io.CopyBuffer(dst, newReader(src), buffer)
		cancel()
		_ = a.Close()
		_ = b.Close()
		done <- struct{}{}
	}
	go copyOne(a, b)
	go copyOne(b, a)
	<-done
	<-done
}
