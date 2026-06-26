package com.bzonfhir.subscriptionservice.auth;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.exceptions.AuthenticationException;
import com.nimbusds.jwt.JWTClaimsSet;

/**
 * Behavioral tests for the authentication interceptor. JWT validation itself is exercised
 * in {@link JwtValidatorTest}; here we mock the validator so each test focuses on the
 * interceptor's request-handling logic (header parsing, anonymous allow-list, userData
 * stash).
 */
class OidcJwtAuthenticationInterceptorTest {

  private AuthProperties props;
  private JwtValidator validator;
  private OidcJwtAuthenticationInterceptor interceptor;

  @BeforeEach
  void setUp() {
    props = new AuthProperties();
    props.setEnabled(true);
    props.setIssuer("https://issuer.example.com/realms/test");
    validator = mock(JwtValidator.class);
    interceptor = new OidcJwtAuthenticationInterceptor(props, validator);
  }

  private RequestDetails mockRequest(String path, String authHeader) {
    RequestDetails rd = mock(RequestDetails.class);
    lenient().when(rd.getRequestPath()).thenReturn(path);
    lenient().when(rd.getHeader("Authorization")).thenReturn(authHeader);
    Map<Object, Object> userData = new HashMap<>();
    lenient().when(rd.getUserData()).thenReturn(userData);
    return rd;
  }

  @Test
  void allowsMetadataAnonymously() {
    RequestDetails rd = mockRequest("metadata", null);
    assertThat(interceptor.authenticate(rd)).isTrue();
  }

  @Test
  void allowsSmartConfigurationAnonymously() {
    RequestDetails rd = mockRequest(".well-known/smart-configuration", null);
    assertThat(interceptor.authenticate(rd)).isTrue();
  }

  @Test
  void rejectsMissingAuthorizationHeader() {
    RequestDetails rd = mockRequest("Patient", null);
    assertThatThrownBy(() -> interceptor.authenticate(rd))
        .isInstanceOf(AuthenticationException.class)
        .hasMessageContaining("Authorization header required");
  }

  @Test
  void rejectsBlankAuthorizationHeader() {
    RequestDetails rd = mockRequest("Patient", "   ");
    assertThatThrownBy(() -> interceptor.authenticate(rd))
        .isInstanceOf(AuthenticationException.class)
        .hasMessageContaining("Authorization");
  }

  @Test
  void rejectsNonBearerScheme() {
    RequestDetails rd = mockRequest("Patient", "Basic dXNlcjpwYXNz");
    assertThatThrownBy(() -> interceptor.authenticate(rd))
        .isInstanceOf(AuthenticationException.class)
        .hasMessageContaining("Bearer");
  }

  @Test
  void rejectsInvalidToken() throws Exception {
    when(validator.validate("badtoken"))
        .thenThrow(new JwtValidator.InvalidTokenException("Token rejected: garbage"));
    RequestDetails rd = mockRequest("Patient", "Bearer badtoken");
    assertThatThrownBy(() -> interceptor.authenticate(rd))
        .isInstanceOf(AuthenticationException.class)
        .hasMessageContaining("garbage");
  }

  @Test
  void stashesClaimsAndScopesOnValidToken() throws Exception {
    JWTClaimsSet claims =
        new JWTClaimsSet.Builder()
            .issuer(props.getIssuer())
            .subject("svc-acc")
            .claim("azp", "subscription-service-cli")
            .claim("scope", "system/Patient.r system/Subscription.crus")
            .build();
    when(validator.validate("goodtoken")).thenReturn(claims);

    RequestDetails rd = mockRequest("Patient", "Bearer goodtoken");
    assertThat(interceptor.authenticate(rd)).isTrue();

    Map<Object, Object> userData = rd.getUserData();
    assertThat(userData)
        .containsKey(OidcJwtAuthenticationInterceptor.USER_DATA_CLAIMS_KEY);
    @SuppressWarnings("unchecked")
    Set<SmartScope> scopes =
        (Set<SmartScope>)
            userData.get(OidcJwtAuthenticationInterceptor.USER_DATA_SCOPES_KEY);
    assertThat(scopes).hasSize(2);
    assertThat(scopes)
        .extracting(SmartScope::getResourceType)
        .containsExactlyInAnyOrder("Patient", "Subscription");
  }

  @Test
  void skipsEverythingWhenDisabled() throws Exception {
    props.setEnabled(false);
    RequestDetails rd = mockRequest("Patient", null);
    assertThat(interceptor.authenticate(rd)).isTrue();
    // Validator never consulted.
    org.mockito.Mockito.verifyNoInteractions(validator);
  }

  @Test
  void caseInsensitiveBearerScheme() throws Exception {
    JWTClaimsSet claims = new JWTClaimsSet.Builder().issuer(props.getIssuer()).build();
    when(validator.validate("t")).thenReturn(claims);
    RequestDetails rd = mockRequest("Patient", "bearer t");
    assertThat(interceptor.authenticate(rd)).isTrue();
  }

  @Test
  void extractScopeClaimHandlesStringArrayValue() {
    JWTClaimsSet claims =
        new JWTClaimsSet.Builder()
            .claim("scope", List.of("system/Patient.r", "system/Patient.cruds"))
            .build();
    assertThat(OidcJwtAuthenticationInterceptor.extractScopeClaim(claims))
        .contains("system/Patient.r", "system/Patient.cruds");
  }

  @Test
  void extractScopeClaimHandlesMissingClaim() {
    JWTClaimsSet claims = new JWTClaimsSet.Builder().build();
    assertThat(OidcJwtAuthenticationInterceptor.extractScopeClaim(claims)).isEmpty();
  }
}
