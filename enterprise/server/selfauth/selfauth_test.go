package selfauth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func TestJWKSMatchesOldOutput(t *testing.T) {
	o, err := NewSelfAuth()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	o.Jwks(w, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	want, err := os.ReadFile("testdata/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json;charset=UTF-8" {
		t.Fatalf("unexpected response: %v", w.Result())
	}
	if w.Body.String() != string(want) {
		t.Fatalf("JWKS changed:\ngot %s\nwant %s", w.Body.String(), want)
	}
}

func TestTokenVerifiesWithPublishedJWKS(t *testing.T) {
	o, err := NewSelfAuth()
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewRecorder()
	o.Jwks(keys, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	// Decode as a client would, without using the server's key or JWK type.
	var set struct{ Keys []struct{ E, Kty, N string } }
	if err := json.Unmarshal(keys.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Keys) != 1 || set.Keys[0].Kty != "RSA" {
		t.Fatalf("unexpected keys: %+v", set)
	}
	n, err := base64.RawURLEncoding.DecodeString(set.Keys[0].N)
	if err != nil {
		t.Fatal(err)
	}
	e, err := base64.RawURLEncoding.DecodeString(set.Keys[0].E)
	if err != nil {
		t.Fatal(err)
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	before := time.Now().Unix()
	w := httptest.NewRecorder()
	o.AccessToken(w, httptest.NewRequest(http.MethodPost, "/oauth/access_token", nil))
	after := time.Now().Unix()
	if w.Code != http.StatusOK {
		t.Fatalf("token endpoint returned %d: %s", w.Code, w.Body.String())
	}
	var response tokenJSON
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.AccessToken != "AccessToken" || response.RefreshToken != "RefreshToken" || response.ExpiresIn != 3600 || response.TokenType != "" {
		t.Fatalf("unexpected response: %+v", response)
	}
	token, err := jwt.Parse(response.IdToken, func(token *jwt.Token) (any, error) { return key, nil }, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		t.Fatal(err)
	}
	if !token.Valid {
		t.Fatal("invalid token")
	}
	header, _ := json.Marshal(token.Header)
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		t.Fatalf("header changed: %s", header)
	}
	claims := token.Claims.(jwt.MapClaims)
	if len(claims) != 10 {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	for k, want := range map[string]string{"iss": o.IssuerURL().String(), "sub": "", "email": "buildbuddy@example.com", "name": "buildbuddy", "given_name": "Default", "at_hash": "LkjhI6Ijpj638f0mirBH2g"} {
		if claims[k] != want {
			t.Errorf("%s = %v, want %q", k, claims[k], want)
		}
	}
	aud, ok := claims["aud"].([]any)
	if !ok || len(aud) != 1 || aud[0] != ClientID {
		t.Fatalf("audience changed: %v", claims["aud"])
	}
	for k, offset := range map[string]int64{"iat": 0, "exp": 3600} {
		value, ok := claims[k].(float64)
		if !ok || int64(value) < before+offset || int64(value) > after+offset {
			t.Errorf("unexpected %s: %v", k, claims[k])
		}
	}
	id, ok := claims["jti"].(string)
	if !ok {
		t.Fatal("missing jti")
	}
	decodedID, err := base64.StdEncoding.DecodeString(id)
	if err != nil || len(decodedID) != 32 {
		t.Fatalf("invalid jti: %q", id)
	}
	// Also verify independently with crypto/rsa, avoiding a same-library round trip.
	parts := strings.Split(response.IdToken, ".")
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatal(err)
	}
}
