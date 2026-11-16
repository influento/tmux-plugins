package logind

import "testing"

func TestParseBool(t *testing.T) {
	tests := []struct {
		name, out string
		want      bool
		wantErr   bool
	}{
		{"true", "b true", true, false},
		{"false", "b false", false, false},
		{"empty", "", false, true},
		{"other type", `s "true"`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBool(tt.out)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("parseBool(%q) = %v, %v; want %v, error %v", tt.out, got, err, tt.want, tt.wantErr)
			}
		})
	}
}
