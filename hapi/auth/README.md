# hapi-auth

Spring Boot auto-configuration JAR that adds Keycloak JWT bearer-token
authentication, SMART-scope authorization, and US Core profile
validation to the upstream `hapiproject/hapi:v7.6.0` image.
Tickets #359 (auth) + #367 (validation).

## What it does

HAPI interceptors, wired into the existing HAPI starter Spring context
via `META-INF/spring/...AutoConfiguration.imports`:

- `KeycloakJwtAuthenticationInterceptor` — validates `Authorization:
  Bearer <jwt>` against the configured Keycloak realm's JWKS. Anonymous
  on `/metadata` and `/.well-known/smart-configuration`; 401 on every
  other path without a valid token.
- `ScopeAuthorizationInterceptor` — extends HAPI's
  `AuthorizationInterceptor` and maps SMART `system/<Resource>.<flags>`
  scopes to HAPI auth rules. 403 on any operation not explicitly granted.
- `RequestValidatingInterceptor` (US Core profile validation, #367) —
  gated by `SUBSCRIPTION_SERVICE_VALIDATION_MODE`. When `mode=warn`
  validation findings are folded into the response OperationOutcome but
  the request succeeds (visible to the client when the request includes
  `Prefer: return=OperationOutcome`); when `mode=enforce` non-conforming
  requests are rejected with HTTP 422. When `mode=off` (the default) the
  interceptor is never registered.

See `docs/auth.md` ("How the FHIR API enforces tokens") for the auth
contract; see `docs/architecture.md` "Profile validation (US Core)" for
the validation contract.

## Build

```bash
cd hapi/auth
mvn package
# -> target/subscription-service-hapi-auth-0.1.0.jar (~20 KB)
```

JVM target is **Java 17** to match the upstream HAPI image's JRE. The
JAR ships with `provided`-scope deps only, so it carries no transitive
runtime libraries — the upstream image already bundles Nimbus JOSE+JWT
9.37.3, Spring Boot 3.2.6, and HAPI FHIR 7.6.0.

## How it gets onto the HAPI classpath

The upstream `hapiproject/hapi` image uses Spring Boot's
`PropertiesLauncher` with
`-Dloader.path=main.war!/WEB-INF/classes/,main.war!/WEB-INF/,/app/extra-classes`.
Anything in `/app/extra-classes/` is added to the runtime classpath, so
the derived image at `hapi/Dockerfile` simply copies the JAR there. The
`@SpringBootApplication`-driven autoconfig discovery in the HAPI starter
then finds the `AutoConfiguration.imports` entry and wires the
interceptors automatically.

## Tests

```bash
mvn test
```

JUnit 5 + AssertJ + Mockito + Wiremock. ~40 tests covering:

- SMART scope parser (all CRUDS combinations, malformed input, claim shapes)
- JWT validation (valid token, expired, wrong issuer, unknown signing key,
  malformed, missing, nbf-in-future)
- Authentication interceptor (anonymous allow-list, header parsing, claims
  stashing, disabled mode)
- Scope-to-rule mapping (every documented scope, multi-scope union, empty
  scope deny-all, disabled mode allow-all)
