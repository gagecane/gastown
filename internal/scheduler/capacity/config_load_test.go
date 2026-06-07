package capacity

import "testing"

func TestGetMaxLoadPerCore(t *testing.T) {
	pos := 2.5
	zero := 0.0
	neg := -1.0
	cases := []struct {
		name string
		cfg  *SchedulerConfig
		want float64
	}{
		{"nil config", nil, 0},
		{"nil field", &SchedulerConfig{}, 0},
		{"zero disables", &SchedulerConfig{MaxLoadPerCore: &zero}, 0},
		{"negative disables", &SchedulerConfig{MaxLoadPerCore: &neg}, 0},
		{"positive", &SchedulerConfig{MaxLoadPerCore: &pos}, 2.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.GetMaxLoadPerCore(); got != tc.want {
				t.Fatalf("GetMaxLoadPerCore() = %v, want %v", got, tc.want)
			}
		})
	}
}
