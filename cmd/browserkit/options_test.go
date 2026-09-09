package main

import "testing"

func TestModes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		options modeOptions
		want    string
	}{
		{"default", modeOptions{}, "new"},
		{"remote chrome in new", modeOptions{mode: "new", chrome: "ws://localhost:9222/devtools/browser/test"}, ""},
		{"attach", modeOptions{mode: "attach", remote: "http://localhost:9222", pageURL: "https://example.com/"}, "attach"},
		{"remote alias", modeOptions{remote: "ws://localhost:9222/devtools/browser/test", pageURL: "https://example.com/"}, "attach"},
		{"unknown", modeOptions{mode: "other"}, ""},
		{"missing CDP", modeOptions{mode: "attach", pageURL: "https://example.com/"}, ""},
		{"active page", modeOptions{mode: "attach", remote: "http://localhost:9222"}, "attach"},
		{"conflict", modeOptions{mode: "new", remote: "http://localhost:9222"}, ""},
		{"launch in attach", modeOptions{mode: "attach", chrome: "chrome", remote: "http://localhost:9222", pageURL: "https://example.com/"}, ""},
		{"inspect is not target", modeOptions{mode: "attach", remote: "http://localhost:9222", pageURL: "chrome://inspect"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.options.validate()
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Fatalf("%s %v", got, err)
			}
		})
	}
}
