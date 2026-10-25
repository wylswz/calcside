package llm

import "testing"

func TestNormalizeModels(t *testing.T) {
	if _, err := NormalizeModels([]string{"a", "a"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	got, err := NormalizeModels([]string{" a ", "b"})
	if err != nil || len(got) != 2 || got[0] != "a" {
		t.Fatalf("got %v %v", got, err)
	}
	if got, err := NormalizeModels(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
}

func TestSecretName(t *testing.T) {
	if got := SecretName(map[string]any{"api_key": "{{ secrets.K }}"}); got != "K" {
		t.Fatalf("got %q", got)
	}
}
