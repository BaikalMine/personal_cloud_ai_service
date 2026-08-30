//go:build windows

package miningagent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const maxMinerArchiveRedirects = 5

func newMinerArchiveHTTPClient(policy archiveSourcePolicy) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialPublicArchiveAddress,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   35 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxMinerArchiveRedirects {
				return errors.New("слишком много перенаправлений при загрузке архива")
			}
			return policy.validateRedirect(req, via)
		},
	}
}

func dialPublicArchiveAddress(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("некорректный адрес архива: %w", err)
	}
	resolved := make([]net.IPAddr, 0, 4)
	if literal, parseErr := netip.ParseAddr(strings.Trim(host, "[]")); parseErr == nil {
		resolved = append(resolved, net.IPAddr{IP: net.IP(literal.AsSlice())})
	} else {
		resolved, err = net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("DNS архива: %w", err)
		}
	}
	if len(resolved) == 0 {
		return nil, errors.New("домен архива не вернул IP-адрес")
	}
	for _, candidate := range resolved {
		address, ok := netip.AddrFromSlice(candidate.IP)
		if !ok || forbiddenArchiveIP(address.Unmap()) {
			return nil, errors.New("домен архива указывает на локальный, приватный или служебный адрес")
		}
	}
	dialer := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, candidate := range resolved {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, fmt.Errorf("подключение к архиву: %w", lastErr)
}
