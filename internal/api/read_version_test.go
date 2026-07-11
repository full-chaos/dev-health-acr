package api

import "testing"

func TestClientVersionCompatibilityUsesSemVerPrecedence(t *testing.T) {
	tests := []struct {
		client, minimum string
		want            bool
	}{
		{client: "1.0.0", minimum: "1.0.0", want: true},
		{client: "1.0.0+build.7", minimum: "1.0.0+build.1", want: true},
		{client: "1.0.0-alpha.2", minimum: "1.0.0-alpha.1", want: true},
		{client: "1.0.0-alpha", minimum: "1.0.0", want: false},
		{client: "1.0.0", minimum: "1.0.0-rc.1", want: true},
		{client: "1.0.0-alpha.1", minimum: "1.0.0-alpha.beta", want: false},
		{client: "v2.0.0", minimum: "1.9.9", want: true},
		{client: "01.0.0", minimum: "1.0.0", want: false},
		{client: "1.0.0-01", minimum: "1.0.0", want: false},
		{client: "1.0.0+", minimum: "1.0.0", want: false},
		{client: "1.0.0+bad!", minimum: "1.0.0", want: false},
	}
	for _, test := range tests {
		if got := clientVersionCompatible(test.client, test.minimum); got != test.want {
			t.Fatalf("compatible(%q, %q) = %t, want %t", test.client, test.minimum, got, test.want)
		}
	}
}
