package social

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
)

// GoogleJWKSURL adalah tempat Google menerbitkan kunci publiknya.
const GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// googleIssuers adalah dua bentuk yang sah-sama dari penerbit yang sama.
// Google memakai keduanya, dan menerima hanya satu akan menolak setengah
// token yang sah.
var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

// providerGoogle adalah satu-satunya penyedia yang dilayani verifier ini.
const providerGoogle = "google"

var (
	// ErrUnsupportedProvider ditolak sebelum jaringan disentuh.
	ErrUnsupportedProvider = errors.New("unsupported social provider")

	// ErrInvalidIDToken menutupi setiap alasan sebuah ID token ditolak.
	// Pemanggil DILARANG membedakannya: "tanda tangannya benar tetapi
	// audiencenya salah" memberi tahu penyerang mana bagian yang sudah tepat.
	ErrInvalidIDToken = errors.New("invalid id token")
)

// GoogleVerifier memeriksa ID token OIDC terbitan Google.
//
// Yang diperiksa ada empat, dan keempatnya wajib:
//
//   - tanda tangan, terhadap kunci publik Google yang diambil dari JWKS-nya;
//   - iss, supaya token dari penerbit lain tidak diterima;
//   - aud, supaya token yang diterbitkan untuk aplikasi LAIN tidak bisa
//     dipakai di sini - ini yang paling sering terlewat, dan tanpanya siapa
//     pun yang punya aplikasi Google bisa menukar token penggunanya menjadi
//     sesi di sistem ini;
//   - exp, lewat parser.
type GoogleVerifier struct {
	clientID string
	jwksURL  string
	client   *http.Client
	keys     *jwksCache
}

// NewGoogleVerifier menyusun verifier untuk satu client id.
//
// jwksURL bisa dikosongkan untuk memakai milik Google; ia bisa diisi supaya
// test bisa menyajikan JWKS-nya sendiri tanpa menyentuh jaringan.
func NewGoogleVerifier(clientID, jwksURL string, client *http.Client, cacheFor time.Duration) (*GoogleVerifier, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("empty google client id")
	}
	if jwksURL == "" {
		jwksURL = GoogleJWKSURL
	}
	if client == nil {
		// Batas waktu ditetapkan, bukan diwarisi dari http.DefaultClient yang
		// tidak punya satu pun. Pengambilan kunci duduk di jalur masuk
		// pengguna, dan penyedia yang menggantung tidak boleh menahannya.
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if cacheFor <= 0 {
		cacheFor = time.Hour
	}
	return &GoogleVerifier{
		clientID: clientID,
		jwksURL:  jwksURL,
		client:   client,
		keys:     &jwksCache{ttl: cacheFor},
	}, nil
}

// googleClaims adalah bagian ID token yang dipakai.
type googleClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`

	// EmailVerified datang sebagai boolean pada ID token, tetapi Google
	// pernah mengirimnya sebagai string pada endpoint lain. Ia diurai lewat
	// tipe sendiri supaya bentuk yang tidak terduga menjadi penolakan, bukan
	// nilai false yang diam-diam melewati pengerasan di F1-11.
	EmailVerified flexibleBool `json:"email_verified"`
}

// flexibleBool menerima true, false, "true", dan "false".
type flexibleBool bool

func (b *flexibleBool) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		*b = flexibleBool(asBool)
		return nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err != nil {
		return fmt.Errorf("email_verified is neither a boolean nor a string: %w", err)
	}
	switch asString {
	case "true":
		*b = true
	case "false":
		*b = false
	default:
		return fmt.Errorf("email_verified is %q, which is neither true nor false", asString)
	}
	return nil
}

func (v *GoogleVerifier) Verify(ctx context.Context, provider, idToken string) (app.SocialIdentity, error) {
	if provider != providerGoogle {
		return app.SocialIdentity{}, fmt.Errorf("%w: %q", ErrUnsupportedProvider, provider)
	}

	var claims googleClaims

	// Penerbit TIDAK diserahkan ke parser, dan itu disengaja.
	//
	// Google memakai dua bentuk penerbit yang sama-sama sah, dan
	// WithIssuer hanya menerima satu. Mencoba dua parser berurutan akan
	// menggandakan pekerjaannya - termasuk pengambilan JWKS - sehingga
	// token dengan kid acak bisa dipakai memaksa dua permintaan ke
	// penyedia per request. Sekali parse, lalu penerbitnya diperiksa
	// sendiri terhadap daftar.
	parser := jwt.NewParser(
		// Pertukaran algoritma hari ini sudah tertutup oleh tipe kunci:
		// keyfunc mengembalikan *rsa.PublicKey, dan verifikasi HMAC menuntut
		// []byte sementara alg=none menuntut sentinel tersendiri. Daftar ini
		// lapis kedua - ia tetap menolak bila kelak keyfunc diubah.
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithAudience(v.clientID),
		jwt.WithExpirationRequired(),
	)

	if _, err := parser.ParseWithClaims(idToken, &claims, v.keyFor(ctx)); err != nil {
		return app.SocialIdentity{}, fmt.Errorf("%w: %w", ErrInvalidIDToken, err)
	}

	if !slices.Contains(googleIssuers, claims.Issuer) {
		return app.SocialIdentity{}, fmt.Errorf("%w: issuer %q", ErrInvalidIDToken, claims.Issuer)
	}

	if claims.Subject == "" {
		return app.SocialIdentity{}, fmt.Errorf("%w: no subject", ErrInvalidIDToken)
	}

	return app.SocialIdentity{
		Provider:      providerGoogle,
		ProviderID:    claims.Subject,
		Email:         claims.Email,
		EmailVerified: bool(claims.EmailVerified),
	}, nil
}

// keyFor mencari kunci publik yang cocok dengan kid di header token.
func (v *GoogleVerifier) keyFor(ctx context.Context) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			// Header tanpa kid, atau kid yang bukan string, sama-sama berarti
			// tokennya tidak menunjuk kunci mana pun. Membuang hasil type
			// assertion akan menyamakan "kid: 123" dengan "tidak ada kid",
			// dan keduanya memang ditolak - tetapi diam-diam.
			return nil, errors.New("the token names no key")
		}

		key, err := v.keys.lookup(ctx, v.client, v.jwksURL, kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	}
}

// jwksCache menyimpan kunci publik penyedia untuk sementara.
//
// Tanpa cache, setiap masuk lewat Google berarti satu permintaan HTTP ke
// penyedia sebelum apa pun bisa diverifikasi - dan penyedianya menjadi
// dependensi di jalur terpanas alur masuk.
type jwksCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func (c *jwksCache) lookup(ctx context.Context, client *http.Client, url, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if key, ok := c.keys[kid]; ok && time.Since(c.fetchedAt) < c.ttl {
		return key, nil
	}

	// kid yang tidak dikenal juga memicu pengambilan ulang, bukan hanya
	// cache yang kedaluwarsa: penyedia merotasi kuncinya, dan kunci baru
	// muncul sebelum salinan lama kedaluwarsa.
	keys, err := fetchJWKS(ctx, client, url)
	if err != nil {
		return nil, err
	}
	c.keys, c.fetchedAt = keys, time.Now()

	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("the provider published no key named %q", kid)
	}
	return key, nil
}

// jwk adalah satu kunci di dalam JWKS, dalam bentuk RSA.
type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func fetchJWKS(ctx context.Context, client *http.Client, url string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building the jwks request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching jwks: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("closing the jwks response", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching jwks: the provider answered %s", resp.Status)
	}

	// Badan dibatasi. Sebuah endpoint yang mengirim gigabita - karena rusak,
	// atau karena bukan endpoint yang kita kira - tidak boleh menghabiskan
	// memori service ini.
	const maxJWKSBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes))
	if err != nil {
		return nil, fmt.Errorf("reading jwks: %w", err)
	}

	var document struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("parsing jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, k := range document.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		key, err := k.publicKey()
		if err != nil {
			// Satu kunci yang rusak tidak membatalkan sisanya: penyedia bisa
			// menerbitkan jenis kunci yang belum kita dukung, dan menolak
			// seluruh dokumen karenanya akan mematikan alur masuk.
			continue
		}
		keys[k.Kid] = key
	}

	if len(keys) == 0 {
		return nil, errors.New("the jwks document contains no usable rsa key")
	}
	return keys, nil
}

func (k jwk) publicKey() (*rsa.PublicKey, error) {
	// base64url tanpa padding, seperti yang ditetapkan RFC 7517.
	modulus, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decoding modulus: %w", err)
	}
	exponent, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decoding exponent: %w", err)
	}
	if len(exponent) == 0 || len(exponent) > 8 {
		return nil, fmt.Errorf("exponent is %d bytes; refusing it", len(exponent))
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulus),
		E: int(new(big.Int).SetBytes(exponent).Int64()),
	}, nil
}
