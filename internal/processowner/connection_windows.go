//go:build windows

package processowner

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ErrUnownedPeer = errors.New("TCP peer does not belong to the owned process group")
	getTCPTable    = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
)

func (g *Group) Contains(pid uint32) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return false, errors.New("process group is closed")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(process)
	var belongs int32
	ok, _, callErr := isProcessInJob.Call(uintptr(process), uintptr(g.job), uintptr(unsafe.Pointer(&belongs)))
	if ok == 0 {
		return false, callErr
	}
	return belongs != 0, nil
}

// Check the accepted connection, not just the listening port. A listener can be
// replaced between probing readiness and establishing the model request.
func (g *Group) ConfirmPeer(ctx context.Context, connection net.Conn) error {
	local, lok := connection.LocalAddr().(*net.TCPAddr)
	remote, rok := connection.RemoteAddr().(*net.TCPAddr)
	if !lok || !rok || !local.IP.Equal(net.IPv4(127, 0, 0, 1)) || !remote.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		return ErrUnownedPeer
	}
	deadline := time.NewTimer(500 * time.Millisecond)
	defer deadline.Stop()
	for {
		table, err := tcpOwners()
		if err != nil {
			return err
		}
		pid, found, err := connectedPeerPID(table, local, remote)
		if err != nil {
			return err
		}
		if found {
			owned, err := g.Contains(pid)
			if err != nil {
				return err
			}
			if !owned {
				return ErrUnownedPeer
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrUnownedPeer
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func tcpOwners() ([]byte, error) {
	var size uint32
	for attempt := 0; attempt < 4; attempt++ {
		if size > 4<<20 {
			return nil, errors.New("TCP ownership table is too large")
		}
		var pointer uintptr
		var table []byte
		if size > 0 {
			table = make([]byte, size)
			pointer = uintptr(unsafe.Pointer(&table[0]))
		}
		// AF_INET, TCP_TABLE_OWNER_PID_ALL, reserved=0.
		code, _, _ := getTCPTable.Call(pointer, uintptr(unsafe.Pointer(&size)), 0, 2, 5, 0)
		if code == 0 {
			return table, nil
		}
		if code != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil, windows.Errno(code)
		}
	}
	return nil, errors.New("TCP ownership table kept changing")
}

func connectedPeerPID(table []byte, local, remote *net.TCPAddr) (uint32, bool, error) {
	if len(table) < 4 {
		return 0, false, errors.New("invalid TCP ownership table")
	}
	count := uint64(binary.LittleEndian.Uint32(table[:4]))
	if count > uint64((len(table)-4)/24) {
		return 0, false, errors.New("truncated TCP ownership table")
	}
	for i := uint64(0); i < count; i++ {
		row := table[4+i*24 : 4+(i+1)*24]
		if binary.LittleEndian.Uint32(row[:4]) != 5 {
			continue
		}
		// The server side reverses the client's local/remote endpoints. Ports
		// occupy the first two network-order bytes of their four-byte DWORD.
		if net.IP(row[4:8]).Equal(remote.IP) && int(binary.BigEndian.Uint16(row[8:10])) == remote.Port &&
			net.IP(row[12:16]).Equal(local.IP) && int(binary.BigEndian.Uint16(row[16:18])) == local.Port {
			return binary.LittleEndian.Uint32(row[20:24]), true, nil
		}
	}
	return 0, false, nil
}
