---
name: keycloak
description: >
  Run and use a development Keycloak as the OIDC identity provider. Use when
  working on authentication, authorization (roles, access rules over claims),
  anything under /api that needs a logged-in user, the web UI login flow, or
  when you need an access token for curl or tests. Covers starting Keycloak,
  the development realm, test users and groups, getting tokens, and changing
  the realm.
---

# Development Keycloak

Use Keycloak as a local OIDC provider when you need real logins: the browser
authorization-code flow, refresh tokens, or real group claims. Automated tests
should not depend on it (see Tests below).

## Conventions

Keep these in the repository. Create them the first time a project needs
Keycloak, and update this skill's tables once the paths exist.

| Item | Convention |
|---|---|
| Realm file | `deploy/dev/keycloak/<realm>-realm.json`, minimal and hand-written |
| Start script | `scripts/dev-keycloak.sh` (and a `make dev-keycloak` target) |
| Token script | `scripts/dev-token.sh <user>`, which prints an access token |
| Port | `8180` |
| Issuer | `http://localhost:8180/realms/<realm>` |

## Start it

```bash
make dev-keycloak        # foreground; or in the background:
nohup scripts/dev-keycloak.sh > /tmp/keycloak.log 2>&1 &
```

- In the devcontainer, run the Keycloak **Java distribution**. Java 25 is
  installed there, and the container cannot run Docker or Podman. Cache the
  download under `~/.cache/<project>`.
- On hosts with Docker or Podman, a compose file under `deploy/dev/` works too.
- It is ready in about 30 s. Check with:
  `curl -sf http://localhost:8180/realms/<realm>/.well-known/openid-configuration`
- Use an in-memory database so every start re-imports the realm. Changes made
  in the admin console are lost on restart.
- Admin console: http://localhost:8180/admin (admin / admin).

Always use `localhost`, not `127.0.0.1`. The issuer is pinned to the host
name, so tokens issued for another host name fail issuer validation.

## Realm contents

Suggested test users: password equals user name, one per role you need to
exercise.

| User | Password | Groups | Intended role |
|---|---|---|---|
| alice | alice | `<project>-admins` | administrator (bootstrap admin rule) |
| bob | bob | `<team>-ops` | can run and edit in one project |
| carol | carol | `<team>-viewers` | read-only |

Client `<project>-web`:

- public client, authorization code + PKCE (S256);
- redirect URIs for the local UI ports, for example `localhost:5173` for the
  Vite dev server and the app's own HTTP port;
- the direct password grant enabled **for development only**, for the token
  script and tests.

Configure mappers so that access tokens carry `aud: "<project>"`, `groups`
(plain group names), `email`, `name` and `sub`. Authorize on claims, usually
`groups`, through the application's own rules; do not use Keycloak roles.

## Get a token and call the API

```bash
TOKEN=$(scripts/dev-token.sh bob)
curl -H "Authorization: Bearer $TOKEN" localhost:<port>/api/v1/<endpoint>
```

Point the application at the issuer through its OIDC issuer setting. Make
alice an admin with the bootstrap admin rule, for example
`'<project>-admins' in claims.groups`. Then, as alice, grant the other users'
groups their roles through the API.

Decode a token's claims when debugging:

```bash
scripts/dev-token.sh alice | cut -d. -f2 | tr '_-' '/+' | base64 -d 2>/dev/null; echo
```

## Changing the realm

1. Edit the realm file (users, groups, client, mappers). Keep passwords equal
   to user names.
2. Restart Keycloak; it re-imports the realm.
3. Verify with `scripts/dev-token.sh <user>` and the decoded claims.
4. Update the tables above, and the OIDC configuration docs, if users, groups
   or claims change.

Do not export the realm from the admin console into the repository wholesale.
Exports contain generated IDs, secrets and defaults; keep the file minimal and
hand-written.

## Tests

Go tests never need a running Keycloak. Use an in-process fake issuer that
serves discovery and JWKS and signs tokens with chosen claims. Browser tests
use a fake OIDC provider that routes the discovery, authorize, token and logout
endpoints (see the frontend-debugging skill). Use real Keycloak only for manual
end-to-end checks and browser login flows.
