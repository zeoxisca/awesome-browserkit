package browserkit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// DNSResolver 解析浏览器请求的目标主机。实现必须尊重 context 取消。
type DNSResolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
}

type browserNetworkPolicy struct {
	allowPrivate   bool
	allowedOrigins map[string]struct{}
	resolver       DNSResolver
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // Shared address space (RFC 6598).
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), // Benchmark networks.
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func newBrowserNetworkPolicy(allowPrivate bool, origins []string, resolver DNSResolver) *browserNetworkPolicy {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if key, err := allowedOriginKey(origin); err == nil {
			allowed[key] = struct{}{}
		}
	}
	if allowPrivate && len(allowed) == 0 {
		return nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &browserNetworkPolicy{allowPrivate: allowPrivate, allowedOrigins: allowed, resolver: resolver}
}

func (policy *browserNetworkPolicy) requestPatterns() []*proto.FetchRequestPattern {
	pattern := &proto.FetchRequestPattern{URLPattern: "*", RequestStage: proto.FetchRequestStageRequest}
	if policy != nil && policy.allowPrivate {
		pattern.ResourceType = proto.NetworkResourceTypeDocument
	}
	return []*proto.FetchRequestPattern{pattern}
}

// check validates every HTTP(S) and WebSocket request at the CDP Fetch boundary.
// Document includes top-level navigations, redirect hops and iframe navigations;
// other resource types still receive DNS/private-network enforcement.
func (policy *browserNetworkPolicy) check(ctx context.Context, address string, resourceType proto.NetworkResourceType) error {
	if policy == nil {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return errors.New("目标不是合法网络 URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ws", "wss":
	default:
		// data:, blob: and about: do not open a new socket and are outside this policy.
		return nil
	}
	if parsed.User != nil || parsed.Hostname() == "" {
		return errors.New("目标不是合法网络 URL")
	}
	if resourceType == proto.NetworkResourceTypeDocument && len(policy.allowedOrigins) > 0 {
		origin, originErr := webOriginKey(parsed)
		if originErr != nil {
			return originErr
		}
		if _, allowed := policy.allowedOrigins[origin]; !allowed {
			return fmt.Errorf("Document origin %s 未获授权", origin)
		}
	}
	if policy.allowPrivate {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("目标解析到本机网络")
	}
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		literal = literal.Unmap()
		if !publicNetworkAddress(literal) {
			return fmt.Errorf("目标 IP %s 不是公网地址", literal)
		}
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addresses, err := policy.resolver.LookupIP(lookupCtx, "ip", host)
	if err != nil {
		return fmt.Errorf("解析目标主机 %s: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("目标主机 %s 没有可用 IP", host)
	}
	for _, address := range addresses {
		value, ok := netip.AddrFromSlice(address)
		if !ok || !publicNetworkAddress(value.Unmap()) {
			return fmt.Errorf("目标主机 %s 解析到非公网地址 %s", host, address)
		}
	}
	return nil
}

func publicNetworkAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

type networkPolicyClient struct {
	proto.Client
	ctx       context.Context
	sessionID proto.TargetSessionID
}

func (client networkPolicyClient) GetContext() context.Context         { return client.ctx }
func (client networkPolicyClient) GetSessionID() proto.TargetSessionID { return client.sessionID }

type networkPolicyJob struct {
	event     *proto.FetchRequestPaused
	sessionID proto.TargetSessionID
}

const networkPolicyWorkers = 32
const networkPolicyQueueSize = 256

func startBrowserNetworkPolicy(ctx context.Context, browser *rod.Browser, policy *browserNetworkPolicy) (func(), error) {
	if policy == nil || browser == nil {
		return func() {}, nil
	}
	policyCtx, cancel := context.WithCancel(ctx)
	if err := (proto.FetchEnable{Patterns: policy.requestPatterns()}).Call(browser.Context(policyCtx)); err != nil {
		cancel()
		return nil, err
	}
	jobs := make(chan networkPolicyJob, networkPolicyQueueSize)
	for range networkPolicyWorkers {
		go func() {
			for {
				select {
				case <-policyCtx.Done():
					return
				case job := <-jobs:
					enforceBrowserNetworkPolicy(policyCtx, browser, policy, job.event, job.sessionID)
				}
			}
		}()
	}
	wait := browser.Context(policyCtx).EachEvent(func(event *proto.FetchRequestPaused, sessionID proto.TargetSessionID) {
		if event == nil || event.Request == nil {
			return
		}
		// 固定 worker 和有界队列限制恶意页面能够制造的 DNS/CDP 并发量；
		// 队列满时对事件读取施加背压，而不是创建无上限 goroutine。
		select {
		case jobs <- networkPolicyJob{event: event, sessionID: sessionID}:
		case <-policyCtx.Done():
		}
	})
	go wait()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			disableCtx, disableCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer disableCancel()
			_ = (proto.FetchDisable{}).Call(browser.Context(disableCtx))
		})
	}, nil
}

func enforceBrowserNetworkPolicy(ctx context.Context, browser proto.Client, policy *browserNetworkPolicy, event *proto.FetchRequestPaused, sessionID proto.TargetSessionID) {
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := networkPolicyClient{Client: browser, ctx: requestCtx, sessionID: sessionID}
	if err := policy.check(requestCtx, event.Request.URL, event.ResourceType); err != nil {
		_ = (proto.FetchFailRequest{RequestID: event.RequestID, ErrorReason: proto.NetworkErrorReasonBlockedByClient}).Call(client)
		return
	}
	_ = (proto.FetchContinueRequest{RequestID: event.RequestID}).Call(client)
}
