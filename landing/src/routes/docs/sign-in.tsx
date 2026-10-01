import { createFileRoute, Link } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { FullRef } from "../../components/FullRef";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/sign-in")({
  head: () =>
    pageHead({
      title: "Sign-in · Eddy docs",
      description:
        "Set up sign-in for Eddy: a GitHub App step by step, OIDC for Google, Okta, Entra ID and Dex, local users, break-glass mode and a trusted proxy.",
      path: "/docs/sign-in/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Sign-in</h1>
      <p className="lead">
        Eddy works out who a person is and which groups they are in. It never decides what they may do: every
        cluster read and write is then impersonated as that user and those groups, and each cluster's own RBAC
        has the final say. This page sets up the sign-in side.
      </p>
      <FullRef path="docs/auth.md" />

      <h2 id="options">Choose a method</h2>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Method</th>
              <th scope="col">Config</th>
              <th scope="col">Best for</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">GitHub</th>
              <td>
                <code>config.auth.github</code>
              </td>
              <td>Teams organised in GitHub organizations and teams (github.com or Enterprise Server)</td>
            </tr>
            <tr>
              <th scope="row">OpenID Connect</th>
              <td>
                <code>config.auth.oidc[]</code>
              </td>
              <td>Google Workspace, Okta, Entra ID, Dex, Keycloak or any OIDC provider; several at once</td>
            </tr>
            <tr>
              <th scope="row">Local users</th>
              <td>
                <code>config.auth.local</code>
              </td>
              <td>Air-gapped installs, kind, or a break-glass admin next to a provider</td>
            </tr>
            <tr>
              <th scope="row">Trusted proxy</th>
              <td>
                <code>config.auth.proxy</code> / <code>oauth2Proxy</code>
              </td>
              <td>An existing oauth2-proxy or Pomerium in front of your tools</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        You can enable several at once. The sign-in page shows a <strong>Continue with …</strong> button per
        provider. With exactly one provider and passwords off, it goes straight to that provider. After a
        failed sign-in it shows a reason and never redirects on its own, so a broken provider cannot loop.
      </p>
      <p>
        For GitHub and OIDC, Eddy uses the OAuth authorization code flow with PKCE. Register this{" "}
        <strong>callback URL</strong> at the provider for each one you enable (the chart's install notes print
        them):
      </p>
      <CodeBlock
        code={`
https://eddy.internal.example.com/auth/github/callback
https://eddy.internal.example.com/auth/<oidc id>/callback
`}
      />
      <p>
        <code>publicURL</code> must be exactly the address people type in the browser.
      </p>

      <h2 id="github">GitHub</h2>
      <p>
        Eddy can sign people in with a <strong>GitHub App</strong> (recommended) or an{" "}
        <strong>OAuth App</strong>. Both use the same flow and give the same result:
      </p>
      <ul>
        <li>
          <strong>User name:</strong> the primary, verified email of the account, in lower case, or{" "}
          <code>github:&lt;login&gt;</code> if there is none.
        </li>
        <li>
          <strong>Groups:</strong> <code>github:&lt;org&gt;</code> for each organization the user is an active
          member of, and <code>github:&lt;org&gt;/&lt;team-slug&gt;</code> for each team. Only organizations
          in <code>allowedOrganizations</code> count. With the <code>eddy:</code> prefix, the team{" "}
          <code>acme/platform</code> becomes <code>eddy:github:acme/platform</code>.
        </li>
        <li>
          <strong>Who may sign in:</strong> members of at least one organization in{" "}
          <code>allowedOrganizations</code>, which is required. With <code>allowedTeams</code> (
          <code>org/team-slug</code>) they must also be in one of those teams.
        </li>
      </ul>

      <h3 id="github-app">Create a GitHub App, step by step</h3>
      <p>You need to be an owner of the organization, or have the GitHub App manager role.</p>
      <ol>
        <li>
          <strong>Open the app settings.</strong> In GitHub, open your organization, then{" "}
          <strong>Settings → Developer settings → GitHub Apps → New GitHub App</strong>. On GitHub Enterprise
          Server the path is the same on your server. To own the app personally, use your account's{" "}
          <strong>Settings → Developer settings → GitHub Apps</strong>.
        </li>
        <li>
          <strong>Fill in the basics.</strong>
          <ul>
            <li>
              <strong>GitHub App name:</strong> for example <code>Eddy (acme)</code>.
            </li>
            <li>
              <strong>Homepage URL:</strong> <code>https://eddy.internal.example.com</code> (your{" "}
              <code>publicURL</code>).
            </li>
            <li>
              <strong>Callback URL:</strong>{" "}
              <code>https://eddy.internal.example.com/auth/github/callback</code>.
            </li>
            <li>
              Leave <strong>Expire user authorization tokens</strong> checked. Leave{" "}
              <strong>Request user authorization (OAuth) during installation</strong> and{" "}
              <strong>Enable Device Flow</strong> unchecked.
            </li>
            <li>
              <strong>Webhook:</strong> uncheck <strong>Active</strong>. Eddy needs no webhooks.
            </li>
          </ul>
        </li>
        <li>
          <strong>Set the permissions.</strong> Everything else stays at No access.
          <ul>
            <li>
              <strong>Organization permissions → Members: Read-only</strong>, for memberships and teams.
            </li>
            <li>
              <strong>Account permissions → Email addresses: Read-only</strong>, for the primary verified
              email.
            </li>
          </ul>
        </li>
        <li>
          <strong>Choose where it can be installed.</strong> Pick <strong>Only on this account</strong>, then
          click <strong>Create GitHub App</strong>.
        </li>
        <li>
          <strong>Note the client ID</strong> on the app's page. It starts with <code>Iv</code> (it is not the
          numeric App ID).
        </li>
        <li>
          <strong>Generate a client secret.</strong> Under <strong>Client secrets</strong>, click{" "}
          <strong>Generate a new client secret</strong> and copy it now, because GitHub shows it once. You do
          not need a private key: Eddy never acts as the app itself.
        </li>
        <li>
          <strong>Install the app</strong> in the organization: <strong>Install App</strong> in the left menu,
          then <strong>Install</strong> next to the organization. No repository access is requested. Without
          the installation, the user's token cannot see the organization and sign-in is denied. Repeat for
          each organization in <code>allowedOrganizations</code>.
        </li>
        <li>
          <strong>Put the secret in the Kubernetes Secret.</strong> Add it to <code>eddy-credentials</code>{" "}
          under the key <code>GITHUB_CLIENT_SECRET</code>, with the{" "}
          <Link to="/docs/install/" hash="add-key">
            add-a-key command
          </Link>
          . The chart loads <code>credentialsSecret</code> as environment variables.
        </li>
        <li>
          <strong>Configure the chart</strong> in <code>hub-values.yaml</code>:
          <CodeBlock
            code={`
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
    local:
      enabled: true
      mode: breakglass       # keep a hidden admin password login, or set enabled: false
`}
          />
          Then upgrade:
          <CodeBlock
            code={`
$ helm upgrade --install eddy-hub oci://ghcr.io/idestis/charts/eddy-hub --version 1.0.0 \\
    -n eddy -f hub-values.yaml
`}
          />
        </li>
        <li>
          <strong>Grant RBAC</strong> in each workload cluster to the new groups. See{" "}
          <Link to="/docs/access/">Access and RBAC</Link>.
        </li>
      </ol>
      <h3 id="github-verify">Verify</h3>
      <ul>
        <li>
          Open <code>publicURL</code> in a private window. You should reach GitHub's Authorize page for your
          app, then land back in Eddy.
        </li>
        <li>
          Open <code>/api/v1/me</code>. The groups should include <code>eddy:authenticated</code>,{" "}
          <code>eddy:github:acme</code> and one <code>eddy:github:acme/&lt;team&gt;</code> per team.
        </li>
        <li>
          The hub's audit log has a <code>login</code> event with <code>"provider":"github"</code>.
        </li>
      </ul>
      <p>
        To rotate the secret, generate a second client secret in GitHub, update the Kubernetes Secret, restart
        the hub (<code>kubectl -n eddy rollout restart deploy/eddy-hub</code>), then delete the old secret in
        GitHub.
      </p>

      <h3 id="github-oauth">Alternative: a GitHub OAuth App</h3>
      <ol>
        <li>
          In the organization (or your account), open{" "}
          <strong>Settings → Developer settings → OAuth Apps → New OAuth App</strong>.
        </li>
        <li>
          Set the <strong>Application name</strong>, the <strong>Homepage URL</strong> (your{" "}
          <code>publicURL</code>) and the <strong>Authorization callback URL</strong> (
          <code>&lt;publicURL&gt;/auth/github/callback</code>). Leave <strong>Enable Device Flow</strong>{" "}
          unchecked and click <strong>Register application</strong>.
        </li>
        <li>
          Copy the <strong>Client ID</strong>, click <strong>Generate a new client secret</strong> and copy
          the secret.
        </li>
        <li>
          Eddy asks for the read-only scopes <code>read:user user:email read:org</code>. If the organization
          restricts OAuth apps (Organization settings → Third-party access), an owner must approve the app.
          Until then GitHub hides the user's membership and Eddy denies sign-in.
        </li>
        <li>Continue with the Kubernetes Secret and chart values above. They are the same.</li>
      </ol>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col" />
              <th scope="col">GitHub App</th>
              <th scope="col">OAuth App</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">Access</th>
              <td>Two read-only permissions, only in organizations where it is installed</td>
              <td>Three read-only scopes, across every organization the user approves</td>
            </tr>
            <tr>
              <th scope="row">Organization approval</th>
              <td>Installing the app is the approval</td>
              <td>An owner must approve it if OAuth app access is restricted</td>
            </tr>
            <tr>
              <th scope="row">User tokens</th>
              <td>Expire after 8 hours</td>
              <td>Do not expire</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>Eddy uses the token only during the callback either way.</p>

      <h3 id="ghes">GitHub Enterprise Server</h3>
      <p>
        Create the app on your server the same way, and set <code>baseURL</code>. Eddy then uses{" "}
        <code>&lt;baseURL&gt;/login/oauth/…</code> for the browser flow and{" "}
        <code>&lt;baseURL&gt;/api/v3</code> for the API. Set <code>apiURL</code> if yours differs.
      </p>
      <CodeBlock
        code={`
config:
  auth:
    github:
      enabled: true
      clientID: Iv23liAbCdEfGhIjKlMn
      baseURL: https://github.example.com
      allowedOrganizations: [acme]
`}
      />
      <p>
        The hub must reach the server over HTTPS. If its certificate comes from a private CA, add the CA to
        the hub image or set <code>SSL_CERT_FILE</code> through <code>extraEnv</code> with a volume. If the
        server is not on port 443, add an egress rule under <code>networkPolicy.egress.extra</code>.
      </p>

      <h2 id="oidc">OpenID Connect</h2>
      <p>
        Each entry in <code>config.auth.oidc</code> is one provider with its own button, routes{" "}
        <code>/auth/&lt;id&gt;/login</code> and <code>/auth/&lt;id&gt;/callback</code>, and sessions named{" "}
        <code>oidc:&lt;id&gt;</code>. The client secret comes from <code>credentialsSecret</code> under{" "}
        <code>OIDC_&lt;ID&gt;_CLIENT_SECRET</code> (the id upper-cased, <code>-</code> becoming <code>_</code>
        ), or the variable named by <code>clientSecretEnv</code>. Add it with the{" "}
        <Link to="/docs/install/" hash="add-key">
          add-a-key command
        </Link>
        .
      </p>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Key</th>
              <th scope="col">Meaning</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">
                <code>id</code>
              </th>
              <td>
                Required. Lower case letters, digits and <code>-</code>
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>preset</code>
              </th>
              <td>
                <code>google</code>, <code>okta</code>, <code>entra</code> or <code>dex</code>: fills in
                defaults. Your settings win
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>issuer</code>, <code>clientID</code>
              </th>
              <td>
                The issuer URL (https) and the client ID. Discovery is{" "}
                <code>&lt;issuer&gt;/.well-known/openid-configuration</code>
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>usernameClaim</code>, <code>groupsClaim</code>
              </th>
              <td>
                The claims that become the Kubernetes user and groups. Groups must be in the ID token: Eddy
                does not call userinfo
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>allowedDomains</code>, <code>allowedGroups</code>
              </th>
              <td>Optional gates on sign-in</td>
            </tr>
            <tr>
              <th scope="row">
                <code>userPrefix</code>
              </th>
              <td>
                Put in front of the user name, for example <code>okta:</code>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        The hub starts even if an issuer cannot be reached. Sign-in through that provider shows &ldquo;cannot
        be reached&rdquo; until discovery succeeds.
      </p>

      <h3 id="google">Google</h3>
      <ol>
        <li>
          In the Google Cloud console, pick a project. Under <strong>Google Auth Platform → Branding</strong>,
          set up the consent screen. With Google Workspace choose <strong>Audience: Internal</strong>.
        </li>
        <li>
          Under <strong>Clients → Create client</strong>, choose <strong>Web application</strong> and add the
          authorized redirect URI <code>https://eddy.internal.example.com/auth/google/callback</code>.
        </li>
        <li>
          Store the secret as <code>OIDC_GOOGLE_CLIENT_SECRET</code> in <code>eddy-credentials</code>, and add
          the values:
          <CodeBlock
            code={`
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
`}
          />
        </li>
      </ol>
      <p>
        <code>allowedDomains</code> is checked against the <code>hd</code> claim, which only Google Workspace
        accounts have, so a personal Gmail account is denied. For real group membership, put Dex in front of
        Google with its Google connector.
      </p>

      <h3 id="okta">Okta</h3>
      <ol>
        <li>
          In the Okta admin console:{" "}
          <strong>Applications → Create App Integration → OIDC → Web Application</strong>. Set the sign-in
          redirect URI to <code>https://eddy.internal.example.com/auth/okta/callback</code>
          and choose who is assigned. Copy the client ID and the secret (<code>OIDC_OKTA_CLIENT_SECRET</code>
          ).
        </li>
        <li>
          Put groups in the ID token. On the org authorization server, edit the app's{" "}
          <strong>Sign On → OpenID Connect ID Token</strong>: Groups claim type <em>Filter</em>, name{" "}
          <code>groups</code>, matching a regex such as <code>^eddy-</code>. On a custom authorization server,
          add a <code>groups</code> claim of value type <em>Groups</em> included in the ID token, always.
        </li>
        <li>
          Add the values:
          <CodeBlock
            code={`
config:
  auth:
    oidc:
      - id: okta
        preset: okta
        issuer: https://example.okta.com/oauth2/default
        clientID: 0oa1b2c3d4e5f6g7h8i9
        allowedGroups: [eddy-users]    # optional gate; becomes eddy:eddy-users
`}
          />
        </li>
      </ol>

      <h3 id="entra">Microsoft Entra ID</h3>
      <ol>
        <li>
          In the Entra admin center: <strong>App registrations → New registration</strong>, single tenant. Add
          a <em>Web</em> redirect URI <code>https://eddy.internal.example.com/auth/entra/callback</code>.
        </li>
        <li>
          <strong>Certificates &amp; secrets → New client secret.</strong> Copy the value (not the secret ID)
          into <code>OIDC_ENTRA_CLIENT_SECRET</code>, and note when it expires.
        </li>
        <li>
          <strong>Token configuration → Add groups claim.</strong> Choose Security groups, and Group ID for
          the ID token. Groups then arrive as object IDs, so you bind <code>eddy:&lt;object id&gt;</code> in
          RBAC.
        </li>
        <li>
          Use the tenant-specific issuer. The multi-tenant <code>common</code> and <code>organizations</code>{" "}
          endpoints do not work: the issuer in their discovery document does not match, and sign-in shows
          &ldquo;cannot be reached&rdquo;.
          <CodeBlock
            code={`
config:
  auth:
    oidc:
      - id: entra
        preset: entra
        name: Microsoft
        issuer: https://login.microsoftonline.com/00000000-0000-0000-0000-000000000000/v2.0
        clientID: 11111111-2222-3333-4444-555555555555
`}
          />
        </li>
      </ol>
      <p>
        The user name is <code>preferred_username</code> (the UPN). Users in more than 200 groups hit Entra's
        overage: the claim is left out and Eddy sees no groups. Use groups assigned to the application, or app
        roles with <code>groupsClaim: roles</code>.
      </p>

      <h3 id="dex">Dex</h3>
      <p>
        Dex federates LDAP, SAML, GitHub, Google groups and more behind one OIDC issuer. Add Eddy as a static
        client in the Dex config, then the provider in the hub values:
      </p>
      <CodeBlock
        code={`
# Dex config
staticClients:
  - id: eddy
    name: Eddy
    secretEnv: EDDY_CLIENT_SECRET
    redirectURIs: ["https://eddy.internal.example.com/auth/dex/callback"]
`}
      />
      <CodeBlock
        code={`
# hub-values.yaml
config:
  auth:
    oidc:
      - id: dex
        preset: dex                    # scopes include groups
        issuer: https://dex.example.com
        clientID: eddy
`}
      />
      <p>
        Store Dex's secret for Eddy as <code>OIDC_DEX_CLIENT_SECRET</code>. Dex's connectors decide what{" "}
        <code>groups</code> holds. The GitHub connector emits <code>org:team</code>, which becomes{" "}
        <code>eddy:acme:platform</code>.
      </p>

      <h2 id="mapping">Group mapping</h2>
      <p>Every method goes through the same mapping. Its output is exactly what the agents impersonate.</p>
      <ol>
        <li>
          <strong>User:</strong> GitHub: the verified email or <code>github:&lt;login&gt;</code>. OIDC: the{" "}
          <code>usernameClaim</code>. Proxy: the user header. Local users: <code>local:&lt;username&gt;</code>
          . <code>system:</code> users and anything matching <code>auth.denyUserPrefixes</code> (default{" "}
          <code>system:</code>, <code>eks:</code>, <code>kubernetes-admin</code>) are refused.
        </li>
        <li>
          <strong>Groups:</strong> every group gets the prefix <code>eddy:</code> (
          <code>auth.groups.prefix</code>), and <code>system:*</code> groups are dropped. The prefix must be
          in the agent's <code>impersonation.allowedGroupPrefixes</code>.
        </li>
        <li>
          Every user also gets <code>eddy:authenticated</code>, plus any <code>groups.static</code> entries
          for their email, login or user name.
        </li>
      </ol>
      <p>
        Groups are captured at sign-in, so a team change takes effect at the next sign-in. Sessions end after
        8 hours idle or 24 hours in total. To apply a change at once, sign out and in, or an admin can end
        every session and token of a user with <code>eddy-hub admin revoke --user &lt;user&gt;</code>. Then
        bind the groups in RBAC: <Link to="/docs/access/">Access and RBAC</Link>.
      </p>

      <h2 id="local">Local users</h2>
      <p>
        Local users keep working next to providers. Their hashes are argon2id (bcrypt at cost 12 or more is
        also accepted).
      </p>
      <ol>
        <li>
          Generate a hash. The command reads the password from standard input:
          <CodeBlock
            code={`
$ read -rs PW && printf '%s' "$PW" | docker run --rm -i ghcr.io/idestis/eddy-hub:1.0.0 hash-password
`}
          />
        </li>
        <li>
          Put it in the values. A group <code>platform</code> becomes the Kubernetes group{" "}
          <code>eddy:platform</code>:
          <CodeBlock
            code={`
users:
  list:
    - username: admin
      passwordHash: "$argon2id$v=19$m=65536,t=3,p=4$...$..."
      groups: [platform]
`}
          />
          Or point <code>users.existingSecret</code> at a Secret with a <code>users.yaml</code> key.
        </li>
      </ol>
      <h3 id="breakglass">Break-glass mode</h3>
      <p>
        <code>mode: breakglass</code> keeps password sign-in for emergencies such as an identity provider
        outage:
      </p>
      <ul>
        <li>
          The form is hidden. A small <strong>Admin sign-in</strong> link leads to <code>/login?local=1</code>
          .
        </li>
        <li>
          Logins are rate-limited like normal mode. Every attempt and success is logged as a <code>WARN</code>
          , and <code>login</code> audit events carry <code>"mode":"breakglass"</code>, so you can alert on
          them.
        </li>
        <li>It needs another method (GitHub, OIDC or proxy), or the chart and hub refuse to start.</li>
        <li>
          <code>allowedUsers: [admin]</code> limits password sign-in to those names.
        </li>
      </ul>
      <CodeBlock
        code={`
config:
  auth:
    local: {enabled: true, mode: breakglass, allowedUsers: [admin]}
`}
      />
      <p>
        Set <code>enabled: false</code> to turn passwords off completely. A typical setup is one{" "}
        <code>admin</code> user in break-glass mode, with its password in a vault.
      </p>

      <h2 id="proxy">Trusted proxy (oauth2-proxy)</h2>
      <p>
        If you already run oauth2-proxy or Pomerium, Eddy can trust its headers. The chart can run
        oauth2-proxy as a sidecar: it listens on 4180 and forwards to the hub on <code>127.0.0.1:8080</code>.
        The hub trusts identity headers only from <code>127.0.0.1/32</code> and only with a matching shared
        secret. <code>/mcp</code> bypasses the proxy, because MCP clients send only a personal access token.
        Do not add authentication in front of it.
      </p>
      <ol>
        <li>
          Add the shared secret, at least 32 random bytes, to <code>eddy-credentials</code> as{" "}
          <code>EDDY_PROXY_SECRET</code> (the{" "}
          <Link to="/docs/install/" hash="add-key">
            add-a-key command
          </Link>{" "}
          with a generated value):
          <CodeBlock
            code={`
$ kubectl -n eddy patch secret eddy-credentials --type merge \\
    -p "{\\"stringData\\":{\\"EDDY_PROXY_SECRET\\":\\"$(openssl rand -base64 48)\\"}}"
`}
          />
        </li>
        <li>
          Create the proxy's own Secret:
          <CodeBlock
            code={`
$ kubectl -n eddy create secret generic eddy-oauth2-proxy \\
    --from-literal=OAUTH2_PROXY_COOKIE_SECRET="$(openssl rand -base64 32 | tr -- '+/' '-_')" \\
    --from-literal=OAUTH2_PROXY_CLIENT_ID=... \\
    --from-literal=OAUTH2_PROXY_CLIENT_SECRET=...
`}
          />
        </li>
        <li>
          Add the values (Google shown):
          <CodeBlock
            code={`
credentialsSecret: eddy-credentials   # holds EDDY_PROXY_SECRET; required with the sidecar
config:
  auth:
    local:
      enabled: false              # or keep it on as a break-glass login
    groups:
      static:
        alice@example.com: [platform]   # Google sends no groups, so map them here
oauth2Proxy:
  enabled: true
  existingSecret: eddy-oauth2-proxy
  extraArgs: ["--email-domain=example.com"]   # who may sign in
  alphaConfig:
    providers:
      - id: google
        provider: google
        clientID: \${OAUTH2_PROXY_CLIENT_ID}
        clientSecret: \${OAUTH2_PROXY_CLIENT_SECRET}
`}
          />
        </li>
      </ol>
      <p>
        The chart enables proxy auth, sets <code>trustedCIDRs</code> to <code>127.0.0.1/32</code> and makes
        the proxy send <code>X-Forwarded-Email</code>, <code>X-Forwarded-Groups</code> and{" "}
        <code>X-Eddy-Proxy-Secret</code>. The oauth2-proxy alpha config can change between releases, so check
        a new version with <code>oauth2-proxy --config-test</code>. Tokens for proxy users last at most 30
        days.
      </p>

      <h2 id="network">Private networks, VPNs and egress</h2>
      <p>
        GitHub and OIDC sign-in work for a hub that is only reachable on a private network.{" "}
        <strong>Both legs of the flow are browser redirects.</strong> The identity provider never connects to
        the hub, so a private callback such as <code>https://eddy.internal.example.com</code> is fine. The hub
        itself needs outbound HTTPS to the provider. The chart's default egress policy already allows TCP 443
        anywhere, plus DNS.
      </p>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Provider</th>
              <th scope="col">Hosts the hub calls</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">GitHub</th>
              <td>
                <code>github.com</code> (token), <code>api.github.com</code> (user, emails, memberships,
                teams)
              </td>
            </tr>
            <tr>
              <th scope="row">GitHub Enterprise Server</th>
              <td>
                Your server: <code>/login/oauth/access_token</code> and <code>/api/v3</code>
              </td>
            </tr>
            <tr>
              <th scope="row">Google</th>
              <td>
                <code>accounts.google.com</code>, <code>oauth2.googleapis.com</code>,{" "}
                <code>www.googleapis.com</code>
              </td>
            </tr>
            <tr>
              <th scope="row">Okta</th>
              <td>
                <code>&lt;org&gt;.okta.com</code>
              </td>
            </tr>
            <tr>
              <th scope="row">Entra ID</th>
              <td>
                <code>login.microsoftonline.com</code>
              </td>
            </tr>
            <tr>
              <th scope="row">Dex and others</th>
              <td>The issuer host and whatever its discovery document names</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        An outbound HTTP proxy is honoured through <code>HTTPS_PROXY</code> and <code>NO_PROXY</code> in{" "}
        <code>extraEnv</code>. Users' browsers also need to reach the provider's authorization page.
      </p>

      <h2 id="saml">SAML</h2>
      <p>
        SAML 2.0 is not in v1.0. It is a possible future addition and would feed the same mapping. Like OIDC,
        it would work on private networks. Until then use OIDC (most SAML identity providers also speak it),
        Dex's SAML connector, or a trusted proxy.
      </p>

      <h2 id="trouble">Troubleshooting</h2>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">You see</th>
              <th scope="col">Usual cause</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">The provider says the redirect URI does not match</th>
              <td>
                The callback registered there differs from{" "}
                <code>&lt;publicURL&gt;/auth/&lt;id&gt;/callback</code>: check scheme, host, port and trailing
                slashes
              </td>
            </tr>
            <tr>
              <th scope="row">&ldquo;Your sign-in expired or was started in another tab&rdquo;</th>
              <td>
                More than 10 minutes at the provider, another tab, blocked cookies, or <code>publicURL</code>{" "}
                differs from the browser address
              </td>
            </tr>
            <tr>
              <th scope="row">&ldquo;Your account is not allowed&rdquo;</th>
              <td>
                Not an active member of an allowed organization, the GitHub App is not installed in it, the
                OAuth App is not approved, the email is unverified, or the domain or group is not allowed. The{" "}
                <code>login</code> audit event has the reason
              </td>
            </tr>
            <tr>
              <th scope="row">&ldquo;The sign-in provider returned an error&rdquo;</th>
              <td>
                Wrong or expired client secret, clock skew on the hub, or the token endpoint is unreachable.
                The hub log has a <code>WARN</code> with the cause, never the token
              </td>
            </tr>
            <tr>
              <th scope="row">&ldquo;cannot be reached&rdquo;</th>
              <td>OIDC discovery failed: check egress, DNS and the issuer URL</td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pager current="/docs/sign-in/" />
    </>
  );
}
