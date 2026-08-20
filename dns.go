package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const managedDNSPort = 53

type bridgeDNSConfig struct {
	Enabled    bool     `json:"enabled,omitempty"`
	Forwarders []string `json:"forwarders,omitempty"`
	Auto       bool     `json:"auto,omitempty"`
	Suffix     string   `json:"suffix,omitempty"`
}

type dnsRecordProvider func() map[string]netip.Addr

type bridgeDNSController interface {
	Start(string, netip.Addr, bridgeDNSConfig, dnsRecordProvider) error
	Stop(string) error
	Status(string) (bool, string)
	Close() error
}

type managedDNSServer interface {
	Serve() error
	Close() error
}

type dnsServerFactory func(netip.Addr, dns.Handler) (managedDNSServer, error)

type dnsRuntime struct {
	server  managedDNSServer
	running bool
	error   string
}

type dnsManager struct {
	mu        sync.Mutex
	newServer dnsServerFactory
	runtimes  map[string]*dnsRuntime
}

func newDNSManager() *dnsManager {
	return &dnsManager{newServer: newInProcessDNSServer, runtimes: make(map[string]*dnsRuntime)}
}

func (m *dnsManager) Start(bridge string, address netip.Addr, config bridgeDNSConfig, records dnsRecordProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if runtime := m.runtimes[bridge]; runtime != nil && runtime.running {
		return nil
	}
	handler := &managedDNSHandler{address: address, config: config, records: records}
	server, err := m.newServer(address, handler)
	if err != nil {
		return fmt.Errorf("listen on %s UDP/TCP port %d: %w", address, managedDNSPort, err)
	}
	runtime := &dnsRuntime{server: server, running: true}
	m.runtimes[bridge] = runtime
	go m.serve(bridge, runtime)
	slog.Info("bridge DNS server started", "bridge", bridge, "address", address, "forwarders", config.Forwarders, "auto_dns", config.Auto, "suffix", config.Suffix)
	return nil
}

func (m *dnsManager) Stop(bridge string) error {
	m.mu.Lock()
	runtime := m.runtimes[bridge]
	if runtime == nil {
		m.mu.Unlock()
		return nil
	}
	delete(m.runtimes, bridge)
	m.mu.Unlock()
	if err := runtime.server.Close(); err != nil {
		return err
	}
	slog.Info("bridge DNS server stopped", "bridge", bridge)
	return nil
}

func (m *dnsManager) Status(bridge string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimes[bridge]
	if runtime == nil {
		return false, ""
	}
	return runtime.running, runtime.error
}

func (m *dnsManager) Close() error {
	m.mu.Lock()
	runtimes := m.runtimes
	m.runtimes = make(map[string]*dnsRuntime)
	m.mu.Unlock()
	var result error
	for _, runtime := range runtimes {
		result = errors.Join(result, runtime.server.Close())
	}
	return result
}

func (m *dnsManager) serve(bridge string, runtime *dnsRuntime) {
	err := runtime.server.Serve()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtimes[bridge] != runtime {
		return
	}
	runtime.running = false
	if err != nil {
		runtime.error = err.Error()
		slog.Error("bridge DNS server stopped unexpectedly", "bridge", bridge, "error", err)
	}
}

type combinedDNSServer struct {
	udp           *dns.Server
	tcp           *dns.Server
	udpConnection net.PacketConn
	tcpListener   net.Listener
	errors        chan error
	close         sync.Once
}

func newInProcessDNSServer(address netip.Addr, handler dns.Handler) (managedDNSServer, error) {
	listenAddress := net.JoinHostPort(address.String(), strconv.Itoa(managedDNSPort))
	udpConnection, err := net.ListenPacket("udp4", listenAddress)
	if err != nil {
		return nil, err
	}
	tcpListener, err := net.Listen("tcp4", listenAddress)
	if err != nil {
		_ = udpConnection.Close()
		return nil, err
	}
	udpStarted := make(chan struct{})
	tcpStarted := make(chan struct{})
	server := &combinedDNSServer{
		udp:           &dns.Server{PacketConn: udpConnection, Handler: handler, NotifyStartedFunc: func() { close(udpStarted) }},
		tcp:           &dns.Server{Listener: tcpListener, Handler: handler, NotifyStartedFunc: func() { close(tcpStarted) }},
		udpConnection: udpConnection,
		tcpListener:   tcpListener,
		errors:        make(chan error, 2),
	}
	go func() { server.errors <- server.udp.ActivateAndServe() }()
	go func() { server.errors <- server.tcp.ActivateAndServe() }()
	for _, started := range []<-chan struct{}{udpStarted, tcpStarted} {
		select {
		case <-started:
		case serveErr := <-server.errors:
			_ = server.Close()
			return nil, serveErr
		case <-time.After(2 * time.Second):
			_ = server.Close()
			return nil, errors.New("timed out starting DNS listener")
		}
	}
	return server, nil
}

func (s *combinedDNSServer) Serve() error {
	first := <-s.errors
	_ = s.Close()
	second := <-s.errors
	return errors.Join(first, second)
}

func (s *combinedDNSServer) Close() error {
	var result error
	s.close.Do(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result = errors.Join(
			s.udp.ShutdownContext(shutdownContext), s.tcp.ShutdownContext(shutdownContext),
			ignoreClosedNetworkError(s.udpConnection.Close()), ignoreClosedNetworkError(s.tcpListener.Close()),
		)
	})
	return result
}

func ignoreClosedNetworkError(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type managedDNSHandler struct {
	address netip.Addr
	config  bridgeDNSConfig
	records dnsRecordProvider
}

func (h *managedDNSHandler) ServeDNS(writer dns.ResponseWriter, request *dns.Msg) {
	if request == nil || len(request.Question) != 1 {
		response := new(dns.Msg)
		if request != nil {
			response.SetRcode(request, dns.RcodeFormatError)
		}
		_ = writer.WriteMsg(response)
		return
	}
	if h.config.Auto && nameWithinDNSZone(request.Question[0].Name, h.config.Suffix) {
		h.serveAuthoritative(writer, request)
		return
	}
	h.serveForwarded(writer, request)
}

func (h *managedDNSHandler) serveAuthoritative(writer dns.ResponseWriter, request *dns.Msg) {
	response := new(dns.Msg)
	response.SetReply(request)
	response.Authoritative = true
	response.RecursionAvailable = true
	question := request.Question[0]
	name := strings.ToLower(dns.Fqdn(question.Name))
	zone := strings.ToLower(dns.Fqdn(h.config.Suffix))
	nameserver := zone
	records := make(map[string]netip.Addr)
	if h.records != nil {
		for label, address := range h.records() {
			records[strings.ToLower(label)+"."+zone] = address
		}
	}

	if name == zone {
		if question.Qtype == dns.TypeA || question.Qtype == dns.TypeANY {
			response.Answer = append(response.Answer, &dns.A{Hdr: dns.RR_Header{Name: zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.IP(h.address.AsSlice())})
		}
		switch question.Qtype {
		case dns.TypeSOA, dns.TypeANY:
			response.Answer = append(response.Answer, authoritativeSOA(zone, nameserver))
		}
		if question.Qtype == dns.TypeNS || question.Qtype == dns.TypeANY {
			response.Answer = append(response.Answer, &dns.NS{Hdr: dns.RR_Header{Name: zone, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: 30}, Ns: nameserver})
		}
	} else if address, found := records[name]; found {
		if question.Qtype == dns.TypeA || question.Qtype == dns.TypeANY {
			response.Answer = append(response.Answer, &dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.IP(address.AsSlice())})
		}
	} else {
		response.Rcode = dns.RcodeNameError
	}
	if len(response.Answer) == 0 {
		response.Ns = append(response.Ns, authoritativeSOA(zone, nameserver))
	}
	if err := writer.WriteMsg(response); err != nil {
		slog.Warn("write authoritative DNS response", "name", question.Name, "error", err)
	}
}

func (h *managedDNSHandler) serveForwarded(writer dns.ResponseWriter, request *dns.Msg) {
	var lastErr error
	for _, forwarder := range h.config.Forwarders {
		endpoint, err := dnsForwarderEndpoint(forwarder)
		if err != nil {
			lastErr = err
			continue
		}
		client := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
		response, _, err := client.ExchangeContext(context.Background(), request.Copy(), endpoint)
		if err == nil && response != nil && response.Truncated {
			client.Net = "tcp"
			response, _, err = client.ExchangeContext(context.Background(), request.Copy(), endpoint)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if err := writer.WriteMsg(response); err != nil {
			slog.Warn("write forwarded DNS response", "name", request.Question[0].Name, "error", err)
		}
		return
	}
	response := new(dns.Msg)
	response.SetRcode(request, dns.RcodeServerFailure)
	_ = writer.WriteMsg(response)
	if lastErr != nil {
		slog.Warn("all DNS forwarders failed", "name", request.Question[0].Name, "error", lastErr)
	}
}

func authoritativeSOA(zone, nameserver string) *dns.SOA {
	return &dns.SOA{
		Hdr: dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 30},
		Ns:  nameserver, Mbox: "hostmaster." + zone, Serial: 1,
		Refresh: 60, Retry: 60, Expire: 300, Minttl: 30,
	}
}

func nameWithinDNSZone(name, suffix string) bool {
	name = strings.ToLower(dns.Fqdn(name))
	zone := strings.ToLower(dns.Fqdn(suffix))
	return name == zone || strings.HasSuffix(name, "."+zone)
}

func validateDNSLabel(label string) error {
	if len(label) == 0 || len(label) > 63 {
		return errors.New("must contain between 1 and 63 characters")
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return errors.New("must not begin or end with a hyphen")
	}
	for _, character := range label {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return errors.New("must use only letters, numbers, and interior hyphens")
	}
	return nil
}

func normalizeDNSSuffix(suffix string) (string, error) {
	suffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(suffix)), ".")
	if len(suffix) == 0 || len(suffix) > 253 {
		return "", errors.New("DNS suffix must contain between 1 and 253 characters")
	}
	for _, label := range strings.Split(suffix, ".") {
		if err := validateDNSLabel(label); err != nil {
			return "", fmt.Errorf("invalid DNS suffix label %q: %w", label, err)
		}
	}
	return suffix, nil
}

func defaultDNSSuffix(bridge string) string {
	return strings.ToLower(bridge) + ".internal"
}

func normalizeDNSForwarders(forwarders []string, server netip.Addr) ([]string, error) {
	result := make([]string, 0, len(forwarders))
	seen := make(map[string]struct{})
	for _, value := range forwarders {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		address, port, err := parseDNSForwarder(value)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS forwarder %q: %w", value, err)
		}
		if address == server && port == managedDNSPort {
			return nil, fmt.Errorf("DNS forwarder %s would send queries back to the managed DNS server", value)
		}
		normalized := address.String()
		if port != managedDNSPort {
			normalized = netip.AddrPortFrom(address, uint16(port)).String()
		}
		if _, found := seen[normalized]; found {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	if len(result) == 0 {
		return nil, errors.New("at least one DNS forwarding server is required")
	}
	if len(result) > 8 {
		return nil, errors.New("at most eight DNS forwarding servers may be configured")
	}
	return result, nil
}

func parseDNSForwarder(value string) (netip.Addr, int, error) {
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap(), managedDNSPort, nil
	}
	endpoint, err := netip.ParseAddrPort(value)
	if err != nil {
		return netip.Addr{}, 0, errors.New("must be an IP address with an optional port")
	}
	if endpoint.Port() == 0 {
		return netip.Addr{}, 0, errors.New("port must be between 1 and 65535")
	}
	return endpoint.Addr().Unmap(), int(endpoint.Port()), nil
}

func dnsForwarderEndpoint(value string) (string, error) {
	address, port, err := parseDNSForwarder(value)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(address.String(), strconv.Itoa(port)), nil
}

func defaultDNSForwarders() []string {
	configuration, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(configuration.Servers))
	for _, server := range configuration.Servers {
		address, err := netip.ParseAddr(server)
		if err == nil {
			result = append(result, address.Unmap().String())
		}
	}
	sort.Strings(result)
	return result
}
