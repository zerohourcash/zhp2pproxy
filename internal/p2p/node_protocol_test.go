package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"

	"zhp2pproxy/internal/config"
	"zhp2pproxy/internal/node"
)

func testVersionMessage(magic []byte, version int32) []byte {
	payload := make([]byte, 86)
	binary.LittleEndian.PutUint32(payload[:4], uint32(version))
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	header := make([]byte, p2pHeaderBytes)
	copy(header[:4], magic)
	copy(header[4:16], []byte("version"))
	binary.LittleEndian.PutUint32(header[16:20], uint32(len(payload)))
	copy(header[20:24], second[:4])
	return append(header, payload...)
}

func readVersionFromBytes(t *testing.T, message []byte) (p2pVersionMessage, error) {
	t.Helper()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	go func() {
		_, _ = right.Write(message)
		_ = right.Close()
	}()
	return readP2PVersion(left, "f1cfa6d3", time.Second)
}

func TestReadP2PVersionValidatesMainnetHandshake(t *testing.T) {
	message := testVersionMessage([]byte{0xf1, 0xcf, 0xa6, 0xd3}, 70018)
	got, err := readVersionFromBytes(t, message)
	if err != nil {
		t.Fatal(err)
	}
	if got.protocolVersion != 70018 || string(got.raw) != string(message) {
		t.Fatalf("unexpected version message: version=%d bytes=%d", got.protocolVersion, len(got.raw))
	}
}

func TestReadP2PVersionRejectsWrongNetworkAndChecksum(t *testing.T) {
	wrongNetwork := testVersionMessage([]byte{0x0d, 0x22, 0x15, 0x06}, 70018)
	if _, err := readVersionFromBytes(t, wrongNetwork); err == nil {
		t.Fatal("wrong network magic was accepted")
	}

	badChecksum := testVersionMessage([]byte{0xf1, 0xcf, 0xa6, 0xd3}, 70018)
	badChecksum[20] ^= 0xff
	if _, err := readVersionFromBytes(t, badChecksum); err == nil {
		t.Fatal("bad checksum was accepted")
	}
}

func TestReadP2PVersionRejectsNonVersionFirstMessage(t *testing.T) {
	message := testVersionMessage([]byte{0xf1, 0xcf, 0xa6, 0xd3}, 70018)
	for i := 4; i < 16; i++ {
		message[i] = 0
	}
	copy(message[4:16], []byte("ping"))
	if _, err := readVersionFromBytes(t, message); err == nil {
		t.Fatal("non-version first command was accepted")
	}
}

func TestDecodeP2PMagic(t *testing.T) {
	for _, value := range []string{"", "f1cf", "not-hex", "f1cfa6d300"} {
		if _, err := decodeP2PMagic(value); err == nil {
			t.Fatalf("invalid magic %q was accepted", value)
		}
	}
	if got, err := decodeP2PMagic("0xF1CFA6D3"); err != nil || len(got) != 4 {
		t.Fatalf("valid magic rejected: %x %v", got, err)
	}
}

func TestSafeAnnouncedAddressRejectsLocalNetworkTargets(t *testing.T) {
	for _, address := range []string{
		"/ip4/127.0.0.1/tcp/10001/p2p/12D3KooWTest",
		"/ip4/192.168.1.10/tcp/10001/p2p/12D3KooWTest",
		"/dns4/internal.example/tcp/10001/p2p/12D3KooWTest",
	} {
		if safeAnnouncedAddress(address) {
			t.Fatalf("unsafe announced address %q was accepted", address)
		}
	}
	for _, address := range []string{
		"/ip4/188.137.231.146/tcp/10001/p2p/12D3KooWTest",
		"/ip4/188.137.231.146/tcp/10001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWTest",
	} {
		if !safeAnnouncedAddress(address) {
			t.Fatalf("safe announced address %q was rejected", address)
		}
	}
}

func TestTwoProxyPeersBridgeValidatedNodeP2P(t *testing.T) {
	remoteNode, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer remoteNode.Close()
	remotePort := remoteNode.Addr().(*net.TCPAddr).Port
	remoteVersion := testVersionMessage([]byte{0xf1, 0xcf, 0xa6, 0xd3}, 70019)
	nodeDone := make(chan error, 1)
	go func() {
		var conn net.Conn
		for {
			accepted, err := remoteNode.Accept()
			if err != nil {
				nodeDone <- err
				return
			}
			if _, err := readP2PVersion(accepted, "f1cfa6d3", time.Second); err != nil {
				_ = accepted.Close()
				continue
			}
			conn = accepted
			break
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(remoteVersion); err != nil {
			nodeDone <- err
			return
		}
		payload := make([]byte, 19)
		if _, err := io.ReadFull(conn, payload); err != nil {
			nodeDone <- err
			return
		}
		_, err = conn.Write(payload)
		nodeDone <- err
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newConfig := func(dir string) config.Config {
		cfg := config.Default()
		cfg.P2PPort = 0
		cfg.EnableIPv6 = false
		cfg.EnableQUIC = false
		cfg.ForceRelay = false
		cfg.RelayEnabled = false
		cfg.BootstrapPeers = []string{}
		cfg.BootstrapURLs = []string{}
		cfg.IdentityKeyPath = filepath.Join(dir, "identity.key")
		cfg.PeerStorePath = filepath.Join(dir, "peers.json")
		cfg.P2PProxyListen = "127.0.0.1:0"
		cfg.NodeP2PPort = remotePort
		return cfg
	}
	leftConfig := newConfig(t.TempDir())
	rightConfig := newConfig(t.TempDir())
	right, err := New(ctx, rightConfig, node.NewClient(rightConfig))
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	left, err := New(ctx, leftConfig, node.NewClient(leftConfig))
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()

	var rightAddr string
	for _, addr := range right.host.Addrs() {
		if _, err := addr.ValueForProtocol(ma.P_TCP); err == nil {
			rightAddr = addr.Encapsulate(ma.StringCast("/p2p/" + right.PeerID())).String()
			break
		}
	}
	if rightAddr == "" {
		t.Fatal("right proxy has no TCP listen address")
	}
	if err := left.ConnectPeer(ctx, rightAddr); err != nil {
		t.Fatal(err)
	}

	var proxyListen string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := left.NodeP2PStatus().(NodeP2PStatus)
		if status.Listening {
			proxyListen = status.Listen
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if proxyListen == "" {
		t.Fatal("local node P2P proxy did not start")
	}
	localNode, err := net.DialTimeout("tcp", proxyListen, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer localNode.Close()
	_ = localNode.SetDeadline(time.Now().Add(5 * time.Second))
	localVersion := testVersionMessage([]byte{0xf1, 0xcf, 0xa6, 0xd3}, 70018)
	if _, err := localNode.Write(localVersion); err != nil {
		t.Fatal(err)
	}
	gotVersion := make([]byte, len(remoteVersion))
	if _, err := io.ReadFull(localNode, gotVersion); err != nil {
		t.Fatal(err)
	}
	if string(gotVersion) != string(remoteVersion) {
		t.Fatal("remote version message changed in transit")
	}
	payload := []byte("post-handshake-data")
	if _, err := localNode.Write(payload); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(localNode, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != string(payload) {
		t.Fatalf("tunnel payload = %q", echo)
	}
	if err := <-nodeDone; err != nil {
		t.Fatal(err)
	}
	if status := left.NodeP2PStatus().(NodeP2PStatus); status.SuccessfulHandshakes != 1 || status.LastRemoteVersion != 70019 {
		t.Fatalf("left P2P status = %+v", status)
	}
	if status := right.NodeP2PStatus().(NodeP2PStatus); status.SuccessfulHandshakes != 1 || status.LastRemoteVersion != 70018 {
		t.Fatalf("right P2P status = %+v", status)
	}
}
