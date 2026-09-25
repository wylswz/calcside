package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mux(t *testing.T) *http.ServeMux {
	t.Helper()
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cs_test" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "unauthorized", "message": "no credentials"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{"id": "usr_1", "email": "a@b.c", "name": "A"}})
	})
	m.HandleFunc("GET /api/v1/instances", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"instances": []any{map[string]any{"id": "ins_1", "status": "running"}}})
	})
	m.HandleFunc("POST /api/v1/instances/ins_1/exec", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"exec_id": "exe_1", "output": "hi\n", "error": nil, "duration_ms": 5, "steps": 10})
	})
	return m
}

func TestMeAndAuth(t *testing.T) {
	srv := httptest.NewServer(mux(t))
	defer srv.Close()
	c := New(srv.URL, "cs_test")
	u, err := c.Me(context.Background())
	if err != nil || u.Email != "a@b.c" {
		t.Fatalf("Me: %v %+v", err, u)
	}
	c2 := New(srv.URL, "")
	if _, err := c2.Me(context.Background()); err == nil {
		t.Fatal("expected 401 error")
	}
}

func TestListAndExec(t *testing.T) {
	srv := httptest.NewServer(mux(t))
	defer srv.Close()
	c := New(srv.URL, "cs_test")
	ins, err := c.ListInstances(context.Background(), "")
	if err != nil || len(ins) != 1 {
		t.Fatalf("ListInstances: %v %v", err, ins)
	}
	r, err := c.Exec(context.Background(), "ins_1", "print(1)", 0)
	if err != nil || r.Output != "hi\n" {
		t.Fatalf("Exec: %v %+v", err, r)
	}
}
