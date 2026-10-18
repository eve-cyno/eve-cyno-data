package keys

import "net/http"

// accountsResolve serves POST /admin/v1/accounts/resolve.
func (a *admin) accountsResolve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider  string `json:"provider"`
		Subject   string `json:"subject"`
		OwnerHash string `json:"owner_hash"`
		SignIn    bool   `json:"sign_in"`
		Tester    *bool  `json:"tester"`
	}
	if !a.decode(w, r, &body, false) {
		return
	}
	acc, err := a.s.Resolve(r.Context(),
		Identity{Provider: body.Provider, Subject: body.Subject, OwnerHash: body.OwnerHash},
		ResolveOptions{SignIn: body.SignIn, Tester: body.Tester})
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, acc)
}

// accountsLink serves POST /admin/v1/accounts/link.
func (a *admin) accountsLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identity Identity `json:"identity"`
		Link     Identity `json:"link"`
	}
	if !a.decode(w, r, &body, false) {
		return
	}
	acc, merged, err := a.s.Link(r.Context(), body.Identity, body.Link)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, struct {
		Account
		Merged []string `json:"merged"`
	}{acc, mergedList(merged)})
}

// accountsUnlink serves POST /admin/v1/accounts/unlink.
func (a *admin) accountsUnlink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identity Identity `json:"identity"`
		Unlink   struct {
			Provider string `json:"provider"`
			Subject  string `json:"subject"`
		} `json:"unlink"`
	}
	if !a.decode(w, r, &body, false) {
		return
	}
	acc, err := a.s.Unlink(r.Context(), body.Identity, body.Unlink.Provider, body.Unlink.Subject)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, acc)
}

// mergedList keeps an empty merge a JSON array, never null.
func mergedList(m []string) []string {
	if m == nil {
		return []string{}
	}
	return m
}

// accountsDelete serves POST /admin/v1/accounts/delete.
func (a *admin) accountsDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identity Identity `json:"identity"`
	}
	if !a.decode(w, r, &body, false) {
		return
	}
	d, err := a.s.DeleteAccountOf(r.Context(), body.Identity)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, d)
}

// opsDeleteAccount serves DELETE /admin/v1/ops/accounts/{account}: the operator deletes an account at its user's
// request. A blocked account is deleted too, and its block stays on its sign-in ids.
func (a *admin) opsDeleteAccount(w http.ResponseWriter, r *http.Request) {
	acct := r.PathValue("account")
	d, err := a.s.DeleteAccount(r.Context(), acct)
	if err != nil {
		a.storeErr(w, r, err)
		return
	}
	a.write(w, http.StatusOK, struct {
		Account string `json:"account"`
		DeletedAccount
	}{acct, d})
}
