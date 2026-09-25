package interactive

import "testing"

func TestDetectLowBandwidth(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"local", nil, false},
		{"ssh connection", map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 55000"}, true},
		{"ssh tty", map[string]string{"SSH_TTY": "/dev/pts/3"}, true},
		{"forced off over ssh", map[string]string{"SSH_CONNECTION": "x", "PIER_LOW_BANDWIDTH": "0"}, false},
		{"forced on locally", map[string]string{"PIER_LOW_BANDWIDTH": "true"}, true},
		{"forced on alias", map[string]string{"PIER_LOW_BANDWIDTH": "YES"}, true},
		{"forced off alias", map[string]string{"PIER_LOW_BANDWIDTH": "off"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := func(key string) string { return tc.env[key] }
			if got := detectLowBandwidth(env); got != tc.want {
				t.Fatalf("detectLowBandwidth = %v, want %v", got, tc.want)
			}
		})
	}

	if detectLowBandwidth(nil) {
		t.Fatal("nil env must default to off")
	}
}
