# Single sign-on with OIDC

Dowitcher can authenticate users through any OpenID Connect provider, alongside the existing
passkeys. Point it at your provider's issuer URL and a **Sign in with …** button appears on the
login page. Nothing here is specific to one provider — Pocket ID, Keycloak, Authentik, Authelia,
Google, Entra ID and the rest all work if they implement OIDC discovery.

## How it behaves

- **Accounts are created on first login.** A provider assertion that names an unknown
  `(issuer, subject)` pair gets a fresh dowitcher account. The provider is the authority for who
  exists, so you do not have to invite people first.
- **Identities are keyed on `(issuer, subject)`**, never on an email or username. A user renaming
  themselves or changing their email at the provider keeps the same account, and two providers
  that happen to use the same subject do not collide.
- **The first account is the admin**, whichever way it signed in — the same first-run rule the
  passkey bootstrap uses.
- **An admin claim can promote, but never demote.** If the provider sends a configured group
  claim, membership grants admin on every login. Losing the claim does *not* strip admin: a claim
  can go missing for mundane reasons (a scope was trimmed, a group query failed), and silently
  revoking rights on that basis is worse than leaving them. Demote explicitly from the settings
  page.
- **OIDC accounts are separate from passkey accounts.** There is no automatic linking, because
  nothing dowitcher stores is a safe join key. Someone who wants both can add a passkey to their
  OIDC account from the settings page.

## Configuration

Set these on the dowitcher container. `DOWITCHER_OIDC_ISSUER` is what turns the feature on; a
misspelled or unreachable issuer stops the process at startup, where the error is easy to see.

| Variable | Default | Meaning |
| --- | --- | --- |
| `DOWITCHER_OIDC_ISSUER` | unset | Provider issuer URL, e.g. `https://id.example.com`. Enables OIDC when set |
| `DOWITCHER_OIDC_CLIENT_ID` | — | Client id from the provider |
| `DOWITCHER_OIDC_CLIENT_SECRET` | empty | Client secret. Leave empty for a public client using PKCE |
| `DOWITCHER_OIDC_NAME` | `SSO` | Label on the login button: "Sign in with `<name>`" |
| `DOWITCHER_OIDC_SCOPES` | `openid profile email` | Comma-separated scopes. `openid` is always added |
| `DOWITCHER_OIDC_NAME_CLAIM` | `preferred_username` | Claim used for the account name, falling back to `name`, `email`, then `subject` |
| `DOWITCHER_OIDC_GROUPS_CLAIM` | `groups` | Claim carrying group membership |
| `DOWITCHER_OIDC_ADMIN_GROUPS` | empty | Comma-separated group names that grant admin. Empty disables claim-based admin |

The callback URL to register at the provider is always:

```
<DOWITCHER_ORIGIN>/auth/oidc/callback
```

So if `DOWITCHER_ORIGIN=https://comics.example.com`, register
`https://comics.example.com/auth/oidc/callback`. The issuer must use HTTPS for anything but a
local development instance — a provider over plain HTTP is rejected by a browser that is
expecting a secure context.

### Any provider

1. Create a new OIDC / OAuth client (confidential, authorization-code flow) at your provider.
2. Register `https://<your-dowitcher-domain>/auth/oidc/callback` as an allowed redirect/callback
   URL.
3. Copy the client id and secret.
4. Start dowitcher with `DOWITCHER_OIDC_ISSUER`, `DOWITCHER_OIDC_CLIENT_ID` and, if the client is
   confidential, `DOWITCHER_OIDC_CLIENT_SECRET` set.

To grant admin from a group, add the provider's groups scope to `DOWITCHER_OIDC_SCOPES`, name the
group in `DOWITCHER_OIDC_ADMIN_GROUPS`, and make sure the login's account is in it (or promote
the account from the settings page).

## Pocket ID walkthrough

[Pocket ID](https://pocket-id.org) is a self-hosted OIDC provider that signs users in with
passkeys. It is a natural fit: dowitcher uses passkeys for its own login, so the whole stack can
be passwordless.

### 1. Run Pocket ID

Pocket ID must be served over HTTPS. The compose file below assumes both apps sit behind a
reverse proxy (Caddy, nginx, Traefik, …) that terminates TLS; only Pocket ID's port is exposed to
the proxy network.

```yaml
services:
  pocket-id:
    image: ghcr.io/pocket-id/pocket-id:v2
    restart: unless-stopped
    environment:
      # Public URL Pocket ID is reached at. Its issuer is this URL.
      APP_URL: https://id.example.com
      # openssl rand -base64 32
      ENCRYPTION_KEY: "REPLACE_WITH_A_32_CHARACTER_KEY"
    volumes:
      - pocket-id-data:/app/data
    # expose through your reverse proxy; not published to the host

volumes:
  pocket-id-data:
```

Complete setup at `https://id.example.com/setup` and create the first Pocket ID admin.

### 2. Create the dowitcher client

In Pocket ID, open **Settings → OIDC Clients → Create Client** and fill in:

- **Name**: `Dowitcher`
- **Callback URLs**: `https://comics.example.com/auth/oidc/callback`
- **Allowed User Groups**: `Unrestrict` to allow everyone, or pick the groups that may sign in.

Save, then copy the generated **Client ID** and **Client Secret** from the client's page. Pocket ID
issues the client id as a UUID.

### 3. Point dowitcher at Pocket ID

```yaml
services:
  dowitcher:
    image: ghcr.io/seriousbug/dowitcher:latest
    restart: unless-stopped
    environment:
      DOWITCHER_ORIGIN: https://comics.example.com
      DOWITCHER_RP_ID: comics.example.com
      DOWITCHER_OIDC_ISSUER: https://id.example.com
      DOWITCHER_OIDC_CLIENT_ID: "the-uuid-from-pocket-id"
      DOWITCHER_OIDC_CLIENT_SECRET: "the-secret-from-pocket-id"
      DOWITCHER_OIDC_NAME: Pocket ID
    volumes:
      - dowitcher-data:/data
      - /path/to/your/comics:/library
    # expose through the same reverse proxy

volumes:
  dowitcher-data:
```

Start it and check the logs. A healthy start prints:

```
dowitcher OIDC login enabled via https://id.example.com — set the client's callback URL to https://comics.example.com/auth/oidc/callback
```

### 4. Sign in

Open `https://comics.example.com`. The login page now shows **Sign in with Pocket ID** under the
passkey button. The first person to sign in becomes the admin.

If you would rather keep the first account on a passkey, enroll the bootstrap admin from the
first-run link in the logs *before* anyone uses the OIDC button; provisioning then treats that
account as the first one.

To make a Pocket ID group an admin group, include the groups scope and name the group:

```yaml
      DOWITCHER_OIDC_SCOPES: openid profile email groups
      DOWITCHER_OIDC_ADMIN_GROUPS: dowitcher-admins
```

## Troubleshooting

- **Startup fails with `discover issuer`.** The issuer URL is wrong or Pocket ID is not reachable
  from the dowitcher container. Check `DOWITCHER_OIDC_ISSUER` and that both containers share a
  network.
- **The provider returns "invalid redirect_uri".** The registered callback URL does not exactly
  match `<DOWITCHER_ORIGIN>/auth/oidc/callback`, including scheme and any trailing slash.
- **"Could not finish signing in with the provider".** The code exchange or ID token verification
  failed. The dowitcher log line `oidc: exchange code: …` has the provider's reason.
- **The button does not appear.** `DOWITCHER_OIDC_ISSUER` is not set (or the process did not
  restart). `GET /auth/providers` returns `{"oidc":{"name":"…"}}` when it is.
- **Two accounts for one person.** They signed in once with a passkey and once with OIDC; these
  are separate by design. Delete one from the settings page, or add the passkey to the OIDC
  account.
