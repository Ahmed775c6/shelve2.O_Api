package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func newUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("cryptographic random source unavailable")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:])
}

func validUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

type jwtClaims struct {
	Subject    string `json:"sub"`
	Issuer     string `json:"iss"`
	Issued     int64  `json:"iat"`
	Expires    int64  `json:"exp"`
	RememberMe bool   `json:"rememberMe,omitempty"`
	Role       string `json:"role,omitempty"`
	AdminName  string `json:"adminName,omitempty"`
}

func signToken(subject string, secret []byte, now time.Time) (string, error) {
	return signTokenWithLifetime(subject, secret, now, tokenLifetime, false)
}

func signSessionToken(subject string, secret []byte, now time.Time, rememberMe bool) (string, error) {
	lifetime := tokenLifetime
	if rememberMe {
		lifetime = rememberedTokenLifetime
	}
	return signTokenWithLifetime(subject, secret, now, lifetime, rememberMe)
}

func signTokenWithLifetime(subject string, secret []byte, now time.Time, lifetime time.Duration, rememberMe bool) (string, error) {
	if !validUUID(subject) || len(secret) < 32 {
		return "", errors.New("invalid token subject or signing secret")
	}
	return signClaims(jwtClaims{
		Subject:    subject,
		Issuer:     tokenIssuer,
		Issued:     now.Unix(),
		Expires:    now.Add(lifetime).Unix(),
		RememberMe: rememberMe,
	}, secret)
}

func signAdminToken(username, role string, secret []byte, now time.Time) (string, error) {
	if (role != "admin" && role != "developer") || strings.TrimSpace(username) == "" || len(secret) < 32 {
		return "", errors.New("invalid admin token claims")
	}
	return signClaims(jwtClaims{
		Subject: newUUID(), Issuer: tokenIssuer, Issued: now.Unix(),
		Expires: now.Add(adminTokenLifetime).Unix(), Role: role, AdminName: username,
	}, secret)
}

func signClaims(tokenClaims jwtClaims, secret []byte) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(tokenClaims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyToken(token string, secret []byte, now time.Time) (string, error) {
	claims, err := verifySignedClaims(token, secret, now)
	if err != nil || claims.Role != "" || !validUUID(claims.Subject) {
		return "", errors.New("invalid or expired token")
	}
	return claims.Subject, nil
}

func verifyAdminToken(token string, secret []byte, now time.Time) (jwtClaims, error) {
	claims, err := verifySignedClaims(token, secret, now)
	if err != nil || !validUUID(claims.Subject) ||
		(claims.Role != "admin" && claims.Role != "developer") || strings.TrimSpace(claims.AdminName) == "" {
		return jwtClaims{}, errors.New("invalid or expired admin token")
	}
	return claims, nil
}

func verifySignedClaims(token string, secret []byte, now time.Time) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(secret) < 32 {
		return jwtClaims{}, errors.New("invalid token")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtClaims{}, errors.New("invalid token")
	}
	var header map[string]string
	if json.Unmarshal(headerBytes, &header) != nil || header["alg"] != "HS256" || header["typ"] != "JWT" {
		return jwtClaims{}, errors.New("invalid token")
	}
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtClaims{}, errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(providedSignature, mac.Sum(nil)) {
		return jwtClaims{}, errors.New("invalid token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, errors.New("invalid token")
	}
	var claims jwtClaims
	if json.Unmarshal(payload, &claims) != nil {
		return jwtClaims{}, errors.New("invalid or expired token")
	}
	validLifetime := false
	lifetime := claims.Expires - claims.Issued
	if claims.Role != "" {
		validLifetime = claims.Role == "admin" || claims.Role == "developer"
		validLifetime = validLifetime && lifetime == int64(adminTokenLifetime.Seconds())
	} else if claims.RememberMe {
		validLifetime = lifetime == int64(rememberedTokenLifetime.Seconds())
	} else {
		validLifetime = lifetime == int64(tokenLifetime.Seconds()) ||
			lifetime == int64(legacyTokenLifetime.Seconds())
	}
	if claims.Issuer != tokenIssuer ||
		!validUUID(claims.Subject) ||
		claims.Issued <= 0 ||
		claims.Issued > now.Add(time.Minute).Unix() ||
		claims.Expires <= now.Unix() ||
		!validLifetime {
		return jwtClaims{}, errors.New("invalid or expired token")
	}
	return claims, nil
}

func safeFilePath(root, kind, filename string) (string, error) {
	if kind != "books" && kind != "covers" && kind != "avatars" {
		return "", errors.New("unsupported file type")
	}
	extension := strings.ToLower(filepath.Ext(filename))
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	if !validUUID(base) || !validUploadType(kind, extension) || filepath.Base(filename) != filename {
		return "", errors.New("invalid file name")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(absoluteRoot, kind, filename)
	relative, err := filepath.Rel(absoluteRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("path escapes upload directory")
	}
	return target, nil
}

func ensureUploadDirectory(root, kind string) error {
	if kind != "books" && kind != "covers" && kind != "avatars" {
		return errors.New("unsupported file type")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(absoluteRoot, kind))
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("upload type directory must be a real directory")
	}
	return nil
}

func validUploadType(kind, extension string) bool {
	switch kind {
	case "books":
		return extension == ".pdf" || extension == ".epub" || extension == ".mobi" || extension == ".txt"
	case "covers", "avatars":
		return extension == ".png" || extension == ".jpg" || extension == ".jpeg" || extension == ".webp"
	default:
		return false
	}
}

func validDetectedType(extension, detected string) bool {
	switch extension {
	case ".pdf":
		return strings.HasPrefix(detected, "application/pdf")
	case ".epub":
		return strings.HasPrefix(detected, "application/zip") || strings.HasPrefix(detected, "application/epub+zip")
	case ".mobi":
		return false
	case ".txt":
		return strings.HasPrefix(detected, "text/plain")
	case ".png":
		return strings.HasPrefix(detected, "image/png")
	case ".jpg", ".jpeg":
		return strings.HasPrefix(detected, "image/jpeg")
	case ".webp":
		return strings.HasPrefix(detected, "image/webp")
	default:
		return false
	}
}

func validMobi(data []byte) bool {
	return len(data) >= 68 && string(data[60:68]) == "BOOKMOBI"
}

func ensureRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("file is not regular")
	}
	return nil
}
