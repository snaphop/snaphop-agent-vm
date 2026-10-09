package hostexec

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const bootKernelFind = "find /boot -maxdepth 1 -name vmlinuz-* -print"

func TestCheckBootKernels_ZeroKernelsIsReadable(t *testing.T) {
	t.Parallel()
	fake := NewFake()

	n, err := CheckBootKernels(context.Background(), fake)
	if err != nil || n != 0 {
		t.Fatalf("CheckBootKernels() = %d, %v; want 0, nil", n, err)
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "test ") {
			t.Errorf("test ran when /boot listed no kernels: %s", argv)
		}
	}
}

func TestCheckBootKernels_CountsReadableKernels(t *testing.T) {
	t.Parallel()
	fake := NewFake().Respond(bootKernelFind, FakeResponse{
		Stdout: "/boot/vmlinuz-7.0.0-34-generic\n/boot/vmlinuz-7.0.0-38-generic\n",
	})

	n, err := CheckBootKernels(context.Background(), fake)
	if err != nil || n != 2 {
		t.Fatalf("CheckBootKernels() = %d, %v; want 2, nil", n, err)
	}
	for _, path := range []string{"/boot/vmlinuz-7.0.0-34-generic", "/boot/vmlinuz-7.0.0-38-generic"} {
		if !fake.Ran("test -r " + path) {
			t.Errorf("did not test %s", path)
		}
	}
}

func TestCheckBootKernels_ReportsTheFirstUnreadableKernel(t *testing.T) {
	t.Parallel()
	const unreadable = "/boot/vmlinuz-7.0.0-34-generic"
	fake := NewFake().
		Respond(bootKernelFind, FakeResponse{
			Stdout: unreadable + "\n/boot/vmlinuz-7.0.0-38-generic\n",
		}).
		Respond("test -r "+unreadable, FakeResponse{ExitCode: 1})

	n, err := CheckBootKernels(context.Background(), fake)
	if n != 1 {
		t.Errorf("checked %d kernels, want 1", n)
	}
	var unread *UnreadableKernelError
	if !errors.As(err, &unread) || unread.Path != unreadable {
		t.Fatalf("err = %v, want UnreadableKernelError for %s", err, unreadable)
	}
	if !strings.Contains(err.Error(), "chmod 0644") || !strings.Contains(unread.Remedy(), "initrd") {
		t.Errorf("the error does not say what to run:\n%s", err)
	}
	if fake.Ran("test -r /boot/vmlinuz-7.0.0-38-generic") {
		t.Error("kept testing kernels after one was unreadable")
	}
}

func TestCheckBootKernels_IgnoresPathsOutsideBoot(t *testing.T) {
	t.Parallel()
	fake := NewFake().Respond(bootKernelFind, FakeResponse{
		Stdout: strings.Join([]string{
			"/tmp/vmlinuz-stolen",
			"/boot/../etc/shadow",
			"/boot/vmlinuz",
			"vmlinuz-7.0.0-38-generic",
			"/boot/vmlinuz-7.0.0-38-generic",
		}, "\n"),
	})

	n, err := CheckBootKernels(context.Background(), fake)
	if err != nil || n != 1 {
		t.Fatalf("CheckBootKernels() = %d, %v; want 1, nil", n, err)
	}
	if !fake.Ran("test -r /boot/vmlinuz-7.0.0-38-generic") {
		t.Error("did not test the one versioned kernel in /boot")
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "test ") && argv != "test -r /boot/vmlinuz-7.0.0-38-generic" {
			t.Errorf("tested a path find was not allowed to name: %s", argv)
		}
	}
}

func TestCheckBootKernels_ReturnsAFindFailure(t *testing.T) {
	t.Parallel()
	fake := NewFake().Respond(bootKernelFind, FakeResponse{ExitCode: 1, Stderr: "find: '/boot': Permission denied\n"})

	n, err := CheckBootKernels(context.Background(), fake)
	if n != 0 || err == nil {
		t.Fatalf("CheckBootKernels() = %d, %v; want a find failure", n, err)
	}
	var unread *UnreadableKernelError
	if errors.As(err, &unread) {
		t.Fatalf("a find failure was reported as an unreadable kernel: %v", err)
	}
}

func TestCheckBootKernels_ReturnsATestFailureThatIsNotAnExitStatus(t *testing.T) {
	t.Parallel()
	const path = "/boot/vmlinuz-7.0.0-38-generic"
	broken := errors.New("runner broken")
	fake := NewFake().
		Respond(bootKernelFind, FakeResponse{Stdout: path + "\n"}).
		Respond("test -r "+path, FakeResponse{Err: broken})

	_, err := CheckBootKernels(context.Background(), fake)
	if !errors.Is(err, broken) {
		t.Fatalf("err = %v, want the runner error", err)
	}
}
