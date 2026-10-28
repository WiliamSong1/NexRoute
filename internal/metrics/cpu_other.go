//go:build !linux && !darwin

package metrics

import "io"

func writeCPU(w io.Writer) {}
