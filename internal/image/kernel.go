package image

import (
	"bytes"
	"compress/gzip"
	"context"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// maxKernelBytes bounds a kernel this build is willing to cache. The section
// table of a unified kernel image is untrusted input from the image just
// built, and a corrupt size must not make the build read without a limit.
const maxKernelBytes = 256 << 20

// efiZbootLinuxMagic is the Linux header magic an EFI zboot image carries.
// QEMU's loader uses the same four bytes to recognize the format.
var efiZbootLinuxMagic = []byte{0xcd, 0x23, 0x82, 0x81}

// unpackUnifiedKernel rewrites a cached kernel that is a unified kernel image
// into the file direct kernel boot can run.
//
// Ubuntu 26.04 ships /boot/vmlinuz as a UKI: a PE executable whose entry point
// is an EFI stub. Direct kernel boot starts that file as a Linux kernel, the
// stub never runs, and the guest agent never connects. The bootable kernel is
// the UKI's .linux section. When that section is itself an EFI zboot image,
// its payload is decompressed here. QEMU 8 direct-boots a raw Image and a
// gzip zboot image; a zstd payload, which is what this Ubuntu kernel carries,
// needs a newer QEMU, so the build decompresses it and caches the raw kernel.
// A kernel that is not a UKI is left byte for byte as virt-copy-out wrote it.
func (b *Builder) unpackUnifiedKernel(ctx context.Context, dir, kernelPath string) error {
	data, err := b.Store.ReadFile(kernelPath)
	if err != nil {
		return fmt.Errorf("reading the extracted kernel: %w", err)
	}
	image, payload, err := bootableKernel(data)
	if err != nil {
		return err
	}
	switch {
	case payload != nil:
		return b.decompressZstdKernel(ctx, dir, kernelPath, payload)
	case image != nil:
		if err := b.Store.WriteFile(kernelPath, image, 0o644); err != nil {
			return fmt.Errorf("writing the direct-boot kernel: %w", err)
		}
	}
	return nil
}

// bootableKernel reports the kernel direct boot should cache.
//
// image is set when the caller should replace the file with those bytes.
// payload is set when those bytes are a zstd frame the caller must decompress.
// Both are nil when data is already a kernel direct boot can run.
func bootableKernel(data []byte) (image, payload []byte, err error) {
	if len(data) < 64 || data[0] != 'M' || data[1] != 'Z' {
		return nil, nil, nil
	}
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		// An MZ file that is not a PE we can parse is left alone. An x86
		// bzImage starts with MZ and is already what direct boot runs.
		return nil, nil, nil
	}
	var section *pe.Section
	for _, candidate := range f.Sections {
		if candidate.Name != ".linux" {
			continue
		}
		if section != nil {
			return nil, nil, fmt.Errorf("the extracted kernel is a unified kernel image with more than one .linux section")
		}
		section = candidate
	}
	if section == nil {
		return nil, nil, nil
	}
	if section.Size == 0 || section.Size > maxKernelBytes {
		return nil, nil, fmt.Errorf("the unified kernel's .linux section is %d bytes; direct boot expects a kernel between 1 and %d bytes", section.Size, maxKernelBytes)
	}
	raw, err := section.Data()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the unified kernel's .linux section: %w", err)
	}
	return unpackEFIZboot(raw)
}

// unpackEFIZboot returns the kernel inside an EFI zboot image.
//
// A .linux section that is not zboot is already the kernel, and is returned
// as image. A gzip payload is decompressed here. A zstd payload is returned
// for the caller to decompress with the zstd tool (ADR-0009).
func unpackEFIZboot(data []byte) (image, payload []byte, err error) {
	if !isEFIZboot(data) {
		return data, nil, nil
	}
	offset := binary.LittleEndian.Uint32(data[8:12])
	size := binary.LittleEndian.Uint32(data[12:16])
	if uint64(offset)+uint64(size) > uint64(len(data)) {
		return nil, nil, fmt.Errorf("the unified kernel's compressed payload extends past its .linux section")
	}
	compressed := data[offset : offset+size]
	switch compressionName(data[24:56]) {
	case "gzip":
		plain, err := gunzipKernel(compressed)
		if err != nil {
			return nil, nil, err
		}
		return plain, nil, nil
	case "zstd":
		if len(compressed) == 0 {
			return nil, nil, fmt.Errorf("the unified kernel's zstd payload is empty")
		}
		return nil, compressed, nil
	default:
		return nil, nil, fmt.Errorf("the unified kernel uses %q compression; direct boot unpacks gzip and zstd", compressionName(data[24:56]))
	}
}

func isEFIZboot(data []byte) bool {
	return len(data) >= 64 &&
		data[0] == 'M' && data[1] == 'Z' &&
		bytes.Equal(data[4:8], []byte("zimg")) &&
		bytes.Equal(data[56:60], efiZbootLinuxMagic)
}

func compressionName(field []byte) string {
	if i := bytes.IndexByte(field, 0); i >= 0 {
		field = field[:i]
	}
	if len(field) == 0 || len(field) > 31 {
		return "unrecognized"
	}
	for _, c := range field {
		if c < 'a' || c > 'z' {
			return "unrecognized"
		}
	}
	return string(field)
}

func gunzipKernel(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("the unified kernel's gzip payload is not gzip data: %w", err)
	}
	// The kernel's zboot linker appends the uncompressed size after some
	// members. A second member is not part of the kernel.
	reader.Multistream(false)
	plain, err := io.ReadAll(io.LimitReader(reader, maxKernelBytes+1))
	closeErr := reader.Close()
	if err != nil {
		return nil, fmt.Errorf("decompressing the unified kernel's gzip payload: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing the unified kernel's gzip payload: %w", closeErr)
	}
	if len(plain) == 0 || len(plain) > maxKernelBytes {
		return nil, fmt.Errorf("the decompressed kernel is %d bytes; direct boot expects a kernel between 1 and %d bytes", len(plain), maxKernelBytes)
	}
	return plain, nil
}

// decompressZstdKernel replaces kernelPath with the raw kernel inside payload.
//
// zstd runs on the hypervisor. The payload file is written through the state
// directory's FS, which is the hypervisor's filesystem for a qemu+ssh://
// connection, and zstd is a hypervisor-located command for the same reason
// the rest of the image build is (ADR-0010).
func (b *Builder) decompressZstdKernel(ctx context.Context, dir, kernelPath string, payload []byte) (err error) {
	payloadPath := filepath.Join(dir, "kernel.zstd")
	unpacked := kernelPath + ".unpacked"
	if err := b.Store.WriteFile(payloadPath, payload, 0o600); err != nil {
		return fmt.Errorf("writing the unified kernel's zstd payload: %w", err)
	}
	defer func() {
		if rmErr := b.Store.Remove(payloadPath); rmErr != nil && err == nil {
			err = fmt.Errorf("removing the temporary zstd payload: %w", rmErr)
		}
	}()

	if _, err := b.run(ctx, hostexec.Command{
		Name: "zstd",
		Args: []string{
			"--decompress",
			"--force",
			"-o", unpacked,
			payloadPath,
		},
		Effect:  hostexec.Mutate,
		Timeout: 2 * time.Minute,
	}); err != nil {
		_ = b.Store.Remove(unpacked)
		return fmt.Errorf("unpacking the unified kernel's zstd payload: install zstd on the hypervisor: %w", err)
	}

	size, err := b.Store.FileSize(unpacked)
	if err != nil {
		return fmt.Errorf("zstd did not write the unpacked kernel: %w", err)
	}
	if size == 0 || size > maxKernelBytes {
		_ = b.Store.Remove(unpacked)
		return fmt.Errorf("the unpacked kernel is %d bytes; direct boot expects a kernel between 1 and %d bytes", size, maxKernelBytes)
	}
	if err := b.Store.Rename(unpacked, kernelPath); err != nil {
		_ = b.Store.Remove(unpacked)
		return fmt.Errorf("installing the unpacked kernel: %w", err)
	}
	return nil
}
