package httpseek

import "testing"

func TestParseSingleRange(t *testing.T) {
	cases := []struct {
		in         string
		start, end int64
		ok         bool
	}{
		{"bytes=0-499", 0, 499, true},
		{"bytes=500-", 500, -1, true},
		{"bytes=-500", -1, 500, true},
		{"0-499", 0, 0, false},
		{"bytes=", 0, 0, false},
		{"bytes=-", 0, 0, false},
		{"bytes=-0", 0, 0, false},
		{"bytes=-abc", 0, 0, false},
		{"bytes=a-499", 0, 0, false},
		{"bytes=0-b", 0, 0, false},
		{"bytes=0-499,600-999", 0, 0, false},
	}
	for _, tc := range cases {
		start, end, ok := parseSingleRange(tc.in)
		if start != tc.start || end != tc.end || ok != tc.ok {
			t.Errorf("parseSingleRange(%q) = (%d, %d, %v), want (%d, %d, %v)",
				tc.in, start, end, ok, tc.start, tc.end, tc.ok)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in                string
		start, end, total int64
		ok                bool
	}{
		{"bytes 0-499/1234", 0, 499, 1234, true},
		{"bytes 0-499/*", 0, 499, -1, true},
		{"0-499/1234", 0, 0, 0, false},
		{"bytes -499/1234", 0, 0, 0, false},
		{"bytes 0499/1234", 0, 0, 0, false},
		{"bytes a-499/1234", 0, 0, 0, false},
		{"bytes 0-/1234", 0, 0, 0, false},
		{"bytes 0-499", 0, 0, 0, false},
		{"bytes 0-b/1234", 0, 0, 0, false},
		{"bytes 0-499/", 0, 0, 0, false},
		{"bytes 0-499/x", 0, 0, 0, false},
	}
	for _, tc := range cases {
		start, end, total, ok := parseContentRange(tc.in)
		if start != tc.start || end != tc.end || total != tc.total || ok != tc.ok {
			t.Errorf("parseContentRange(%q) = (%d, %d, %d, %v), want (%d, %d, %d, %v)",
				tc.in, start, end, total, ok, tc.start, tc.end, tc.total, tc.ok)
		}
	}
}

func TestParseUnsatisfiedContentRange(t *testing.T) {
	cases := []struct {
		in    string
		total int64
		ok    bool
	}{
		{"bytes */1234", 1234, true},
		{"bytes */0", 0, true},
		{"bytes 0-499/1234", 0, false},
		{"bytes */*", 0, false},
		{"bytes */-1", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		total, ok := parseUnsatisfiedContentRange(tc.in)
		if total != tc.total || ok != tc.ok {
			t.Errorf("parseUnsatisfiedContentRange(%q) = (%d, %v), want (%d, %v)",
				tc.in, total, ok, tc.total, tc.ok)
		}
	}
}
