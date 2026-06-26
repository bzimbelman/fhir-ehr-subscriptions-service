package com.bzonfhir.subscriptionservice.interfaceengine

import org.springframework.boot.autoconfigure.SpringBootApplication
import org.springframework.boot.runApplication

/**
 * Main entry point.
 *
 * @SpringBootApplication's default component scan + Spring Boot's JPA
 * autoconfiguration (JpaRepositoriesAutoConfiguration / HibernateJpaAutoConfiguration)
 * already cover everything under com.bzonfhir.subscriptionservice.interfaceengine.**,
 * including the .persistence package added in Epic #378 ticket #380.
 *
 * We deliberately do NOT add explicit @EnableJpaRepositories /
 * @EntityScan annotations here: doing so would register an unconditional
 * dependency on the `entityManagerFactory` bean, which breaks the route
 * tests in this module (IngestRoutesTest / TransformRouteTest) — those
 * tests exclude HibernateJpaAutoConfiguration so they can run without a
 * Postgres available. The autoconfig path stays opt-in (active when the
 * datasource autoconfig is present, dormant when it's excluded), which
 * is exactly what we want.
 */
@SpringBootApplication
class Application

fun main(args: Array<String>) {
    runApplication<Application>(*args)
}
