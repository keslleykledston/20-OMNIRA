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
// whose service account holds only realm-management view-users and manage-users. An account it creates is bound to the invitation that
// created it (omnira_invitation + its expiry). It only refreshes, re-enables or disables such an account while it still waits for its first
// password (UPDATE_PASSWORD), and only for that invitation, the invitations it replaced, or once that invitation is over (expired, or
// cancelled: OMNIRA turned the account off). So an account in use is never touched, and an invitation of another instance cannot take over,
// reset or turn off the account a live invitation is waiting on. Nothing secret (client secret, token, password) reaches an error or a log.
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

const (
	provisionedAttribute = "omnira_provisioned"
	invitationAttribute  = "omnira_invitation"
	expiresAttribute     = "omnira_invitation_expires"
	disabledAttribute    = "omnira_disabled"
)

// LoginInvitation is what the sign-in account is prepared for: the e-mail, the temporary password the e-mail carries, and the invitation.
type LoginInvitation struct {
	Email     string
	Password  string
	ID        string
	ExpiresAt time.Time
	// Replaces lists the invitations of the same instance this one has just replaced (a new invitation to the same e-mail).
	Replaces []string
}

var (
	errLoginMarkerDropped = errors.New("keycloak: the realm dropped the omnira attributes; set the user profile's unmanaged attribute policy to ADMIN_EDIT")
	errLoginDisabledByAdm = errors.New("keycloak: the account was disabled by an administrator; it is not turned back on by an invitation")
	errLoginChanged       = errors.New("keycloak: the account changed while it was being prepared; try again")
)

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

func (u kcUser) attr(name string) string {
	if v := u.Attributes[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (u kcUser) createdByOmnira() bool { return u.attr(provisionedAttribute) == "true" }

func (u kcUser) disabledByOmnira() bool { return u.attr(disabledAttribute) == "true" }

func (u kcUser) waitsForFirstPassword() bool {
	for _, a := range u.RequiredActions {
		if a == "UPDATE_PASSWORD" {
			return true
		}
	}
	return false
}

// mayTakeOver: the account waits for the invitation inv, for one inv replaced, or for an invitation that is over (cancelled: OMNIRA
// turned it off; or past its expiry). Without a complete binding (invitation and a readable expiry) nobody may take it: fail closed.
func (u kcUser) mayTakeOver(inv LoginInvitation, now time.Time) bool {
	bound := u.attr(invitationAttribute)
	until, err := time.Parse(time.RFC3339, u.attr(expiresAttribute))
	if bound == "" || err != nil {
		return false
	}
	if bound == inv.ID {
		return true
	}
	for _, id := range inv.Replaces {
		if bound == id {
			return true
		}
	}
	return u.disabledByOmnira() || !now.Before(until)
}

// boundTo: a complete binding to exactly this invitation (what create and the binding write leave behind).
func (u kcUser) boundTo(inv LoginInvitation) bool {
	return u.createdByOmnira() && u.attr(invitationAttribute) == inv.ID &&
		u.attr(expiresAttribute) == inv.ExpiresAt.UTC().Format(time.RFC3339)
}

// mayPrepare: an unused account OMNIRA created, on (or turned off by OMNIRA), that inv may take over.
func (u kcUser) mayPrepare(inv LoginInvitation, now time.Time) bool {
	return u.createdByOmnira() && u.waitsForFirstPassword() && (u.Enabled || u.disabledByOmnira()) && u.mayTakeOver(inv, now)
}

// mayDisable: an unused account OMNIRA created for exactly this invitation, still on.
func (u kcUser) mayDisable(invitationID string) bool {
	return u.createdByOmnira() && u.waitsForFirstPassword() && u.Enabled && u.attr(invitationAttribute) == invitationID
}

func binding(inv LoginInvitation) map[string]string {
	return map[string]string{
		provisionedAttribute: "true",
		invitationAttribute:  inv.ID,
		expiresAttribute:     inv.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// Prepare makes inv.Password the sign-in password of inv.Email: it creates the account when there is none, and refreshes an account this
// service created, never used, that may be taken over (see mayTakeOver). passwordSet=false means the account is left as it is: the person's
// own, already in use, or waiting on another live invitation (whose e-mail carries the password).
func (k *KeycloakLogins) Prepare(ctx context.Context, inv LoginInvitation) (bool, error) {
	u, err := k.find(ctx, inv.Email)
	if err != nil {
		return false, err
	}
	if u == nil {
		created, err := k.create(ctx, inv)
		if err != nil || created {
			return created, err
		}
		// Another request created it in between: decide on what is there now.
		if u, err = k.find(ctx, inv.Email); err != nil {
			return false, err
		}
		if u == nil {
			return false, errors.New("keycloak: account vanished after a conflicting creation")
		}
	}
	if !u.createdByOmnira() || !u.waitsForFirstPassword() {
		return false, nil
	}
	if !u.Enabled && !u.disabledByOmnira() {
		return false, errLoginDisabledByAdm
	}
	if !u.mayTakeOver(inv, time.Now()) {
		return false, nil
	}
	// Bind first, then the password: a failure in between leaves the account bound to this invitation, so "Reenviar" takes it again.
	// The rule is checked again on the representation read right before the write (another actor may have changed it meanwhile).
	if err := k.updateUser(ctx, u.ID, func(fresh kcUser) bool { return fresh.mayPrepare(inv, time.Now()) }, func(rep map[string]any) {
		rep["enabled"] = true
		setAttributes(rep, binding(inv), disabledAttribute)
	}); err != nil {
		return false, err
	}
	// The password last, after reading the account once more: still unused, on and bound to this invitation (the person may have finished
	// a first sign-in in between; then their own password stays).
	var fresh kcUser
	if err := k.call(ctx, http.MethodGet, "/users/"+url.PathEscape(u.ID), nil, &fresh); err != nil {
		return false, err
	}
	if !fresh.boundTo(inv) || !fresh.waitsForFirstPassword() || !fresh.Enabled {
		return false, errLoginChanged
	}
	if err := k.call(ctx, http.MethodPut, "/users/"+url.PathEscape(u.ID)+"/reset-password",
		kcCredential{Type: "password", Value: inv.Password, Temporary: true}, nil); err != nil {
		return false, err
	}
	return true, nil
}

// Disable turns off the account of email when this service created it for invitationID and it was never used (the invitation was
// cancelled). disabled=false: nothing to turn off (another invitation's account, the person's own, already in use or already off).
func (k *KeycloakLogins) Disable(ctx context.Context, email, invitationID string) (bool, error) {
	u, err := k.find(ctx, email)
	if err != nil || u == nil {
		return false, err
	}
	if !u.mayDisable(invitationID) {
		return false, nil
	}
	if err := k.updateUser(ctx, u.ID, func(fresh kcUser) bool { return fresh.mayDisable(invitationID) }, func(rep map[string]any) {
		rep["enabled"] = false
		setAttributes(rep, map[string]string{disabledAttribute: "true"})
	}); err != nil {
		return false, err
	}
	return true, nil
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

func (k *KeycloakLogins) create(ctx context.Context, inv LoginInvitation) (bool, error) {
	attrs := map[string][]string{}
	for name, v := range binding(inv) {
		attrs[name] = []string{v}
	}
	u := kcUser{
		Username: inv.Email, Email: inv.Email, EmailVerified: true, Enabled: true,
		RequiredActions: []string{"UPDATE_PASSWORD"},
		Attributes:      attrs,
		Credentials:     []kcCredential{{Type: "password", Value: inv.Password, Temporary: true}},
	}
	header, err := k.do(ctx, http.MethodPost, "/users", u, nil)
	var conflict *kcStatusError
	if errors.As(err, &conflict) && conflict.status == http.StatusConflict {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Keycloak 24+ drops attributes its user profile does not declare unless the realm lets admins edit unmanaged ones. An account without
	// the binding could never be recognised again (resend, cancel): fail closed, remove THE account just created (by the id Keycloak
	// answered, never by a search) and say why. Without that id nothing is removed.
	id := createdID(header.Get("Location"))
	if id == "" {
		return false, errors.New("keycloak: account created but its id was not returned; cannot verify its binding")
	}
	var created kcUser
	if err := k.call(ctx, http.MethodGet, "/users/"+url.PathEscape(id), nil, &created); err != nil {
		return false, err
	}
	if !created.boundTo(inv) {
		if derr := k.call(ctx, http.MethodDelete, "/users/"+url.PathEscape(id), nil, nil); derr != nil {
			log.Printf("keycloak: account created without the omnira attributes could not be removed: %v", derr)
		}
		log.Printf("%v", errLoginMarkerDropped)
		return false, errLoginMarkerDropped
	}
	return true, nil
}

// createdID reads the new user's id from the Location header of POST /users (.../users/<id>).
func createdID(location string) string {
	i := strings.LastIndex(location, "/users/")
	if i < 0 {
		return ""
	}
	id := location[i+len("/users/"):]
	if id == "" || strings.ContainsAny(id, "/?#") {
		return ""
	}
	return id
}

// updateUser writes back the whole representation read from Keycloak with only what change touches, so names and attributes are kept.
// still is asked again on that fresh representation: if the account no longer qualifies, nothing is written (errLoginChanged).
func (k *KeycloakLogins) updateUser(ctx context.Context, id string, still func(kcUser) bool, change func(rep map[string]any)) error {
	var raw json.RawMessage
	path := "/users/" + url.PathEscape(id)
	if err := k.call(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return err
	}
	var fresh kcUser
	var rep map[string]any
	if err := json.Unmarshal(raw, &fresh); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return err
	}
	if !still(fresh) {
		return errLoginChanged
	}
	change(rep)
	return k.call(ctx, http.MethodPut, path, rep, nil)
}

// setAttributes sets the given attributes on a user representation and removes the ones named in drop.
func setAttributes(rep map[string]any, set map[string]string, drop ...string) {
	attrs, _ := rep["attributes"].(map[string]any)
	if attrs == nil {
		attrs = map[string]any{}
	}
	for name, v := range set {
		attrs[name] = []any{v}
	}
	for _, name := range drop {
		delete(attrs, name)
	}
	rep["attributes"] = attrs
}

type kcStatusError struct {
	method, path string
	status       int
}

func (e *kcStatusError) Error() string {
	return fmt.Sprintf("keycloak: %s %s answered %d", e.method, e.path, e.status)
}

func (k *KeycloakLogins) call(ctx context.Context, method, path string, body, out any) error {
	_, err := k.do(ctx, method, path, body, out)
	return err
}

// do is call that also returns the response headers (POST /users answers the new id in Location).
func (k *KeycloakLogins) do(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	for attempt := 0; ; attempt++ {
		token, err := k.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, k.base+"/admin/realms/"+url.PathEscape(k.realm)+path, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := k.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("keycloak: %s unreachable", method)
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 {
			k.forgetToken()
			continue
		}
		if res.StatusCode >= 300 {
			return res.Header, &kcStatusError{method: method, path: strings.SplitN(path, "?", 2)[0], status: res.StatusCode}
		}
		if out != nil && len(data) > 0 {
			return res.Header, json.Unmarshal(data, out)
		}
		return res.Header, nil
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
