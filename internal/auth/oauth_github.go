package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/oauth2"

	"github.com/idestis/eddy/internal/config"
)

// ProviderGitHub names GitHub sessions and tokens.
const ProviderGitHub = "github"

const (
	githubPerPage  = 100
	githubMaxPages = 10 // 1,000 orgs or teams per user is plenty
	githubMaxBody  = 1 << 20
)

// githubLoginRE is GitHub's login and slug shape (lower-cased before use).
var githubLoginRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,98}[a-z0-9])?$`)

// githubProvider signs users in through a GitHub OAuth App or a GitHub App
// (user-to-server tokens). Both use the same web flow; the GitHub App needs
// the Members (organization, read) and Email addresses (account, read)
// permissions, the OAuth App the scopes read:user user:email read:org.
type githubProvider struct {
	cfg          config.GitHubAuth
	oc           *oauth2.Config
	api          string
	svc          *Service
	allowedOrgs  map[string]bool // lower-cased; empty means any (allowAllUsers)
	allowedTeams map[string]bool // lower-cased org/slug
}

func newGitHubProvider(s *Service, c config.GitHubAuth, secret string) (*githubProvider, error) {
	web := "https://github.com"
	api := "https://api.github.com"
	if c.BaseURL != "" {
		web = strings.TrimSuffix(c.BaseURL, "/")
		api = web + "/api/v3"
	}
	if c.APIURL != "" {
		api = strings.TrimSuffix(c.APIURL, "/")
	}
	if _, err := url.Parse(api); err != nil {
		return nil, fmt.Errorf("auth: github api url: %w", err)
	}
	p := &githubProvider{
		cfg: c,
		api: api,
		svc: s,
		oc: &oauth2.Config{
			ClientID:     c.ClientID,
			ClientSecret: secret,
			Endpoint: oauth2.Endpoint{
				AuthURL:   web + "/login/oauth/authorize",
				TokenURL:  web + "/login/oauth/access_token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
			RedirectURL: s.callbackURL(ProviderGitHub),
			// GitHub Apps ignore scopes and use the app's permissions.
			Scopes: []string{"read:user", "user:email", "read:org"},
		},
		allowedOrgs:  map[string]bool{},
		allowedTeams: map[string]bool{},
	}
	for _, o := range c.AllowedOrganizations {
		p.allowedOrgs[strings.ToLower(strings.TrimSpace(o))] = true
	}
	for _, t := range c.AllowedTeams {
		p.allowedTeams[strings.ToLower(strings.TrimSpace(t))] = true
	}
	return p, nil
}

func (p *githubProvider) info() ProviderInfo {
	return ProviderInfo{ID: ProviderGitHub, Name: p.cfg.Name, Kind: "github", Icon: "github", LoginURL: "/auth/github/login"}
}

func (p *githubProvider) sessionProvider() string { return ProviderGitHub }

func (p *githubProvider) authCodeURL(_ context.Context, f oauthFlow) (string, error) {
	return p.oc.AuthCodeURL(f.State, oauth2.S256ChallengeOption(f.Verifier), oauth2.SetAuthURLParam("allow_signup", "false")), nil
}

type githubUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

type githubMembership struct {
	State        string `json:"state"`
	Organization struct {
		Login string `json:"login"`
	} `json:"organization"`
}

type githubTeam struct {
	Slug         string `json:"slug"`
	Organization struct {
		Login string `json:"login"`
	} `json:"organization"`
}

func (p *githubProvider) identify(ctx context.Context, code string, f oauthFlow) (*externalIdentity, error) {
	tok, err := p.oc.Exchange(p.svc.oauthContext(ctx), code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return nil, fmt.Errorf("auth: github code exchange: %w", sanitizeOAuthErr(err))
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("auth: github code exchange: no access token")
	}
	bearer := tok.AccessToken

	var u githubUser
	if err := p.get(ctx, bearer, "/user", &u); err != nil {
		return nil, err
	}
	login := strings.ToLower(u.Login)
	if !githubLoginRE.MatchString(login) {
		return nil, denied("github login has an unexpected shape")
	}

	email := ""
	var emails []githubEmail
	if err := p.pages(ctx, bearer, "/user/emails", func(b []byte) (int, error) {
		var page []githubEmail
		err := json.Unmarshal(b, &page)
		emails = append(emails, page...)
		return len(page), err
	}); err != nil {
		return nil, err
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			email = strings.ToLower(strings.TrimSpace(e.Email))
			break
		}
	}

	var orgs []string
	if err := p.pages(ctx, bearer, "/user/memberships/orgs?state=active", func(b []byte) (int, error) {
		var page []githubMembership
		err := json.Unmarshal(b, &page)
		for _, m := range page {
			o := strings.ToLower(m.Organization.Login)
			if m.State == "active" && githubLoginRE.MatchString(o) {
				orgs = append(orgs, o)
			}
		}
		return len(page), err
	}); err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, o := range orgs {
		if len(p.allowedOrgs) == 0 || p.allowedOrgs[o] {
			kept[o] = true
		}
	}
	if len(p.allowedOrgs) > 0 && len(kept) == 0 {
		return nil, denied("github user %s is not an active member of an allowed organization", login)
	}

	raw := make([]string, 0, len(kept))
	for _, o := range orgs {
		if kept[o] {
			raw = append(raw, "github:"+o)
		}
	}
	if p.cfg.TeamsAsGroupsOrDefault() || len(p.allowedTeams) > 0 {
		var teams []string
		if err := p.pages(ctx, bearer, "/user/teams", func(b []byte) (int, error) {
			var page []githubTeam
			err := json.Unmarshal(b, &page)
			for _, t := range page {
				o, slug := strings.ToLower(t.Organization.Login), strings.ToLower(t.Slug)
				if kept[o] && githubLoginRE.MatchString(slug) {
					teams = append(teams, o+"/"+slug)
				}
			}
			return len(page), err
		}); err != nil {
			return nil, err
		}
		if len(p.allowedTeams) > 0 {
			ok := false
			for _, t := range teams {
				if p.allowedTeams[t] {
					ok = true
					break
				}
			}
			if !ok {
				return nil, denied("github user %s is not in an allowed team", login)
			}
		}
		if p.cfg.TeamsAsGroupsOrDefault() {
			for _, t := range teams {
				raw = append(raw, "github:"+t)
			}
		}
	}

	m := p.svc.mapper
	var subject, display, value string
	if email != "" {
		value = email
		subject, err = m.OAuthUser(p.cfg.UserPrefix, email)
	} else {
		value = "github:" + login
		subject, err = m.OAuthUser(p.cfg.UserPrefix+"github:", login)
	}
	if err != nil {
		return nil, denied("github user name not allowed: %v", err)
	}
	display = u.Name
	if display == "" || len(display) > 128 {
		display = u.Login
	}
	return &externalIdentity{
		subject: subject,
		display: display,
		groups:  m.Groups(raw, value, subject, "github:"+login),
		login:   value,
	}, nil
}

// pages walks a list endpoint with per_page/page parameters until a short
// page. Following the Link header is avoided on purpose so the hub only
// ever calls the configured API host.
func (p *githubProvider) pages(ctx context.Context, bearer, path string, add func([]byte) (int, error)) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= githubMaxPages; page++ {
		var body json.RawMessage
		if err := p.get(ctx, bearer, path+sep+"per_page="+strconv.Itoa(githubPerPage)+"&page="+strconv.Itoa(page), &body); err != nil {
			return err
		}
		n, err := add(body)
		if err != nil {
			return fmt.Errorf("auth: github %s: decode: %w", strings.SplitN(path, "?", 2)[0], err)
		}
		if n < githubPerPage {
			return nil
		}
	}
	return nil
}

func (p *githubProvider) get(ctx context.Context, bearer, path string, out any) error {
	name := strings.SplitN(path, "?", 2)[0]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.api+path, nil)
	if err != nil {
		return fmt.Errorf("auth: github %s: %w", name, err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := p.svc.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth: github %s: %w", name, sanitizeOAuthErr(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, githubMaxBody))
		return fmt.Errorf("auth: github %s: status %d", name, res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, githubMaxBody)).Decode(out); err != nil {
		return fmt.Errorf("auth: github %s: decode: %w", name, err)
	}
	return nil
}

// sanitizeOAuthErr drops response bodies from oauth2 errors (a token
// endpoint can echo request parameters) and URLs from transport errors.
func sanitizeOAuthErr(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode != "" && idpErrorRE.MatchString(re.ErrorCode) {
			return fmt.Errorf("token endpoint error %s", re.ErrorCode)
		}
		if re.Response != nil {
			return fmt.Errorf("token endpoint status %d", re.Response.StatusCode)
		}
		return fmt.Errorf("token endpoint error")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s request failed: %w", ue.Op, ue.Err)
	}
	return err
}
