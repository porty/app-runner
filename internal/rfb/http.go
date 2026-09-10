package rfb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// UpgradeProtocol is the Upgrade header token that switches an HTTP
// connection to raw RFB. It is what App Runner's console endpoint accepts
// from native clients; browsers use the websocket upgrade instead.
const UpgradeProtocol = "rfb"

// IsUpgradeRequest reports whether request asks to switch to raw RFB.
func IsUpgradeRequest(request *http.Request) bool {
	return strings.EqualFold(request.Header.Get("Upgrade"), UpgradeProtocol)
}

// httpTransport pins the upgrade to HTTP/1.1; HTTP/2 has no Upgrade.
var httpTransport = &http.Transport{
	Proxy:     http.ProxyFromEnvironment,
	Protocols: http1Only(),
}

func http1Only() *http.Protocols {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	return protocols
}

// DialHTTP upgrades a GET of consoleURL to a raw RFB stream. ctx bounds the
// request; the returned stream outlives it and must be closed by the caller.
func DialHTTP(ctx context.Context, consoleURL string) (io.ReadWriteCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, consoleURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", UpgradeProtocol)
	response, err := httpTransport.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = response.Status
		}
		return nil, errors.New(message)
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		response.Body.Close()
		return nil, fmt.Errorf("the console connection cannot be written to")
	}
	return stream, nil
}
