// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"errors"
	"strings"
	"testing"
)

// dockerGate is the harness's contract for what to do when testcontainers
// can't reach Docker. The default is to fail loudly; CI never sets the
// escape hatch. Local devs running `make e2e` without Docker can opt
// into soft-skip via E2E_ALLOW_NO_DOCKER=1.
//
// These tests pin the contract by driving dockerGate against a fake
// errSink (a `*testing.T` lookalike that records Skip/Fatal calls)
// rather than against the real harness.

type fakeT struct {
	skips  []string
	fatals []string
}

func (f *fakeT) Skipf(format string, args ...any) {
	f.skips = append(f.skips, fmtSprintf(format, args...))
}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.fatals = append(f.fatals, fmtSprintf(format, args...))
}
func (f *fakeT) Helper() {}

// fmtSprintf is a tiny indirection so we don't add an `fmt` import
// solely for this test file. The implementation here is intentionally
// trivial; it does not pretend to be Sprintf-equivalent.
func fmtSprintf(format string, args ...any) string {
	// Just append all args separated by spaces — sufficient for the
	// "did the right path fire" assertions below.
	out := format
	for _, a := range args {
		out = out + " " + toString(a)
	}
	return out
}

func toString(v any) string {
	switch x := v.(type) {
	case error:
		return x.Error()
	case string:
		return x
	default:
		return ""
	}
}

func TestDockerGate_DefaultFailsLoud(t *testing.T) {
	t.Parallel()
	ft := &fakeT{}
	err := errors.New("docker unreachable: no socket")
	dockerGate(ft, err, false /* allowNoDocker */)

	if len(ft.skips) != 0 {
		t.Errorf("expected no skips, got %v", ft.skips)
	}
	if len(ft.fatals) != 1 {
		t.Fatalf("expected one Fatalf, got %d", len(ft.fatals))
	}
	if !strings.Contains(ft.fatals[0], "docker unreachable") {
		t.Errorf("Fatalf should include the underlying error; got %q", ft.fatals[0])
	}
	if !strings.Contains(ft.fatals[0], "E2E_ALLOW_NO_DOCKER") {
		t.Errorf("Fatalf should mention the escape hatch; got %q", ft.fatals[0])
	}
}

func TestDockerGate_AllowNoDockerSkips(t *testing.T) {
	t.Parallel()
	ft := &fakeT{}
	err := errors.New("docker unreachable: no socket")
	dockerGate(ft, err, true /* allowNoDocker */)

	if len(ft.fatals) != 0 {
		t.Errorf("expected no Fatalfs when allowNoDocker=true, got %v", ft.fatals)
	}
	if len(ft.skips) != 1 {
		t.Fatalf("expected one Skipf, got %d", len(ft.skips))
	}
	if !strings.Contains(ft.skips[0], "docker unreachable") {
		t.Errorf("Skipf should surface the underlying error; got %q", ft.skips[0])
	}
}

func TestDockerGate_NilErrIsNoop(t *testing.T) {
	t.Parallel()
	ft := &fakeT{}
	dockerGate(ft, nil, false)
	if len(ft.fatals) != 0 || len(ft.skips) != 0 {
		t.Errorf("nil err must be no-op; skips=%v fatals=%v", ft.skips, ft.fatals)
	}
}
