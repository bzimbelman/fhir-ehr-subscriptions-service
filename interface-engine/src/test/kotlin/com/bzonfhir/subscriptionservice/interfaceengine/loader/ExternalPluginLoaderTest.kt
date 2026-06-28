package com.bzonfhir.subscriptionservice.interfaceengine.loader

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.io.TempDir
import org.springframework.boot.autoconfigure.AutoConfiguration
import org.springframework.boot.context.annotation.ImportCandidates
import org.springframework.context.annotation.AnnotationConfigApplicationContext
import org.springframework.context.annotation.Bean
import java.io.File
import java.net.URLClassLoader
import java.nio.file.Files
import java.nio.file.Path
import java.util.jar.JarEntry
import java.util.jar.JarOutputStream
import java.util.jar.Manifest
import java.util.zip.ZipFile

/**
 * Smoke test for the external plugin loader (ticket #517, Epic #428).
 *
 * # What this test proves
 *
 * The FOSS interface-engine packages itself as a Spring Boot fat JAR. By
 * default, Spring Boot's `JarLauncher` only sees `BOOT-INF/lib/` inside
 * the fat JAR — anything dropped on disk at `/app/plugins/` (any .jar)
 * is invisible. The commercial image (ticket #515) bundles licensed
 * plugin JARs into `/app/plugins/`, but those plugins' `@AutoConfiguration`
 * classes never fire because the launcher can't see them.
 *
 * The fix is to switch the launcher to `PropertiesLauncher`, which reads
 * `LOADER_PATH` (or system property `loader.path`) and ADDs those entries
 * to the classloader BEFORE `Start-Class` is invoked. The commercial
 * layer sets `LOADER_PATH=/app/plugins` and everything inside lights up.
 *
 * This test validates the mechanism in two ways:
 *
 *   1. **Manifest assertion** — after `:interface-engine:bootJar`, the
 *      produced JAR's `META-INF/MANIFEST.MF` must list
 *      `Main-Class: org.springframework.boot.loader.launch.PropertiesLauncher`.
 *      This is the directly load-bearing change in `build.gradle.kts`.
 *
 *   2. **End-to-end auto-config discovery** — given a synthesised
 *      external plugin JAR containing one `@AutoConfiguration` + its
 *      entry in `META-INF/spring/.../AutoConfiguration.imports`, when
 *      Spring is started with a classloader that includes that JAR
 *      (which is exactly what `PropertiesLauncher` constructs from
 *      `LOADER_PATH`), then the external `@Bean` is registered in the
 *      `ApplicationContext`. When run WITHOUT the external JAR on the
 *      loader, the same bean is absent — proving the gap that the
 *      launcher switch closes.
 *
 * The second assertion does not actually invoke `PropertiesLauncher` in
 * the test JVM (the launcher classes live nested inside the bootJar and
 * are not on the unit-test classpath). Instead it reproduces the
 * downstream effect: a Spring boot context discovering an
 * auto-configuration via a `URLClassLoader` whose URLs include an
 * external plugin JAR. That is the exact runtime shape
 * `PropertiesLauncher` creates.
 */
class ExternalPluginLoaderTest {

    /**
     * After the launcher switch in `build.gradle.kts`, the bootJar's
     * MANIFEST.MF must declare `PropertiesLauncher` as `Main-Class`.
     *
     * Reads the manifest of the previously built jar at
     * `interface-engine/build/libs/`. The test is skipped (passes
     * trivially) if no jar has been built — which keeps the unit-test
     * loop fast for contributors who run `:test` without `:bootJar`. CI
     * runs both, so a regression cannot slip through there.
     */
    @Test
    fun `bootJar manifest declares PropertiesLauncher`() {
        val libsDir = locateBootJarLibsDir()
        if (libsDir == null || !libsDir.exists()) {
            // bootJar not built in this loop; skip silently. CI builds both.
            return
        }
        val bootJar = libsDir.listFiles { f -> f.name.endsWith(".jar") && !f.name.endsWith("-plain.jar") }
            ?.firstOrNull()
            ?: return

        ZipFile(bootJar).use { zip ->
            val manifestEntry = zip.getEntry("META-INF/MANIFEST.MF")
                ?: error("MANIFEST.MF not found in ${bootJar.name}")
            val manifest = zip.getInputStream(manifestEntry).use { Manifest(it) }
            val mainClass = manifest.mainAttributes.getValue("Main-Class")
            val startClass = manifest.mainAttributes.getValue("Start-Class")

            assertThat(mainClass)
                .`as`("Main-Class must point at PropertiesLauncher so external plugin jars are discoverable at runtime")
                .isEqualTo("org.springframework.boot.loader.launch.PropertiesLauncher")
            assertThat(startClass)
                .`as`("Start-Class must still point at the Kotlin Application entry point")
                .isEqualTo("com.bzonfhir.subscriptionservice.interfaceengine.ApplicationKt")
        }
    }

    /**
     * Reproduces the runtime classloader topology that `PropertiesLauncher`
     * builds from `LOADER_PATH=/app/plugins`: a `URLClassLoader` whose
     * URLs include an external plugin JAR. Spring's `ImportCandidates`
     * (the API used by `AutoConfigurationImportSelector` to discover
     * which `@AutoConfiguration` classes to register) must surface the
     * external class via that classloader. We then load + register the
     * config in a tiny `AnnotationConfigApplicationContext` and assert
     * the `@Bean` is present.
     *
     * We deliberately do NOT use `SpringApplicationBuilder` /
     * `@EnableAutoConfiguration` here: that machinery would drag in the
     * interface-engine's full auto-config chain (Camel, JPA, Flyway,
     * actuator health groups, …), which needs real backends to start.
     * The mechanism PropertiesLauncher enables — "extra jars on the
     * classloader become discoverable as auto-config sources" — is what
     * we're testing, and `ImportCandidates` is exactly the API Spring
     * uses to do that discovery.
     */
    @Test
    fun `auto-configuration in an external jar is discovered when present on the classloader`(@TempDir tmp: Path) {
        val pluginJar = buildPluginJarContainingProbeAutoConfig(tmp)
        val pluginLoader = URLClassLoader(arrayOf(pluginJar.toURI().toURL()), javaClass.classLoader)

        // Step 1: the imports file resolves via the new loader. This is
        // the exact lookup `AutoConfigurationImportSelector` performs.
        val candidates = ImportCandidates.load(AutoConfiguration::class.java, pluginLoader)
            .getCandidates()
        assertThat(candidates)
            .`as`("ImportCandidates must surface the external auto-config when the plugin jar is on the loader")
            .contains(ExternalProbeAutoConfiguration::class.java.name)

        // Step 2: load the config class through the URLClassLoader and
        // register it. The bean must end up in the context.
        val configClass = Class.forName(
            ExternalProbeAutoConfiguration::class.java.name,
            true,
            pluginLoader,
        )
        AnnotationConfigApplicationContext().use { ctx ->
            ctx.classLoader = pluginLoader
            ctx.register(configClass)
            ctx.refresh()

            assertThat(ctx.containsBean("externalProbe"))
                .`as`("external auto-config from the plugin jar must register its @Bean")
                .isTrue()
            assertThat(ctx.getBean("externalProbe", String::class.java)).isEqualTo("loaded")
        }
    }

    /**
     * Negative control: WITHOUT the external jar on the loader, the
     * `.imports` file is invisible. Proves the positive test isn't
     * passing by accident — the discovery mechanism really does require
     * the JAR to be on the classloader's URL list.
     *
     * The `.imports` file deliberately is NOT in src/test/resources;
     * the only copy lives inside the synthesised plugin jar.
     */
    @Test
    fun `external auto-config is invisible when the plugin jar is NOT on the loader`() {
        val candidates = ImportCandidates.load(AutoConfiguration::class.java, javaClass.classLoader)
            .getCandidates()
        assertThat(candidates)
            .`as`("without the plugin jar, the regular test classloader must NOT surface the external auto-config")
            .doesNotContain(ExternalProbeAutoConfiguration::class.java.name)
    }

    // ---------------------------------------------------------------------
    // Helpers
    // ---------------------------------------------------------------------

    /**
     * Synthesises a JAR in `dir` containing:
     *   - the already-compiled `ExternalProbeAutoConfiguration` class
     *     (lifted from the test classpath)
     *   - a `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports`
     *     entry naming that class
     *
     * The class file already lives on the test classpath (because this
     * test source set compiles it), but Spring's auto-config discovery
     * keys off the `.imports` file — which is ONLY in the synthesised
     * jar. So the bean is invisible to the regular test classloader and
     * only becomes visible when the synthesised jar is added on top.
     */
    private fun buildPluginJarContainingProbeAutoConfig(dir: Path): File {
        val jarFile = dir.resolve("extra-plugin.jar").toFile()
        val probeClass = ExternalProbeAutoConfiguration::class.java
        val classResource = probeClass.name.replace('.', '/') + ".class"
        val classBytes = probeClass.classLoader.getResourceAsStream(classResource)
            ?.use { it.readBytes() }
            ?: error("could not locate $classResource on the test classpath")

        JarOutputStream(Files.newOutputStream(jarFile.toPath())).use { jar ->
            // META-INF/MANIFEST.MF (minimal, real-world plugin jars have one).
            jar.putNextEntry(JarEntry("META-INF/MANIFEST.MF"))
            jar.write("Manifest-Version: 1.0\n\n".toByteArray())
            jar.closeEntry()

            // The class file itself.
            jar.putNextEntry(JarEntry(classResource))
            jar.write(classBytes)
            jar.closeEntry()

            // The imports file — the actual hook Spring Boot reads.
            jar.putNextEntry(
                JarEntry("META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports")
            )
            jar.write("${probeClass.name}\n".toByteArray())
            jar.closeEntry()
        }
        return jarFile
    }

    /**
     * Find `interface-engine/build/libs/`. We're running with cwd =
     * `interface-engine/` under Gradle, or repo root for IDE runs; cover both.
     */
    private fun locateBootJarLibsDir(): File? {
        val cwd = File(".").absoluteFile.canonicalFile
        val candidates = listOf(
            File(cwd, "build/libs"),
            File(cwd, "interface-engine/build/libs"),
            File(cwd.parentFile, "interface-engine/build/libs"),
        )
        return candidates.firstOrNull { it.isDirectory }
    }

}

/**
 * Test fixture mimicking a real commercial plugin's auto-config. Kept
 * package-public + `open` so a fresh `URLClassLoader` can also load it
 * by name when the plugin jar is on the path. `@AutoConfiguration` is
 * a `@Configuration` meta-annotation that Spring Boot's auto-config
 * machinery looks for when it scans the `AutoConfiguration.imports`
 * file in `META-INF/spring/`.
 */
@AutoConfiguration
open class ExternalProbeAutoConfiguration {
    @Bean
    open fun externalProbe(): String = "loaded"
}
