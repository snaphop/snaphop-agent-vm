package image

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"testing"
)

func TestBootableKernel_LeavesAPlainKernelUntouched(t *testing.T) {
	t.Parallel()
	image, payload, err := bootableKernel([]byte("not a kernel, and not a PE\n"))
	if err != nil {
		t.Fatalf("bootableKernel: %v", err)
	}
	if image != nil || payload != nil {
		t.Fatalf("image=%q payload=%d, want both unset", image, len(payload))
	}
}

func TestBootableKernel_LeavesAnMZKernelWithoutALinuxSectionUntouched(t *testing.T) {
	t.Parallel()
	// An x86 bzImage starts with MZ. It has no .linux section, and direct
	// boot already runs it.
	data := peWithSections([]namedBytes{{name: ".text", data: []byte("bzImage")}})
	image, payload, err := bootableKernel(data)
	if err != nil {
		t.Fatalf("bootableKernel: %v", err)
	}
	if image != nil || payload != nil {
		t.Fatalf("a PE without .linux was rewritten (image %d bytes, payload %d bytes)", len(image), len(payload))
	}
}

func TestBootableKernel_ReturnsTheLinuxSectionWhenItIsAlreadyAKernel(t *testing.T) {
	t.Parallel()
	want := []byte("raw-kernel-image")
	data := peWithSections([]namedBytes{{name: ".linux", data: want}})
	image, payload, err := bootableKernel(data)
	if err != nil {
		t.Fatalf("bootableKernel: %v", err)
	}
	if payload != nil {
		t.Fatal("a raw .linux section was treated as a compressed payload")
	}
	if !bytes.Equal(image, want) {
		t.Fatalf("kernel = %q, want %q", image, want)
	}
}

func TestBootableKernel_DecompressesAGzipZbootPayload(t *testing.T) {
	t.Parallel()
	want := []byte("gzip-direct-boot-kernel")
	data := peWithSections([]namedBytes{{name: ".linux", data: efiZboot("gzip", gzipBytes(t, want), 0)}})
	image, payload, err := bootableKernel(data)
	if err != nil {
		t.Fatalf("bootableKernel: %v", err)
	}
	if payload != nil {
		t.Fatal("gzip payload was handed to zstd")
	}
	if !bytes.Equal(image, want) {
		t.Fatalf("kernel = %q, want %q", image, want)
	}
}

func TestBootableKernel_IgnoresBytesTrailingAGzipPayload(t *testing.T) {
	t.Parallel()
	want := []byte("gzip-with-size-trailer")
	// The kernel's zboot linker appends the uncompressed size after some
	// compression streams. gzip stops at the end of its member.
	data := peWithSections([]namedBytes{{name: ".linux", data: efiZboot("gzip", gzipBytes(t, want), 4)}})
	image, _, err := bootableKernel(data)
	if err != nil {
		t.Fatalf("bootableKernel: %v", err)
	}
	if !bytes.Equal(image, want) {
		t.Fatalf("kernel = %q, want %q", image, want)
	}
}

func TestBootableKernel_ReturnsAZstdPayloadForTheCallerToDecompress(t *testing.T) {
	t.Parallel()
	frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x01, 0x02, 0x03}
	data := peWithSections([]namedBytes{{name: ".linux", data: efiZboot("zstd", frame, 4)}})
	image, payload, err := bootableKernel(data)
	if err != nil {
		t.Fatalf("bootableKernel: %v", err)
	}
	if image != nil {
		t.Fatal("zstd payload was decompressed in process")
	}
	if !bytes.Equal(payload, append(append([]byte{}, frame...), 0, 0, 0, 0)) {
		t.Fatalf("payload = %x, want the frame plus its size trailer", payload)
	}
}

func TestBootableKernel_RejectsTwoLinuxSections(t *testing.T) {
	t.Parallel()
	data := peWithSections([]namedBytes{
		{name: ".linux", data: []byte("one")},
		{name: ".linux", data: []byte("two")},
	})
	if _, _, err := bootableKernel(data); err == nil {
		t.Fatal("two .linux sections were accepted")
	}
}

func TestBootableKernel_RejectsAPayloadThatExtendsPastTheSection(t *testing.T) {
	t.Parallel()
	zboot := efiZboot("gzip", []byte("short"), 0)
	// Claim a payload larger than the section. The header's size field is
	// what a corrupt image would lie about.
	binary.LittleEndian.PutUint32(zboot[12:16], uint32(len(zboot)))
	data := peWithSections([]namedBytes{{name: ".linux", data: zboot}})
	if _, _, err := bootableKernel(data); err == nil {
		t.Fatal("a payload that runs past the section was accepted")
	}
}

func TestBootableKernel_RejectsAnUnknownCompression(t *testing.T) {
	t.Parallel()
	data := peWithSections([]namedBytes{{name: ".linux", data: efiZboot("xz", []byte("payload"), 0)}})
	if _, _, err := bootableKernel(data); err == nil {
		t.Fatal("xz compression was accepted")
	}
}

func TestBootableKernel_RejectsAnEmptyLinuxSection(t *testing.T) {
	t.Parallel()
	data := peWithSections([]namedBytes{{name: ".linux", data: nil}})
	if _, _, err := bootableKernel(data); err == nil {
		t.Fatal("an empty .linux section was accepted")
	}
}

type namedBytes struct {
	name string
	data []byte
}

// peWithSections builds the smallest PE debug/pe will parse, with the named
// sections laid out in order. It exists so the UKI tests do not need a real
// kernel image.
func peWithSections(sections []namedBytes) []byte {
	const (
		peOffset = 64
		coffSize = 20
		secSize  = 40
	)
	headerSize := peOffset + 4 + coffSize + len(sections)*secSize
	body := []byte{}
	type placed struct {
		name string
		off  int
		size int
	}
	placedSecs := make([]placed, 0, len(sections))
	raw := headerSize
	for _, section := range sections {
		placedSecs = append(placedSecs, placed{section.name, raw, len(section.data)})
		body = append(body, section.data...)
		raw += len(section.data)
	}

	buf := bytes.NewBuffer(make([]byte, 0, headerSize+len(body)))
	dos := make([]byte, peOffset)
	dos[0], dos[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(dos[0x3c:], peOffset)
	buf.Write(dos)
	buf.WriteString("PE\x00\x00")
	writeBinary(buf, uint16(0xAA64)) // IMAGE_FILE_MACHINE_ARM64
	writeBinary(buf, uint16(len(sections)))
	writeBinary(buf, uint32(0)) // time
	writeBinary(buf, uint32(0)) // symbol table
	writeBinary(buf, uint32(0)) // symbol count
	writeBinary(buf, uint16(0)) // optional header size
	writeBinary(buf, uint16(0)) // characteristics
	for _, section := range placedSecs {
		name := make([]byte, 8)
		copy(name, section.name)
		buf.Write(name)
		writeBinary(buf, uint32(section.size))
		writeBinary(buf, uint32(0x1000))
		writeBinary(buf, uint32(section.size))
		writeBinary(buf, uint32(section.off))
		buf.Write(make([]byte, 16))
	}
	buf.Write(body)
	return buf.Bytes()
}

// efiZboot builds a Linux EFI zboot header around payload. trailer extra
// bytes are included in the payload size, matching the size word the kernel's
// zboot linker appends for some compressors.
func efiZboot(compression string, payload []byte, trailer int) []byte {
	body := append(append([]byte{}, payload...), make([]byte, trailer)...)
	header := make([]byte, 64+len(body))
	header[0], header[1] = 'M', 'Z'
	copy(header[4:8], "zimg")
	binary.LittleEndian.PutUint32(header[8:12], 64)
	binary.LittleEndian.PutUint32(header[12:16], uint32(len(body)))
	copy(header[24:56], compression)
	copy(header[56:60], efiZbootLinuxMagic)
	copy(header[64:], body)
	return header
}

func gzipBytes(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(plain); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func writeBinary(buf *bytes.Buffer, value any) {
	var scratch [8]byte
	switch v := value.(type) {
	case uint16:
		binary.LittleEndian.PutUint16(scratch[:2], v)
		buf.Write(scratch[:2])
	case uint32:
		binary.LittleEndian.PutUint32(scratch[:4], v)
		buf.Write(scratch[:4])
	default:
		buf.WriteString("unsupported test integer")
	}
}
