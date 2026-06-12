package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/account-service/auth"
)

func newTestServer() *httptest.Server {
	c, err := auth.NewCipher(make([]byte, 32))
	if err != nil {
		panic(err)
	}
	svc := account.NewService(account.NewMemStore()).WithCipher(c)
	return httptest.NewServer(New(svc, slog.New(slog.NewTextHandler(io.Discard, nil))))
}

func post(t *testing.T, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestSignupLoginMeFlow(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, body := post(t, ts.URL+"/v1/signup", "", map[string]string{
		"email": "trader@example.com", "password": "averysecurepw",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup: %d %v", resp.StatusCode, body)
	}

	resp, body = post(t, ts.URL+"/v1/login", "", map[string]string{
		"email": "trader@example.com", "password": "averysecurepw",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %v", resp.StatusCode, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatal("no token returned")
	}

	req, _ := http.NewRequest("GET", ts.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	meResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer meResp.Body.Close()
	var me map[string]any
	json.NewDecoder(meResp.Body).Decode(&me)
	if meResp.StatusCode != http.StatusOK || me["email"] != "trader@example.com" {
		t.Fatalf("/v1/me: %d %v", meResp.StatusCode, me)
	}
}

func TestAuthRequired(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	for _, path := range []string{"/v1/2fa/setup", "/v1/apikeys"} {
		resp, _ := post(t, ts.URL+path, "", map[string]string{})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token: got %d, want 401", path, resp.StatusCode)
		}
		resp, _ = post(t, ts.URL+path, "bogus-token", map[string]string{})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with bad token: got %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestBadCredentialsAreUniform(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	post(t, ts.URL+"/v1/signup", "", map[string]string{
		"email": "a@b.com", "password": "averysecurepw",
	})

	// Wrong password and unknown email must produce identical responses.
	r1, b1 := post(t, ts.URL+"/v1/login", "", map[string]string{"email": "a@b.com", "password": "wrongpassword"})
	r2, b2 := post(t, ts.URL+"/v1/login", "", map[string]string{"email": "ghost@b.com", "password": "averysecurepw"})
	if r1.StatusCode != http.StatusUnauthorized || r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401/401, got %d/%d", r1.StatusCode, r2.StatusCode)
	}
	if b1["error"] != b2["error"] {
		t.Fatalf("error bodies differ: %v vs %v", b1, b2)
	}
}

func TestAPIKeyCreation(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	post(t, ts.URL+"/v1/signup", "", map[string]string{"email": "a@b.com", "password": "averysecurepw"})
	_, body := post(t, ts.URL+"/v1/login", "", map[string]string{"email": "a@b.com", "password": "averysecurepw"})
	token := body["token"].(string)

	resp, key := post(t, ts.URL+"/v1/apikeys", token, map[string]string{"label": "bot"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("apikeys: %d %v", resp.StatusCode, key)
	}
	if key["key_id"] == "" || key["secret"] == "" {
		t.Fatalf("missing key material: %v", key)
	}
}
