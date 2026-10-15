package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// KeycloakLogins creates the sign-in account of an invited e-mail in the Keycloak realm (ADR-0046), through the admin REST API with a client
// whose service account holds only realm-management view-users and manage-users. Accounts it creates carry omnira_provisioned=true; it only
// resets or disables such an account while it still waits for its first password (UPDATE_PASSWORD), so an account in use is never touched.
// Nothing secret (client secret, token, password) ever reaches an error or a log line.
type KeycloakLogins struct {
	base     string
	realm    string
	clientID string
	secret   string
	client   *http.Client

	mu         sync.Mutex
	token      string
	tokenUntil time.Time
}

const provisionedAttribute = "omnira_provisioned"

func NewKeycloakLogins(baseURL, realm, clientID, secret string) *KeycloakLogins {
	return &KeycloakLogins{
		base: strings.TrimRight(baseURL, "/"), realm: realm, clientID: clientID, secret: secret,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// RealmFromIssuer reads the realm out of an OIDC issuer such as https://auth.example.com/realms/omnira.
func RealmFromIssuer(issuer string) string {
	i := strings.LastIndex(issuer, "/realms/")
	if i < 0 {
		return ""
	}
	return strings.Trim(issuer[i+len("/realms/"):], "/")
}

type kcUser struct {
	ID              string              `json:"id,omitempty"`
	Username        string              `json:"username,omitempty"`
	Email           string              `json:"email,omitempty"`
	EmailVerified   bool                `json:"emailVerified"`
	Enabled         bool                `json:"enabled"`
	RequiredActions []string            `json:"requiredActions"`
	Attributes      map[string][]string `json:"attributes,omitempty"`
	Credentials     []kcCredential      `json:"credentials,omitempty"`
}

type kcCredential struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Temporary bool   `json:"temporary"`
}

func (u kcUser) createdByOmnira() bool {
	v := u.Attributes[provisionedAttribute]
	return len(v) > 0 && v[0] == "true"
}

func (u kcUser) waitsForFirstPassword() bool {
	for _, a := range u.RequiredActions {
		if a == "UPDATE_PASSWORD" {
			return true
		}
	}
	return false
}

// Prepare makes tempPassword the sign-in password of email: it creates the account when there is none and resets it on an account this
// service created that was never used. passwordSet=false means the person already has an account of their own, left untouched.
func (k *KeycloakLogins) Prepare(ctx context.Context, email, tempPassword string) (bool, error) {
	u, err := k.find(ctx, email)
	if err != nil {
		return false, err
	}
	if u == nil {
		created, err := k.create(ctx, email, tempPassword)
		if err != nil || created {
			return created, err
		}
		// Another request created it in between: decide on what is there now.
		if u, err = k.find(ctx, email); err != nil {
			return false, err
		}
		if u == nil {
			return false, errors.New("keycloak: account vanished after a conflicting creation")
		}
	}
	if !u.createdByOmnira() || !u.waitsForFirstPassword() {
		return false, nil
	}
	if err := k.call(ctx, http.MethodPut, "/users/"+url.PathEscape(u.ID)+"/reset-password",
		kcCredential{Type: "password", Value: tempPassword, Temporary: true}, nil); err != nil {
		return false, err
	}
	if !u.Enabled {
		if err := k.setEnabled(ctx, u.ID, true); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Disable turns off the account of email when this service created it and it was never used (the invitation was cancelled).
func (k *KeycloakLogins) Disable(ctx context.Context, email string) error {
	u, err := k.find(ctx, email)
	if err != nil || u == nil {
		return err
	}
	if !u.createdByOmnira() || !u.waitsForFirstPassword() || !u.Enabled {
		return nil
	}
	return k.setEnabled(ctx, u.ID, false)
}

func (k *KeycloakLogins) find(ctx context.Context, email string) (*kcUser, error) {
	var users []kcUser
	q := "/users?exact=true&briefRepresentation=false&email=" + url.QueryEscape(email)
	if err := k.call(ctx, http.MethodGet, q, nil, &users); err != nil {
		return nil, err
	}
	for i := range users {
		if strings.EqualFold(users[i].Email, email) {
			return &users[i], nil
		}
	}
	return nil, nil
}

func (k *KeycloakLogins) create(ctx context.Context, email, tempPassword string) (bool, error) {
	u := kcUser{
		Username: email, Email: email, EmailVerified: true, Enabled: true,
		RequiredActions: []string{"UPDATE_PASSWORD"},
		Attributes:      map[string][]string{provisionedAttribute: {"true"}},
		Credentials:     []kcCredential{{Type: "password", Value: tempPassword, Temporary: true}},
	}
	err := k.call(ctx, http.MethodPost, "/users", u, nil)
	var conflict *kcStatusError
	if errors.As(err, &conflict) && conflict.status == http.StatusConflict {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Keycloak 24+ drops attributes its user profile does not declare unless the realm lets admins edit unmanaged ones. Without the
	// marker the account still works, but a resend or a cancellation would leave it alone: say so where operators look.
	if created, err := k.find(ctx, email); err == nil && created != nil && !created.createdByOmnira() {
		log.Printf("keycloak: account created without the %s attribute; set the realm's unmanaged attribute policy to ADMIN_EDIT", provisionedAttribute)
	}
	return true, nil
}

// setEnabled writes back the whole representation read from Keycloak with only `enabled` changed, so names and attributes are kept.
func (k *KeycloakLogins) setEnabled(ctx context.Context, id string, enabled bool) error {
	var rep map[string]any
	path := "/users/" + url.PathEscape(id)
	if err := k.call(ctx, http.MethodGet, path, nil, &rep); err != nil {
		return err
	}
	rep["enabled"] = enabled
	return k.call(ctx, http.MethodPut, path, rep, nil)
}

type kcStatusError struct {
	method, path string
	status       int
}

func (e *kcStatusError) Error() string {
	return fmt.Sprintf("keycloak: %s %s answered %d", e.method, e.path, e.status)
}

func (k *KeycloakLogins) call(ctx context.Context, method, path string, body, out any) error {
	for attempt := 0; ; attempt++ {
		token, err := k.accessToken(ctx)
		if err != nil {
			return err
		}
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, k.base+"/admin/realms/"+url.PathEscape(k.realm)+path, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := k.client.Do(req)
		if err != nil {
			return fmt.Errorf("keycloak: %s unreachable", method)
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 {
			k.forgetToken()
			continue
		}
		if res.StatusCode >= 300 {
			return &kcStatusError{method: method, path: strings.SplitN(path, "?", 2)[0], status: res.StatusCode}
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

func (k *KeycloakLogins) accessToken(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.token != "" && time.Now().Before(k.tokenUntil) {
		return k.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {k.clientID}, "client_secret": {k.secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.base+"/realms/"+url.PathEscape(k.realm)+"/protocol/openid-connect/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := k.client.Do(req)
	if err != nil {
		return "", errors.New("keycloak: token endpoint unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak: token endpoint answered %d", res.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("keycloak: token endpoint gave no token")
	}
	k.token = tok.AccessToken
	k.tokenUntil = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 30*time.Second)
	return k.token, nil
}

func (k *KeycloakLogins) forgetToken() {
	k.mu.Lock()
	k.token = ""
	k.mu.Unlock()
}
