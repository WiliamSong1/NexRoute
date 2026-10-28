//go:build linux || darwin

package metrics

import (
	"fmt"
	"io"
	"syscall"
)

func writeCPU(w io.Writer) {
	var r syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &r) == nil {
		sec := float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
		fmt.Fprintf(w, "process_cpu_seconds_total %g\n", sec)
	}
}
