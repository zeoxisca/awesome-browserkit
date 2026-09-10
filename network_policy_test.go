package browserkit

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

type policyResolver struct {
	addresses map[string][]net.IP
	err       error
}

func (resolver policyResolver) LookupIP(_ context.Context, _, host string) ([]net.IP, error) {
	if resolver.err != nil {
		return nil, resolver.err
	}
	return resolver.addresses[host], nil
}

func TestBrowserNetworkPolicyRejectsPrivateDNSForEveryResource(t *testing.T) {
	policy := newBrowserNetworkPolicy(false, nil, policyResolver{addresses: map[string][]net.IP{
		"public.test":   {net.ParseIP("93.184.216.34")},
		"rebind.test":   {net.ParseIP("127.0.0.1")},
		"mixed.test":    {net.ParseIP("93.184.216.34"), net.ParseIP("169.254.169.254")},
		"metadata.test": {net.ParseIP("169.254.169.254")},
	}})
	for _, test := range []struct {
		name, address string
		resource      proto.NetworkResourceType
		blocked       bool
	}{
		{name: "local data URL", address: "data:text/plain,ok", resource: proto.NetworkResourceTypeImage},
		{name: "public document", address: "https://public.test/", resource: proto.NetworkResourceTypeDocument},
		{name: "private literal", address: "http://127.0.0.1/admin", resource: proto.NetworkResourceTypeDocument, blocked: true},
		{name: "mapped private literal", address: "http://[::ffff:127.0.0.1]/admin", resource: proto.NetworkResourceTypeDocument, blocked: true},
		{name: "localhost subdomain", address: "http://api.localhost/data", resource: proto.NetworkResourceTypeXHR, blocked: true},
		{name: "carrier grade NAT", address: "http://100.64.0.1/", resource: proto.NetworkResourceTypeDocument, blocked: true},
		{name: "private DNS subresource", address: "https://rebind.test/app.js", resource: proto.NetworkResourceTypeScript, blocked: true},
		{name: "mixed DNS answers", address: "https://mixed.test/style.css", resource: proto.NetworkResourceTypeStylesheet, blocked: true},
		{name: "metadata DNS", address: "http://metadata.test/latest", resource: proto.NetworkResourceTypeFetch, blocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := policy.check(context.Background(), test.address, test.resource)
			if (err != nil) != test.blocked {
				t.Fatalf("check(%q) error=%v blocked=%t", test.address, err, test.blocked)
			}
		})
	}
}

func TestBrowserNetworkPolicyChecksRedirectDocumentsAgainstAllowedOrigins(t *testing.T) {
	resolver := policyResolver{addresses: map[string][]net.IP{
		"allowed.test":  {net.ParseIP("93.184.216.34")},
		"redirect.test": {net.ParseIP("93.184.216.35")},
	}}
	policy := newBrowserNetworkPolicy(false, []string{"https://allowed.test"}, resolver)
	if err := policy.check(context.Background(), "https://allowed.test/start", proto.NetworkResourceTypeDocument); err != nil {
		t.Fatal(err)
	}
	if err := policy.check(context.Background(), "https://redirect.test/final", proto.NetworkResourceTypeDocument); err == nil {
		t.Fatal("redirect Document escaped the allowed-origin policy")
	}
	if err := policy.check(context.Background(), "https://redirect.test/app.js", proto.NetworkResourceTypeScript); err != nil {
		t.Fatalf("public cross-origin subresource should remain available: %v", err)
	}
}

func TestBrowserNetworkPolicyFailsClosedOnDNSFailure(t *testing.T) {
	policy := newBrowserNetworkPolicy(false, nil, policyResolver{err: errors.New("DNS unavailable")})
	if err := policy.check(context.Background(), "https://example.test/", proto.NetworkResourceTypeDocument); err == nil {
		t.Fatal("DNS failure was allowed")
	}
	policy = newBrowserNetworkPolicy(true, nil, policyResolver{err: errors.New("DNS unavailable")})
	if err := policy.check(context.Background(), "https://example.test/", proto.NetworkResourceTypeDocument); err != nil {
		t.Fatalf("explicit private-network permission should bypass DNS policy: %v", err)
	}
}

type policyCDPCall struct {
	session string
	method  string
}

type policyCDPClient struct {
	mu    sync.Mutex
	calls []policyCDPCall
}

func (client *policyCDPClient) Call(_ context.Context, sessionID, method string, _ interface{}) ([]byte, error) {
	client.mu.Lock()
	client.calls = append(client.calls, policyCDPCall{session: sessionID, method: method})
	client.mu.Unlock()
	return []byte(`{}`), nil
}

func TestEnforceBrowserNetworkPolicyContinuesOrFailsPausedRequest(t *testing.T) {
	policy := newBrowserNetworkPolicy(false, nil, policyResolver{addresses: map[string][]net.IP{
		"public.test":  {net.ParseIP("93.184.216.34")},
		"private.test": {net.ParseIP("10.0.0.1")},
	}})
	client := &policyCDPClient{}
	for index, address := range []string{"https://public.test/app.js", "https://private.test/secret"} {
		event := &proto.FetchRequestPaused{
			RequestID:    proto.FetchRequestID(string(rune('a' + index))),
			Request:      &proto.NetworkRequest{URL: address},
			ResourceType: proto.NetworkResourceTypeScript,
		}
		enforceBrowserNetworkPolicy(context.Background(), client, policy, event, proto.TargetSessionID("target-session"))
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 2 || client.calls[0].method != "Fetch.continueRequest" || client.calls[1].method != "Fetch.failRequest" {
		t.Fatalf("unexpected CDP calls: %+v", client.calls)
	}
	for _, call := range client.calls {
		if call.session != "target-session" {
			t.Fatalf("request resumed in wrong CDP session: %+v", call)
		}
	}
}
