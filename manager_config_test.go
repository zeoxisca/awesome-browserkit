package browserkit

import "testing"

func TestLiveQualityOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   LiveQuality
		wantFPS int
		wantQ   int
		wantNth int
	}{
		{name: "low", value: LiveQualityLow, wantFPS: 12, wantQ: 50, wantNth: 2},
		{name: "medium", value: LiveQualityMedium, wantFPS: 12, wantQ: 75, wantNth: 1},
		{name: "high", value: LiveQualityHigh, wantFPS: 20, wantQ: 100, wantNth: 1},
		{name: "ultra", value: LiveQualityUltra, wantFPS: 30, wantQ: 100, wantNth: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fps, quality, everyNthFrame := (Config{LiveQuality: test.value}).liveOptions()
			if fps != test.wantFPS || quality != test.wantQ || everyNthFrame != test.wantNth {
				t.Fatalf("liveOptions()=(%d, %d, %d), want (%d, %d, %d)", fps, quality, everyNthFrame, test.wantFPS, test.wantQ, test.wantNth)
			}
		})
	}
}

func TestLiveQualityDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	config, err := (Config{}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if config.LiveQuality != LiveQualityMedium {
		t.Fatalf("default LiveQuality=%q", config.LiveQuality)
	}
	if err := (Config{LiveQuality: "unexpected"}).Validate(); err == nil {
		t.Fatal("未知直播质量档位未被拒绝")
	}
}

func TestAllowedOriginsUseCanonicalWebOrigins(t *testing.T) {
	t.Parallel()
	manager, err := NewManager(Config{AllowPrivateNetwork: true, AllowedOrigins: []string{"HTTPS://Example.COM:443/"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(nil) })
	if err := manager.validateAddress("HTTPS://example.com/path", ""); err != nil {
		t.Fatalf("默认端口和大小写归一化后未匹配: %v", err)
	}
	if err := manager.validateAddress("https://example.com:444/path", ""); err == nil {
		t.Fatal("不同端口不应当成同一 origin")
	}
}

func TestAllowedOriginsRejectNonOriginComponents(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{
		"https://user@example.com",
		"https://example.com/path",
		"https://example.com?query=1",
		"https://example.com#fragment",
	} {
		if err := (Config{AllowedOrigins: []string{origin}}).Validate(); err == nil {
			t.Fatalf("AllowedOrigin %q 应被拒绝", origin)
		}
	}
}
