package main

import (
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type recordingDNSWriter struct {
	message *dns.Msg
}

func (w *recordingDNSWriter) LocalAddr() net.Addr             { return &net.UDPAddr{} }
func (w *recordingDNSWriter) RemoteAddr() net.Addr            { return &net.UDPAddr{} }
func (w *recordingDNSWriter) WriteMsg(message *dns.Msg) error { w.message = message.Copy(); return nil }
func (w *recordingDNSWriter) Write(bytes []byte) (int, error) {
	message := new(dns.Msg)
	if err := message.Unpack(bytes); err != nil {
		return 0, err
	}
	w.message = message
	return len(bytes), nil
}
func (w *recordingDNSWriter) Close() error        { return nil }
func (w *recordingDNSWriter) TsigStatus() error   { return nil }
func (w *recordingDNSWriter) TsigTimersOnly(bool) {}
func (w *recordingDNSWriter) Hijack()             {}

type fakeManagedDNSServer struct {
	closed chan struct{}
	once   sync.Once
}

func newFakeManagedDNSServer() *fakeManagedDNSServer {
	return &fakeManagedDNSServer{closed: make(chan struct{})}
}

func (s *fakeManagedDNSServer) Serve() error {
	<-s.closed
	return nil
}

func (s *fakeManagedDNSServer) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestManagedDNSAnswersAuthoritativeVMRecords(t *testing.T) {
	handler := &managedDNSHandler{
		address: netip.MustParseAddr("192.168.100.1"),
		config:  bridgeDNSConfig{Enabled: true, Auto: true, Suffix: "br0.internal", Forwarders: []string{"1.1.1.1"}},
		records: func() map[string]netip.Addr {
			return map[string]netip.Addr{"web-1": netip.MustParseAddr("192.168.100.50")}
		},
	}
	request := new(dns.Msg)
	request.SetQuestion("web-1.br0.internal.", dns.TypeA)
	writer := &recordingDNSWriter{}
	handler.ServeDNS(writer, request)
	if writer.message == nil || !writer.message.Authoritative || writer.message.Rcode != dns.RcodeSuccess || len(writer.message.Answer) != 1 {
		t.Fatalf("unexpected authoritative response: %#v", writer.message)
	}
	record, ok := writer.message.Answer[0].(*dns.A)
	if !ok || record.A.String() != "192.168.100.50" {
		t.Fatalf("unexpected VM A record: %#v", writer.message.Answer)
	}

	request.SetQuestion("missing.br0.internal.", dns.TypeA)
	writer.message = nil
	handler.ServeDNS(writer, request)
	if writer.message == nil || writer.message.Rcode != dns.RcodeNameError || !writer.message.Authoritative || len(writer.message.Ns) != 1 {
		t.Fatalf("unknown local name was not answered authoritatively: %#v", writer.message)
	}
}

func TestManagedDNSForwardsExternalQueries(t *testing.T) {
	connection, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream := &dns.Server{PacketConn: connection, Handler: dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = append(response.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("203.0.113.9"),
		})
		_ = writer.WriteMsg(response)
	})}
	started := make(chan struct{})
	upstream.NotifyStartedFunc = func() { close(started) }
	go func() { _ = upstream.ActivateAndServe() }()
	<-started
	t.Cleanup(func() { _ = upstream.Shutdown() })

	handler := &managedDNSHandler{
		address: netip.MustParseAddr("192.168.100.1"),
		config:  bridgeDNSConfig{Enabled: true, Forwarders: []string{connection.LocalAddr().String()}},
	}
	request := new(dns.Msg)
	request.SetQuestion("example.test.", dns.TypeA)
	writer := &recordingDNSWriter{}
	handler.ServeDNS(writer, request)
	if writer.message == nil || writer.message.Rcode != dns.RcodeSuccess || len(writer.message.Answer) != 1 {
		t.Fatalf("query was not forwarded: %#v", writer.message)
	}
	if answer := writer.message.Answer[0].(*dns.A); answer.A.String() != "203.0.113.9" {
		t.Fatalf("unexpected forwarded response: %#v", answer)
	}
}

func TestDNSManagerStartsOnceAndStopsAfterClose(t *testing.T) {
	manager := newDNSManager()
	servers := make([]*fakeManagedDNSServer, 0)
	manager.newServer = func(netip.Addr, dns.Handler) (managedDNSServer, error) {
		server := newFakeManagedDNSServer()
		servers = append(servers, server)
		return server, nil
	}
	config := bridgeDNSConfig{Enabled: true, Forwarders: []string{"1.1.1.1"}}
	if err := manager.Start("br0", netip.MustParseAddr("192.168.100.1"), config, nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start("br0", netip.MustParseAddr("192.168.100.1"), config, nil); err != nil {
		t.Fatal(err)
	}
	if running, _ := manager.Status("br0"); len(servers) != 1 || !running {
		t.Fatalf("DNS manager did not reuse its bridge listener: servers=%d running=%v", len(servers), running)
	}
	if err := manager.Stop("br0"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-servers[0].closed:
	case <-time.After(time.Second):
		t.Fatal("DNS listener was not closed")
	}
}

func TestDNSConfigurationValidation(t *testing.T) {
	for _, invalid := range []string{"has space", "-leading", "trailing-", "under_score"} {
		if err := validateDNSLabel(invalid); err == nil {
			t.Fatalf("invalid DNS label %q was accepted", invalid)
		}
	}
	if suffix, err := normalizeDNSSuffix("BR0.Internal."); err != nil || suffix != "br0.internal" {
		t.Fatalf("DNS suffix was not normalized: %q, %v", suffix, err)
	}
	forwarders, err := normalizeDNSForwarders([]string{"1.1.1.1", "1.1.1.1", "[2001:4860:4860::8888]:5353"}, netip.MustParseAddr("192.168.100.1"))
	if err != nil || len(forwarders) != 2 || forwarders[1] != "[2001:4860:4860::8888]:5353" {
		t.Fatalf("DNS forwarders were not normalized: %v, %v", forwarders, err)
	}
}
