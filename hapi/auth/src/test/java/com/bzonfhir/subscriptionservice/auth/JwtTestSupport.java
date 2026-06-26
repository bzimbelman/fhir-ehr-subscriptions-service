package com.bzonfhir.subscriptionservice.auth;

import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.interfaces.RSAPrivateKey;
import java.security.interfaces.RSAPublicKey;
import java.util.Date;
import java.util.UUID;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.RSASSASigner;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.KeyUse;
import com.nimbusds.jose.jwk.RSAKey;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;

/**
 * Helpers for generating short-lived RSA key pairs and signed JWTs in tests. Kept off the
 * runtime classpath because everything here is test-only.
 */
final class JwtTestSupport {

  private JwtTestSupport() {}

  static KeyPair generateRsaKeyPair() throws Exception {
    KeyPairGenerator g = KeyPairGenerator.getInstance("RSA");
    g.initialize(2048);
    return g.generateKeyPair();
  }

  static String keyId(KeyPair kp) {
    // Deterministic-ish but unique per key. Real IdPs publish a kid in the JWKS; we
    // just need a non-null value that matches across signing and the published JWKS.
    return "test-key-" + UUID.nameUUIDFromBytes(kp.getPublic().getEncoded());
  }

  static RSAKey toJwk(KeyPair kp) {
    return new RSAKey.Builder((RSAPublicKey) kp.getPublic())
        .privateKey((RSAPrivateKey) kp.getPrivate())
        .keyUse(KeyUse.SIGNATURE)
        .algorithm(JWSAlgorithm.RS256)
        .keyID(keyId(kp))
        .build();
  }

  /** Public-only JWKS suitable for serving from a mock JWKS endpoint. */
  static String publicJwksJson(KeyPair kp) {
    return new JWKSet(toJwk(kp).toPublicJWK()).toString();
  }

  static String signJwt(KeyPair kp, JWTClaimsSet claims) throws JOSEException {
    JWSHeader header =
        new JWSHeader.Builder(JWSAlgorithm.RS256).keyID(keyId(kp)).type(null).build();
    SignedJWT jwt = new SignedJWT(header, claims);
    jwt.sign(new RSASSASigner((RSAPrivateKey) kp.getPrivate()));
    return jwt.serialize();
  }

  static JWTClaimsSet.Builder defaultClaims(String issuer) {
    long now = System.currentTimeMillis();
    return new JWTClaimsSet.Builder()
        .issuer(issuer)
        .subject("test-subject")
        .audience("account")
        .claim("azp", "subscription-service-cli")
        .issueTime(new Date(now))
        .notBeforeTime(new Date(now - 5_000))
        .expirationTime(new Date(now + 60_000));
  }
}
