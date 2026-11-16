package manager

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/scrypt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func secret(path, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	b, err := os.ReadFile(path)
	if err == nil {
		value := strings.TrimSpace(string(b))
		if value == "" {
			return "", fmt.Errorf("secret file %s is empty; restore it from backup", path)
		}
		return value, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", err
	}
	value := hex.EncodeToString(raw)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return secret(path, override)
	}
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(value)
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	return value, closeErr
}
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := scrypt.Key([]byte(password), salt, 16384, 8, 1, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt$16384$8$1$%s$%s", base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(key)), nil
}
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false
	}
	n, e1 := strconv.Atoi(parts[1])
	r, e2 := strconv.Atoi(parts[2])
	p, e3 := strconv.Atoi(parts[3])
	salt, e4 := base64.StdEncoding.DecodeString(parts[4])
	expected, e5 := base64.StdEncoding.DecodeString(parts[5])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || n < 2 || n > 1<<20 || r < 1 || r > 32 || p < 1 || p > 16 || len(expected) != 32 || int64(n)*int64(r) > 1<<22 {
		return false
	}
	key, err := scrypt.Key([]byte(password), salt, n, r, p, len(expected))
	return err == nil && hmac.Equal(key, expected)
}
func mac(key, payload []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(payload)
	return h.Sum(nil)
}
func (a *App) token(id int64, username string) string {
	payload, err := json.Marshal(M{"uid": id, "sub": username, "exp": time.Now().Unix() + int64(a.cfg.SessionMinutes)*60})
	check(err)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac([]byte(a.sessionKey), payload))
}
func (a *App) claims(token string) M {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil
	}
	payload, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	if e1 != nil || e2 != nil || !hmac.Equal(mac([]byte(a.sessionKey), payload), sig) {
		return nil
	}
	var claims M
	if json.Unmarshal(payload, &claims) != nil || integer(claims["exp"]) <= time.Now().Unix() {
		return nil
	}
	return claims
}
func (a *App) encrypt(plain string) string {
	key := sha256.Sum256([]byte(a.encryptionKey))
	block, err := aes.NewCipher(key[16:])
	check(err)
	iv := make([]byte, aes.BlockSize)
	_, err = rand.Read(iv)
	check(err)
	data := []byte(plain)
	padding := aes.BlockSize - len(data)%aes.BlockSize
	for i := 0; i < padding; i++ {
		data = append(data, byte(padding))
	}
	encrypted := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, data)
	buf := make([]byte, 9)
	buf[0] = 0x80
	binary.BigEndian.PutUint64(buf[1:], uint64(time.Now().Unix()))
	buf = append(buf, iv...)
	buf = append(buf, encrypted...)
	buf = append(buf, mac(key[:16], buf)...)
	return base64.URLEncoding.EncodeToString(buf)
}
func (a *App) decrypt(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	key := sha256.Sum256([]byte(a.encryptionKey))
	buf, err := base64.URLEncoding.DecodeString(token)
	bad := errors.New("stored credential could not be decrypted: encryption.key does not match the database")
	if err != nil || len(buf) < 73 || buf[0] != 0x80 {
		return "", bad
	}
	end := len(buf) - 32
	if !hmac.Equal(mac(key[:16], buf[:end]), buf[end:]) {
		return "", bad
	}
	encrypted := buf[25:end]
	if len(encrypted) == 0 || len(encrypted)%16 != 0 {
		return "", bad
	}
	block, _ := aes.NewCipher(key[16:])
	data := make([]byte, len(encrypted))
	cipher.NewCBCDecrypter(block, buf[9:25]).CryptBlocks(data, encrypted)
	padding := int(data[len(data)-1])
	if padding < 1 || padding > 16 || padding > len(data) {
		return "", bad
	}
	for _, b := range data[len(data)-padding:] {
		if int(b) != padding {
			return "", bad
		}
	}
	return string(data[:len(data)-padding]), nil
}
func (a *App) initSecrets() error {
	var err error
	a.sessionKey, err = secret(filepath.Join(a.cfg.DataDir, "session.key"), a.cfg.SecretKey)
	if err != nil {
		return err
	}
	a.encryptionKey, err = secret(filepath.Join(a.cfg.DataDir, "encryption.key"), a.cfg.EncryptionKey)
	return err
}
