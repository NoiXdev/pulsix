// Package uidboundary holds the in-image half of the playbook uid
// separation's boundary test (backlog 2.42). It carries no code of its
// own: boundary_test.go drives the real worker — worker.Loop with the real
// runner.Runner executor, against a fake manager — INSIDE the built worker
// image, where uid 65533, the shared group and the setuid helper exist.
//
// It is compiled and run by internal/e2e's TestPlaybookUIDBoundary, which
// builds this package's test binary for the image's platform and mounts it
// into a container. Run directly on a developer's machine every test here
// skips: PULSIX_UID_BOUNDARY_IN_IMAGE is what the driver sets.
package uidboundary
