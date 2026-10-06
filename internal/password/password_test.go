package password

import (
	"regexp"
	"testing"
	"time"
)

func TestHash(t *testing.T) {
	pwd := "MySecure@Password123"
	hash, err := Hash(pwd)
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if hash == "" {
		t.Fatalf("Hash() returned empty string")
	}
	// Hash começa com $2a$ (bcrypt prefix)
	if !regexp.MustCompile(`^\$2[aby]\$`).MatchString(hash) {
		t.Errorf("Hash() = %q, want bcrypt format", hash)
	}
}

func TestVerify(t *testing.T) {
	pwd := "MySecure@Password123"
	hash, _ := Hash(pwd)

	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{"correct", pwd, true},
		{"wrong", "WrongPassword", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verify(hash, tt.password); got != tt.want {
				t.Errorf("Verify() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGenerate(t *testing.T) {
	pwd, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(pwd) != TempLength {
		t.Errorf("Generate() length = %d, want %d", len(pwd), TempLength)
	}
	if pwd == "" {
		t.Fatalf("Generate() returned empty string")
	}
	// Deve ser diferente a cada geração
	pwd2, _ := Generate()
	if pwd == pwd2 {
		t.Errorf("Generate() produced same password twice (should be random)")
	}
}

func TestExpiresAt(t *testing.T) {
	expiry := ExpiresAt()
	now := time.Now()
	diff := expiry.Sub(now)

	// Deve ser ~72h no futuro (±10 minutos de tolerância)
	const tolerance = 10 * time.Minute
	minExpected := 72*time.Hour - tolerance
	maxExpected := 72*time.Hour + tolerance

	if diff < minExpected || diff > maxExpected {
		t.Errorf("ExpiresAt() diff = %v, want ~72h (between %v and %v)", diff, minExpected, maxExpected)
	}
}
