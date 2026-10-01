# Signing in to Eddy

Eddy never decides what a person may do. It works out who they are and which groups they are in, and every cluster read and write then runs impersonated as that user and those groups, so each workload cluster's own RBAC has the final say. This page covers the sign-in side: which providers exist, how to set each one up, and how identities become Kubernetes users and groups.

- [Sign-in options](#sign-in-options)
- [How a provider sign-in works](#how-a-provider-sign-in-works)
- [GitHub](#github): [create a GitHub App](#create-your-own-github-app), or [an OAuth App](#alternative-a-github-oauth-app)
- [OpenID Connect](#openid-connect): [Google](#google), [Okta](#okta), [Microsoft Entra ID](#microsoft-entra-id), [Dex](#dex)
- [From identity to Kubernetes user and groups](#from-identity-to-kubernetes-user-and-groups)
- [Passwords: normal or break-glass](#passwords-normal-or-break-glass)
- [Private networks, VPNs and egress](#private-networks-vpns-and-egress)
- [Sessions, group freshness and tokens](#sessions-group-freshness-and-tokens)
- [Troubleshooting](#troubleshooting)
- [SAML](#saml)

## Sign-in options

| Option | Config | Best for |
|---|---|---|
| GitHub | `config.auth.github` | Teams that already organise people in GitHub organizations and teams. github.com or GitHub Enterprise Server |
| OpenID Connect | `config.auth.oidc[]` | Google Workspace, Okta, Microsoft Entra ID, Dex, Keycloak, GitLab, any OIDC provider. Several at once |
| Local users | `config.auth.local` | Air-gapped installs, kind, or a break-glass admin next to a provider |
| Trusted proxy headers | `config.auth.proxy` / `oauth2Proxy` | An existing oauth2-proxy or Pomerium in front of your tools ([install guide, step 6](install.md#6-optional-sign-in-through-your-identity-provider)) |

You can enable several at once. The sign-in page shows a **Continue with …** button per provider, then the password form. Two settings make it seamless:

- With exactly one provider and passwords off (or in [break-glass mode](#passwords-normal-or-break-glass)), the page goes straight to that provider. It shows "Signing you in with GitHub…" for a moment, with a link to stop.
- After a failed sign-in, the page shows a short reason and never redirects on its own, so a broken provider cannot loop.

## How a provider sign-in works

GitHub and OIDC use the OAuth 2.0 authorization code flow with PKCE, and OIDC adds an ID token with a nonce.

1. The browser opens `/auth/<id>/login?returnTo=/where/you/were`. `returnTo` must be a local path.
2. The hub creates a random `state`, a `nonce` and a PKCE verifier. It stores them, with `returnTo`, in a cookie (`__Host-eddy_oauth`) that is HttpOnly, SameSite=Lax, valid for 10 minutes and encrypted and authenticated with a key derived from `auth.keyFile`. Then it redirects the browser to the provider.
3. The provider sends the browser back to `/auth/<id>/callback?code=…&state=…`. The hub reads the cookie, clears it whatever happens, and checks that `state` matches. A callback that the browser did not start (a login CSRF attempt) fails here.
4. The hub exchanges the code with the client secret and the PKCE verifier. For OIDC it verifies the ID token's signature against the provider's JWKS, and checks `iss`, `aud` (and `azp`), `exp` and the `nonce`.
5. The provider's rules run (organizations, teams, domains, groups, verified email). Then the shared mapping turns the identity into a Kubernetes user and groups, exactly as for every other sign-in method.
6. The hub creates a new server-side session with a fresh id, sets the session cookie, writes a `login` audit event with the provider, and redirects to `returnTo`.

Failures redirect to `/login?error=<code>` with a generic message. The detailed reason is only in the audit log. Failed callbacks are rate-limited per client address (`auth.loginRateLimit.perIPPerMinute`, default 20 a minute). Access tokens and ID tokens are used once during the callback, then dropped: they are never stored or logged.

Register this **callback URL** at the provider for each one you enable. The chart's NOTES print them after install:

```
<publicURL>/auth/github/callback
<publicURL>/auth/<oidc id>/callback
```

`publicURL` must be exactly the address people type in the browser, including the scheme and any port.

## GitHub

Eddy can sign people in with a **GitHub App** (recommended) or an **OAuth App**. Both use the same browser flow, and Eddy works the same way with either one:

- **User name:** the primary, verified email address of the account, in lower case. If there is none, it is `github:<login>`, also lower case.
- **Groups:** `github:<org>` for each organization the user is an **active** member of, and `github:<org>/<team-slug>` for each team. Only organizations in `allowedOrganizations` count. Names are lower case, and get the `eddy:` prefix like every group, so the team `acme/platform` becomes `eddy:github:acme/platform`.
- **Who may sign in:** members of at least one organization in `allowedOrganizations`. With `allowedTeams` (`org/team-slug`), they must also be in one of those teams. `allowedOrganizations` is required, because otherwise any GitHub account could sign in. `allowAllUsers: true` turns that off, which only makes sense on GitHub Enterprise Server.

GitHub App or OAuth App?

| | GitHub App | OAuth App |
|---|---|---|
| Access | Exactly two read-only permissions, only in the organizations it is installed in | The scopes `read:user user:email read:org`, across every organization the user approves |
| Organization approval | Installing the app in the organization is the approval | If the organization restricts OAuth apps, an owner must approve it, or memberships stay hidden and sign-in is denied |
| User tokens | Expire after 8 hours | Do not expire |

Eddy uses the token only during the callback either way.

### Create your own GitHub App

You need to be an owner of the organization (or have the GitHub App manager role).

1. **Open the app settings.** In GitHub, go to your organization, then **Settings → Developer settings → GitHub Apps → New GitHub App**. On GitHub Enterprise Server the path is the same on your server. To own the app personally instead, use your account's **Settings → Developer settings → GitHub Apps**.
2. **Fill in the basics:**
   - **GitHub App name:** for example `Eddy (acme)`. It is shown when people authorize it.
   - **Homepage URL:** your `publicURL`, for example `https://eddy.internal.example.com`.
   - **Callback URL:** `https://eddy.internal.example.com/auth/github/callback`.
   - Leave **Expire user authorization tokens** checked.
   - Leave **Request user authorization (OAuth) during installation** and **Enable Device Flow** unchecked.
   - **Webhook:** uncheck **Active**. Eddy needs no webhooks.
3. **Set the permissions.** Everything else stays at "No access":
   - **Organization permissions → Members: Read-only**, for organization memberships and teams.
   - **Account permissions → Email addresses: Read-only**, for the primary verified email.
4. **Where can this GitHub App be installed?** Choose **Only on this account**. Then click **Create GitHub App**.
5. **Note the client ID** on the app's page. It starts with `Iv` (not the numeric App ID).
6. **Generate a client secret.** Under **Client secrets**, click **Generate a new client secret** and copy it now, because GitHub shows it only once. You do **not** need a private key: Eddy never acts as the app itself.
7. **Install the app** in the organization: **Install App** in the left menu, then **Install** next to the organization, for all repositories. No repository access is requested. Without the installation, the user's token cannot see the organization and sign-in is denied. Repeat this for each organization in `allowedOrganizations`.
8. **Put the secret in a Kubernetes Secret.** The chart loads `credentialsSecret` as environment variables:

   ```sh
   read -rs GH_SECRET   # paste the client secret, then Enter
   kubectl -n eddy create secret generic eddy-credentials \
     --from-literal=GITHUB_CLIENT_SECRET="$GH_SECRET" \
     --dry-run=client -o yaml | kubectl apply -f -
   ```

   If `eddy-credentials` already holds other keys (`ANTHROPIC_API_KEY`, `EDDY_PROXY_SECRET`), add the new key to it rather than replacing it, for example with `kubectl edit secret` or your secrets tooling (External Secrets, Sealed Secrets, SOPS).
9. **Configure the chart** in `hub-values.yaml`:

   ```yaml
   publicURL: https://eddy.internal.example.com
   credentialsSecret: eddy-credentials

   config:
     auth:
       github:
         enabled: true
         clientID: Iv23liAbCdEfGhIjKlMn
         allowedOrganizations: [acme]
         # allowedTeams: [acme/platform, acme/sre]   # optional: also require one of these teams
         # teamsAsGroups: true                       # the default
         # baseURL: https://github.example.com       # GitHub Enterprise Server only
       local:
         enabled: true
         mode: breakglass          # keep a hidden admin password login (see below), or enabled: false
   ```

   Then run `helm upgrade --install eddy oci://ghcr.io/idestis/charts/eddy-hub -n eddy -f hub-values.yaml`.
10. **Test it.** Open `publicURL` in a private window. You should land on GitHub's **Authorize** page for your app, then back in Eddy. Hover your name at the bottom of the sidebar, or open `/api/v1/me`, to check the groups: `eddy:authenticated`, `eddy:github:acme` and one `eddy:github:acme/<team>` per team. The hub's audit log has a `login` event with `"provider":"github"`.
11. **Grant RBAC** in each workload cluster to the new groups. See [the example below](#from-identity-to-kubernetes-user-and-groups).

To rotate the secret, generate a second client secret in GitHub, update the Kubernetes Secret, restart the hub (`kubectl -n eddy rollout restart deploy/eddy-hub`), then delete the old secret in GitHub.

### Alternative: a GitHub OAuth App

1. In the organization (or your account), open **Settings → Developer settings → OAuth Apps → New OAuth App**.
2. Set **Application name**, the **Homepage URL** (your `publicURL`) and the **Authorization callback URL** (`<publicURL>/auth/github/callback`). Leave **Enable Device Flow** unchecked. Click **Register application**.
3. Copy the **Client ID**, click **Generate a new client secret** and copy the secret.
4. Eddy asks for the scopes `read:user user:email read:org`, all read-only. If the organization has **OAuth app access restrictions** on (Organization settings → Third-party access → OAuth application policy), an owner must **approve** the app. Until then GitHub hides the user's membership and Eddy denies the sign-in. A user can request approval on GitHub's authorize page.
5. Continue with steps 8 to 11 above. The values are the same.

### GitHub Enterprise Server

Set `baseURL: https://github.example.com`. Eddy then uses `<baseURL>/login/oauth/…` for the browser flow and `<baseURL>/api/v3` for the API. A non-standard API location can be set with `apiURL`. The hub must reach the server over HTTPS. If its certificate comes from a private CA, add the CA to the hub image or mount it with `extraEnv: [{name: SSL_CERT_FILE, value: …}]` plus a volume. If the server is not on port 443, add an egress rule under `networkPolicy.egress.extra`.

## OpenID Connect

Each entry in `config.auth.oidc` is one provider with its own button, its own routes `/auth/<id>/login` and `/auth/<id>/callback`, and sessions named `oidc:<id>`.

| Key | Default | Meaning |
|---|---|---|
| `id` | required | `^[a-z0-9][a-z0-9-]{0,30}$`. Not `local`, `proxy`, `dev`, `github`, `csrf`, `providers`, `logout`, `oidc` or `saml` |
| `name` | the preset's name, or `id` | Button label: "Continue with `<name>`" |
| `preset` | none | `google`, `okta`, `entra` or `dex`. Fills in the defaults below. Your settings win |
| `issuer` | `https://accounts.google.com` for `google` | The issuer URL. Discovery is `<issuer>/.well-known/openid-configuration`. It must be https (http only for localhost) |
| `clientID` | required | |
| `clientSecretEnv` | `OIDC_<ID>_CLIENT_SECRET` | The env var in `credentialsSecret` holding the secret. The id is upper-cased and `-` becomes `_` |
| `scopes` | `openid email profile` (+ `groups` for `okta` and `dex`) | Must include `openid` |
| `usernameClaim` | `email` (`preferred_username` for `entra`) | The claim that becomes the Kubernetes user name |
| `groupsClaim` | `groups` for `okta`, `entra` and `dex` | A claim with a list of strings. Empty means the provider sends no groups |
| `allowedDomains` | none | Only these domains may sign in. Read from `hostedDomainClaim` when set, otherwise from the verified email |
| `hostedDomainClaim` | `hd` for `google` | |
| `allowedGroups` | none | When set, the user needs at least one of these groups (raw names, before the `eddy:` prefix) |
| `requireVerifiedEmail` | `true` (`false` for `entra`) | When the email names the user or decides the domain, `email_verified` must be `true` |
| `allowAllUsers` | `false` | Required with the `google` preset when `allowedDomains` is empty |
| `userPrefix` | `""` | Put in front of the user name, for example `okta:` |

The hub starts even if an issuer cannot be reached. Sign-in through that provider shows "cannot be reached" until discovery succeeds, and the hub retries at most every 10 seconds. Only claims from the verified ID token are used. Eddy does not call the userinfo endpoint, so the groups must be in the ID token.

### Google

1. In the [Google Cloud console](https://console.cloud.google.com/), pick or create a project. Under **Google Auth Platform → Branding**, set up the consent screen. With Google Workspace, choose **Audience: Internal** so only your organization can use it.
2. Under **Clients → Create client**, choose **Web application**. Add the **Authorized redirect URI** `<publicURL>/auth/google/callback`.
3. Copy the client ID and secret. Store the secret as `OIDC_GOOGLE_CLIENT_SECRET` in `credentialsSecret`.

```yaml
config:
  auth:
    oidc:
      - id: google
        preset: google
        clientID: 1234567890-abc.apps.googleusercontent.com
        allowedDomains: [example.com]
    groups:
      static:                          # Google ID tokens carry no groups
        alice@example.com: [platform]
        bob@example.com: [platform, oncall]
```

`allowedDomains` is checked against the `hd` (hosted domain) claim, which only Google Workspace accounts have. A personal Gmail account, or an account from another Workspace, is denied even if its email looks right. The email must also be verified. With one allowed domain, Eddy also passes `hd` to Google, which then pre-selects the right account. For real group membership from Google Workspace, put [Dex](#dex) in front with its Google connector.

### Okta

1. In the Okta admin console: **Applications → Create App Integration → OIDC - OpenID Connect → Web Application**.
2. Set **Sign-in redirect URIs** to `<publicURL>/auth/okta/callback`. Under **Assignments**, choose who may use it. Save, then copy the client ID and secret (`OIDC_OKTA_CLIENT_SECRET`).
3. Put groups in the ID token:
   - **Org authorization server** (issuer `https://<org>.okta.com`): in the app's **Sign On** tab, edit **OpenID Connect ID Token** and set the **Groups claim** to type *Filter*, name `groups`, *Matches regex* `.*` (or a narrower filter such as `^eddy-`).
   - **Custom authorization server** (issuer `https://<org>.okta.com/oauth2/default`): under **Security → API → Authorization Servers → default → Claims**, add a claim `groups` of value type *Groups*, a filter, and **Include in token type: ID Token, Always**. If that server has no `groups` scope, set `scopes: [openid, email, profile]`.

```yaml
config:
  auth:
    oidc:
      - id: okta
        preset: okta
        issuer: https://example.okta.com/oauth2/default
        clientID: 0oa1b2c3d4e5f6g7h8i9
        allowedGroups: [eddy-users]    # optional gate; eddy-users becomes eddy:eddy-users
```

### Microsoft Entra ID

1. In the Entra admin center: **Applications → App registrations → New registration**. Choose **Accounts in this organizational directory only** (single tenant). Set the **Redirect URI** to platform *Web*, `<publicURL>/auth/entra/callback`.
2. **Certificates & secrets → New client secret.** Copy the value (not the secret ID) into `OIDC_ENTRA_CLIENT_SECRET`. Note when it expires.
3. **Token configuration → Add groups claim.** Choose *Security groups* (or *Groups assigned to the application*), and for the **ID** token, *Group ID*. Groups then arrive as object IDs, so `eddy:3f2c…` is what you bind in RBAC. For groups synced from on-premises AD you can choose *sAMAccountName* instead.
4. Use the tenant-specific issuer `https://login.microsoftonline.com/<tenant-id>/v2.0`. The multi-tenant `common` and `organizations` endpoints are rejected because their issuer does not match.

```yaml
config:
  auth:
    oidc:
      - id: entra
        preset: entra
        name: Microsoft
        issuer: https://login.microsoftonline.com/00000000-0000-0000-0000-000000000000/v2.0
        clientID: 11111111-2222-3333-4444-555555555555
```

The user name is `preferred_username` (the UPN, for example `alice@contoso.com`), because Entra ID sends no `email_verified`. If a user is in more than 200 groups, Entra leaves the claim out of the token ("overage"). Then Eddy sees no groups. Use *Groups assigned to the application*, or app roles with `groupsClaim: roles`, to keep the list short.

### Dex

[Dex](https://dexidp.io) federates LDAP, SAML, GitHub, Google Workspace groups and more behind one OIDC issuer. Add Eddy as a static client in the Dex config:

```yaml
staticClients:
  - id: eddy
    name: Eddy
    secretEnv: EDDY_CLIENT_SECRET
    redirectURIs: ["https://eddy.internal.example.com/auth/dex/callback"]
```

```yaml
config:
  auth:
    oidc:
      - id: dex
        preset: dex                    # scopes include groups; groupsClaim: groups
        issuer: https://dex.example.com
        clientID: eddy
```

Dex's connectors decide what `groups` contains. For example, the GitHub connector emits `org:team`, which becomes `eddy:acme:platform`.

## From identity to Kubernetes user and groups

Every sign-in method goes through the same mapping. Its output is exactly what the agents impersonate:

1. **User:**
   - GitHub: the primary verified email, or `github:<login>`.
   - OIDC: the `usernameClaim`.
   - Proxy: the user header.
   - Local users: `local:<username>`.
   
   `userPrefix` comes first. The name must be an email address or match `^[a-zA-Z0-9._@-]{1,128}$` (plus the prefix). `system:` users and anything matching `auth.denyUserPrefixes` (default `system:`, `eks:`, `kubernetes-admin`) are refused.
2. **Groups:** each raw group must match `^[A-Za-z0-9._:/@-]+$`. `system:*` groups are dropped, both before and after prefixing. Every group gets `auth.groups.prefix` (`eddy:`), which must be in the agent's `impersonation.allowedGroupPrefixes`.
3. Every user also gets `eddy:authenticated` (`groups.allUsers`), plus any `groups.static` entries for their email, login or user name.
4. Duplicates are removed. Groups longer than `maxGroupLength` (128), and any beyond `maxGroups` (64), are dropped with a warning.

Grant access in each workload cluster. `deploy/rbac/eddy-user-rbac.yaml` defines `eddy-viewer` (bound to `eddy:authenticated`) and `eddy-operator` (adds `patch` on Flux kinds, which reconcile, suspend and resume need). To let the GitHub team `acme/platform` operate:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: eddy-operator-platform
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: eddy-operator
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: eddy:github:acme/platform
```

The same pattern works for `eddy:<okta group>`, `eddy:<Entra group object id>` and so on. Use a RoleBinding to limit a team to some namespaces.

## Passwords: normal or break-glass

`config.auth.local` keeps working next to providers:

- `enabled: false` turns password sign-in off completely.
- `mode: normal` (the default) shows the password form on the sign-in page.
- `mode: breakglass` keeps password sign-in for emergencies, such as an identity provider outage or a misconfigured organization list:
  - The form is hidden. A small **Admin sign-in** link leads to `/login?local=1`, the only place it appears.
  - Logins are rate-limited and locked out exactly like normal mode.
  - Every attempt and every success is logged as a `WARN`. The `login` audit events carry `"mode":"breakglass"`, so you can alert on them.
  - It needs another sign-in method (GitHub, OIDC or proxy), otherwise the chart and the hub refuse to start.
- `allowedUsers: [admin]` limits password sign-in to those usernames. Others get the same generic error as a wrong password, and existing sessions and tokens of users outside the list stop working.

A typical setup: one local `admin` user in group `platform`, in break-glass mode, with its password in a vault.

```yaml
config:
  auth:
    local: {enabled: true, mode: breakglass, allowedUsers: [admin]}
users:
  list:
    - username: admin
      passwordHash: "$argon2id$v=19$m=65536,t=3,p=4$...$..."
      groups: [platform]
```

## Private networks, VPNs and egress

GitHub and OIDC sign-in work for a hub that is only reachable on a private network or VPN.

- **Both legs of the flow are browser redirects.** The identity provider never connects to the hub. The callback URL only has to resolve and be reachable *from the user's browser*, so a private hostname such as `https://eddy.internal.example.com` is fine. GitHub, Google, Okta and Entra all accept private and VPN-only callback URLs.
- **The hub makes outbound HTTPS calls** to the provider. Allow them in your firewall, proxy and NetworkPolicy. The chart's default egress policy already allows TCP 443 to anywhere, plus DNS.

| Provider | Hosts the hub calls | For |
|---|---|---|
| GitHub | `github.com` (token), `api.github.com` (user, emails, memberships, teams) | Every sign-in |
| GitHub Enterprise Server | your server: `/login/oauth/access_token` and `/api/v3` | Every sign-in |
| Google | `accounts.google.com` (discovery), `oauth2.googleapis.com` (token), `www.googleapis.com` (JWKS) | Discovery once, then every sign-in. JWKS when keys rotate |
| Okta | `<org>.okta.com` | The same |
| Entra ID | `login.microsoftonline.com` | The same |
| Dex and others | the issuer host, and whatever its discovery document names | The same |

- An outbound HTTP proxy is honoured through the standard `HTTPS_PROXY` and `NO_PROXY` variables (`extraEnv`).
- Only the browser talks to the provider's authorization page, so users need to reach it too. That is usually the public internet, or your IdP's own private endpoint.

## Sessions, group freshness and tokens

- Sessions are server-side. The cookie is `__Host-eddy_session` (Secure, HttpOnly, SameSite=Lax). Its id is new at every sign-in. Sessions expire after 8 hours idle or 24 hours in total (`auth.session`).
- **Groups are captured at sign-in.** A GitHub team change, or an IdP group change, takes effect at the next sign-in. The 24 hour absolute timeout bounds how stale a session's groups can be. To apply a change at once, the user signs out and in again. An admin can end every session and token of a user with `eddy-hub admin revoke --user <user>`.
- Removing a provider from the config ends its sessions and invalidates its tokens on the next request.
- **Personal access tokens** for GitHub and OIDC users follow the proxy-user rules. They last at most `auth.tokens.maxTTLProxy` (30 days), because Eddy cannot see deprovisioning at the IdP. Their groups are the snapshot taken at creation, intersected with the groups of the user's latest session, so they can only shrink.

## Troubleshooting

| You see | Usual cause |
|---|---|
| GitHub or the IdP says the redirect URI does not match | The callback registered there differs from `<publicURL>/auth/<id>/callback`: check the scheme, host, port and trailing slashes |
| "Your sign-in expired or was started in another tab" (`error=state`) | More than 10 minutes at the provider, sign-in started in another tab, cookies blocked, or `publicURL` differs from the address in the browser (so the cookie is not sent back) |
| "Your account is not allowed" (`error=denied`) | Not an active member of an `allowedOrganizations` org, the GitHub App is not installed in the org, the OAuth App is not approved by the org, the email is unverified, or the domain or group is not allowed. The `login` audit event has the reason |
| "The sign-in provider returned an error" (`error=provider`) | Wrong client secret, an expired Entra secret, clock skew on the hub (the ID token's `exp`), or the token endpoint is unreachable. The hub log has a `WARN` line with the provider and cause, never the token |
| "cannot be reached" (`error=unavailable`) | OIDC discovery failed: check egress, DNS and the issuer URL |
| The page loops back to the provider | It cannot loop: after an error the page stops auto sign-in. Use **Show other sign-in options** or `/login?local=1` |

## SAML

SAML 2.0 is not in v1.0. It is planned with IdP metadata URLs, an SP key pair (`samlCertSecret`) and email and groups attributes, and it will feed the same mapping. Like OIDC, SAML works on private networks: the IdP sends its response *through the browser*, as a form POST to the hub's assertion consumer service (ACS) URL, so the IdP never needs to reach the hub. Until then, use OIDC (most SAML IdPs also speak OIDC), Dex's SAML connector, or a proxy.
