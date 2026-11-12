//go:build windows

package processowner

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestConnectedPeerPIDRequiresExactEstablishedPair(t *testing.T) {
	local := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000}
	remote := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 11435}
	table := make([]byte, 28)
	binary.LittleEndian.PutUint32(table[:4], 1)
	row := table[4:]
	binary.LittleEndian.PutUint32(row[:4], 5)
	copy(row[4:8], remote.IP.To4())
	binary.BigEndian.PutUint16(row[8:10], uint16(remote.Port))
	copy(row[12:16], local.IP.To4())
	binary.BigEndian.PutUint16(row[16:18], uint16(local.Port))
	binary.LittleEndian.PutUint32(row[20:24], 1234)
	if pid, found, err := connectedPeerPID(table, local, remote); err != nil || !found || pid != 1234 {
		t.Fatal(pid, found, err)
	}
	for _, offset := range []int{4, 8, 12, 16} {
		changed := append([]byte(nil), table...)
		changed[4+offset] ^= 1
		if _, found, err := connectedPeerPID(changed, local, remote); err != nil || found {
			t.Fatal("wrong connection matched", offset, found, err)
		}
	}
	binary.LittleEndian.PutUint32(row[:4], 2)
	if _, found, err := connectedPeerPID(table, local, remote); err != nil || found {
		t.Fatal("listener used as connection proof", found, err)
	}
	for _, truncated := range [][]byte{nil, table[:3], table[:27]} {
		if _, _, err := connectedPeerPID(truncated, local, remote); err == nil {
			t.Fatal("truncated table accepted")
		}
	}
	binary.LittleEndian.PutUint32(table[:4], ^uint32(0))
	if _, _, err := connectedPeerPID(table, local, remote); err == nil {
		t.Fatal("invalid count accepted")
	}
}

func TestEnvironmentBlockEncoding(t *testing.T) {
	block, err := environmentBlock([]string{"z=last", "A=\u6a21\u578b", "=C:=C:\\fixture"})
	if err != nil {
		t.Fatal(err)
	}
	decoded := string(utf16.Decode(block))
	if decoded != "=C:=C:\\fixture\x00A=\u6a21\u578b\x00z=last\x00\x00" {
		t.Fatal(decoded)
	}
	empty, err := environmentBlock(nil)
	if err != nil || len(empty) != 2 || empty[0] != 0 || empty[1] != 0 {
		t.Fatal(empty, err)
	}
	for _, bad := range []string{"missing value", "A=x\x00B=y"} {
		if _, err := environmentBlock([]string{bad}); err == nil {
			t.Fatal("invalid environment accepted", strings.ReplaceAll(bad, "\x00", "\\0"))
		}
	}
}
