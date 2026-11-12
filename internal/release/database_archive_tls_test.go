package release

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The proxy terminates the PostgreSQL SSLRequest handshake and forwards the
// unchanged PostgreSQL protocol to the owned fixture. Both Go's connector and
// the real libpq pg_dump must pass its TLS handshake to reach that database.
func archiveTLSProxy(t *testing.T, backend string, clientRequired bool) (string, map[string]string, *atomic.Int64) {
	t.Helper()
	directory := t.TempDir()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Archive Test Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})
	files := map[string]string{"sslrootcert": filepath.Join(directory, "root.pem")}
	if err := os.WriteFile(files["sslrootcert"], rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "Archive Test Peer"},
			NotBefore: root.NotBefore, NotAfter: root.NotAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		certificate, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
		if err != nil {
			t.Fatal(err)
		}
		private, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
	}
	serverPEM, serverKey := issue(2, x509.ExtKeyUsageServerAuth)
	certificate, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	if clientRequired {
		clientPEM, clientKey := issue(3, x509.ExtKeyUsageClientAuth)
		files["sslcert"], files["sslkey"] = filepath.Join(directory, "client.pem"), filepath.Join(directory, "client-key.pem")
		if err := os.WriteFile(files["sslcert"], clientPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files["sslkey"], clientKey, 0o600); err != nil {
			t.Fatal(err)
		}
		config.ClientAuth = tls.RequireAndVerifyClientCert
		config.ClientCAs = x509.NewCertPool()
		config.ClientCAs.AddCert(root)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var mutex sync.Mutex
	connections := make(map[net.Conn]bool)
	var workers sync.WaitGroup
	handshakes := &atomic.Int64{}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			mutex.Lock()
			if ctx.Err() != nil {
				mutex.Unlock()
				connection.Close()
				return
			}
			connections[connection] = true
			mutex.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer connection.Close()
				defer func() { mutex.Lock(); delete(connections, connection); mutex.Unlock() }()
				_ = connection.SetDeadline(time.Now().Add(60 * time.Second))
				var request [8]byte
				if _, err := io.ReadFull(connection, request[:]); err != nil || binary.BigEndian.Uint32(request[:4]) != 8 || binary.BigEndian.Uint32(request[4:]) != 80877103 {
					return
				}
				if _, err := connection.Write([]byte("S")); err != nil {
					return
				}
				secure := tls.Server(connection, config)
				if secure.HandshakeContext(ctx) != nil {
					return
				}
				handshakes.Add(1)
				upstream, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", backend)
				if err != nil {
					return
				}
				defer upstream.Close()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(upstream, secure); upstream.Close(); close(done) }()
				_, _ = io.Copy(secure, upstream)
				connection.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		mutex.Lock()
		for connection := range connections {
			connection.Close()
		}
		mutex.Unlock()
		workers.Wait()
	})
	return listener.Addr().String(), files, handshakes
}

func TestDatabaseArchiveTLS(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	for _, mode := range []string{"require", "verify-ca", "verify-full", "mutual"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			u, _ := url.Parse(dockerConfigurationFixtureURL(t, f))
			address, files, handshakes := archiveTLSProxy(t, u.Host, mode == "mutual")
			u.Host = address
			query := u.Query()
			query.Set("sslmode", mode)
			if mode == "mutual" {
				query.Set("sslmode", "verify-full")
			}
			if mode != "require" {
				for option, path := range files {
					query.Set(option, path)
				}
			}
			u.RawQuery = query.Encode()
			prepareDatabaseArchiveFixtureURL(t, f, u.String())
			if _, err := f.j.archiveDockerDatabase(context.Background(), f.controller, 0, options, f.open, runPostgreSQLDump); err != nil {
				t.Fatal("real TLS pg_dump failed", err)
			}
			if handshakes.Load() < 3 {
				t.Fatal("expected connector and pg_dump TLS handshakes were not observed")
			}
		})
	}
}
