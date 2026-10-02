package config

import "testing"

func TestParseSize_AcceptsTheFormsOperatorsWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want Size
	}{
		{"512M", 512 * MiB},
		{"4G", 4 * GiB},
		{"50G", 50 * GiB},
		{"50GiB", 50 * GiB},
		{"50gb", 50 * GiB},
		{"2T", 2 * TiB},
		{"1024", 1024},
		{"1.5G", 1536 * MiB},
		{" 4G ", 4 * GiB},
	}
	for _, tt := range tests {
		got, err := ParseSize(tt.in)
		if err != nil {
			t.Errorf("ParseSize(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseSize_RejectsMalformedValues(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "G", "abc", "4X", "-4G", "4 4G", "0x10"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want an error", in, got)
		}
	}
}

func TestSizeString_RoundTrips(t *testing.T) {
	t.Parallel()
	tests := []struct {
		size Size
		want string
	}{
		{4 * GiB, "4G"},
		{512 * MiB, "512M"},
		{50 * GiB, "50G"},
		{2 * TiB, "2T"},
		{1 * KiB, "1K"},
		{1500, "1500"},
	}
	for _, tt := range tests {
		if got := tt.size.String(); got != tt.want {
			t.Errorf("Size(%d).String() = %q, want %q", tt.size, got, tt.want)
		}
		back, err := ParseSize(tt.want)
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", tt.want, err)
		}
		if back != tt.size {
			t.Errorf("ParseSize(%q) = %d, want %d", tt.want, back, tt.size)
		}
	}
}

func TestSizeMiBValue_IsWhatVirtInstallMemoryTakes(t *testing.T) {
	t.Parallel()
	if got := (4 * GiB).MiBValue(); got != 4096 {
		t.Errorf("(4G).MiBValue() = %d, want 4096", got)
	}
}
