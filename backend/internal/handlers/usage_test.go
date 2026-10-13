package handlers

import (
	"slices"
	"testing"
)

func TestUsageFlags(t *testing.T) {
	cases := []struct {
		name string
		u    usageUser
		cpu  float64 // cpu core-seconds over 7d
		want []string
	}{
		{"normal", usageUser{ActiveMin7d: 120, KernelMin7d: 150, CPUCoreHours7d: 1}, 3600, []string{}},
		{"heavy cpu", usageUser{ActiveMin7d: 600, KernelMin7d: 700, CPUCoreHours7d: 12}, 12 * 3600, []string{"heavy_cpu"}},
		{"mining-like average", usageUser{ActiveMin7d: 60, KernelMin7d: 60, CPUCoreHours7d: 1.9}, 1.9 * 3600, []string{"high_avg_cpu"}},
		{"idle kernel", usageUser{ActiveMin7d: 30, KernelMin7d: 300, CPUCoreHours7d: 0.2}, 720, []string{"idle_kernel"}},
	}
	for _, c := range cases {
		if got := usageFlags(&c.u, c.cpu); !slices.Equal(got, c.want) {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
