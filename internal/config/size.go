package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Size is a byte count parsed from an operator-supplied value such as "4G".
// Sizes are parsed and bounded rather than passed through as strings, because
// they end up as arguments to qemu-img and virt-install (SECURITY.md, "Input,
// Output, And Subprocess Execution").
type Size int64

// Binary units, matching what qemu-img and virt-install mean by these suffixes.
const (
	KiB Size = 1 << 10
	MiB Size = 1 << 20
	GiB Size = 1 << 30
	TiB Size = 1 << 40
)

// sizeNumber is the number part of a size: plain decimal digits with an
// optional fraction. It is checked before strconv.ParseFloat sees it, because
// that function also accepts "NaN", "Inf", hexadecimal, and exponents, none of
// which is a size anyone writes, and NaN converts to an arbitrary integer.
var sizeNumber = regexp.MustCompile(`^([0-9]+(\.[0-9]*)?|\.[0-9]+)$`)

// ParseSize reads "512M", "4G", "4 G", "50GiB", or a bare byte count. Suffixes
// are binary (1G is 1024 MiB), which is what the tools we hand these values to
// use.
func ParseSize(s string) (Size, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}

	number := strings.TrimRight(trimmed, "kKmMgGtTiIbB")
	suffix := strings.ToLower(trimmed[len(number):])
	// One space may separate the number from its unit ("4 G"); it belongs to
	// neither.
	digits := strings.TrimSuffix(number, " ")

	if !sizeNumber.MatchString(digits) {
		return 0, fmt.Errorf("invalid size %q: expected a number with an optional K, M, G, or T suffix", s)
	}
	value, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: expected a number with an optional K, M, G, or T suffix", s)
	}

	var unit Size
	switch suffix {
	case "", "b":
		unit = 1
	case "k", "kb", "kib":
		unit = KiB
	case "m", "mb", "mib":
		unit = MiB
	case "g", "gb", "gib":
		unit = GiB
	case "t", "tb", "tib":
		unit = TiB
	default:
		return 0, fmt.Errorf("invalid size %q: unknown unit %q, expected K, M, G, or T", s, suffix)
	}

	scaled := value * float64(unit)
	if scaled > float64(1<<62) {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return Size(scaled), nil
}

// String renders the size the way an operator would write it, preferring the
// largest unit that divides evenly.
func (s Size) String() string {
	switch {
	case s == 0:
		return "0"
	case s%TiB == 0:
		return fmt.Sprintf("%dT", s/TiB)
	case s%GiB == 0:
		return fmt.Sprintf("%dG", s/GiB)
	case s%MiB == 0:
		return fmt.Sprintf("%dM", s/MiB)
	case s%KiB == 0:
		return fmt.Sprintf("%dK", s/KiB)
	default:
		return strconv.FormatInt(int64(s), 10)
	}
}

// Human renders the size for a person reading a report, where "1.7T" is more
// useful than the exact byte count String would preserve. It is display only —
// never use it for a value that will be parsed back.
func (s Size) Human() string {
	units := []struct {
		unit   Size
		suffix string
	}{{TiB, "T"}, {GiB, "G"}, {MiB, "M"}, {KiB, "K"}}
	for _, u := range units {
		if s >= u.unit {
			return strconv.FormatFloat(float64(s)/float64(u.unit), 'f', 1, 64) + u.suffix
		}
	}
	return strconv.FormatInt(int64(s), 10) + "B"
}

// MiBValue is the size in whole MiB, the unit virt-install's --memory takes.
func (s Size) MiBValue() int64 { return int64(s / MiB) }

// MarshalText makes Size round-trip through JSON state files as "4G" rather
// than as a bare byte count, so vm.json stays readable by an operator.
func (s Size) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText parses the form MarshalText writes.
func (s *Size) UnmarshalText(b []byte) error {
	parsed, err := ParseSize(string(b))
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}
