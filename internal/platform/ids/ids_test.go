package ids

import "testing"

func TestNewIsValidAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := New()
		if !Valid(id) {
			t.Fatalf("identificador no válido: %q", id)
		}
		if id[14] != '4' {
			t.Fatalf("se esperaba versión 4 en %q", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("identificador duplicado: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"canónico", "f81d4fae-7dec-11d0-a765-00a0c91e6bf6", true},
		{"mayúsculas", "F81D4FAE-7DEC-11D0-A765-00A0C91E6BF6", true},
		{"vacío", "", false},
		{"longitud incorrecta", "f81d4fae-7dec-11d0-a765-00a0c91e6bf", false},
		{"guion desplazado", "f81d4fae7-dec-11d0-a765-00a0c91e6bf6", false},
		{"carácter no hexadecimal", "z81d4fae-7dec-11d0-a765-00a0c91e6bf6", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Valid(tc.in); got != tc.want {
				t.Fatalf("Valid(%q) = %v, se esperaba %v", tc.in, got, tc.want)
			}
		})
	}
}
